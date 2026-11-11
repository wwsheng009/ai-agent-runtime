package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

type AuditEvent struct {
	ID        string                 `json:"id"`
	MemoryID  string                 `json:"memory_id"`
	EventType string                 `json:"event_type"`
	Actor     string                 `json:"actor"`
	SessionID string                 `json:"session_id,omitempty"`
	Revision  int64                  `json:"revision"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

func (s *Store) RevalidateRecord(ctx context.Context, id string) (Record, error) {
	record, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Status != StatusActive {
		return Record{}, fmt.Errorf("memorystore: only active records can be revalidated")
	}
	now := time.Now().UTC()
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET freshness_status = 'fresh', last_verified_at = ?, stale_reason = NULL,
    revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = 'active'
`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), record.ID, s.cfg.ProjectKey, record.Revision)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: record %s changed concurrently", record.ID)
	}
	if err := insertEventTx(ctx, tx, record.ID, "revalidated", "admin", record.SourceSessionID, record.Revision+1, map[string]interface{}{
		"reason": "explicit_revalidate",
	}); err != nil {
		return Record{}, err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, record.ID, "record_revalidated"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(ctx, id)
}

func (s *Store) RestoreRecord(ctx context.Context, id string) (Record, error) {
	record, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Status != StatusDeleted {
		return Record{}, fmt.Errorf("memorystore: only a deleted tombstone can be restored")
	}
	now := time.Now().UTC()
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET status = 'active', deleted_at = NULL, freshness_status = 'fresh',
    last_verified_at = ?, revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = 'deleted'
`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), record.ID, s.cfg.ProjectKey, record.Revision)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: deleted record %s changed concurrently", record.ID)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_suppressions
WHERE source_memory_id = ? AND project_key = ? AND reason = 'deleted'
`, record.ID, s.cfg.ProjectKey); err != nil {
		return Record{}, err
	}
	if err := insertEventTx(ctx, tx, record.ID, "restored", "admin", record.SourceSessionID, record.Revision+1, map[string]interface{}{
		"reason": "explicit_restore",
	}); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(ctx, id)
}

