package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

var (
	errV2Unavailable = errors.New("memorystore: v2 backend unavailable")
	errMemoryClosed  = errors.New("memorystore: store is closed")
)

func normalizeConfig(cfg Config) Config {
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch cfg.Mode {
	case "shadow", "on":
	default:
		cfg.Mode = "off"
	}
	cfg.InjectionMode = strings.ToLower(strings.TrimSpace(cfg.InjectionMode))
	if cfg.InjectionMode == "" {
		cfg.InjectionMode = "notes"
	}
	if cfg.PinnedBudgetRatio <= 0 {
		cfg.PinnedBudgetRatio = 0.20
	}
	if cfg.PinnedBudgetRatio > 1 {
		cfg.PinnedBudgetRatio = 1
	}
	if cfg.RecallTraceSampleRate <= 0 {
		cfg.RecallTraceSampleRate = 0.01
	}
	if cfg.RecallTraceSampleRate > 1 {
		cfg.RecallTraceSampleRate = 1
	}
	if strings.TrimSpace(cfg.ProjectKey) == "" {
		projectRoot := strings.TrimSpace(cfg.ProjectRoot)
		if projectRoot == "" && strings.TrimSpace(cfg.Root) != "" {
			root := filepath.Clean(cfg.Root)
			if filepath.Base(root) == "memory" && filepath.Base(filepath.Dir(root)) == ".aicli" {
				projectRoot = filepath.Dir(filepath.Dir(root))
			} else {
				projectRoot = root
			}
		}
		if projectRoot != "" {
			cfg.ProjectKey = ResolveProjectKey(projectRoot)
		}
	}
	return cfg
}

func resolveV2Path(cfg Config, root string) string {
	storePath := strings.TrimSpace(cfg.StorePath)
	if storePath == "" {
		return filepath.Join(root, "memory.db")
	}
	if filepath.IsAbs(storePath) {
		return filepath.Clean(storePath)
	}
	return filepath.Join(root, storePath)
}

func (s *Store) v2Configured() bool {
	if s == nil {
		return false
	}
	return s.cfg.Mode == "shadow" || s.cfg.Mode == "on"
}

func (s *Store) ensureV2() (*sql.DB, error) {
	if s == nil {
		return nil, fmt.Errorf("memorystore: store is nil")
	}
	s.v2Mu.Lock()
	defer s.v2Mu.Unlock()
	if s.closed {
		return nil, errMemoryClosed
	}
	if s.db != nil {
		return s.db, nil
	}
	if s.v2Err != nil {
		return nil, s.v2Err
	}
	if !s.v2Configured() {
		return nil, errV2Unavailable
	}
	if err := ensureMemoryParent(s.v2Path); err != nil {
		s.v2Err = fmt.Errorf("memorystore: create v2 directory: %w", err)
		return nil, fmt.Errorf("%w: %v", errV2Unavailable, s.v2Err)
	}
	db, err := sqliteutil.OpenFile(s.v2Path, true)
	if err != nil {
		s.v2Err = fmt.Errorf("memorystore: open v2 db: %w", err)
		return nil, fmt.Errorf("%w: %v", errV2Unavailable, s.v2Err)
	}
	if err := initMemorySchema(context.Background(), db); err != nil {
		_ = db.Close()
		s.v2Err = err
		return nil, fmt.Errorf("%w: %v", errV2Unavailable, s.v2Err)
	}
	if _, err := s.migrateNotesV2(context.Background(), db); err != nil {
		_ = db.Close()
		s.v2Err = err
		return nil, fmt.Errorf("%w: %v", errV2Unavailable, s.v2Err)
	}
	s.db = db
	return db, nil
}

// Close releases the SQLite backend. The legacy JSONL path has no handle.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.v2Mu.Lock()
	defer s.v2Mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		return err
	}
	return nil
}

// DBPath returns the v2 database path even before the store is opened. It does
// not create the directory or database.
func (s *Store) DBPath() string {
	if s == nil {
		return ""
	}
	return s.v2Path
}

