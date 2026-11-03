package sessionpurge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

const deleteChunkSize = 300

type rowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func openRuntimeDB(path string, readonly bool) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var db *sql.DB
	if readonly {
		// Prefer mode=ro, but fall back to query_only when a WAL sidecar is not
		// writable. query_only still guarantees that this process cannot write.
		roDSN := "file:" + filepath.ToSlash(abs) + "?mode=ro"
		if candidate, openErr := sql.Open("sqlite3", roDSN); openErr == nil {
			if pingErr := candidate.PingContext(context.Background()); pingErr == nil {
				db = candidate
			} else {
				_ = candidate.Close()
			}
		}
	}
	if db == nil {
		candidate, openErr := sql.Open("sqlite3", abs)
		if openErr != nil {
			return nil, openErr
		}
		db = candidate
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if readonly {
		if _, err := db.ExecContext(context.Background(), "PRAGMA query_only=ON"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set query_only: %w", err)
		}
	}
	return db, nil
}

func tableExists(ctx context.Context, q rowQueryer, table string) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, rows.Err()
}

func queryStringIDs(ctx context.Context, q rowQueryer, query string, args ...interface{}) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if value := strings.TrimSpace(id.String); value != "" {
			ids = append(ids, value)
		}
	}
	return ids, rows.Err()
}

func querySessionTimes(ctx context.Context, q rowQueryer, query string) (map[string]time.Time, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]time.Time)
	for rows.Next() {
		var sessionID string
		var raw sql.NullString
		if err := rows.Scan(&sessionID, &raw); err != nil {
			return nil, err
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		ts, ok := parseRuntimeTime(raw.String)
		if !ok {
			continue
		}
		if previous, exists := result[sessionID]; !exists || ts.After(previous) {
			result[sessionID] = ts
		}
	}
	return result, rows.Err()
}

func querySessionUnixTimes(ctx context.Context, q rowQueryer, query string) (map[string]time.Time, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]time.Time)
	for rows.Next() {
		var sessionID string
		var nano sql.NullInt64
		if err := rows.Scan(&sessionID, &nano); err != nil {
			return nil, err
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" || !nano.Valid || nano.Int64 <= 0 {
			continue
		}
		ts := time.Unix(0, nano.Int64).UTC()
		if previous, exists := result[sessionID]; !exists || ts.After(previous) {
			result[sessionID] = ts
		}
	}
	return result, rows.Err()
}

func countRowsByIDs(ctx context.Context, q rowQueryer, table string, columns []string, ids []string) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += deleteChunkSize {
		end := start + deleteChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		predicate, args := idPredicate(columns, ids[start:end])
		rows, err := q.QueryContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, predicate), args...)
		if err != nil {
			return total, fmt.Errorf("count %s: %w", table, err)
		}
		var count int64
		if rows.Next() {
			if err := rows.Scan(&count); err != nil {
				_ = rows.Close()
				return total, err
			}
		}
		if err := rows.Close(); err != nil {
			return total, err
		}
		total += count
	}
	return total, nil
}

func deleteRowsByIDs(ctx context.Context, e execer, table string, columns []string, ids []string) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += deleteChunkSize {
		end := start + deleteChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		predicate, args := idPredicate(columns, ids[start:end])
		result, err := e.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s", table, predicate), args...)
		if err != nil {
			return total, fmt.Errorf("delete %s: %w", table, err)
		}
		affected, _ := result.RowsAffected()
		total += affected
	}
	return total, nil
}

func queryIDsByColumns(ctx context.Context, q rowQueryer, table, idColumn string, filterColumns []string, ids []string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string
	for start := 0; start < len(ids); start += deleteChunkSize {
		end := start + deleteChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		predicate, args := idPredicate(filterColumns, ids[start:end])
		query := fmt.Sprintf("SELECT DISTINCT %s FROM %s WHERE %s", idColumn, table, predicate)
		values, err := queryStringIDs(ctx, q, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query %s.%s: %w", table, idColumn, err)
		}
		for _, value := range values {
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}

func idPredicate(columns []string, ids []string) (string, []interface{}) {
	parts := make([]string, 0, len(columns))
	args := make([]interface{}, 0, len(columns)*len(ids))
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	for _, column := range columns {
		parts = append(parts, fmt.Sprintf("%s IN (%s)", column, placeholders))
		for _, id := range ids {
			args = append(args, id)
		}
	}
	return strings.Join(parts, " OR "), args
}

func parseRuntimeTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

func pathWithin(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
