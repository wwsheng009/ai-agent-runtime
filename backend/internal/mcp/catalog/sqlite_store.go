package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	_ "github.com/mattn/go-sqlite3"
)

// SQLiteSnapshotStore 使用 SQLite 持久化 catalog 快照。
type SQLiteSnapshotStore struct {
	path       string
	dsn        string
	db         *sql.DB
	ftsEnabled bool
}

// NewSQLiteSnapshotStore 创建一个 SQLite-backed snapshot store。
func NewSQLiteSnapshotStore(path string) (*SQLiteSnapshotStore, error) {
	store := &SQLiteSnapshotStore{
		path: strings.TrimSpace(path),
	}
	dsn, err := store.resolveDSN()
	if err != nil {
		return nil, err
	}
	store.dsn = dsn
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open catalog sqlite store: %w", err)
	}
	store.db = db
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 关闭底层数据库连接。
func (s *SQLiteSnapshotStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path 返回底层 sqlite 文件路径。
func (s *SQLiteSnapshotStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// LoadCatalogSnapshot 读取最近一次快照。
func (s *SQLiteSnapshotStore) LoadCatalogSnapshot() (*Snapshot, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	row := s.db.QueryRow(`
		SELECT snapshot_json
		FROM catalog_snapshot
		WHERE id = 1
	`)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load catalog sqlite snapshot: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, fmt.Errorf("decode catalog sqlite snapshot: %w", err)
	}
	return &snapshot, nil
}

// SaveCatalogSnapshot 持久化最新快照。
func (s *SQLiteSnapshotStore) SaveCatalogSnapshot(snapshot Snapshot) error {
	if s == nil || s.db == nil {
		return nil
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode catalog sqlite snapshot: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO catalog_snapshot (id, snapshot_json, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			snapshot_json = excluded.snapshot_json,
			updated_at = excluded.updated_at
	`, string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save catalog sqlite snapshot: %w", err)
	}
	if err := s.replaceCatalogTools(snapshot.Tools); err != nil {
		return err
	}
	return nil
}

// SearchCatalogTools 使用持久化目录索引搜索工具。
func (s *SQLiteSnapshotStore) SearchCatalogTools(query string, limit int) ([]skill.ToolInfo, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	if s.ftsEnabled {
		ftsQuery, normalizedQuery := buildSQLiteFTSQuery(query)
		if strings.TrimSpace(ftsQuery) == "" {
			ftsQuery = query
			normalizedQuery = normalizeForSearch(query)
		}
		rows, err := s.db.Query(`
			SELECT c.name, c.description, c.input_schema_json, c.mcp_name, c.mcp_trust_level, c.execution_mode, c.enabled
			FROM catalog_tools_fts5 f
			JOIN catalog_tools c ON c.name = f.tool_key
			WHERE catalog_tools_fts5 MATCH ?
			ORDER BY
				CASE WHEN f.search_name = ? THEN 1 ELSE 0 END DESC,
				CASE WHEN ? != '' AND instr(f.search_name, ?) > 0 THEN 1 ELSE 0 END DESC,
				CASE WHEN ? != '' AND instr(f.description, ?) > 0 THEN 1 ELSE 0 END DESC,
				bm25(catalog_tools_fts5, 6.0, 2.0, 1.0) ASC,
				c.name ASC
			LIMIT ?
		`, ftsQuery, normalizedQuery, normalizedQuery, normalizedQuery, normalizedQuery, normalizedQuery, limit)
		if err == nil {
			return scanCatalogToolRows(rows)
		}
	}

	needle := "%" + normalizeForSearch(query) + "%"
	rows, err := s.db.Query(`
		SELECT name, description, input_schema_json, mcp_name, mcp_trust_level, execution_mode, enabled
		FROM catalog_tools
		WHERE lower(replace(name, '_', ' ')) LIKE ?
		   OR lower(replace(description, '_', ' ')) LIKE ?
		   OR lower(replace(arg_names, '_', ' ')) LIKE ?
		ORDER BY name ASC
		LIMIT ?
	`, needle, needle, needle, limit)
	if err != nil {
		return nil, fmt.Errorf("search catalog sqlite tools: %w", err)
	}
	return scanCatalogToolRows(rows)
}

func buildSQLiteFTSQuery(query string) (string, string) {
	normalized := normalizeForSearch(query)
	tokens := tokenize(normalized)
	if len(tokens) == 0 {
		return "", normalized
	}
	if len(tokens) == 1 {
		return tokens[0] + "*", normalized
	}
	phrase := `"` + strings.Join(tokens, " ") + `"`
	andQuery := strings.Join(tokens, " ")
	return phrase + " OR " + andQuery, normalized
}

func (s *SQLiteSnapshotStore) init() error {
	if s == nil || s.db == nil {
		return nil
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS catalog_snapshot (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			snapshot_json TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create catalog_snapshot table: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS catalog_tools (
			name TEXT PRIMARY KEY,
			description TEXT NOT NULL,
			input_schema_json TEXT NOT NULL DEFAULT '{}',
			arg_names TEXT NOT NULL DEFAULT '',
			mcp_name TEXT NOT NULL DEFAULT '',
			mcp_trust_level TEXT NOT NULL DEFAULT '',
			execution_mode TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1
		)
	`); err != nil {
		return fmt.Errorf("create catalog_tools table: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS catalog_tools_fts5
		USING fts5(tool_key UNINDEXED, search_name, description, arg_names)
	`); err == nil {
		s.ftsEnabled = true
	}
	return nil
}

func (s *SQLiteSnapshotStore) resolveDSN() (string, error) {
	if s == nil {
		return "", fmt.Errorf("sqlite snapshot store is nil")
	}
	if strings.TrimSpace(s.path) == "" {
		return "", fmt.Errorf("sqlite snapshot store path is required")
	}
	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create sqlite snapshot dir: %w", err)
		}
	}
	return s.path, nil
}

func (s *SQLiteSnapshotStore) replaceCatalogTools(tools []skill.ToolInfo) error {
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin catalog sqlite refresh: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	existing, loadErr := s.loadCatalogToolSignatures(tx)
	if loadErr != nil {
		return loadErr
	}
	seen := make(map[string]bool, len(tools))

	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		seen[name] = true
		signature := toolSignature(tool)
		if current, ok := existing[name]; ok && current == signature {
			continue
		}
		schemaJSON, marshalErr := json.Marshal(tool.InputSchema)
		if marshalErr != nil {
			err = fmt.Errorf("encode catalog tool schema: %w", marshalErr)
			return err
		}
		argNames := strings.Join(extractArgNames(tool.InputSchema), " ")
		enabled := 0
		if tool.Enabled {
			enabled = 1
		}
		if _, err = tx.Exec(`
			INSERT INTO catalog_tools (
				name, description, input_schema_json, arg_names, mcp_name, mcp_trust_level, execution_mode, enabled
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				description = excluded.description,
				input_schema_json = excluded.input_schema_json,
				arg_names = excluded.arg_names,
				mcp_name = excluded.mcp_name,
				mcp_trust_level = excluded.mcp_trust_level,
				execution_mode = excluded.execution_mode,
				enabled = excluded.enabled
		`, name, tool.Description, string(schemaJSON), argNames, tool.MCPName, tool.MCPTrustLevel, tool.ExecutionMode, enabled); err != nil {
			return fmt.Errorf("upsert catalog tool: %w", err)
		}
		if s.ftsEnabled {
			if _, err = tx.Exec(`
				DELETE FROM catalog_tools_fts5 WHERE tool_key = ?
			`, name); err != nil {
				return fmt.Errorf("delete prior catalog tool fts row: %w", err)
			}
			if _, err = tx.Exec(`
				INSERT INTO catalog_tools_fts5 (tool_key, search_name, description, arg_names)
				VALUES (?, ?, ?, ?)
			`, name, normalizeForSearch(tool.Name), normalizeForSearch(tool.Description), normalizeForSearch(argNames)); err != nil {
				return fmt.Errorf("insert catalog tool fts row: %w", err)
			}
		}
	}
	for name := range existing {
		if seen[name] {
			continue
		}
		if _, err = tx.Exec(`DELETE FROM catalog_tools WHERE name = ?`, name); err != nil {
			return fmt.Errorf("delete removed catalog tool: %w", err)
		}
		if s.ftsEnabled {
			if _, err = tx.Exec(`DELETE FROM catalog_tools_fts5 WHERE tool_key = ?`, name); err != nil {
				return fmt.Errorf("delete removed catalog tool fts row: %w", err)
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog sqlite refresh: %w", err)
	}
	return nil
}

func (s *SQLiteSnapshotStore) loadCatalogToolSignatures(tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.Query(`
		SELECT name, description, input_schema_json, mcp_name, mcp_trust_level, execution_mode, enabled
		FROM catalog_tools
	`)
	if err != nil {
		return nil, fmt.Errorf("load catalog tool signatures: %w", err)
	}
	defer rows.Close()

	signatures := make(map[string]string)
	for rows.Next() {
		var (
			name          string
			description   string
			schemaJSON    string
			mcpName       string
			trustLevel    string
			executionMode string
			enabled       int
		)
		if err := rows.Scan(&name, &description, &schemaJSON, &mcpName, &trustLevel, &executionMode, &enabled); err != nil {
			return nil, fmt.Errorf("scan catalog tool signature: %w", err)
		}
		tool := skill.ToolInfo{
			Name:          name,
			Description:   description,
			MCPName:       mcpName,
			MCPTrustLevel: trustLevel,
			ExecutionMode: executionMode,
			Enabled:       enabled != 0,
		}
		if strings.TrimSpace(schemaJSON) != "" {
			tool.InputSchema = map[string]interface{}{}
			_ = json.Unmarshal([]byte(schemaJSON), &tool.InputSchema)
		}
		signatures[name] = toolSignature(tool)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog tool signatures: %w", err)
	}
	return signatures, nil
}

func scanCatalogToolRows(rows *sql.Rows) ([]skill.ToolInfo, error) {
	if rows == nil {
		return nil, nil
	}
	defer rows.Close()

	results := make([]skill.ToolInfo, 0)
	for rows.Next() {
		var (
			tool       skill.ToolInfo
			schemaJSON string
			enabled    int
		)
		if err := rows.Scan(&tool.Name, &tool.Description, &schemaJSON, &tool.MCPName, &tool.MCPTrustLevel, &tool.ExecutionMode, &enabled); err != nil {
			return nil, fmt.Errorf("scan catalog sqlite tool: %w", err)
		}
		tool.Enabled = enabled != 0
		if strings.TrimSpace(schemaJSON) != "" {
			tool.InputSchema = map[string]interface{}{}
			_ = json.Unmarshal([]byte(schemaJSON), &tool.InputSchema)
		}
		results = append(results, tool)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog sqlite tools: %w", err)
	}
	return results, nil
}
