package artifact

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	_ "github.com/mattn/go-sqlite3"
)

// StoreConfig 控制 artifact store 的持久化方式。
type StoreConfig struct {
	Path string
	DSN  string
}

// Record 表示一条窗口外归档的工具输出。
type Record struct {
	ID         string
	SessionID  string
	ToolName   string
	ToolCallID string
	Summary    string
	Content    string
	Metadata   map[string]interface{}
	CreatedAt  time.Time
}

// SearchResult 表示一次召回命中。
type SearchResult struct {
	ID         string
	SessionID  string
	ToolName   string
	ToolCallID string
	Summary    string
	Preview    string
	Metadata   map[string]interface{}
	SourceRefs []string
	CreatedAt  time.Time
}

// MemoryEntry 表示一条持久化 ledger 记录。
type MemoryEntry struct {
	ID         string
	SessionID  string
	TaskID     string
	Kind       string
	Priority   int
	Content    map[string]interface{}
	SourceRefs []string
	SourceHash string
	CreatedAt  time.Time
}

// Checkpoint 表示一次 compaction/checkpoint 快照。
type Checkpoint struct {
	ID           string
	SessionID    string
	TaskID       string
	Reason       string
	HistoryHash  string
	MessageCount int
	Ledger       []MemoryEntry
	Metadata     map[string]interface{}
	CreatedAt    time.Time
}

// Store 负责将原始输出落到 SQLite，并在可能时启用 FTS5。
type Store struct {
	db         *sql.DB
	ftsEnabled bool
}

