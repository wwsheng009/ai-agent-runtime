package memorystore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

func nowText() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func nullableString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func nullableTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableInt64(value int64) interface{} {
	if value == 0 {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "constraint failed")
}

func normalizeMemoryText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func normalizeSensitivity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "private":
		return "private"
	default:
		return "internal"
	}
}

func memoryContentHash(scope, projectKey, kind, text string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + projectKey + "\x00" + kind + "\x00" + normalizeMemoryText(text)))
	return hex.EncodeToString(sum[:])
}

func recordToNote(record Record) Note {
	return Note{
		ID:        record.ID,
		Text:      record.Text,
		Tags:      append([]string(nil), record.Tags...),
		Source:    record.SourceKind,
		SessionID: record.SourceSessionID,
		CreatedAt: record.CreatedAt,
	}
}

func recordByID(records []Record, id string) Record {
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	return Record{}
}

// scoreRecordForRecall is the v2 selector's deterministic ranking function.
// Query match is a hard gate for non-empty queries; recency, usage, confidence,
// and semantic-kind boosts only reorder records that actually matched.
func scoreRecordForRecall(record Record, tokens []string, rawQuery string, now time.Time) float64 {
	text := strings.ToLower(strings.TrimSpace(record.NormalizedText))
	if text == "" {
		text = strings.ToLower(record.Text)
	}
	tagBlob := strings.ToLower(strings.Join(record.Tags, " "))
	kind := strings.ToLower(strings.TrimSpace(record.Kind))
	sourceKind := strings.ToLower(strings.TrimSpace(record.SourceKind))
	raw := strings.ToLower(strings.TrimSpace(rawQuery))

	match := 0.0
	if raw == "" {
		match = 1
	} else {
		if strings.Contains(text, raw) {
			match += 5
		}
		for _, token := range tokens {
			if strings.Contains(text, token) {
				match += 2
			}
			if strings.Contains(tagBlob, token) {
				match += 3
			}
			if kind == token {
				match += 0.75
			}
			if sourceKind == token {
				match += 0.5
			}
		}
		if match <= 0 {
			return 0
		}
	}

	score := match
	if kind == "correction" {
		score += 1.0
	} else if kind == "decision" || kind == "preference" {
		score += 0.35
	}
	score += 0.4 * clampUnit(record.Confidence)

	lastActivity := record.UpdatedAt
	if lastActivity.IsZero() {
		lastActivity = record.CreatedAt
	}
	if !lastActivity.IsZero() {
		ageDays := now.Sub(lastActivity).Hours() / 24
		score += 0.8 * math.Exp(-math.Max(0, ageDays)/45)
	}
	if record.UseCount > 0 {
		usedAt := record.LastUsedAt
		if usedAt.IsZero() {
			usedAt = lastActivity
		}
		usageAgeDays := 0.0
		if !usedAt.IsZero() {
			usageAgeDays = math.Max(0, now.Sub(usedAt).Hours()/24)
		}
		score += 0.65 * math.Log1p(float64(record.UseCount)) * math.Exp(-usageAgeDays/30)
	}
	return score
}

func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func recallDedupKey(record Record) string {
	var cleaned strings.Builder
	for _, r := range strings.ToLower(record.Text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cleaned.WriteRune(r)
			continue
		}
		cleaned.WriteByte(' ')
	}
	text := strings.Join(strings.Fields(cleaned.String()), " ")
	if text == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(record.Kind)) + "\x00" + text
}

// diversifyRecallOrder prevents one kind from filling the first recall slots
// when several kinds matched. It only reorders candidates; it never drops a
// record or bypasses the token-budget loop.
func diversifyRecallOrder(candidates []SearchHit, records []Record, limit int) []SearchHit {
	if limit <= 1 || len(candidates) < 2 {
		return candidates
	}
	perKindCap := limit / 2
	if perKindCap < 1 {
		perKindCap = 1
	}
	counts := make(map[string]int)
	ordered := make([]SearchHit, 0, len(candidates))
	deferred := make([]SearchHit, 0, len(candidates))
	for _, hit := range candidates {
		record := recordByID(records, hit.Note.ID)
		kind := strings.ToLower(strings.TrimSpace(record.Kind))
		if kind == "" {
			kind = "memory"
		}
		if counts[kind] < perKindCap {
			ordered = append(ordered, hit)
			counts[kind]++
			continue
		}
		deferred = append(deferred, hit)
	}
	return append(ordered, deferred...)
}

const memoryRecordTokenOverhead = 16

