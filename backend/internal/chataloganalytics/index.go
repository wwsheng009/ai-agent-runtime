package chataloganalytics

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

const (
	analyticsIndexRefreshInterval = 3 * time.Second
	analyticsIndexParserVersion   = "4"
)

var analyticsIndexState struct {
	sync.Mutex
	root        string
	refreshedAt time.Time
}

func analyticsIndexPath(root string) (string, bool) {
	defaultRoot, defaultErr := filepath.Abs(aiclipaths.DefaultChatLogsDir())
	resolvedRoot, rootErr := filepath.Abs(strings.TrimSpace(root))
	if defaultErr != nil || rootErr != nil || !strings.EqualFold(filepath.Clean(defaultRoot), filepath.Clean(resolvedRoot)) {
		return "", false
	}
	return filepath.Join(aiclipaths.DefaultSessionsDir(), "runtime", "usage_analytics.sqlite"), true
}

func openAnalyticsIndex(root string) (*sql.DB, bool, error) {
	path, enabled := analyticsIndexPath(root)
	if !enabled {
		return nil, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, false, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		`CREATE TABLE IF NOT EXISTS analytics_sessions (
            session_id TEXT PRIMARY KEY,
            rel_path TEXT NOT NULL,
            source_fingerprint TEXT NOT NULL,
            rollup_json BLOB NOT NULL,
            updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS analytics_turns (
            session_id TEXT NOT NULL,
            trace_id TEXT NOT NULL,
            ordinal INTEGER NOT NULL,
            fact_json BLOB NOT NULL,
            PRIMARY KEY (session_id, trace_id)
        )`,
		`CREATE TABLE IF NOT EXISTS analytics_llm_requests (
            session_id TEXT NOT NULL,
            trace_id TEXT NOT NULL,
            step INTEGER NOT NULL,
            fact_json BLOB NOT NULL,
            PRIMARY KEY (session_id, trace_id, step)
        )`,
		"CREATE INDEX IF NOT EXISTS idx_analytics_sessions_updated ON analytics_sessions(updated_at DESC)",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, false, err
		}
	}
	return db, true, nil
}

func sessionSourceFingerprint(dir SessionDir) string {
	parts := []string{"parser:" + analyticsIndexParserVersion}
	if chatPath, err := findChatLogFile(dir.Path); err == nil && chatPath != "" {
		if info, statErr := os.Stat(chatPath); statErr == nil {
			parts = append(parts, fmt.Sprintf("chat:%d:%d", info.ModTime().UnixNano(), info.Size()))
		}
	}
	if info, err := os.Stat(sessionDebugLogPath(dir)); err == nil {
		parts = append(parts, fmt.Sprintf("debug:%d:%d", info.ModTime().UnixNano(), info.Size()))
	}
	return strings.Join(parts, "|")
}

func indexedRollups(root string, maxScan int) ([]SessionRollup, bool, error) {
	db, enabled, err := openAnalyticsIndex(root)
	if err != nil || !enabled {
		return nil, enabled, err
	}
	defer db.Close()

	analyticsIndexState.Lock()
	defer analyticsIndexState.Unlock()
	now := time.Now()
	if analyticsIndexState.root != root || now.Sub(analyticsIndexState.refreshedAt) >= analyticsIndexRefreshInterval {
		if err := refreshAnalyticsIndex(db, root, maxScan); err != nil {
			return nil, true, err
		}
		analyticsIndexState.root = root
		analyticsIndexState.refreshedAt = now
	}
	rollups, err := readIndexedRollups(db)
	if err == nil {
		enrichRollupTitlesFromSessionHistory(root, rollups)
	}
	return rollups, true, err
}

