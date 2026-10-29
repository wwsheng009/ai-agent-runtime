package workbuddy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

func openDB(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(absolute) + "?mode=ro"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("打开 WorkBuddy SQLite 失败: %w", err)
	}
	return db, nil
}

func dbSchema(db *sql.DB, ctx context.Context) (string, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sessions'").Scan(&count); err != nil {
		return "", err
	}
	if count == 0 {
		return "", fmt.Errorf("WorkBuddy 数据库缺少 sessions 表")
	}
	var latest string
	_ = db.QueryRowContext(ctx, "SELECT name FROM __workbuddy_drizzle_migrations ORDER BY id DESC LIMIT 1").Scan(&latest)
	return latest, nil
}

func queryDBSessions(ctx context.Context, db *sql.DB) ([]dbSession, error) {
	rows, err := db.QueryContext(ctx, "SELECT * FROM sessions ORDER BY updated_at DESC")
	if err != nil {
		return nil, fmt.Errorf("读取 WorkBuddy sessions 失败: %w", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	index := make(map[string]int, len(columns))
	for i, name := range columns {
		index[strings.ToLower(name)] = i
	}
	if _, ok := index["id"]; !ok {
		return nil, fmt.Errorf("WorkBuddy sessions 表缺少 id 列")
	}
	if _, ok := index["cwd"]; !ok {
		return nil, fmt.Errorf("WorkBuddy sessions 表缺少 cwd 列")
	}
	sessions := make([]dbSession, 0, 32)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		item := dbSession{
			ID:              stringAt(values, index, "id"),
			CWD:             stringAt(values, index, "cwd"),
			UserID:          stringAt(values, index, "user_id"),
			Title:           stringAt(values, index, "title"),
			CustomTitle:     stringAt(values, index, "custom_title"),
			Status:          stringAt(values, index, "status"),
			Model:           stringAt(values, index, "model"),
			Transport:       stringAt(values, index, "transport"),
			CreatedAt:       intAt(values, index, "created_at"),
			UpdatedAt:       intAt(values, index, "updated_at"),
			LastActivityAt:  intAt(values, index, "last_activity_at"),
			DeletedAt:       nullableIntAt(values, index, "deleted_at"),
			IsPlayground:    boolAt(values, index, "is_playground"),
			PermissionMode:  stringAt(values, index, "permission_mode"),
			ConversationSrc: stringAt(values, index, "conversation_origin"),
		}
		if item.ID != "" {
			sessions = append(sessions, item)
		}
	}
	return sessions, rows.Err()
}

func stringAt(values []any, index map[string]int, key string) string {
	i, ok := index[key]
	if !ok || i < 0 || i >= len(values) {
		return ""
	}
	switch value := values[i].(type) {
	case string:
		return value
	case []byte:
		return string(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func intAt(values []any, index map[string]int, key string) int64 {
	i, ok := index[key]
	if !ok || i < 0 || i >= len(values) {
		return 0
	}
	switch value := values[i].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case []byte:
		n, _ := strconv.ParseInt(string(value), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(value, 10, 64)
		return n
	default:
		return 0
	}
}

func nullableIntAt(values []any, index map[string]int, key string) *int64 {
	if _, ok := index[key]; !ok || values[index[key]] == nil {
		return nil
	}
	value := intAt(values, index, key)
	return &value
}

func boolAt(values []any, index map[string]int, key string) bool {
	i, ok := index[key]
	if !ok || i < 0 || i >= len(values) {
		return false
	}
	switch value := values[i].(type) {
	case int64:
		return value != 0
	case bool:
		return value
	case []byte:
		return string(value) == "1" || strings.EqualFold(string(value), "true")
	default:
		return false
	}
}

// runtimeBusy reports whether a WorkBuddy runtime heartbeat points at the
// selected session. It is advisory only; import is read-only and never blocks.
func runtimeBusy(home, sessionID string) (bool, string) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(sessionID) == "" {
		return false, ""
	}
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil {
		return false, ""
	}
	now := time.Now().UnixMilli()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(home, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		var value struct {
			SessionID     string `json:"sessionId"`
			LastHeartbeat int64  `json:"lastHeartbeat"`
			PID           int    `json:"pid"`
			Version       string `json:"version"`
		}
		if json.Unmarshal(raw, &value) != nil || value.SessionID != sessionID {
			continue
		}
		age := now - value.LastHeartbeat
		if value.LastHeartbeat > 0 && age >= 0 && age < 2*60*1000 {
			reason := fmt.Sprintf("WorkBuddy 运行实例心跳最近更新（pid=%d", value.PID)
			if value.Version != "" {
				reason += ", version=" + value.Version
			}
			return true, reason + "）"
		}
	}
	return false, ""
}

func appVersion(home string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(home, "last-launch.json"))
	if err == nil {
		var value struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.Version) != "" {
			return strings.TrimSpace(value.Version)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil {
		return ""
	}
	version := ""
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(home, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		var value struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.Version) != "" {
			version = strings.TrimSpace(value.Version)
		}
	}
	return version
}

func sortDBSessions(sessions []dbSession) {
	sort.SliceStable(sessions, func(i, j int) bool {
		left := sessions[i].LastActivityAt
		if left == 0 {
			left = sessions[i].UpdatedAt
		}
		right := sessions[j].LastActivityAt
		if right == 0 {
			right = sessions[j].UpdatedAt
		}
		return left > right
	})
}
