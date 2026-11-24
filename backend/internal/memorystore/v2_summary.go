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

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
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
			sensitivity                    string
			validUntil, deletedAt          string
			revision                       int64
			pinned                         int
			sourceKind                     string
		)
		if err := db.QueryRowContext(ctx, `
SELECT status, freshness_status, content_hash, revision, pinned, source_kind, sensitivity,
       COALESCE(valid_until, ''), COALESCE(deleted_at, '')
FROM memory_entries WHERE id = ?
`, id).Scan(&status, &freshness, &contentHash, &revision, &pinned, &sourceKind, &sensitivity, &validUntil, &deletedAt); err != nil {
			return Summary{}, err
		}
		if status != string(StatusActive) || freshness != string(FreshnessFresh) || deletedAt != "" {
			return Summary{}, fmt.Errorf("memorystore: summary item %s is not active+fresh", id)
		}
		if until := parseTime(validUntil); !until.IsZero() && !until.After(time.Now().UTC()) {
			return Summary{}, fmt.Errorf("memorystore: summary item %s is expired", id)
		}
		if pinned != 0 || strings.EqualFold(sourceKind, "manual") {
			return Summary{}, fmt.Errorf("memorystore: summary item %s is pinned/manual and must be rendered separately", id)
		}
		if normalizeSensitivity(sensitivity) == "private" {
			return Summary{}, fmt.Errorf("memorystore: summary item %s is private and cannot be compiled into an injectable summary", id)
		}
		states = append(states, summaryItemState{ID: id, Revision: revision, Hash: contentHash, Status: status, Freshness: freshness})
	}
	if summary.Scope == "" {
		summary.Scope = ScopeProject
	}
	if summary.ProjectKey == "" {
		summary.ProjectKey = s.cfg.ProjectKey
	}
	summaryScopeKind := ScopeProject
	summaryScopeKey := summary.ProjectKey
	tenantID := s.tenantID()
	if summary.SummaryKind == "" {
		summary.SummaryKind = "long_term"
	}
	if summary.Revision <= 0 {
		var revision int64
		// Revision allocation stays project-wide because the v1 schema still has
		// UNIQUE(scope, project_key, summary_kind, revision). Reads and writes
		// below remain tenant-scoped; a shared revision counter is not a data
		// visibility boundary.
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
	if rule := memoryredact.DetectSecret(summary.Text); rule != "" {
		return Summary{}, fmt.Errorf("memorystore: refusing to store secret-shaped summary (%s)", rule)
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
	if err := validateMaintenanceLeaseTx(ctx, tx, summary.ProjectKey); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_summaries SET status = 'superseded'
WHERE tenant_id = ? AND project_key = ? AND scope_kind = ? AND scope_key = ? AND summary_kind = ? AND status = 'active'
`, tenantID, summary.ProjectKey, string(summaryScopeKind), summaryScopeKey, summary.SummaryKind); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_summaries (
    id, scope, project_key, summary_kind, revision, status, freshness_status,
    summary, summary_tokens, source_count, input_watermark, generated_at,
    model, content_hash, record_ids_json, metadata_json,
    tenant_id, scope_kind, scope_key
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, summary.ID, string(summary.Scope), summary.ProjectKey, summary.SummaryKind,
		summary.Revision, summary.Status, string(summary.Freshness), summary.Text,
		summary.TokenEstimate, summary.SourceCount, summary.InputWatermark,
		summary.GeneratedAt.UTC().Format(time.RFC3339Nano), summary.Model,
		summary.ContentHash, string(recordIDs), string(metadata),
		tenantID, string(summaryScopeKind), summaryScopeKey); err != nil {
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
	if err := s.writeSummaryArtifactsIfCurrent(ctx, summary); err != nil {
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

func (s *Store) clearSummaryArtifacts() error {
	if s == nil {
		return nil
	}
	for _, name := range []string{"memory_summary.md", "MEMORY.md"} {
		if err := os.Remove(filepath.Join(s.root, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// writeSummaryArtifactsIfCurrent serializes materialized-view writes with
// memory-store writers. The DB transaction both fences maintenance ownership
// and confirms this summary is still active before publishing its files.
func (s *Store) writeSummaryArtifactsIfCurrent(ctx context.Context, summary Summary) error {
	if s == nil {
		return nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, summary.ProjectKey); err != nil {
		return err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_summaries
WHERE id = ? AND tenant_id = ? AND project_key = ? AND scope_kind = 'project' AND scope_key = ?
  AND status = 'active' AND freshness_status = 'fresh'
`, summary.ID, s.tenantID(), summary.ProjectKey, summary.ProjectKey).Scan(&current); err != nil {
		return err
	}
	if current == 0 {
		return tx.Commit()
	}
	if err := s.writeSummaryArtifacts(summary); err != nil {
		return err
	}
	return tx.Commit()
}

// clearSummaryArtifactsIfNoFreshActiveSummary avoids an invalidation or
// retirement racing with a newer summary publication.
func (s *Store) clearSummaryArtifactsIfNoFreshActiveSummary(ctx context.Context, projectKey string) error {
	if s == nil {
		return nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, projectKey); err != nil {
		return err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_summaries
WHERE tenant_id = ? AND project_key = ? AND scope_kind = 'project' AND scope_key = ?
  AND status = 'active' AND freshness_status = 'fresh'
`, s.tenantID(), projectKey, projectKey).Scan(&current); err != nil {
		return err
	}
	if current == 0 {
		if err := s.clearSummaryArtifacts(); err != nil {
			return err
		}
	}
	return tx.Commit()
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
WHERE tenant_id = ? AND project_key = ? AND scope_kind = 'project' AND scope_key = ?
ORDER BY revision DESC LIMIT ?
`, s.tenantID(), s.cfg.ProjectKey, s.cfg.ProjectKey, limit)
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

// GatedActiveSummary applies the same provenance/freshness gate used by
// injection. Summary reuse must not trust an active status alone.
func (s *Store) GatedActiveSummary(ctx context.Context, now time.Time) (*Summary, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.loadGatedActiveSummary(ctx, db, now)
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
WHERE tenant_id = ? AND project_key = ? AND scope_kind = 'project' AND scope_key = ?
  AND summary_kind = 'long_term' AND status = 'active'
ORDER BY revision DESC LIMIT 1
`, s.tenantID(), s.cfg.ProjectKey, s.cfg.ProjectKey).Scan(
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
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_summaries SET status = 'superseded', freshness_status = 'stale'
WHERE tenant_id = ? AND project_key = ? AND scope_kind = 'project' AND scope_key = ?
  AND summary_kind = 'long_term' AND status = 'active'
`, s.tenantID(), s.cfg.ProjectKey, s.cfg.ProjectKey)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if affected > 0 {
		if err := s.clearSummaryArtifactsIfNoFreshActiveSummary(ctx, s.cfg.ProjectKey); err != nil {
			return true, err
		}
	}
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
WHERE tenant_id = ? AND project_key = ? AND scope_kind = 'project' AND scope_key = ?
  AND status = 'active' AND freshness_status = 'stale'
ORDER BY revision DESC
`, s.tenantID(), s.cfg.ProjectKey, s.cfg.ProjectKey)
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
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return 0, err
	}
	inserted := 0
	for _, summary := range summaries {
		affected, err := enqueueSummaryRebuild(ctx, tx, s.tenantID(), summary.id+":"+summary.watermark,
			summary.scope, summary.projectKey, summary.watermark)
		if err != nil {
			return inserted, err
		}
		inserted += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, err
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