func (s *Store) Mode() string {
	if s == nil {
		return ""
	}
	return s.cfg.Mode
}

func (s *Store) ProjectKey() string {
	if s == nil {
		return ""
	}
	return s.cfg.ProjectKey
}

func (s *Store) InjectionMode() string {
	if s == nil {
		return ""
	}
	return s.cfg.InjectionMode
}

func (s *Store) SchemaVersion() (int, error) {
	if s == nil || !s.v2Configured() {
		return 0, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

// V2InjectionEnabled reports whether contextmgr should use the v2 selector.
// off/shadow and disabled injection intentionally use the legacy manual path.
func (s *Store) V2InjectionEnabled() bool {
	if s == nil {
		return false
	}
	return s.cfg.Mode == "on" && !s.cfg.DisableInjection
}

func (s *Store) MemoryStageSplitEnabled() bool {
	return s != nil && !s.cfg.DisableStageSplit
}

func (s *Store) appendV2(opts AppendNoteOptions) (Note, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Note{}, err
	}
	text := strings.TrimSpace(opts.Text)
	if text == "" {
		return Note{}, fmt.Errorf("memorystore: note text is required")
	}
	now := opts.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	kind := strings.TrimSpace(opts.Kind)
	if kind == "" {
		kind = "reference"
	}
	scope := strings.ToLower(strings.TrimSpace(string(opts.Scope)))
	if scope == "" {
		scope = string(ScopeProject)
	}
	pinned := true
	if opts.Pinned != nil {
		pinned = *opts.Pinned
	}
	tags := normalizeTags(opts.Tags)
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = "manual"
	}
	note := Note{
		ID:        strings.TrimSpace(opts.ID),
		Text:      text,
		Tags:      tags,
		Source:    source,
		SessionID: strings.TrimSpace(opts.SessionID),
		CreatedAt: now,
	}
	if note.ID == "" {
		note.ID = newNoteID(note)
	}
	record := Record{
		ID:              note.ID,
		Scope:           Scope(scope),
		ProjectKey:      s.cfg.ProjectKey,
		Kind:            kind,
		Status:          StatusActive,
		Text:            text,
		NormalizedText:  normalizeMemoryText(text),
		Tags:            tags,
		Confidence:      1.0,
		Sensitivity:     "internal",
		Pinned:          pinned,
		RetentionClass:  "permanent",
		SourceKind:      "manual",
		SourceSessionID: note.SessionID,
		SourceRefs:      []string{"manual:" + note.ID},
		ContentHash:     memoryContentHash(scope, s.cfg.ProjectKey, kind, text),
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Freshness:       FreshnessFresh,
		LastVerifiedAt:  now,
	}
	if err := s.insertRecordTx(context.Background(), db, record, "user", note.SessionID); err != nil {
		return Note{}, err
	}
	return note, nil
}

