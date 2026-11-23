package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The FTS table is deliberately created outside the migration ledger. FTS5 is
// an optional SQLite extension: a build without it must still be able to open
// and migrate the main memory database. On the mainline ncruces driver the
// sqlitedriver package registers ext/fts5; win7compat's embedded Wasm build
// already includes FTS5. The capability is still probed at runtime, so any
// driver without the module keeps the LIKE fallback.
const (
	memoryFTSTable = "memory_entries_fts"

	createMemoryFTSSQL = `
CREATE VIRTUAL TABLE IF NOT EXISTS memory_entries_fts USING fts5(
    title, text, summary, tags_json,
    content='memory_entries', content_rowid='rowid',
    tokenize='trigram'
)`

	memoryFTSRebuildSQL = `INSERT INTO memory_entries_fts(memory_entries_fts) VALUES('rebuild')`

	// Keep this column order aligned with scanRecord.
	memoryRecordFTSSelectColumns = `
e.id, e.scope, e.project_key, e.kind, e.status, e.text, e.normalized_text, e.tags_json,
e.confidence, e.sensitivity, e.pinned, e.retention_class, e.source_kind, e.source_session_id,
e.source_session_generation, e.source_turn_id, e.source_refs_json, e.content_hash,
e.supersedes_id, e.revision, e.created_at, e.updated_at, e.last_used_at, e.use_count,
e.valid_from, e.valid_until, e.freshness_status, e.last_verified_at, e.revalidate_after,
e.stale_reason, e.deleted_at, e.metadata_json, e.title, e.summary, e.canonical_id,
e.superseded_by, e.archived`
)

var memoryFTSTriggerSQL = []string{
	`CREATE TRIGGER IF NOT EXISTS memory_entries_fts_ai AFTER INSERT ON memory_entries BEGIN
		INSERT INTO memory_entries_fts(rowid, title, text, summary, tags_json)
		VALUES (new.rowid, new.title, new.text, new.summary, new.tags_json);
	END`,
	`CREATE TRIGGER IF NOT EXISTS memory_entries_fts_ad AFTER DELETE ON memory_entries BEGIN
		INSERT INTO memory_entries_fts(memory_entries_fts, rowid, title, text, summary, tags_json)
		VALUES ('delete', old.rowid, old.title, old.text, old.summary, old.tags_json);
	END`,
	`CREATE TRIGGER IF NOT EXISTS memory_entries_fts_au AFTER UPDATE ON memory_entries BEGIN
		INSERT INTO memory_entries_fts(memory_entries_fts, rowid, title, text, summary, tags_json)
		VALUES ('delete', old.rowid, old.title, old.text, old.summary, old.tags_json);
		INSERT INTO memory_entries_fts(rowid, title, text, summary, tags_json)
		VALUES (new.rowid, new.title, new.text, new.summary, new.tags_json);
	END`,
}

func memoryFTSTableExists(ctx context.Context, db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		memoryFTSTable).Scan(&name)
	switch {
	case err == nil:
		return true, nil
	case err == sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}

// ensureMemoryFTS prepares the derived FTS index. The main store remains the
// source of truth. If FTS5 is unavailable, all FTS objects created by a prior
// run are removed so ordinary memory writes cannot fail on missing triggers.
func ensureMemoryFTS(ctx context.Context, db *sql.DB) (bool, error) {
	existed, err := memoryFTSTableExists(ctx, db)
	if err != nil {
		return false, fmt.Errorf("memorystore: inspect FTS table: %w", err)
	}
	triggersReady, err := memoryFTSTriggersExist(ctx, db)
	if err != nil {
		return false, fmt.Errorf("memorystore: inspect FTS triggers: %w", err)
	}
	if !existed {
		if _, err := db.ExecContext(ctx, createMemoryFTSSQL); err != nil {
			_ = dropMemoryFTSObjects(ctx, db)
			return false, fmt.Errorf("memorystore: create FTS table: %w", err)
		}
	}
	for _, ddl := range memoryFTSTriggerSQL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			_ = dropMemoryFTSObjects(ctx, db)
			return false, fmt.Errorf("memorystore: create FTS trigger: %w", err)
		}
	}
	// A real read proves the module is usable, not just that the catalogue row
	// exists from a previous build.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_entries_fts`).Scan(&count); err != nil {
		_ = dropMemoryFTSObjects(ctx, db)
		return false, fmt.Errorf("memorystore: probe FTS table: %w", err)
	}
	if !existed || !triggersReady {
		if _, err := db.ExecContext(ctx, memoryFTSRebuildSQL); err != nil {
			_ = dropMemoryFTSObjects(ctx, db)
			return false, fmt.Errorf("memorystore: rebuild FTS index: %w", err)
		}
	}
	return true, nil
}

func memoryFTSTriggersExist(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM sqlite_master
WHERE type = 'trigger' AND name IN (
    'memory_entries_fts_ai', 'memory_entries_fts_ad', 'memory_entries_fts_au'
)
`).Scan(&count); err != nil {
		return false, err
	}
	return count == len(memoryFTSTriggerSQL), nil
}

func dropMemoryFTSObjects(ctx context.Context, db *sql.DB) error {
	for _, name := range []string{
		"memory_entries_fts_ai",
		"memory_entries_fts_ad",
		"memory_entries_fts_au",
	} {
		if _, err := db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+name); err != nil {
			return err
		}
	}
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+memoryFTSTable)
	return nil
}

func memoryFTSMatchQuery(text string) string {
	parts := make([]string, 0, 4)
	for _, token := range strings.Fields(strings.TrimSpace(text)) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		// Quoting keeps FTS operators and punctuation inert. Double quotes in
		// user input are escaped according to the FTS5 string-literal rule.
		token = strings.ReplaceAll(token, `"`, `""`)
		parts = append(parts, `"`+token+`"`)
	}
	return strings.Join(parts, " AND ")
}