func refreshAnalyticsIndex(db *sql.DB, root string, maxScan int) error {
	dirs, err := DiscoverSessionDirs(root, maxScan)
	if err != nil {
		return err
	}
	existing := make(map[string]string)
	rows, err := db.Query("SELECT session_id, source_fingerprint FROM analytics_sessions")
	if err != nil {
		return err
	}
	for rows.Next() {
		var sessionID, fingerprint string
		if rows.Scan(&sessionID, &fingerprint) == nil {
			existing[sessionID] = fingerprint
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, dir := range dirs {
		fingerprint := sessionSourceFingerprint(dir)
		if fingerprint != "" && existing[dir.SessionID] == fingerprint {
			continue
		}
		rollup, steps, evidence, loadErr := loadSessionRollup(dir, true)
		if loadErr != nil {
			continue
		}
		if err := upsertAnalyticsFacts(db, dir, fingerprint, rollup, steps, buildTurns(steps, evidence)); err != nil {
			return err
		}
	}
	return nil
}

func upsertAnalyticsFacts(db *sql.DB, dir SessionDir, fingerprint string, rollup SessionRollup, steps []StepUsage, turns []TurnUsage) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rollupJSON, err := json.Marshal(rollup)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO analytics_sessions(session_id, rel_path, source_fingerprint, rollup_json, updated_at)
        VALUES(?, ?, ?, ?, ?)
        ON CONFLICT(session_id) DO UPDATE SET rel_path=excluded.rel_path, source_fingerprint=excluded.source_fingerprint,
        rollup_json=excluded.rollup_json, updated_at=excluded.updated_at`,
		rollup.SessionID, dir.RelPath, fingerprint, rollupJSON, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM analytics_turns WHERE session_id = ?", rollup.SessionID); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM analytics_llm_requests WHERE session_id = ?", rollup.SessionID); err != nil {
		return err
	}
	for index, turn := range turns {
		factJSON, marshalErr := json.Marshal(turn)
		if marshalErr != nil {
			return marshalErr
		}
		traceID := firstNonEmpty(turn.TraceID, fmt.Sprintf("unknown-%d", index+1))
		if _, err = tx.Exec("INSERT INTO analytics_turns(session_id, trace_id, ordinal, fact_json) VALUES(?, ?, ?, ?)", rollup.SessionID, traceID, turn.Ordinal, factJSON); err != nil {
			return err
		}
	}
	for index, step := range steps {
		factJSON, marshalErr := json.Marshal(step)
		if marshalErr != nil {
			return marshalErr
		}
		traceID := firstNonEmpty(step.TraceID, fmt.Sprintf("unknown-%d", index+1))
		if _, err = tx.Exec("INSERT INTO analytics_llm_requests(session_id, trace_id, step, fact_json) VALUES(?, ?, ?, ?)", rollup.SessionID, traceID, step.Step, factJSON); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func firstNonEmpty(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func readIndexedRollups(db *sql.DB) ([]SessionRollup, error) {
	rows, err := db.Query("SELECT rel_path, rollup_json FROM analytics_sessions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rollups := make([]SessionRollup, 0, 256)
	for rows.Next() {
		var relPath string
		var raw []byte
		if err := rows.Scan(&relPath, &raw); err != nil {
			return nil, err
		}
		var rollup SessionRollup
		if json.Unmarshal(raw, &rollup) != nil {
			continue
		}
		rollup.RelPath = relPath
		rollups = append(rollups, rollup)
	}
	return rollups, rows.Err()
}

func indexedSessionUsage(root, sessionID string) (SessionUsageDetail, bool, error) {
	db, enabled, err := openAnalyticsIndex(root)
	if err != nil || !enabled {
		return SessionUsageDetail{}, enabled, err
	}
	defer db.Close()
	if _, _, err := indexedRollups(root, defaultMaxScan); err != nil {
		return SessionUsageDetail{}, true, err
	}

	var relPath string
	var raw []byte
	err = db.QueryRow("SELECT rel_path, rollup_json FROM analytics_sessions WHERE session_id = ?", sessionID).Scan(&relPath, &raw)
	if err == sql.ErrNoRows {
		return SessionUsageDetail{}, true, errSessionNotFound(sessionID)
	}
	if err != nil {
		return SessionUsageDetail{}, true, err
	}
	var rollup SessionRollup
	if err := json.Unmarshal(raw, &rollup); err != nil {
		return SessionUsageDetail{}, true, err
	}
	rollup.RelPath = relPath
	singleRollup := []SessionRollup{rollup}
	enrichRollupTitlesFromSessionHistory(root, singleRollup)
	rollup = singleRollup[0]
	steps, err := readIndexedSteps(db, sessionID)
	if err != nil {
		return SessionUsageDetail{}, true, err
	}
	turns, err := readIndexedTurns(db, sessionID)
	if err != nil {
		return SessionUsageDetail{}, true, err
	}
	return buildSessionUsageDetail(rollup, steps, turns), true, nil
}

func readIndexedSteps(db *sql.DB, sessionID string) ([]StepUsage, error) {
	rows, err := db.Query("SELECT fact_json FROM analytics_llm_requests WHERE session_id = ? ORDER BY trace_id, step", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := make([]StepUsage, 0, 32)
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var step StepUsage
		if json.Unmarshal(raw, &step) == nil {
			steps = append(steps, step)
		}
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Timestamp.Before(steps[j].Timestamp) })
	return steps, rows.Err()
}

func readIndexedTurns(db *sql.DB, sessionID string) ([]TurnUsage, error) {
	rows, err := db.Query("SELECT fact_json FROM analytics_turns WHERE session_id = ? ORDER BY ordinal", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	turns := make([]TurnUsage, 0, 8)
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var turn TurnUsage
		if json.Unmarshal(raw, &turn) == nil {
			turns = append(turns, turn)
		}
	}
	return turns, rows.Err()
}

func buildSessionUsageDetail(rollup SessionRollup, steps []StepUsage, turns []TurnUsage) SessionUsageDetail {
	return SessionUsageDetail{
		SchemaVersion:   SchemaVersion,
		GeneratedAt:     time.Now(),
		Session:         rollup,
		Steps:           steps,
		StepCount:       len(steps),
		Turns:           turns,
		Diagnostics:     buildDiagnostics(rollup, turns),
		ErrorCategories: errorCategoryCounts(steps),
		Coverage:        coverageFor([]SessionRollup{rollup}),
		Partial:         rollup.Partial,
		PartialReasons:  append([]string(nil), rollup.PartialReasons...),
	}
}

type sessionTitleRecord struct {
	SessionID         string
	Title             string
	CreatedAt         time.Time
	TimeMatchEligible bool
}

type sessionTitleCatalog struct {
	ByID    map[string]sessionTitleRecord
	Records []sessionTitleRecord
}

func enrichRollupTitlesFromSessionHistory(root string, rollups []SessionRollup) {
	catalog, err := loadSessionTitleCatalog(root)
	if err != nil || catalog == nil {
		return
	}
	for index := range rollups {
		rollup := &rollups[index]
		if runtimeSessionID := strings.TrimSpace(rollup.RuntimeSessionID); runtimeSessionID != "" {
			if record, ok := catalog.ByID[strings.ToLower(runtimeSessionID)]; ok {
				rollup.Title = record.Title
				rollup.TitleSource = "session_history_id"
				continue
			}
		}
		if rollup.Title != "" && rollup.TitleSource != "initial_message" {
			continue
		}
		if record, ok := matchSessionTitleByStart(rollup.StartTime, catalog.Records); ok {
			rollup.RuntimeSessionID = record.SessionID
			rollup.Title = record.Title
			rollup.TitleSource = "session_history_time_match"
		}
	}
}

func loadSessionTitleCatalog(root string) (*sessionTitleCatalog, error) {
	if _, enabled := analyticsIndexPath(root); !enabled {
		return nil, nil
	}
	path := filepath.Join(aiclipaths.DefaultSessionsDir(), "session_history.sqlite")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout=2000"); err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id, title, created_at FROM sessions WHERE TRIM(title) <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalog := &sessionTitleCatalog{ByID: make(map[string]sessionTitleRecord)}
	for rows.Next() {
		var id, title, createdAtRaw string
		if rows.Scan(&id, &title, &createdAtRaw) != nil {
			continue
		}
		record := sessionTitleRecord{
			SessionID: strings.TrimSpace(id),
			Title:     normalizeSessionTitle(title),
			CreatedAt: parseSessionHistoryTime(createdAtRaw),
		}
		if sessionHistoryTitleNeedsRepair(record.Title) {
			record.Title = deriveSessionHistoryTitle(db, record.SessionID)
		}
		if record.SessionID == "" || record.Title == "" {
			continue
		}
		record.TimeMatchEligible = sessionHistoryTimeMatchEligible(record)
		catalog.ByID[strings.ToLower(record.SessionID)] = record
		if !record.CreatedAt.IsZero() {
			catalog.Records = append(catalog.Records, record)
		}
	}
	return catalog, rows.Err()
}

func parseSessionHistoryTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func matchSessionTitleByStart(start time.Time, records []sessionTitleRecord) (sessionTitleRecord, bool) {
	if start.IsZero() {
		return sessionTitleRecord{}, false
	}
	const matchWindow = time.Second
	const ambiguityGap = 100 * time.Millisecond
	bestIndex := -1
	bestDiff := matchWindow + time.Nanosecond
	secondDiff := matchWindow + time.Nanosecond
	for index, record := range records {
		if !record.TimeMatchEligible {
			continue
		}
		diff := start.Sub(record.CreatedAt)
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			secondDiff = bestDiff
			bestDiff = diff
			bestIndex = index
		} else if diff < secondDiff {
			secondDiff = diff
		}
	}
	if bestIndex < 0 || bestDiff > matchWindow {
		return sessionTitleRecord{}, false
	}
	if secondDiff <= matchWindow && secondDiff-bestDiff < ambiguityGap {
		return sessionTitleRecord{}, false
	}
	return records[bestIndex], true
}

func sessionHistoryTimeMatchEligible(record sessionTitleRecord) bool {
	sessionID := strings.ToLower(strings.TrimSpace(record.SessionID))
	return strings.HasPrefix(sessionID, "session_") && !sessionHistoryTitleNeedsRepair(record.Title)
}

func sessionHistoryTitleNeedsRepair(title string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(title)), " "))
	switch {
	case strings.HasPrefix(normalized, "shell guidance:"):
		return true
	case strings.HasPrefix(normalized, "file editing guidance:"):
		return true
	case strings.HasPrefix(normalized, "parallel tool guidance:"):
		return true
	case strings.HasPrefix(normalized, "detected operating system:"):
		return true
	case strings.HasPrefix(normalized, "running shell commands="):
		return true
	case strings.HasPrefix(normalized, "\u2022 running shell commands="):
		return true
	case strings.HasPrefix(normalized, "exit code:") && strings.Contains(normalized, " shell:"):
		return true
	case strings.HasPrefix(normalized, "runtime tool result contract:"):
		return true
	default:
		return false
	}
}

type sessionHistoryMessageTitleCandidate struct {
	Role    string
	Content string
}

func deriveSessionHistoryTitle(db *sql.DB, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	rows, err := db.Query("SELECT role, payload_json FROM session_messages WHERE session_id = ? ORDER BY seq LIMIT 64", sessionID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	messages := make([]sessionHistoryMessageTitleCandidate, 0, 8)
	for rows.Next() {
		var role string
		var raw []byte
		if rows.Scan(&role, &raw) != nil {
			continue
		}
		candidate := sessionHistoryMessageTitleCandidate{Role: strings.TrimSpace(role)}
		var payload map[string]interface{}
		if json.Unmarshal(raw, &payload) == nil {
			if payloadRole := stringValue(payload["role"]); strings.TrimSpace(payloadRole) != "" {
				candidate.Role = strings.TrimSpace(payloadRole)
			}
			candidate.Content = sessionHistoryContentString(payload["content"])
		}
		if strings.TrimSpace(candidate.Content) != "" {
			messages = append(messages, candidate)
		}
	}
	for _, role := range []string{"user", "assistant"} {
		for _, message := range messages {
			if strings.EqualFold(strings.TrimSpace(message.Role), role) && sessionHistoryTitleContentUsable(message.Role, message.Content) {
				return normalizeSessionTitle(message.Content)
			}
		}
	}
	for _, message := range messages {
		if sessionHistoryTitleContentUsable(message.Role, message.Content) {
			return normalizeSessionTitle(message.Content)
		}
	}
	return ""
}

func sessionHistoryContentString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			switch part := item.(type) {
			case string:
				parts = append(parts, part)
			case map[string]interface{}:
				if textValue := stringValue(part["text"]); strings.TrimSpace(textValue) != "" {
					parts = append(parts, textValue)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func sessionHistoryTitleContentUsable(role, content string) bool {
	content = strings.TrimSpace(content)
	if content == "" || sessionHistoryTitleNeedsRepair(content) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer", "tool":
		return false
	default:
		return true
	}
}