func (s *Store) insertRecordTx(ctx context.Context, db *sql.DB, record Record, actor, sessionID string) error {
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin record tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.insertRecordSQLTx(ctx, tx, record, actor, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) insertRecordSQLTx(ctx context.Context, tx *sql.Tx, record Record, actor, sessionID string) error {
	if record.Status == "" {
		record.Status = StatusActive
	}
	if record.Freshness == "" {
		record.Freshness = FreshnessFresh
	}
	if record.Revision <= 0 {
		record.Revision = 1
	}
	record.Sensitivity = normalizeSensitivity(record.Sensitivity)
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	var suppressed int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_suppressions
WHERE scope = ? AND project_key = ? AND content_hash = ?
  AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
`, string(record.Scope), record.ProjectKey, record.ContentHash, nowText()).Scan(&suppressed); err != nil {
		return fmt.Errorf("memorystore: check suppression: %w", err)
	}
	if suppressed > 0 {
		return fmt.Errorf("memorystore: content is suppressed")
	}
	if rule := memoryredact.DetectSecret(record.Text); rule != "" {
		return fmt.Errorf("memorystore: refusing to store secret-shaped content (%s)", rule)
	}
	tagsJSON, _ := json.Marshal(record.Tags)
	refsJSON, _ := json.Marshal(record.SourceRefs)
	metadataJSON, _ := json.Marshal(record.Metadata)
	_, err := tx.ExecContext(ctx, `
INSERT INTO memory_entries (
    id, scope, project_key, kind, status, text, normalized_text, tags_json,
    confidence, sensitivity, pinned, retention_class, source_kind,
    source_session_id, source_turn_id, source_refs_json, content_hash,
    supersedes_id, revision, created_at, updated_at, last_used_at, use_count,
    valid_from, valid_until, freshness_status, last_verified_at,
    revalidate_after, stale_reason, deleted_at, metadata_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID, string(record.Scope), record.ProjectKey, record.Kind, string(record.Status),
		record.Text, record.NormalizedText, string(tagsJSON), record.Confidence, record.Sensitivity,
		boolInt(record.Pinned), record.RetentionClass, record.SourceKind,
		nullableString(record.SourceSessionID), nullableString(record.SourceTurnID),
		string(refsJSON), record.ContentHash, nullableString(record.SupersedesID),
		record.Revision, record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano), nullableTime(record.LastUsedAt),
		record.UseCount, nullableTime(record.ValidFrom), nullableTime(record.ValidUntil),
		string(record.Freshness), nullableTime(record.LastVerifiedAt),
		nullableTime(record.RevalidateAfter), nullableString(record.StaleReason),
		nullableTime(record.DeletedAt), string(metadataJSON),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("memorystore: duplicate memory content or id")
		}
		return fmt.Errorf("memorystore: insert record: %w", err)
	}
	if err := insertSourceTx(ctx, tx, record, sessionID); err != nil {
		return err
	}
	if err := insertEventTx(ctx, tx, record.ID, "created", actor, sessionID, record.Revision, map[string]interface{}{
		"kind": record.Kind, "source_kind": record.SourceKind,
	}); err != nil {
		return err
	}
	return nil
}