// fitRecordToBudget truncates a single oversized record while preserving its
// id, kind, tags, and source metadata. The formatted prompt line adds kind/id
// decoration, so reserve a conservative fixed overhead instead of counting
// only the body text.
func fitRecordToBudget(record Record, maxTokens int) (Record, bool) {
	if maxTokens <= memoryRecordTokenOverhead {
		return Record{}, false
	}
	if EstimateTokens(record.Text)+memoryRecordTokenOverhead <= maxTokens {
		return record, true
	}
	body := truncateTextToTokenBudget(record.Text, maxTokens-memoryRecordTokenOverhead)
	if strings.TrimSpace(body) == "" {
		return Record{}, false
	}
	record.Text = body
	return record, true
}

func truncateTextToTokenBudget(text string, maxTokens int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxTokens <= 0 {
		return ""
	}
	if EstimateTokens(text) <= maxTokens {
		return text
	}
	runes := []rune(text)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		candidate := strings.TrimSpace(string(runes[:mid])) + "…"
		if EstimateTokens(candidate) <= maxTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 {
		return ""
	}
	return strings.TrimSpace(string(runes[:low])) + "…"
}

func scanRecord(rows *sql.Rows) (Record, error) {
	return scanRecordColumns(rows)
}

type summaryItemState struct {
	ID        string
	Revision  int64
	Hash      string
	Status    string
	Freshness string
}

func summaryInputWatermark(items []summaryItemState) string {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	payload := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		payload = append(payload, map[string]interface{}{
			"id": item.ID, "revision": item.Revision, "content_hash": item.Hash,
			"status": item.Status, "freshness_status": item.Freshness,
		})
	}
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256(body)
	return "v1:" + hex.EncodeToString(sum[:])
}

