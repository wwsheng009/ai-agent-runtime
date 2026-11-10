package memorystore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// CompileSummary creates a new active summary revision from a fixed item set.
// The input items are validated as active+fresh; the existing active revision
// is superseded in the same transaction. Markdown files are materialized
// views and can be regenerated from the DB.
func (s *Store) CompileSummary(ctx context.Context, summary Summary, itemIDs []string) (Summary, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Summary{}, err
	}
	itemIDs = uniqueStrings(itemIDs)
	if len(itemIDs) == 0 {
		return Summary{}, fmt.Errorf("memorystore: summary requires at least one item")
	}
	states := make([]summaryItemState, 0, len(itemIDs))
	for _, id := range itemIDs {
		var (
			status, freshness, contentHash string
			revision                       int64
		)
		if err := db.QueryRowContext(ctx, `
SELECT status, freshness_status, content_hash, revision FROM memory_entries WHERE id = ?
`, id).Scan(&status, &freshness, &contentHash, &revision); err != nil {
			return Summary{}, err
		}
		if status != string(StatusActive) || freshness != string(FreshnessFresh) {
			return Summary{}, fmt.Errorf("memorystore: summary item %s is not active+fresh", id)
		}
		states = append(states, summaryItemState{ID: id, Revision: revision, Hash: contentHash, Status: status, Freshness: freshness})
	}
	if summary.Scope == "" {
		summary.Scope = ScopeProject
	}
	if summary.ProjectKey == "" {
		summary.ProjectKey = s.cfg.ProjectKey
	}
	if summary.SummaryKind == "" {
		summary.SummaryKind = "long_term"
	}
	if summary.Revision <= 0 {
		var revision int64
		if err := db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(revision), 0) + 1 FROM memory_summaries