func (s *Store) GetSummary(ctx context.Context, id string) (Summary, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Summary{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, scope, project_key, summary_kind, revision, status, freshness_status,
       summary, summary_tokens, source_count, input_watermark, generated_at,
       model, content_hash, record_ids_json, metadata_json
FROM memory_summaries
WHERE id = ? AND project_key = ?
`, id, s.cfg.ProjectKey)
	return scanSummary(row)
}

func (s *Store) ActivateSummary(ctx context.Context, id string) (Summary, error) {
	summary, err := s.GetSummary(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return Summary{}, err
	}
	valid, err := s.validateSummaryGate(ctx, db, summary, time.Now().UTC())
	if err != nil {
		return Summary{}, err
	}
	if !valid {
		return Summary{}, fmt.Errorf("memorystore: summary gate rejected revision %d", summary.Revision)
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_summaries SET status = 'superseded'
WHERE scope = ? AND project_key = ? AND summary_kind = ? AND status = 'active'
`, string(summary.Scope), summary.ProjectKey, summary.SummaryKind); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_summaries SET status = 'active', freshness_status = 'fresh'
WHERE id = ? AND project_key = ?
`, summary.ID, s.cfg.ProjectKey); err != nil {
		return Summary{}, err
	}
	if err := insertEventTx(ctx, tx, summary.ID, "summary_activated", "admin", "", summary.Revision, map[string]interface{}{
		"summary_kind": summary.SummaryKind,
	}); err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, err
	}
	summary.Status = "active"
	summary.Freshness = FreshnessFresh
	if err := s.writeSummaryArtifacts(summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *Store) SweepFreshness(ctx context.Context, now time.Time) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
	SELECT id, COALESCE(source_session_id, ''), revision
FROM memory_entries
WHERE project_key = ? AND status = 'active' AND freshness_status = 'fresh'
  AND revalidate_after IS NOT NULL AND revalidate_after != '' AND revalidate_after <= ?
`, s.cfg.ProjectKey, now.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	type staleRecord struct {
		id, session string
		revision    int64
	}
	var stale []staleRecord
	for rows.Next() {
		var item staleRecord
		if err := rows.Scan(&item.id, &item.session, &item.revision); err != nil {
			_ = rows.Close()
			return 0, err
		}
		stale = append(stale, item)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, item := range stale {
		if _, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET freshness_status = 'stale', stale_reason = 'revalidate_due',
    revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ?
`, now.Format(time.RFC3339Nano), item.id, s.cfg.ProjectKey); err != nil {
			return 0, err
		}
		if err := insertEventTx(ctx, tx, item.id, "stale_marked", "system", item.session, item.revision+1, map[string]interface{}{
			"reason": "revalidate_due",
		}); err != nil {
			return 0, err
		}
		if err := s.markSummariesStaleForRecordTx(ctx, tx, item.id, "freshness_sweep"); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, memory_id, event_type, actor, COALESCE(session_id, ''),
       revision, payload_json, created_at
FROM memory_events
WHERE memory_id IN (SELECT id FROM memory_entries WHERE project_key = ?)
   OR memory_id IN (SELECT id FROM memory_summaries WHERE project_key = ?)
ORDER BY created_at DESC LIMIT ?
`, s.cfg.ProjectKey, s.cfg.ProjectKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditEvent, 0, limit)
	for rows.Next() {
		var (
			event              AuditEvent
			id, memoryID       sql.NullString
			eventType, actor   sql.NullString
			sessionID, payload sql.NullString
			revision           sql.NullInt64
			createdAt          sql.NullString
		)
		if err := rows.Scan(&id, &memoryID, &eventType, &actor, &sessionID, &revision, &payload, &createdAt); err != nil {
			return nil, err
		}
		event = AuditEvent{
			ID: id.String, MemoryID: memoryID.String, EventType: eventType.String,
			Actor: actor.String, SessionID: sessionID.String, Revision: revision.Int64,
			CreatedAt: parseTime(createdAt.String),
		}
		_ = json.Unmarshal([]byte(payload.String), &event.Payload)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) ExportJSONL(ctx context.Context) (string, error) {
	db, err := s.ensureV2()
	if err != nil {
		return "", err
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json
FROM memory_entries WHERE project_key = ? ORDER BY created_at
`, s.cfg.ProjectKey)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return "", err
		}
		line, _ := json.Marshal(map[string]interface{}{"type": "record", "record": record})
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	_ = rows.Close()
	summaries, err := s.ListSummaries(ctx, 1000)
	if err != nil {
		return "", err
	}
	for _, summary := range summaries {
		line, _ := json.Marshal(map[string]interface{}{"type": "summary", "summary": summary})
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func (s *Store) ExportMarkdown(ctx context.Context) (string, error) {
	db, err := s.ensureV2()
	if err != nil {
		return "", err
	}
	summaries, err := s.ListSummaries(ctx, 1000)
	if err != nil {
		return "", err
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json
FROM memory_entries WHERE project_key = ? ORDER BY created_at, id
`, s.cfg.ProjectKey)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	records := make([]Record, 0, 64)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return "", scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Agent Memory Export\n\n")
	for _, summary := range summaries {
		b.WriteString(fmt.Sprintf("## Summary %s (revision %d, %s)\n\n", summary.ID, summary.Revision, summary.Status))
		b.WriteString(strings.TrimSpace(summary.Text))
		b.WriteString("\n\n")
	}
	b.WriteString("## Records\n\n")
	for _, record := range records {
		b.WriteString(fmt.Sprintf("- [%s/%s/%s] `%s` %s\n",
			record.Kind, record.Status, record.Freshness, record.ID, strings.TrimSpace(record.Text)))
	}
	return b.String(), nil
}

func (s *Store) Reset(ctx context.Context) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	projectKey := s.cfg.ProjectKey
	statements := []struct {
		query string
		args  []interface{}
	}{
		{`DELETE FROM memory_summary_items
		  WHERE summary_id IN (SELECT id FROM memory_summaries WHERE project_key = ?)`, []interface{}{projectKey}},
		{`DELETE FROM memory_usage
		  WHERE memory_id IN (SELECT id FROM memory_entries WHERE project_key = ?)`, []interface{}{projectKey}},
		{`DELETE FROM memory_recall_traces WHERE project_key = ?`, []interface{}{projectKey}},
		{`DELETE FROM memory_suppressions WHERE project_key = ?`, []interface{}{projectKey}},
		{`DELETE FROM memory_session_modes WHERE project_key = ?`, []interface{}{projectKey}},
		{`DELETE FROM memory_jobs WHERE project_key = ?`, []interface{}{projectKey}},
		{`DELETE FROM memory_sources
		  WHERE memory_id IN (SELECT id FROM memory_entries WHERE project_key = ?)`, []interface{}{projectKey}},
		{`DELETE FROM memory_events
		  WHERE memory_id IN (SELECT id FROM memory_entries WHERE project_key = ?)
		     OR memory_id IN (SELECT id FROM memory_summaries WHERE project_key = ?)`, []interface{}{projectKey, projectKey}},
		{`DELETE FROM memory_summaries WHERE project_key = ?`, []interface{}{projectKey}},
		{`DELETE FROM memory_entries WHERE project_key = ?`, []interface{}{projectKey}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.clearSummaryArtifacts()
}

func scanSummary(row interface{ Scan(...interface{}) error }) (Summary, error) {
	var (
		summary                                          Summary
		id, scope, projectKey, kind, status, freshness   sql.NullString
		text, watermark, generatedAt, model, contentHash sql.NullString
		recordIDsJSON, metadataJSON                      sql.NullString
		revision, tokens, sourceCount                    sql.NullInt64
	)
	if err := row.Scan(&id, &scope, &projectKey, &kind, &revision, &status, &freshness,
		&text, &tokens, &sourceCount, &watermark, &generatedAt, &model,
		&contentHash, &recordIDsJSON, &metadataJSON); err != nil {
		return Summary{}, err
	}
	summary = Summary{
		ID: id.String, Scope: Scope(scope.String), ProjectKey: projectKey.String,
		SummaryKind: kind.String, Revision: revision.Int64, Status: status.String,
		Freshness: Freshness(freshness.String), Text: text.String,
		TokenEstimate: int(tokens.Int64), SourceCount: int(sourceCount.Int64),
		InputWatermark: watermark.String, GeneratedAt: parseTime(generatedAt.String),
		Model: model.String, ContentHash: contentHash.String,
	}
	_ = json.Unmarshal([]byte(recordIDsJSON.String), &summary.RecordIDs)
	_ = json.Unmarshal([]byte(metadataJSON.String), &summary.Metadata)
	return summary, nil
}