// NewStore 创建一个 artifact store。
func NewStore(cfg *StoreConfig) (*Store, error) {
	if cfg == nil {
		cfg = &StoreConfig{}
	}

	dsn, err := resolveDSN(cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open artifact db: %w", err)
	}

	store := &Store{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

// Close 关闭底层数据库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Put 存储一条原始输出并返回 artifact id。
func (s *Store) Put(ctx context.Context, record Record) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("artifact store is not initialized")
	}

	if record.ID == "" {
		record.ID = "art_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	metadataJSON := "{}"
	if len(record.Metadata) > 0 {
		payload, err := json.Marshal(record.Metadata)
		if err != nil {
			return "", fmt.Errorf("marshal artifact metadata: %w", err)
		}
		metadataJSON = string(payload)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO artifacts (
			id, session_id, tool_name, tool_call_id, summary, content, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID,
		record.SessionID,
		record.ToolName,
		record.ToolCallID,
		record.Summary,
		record.Content,
		metadataJSON,
		record.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return "", fmt.Errorf("insert artifact: %w", err)
	}

	if s.ftsEnabled {
		_, _ = s.db.ExecContext(ctx, `
			INSERT INTO artifacts_fts (id, session_id, tool_name, content)
			VALUES (?, ?, ?, ?)
		`, record.ID, record.SessionID, record.ToolName, record.Content)
	}

	return record.ID, nil
}

// Get 读取一条 artifact。
func (s *Store) Get(ctx context.Context, id string) (*Record, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, tool_name, tool_call_id, summary, content, metadata_json, created_at
		FROM artifacts
		WHERE id = ?
	`, id)

	var (
		record       Record
		metadataJSON string
		createdAtRaw string
	)
	if err := row.Scan(
		&record.ID,
		&record.SessionID,
		&record.ToolName,
		&record.ToolCallID,
		&record.Summary,
		&record.Content,
		&metadataJSON,
		&createdAtRaw,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load artifact: %w", err)
	}

	if metadataJSON != "" {
		record.Metadata = map[string]interface{}{}
		_ = json.Unmarshal([]byte(metadataJSON), &record.Metadata)
	}
	if createdAtRaw != "" {
		record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
	}

	return &record, nil
}

// Search 在当前 session 内执行召回搜索。
func (s *Store) Search(ctx context.Context, sessionID, query string, limit int) ([]SearchResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}
	if limit <= 0 {
		limit = 5
	}

	searchWithFallback := func(useFTS bool) (*sql.Rows, error) {
		if useFTS && s.ftsEnabled && strings.TrimSpace(query) != "" {
			return s.db.QueryContext(ctx, `
				SELECT a.id, a.session_id, a.tool_name, a.tool_call_id, a.summary, a.content, a.metadata_json, a.created_at
				FROM artifacts_fts f
				JOIN artifacts a ON a.id = f.id
				WHERE a.session_id = ? AND artifacts_fts MATCH ?
				ORDER BY a.created_at DESC
				LIMIT ?
			`, sessionID, query, limit)
		}

		needle := "%" + query + "%"
		return s.db.QueryContext(ctx, `
			SELECT id, session_id, tool_name, tool_call_id, summary, content, metadata_json, created_at
			FROM artifacts
			WHERE session_id = ?
			  AND (content LIKE ? OR summary LIKE ? OR tool_name LIKE ?)
			ORDER BY created_at DESC
			LIMIT ?
		`, sessionID, needle, needle, needle, limit)
	}

	rows, err := searchWithFallback(true)
	if err != nil {
		rows, err = searchWithFallback(false)
	}
	if err != nil {
		return nil, fmt.Errorf("search artifacts: %w", err)
	}
	results, err := scanSearchResults(rows, limit)
	if err != nil {
		return nil, err
	}
	if len(results) > 0 || !s.ftsEnabled || strings.TrimSpace(query) == "" {
		return results, nil
	}

	rows, err = searchWithFallback(false)
	if err != nil {
		return nil, fmt.Errorf("search artifacts fallback: %w", err)
	}
	return scanSearchResults(rows, limit)
}

// InsertMemoryEntry 持久化一条 ledger，并基于 source hash 去重。
func (s *Store) InsertMemoryEntry(ctx context.Context, entry MemoryEntry) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("artifact store is not initialized")
	}
	if strings.TrimSpace(entry.SessionID) == "" {
		return "", fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(entry.Kind) == "" {
		return "", fmt.Errorf("memory entry kind is required")
	}
	if entry.Content == nil {
		entry.Content = map[string]interface{}{}
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.ID == "" {
		entry.ID = "mem_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if entry.SourceHash == "" {
		entry.SourceHash = hashMemoryEntry(entry)
	}

	contentJSON, err := json.Marshal(entry.Content)
	if err != nil {
		return "", fmt.Errorf("marshal memory content: %w", err)
	}
	sourceRefsJSON, err := json.Marshal(entry.SourceRefs)
	if err != nil {
		return "", fmt.Errorf("marshal memory source refs: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_entries (
			id, session_id, task_id, kind, priority, content_json, source_refs_json, source_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, kind, source_hash) DO NOTHING
	`,
		entry.ID,
		entry.SessionID,
		entry.TaskID,
		entry.Kind,
		entry.Priority,
		string(contentJSON),
		string(sourceRefsJSON),
		entry.SourceHash,
		entry.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return "", fmt.Errorf("insert memory entry: %w", err)
	}

	return entry.ID, nil
}

// LoadMemoryEntries 读取 ledger 项。
func (s *Store) LoadMemoryEntries(ctx context.Context, sessionID string, kinds []string, limit int) ([]MemoryEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}
	if limit <= 0 {
		limit = 12
	}

	var (
		rows *sql.Rows
		err  error
	)
	if len(kinds) == 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, session_id, task_id, kind, priority, content_json, source_refs_json, source_hash, created_at
			FROM memory_entries
			WHERE session_id = ?
			ORDER BY priority DESC, created_at DESC
			LIMIT ?
		`, sessionID, limit)
	} else {
		placeholders := make([]string, 0, len(kinds))
		args := make([]interface{}, 0, len(kinds)+2)
		args = append(args, sessionID)
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		args = append(args, limit)
		rows, err = s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT id, session_id, task_id, kind, priority, content_json, source_refs_json, source_hash, created_at
			FROM memory_entries
			WHERE session_id = ? AND kind IN (%s)
			ORDER BY priority DESC, created_at DESC
			LIMIT ?
		`, strings.Join(placeholders, ",")), args...)
	}
	if err != nil {
		return nil, fmt.Errorf("load memory entries: %w", err)
	}
	defer rows.Close()

	entries := make([]MemoryEntry, 0, limit)
	for rows.Next() {
		var (
			entry          MemoryEntry
			contentJSON    string
			sourceRefsJSON string
			createdAtRaw   string
		)
		if err := rows.Scan(
			&entry.ID,
			&entry.SessionID,
			&entry.TaskID,
			&entry.Kind,
			&entry.Priority,
			&contentJSON,
			&sourceRefsJSON,
			&entry.SourceHash,
			&createdAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan memory entry: %w", err)
		}
		entry.Content = map[string]interface{}{}
		_ = json.Unmarshal([]byte(contentJSON), &entry.Content)
		_ = json.Unmarshal([]byte(sourceRefsJSON), &entry.SourceRefs)
		entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// SaveCheckpoint 持久化一次 checkpoint 快照。
func (s *Store) SaveCheckpoint(ctx context.Context, checkpoint Checkpoint) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("artifact store is not initialized")
	}
	if checkpoint.SessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	if checkpoint.ID == "" {
		checkpoint.ID = "chk_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if checkpoint.Metadata == nil {
		checkpoint.Metadata = map[string]interface{}{}
	}

	ledgerJSON, err := json.Marshal(checkpoint.Ledger)
	if err != nil {
		return "", fmt.Errorf("marshal checkpoint ledger: %w", err)
	}
	metadataJSON, err := json.Marshal(checkpoint.Metadata)
	if err != nil {
		return "", fmt.Errorf("marshal checkpoint metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO checkpoints (
			id, session_id, task_id, reason, history_hash, message_count, ledger_json, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, checkpoint.ID, checkpoint.SessionID, checkpoint.TaskID, checkpoint.Reason, checkpoint.HistoryHash, checkpoint.MessageCount, string(ledgerJSON), string(metadataJSON), checkpoint.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return "", fmt.Errorf("save checkpoint: %w", err)
	}

	return checkpoint.ID, nil
}