WHERE scope = ? AND project_key = ? AND summary_kind = ?
`, string(summary.Scope), summary.ProjectKey, summary.SummaryKind).Scan(&revision); err != nil {
			return Summary{}, err
		}
		summary.Revision = revision
	}
	if summary.ID == "" {
		summary.ID = "sum_" + uuid.NewString()
	}
	summary.Status = "active"
	summary.Freshness = FreshnessFresh
	summary.InputWatermark = summaryInputWatermark(states)
	if summary.Text = strings.TrimSpace(summary.Text); summary.Text == "" {
		return Summary{}, fmt.Errorf("memorystore: summary text is required")
	}
	if summary.TokenEstimate <= 0 {
		summary.TokenEstimate = EstimateTokens(summary.Text)
	}
	summary.SourceCount = len(itemIDs)
	if summary.GeneratedAt.IsZero() {
		summary.GeneratedAt = time.Now().UTC()
	}
	sum := sha256.Sum256([]byte(summary.Text))
	summary.ContentHash = hex.EncodeToString(sum[:])
	recordIDs, _ := json.Marshal(itemIDs)
	metadata, _ := json.Marshal(summary.Metadata)
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
INSERT INTO memory_summaries (
    id, scope, project_key, summary_kind, revision, status, freshness_status,
    summary, summary_tokens, source_count, input_watermark, generated_at,
    model, content_hash, record_ids_json, metadata_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, summary.ID, string(summary.Scope), summary.ProjectKey, summary.SummaryKind,
		summary.Revision, summary.Status, string(summary.Freshness), summary.Text,
		summary.TokenEstimate, summary.SourceCount, summary.InputWatermark,
		summary.GeneratedAt.UTC().Format(time.RFC3339Nano), summary.Model,
		summary.ContentHash, string(recordIDs), string(metadata)); err != nil {
		return Summary{}, err
	}
	for rank, id := range itemIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_summary_items (summary_id, memory_id, rank, token_estimate, included_reason)
VALUES (?, ?, ?, 0, 'summary_compile')
`, summary.ID, id, rank); err != nil {
			return Summary{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, err
	}
	if err := s.writeSummaryArtifacts(summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *Store) writeSummaryArtifacts(summary Summary) error {
	if s == nil {
		return nil
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	body := summary.Text
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	files := map[string]string{
		"memory_summary.md": body,
		"MEMORY.md":         body,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(s.root, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListSummaries(ctx context.Context, limit int) ([]Summary, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, summary_kind, revision, status, freshness_status,
       summary, summary_tokens, source_count, input_watermark, generated_at,
       COALESCE(model, ''), content_hash, record_ids_json, metadata_json
FROM memory_summaries
WHERE project_key = ?
ORDER BY revision DESC LIMIT ?
`, s.cfg.ProjectKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Summary, 0, limit)
	for rows.Next() {
		var (
			summary                     Summary
			id, scope, project, kind    sql.NullString
			status, freshness, text     sql.NullString
			watermark, generated, model sql.NullString
			contentHash, idsJSON        sql.NullString
			metadataJSON                sql.NullString
			revision, tokens, source    sql.NullInt64
		)
		if err := rows.Scan(&id, &scope, &project, &kind, &revision, &status, &freshness,
			&text, &tokens, &source, &watermark, &generated, &model, &contentHash,
			&idsJSON, &metadataJSON); err != nil {
			return nil, err
		}
		summary = Summary{
			ID: id.String, Scope: Scope(scope.String), ProjectKey: project.String,
			SummaryKind: kind.String, Revision: revision.Int64, Status: status.String,
			Freshness: Freshness(freshness.String), Text: text.String,
			TokenEstimate: int(tokens.Int64), SourceCount: int(source.Int64),
			InputWatermark: watermark.String, GeneratedAt: parseTime(generated.String),
			Model: model.String, ContentHash: contentHash.String,
		}
		_ = json.Unmarshal([]byte(idsJSON.String), &summary.RecordIDs)
		_ = json.Unmarshal([]byte(metadataJSON.String), &summary.Metadata)
		out = append(out, summary)
	}
	return out, rows.Err()
}

// ActiveSummary returns the current active long-term summary without applying
// the injection gate. Callers that render or inject it must still validate the
// gate through SelectForInjectV2.
func (s *Store) ActiveSummary(ctx context.Context) (*Summary, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	var (
		summary                                               Summary
		summaryID, scope, projectKey, kind, status, freshness sql.NullString
		text, watermark, generatedAt, model, contentHash      sql.NullString
		recordIDsJSON, metadataJSON                           sql.NullString
		revision, tokens, sourceCount                         int64
	)
	err = db.QueryRowContext(ctx, `
SELECT id, scope, project_key, summary_kind, revision, status, freshness_status,
       summary, summary_tokens, source_count, input_watermark, generated_at,
       model, content_hash, record_ids_json, metadata_json
FROM memory_summaries
WHERE project_key = ? AND summary_kind = 'long_term' AND status = 'active'
ORDER BY revision DESC LIMIT 1
`, s.cfg.ProjectKey).Scan(
		&summaryID, &scope, &projectKey, &kind, &revision, &status, &freshness,
		&text, &tokens, &sourceCount, &watermark, &generatedAt, &model,
		&contentHash, &recordIDsJSON, &metadataJSON,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summary = Summary{
		ID: summaryID.String, Scope: Scope(scope.String), ProjectKey: projectKey.String,
		SummaryKind: kind.String, Revision: revision, Status: status.String,
		Freshness: Freshness(freshness.String), Text: text.String,
		TokenEstimate: int(tokens), SourceCount: int(sourceCount),
		InputWatermark: watermark.String, GeneratedAt: parseTime(generatedAt.String),
		Model: model.String, ContentHash: contentHash.String,
	}
	_ = json.Unmarshal([]byte(recordIDsJSON.String), &summary.RecordIDs)
	if strings.TrimSpace(metadataJSON.String) != "" {
		_ = json.Unmarshal([]byte(metadataJSON.String), &summary.Metadata)
	}
	return &summary, nil
}

// RetireActiveSummary removes an unusable active summary when no replacement
// can be compiled (for example, all source records were deleted).
func (s *Store) RetireActiveSummary(ctx context.Context) (bool, error) {
	db, err := s.ensureV2()
	if err != nil {
		return false, err
	}
	res, err := db.ExecContext(ctx, `
UPDATE memory_summaries SET status = 'superseded', freshness_status = 'stale'
WHERE project_key = ? AND summary_kind = 'long_term' AND status = 'active'
`, s.cfg.ProjectKey)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// EnqueueStaleSummaryRebuilds is the crash-recovery path for summary
// invalidation. It is idempotent by (summary_id, input_watermark).
func (s *Store) EnqueueStaleSummaryRebuilds(ctx context.Context) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, input_watermark
FROM memory_summaries
WHERE project_key = ? AND status = 'active' AND freshness_status = 'stale'
ORDER BY revision DESC
`, s.cfg.ProjectKey)
	if err != nil {
		return 0, err
	}
	type staleSummary struct {
		id, scope, projectKey, watermark string
	}
	var summaries []staleSummary
	for rows.Next() {
		var summary staleSummary
		if err := rows.Scan(&summary.id, &summary.scope, &summary.projectKey, &summary.watermark); err != nil {
			_ = rows.Close()
			return 0, err
		}
		summaries = append(summaries, summary)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	inserted := 0
	for _, summary := range summaries {
		res, err := db.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, priority, attempt,
    max_attempts, input_watermark, created_at, updated_at
) VALUES (?, 'summary_rebuild', ?, ?, ?, 'pending', 0, 0, 3, ?, ?, ?)
`, "job_"+uuid.NewString(), summary.id+":"+summary.watermark,
			summary.scope, summary.projectKey, summary.watermark, nowText(), nowText())
		if err != nil {
			return inserted, err
		}
		affected, _ := res.RowsAffected()
		inserted += int(affected)
	}
	return inserted, nil
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