func (s *Store) searchV2FTS(ctx context.Context, opts SearchOptions) ([]SearchHit, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if !s.ftsEnabled {
		return nil, nil
	}
	query := memoryFTSMatchQuery(opts.Query)
	if query == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	queryLimit := limit * 4
	if queryLimit < limit {
		queryLimit = limit
	}
	if queryLimit > 200 {
		queryLimit = 200
	}

	sqlQuery := `
SELECT e.id, e.text, e.tags_json, e.source_kind, e.source_session_id, e.created_at,
       e.title, e.summary, e.canonical_id, e.archived, bm25(memory_entries_fts)
FROM memory_entries_fts
JOIN memory_entries e ON e.rowid = memory_entries_fts.rowid
WHERE memory_entries_fts MATCH ?
  AND e.scope = ? AND e.project_key = ? AND e.status = 'active' AND e.archived = 0`
	args := []interface{}{query, string(ScopeProject), s.cfg.ProjectKey}
	if sourceKind := strings.ToLower(strings.TrimSpace(opts.SourceKind)); sourceKind != "" {
		sqlQuery += " AND LOWER(e.source_kind) = ?"
		args = append(args, sourceKind)
	}
	sqlQuery += " ORDER BY bm25(memory_entries_fts), e.updated_at DESC LIMIT ?"
	args = append(args, queryLimit)

	rows, err := db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: FTS search: %w", err)
	}
	defer rows.Close()

	requiredTags := normalizeTags(opts.Tags)
	hits := make([]SearchHit, 0, queryLimit)
	for rows.Next() {
		var (
			note                            Note
			tagsJSON, sourceKind, sessionID sql.NullString
			title, summary, canonicalID     sql.NullString
			createdAt                       string
			archived                        int
			rank                            float64
		)
		if err := rows.Scan(&note.ID, &note.Text, &tagsJSON, &sourceKind, &sessionID, &createdAt,
			&title, &summary, &canonicalID, &archived, &rank); err != nil {
			return nil, fmt.Errorf("memorystore: scan FTS hit: %w", err)
		}
		note.Title = title.String
		note.Summary = summary.String
		note.CanonicalID = canonicalID.String
		note.Source = sourceKind.String
		note.SessionID = sessionID.String
		note.Archived = archived != 0
		note.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(tagsJSON.String), &note.Tags)
		if !noteHasAllTags(note, requiredTags) {
			continue
		}
		hits = append(hits, SearchHit{Note: note, Score: -rank})
		if len(hits) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memorystore: read FTS hits: %w", err)
	}
	return hits, nil
}

// listRecordsFTS serves the HTTP records query through the same FTS index as
// Store.Search. It is intentionally bounded and leaves cursor semantics
// identical to the LIKE path: offset is an opaque page cursor, not a record id.
func (s *Store) listRecordsFTS(
	ctx context.Context, db *sql.DB, filter RecordFilter, limit, offset int,
) (RecordPage, error) {
	match := memoryFTSMatchQuery(filter.Query)
	if match == "" {
		return RecordPage{}, nil
	}
	where := []string{
		"memory_entries_fts MATCH ?",
		"e.scope = ?",
		"e.project_key = ?",
	}
	args := []interface{}{match, string(ScopeProject), s.cfg.ProjectKey}
	if !filter.IncludeArchived {
		where = append(where, "e.archived = 0")
	}
	if filter.Status != "" {
		where = append(where, "e.status = ?")
		args = append(args, string(filter.Status))
	} else {
		where = append(where, "e.status = 'active'")
	}
	if filter.Status == "" || filter.Status == StatusActive {
		where = append(where, "(e.valid_until IS NULL OR e.valid_until = '' OR e.valid_until > ?)")
		args = append(args, time.Now().UTC().Format(time.RFC3339Nano))
	}
	if filter.Kind != "" {
		where = append(where, "e.kind = ?")
		args = append(args, filter.Kind)
	}
	if sourceKind := strings.ToLower(strings.TrimSpace(filter.SourceKind)); sourceKind != "" {
		where = append(where, "LOWER(e.source_kind) = ?")
		args = append(args, sourceKind)
	}
	if strings.TrimSpace(filter.Tag) != "" {
		where = append(where, "LOWER(e.tags_json) LIKE ?")
		args = append(args, "%\""+strings.ToLower(strings.TrimSpace(filter.Tag))+"\"%")
	}
	if strings.TrimSpace(filter.SourceSessionID) != "" {
		where = append(where, "e.source_session_id = ?")
		args = append(args, strings.TrimSpace(filter.SourceSessionID))
	}
	if filter.SourceSessionGeneration != nil {
		where = append(where, "e.source_session_generation = ?")
		args = append(args, *filter.SourceSessionGeneration)
	}
	if filter.PinnedOnly {
		where = append(where, "e.pinned = 1")
	}
	query := `SELECT ` + memoryRecordFTSSelectColumns + `
FROM memory_entries_fts
JOIN memory_entries e ON e.rowid = memory_entries_fts.rowid
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY bm25(memory_entries_fts), e.updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return RecordPage{}, fmt.Errorf("memorystore: list FTS records: %w", err)
	}
	defer rows.Close()
	items := make([]Record, 0, limit)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return RecordPage{}, scanErr
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return RecordPage{}, fmt.Errorf("memorystore: read FTS records: %w", err)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.Itoa(offset + limit)
	}
	return RecordPage{Items: items, NextCursor: next, Count: len(items)}, nil
}