func (s *Store) loadGatedActiveSummary(ctx context.Context, db *sql.DB, now time.Time) (*Summary, error) {
	var (
		summary                                               Summary
		summaryID, scope, projectKey, kind, status, freshness sql.NullString
		text, watermark, generatedAt, model, contentHash      sql.NullString
		recordIDsJSON, metadataJSON                           sql.NullString
		revision, tokens, sourceCount                         int64
	)
	err := db.QueryRowContext(ctx, `
SELECT id, scope, project_key, summary_kind, revision, status, freshness_status,
       summary, summary_tokens, source_count, input_watermark, generated_at,
       model, content_hash, record_ids_json, metadata_json
FROM memory_summaries
WHERE scope = ? AND project_key = ? AND summary_kind = 'long_term'
  AND status = 'active' AND freshness_status = 'fresh'
ORDER BY revision DESC LIMIT 1
`, string(ScopeProject), s.cfg.ProjectKey).Scan(
		&summaryID, &scope, &projectKey, &kind, &revision, &status, &freshness,
		&text, &tokens, &sourceCount, &watermark, &generatedAt, &model,
		&contentHash, &recordIDsJSON, &metadataJSON,
	)
	if errorsIsNoRows(err) {
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
	if valid, err := s.validateSummaryGate(ctx, db, summary, now); err != nil {
		return nil, err
	} else if !valid {
		return nil, nil
	}
	return &summary, nil
}

func (s *Store) validateSummaryGate(ctx context.Context, db *sql.DB, summary Summary, now time.Time) (bool, error) {
	if rule := memoryredact.DetectSecret(summary.Text); rule != "" {
		_ = s.invalidateSummary(ctx, db, summary, "summary_secret:"+rule)
		return false, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT memory_id FROM memory_summary_items WHERE summary_id = ? ORDER BY memory_id
`, summary.ID)
	if err != nil {
		return false, err
	}
	itemIDs := make([]string, 0, len(summary.RecordIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return false, err
		}
		itemIDs = append(itemIDs, id)
	}
	_ = rows.Close()
	if len(itemIDs) != len(summary.RecordIDs) {
		_ = s.invalidateSummary(ctx, db, summary, "items_missing")
		return false, nil
	}
	expected := append([]string(nil), summary.RecordIDs...)
	sort.Strings(expected)
	sort.Strings(itemIDs)
	for i := range expected {
		if expected[i] != itemIDs[i] {
			_ = s.invalidateSummary(ctx, db, summary, "items_mismatch")
			return false, nil
		}
	}
	if len(itemIDs) == 0 {
		return false, nil
	}
	states := make([]summaryItemState, 0, len(itemIDs))
	for _, id := range itemIDs {
		var (
			status, freshness, contentHash, validUntil, deletedAt string
			sourceKind, sensitivity                               string
			revision                                              int64
			pinned                                                int
		)
		err := db.QueryRowContext(ctx, `
SELECT status, freshness_status, content_hash, revision, COALESCE(valid_until, ''),
       COALESCE(deleted_at, ''), pinned, source_kind, sensitivity
FROM memory_entries WHERE id = ?
`, id).Scan(&status, &freshness, &contentHash, &revision, &validUntil, &deletedAt, &pinned, &sourceKind, &sensitivity)
		if errorsIsNoRows(err) {
			_ = s.invalidateSummary(ctx, db, summary, "item_missing")
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if status != string(StatusActive) || freshness != string(FreshnessFresh) || deletedAt != "" {
			_ = s.invalidateSummary(ctx, db, summary, "item_not_active_fresh")
			return false, nil
		}
		// Pinned/manual records have their own stable budget and rendering. A
		// summary that also contains them would duplicate the same claim in the
		// prompt, so fail the gate and let the rebuild path exclude them.
		if pinned != 0 || strings.EqualFold(sourceKind, "manual") {
			_ = s.invalidateSummary(ctx, db, summary, "item_stable")
			return false, nil
		}
		if normalizeSensitivity(sensitivity) == "private" {
			_ = s.invalidateSummary(ctx, db, summary, "item_private")
			return false, nil
		}
		if validUntil != "" {
			if until := parseTime(validUntil); !until.IsZero() && !until.After(now) {
				_ = s.invalidateSummary(ctx, db, summary, "item_expired")
				return false, nil
			}
		}
		states = append(states, summaryItemState{ID: id, Revision: revision, Hash: contentHash, Status: status, Freshness: freshness})
	}
	if summary.InputWatermark != "" && summary.InputWatermark != summaryInputWatermark(states) {
		_ = s.invalidateSummary(ctx, db, summary, "watermark_mismatch")
		return false, nil
	}
	return true, nil
}

func (s *Store) invalidateSummary(ctx context.Context, db *sql.DB, summary Summary, reason string) error {
	if summary.ID == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, summary.ProjectKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_summaries SET freshness_status = 'stale' WHERE id = ? AND status = 'active'`, summary.ID); err != nil {
		return err
	}
	jobKey := summary.ID + ":" + summary.InputWatermark
	if _, err := enqueueSummaryRebuild(ctx, tx, jobKey, string(summary.Scope), summary.ProjectKey, summary.InputWatermark); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.clearSummaryArtifactsIfNoFreshActiveSummary(ctx, summary.ProjectKey)
}

func errorsIsNoRows(err error) bool {
	return err == sql.ErrNoRows
}

func (s *Store) createSummary(ctx context.Context, summary Summary, itemIDs []string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
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
			return err
		}
		states = append(states, summaryItemState{ID: id, Revision: revision, Hash: contentHash, Status: status, Freshness: freshness})
	}
	summary.InputWatermark = summaryInputWatermark(states)
	if summary.ID == "" {
		summary.ID = "sum_" + uuid.NewString()
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
	if summary.Status == "" {
		summary.Status = "active"
	}
	if summary.Freshness == "" {
		summary.Freshness = FreshnessFresh
	}
	if summary.GeneratedAt.IsZero() {
		summary.GeneratedAt = time.Now().UTC()
	}
	contentHash := sha256.Sum256([]byte(summary.Text))
	if summary.ContentHash == "" {
		summary.ContentHash = hex.EncodeToString(contentHash[:])
	}
	recordIDs, _ := json.Marshal(append([]string(nil), itemIDs...))
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_summaries SET status = 'superseded'
WHERE scope = ? AND project_key = ? AND summary_kind = ? AND status = 'active'
`, string(summary.Scope), summary.ProjectKey, summary.SummaryKind); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_summaries (
    id, scope, project_key, summary_kind, revision, status, freshness_status,
    summary, summary_tokens, source_count, input_watermark, generated_at,
    model, content_hash, record_ids_json, metadata_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')
`, summary.ID, string(summary.Scope), summary.ProjectKey, summary.SummaryKind,
		summary.Revision, summary.Status, string(summary.Freshness), summary.Text,
		summary.TokenEstimate, len(itemIDs), summary.InputWatermark,
		summary.GeneratedAt.UTC().Format(time.RFC3339Nano), summary.Model,
		summary.ContentHash, string(recordIDs)); err != nil {
		return err
	}
	for rank, id := range itemIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_summary_items (summary_id, memory_id, rank, token_estimate, included_reason)
VALUES (?, ?, ?, ?, 'test_seed')
`, summary.ID, id, rank, 0); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) markSummariesStaleForRecordTx(ctx context.Context, tx *sql.Tx, recordID, reason string) error {
	rows, err := tx.QueryContext(ctx, `