func insertSourceTx(ctx context.Context, tx *sql.Tx, record Record, sessionID string) error {
	sourceID := "src_" + uuid.NewString()
	messageIDs, _ := json.Marshal(record.SourceMessageIDs)
	sourceHash := strings.TrimSpace(record.SourceHash)
	if sourceHash == "" {
		sourceHash = record.ContentHash
	}
	sourceUpdatedAt := record.SourceUpdatedAt
	if sourceUpdatedAt.IsZero() {
		sourceUpdatedAt = record.UpdatedAt
	}
	sourceObservedAt := record.SourceObservedAt
	if sourceObservedAt.IsZero() {
		sourceObservedAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_sources (
    id, memory_id, source_kind, source_id, session_id, turn_id,
    message_ids_json, artifact_refs_json, fact_refs_json, knowledge_refs_json,
    source_hash, source_revision, source_updated_at, source_observed_at, quote, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, '[]', '[]', '[]', ?, ?, ?, ?, ?, ?)
`, sourceID, record.ID, record.SourceKind, record.ID, nullableString(record.SourceSessionID),
		nullableString(record.SourceTurnID), string(messageIDs), sourceHash,
		nullableInt64(record.SourceRevision),
		sourceUpdatedAt.UTC().Format(time.RFC3339Nano),
		sourceObservedAt.UTC().Format(time.RFC3339Nano), record.Text, nowText()); err != nil {
		return fmt.Errorf("memorystore: insert source: %w", err)
	}
	return nil
}

func insertEventTx(ctx context.Context, tx *sql.Tx, memoryID, eventType, actor, sessionID string, revision int64, payload map[string]interface{}) error {
	body, _ := json.Marshal(payload)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_events (id, memory_id, event_type, actor, session_id, revision, payload_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, "evt_"+uuid.NewString(), memoryID, eventType, actor, nullableString(sessionID), revision, string(body), nowText()); err != nil {
		return fmt.Errorf("memorystore: insert event: %w", err)
	}
	return nil
}

func (s *Store) listV2(limit int) ([]Note, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 0
	}
	query := `
SELECT id, text, tags_json, source_kind, source_session_id, created_at
FROM memory_entries
WHERE scope = ? AND project_key = ? AND status = 'active' AND source_kind = 'manual'
ORDER BY updated_at DESC, id DESC`
	args := []interface{}{string(ScopeProject), s.cfg.ProjectKey}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := make([]Note, 0, 8)
	for rows.Next() {
		var (
			id, text, tagsJSON, sourceKind, sessionID sql.NullString
			createdAt                                 string
		)
		if err := rows.Scan(&id, &text, &tagsJSON, &sourceKind, &sessionID, &createdAt); err != nil {
			return nil, err
		}
		note := Note{ID: id.String, Text: text.String, Source: sourceKind.String, SessionID: sessionID.String}
		_ = json.Unmarshal([]byte(tagsJSON.String), &note.Tags)
		note.CreatedAt = parseTime(createdAt)
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

func (s *Store) searchV2(opts SearchOptions) ([]SearchHit, error) {
	notes, err := s.listV2(0)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	requiredTags := normalizeTags(opts.Tags)
	query := strings.TrimSpace(opts.Query)
	tokens := tokenize(query)
	hits := make([]SearchHit, 0, len(notes))
	for _, note := range notes {
		if !noteHasAllTags(note, requiredTags) {
			continue
		}
		if query == "" {
			hits = append(hits, SearchHit{Note: note, Score: float64(note.CreatedAt.Unix())})
			continue
		}
		score := scoreNote(note, tokens, query)
		if score > 0 {
			hits = append(hits, SearchHit{Note: note, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Note.CreatedAt.After(hits[j].Note.CreatedAt)
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *Store) selectForInjectV2(opts InjectOptions) (Selection, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Selection{}, err
	}
	budget := opts.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultInjectLimit
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	records, err := s.loadInjectableRecords(context.Background(), db, now)
	if err != nil {
		return Selection{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(s.cfg.InjectionMode))
	if mode != "summary" && mode != "hybrid" && mode != "notes" {
		mode = "notes"
	}
	supportsSummary := mode == "summary" || mode == "hybrid"
	supportsRecall := mode == "hybrid" || mode == "notes"
	selection := Selection{CandidateCount: len(records)}
	pinnedBudget := int(float64(budget) * s.cfg.PinnedBudgetRatio)
	pinnedUsed := 0
	stableIDs := map[string]struct{}{}
	for _, record := range records {
		if !record.Pinned && strings.ToLower(record.SourceKind) != "manual" {
			continue
		}
		cost := EstimateTokens(record.Text) + memoryRecordTokenOverhead
		if cost > pinnedBudget-pinnedUsed {
			if len(selection.Stable) == 0 {
				if fitted, ok := fitRecordToBudget(record, pinnedBudget); ok {
					record = fitted
					cost = EstimateTokens(record.Text) + memoryRecordTokenOverhead
				} else {
					selection.Dropped = append(selection.Dropped, DroppedRecord{ID: record.ID, Reason: "pinned_budget"})
					continue
				}
			} else {
				selection.Dropped = append(selection.Dropped, DroppedRecord{ID: record.ID, Reason: "pinned_budget"})
				continue
			}
		}
		selection.Stable = append(selection.Stable, record)
		stableIDs[record.ID] = struct{}{}
		pinnedUsed += cost
	}
	summaryUsed := 0
	if supportsSummary {
		if summary, err := s.loadGatedActiveSummary(context.Background(), db, now); err == nil && summary != nil {
			summary.TokenEstimate = EstimateTokens(summary.Text)
			summaryCost := summary.TokenEstimate
			summaryCap := budget
			switch mode {
			case "summary":
				if len(selection.Stable) > 0 {
					summaryCap = budget * 80 / 100
				}
			case "hybrid":
				summaryCap = budget * 50 / 100
			}
			if summaryCost <= summaryCap && summaryCost+pinnedUsed <= budget {
				selection.Summary = summary
				selection.Revision = summary.Revision
				summaryUsed = summaryCost
				for _, id := range summary.RecordIDs {
					stableIDs[id] = struct{}{}
				}
			} else {
				selection.Dropped = append(selection.Dropped, DroppedRecord{ID: summary.ID, Reason: "summary_budget"})
			}
		}
	}
	candidates := make([]SearchHit, 0, len(records))
	for _, record := range records {
		if _, exists := stableIDs[record.ID]; exists {
			continue
		}
		note := recordToNote(record)
		score := scoreNote(note, tokenize(opts.Query), opts.Query)
		if opts.Query != "" && score <= 0 {
			continue
		}
		candidates = append(candidates, SearchHit{Note: note, Score: score})
	}
	if !supportsRecall {
		selection.TokenEstimate = summaryUsed + pinnedUsed
		selection.IDs = appendSelectionIDs(selection.IDs, selection.Stable, selection.Summary)
		selection.FilteredCount = len(selection.Dropped)
		if !opts.SkipTrace {
			_ = s.recordRecallTrace(opts, selection)
		}
		return selection, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Note.CreatedAt.After(candidates[j].Note.CreatedAt)
		}
		return candidates[i].Score > candidates[j].Score
	})
	remaining := budget - summaryUsed - pinnedUsed
	if remaining < 0 {
		remaining = 0
	}
	recallCap := budget * 80 / 100
	if mode == "hybrid" {
		recallCap = budget * 30 / 100
	}
	recallUsed := 0
	for _, hit := range candidates {
		if len(selection.Recall) >= limit {
			selection.Dropped = append(selection.Dropped, DroppedRecord{ID: hit.Note.ID, Reason: "memory_budget"})
			continue
		}
		record := recordByID(records, hit.Note.ID)
		cost := EstimateTokens(record.Text) + memoryRecordTokenOverhead
		perRecordCap := remaining
		if recallCap < perRecordCap {
			perRecordCap = recallCap
		}
		if cost > perRecordCap-recallUsed {
			if len(selection.Recall) == 0 {
				if fitted, ok := fitRecordToBudget(record, perRecordCap); ok {
					record = fitted
					cost = EstimateTokens(record.Text) + memoryRecordTokenOverhead
				} else {
					selection.Dropped = append(selection.Dropped, DroppedRecord{ID: hit.Note.ID, Reason: "memory_budget"})
					continue
				}
			} else {
				selection.Dropped = append(selection.Dropped, DroppedRecord{ID: hit.Note.ID, Reason: "memory_budget"})
				continue
			}
		}
		selection.Recall = append(selection.Recall, record)
		recallUsed += cost
	}
	selection.TokenEstimate = summaryUsed + pinnedUsed + recallUsed
	selection.IDs = appendSelectionIDs(selection.IDs, selection.Stable, selection.Summary)
	for _, record := range selection.Recall {
		selection.IDs = append(selection.IDs, record.ID)
	}
	selection.FilteredCount = len(selection.Dropped)
	if !opts.SkipTrace {
		_ = s.recordRecallTrace(opts, selection)
	}
	return selection, nil
}

func appendSelectionIDs(ids []string, stable []Record, summary *Summary) []string {
	for _, record := range stable {
		ids = append(ids, record.ID)
	}
	if summary != nil {
		ids = append(ids, summary.RecordIDs...)
	}
	return uniqueStrings(ids)
}

func (s *Store) loadInjectableRecords(ctx context.Context, db *sql.DB, now time.Time) ([]Record, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json
FROM memory_entries
WHERE scope = ? AND project_key = ? AND status = 'active' AND freshness_status = 'fresh'
  AND sensitivity != 'private'
  AND (deleted_at IS NULL OR deleted_at = '')
  AND (valid_until IS NULL OR valid_until = '' OR valid_until > ?)
ORDER BY pinned DESC, use_count DESC, updated_at DESC
`, string(ScopeProject), s.cfg.ProjectKey, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, 32)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		if memoryredact.DetectSecret(record.Text) != "" {
			continue
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
