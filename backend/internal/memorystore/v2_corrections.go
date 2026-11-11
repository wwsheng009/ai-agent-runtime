package memorystore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// CorrectRecord creates a new correction revision, supersedes the old record,
// and writes a suppression so the corrected content cannot be recreated by
// extraction or manual append. The operation is atomic.
func (s *Store) CorrectRecord(ctx context.Context, id, text string) (Record, error) {
	old, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if old.Status != StatusActive {
		return Record{}, fmt.Errorf("memorystore: only active records can be corrected")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Record{}, fmt.Errorf("memorystore: correction text is required")
	}
	if normalizeMemoryText(text) == old.NormalizedText {
		return old, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	record := Record{
		ID: "mem_" + uuid.NewString(), Scope: old.Scope, ProjectKey: old.ProjectKey,
		Kind: "correction", Status: StatusActive, Text: text,
		NormalizedText: normalizeMemoryText(text), Tags: append([]string(nil), old.Tags...),
		Confidence: 1.0, Sensitivity: old.Sensitivity, Pinned: old.Pinned, RetentionClass: old.RetentionClass,
		SourceKind: "manual", SourceSessionID: old.SourceSessionID,
		SourceRefs:   []string{"correction:" + old.ID},
		ContentHash:  memoryContentHash(string(old.Scope), old.ProjectKey, "correction", text),
		SupersedesID: old.ID, Revision: old.Revision + 1,
		CreatedAt: now, UpdatedAt: now, Freshness: FreshnessFresh, LastVerifiedAt: now,
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.insertRecordSQLTx(ctx, tx, record, "user", old.SourceSessionID); err != nil {
		return Record{}, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET status = 'superseded', updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = 'active'
`, now.Format(time.RFC3339Nano), old.ID, s.cfg.ProjectKey, old.Revision)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: record %s changed concurrently", old.ID)
	}
	if err := insertEventTx(ctx, tx, old.ID, "superseded", "user", old.SourceSessionID, old.Revision, map[string]interface{}{
		"reason": "corrected", "superseded_by": record.ID,
	}); err != nil {
		return Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_suppressions (
    id, scope, project_key, content_hash, reason, source_memory_id, created_at, metadata_json
) VALUES (?, ?, ?, ?, 'corrected', ?, ?, '{}')
`, "sup_"+uuid.NewString(), string(old.Scope), old.ProjectKey, old.ContentHash, old.ID,
		now.Format(time.RFC3339Nano)); err != nil {
		return Record{}, err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, old.ID, "record_corrected"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}