SELECT s.id, s.input_watermark, s.scope, s.project_key
FROM memory_summaries s
JOIN memory_summary_items i ON i.summary_id = s.id
WHERE i.memory_id = ? AND s.status = 'active'
`, recordID)
	if err != nil {
		return err
	}
	type pending struct {
		id, watermark, scope, projectKey string
	}
	var summaries []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.watermark, &item.scope, &item.projectKey); err != nil {
			_ = rows.Close()
			return err
		}
		summaries = append(summaries, item)
	}
	_ = rows.Close()
	for _, summary := range summaries {
		if _, err := tx.ExecContext(ctx, `UPDATE memory_summaries SET freshness_status = 'stale' WHERE id = ?`, summary.id); err != nil {
			return err
		}
		if _, err := enqueueSummaryRebuild(ctx, tx, summary.id+":"+summary.watermark,
			summary.scope, summary.projectKey, summary.watermark); err != nil {
			return err
		}
	}
	return nil
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

func enqueueSummaryRebuild(ctx context.Context, execer sqlExecer, jobKey, scope, projectKey, watermark string) (int, error) {
	now := nowText()
	result, err := execer.ExecContext(ctx, `
INSERT INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, priority, attempt,
    max_attempts, input_watermark, created_at, updated_at
) VALUES (?, 'summary_rebuild', ?, ?, ?, 'pending', 0, 0, 3, ?, ?, ?)
ON CONFLICT(kind, scope, project_key, job_key) DO UPDATE SET
    status = 'pending',
    worker_id = NULL,
    lease_until = NULL,
    retry_at = NULL,
    finished_at = NULL,
    last_error = NULL,
    input_watermark = excluded.input_watermark,
    updated_at = excluded.updated_at
WHERE memory_jobs.status IN ('failed', 'succeeded', 'cancelled')
`, "job_"+uuid.NewString(), jobKey, scope, projectKey, watermark, now, now)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

func (s *Store) SelectForInjectV2(opts InjectOptions) (Selection, error) {
	return s.selectForInjectV2(opts)
}

func (s *Store) ListRecords(ctx context.Context, filter RecordFilter) (RecordPage, error) {
	if !s.v2Configured() {
		return s.listRecordsLegacy(filter)
	}
	db, err := s.ensureV2()
	if err != nil {
		return RecordPage{}, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if strings.TrimSpace(filter.Cursor) != "" {
		offset, _ = strconv.Atoi(strings.TrimSpace(filter.Cursor))
		if offset < 0 {
			offset = 0
		}
	}
	args := []interface{}{string(ScopeProject), s.cfg.ProjectKey}
	where := []string{"scope = ?", "project_key = ?"}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(filter.Status))
	} else {
		where = append(where, "status = 'active'")
	}
	if filter.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, filter.Kind)
	}
	if strings.TrimSpace(filter.Tag) != "" {
		where = append(where, "LOWER(tags_json) LIKE ?")
		args = append(args, "%\""+strings.ToLower(strings.TrimSpace(filter.Tag))+"\"%")
	}
	if strings.TrimSpace(filter.Query) != "" {
		where = append(where, "(LOWER(text) LIKE ? OR LOWER(tags_json) LIKE ?)")
		like := "%" + strings.ToLower(strings.TrimSpace(filter.Query)) + "%"
		args = append(args, like, like)
	}
	if strings.TrimSpace(filter.SourceSessionID) != "" {
		where = append(where, "source_session_id = ?")
		args = append(args, strings.TrimSpace(filter.SourceSessionID))
	}
	if filter.SourceSessionGeneration != nil {
		where = append(where, "source_session_generation = ?")
		args = append(args, *filter.SourceSessionGeneration)
	}
	query := `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_session_generation,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json
FROM memory_entries WHERE ` + strings.Join(where, " AND ") + `
ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return RecordPage{}, err
	}
	defer rows.Close()
	items := make([]Record, 0, limit)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return RecordPage{}, err
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return RecordPage{}, err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.Itoa(offset + limit)
	}
	return RecordPage{Items: items, NextCursor: next, Count: len(items)}, nil
}