// UpdateCheckpoint updates an existing checkpoint record.
func (s *Store) UpdateCheckpoint(ctx context.Context, checkpoint Checkpoint) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("artifact store is not initialized")
	}
	if strings.TrimSpace(checkpoint.ID) == "" {
		return fmt.Errorf("checkpoint id is required")
	}
	if strings.TrimSpace(checkpoint.SessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	if checkpoint.Metadata == nil {
		checkpoint.Metadata = map[string]interface{}{}
	}

	ledgerJSON, err := json.Marshal(checkpoint.Ledger)
	if err != nil {
		return fmt.Errorf("marshal checkpoint ledger: %w", err)
	}
	metadataJSON, err := json.Marshal(checkpoint.Metadata)
	if err != nil {
		return fmt.Errorf("marshal checkpoint metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE checkpoints
		SET session_id = ?, task_id = ?, reason = ?, history_hash = ?, message_count = ?, ledger_json = ?, metadata_json = ?, created_at = ?
		WHERE id = ?
	`, checkpoint.SessionID, checkpoint.TaskID, checkpoint.Reason, checkpoint.HistoryHash, checkpoint.MessageCount, string(ledgerJSON), string(metadataJSON), checkpoint.CreatedAt.Format(time.RFC3339Nano), checkpoint.ID)
	if err != nil {
		return fmt.Errorf("update checkpoint: %w", err)
	}
	return nil
}

// LatestCheckpoint 返回 session 最近的 checkpoint。
func (s *Store) LatestCheckpoint(ctx context.Context, sessionID string) (*Checkpoint, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, task_id, reason, history_hash, message_count, ledger_json, metadata_json, created_at
		FROM checkpoints
		WHERE session_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID)

	var (
		checkpoint   Checkpoint
		ledgerJSON   string
		metadataJSON string
		createdAtRaw string
	)
	if err := row.Scan(
		&checkpoint.ID,
		&checkpoint.SessionID,
		&checkpoint.TaskID,
		&checkpoint.Reason,
		&checkpoint.HistoryHash,
		&checkpoint.MessageCount,
		&ledgerJSON,
		&metadataJSON,
		&createdAtRaw,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load latest checkpoint: %w", err)
	}

	_ = json.Unmarshal([]byte(ledgerJSON), &checkpoint.Ledger)
	checkpoint.Metadata = map[string]interface{}{}
	_ = json.Unmarshal([]byte(metadataJSON), &checkpoint.Metadata)
	checkpoint.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
	return &checkpoint, nil
}

// GetCheckpoint returns a checkpoint by id.
func (s *Store) GetCheckpoint(ctx context.Context, id string) (*Checkpoint, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, task_id, reason, history_hash, message_count, ledger_json, metadata_json, created_at
		FROM checkpoints
		WHERE id = ?
	`, id)

	var (
		checkpoint   Checkpoint
		ledgerJSON   string
		metadataJSON string
		createdAtRaw string
	)
	if err := row.Scan(
		&checkpoint.ID,
		&checkpoint.SessionID,
		&checkpoint.TaskID,
		&checkpoint.Reason,
		&checkpoint.HistoryHash,
		&checkpoint.MessageCount,
		&ledgerJSON,
		&metadataJSON,
		&createdAtRaw,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load checkpoint: %w", err)
	}

	_ = json.Unmarshal([]byte(ledgerJSON), &checkpoint.Ledger)
	checkpoint.Metadata = map[string]interface{}{}
	_ = json.Unmarshal([]byte(metadataJSON), &checkpoint.Metadata)
	checkpoint.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
	return &checkpoint, nil
}

// ListCheckpoints returns checkpoints for a session ordered by recency.
func (s *Store) ListCheckpoints(ctx context.Context, sessionID string, limit, offset int) ([]Checkpoint, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}

	query := `
		SELECT id, session_id, task_id, reason, history_hash, message_count, ledger_json, metadata_json, created_at
		FROM checkpoints
		WHERE session_id = ?
		ORDER BY created_at DESC
	`
	args := []interface{}{sessionID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
		if offset > 0 {
			query += " OFFSET ?"
			args = append(args, offset)
		}
	} else if offset > 0 {
		query += " LIMIT -1 OFFSET ?"
		args = append(args, offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	defer rows.Close()

	results := make([]Checkpoint, 0)
	for rows.Next() {
		var (
			checkpoint   Checkpoint
			ledgerJSON   string
			metadataJSON string
			createdAtRaw string
		)
		if err := rows.Scan(
			&checkpoint.ID,
			&checkpoint.SessionID,
			&checkpoint.TaskID,
			&checkpoint.Reason,
			&checkpoint.HistoryHash,
			&checkpoint.MessageCount,
			&ledgerJSON,
			&metadataJSON,
			&createdAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan checkpoint: %w", err)
		}
		if ledgerJSON != "" {
			_ = json.Unmarshal([]byte(ledgerJSON), &checkpoint.Ledger)
		}
		checkpoint.Metadata = map[string]interface{}{}
		if metadataJSON != "" {
			_ = json.Unmarshal([]byte(metadataJSON), &checkpoint.Metadata)
		}
		if createdAtRaw != "" {
			checkpoint.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
		}
		results = append(results, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) init(ctx context.Context) error {
	migrations := []migrate.Migration{
		{
			Version: 1,
			Name:    "artifacts",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS artifacts (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					tool_name TEXT NOT NULL,
					tool_call_id TEXT,
					summary TEXT,
					content TEXT NOT NULL,
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_artifacts_session_created
				ON artifacts(session_id, created_at DESC);
			`,
		},
		{
			Version: 2,
			Name:    "memory_entries",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS memory_entries (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					task_id TEXT,
					kind TEXT NOT NULL,
					priority INTEGER NOT NULL DEFAULT 0,
					content_json TEXT NOT NULL,
					source_refs_json TEXT NOT NULL DEFAULT '[]',
					source_hash TEXT NOT NULL,
					created_at TEXT NOT NULL
				);
				CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entries_session_kind_hash
				ON memory_entries(session_id, kind, source_hash);
				CREATE INDEX IF NOT EXISTS idx_memory_entries_session_priority
				ON memory_entries(session_id, priority DESC, created_at DESC);
			`,
		},
		{
			Version: 3,
			Name:    "checkpoints",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS checkpoints (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					task_id TEXT,
					reason TEXT,
					history_hash TEXT NOT NULL,
					message_count INTEGER NOT NULL DEFAULT 0,
					ledger_json TEXT NOT NULL,
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_checkpoints_session_created
				ON checkpoints(session_id, created_at DESC);
			`,
		},
		{
			Version: 4,
			Name:    "checkpoint_files",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS checkpoint_files (
					id TEXT PRIMARY KEY,
					checkpoint_id TEXT NOT NULL,
					path TEXT NOT NULL,
					op TEXT NOT NULL,
					before_blob_id TEXT,
					after_blob_id TEXT,
					before_hash TEXT,
					after_hash TEXT,
					diff_text BLOB
				);
				CREATE INDEX IF NOT EXISTS idx_checkpoint_files_checkpoint
				ON checkpoint_files(checkpoint_id);
			`,
		},
		{
			Version: 5,
			Name:    "blobs",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS blobs (
					id TEXT PRIMARY KEY,
					sha256 TEXT NOT NULL UNIQUE,
					encoding TEXT NOT NULL,
					data BLOB NOT NULL
				);
			`,
		},
	}
	if err := migrate.Apply(ctx, s.db, migrations); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE VIRTUAL TABLE IF NOT EXISTS artifacts_fts
		USING fts5(id UNINDEXED, session_id UNINDEXED, tool_name, content)
	`); err == nil {
		s.ftsEnabled = true
	}

	return nil
}

func resolveDSN(cfg *StoreConfig) (string, error) {
	if cfg.Path != "" {
		dir := filepath.Dir(cfg.Path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create artifact directory: %w", err)
			}
		}
		return cfg.Path, nil
	}
	if cfg.DSN != "" {
		return cfg.DSN, nil
	}

	return fmt.Sprintf("file:runtime-artifacts-%s?mode=memory&cache=shared", uuid.NewString()), nil
}

func preview(content string, maxLen int) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if maxLen <= 0 || len(content) <= maxLen {
		return content
	}
	if maxLen <= 3 {
		return content[:maxLen]
	}
	return content[:maxLen-3] + "..."
}

func scanSearchResults(rows *sql.Rows, limit int) ([]SearchResult, error) {
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var (
			item         SearchResult
			summary      string
			content      string
			metadataJSON string
			createdAtRaw string
		)
		if err := rows.Scan(
			&item.ID,
			&item.SessionID,
			&item.ToolName,
			&item.ToolCallID,
			&summary,
			&content,
			&metadataJSON,
			&createdAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan artifact search result: %w", err)
		}
		item.Summary = strings.TrimSpace(summary)
		item.Preview = preview(content, 280)
		if strings.TrimSpace(metadataJSON) != "" {
			item.Metadata = map[string]interface{}{}
			_ = json.Unmarshal([]byte(metadataJSON), &item.Metadata)
			item.SourceRefs = extractSourceRefs(item.Metadata)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
		results = append(results, item)
	}

	return results, rows.Err()
}

func extractSourceRefs(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	refs := make([]string, 0)
	for _, key := range []string{"source_refs", "artifact_refs"} {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			refs = append(refs, typed...)
		case []interface{}:
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					refs = append(refs, strings.TrimSpace(text))
				}
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return dedupeStrings(refs)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hashMemoryEntry(entry MemoryEntry) string {
	payload := map[string]interface{}{
		"kind":        entry.Kind,
		"task_id":     entry.TaskID,
		"content":     entry.Content,
		"source_refs": entry.SourceRefs,
	}

	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ordered := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		ordered[key] = payload[key]
	}

	data, _ := json.Marshal(ordered)
	sum := sha1.Sum(data)
	return fmt.Sprintf("%x", sum[:])
}