func (s *Store) GetRecord(ctx context.Context, id string) (Record, error) {
	if !s.v2Configured() {
		notes, err := s.loadAll()
		if err != nil {
			return Record{}, err
		}
		for _, note := range notes {
			if note.ID == strings.TrimSpace(id) {
				return legacyNoteToRecord(note, s.cfg.ProjectKey), nil
			}
		}
		return Record{}, sql.ErrNoRows
	}
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_session_generation,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json
FROM memory_entries WHERE id = ? AND project_key = ?
`, strings.TrimSpace(id), s.cfg.ProjectKey)
	record, err := scanRecordColumns(row)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) listRecordsLegacy(filter RecordFilter) (RecordPage, error) {
	notes, err := s.loadAll()
	if err != nil {
		return RecordPage{}, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if filter.Cursor != "" {
		offset, _ = strconv.Atoi(filter.Cursor)
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	tag := strings.ToLower(strings.TrimSpace(filter.Tag))
	items := make([]Record, 0, len(notes))
	for _, note := range notes {
		record := legacyNoteToRecord(note, s.cfg.ProjectKey)
		if filter.Status != "" && record.Status != filter.Status {
			continue
		}
		if filter.Kind != "" && record.Kind != filter.Kind {
			continue
		}
		if tag != "" && !strings.Contains(strings.ToLower(strings.Join(record.Tags, " ")), tag) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(record.Text+" "+strings.Join(record.Tags, " ")), query) {
			continue
		}
		items = append(items, record)
	}
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.Itoa(offset + limit)
	}
	return RecordPage{Items: items, NextCursor: next, Count: len(items)}, nil
}

func legacyNoteToRecord(note Note, projectKey string) Record {
	return Record{
		ID: note.ID, Scope: ScopeProject, ProjectKey: projectKey,
		Kind: "reference", Status: StatusActive, Text: note.Text,
		NormalizedText: normalizeMemoryText(note.Text), Tags: append([]string(nil), note.Tags...),
		Sensitivity: "internal", Pinned: true, RetentionClass: "permanent", SourceKind: "manual",
		SourceSessionID: note.SessionID, Revision: 1,
		CreatedAt: note.CreatedAt, UpdatedAt: note.CreatedAt,
		Freshness: FreshnessFresh,
	}
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanRecordColumns(row rowScanner) (Record, error) {
	var (
		record                                      Record
		id, scope, projectKey, kind, status         sql.NullString
		text, normalizedText, tagsJSON              sql.NullString
		retentionClass, sourceKind                  sql.NullString
		sourceSessionID, sourceTurnID               sql.NullString
		sourceRefsJSON, contentHash                 sql.NullString
		supersedesID, freshness                     sql.NullString
		staleReason, createdAt, updatedAt           sql.NullString
		lastUsedAt, validFrom, validUntil           sql.NullString
		lastVerifiedAt, revalidateAfter             sql.NullString
		deletedAt, metadataJSON                     sql.NullString
		confidence                                  float64
		sensitivity                                 sql.NullString
		pinned                                      int
		revision, useCount, sourceSessionGeneration int64
	)
	if err := row.Scan(
		&id, &scope, &projectKey, &kind, &status, &text, &normalizedText, &tagsJSON,
		&confidence, &sensitivity, &pinned, &retentionClass, &sourceKind, &sourceSessionID,
		&sourceSessionGeneration, &sourceTurnID, &sourceRefsJSON, &contentHash, &supersedesID, &revision,
		&createdAt, &updatedAt, &lastUsedAt, &useCount, &validFrom, &validUntil,
		&freshness, &lastVerifiedAt, &revalidateAfter, &staleReason, &deletedAt,
		&metadataJSON,
	); err != nil {
		return Record{}, err
	}
	record = Record{
		ID: id.String, Scope: Scope(scope.String), ProjectKey: projectKey.String,
		Kind: kind.String, Status: Status(status.String), Text: text.String,
		NormalizedText: normalizedText.String, Confidence: confidence, Sensitivity: sensitivity.String,
		Pinned: pinned != 0, RetentionClass: retentionClass.String,
		SourceKind: sourceKind.String, SourceSessionID: sourceSessionID.String,
		SourceSessionGeneration: sourceSessionGeneration,
		SourceTurnID:            sourceTurnID.String, ContentHash: contentHash.String,
		SupersedesID: supersedesID.String, Revision: revision,
		CreatedAt: parseTime(createdAt.String), UpdatedAt: parseTime(updatedAt.String),
		LastUsedAt: parseTime(lastUsedAt.String), UseCount: int(useCount),
		ValidFrom: parseTime(validFrom.String), ValidUntil: parseTime(validUntil.String),
		Freshness: Freshness(freshness.String), LastVerifiedAt: parseTime(lastVerifiedAt.String),
		RevalidateAfter: parseTime(revalidateAfter.String), StaleReason: staleReason.String,
		DeletedAt: parseTime(deletedAt.String),
	}
	_ = json.Unmarshal([]byte(tagsJSON.String), &record.Tags)
	_ = json.Unmarshal([]byte(sourceRefsJSON.String), &record.SourceRefs)
	if strings.TrimSpace(metadataJSON.String) != "" {
		_ = json.Unmarshal([]byte(metadataJSON.String), &record.Metadata)
	}
	return record, nil
}

func (s *Store) UpdateRecord(ctx context.Context, id string, patch RecordPatch) (Record, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	record, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	expectedRevision := record.Revision
	expectedStatus := record.Status
	if patch.Text != nil {
		text := strings.TrimSpace(*patch.Text)
		if text == "" {
			return Record{}, fmt.Errorf("memorystore: record text is required")
		}
		if rule := memoryredact.DetectSecret(text); rule != "" {
			return Record{}, fmt.Errorf("memorystore: refusing to store secret-shaped content (%s)", rule)
		}
		record.Text = text
		record.NormalizedText = normalizeMemoryText(text)
		record.ContentHash = memoryContentHash(string(record.Scope), record.ProjectKey, record.Kind, text)
	}
	if patch.Tags != nil {
		record.Tags = normalizeTags(*patch.Tags)
	}
	if patch.Pinned != nil {
		record.Pinned = *patch.Pinned
	}
	if patch.Status != nil {
		next := *patch.Status
		switch next {
		case StatusCandidate, StatusActive, StatusSuperseded, StatusExpired:
		case StatusDeleted:
			return Record{}, fmt.Errorf("memorystore: use DeleteRecord for tombstone deletion")
		default:
			return Record{}, fmt.Errorf("memorystore: invalid status %q", next)
		}
		if record.Status == StatusDeleted {
			return Record{}, fmt.Errorf("memorystore: deleted records must be restored through RestoreRecord")
		}
		if next == StatusActive && record.Status != StatusActive && record.Status != StatusCandidate {
			return Record{}, fmt.Errorf("memorystore: only candidate/active records can be activated")
		}
		if next == StatusCandidate && record.Status == StatusSuperseded {
			return Record{}, fmt.Errorf("memorystore: superseded records cannot return to candidate")
		}
		record.Status = next
	}
	record.Revision++
	record.UpdatedAt = time.Now().UTC()
	tagsJSON, _ := json.Marshal(record.Tags)
	metadataJSON, _ := json.Marshal(record.Metadata)
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET text = ?, normalized_text = ?, tags_json = ?, pinned = ?, status = ?,
    content_hash = ?, revision = ?, updated_at = ?, metadata_json = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = ?
`, record.Text, record.NormalizedText, string(tagsJSON), boolInt(record.Pinned),
		string(record.Status), record.ContentHash, record.Revision,
		record.UpdatedAt.UTC().Format(time.RFC3339Nano), string(metadataJSON),
		record.ID, s.cfg.ProjectKey, expectedRevision, string(expectedStatus))
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: record %s changed concurrently", record.ID)
	}
	if err := insertEventTx(ctx, tx, record.ProjectKey, record.ID, "updated", "user", record.SourceSessionID, record.Revision, map[string]interface{}{"reason": "manual_patch"}); err != nil {
		return Record{}, err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, record.ID, "record_updated"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// MarkConflictOpenQuestion records a non-destructive ambiguity marker. Both
// records stay active; consolidation must not silently choose one.
func (s *Store) MarkConflictOpenQuestion(ctx context.Context, ids []string, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "ambiguous_conflict"
	}
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	records := make([]Record, 0, len(ids))
	for _, id := range uniqueStrings(ids) {
		record, err := s.GetRecord(ctx, id)
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	for _, record := range records {
		if err := insertEventTx(ctx, tx, record.ProjectKey, record.ID, "open_question", "consolidation", record.SourceSessionID, record.Revision, map[string]interface{}{
			"reason": reason,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteRecord(ctx context.Context, id string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	record, err := s.GetRecord(ctx, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET status = 'deleted', deleted_at = ?, revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = ?
`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), record.ID, s.cfg.ProjectKey,
		record.Revision, string(record.Status))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("memorystore: record %s changed concurrently", record.ID)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_suppressions (
    id, scope, project_key, content_hash, reason, source_memory_id, created_at, metadata_json
) VALUES (?, ?, ?, ?, 'deleted', ?, ?, '{}')
`, "sup_"+uuid.NewString(), string(record.Scope), record.ProjectKey, record.ContentHash, record.ID, nowText()); err != nil {
		return err
	}
	if err := insertEventTx(ctx, tx, record.ProjectKey, record.ID, "deleted", "user", record.SourceSessionID, record.Revision+1, map[string]interface{}{"reason": "manual_delete"}); err != nil {
		return err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, record.ID, "record_deleted"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetSessionMode(ctx context.Context, sessionID string, mode SessionMode, projectKey string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("memorystore: session id is required")
	}
	switch mode {
	case SessionModeEnabled, SessionModeDisabled, SessionModePolluted:
	default:
		return fmt.Errorf("memorystore: invalid session mode")
	}
	if strings.TrimSpace(projectKey) == "" {
		projectKey = s.cfg.ProjectKey
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_session_modes (session_id, scope, project_key, mode, updated_at, metadata_json)
VALUES (?, 'project', ?, ?, ?, '{}')
ON CONFLICT(project_key, session_id) DO UPDATE SET mode = excluded.mode, scope = excluded.scope,
    updated_at = excluded.updated_at
`, strings.TrimSpace(sessionID), projectKey, string(mode), nowText())
	return err
}

func (s *Store) GetSessionMode(ctx context.Context, sessionID string) (SessionMode, error) {
	db, err := s.ensureV2()
	if err != nil {
		return "", err
	}
	var mode string
	err = db.QueryRowContext(ctx, `
SELECT mode FROM memory_session_modes WHERE session_id = ? AND project_key = ?
`, strings.TrimSpace(sessionID), s.cfg.ProjectKey).Scan(&mode)
	if errorsIsNoRows(err) {
		return SessionModeEnabled, nil
	}
	if err != nil {
		return "", err
	}
	return SessionMode(mode), nil
}

func (s *Store) Status(ctx context.Context) (StatusReport, error) {
	report := StatusReport{Mode: s.cfg.Mode, Scope: "project", DBPath: s.v2Path, ByStatus: map[string]int{}, ByFreshness: map[string]int{}, Jobs: map[string]int{}, TurnDigests: map[string]int{}, TurnWindows: map[string]int{}}
	if !s.v2Configured() {
		notes, err := s.loadAll()
		if err != nil {
			return report, err
		}
		report.Total = len(notes)
		report.ByStatus["active"] = len(notes)
		report.ByFreshness["fresh"] = len(notes)
		report.Fallback = true
		report.FallbackReason = "mode_off"
		return report, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		report.Fallback = true
		report.FallbackReason = err.Error()
		return report, err
	}
	var version int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	report.SchemaVersion = version
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(1) FROM memory_entries WHERE project_key = ? GROUP BY status`, s.cfg.ProjectKey)
	if err == nil {
		for rows.Next() {
			var status string
			var count int
			if scanErr := rows.Scan(&status, &count); scanErr == nil {
				report.ByStatus[status] = count
				report.Total += count
			}
		}
		_ = rows.Close()
	}
	rows, err = db.QueryContext(ctx, `SELECT freshness_status, COUNT(1) FROM memory_entries WHERE project_key = ? GROUP BY freshness_status`, s.cfg.ProjectKey)
	if err == nil {
		for rows.Next() {
			var freshness string
			var count int
			if scanErr := rows.Scan(&freshness, &count); scanErr == nil {
				report.ByFreshness[freshness] = count
			}
		}
		_ = rows.Close()
	}
	var summaryRevision int64
	var summaryStatus, summaryFreshness string
	err = db.QueryRowContext(ctx, `
SELECT revision, status, freshness_status FROM memory_summaries
WHERE project_key = ? AND status = 'active' ORDER BY revision DESC LIMIT 1
`, s.cfg.ProjectKey).Scan(&summaryRevision, &summaryStatus, &summaryFreshness)
	if err == nil {
		report.SummaryRevision = summaryRevision
		report.SummaryStatus = summaryStatus
		report.SummaryFreshness = summaryFreshness
	}
	_ = db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_entries
WHERE project_key = ? AND status = 'active' AND pinned = 1
`, s.cfg.ProjectKey).Scan(&report.PinnedCount)
	report.InjectionMode = s.cfg.InjectionMode
	rows, err = db.QueryContext(ctx, `SELECT status, COUNT(1) FROM memory_jobs WHERE project_key = ? GROUP BY status`, s.cfg.ProjectKey)
	if err == nil {
		for rows.Next() {
			var status string
			var count int
			if scanErr := rows.Scan(&status, &count); scanErr == nil {
				report.Jobs[status] = count
			}
		}
		_ = rows.Close()
	}
	rows, err = db.QueryContext(ctx, `SELECT status, COUNT(1) FROM memory_turn_digests WHERE project_key = ? GROUP BY status`, s.cfg.ProjectKey)
	if err == nil {
		for rows.Next() {
			var status string
			var count int
			if scanErr := rows.Scan(&status, &count); scanErr == nil {
				report.TurnDigests[status] = count
			}
		}
		_ = rows.Close()
	}
	rows, err = db.QueryContext(ctx, `SELECT status, COUNT(1) FROM memory_turn_windows WHERE project_key = ? GROUP BY status`, s.cfg.ProjectKey)
	if err == nil {
		for rows.Next() {
			var status string
			var count int
			if scanErr := rows.Scan(&status, &count); scanErr == nil {
				report.TurnWindows[status] = count
			}
		}
		_ = rows.Close()
	}
	var pendingDigests, pendingSessions int
	var oldestPending string
	_ = db.QueryRowContext(ctx, `
SELECT COUNT(1), COUNT(DISTINCT session_id), COALESCE(MIN(created_at), '')
FROM memory_turn_digests
WHERE project_key = ? AND status = 'pending'
`, s.cfg.ProjectKey).Scan(&pendingDigests, &pendingSessions, &oldestPending)
	report.PendingDigestSessions = pendingSessions
	batchSize := s.cfg.TurnDigestBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	if pendingDigests > 0 {
		report.PendingDigestBacklogBatches = (pendingDigests + batchSize - 1) / batchSize
	}
	if oldestPending != "" {
		if age := time.Since(parseTime(oldestPending)); age > 0 {
			report.OldestPendingDigestAgeSeconds = int64(age.Seconds())
		}
	}
	if recovered, metricErr := s.RuntimeMetric(ctx, MetricRecoveredDigestSessions); metricErr == nil {
		report.RecoveredDigestSessions = recovered
	}
	return report, nil
}

func (s *Store) ListRecallTraces(ctx context.Context, sessionID string, limit int) ([]RecallTrace, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	args := []interface{}{s.cfg.ProjectKey}
	where := []string{"project_key = ?"}
	if strings.TrimSpace(sessionID) != "" {
		where = append(where, "session_id = ?")
		args = append(args, strings.TrimSpace(sessionID))
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT id, session_id, COALESCE(turn_id, ''), project_key, query_hash,
       COALESCE(query_preview, ''), COALESCE(summary_revision, 0),
       candidate_count, filtered_count, selected_ids_json, dropped_json,
       token_estimate, token_budget, COALESCE(budget_source, ''), injected,
       created_at
FROM memory_recall_traces
WHERE `+strings.Join(where, " AND ")+`
ORDER BY created_at DESC LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	traces := make([]RecallTrace, 0, limit)
	for rows.Next() {
		var (
			trace                                 RecallTrace
			id, session, turn, project, queryHash sql.NullString
			preview, selectedJSON, droppedJSON    sql.NullString
			budgetSource, createdAt               sql.NullString
			summaryRevision, candidate, filtered  sql.NullInt64
			tokens, budget, injected              sql.NullInt64
		)
		if err := rows.Scan(&id, &session, &turn, &project, &queryHash, &preview,
			&summaryRevision, &candidate, &filtered, &selectedJSON, &droppedJSON,
			&tokens, &budget, &budgetSource, &injected, &createdAt); err != nil {
			return nil, err
		}
		trace = RecallTrace{
			ID: id.String, SessionID: session.String, TurnID: turn.String,
			ProjectKey: project.String, QueryHash: queryHash.String,
			QueryPreview: preview.String, SummaryRevision: summaryRevision.Int64,
			CandidateCount: int(candidate.Int64), FilteredCount: int(filtered.Int64),
			TokenEstimate: int(tokens.Int64), TokenBudget: int(budget.Int64),
			BudgetSource: budgetSource.String, Injected: injected.Int64 != 0,
			CreatedAt: parseTime(createdAt.String),
		}
		_ = json.Unmarshal([]byte(selectedJSON.String), &trace.SelectedIDs)
		_ = json.Unmarshal([]byte(droppedJSON.String), &trace.Dropped)
		traces = append(traces, trace)
	}
	return traces, rows.Err()
}

func (s *Store) recordRecallTrace(opts InjectOptions, selection Selection) error {
	if strings.TrimSpace(opts.SessionID) == "" {
		return nil
	}
	if s == nil || !s.cfg.RecallTraceEnabled {
		return nil
	}
	rate := s.cfg.RecallTraceSampleRate
	if rate <= 0 {
		return nil
	}
	if rate < 1 && rand.Float64() >= rate {
		return nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	budget := opts.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(opts.Query)))
	selected, _ := json.Marshal(selection.IDs)
	dropped, _ := json.Marshal(selection.Dropped)
	preview := strings.TrimSpace(opts.Query)
	preview, _ = memoryredact.Redact(preview)
	if runes := []rune(preview); len(runes) > 160 {
		preview = string(runes[:160]) + "…"
	}
	_, err = db.ExecContext(context.Background(), `
INSERT INTO memory_recall_traces (
    id, session_id, turn_id, project_key, query_hash, query_preview,
    summary_revision, candidate_count, filtered_count, selected_ids_json,
    dropped_json, token_estimate, token_budget, budget_source, injected, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'contextmgr', ?, ?)
`, "trace_"+uuid.NewString(), strings.TrimSpace(opts.SessionID), nullableString(opts.TurnID),
		s.cfg.ProjectKey, hex.EncodeToString(sum[:]), preview, selection.Revision,
		selection.CandidateCount, selection.FilteredCount, string(selected),
		string(dropped), selection.TokenEstimate, budget, boolInt(len(selection.IDs) > 0), nowText())
	return err
}
