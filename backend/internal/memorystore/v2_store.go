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
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

var (
	errV2Unavailable = errors.New("memorystore: v2 backend unavailable")
	errMemoryClosed  = errors.New("memorystore: store is closed")
)

const maxMemorySourceQuoteRunes = 1200

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
	cfg.CategoryBudgets = cfg.CategoryBudgets.normalized()
	if cfg.RecallTraceSampleRate <= 0 {
		cfg.RecallTraceSampleRate = 0.01
	}
	if cfg.RecallTraceSampleRate > 1 {
		cfg.RecallTraceSampleRate = 1
	}
	cfg.StaleBehavior = strings.ToLower(strings.TrimSpace(cfg.StaleBehavior))
	if cfg.StaleBehavior != "warn" {
		cfg.StaleBehavior = "exclude"
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
	cfg.Scope.ProjectKey = firstNonEmpty(cfg.Scope.ProjectKey, cfg.ProjectKey)
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
	ftsEnabled, ftsErr := ensureMemoryFTS(context.Background(), db)
	_ = ftsErr // FTS is a derived index; its absence must not block the main DB.
	s.ftsEnabled = ftsEnabled
	s.ftsErr = ftsErr
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

func (s *Store) appendV2(ctx context.Context, opts AppendNoteOptions) (Note, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Note{}, err
	}
	resolvedScope, err := s.resolveScopeContext(ctx)
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
	scopeKind := normalizeScopeKind(Scope(firstNonEmpty(opts.ScopeKind, opts.Scope)))
	if scopeKind == "" {
		scopeKind = normalizeScopeKind(resolvedScope.Scope)
	}
	if scopeKind == "" {
		scopeKind = ScopeProject
	}
	if !validMemoryScopeKind(scopeKind) {
		return Note{}, fmt.Errorf("memorystore: invalid scope kind %q", scopeKind)
	}
	tenantID := firstNonEmpty(opts.TenantID, resolvedScope.TenantID)
	ownerID := firstNonEmpty(opts.OwnerID, resolvedScope.OwnerID)
	agentID := firstNonEmpty(opts.AgentID, resolvedScope.AgentID)
	taskID := firstNonEmpty(opts.TaskID, resolvedScope.TaskID)
	sessionID := firstNonEmpty(opts.SessionID, resolvedScope.SessionID)
	scopeKey := strings.TrimSpace(opts.ScopeKey)
	switch scopeKind {
	case ScopeProject:
		scopeKey = s.cfg.ProjectKey
	case ScopeUser, ScopeProfile:
		ownerID = firstNonEmpty(ownerID, scopeKey)
		scopeKey = firstNonEmpty(scopeKey, ownerID)
		if ownerID == "" {
			return Note{}, fmt.Errorf("memorystore: user scope requires owner id")
		}
	case ScopeAgent:
		agentID = firstNonEmpty(agentID, scopeKey)
		scopeKey = firstNonEmpty(scopeKey, agentID)
		if agentID == "" {
			return Note{}, fmt.Errorf("memorystore: agent scope requires agent id")
		}
	case ScopeSession:
		sessionID = firstNonEmpty(sessionID, scopeKey)
		scopeKey = firstNonEmpty(scopeKey, sessionID)
		if sessionID == "" {
			return Note{}, fmt.Errorf("memorystore: session scope requires session id")
		}
	case ScopeTask:
		taskID = firstNonEmpty(taskID, scopeKey)
		scopeKey = firstNonEmpty(scopeKey, taskID)
		if taskID == "" {
			return Note{}, fmt.Errorf("memorystore: task scope requires task id")
		}
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
		ID:          strings.TrimSpace(opts.ID),
		Text:        text,
		Title:       strings.TrimSpace(opts.Title),
		Summary:     strings.TrimSpace(opts.Summary),
		CanonicalID: strings.TrimSpace(opts.CanonicalID),
		Tags:        tags,
		Source:      source,
		SessionID:   sessionID,
		Archived:    opts.Archived,
		CreatedAt:   now,
	}
	if note.ID == "" {
		note.ID = newNoteID(note)
	}
	if note.CanonicalID == "" {
		note.CanonicalID = note.ID
	}
	record := Record{
		ID:              note.ID,
		Scope:           scopeKind,
		ScopeKind:       scopeKind,
		ScopeKey:        scopeKey,
		TenantID:        tenantID,
		OwnerID:         ownerID,
		AgentID:         agentID,
		TaskID:          taskID,
		ProjectKey:      s.cfg.ProjectKey,
		Kind:            kind,
		Status:          StatusActive,
		Text:            text,
		Title:           note.Title,
		Summary:         note.Summary,
		CanonicalID:     note.CanonicalID,
		Archived:        note.Archived,
		NormalizedText:  normalizeMemoryText(text),
		Tags:            tags,
		Confidence:      1.0,
		Sensitivity:     "internal",
		Pinned:          pinned,
		RetentionClass:  "permanent",
		SourceKind:      "manual",
		SourceSessionID: note.SessionID,
		SourceRefs:      []string{"manual:" + note.ID},
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Freshness:       FreshnessFresh,
		LastVerifiedAt:  now,
	}
	if err := s.insertRecordTx(ctx, db, record, "user", note.SessionID); err != nil {
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
	if err := validateMaintenanceLeaseTx(ctx, tx, record.ProjectKey); err != nil {
		return err
	}
	if err := checkRecordSessionPurgeFenceTx(ctx, tx, record); err != nil {
		return err
	}
	if err := s.insertRecordSQLTx(ctx, tx, record, actor, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) insertRecordSQLTx(ctx context.Context, tx *sql.Tx, record Record, actor, sessionID string) error {
	return s.insertRecordSQLTxWithOptions(ctx, tx, record, actor, sessionID, recordInsertOptions{})
}

type recordInsertOptions struct {
	eventType     string
	changeType    string
	appendVersion bool
}

func (s *Store) insertRecordSQLTxWithOptions(
	ctx context.Context,
	tx *sql.Tx,
	record Record,
	actor, sessionID string,
	options recordInsertOptions,
) error {
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
	if err := validateRecordMetadata(&record); err != nil {
		return err
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	if record.ValidFrom.IsZero() {
		record.ValidFrom = record.CreatedAt
	}
	if err := s.normalizeRecordScope(ctx, &record); err != nil {
		return err
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
	record.SourceQuote = strings.TrimSpace(record.SourceQuote)
	if utf8.RuneCountInString(record.SourceQuote) > maxMemorySourceQuoteRunes {
		return fmt.Errorf("memorystore: source evidence quote exceeds %d runes", maxMemorySourceQuoteRunes)
	}
	if rule := memoryredact.DetectSecret(record.SourceQuote); rule != "" {
		return fmt.Errorf("memorystore: refusing to store secret-shaped source evidence (%s)", rule)
	}
	tagsJSON, _ := json.Marshal(record.Tags)
	refsJSON, _ := json.Marshal(record.SourceRefs)
	metadataJSON, _ := json.Marshal(record.Metadata)
	_, err := tx.ExecContext(ctx, `
INSERT INTO memory_entries (
    id, scope, project_key, kind, status, text, normalized_text, tags_json,
	    confidence, sensitivity, pinned, retention_class, source_kind,
	    source_session_id, source_session_generation, source_turn_id, source_refs_json, content_hash,
    supersedes_id, revision, created_at, updated_at, last_used_at, use_count,
    valid_from, valid_until, freshness_status, last_verified_at,
    revalidate_after, stale_reason, deleted_at, metadata_json,
    title, summary, canonical_id, superseded_by, archived,
    tenant_id, owner_id, agent_id, task_id, scope_kind, scope_key
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID, string(record.Scope), record.ProjectKey, record.Kind, string(record.Status),
		record.Text, record.NormalizedText, string(tagsJSON), record.Confidence, record.Sensitivity,
		boolInt(record.Pinned), record.RetentionClass, record.SourceKind,
		nullableString(record.SourceSessionID), record.SourceSessionGeneration, nullableString(record.SourceTurnID),
		string(refsJSON), record.ContentHash, nullableString(record.SupersedesID),
		record.Revision, record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano), nullableTime(record.LastUsedAt),
		record.UseCount, nullableTime(record.ValidFrom), nullableTime(record.ValidUntil),
		string(record.Freshness), nullableTime(record.LastVerifiedAt),
		nullableTime(record.RevalidateAfter), nullableString(record.StaleReason),
		nullableTime(record.DeletedAt), string(metadataJSON),
		record.Title, record.Summary, record.CanonicalID, nullableString(record.SupersededBy), boolInt(record.Archived),
		record.TenantID, record.OwnerID, record.AgentID, record.TaskID, string(record.ScopeKind), record.ScopeKey,
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
	eventType := strings.TrimSpace(options.eventType)
	if eventType == "" {
		eventType = "created"
	}
	eventID, err := insertEventIDTx(ctx, tx, record.ProjectKey, record.ID, eventType, actor, sessionID, record.Revision, map[string]interface{}{
		"kind": record.Kind, "source_kind": record.SourceKind,
	})
	if err != nil {
		return err
	}
	if options.appendVersion || (options.eventType == "" && options.changeType == "") {
		changeType := strings.TrimSpace(options.changeType)
		if changeType == "" {
			changeType = "create"
		}
		if err := appendCurrentRecordVersionTx(ctx, tx, record.ID, record.ProjectKey, actor, "record created", changeType, eventID); err != nil {
			return err
		}
	}
	return nil
}

func validateRecordMetadata(record *Record) error {
	if record == nil {
		return fmt.Errorf("memorystore: record metadata is nil")
	}
	record.Title = strings.TrimSpace(record.Title)
	record.Summary = strings.TrimSpace(record.Summary)
	record.CanonicalID = strings.TrimSpace(record.CanonicalID)
	record.SupersededBy = strings.TrimSpace(record.SupersededBy)
	if record.CanonicalID == "" {
		record.CanonicalID = record.ID
	}
	if len([]rune(record.Title)) > 200 {
		return fmt.Errorf("memorystore: record title exceeds 200 runes")
	}
	if len([]rune(record.Summary)) > 4000 {
		return fmt.Errorf("memorystore: record summary exceeds 4000 runes")
	}
	if len([]rune(record.CanonicalID)) > 200 {
		return fmt.Errorf("memorystore: canonical id exceeds 200 runes")
	}
	if len([]rune(record.SupersededBy)) > 200 {
		return fmt.Errorf("memorystore: superseded_by exceeds 200 runes")
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
	sourceType := strings.TrimSpace(record.SourceType)
	if sourceType == "" {
		switch strings.ToLower(strings.TrimSpace(record.SourceKind)) {
		case "manual":
			sourceType = "manual"
		case "turn_window":
			sourceType = "turn_window_summary"
		default:
			sourceType = "transcript"
		}
	}
	sourceTrust := strings.TrimSpace(record.SourceTrust)
	if sourceTrust == "" {
		sourceTrust = "unverified"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_sources (
	    id, memory_id, source_kind, source_id, session_id, source_session_generation, turn_id,
    source_window, message_ids_json, artifact_refs_json, fact_refs_json, knowledge_refs_json,
    source_hash, source_revision, source_updated_at, source_observed_at, quote,
    source_type, source_trust, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '[]', '[]', ?, ?, ?, ?, ?, ?, ?, ?)
	`, sourceID, record.ID, record.SourceKind, record.ID, nullableString(record.SourceSessionID),
		record.SourceSessionGeneration, nullableString(record.SourceTurnID), nullableString(record.SourceWindow), string(messageIDs), sourceHash,
		nullableInt64(record.SourceRevision),
		sourceUpdatedAt.UTC().Format(time.RFC3339Nano),
		sourceObservedAt.UTC().Format(time.RFC3339Nano), nullableString(record.SourceQuote),
		sourceType, sourceTrust, nowText()); err != nil {
		return fmt.Errorf("memorystore: insert source: %w", err)
	}
	return nil
}

func insertEventTx(ctx context.Context, tx *sql.Tx, projectKey, memoryID, eventType, actor, sessionID string, revision int64, payload map[string]interface{}) error {
	_, err := insertEventIDTx(ctx, tx, projectKey, memoryID, eventType, actor, sessionID, revision, payload)
	return err
}

func insertEventIDTx(ctx context.Context, tx *sql.Tx, projectKey, memoryID, eventType, actor, sessionID string, revision int64, payload map[string]interface{}) (string, error) {
	body, _ := json.Marshal(payload)
	eventID := "evt_" + uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_events (id, project_key, memory_id, event_type, actor, session_id, revision, payload_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, eventID, projectKey, memoryID, eventType, actor, nullableString(sessionID), revision, string(body), nowText()); err != nil {
		return "", fmt.Errorf("memorystore: insert event: %w", err)
	}
	return eventID, nil
}

func (s *Store) listV2(limit int) ([]Note, error) {
	return s.listV2WithOptions(ListOptions{Limit: limit})
}

func (s *Store) listV2WithOptions(opts ListOptions) ([]Note, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	scopePredicate, scopeArgs, err := s.memoryScopeVisibility(context.Background(), "", ScopePermissionRead, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	query := `
SELECT id, text, tags_json, source_kind, source_session_id, created_at,
       title, summary, canonical_id, archived
FROM memory_entries
WHERE ` + scopePredicate + ` AND status = 'active' AND archived = 0`
	args := append([]interface{}(nil), scopeArgs...)
	if sourceKind := strings.ToLower(strings.TrimSpace(opts.SourceKind)); sourceKind != "" {
		query += " AND LOWER(source_kind) = ?"
		args = append(args, sourceKind)
	}
	// Keep the query construction above easy to audit: source filtering is
	// parameterized and the stable ordering remains identical for all callers.
	query += "\nORDER BY updated_at DESC, id DESC"
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
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
			title, summary, canonicalID               sql.NullString
			createdAt                                 string
			archived                                  int
		)
		if err := rows.Scan(&id, &text, &tagsJSON, &sourceKind, &sessionID, &createdAt,
			&title, &summary, &canonicalID, &archived); err != nil {
			return nil, err
		}
		note := Note{
			ID: id.String, Text: text.String, Source: sourceKind.String, SessionID: sessionID.String,
			Title: title.String, Summary: summary.String, CanonicalID: canonicalID.String,
			Archived: archived != 0,
		}
		_ = json.Unmarshal([]byte(tagsJSON.String), &note.Tags)
		note.CreatedAt = parseTime(createdAt)
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

func (s *Store) searchV2(opts SearchOptions) ([]SearchHit, error) {
	query := strings.TrimSpace(opts.Query)
	if query != "" {
		if hits, err := s.searchV2FTS(context.Background(), opts); err == nil && len(hits) > 0 {
			return hits, nil
		}
	}
	notes, err := s.listV2WithOptions(ListOptions{SourceKind: opts.SourceKind})
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	requiredTags := normalizeTags(opts.Tags)
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
	return s.selectForInjectV2Internal(opts, false)
}

// DiagnoseInjection runs the normal selection pipeline but also explains why
// otherwise eligible records were excluded (status, freshness, privacy,
// expiry, suppression, budget or query mismatch). It never writes a recall
// trace and never exposes record text.
func (s *Store) DiagnoseInjection(opts InjectOptions) (Selection, error) {
	return s.selectForInjectV2Internal(opts, true)
}

func (s *Store) selectForInjectV2Internal(opts InjectOptions, diagnostic bool) (Selection, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Selection{}, err
	}
	ctx := context.Background()
	if opts.Scope.TenantID != "" || opts.Scope.ProjectKey != "" || opts.Scope.OwnerID != "" ||
		opts.Scope.AgentID != "" || opts.Scope.SessionID != "" || opts.Scope.TaskID != "" || opts.Scope.Scope != "" {
		ctx = WithMemoryScope(ctx, opts.Scope)
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
	var (
		records           []Record
		diagnosticRecords []Record
		initialDropped    []DroppedRecord
	)
	if diagnostic {
		diagnosticRecords, err = s.loadRecordsForInjectionDiagnostics(ctx, db, 500)
		if err != nil {
			return Selection{}, err
		}
		records, initialDropped, err = s.classifyInjectableRecords(ctx, db, diagnosticRecords, now)
		if err != nil {
			return Selection{}, err
		}
	} else {
		records, err = s.loadInjectableCandidates(ctx, db, opts, now)
		if err != nil {
			return Selection{}, err
		}
	}
	mode := strings.ToLower(strings.TrimSpace(s.cfg.InjectionMode))
	if mode != "summary" && mode != "hybrid" && mode != "notes" {
		mode = "notes"
	}
	supportsSummary := mode == "summary" || mode == "hybrid"
	supportsRecall := mode == "hybrid" || mode == "notes"
	candidateCount := len(records)
	if diagnostic {
		candidateCount = len(diagnosticRecords)
	}
	selection := Selection{CandidateCount: candidateCount, Dropped: initialDropped}
	categoryBudgetsEnabled := s.cfg.CategoryBudgetsEnabled || s.cfg.CategoryBudgets.enabled()
	if categoryBudgetsEnabled {
		selection.CategoryBudget = s.cfg.CategoryBudgets.allocation(budget)
		selection.CategoryUsed = map[string]int{
			InjectionCategoryCore: 0, InjectionCategoryTask: 0,
			InjectionCategoryRetrieved: 0, InjectionCategoryProcedure: 0,
			InjectionCategoryReference: 0,
		}
	}
	categoryRemaining := func(category string) int {
		if !categoryBudgetsEnabled {
			return budget
		}
		remaining := selection.CategoryBudget[category] - selection.CategoryUsed[category]
		if remaining < 0 {
			return 0
		}
		return remaining
	}
	coreUsed := 0
	if core, coreErr := s.GetCore(context.Background()); coreErr == nil {
		// Core is the highest-priority stable item, but it must still fit inside
		// the caller's project-memory budget. Keep reviewed content intact rather
		// than truncating it into a potentially misleading partial assertion.
		coreText := fmt.Sprintf("[reviewed project core v%d] %s",
			core.Version, strings.TrimSpace(core.Content))
		coreCost := EstimateTokens(coreText)
		coreCap := budget
		if categoryBudgetsEnabled && selection.CategoryBudget[InjectionCategoryCore] < coreCap {
			coreCap = selection.CategoryBudget[InjectionCategoryCore]
		}
		if coreCost <= coreCap {
			selection.Core = &core
			coreUsed = coreCost
			if categoryBudgetsEnabled {
				selection.CategoryUsed[InjectionCategoryCore] += coreCost
			}
		} else {
			dropped := DroppedRecord{ID: core.ID, Reason: "core_budget"}
			if categoryBudgetsEnabled {
				dropped.Category = InjectionCategoryCore
			}
			selection.Dropped = append(selection.Dropped, dropped)
		}
	} else if !errors.Is(coreErr, sql.ErrNoRows) &&
		!errors.Is(coreErr, ErrCoreSourceIneligible) {
		return Selection{}, coreErr
	}
	pinnedBudget := int(float64(budget) * s.cfg.PinnedBudgetRatio)
	if available := budget - coreUsed; pinnedBudget > available {
		pinnedBudget = available
	}
	pinnedUsed := 0
	stableIDs := map[string]struct{}{}
	for _, record := range records {
		if !record.Pinned && strings.ToLower(record.SourceKind) != "manual" {
			continue
		}
		category := injectionCategoryForRecord(record)
		cost := EstimateTokens(record.Text) + memoryRecordTokenOverhead
		available := pinnedBudget - pinnedUsed
		if categoryCap := categoryRemaining(category); categoryBudgetsEnabled && categoryCap < available {
			available = categoryCap
		}
		if cost > available {
			if len(selection.Stable) == 0 {
				if fitted, ok := fitRecordToBudget(record, available); ok {
					record = fitted
					cost = EstimateTokens(record.Text) + memoryRecordTokenOverhead
				} else {
					reason := "pinned_budget"
					if categoryBudgetsEnabled && categoryRemaining(category) < pinnedBudget-pinnedUsed {
						reason = "category_budget"
					}
					dropped := DroppedRecord{ID: record.ID, Reason: reason}
					if reason == "category_budget" {
						dropped.Category = category
					}
					selection.Dropped = append(selection.Dropped, dropped)
					continue
				}
			} else {
				reason := "pinned_budget"
				if categoryBudgetsEnabled && categoryRemaining(category) < pinnedBudget-pinnedUsed {
					reason = "category_budget"
				}
				dropped := DroppedRecord{ID: record.ID, Reason: reason}
				if reason == "category_budget" {
					dropped.Category = category
				}
				selection.Dropped = append(selection.Dropped, dropped)
				continue
			}
		}
		selection.Stable = append(selection.Stable, record)
		stableIDs[record.ID] = struct{}{}
		pinnedUsed += cost
		if categoryBudgetsEnabled {
			selection.CategoryUsed[category] += cost
		}
	}
	summaryUsed := 0
	if supportsSummary {
		if summary, err := s.loadGatedActiveSummary(context.Background(), db, now); err == nil && summary != nil {
			summary.TokenEstimate = EstimateTokens(summary.Text)
			summaryCost := summary.TokenEstimate
			summaryCap := budget - coreUsed - pinnedUsed
			if categoryBudgetsEnabled && categoryRemaining(InjectionCategoryRetrieved) < summaryCap {
				summaryCap = categoryRemaining(InjectionCategoryRetrieved)
			}
			if summaryCap < 0 {
				summaryCap = 0
			}
			switch mode {
			case "summary":
				if len(selection.Stable) > 0 {
					if modeCap := budget * 80 / 100; summaryCap > modeCap {
						summaryCap = modeCap
					}
				}
			case "hybrid":
				if modeCap := budget * 50 / 100; summaryCap > modeCap {
					summaryCap = modeCap
				}
			}
			if summaryCost <= summaryCap &&
				summaryCost+coreUsed+pinnedUsed <= budget {
				selection.Summary = summary
				selection.Revision = summary.Revision
				summaryUsed = summaryCost
				if categoryBudgetsEnabled {
					selection.CategoryUsed[InjectionCategoryRetrieved] += summaryCost
				}
				for _, id := range summary.RecordIDs {
					stableIDs[id] = struct{}{}
				}
			} else {
				dropped := DroppedRecord{ID: summary.ID, Reason: "summary_budget"}
				if categoryBudgetsEnabled && summaryCost > categoryRemaining(InjectionCategoryRetrieved) {
					dropped.Reason = "category_budget"
					dropped.Category = InjectionCategoryRetrieved
				}
				selection.Dropped = append(selection.Dropped, dropped)
			}
		}
	}
	candidates := make([]recallCandidate, 0, len(records))
	queryTokens := tokenize(opts.Query)
	for recordIndex, record := range records {
		if _, exists := stableIDs[record.ID]; exists {
			continue
		}
		score := scoreRecordForRecall(record, queryTokens, opts.Query, now)
		if opts.Query != "" && score <= 0 {
			continue
		}
		candidates = append(candidates, recallCandidate{recordIndex: recordIndex, score: score})
	}
	if categoryBudgetsEnabled {
		candidateCategories := make(map[string]bool, len(candidates))
		if supportsRecall {
			for _, candidate := range candidates {
				if candidate.recordIndex >= 0 && candidate.recordIndex < len(records) {
					candidateCategories[injectionCategoryForRecord(records[candidate.recordIndex])] = true
				}
			}
		}
		for _, category := range []string{
			InjectionCategoryCore, InjectionCategoryTask,
			InjectionCategoryProcedure, InjectionCategoryReference,
		} {
			// A category with no selectable candidate releases its unused
			// allocation to retrieved. Existing use (for example a pinned task
			// item or active Core) remains charged to the original category.
			if category != InjectionCategoryCore && candidateCategories[category] {
				continue
			}
			unused := selection.CategoryBudget[category] - selection.CategoryUsed[category]
			if unused > 0 {
				selection.CategoryBudget[InjectionCategoryRetrieved] += unused
				selection.CategoryBudget[category] = selection.CategoryUsed[category]
			}
		}
	}
	if !supportsRecall {
		selection.TokenEstimate = coreUsed + summaryUsed + pinnedUsed
		selection.IDs = appendCoreSelectionID(selection.IDs, selection.Core)
		selection.IDs = appendSelectionIDs(selection.IDs, selection.Stable, selection.Summary)
		if !diagnostic {
			if err := s.hydrateSelectionRecords(context.Background(), db, &selection); err != nil {
				return Selection{}, err
			}
		}
		if diagnostic {
			appendWhyNotNoMatch(&selection, diagnosticRecords, opts.Query)
		}
		selection.FilteredCount = len(selection.Dropped)
		if !opts.SkipTrace {
			_ = s.recordRecallTrace(opts, selection)
		}
		return selection, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return records[candidates[i].recordIndex].CreatedAt.After(
				records[candidates[j].recordIndex].CreatedAt)
		}
		return candidates[i].score > candidates[j].score
	})
	candidates = diversifyRecallOrder(candidates, records, limit)
	if categoryBudgetsEnabled {
		candidates = prioritizeRecallCandidates(candidates, records)
	}
	remaining := budget - coreUsed - summaryUsed - pinnedUsed
	if remaining < 0 {
		remaining = 0
	}
	recallCap := budget * 80 / 100
	if mode == "hybrid" {
		recallCap = budget * 30 / 100
	}
	recallUsed := 0
	seenDuplicates := make(map[string]struct{}, len(candidates))
	for _, hit := range candidates {
		if hit.recordIndex < 0 || hit.recordIndex >= len(records) {
			continue
		}
		record := records[hit.recordIndex]
		if key := recallDedupKey(record); key != "" {
			if _, seen := seenDuplicates[key]; seen {
				selection.Dropped = append(selection.Dropped, DroppedRecord{ID: record.ID, Reason: "duplicate"})
				continue
			}
			seenDuplicates[key] = struct{}{}
		}
		if len(selection.Recall) >= limit {
			dropped := DroppedRecord{ID: record.ID, Reason: "memory_budget"}
			if categoryBudgetsEnabled {
				dropped.Category = injectionCategoryForRecord(record)
			}
			selection.Dropped = append(selection.Dropped, dropped)
			continue
		}
		category := injectionCategoryForRecord(record)
		cost := EstimateTokens(record.Text) + memoryRecordTokenOverhead
		perRecordCap := remaining
		recallRemaining := recallCap - recallUsed
		if recallRemaining < perRecordCap {
			perRecordCap = recallRemaining
		}
		categoryLimited := false
		if categoryBudgetsEnabled {
			categoryCap := categoryRemaining(category)
			if categoryCap < perRecordCap {
				perRecordCap = categoryCap
				categoryLimited = true
			}
		}
		if cost > perRecordCap {
			if len(selection.Recall) == 0 {
				if fitted, ok := fitRecordToBudget(record, perRecordCap); ok {
					record = fitted
					cost = EstimateTokens(record.Text) + memoryRecordTokenOverhead
				} else {
					reason := "memory_budget"
					if categoryLimited {
						reason = "category_budget"
					}
					dropped := DroppedRecord{ID: record.ID, Reason: reason}
					if categoryBudgetsEnabled {
						dropped.Category = category
					}
					selection.Dropped = append(selection.Dropped, dropped)
					continue
				}
			} else {
				reason := "memory_budget"
				if categoryLimited {
					reason = "category_budget"
				}
				dropped := DroppedRecord{ID: record.ID, Reason: reason}
				if categoryBudgetsEnabled {
					dropped.Category = category
				}
				selection.Dropped = append(selection.Dropped, dropped)
				continue
			}
		}
		selection.Recall = append(selection.Recall, record)
		recallUsed += cost
		if categoryBudgetsEnabled {
			selection.CategoryUsed[category] += cost
		}
	}
	selection.TokenEstimate = coreUsed + summaryUsed + pinnedUsed + recallUsed
	selection.IDs = appendCoreSelectionID(selection.IDs, selection.Core)
	selection.IDs = appendSelectionIDs(selection.IDs, selection.Stable, selection.Summary)
	for _, record := range selection.Recall {
		selection.IDs = append(selection.IDs, record.ID)
	}
	if diagnostic {
		appendWhyNotNoMatch(&selection, diagnosticRecords, opts.Query)
	}
	if !diagnostic {
		if err := s.hydrateSelectionRecords(context.Background(), db, &selection); err != nil {
			return Selection{}, err
		}
	}
	selection.FilteredCount = len(selection.Dropped)
	if !opts.SkipTrace {
		_ = s.recordRecallTrace(opts, selection)
	}
	return selection, nil
}

func (s *Store) loadRecordsForInjectionDiagnostics(ctx context.Context, db *sql.DB, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 500
	}
	scopePredicate, scopeArgs, err := s.memoryScopeVisibility(ctx, "", ScopePermissionRead, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	args := append([]interface{}(nil), scopeArgs...)
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_session_generation,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json, title, summary, canonical_id, superseded_by, archived,
       tenant_id, owner_id, agent_id, task_id, scope_kind, scope_key
 FROM memory_entries
WHERE `+scopePredicate+`
ORDER BY pinned DESC, use_count DESC, updated_at DESC, id ASC
LIMIT ?
`, args...)
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
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) classifyInjectableRecords(ctx context.Context, db *sql.DB, records []Record, now time.Time) ([]Record, []DroppedRecord, error) {
	suppressed, err := s.loadActiveSuppressionHashes(ctx, db, now)
	if err != nil {
		return nil, nil, err
	}
	eligible := make([]Record, 0, len(records))
	dropped := make([]DroppedRecord, 0)
	for _, record := range records {
		reason := ""
		switch {
		case record.Archived:
			reason = "archived"
		case !record.DeletedAt.IsZero():
			reason = "deleted"
		case record.Status != StatusActive:
			reason = "status_" + strings.ToLower(string(record.Status))
		case !record.ValidUntil.IsZero() && !record.ValidUntil.After(now):
			reason = "expired"
		case strings.EqualFold(record.Sensitivity, "private"):
			reason = "private"
		case record.Freshness != FreshnessFresh &&
			!(s.cfg.StaleBehavior == "warn" && record.Freshness == FreshnessStale && warnInjectableKind(record.Kind)):
			if strings.TrimSpace(string(record.Freshness)) == "" {
				reason = "unverified"
			} else {
				reason = strings.ToLower(string(record.Freshness))
			}
		case !s.cfg.DisableSecretFilter && memoryredact.DetectSecret(record.Text) != "":
			reason = "secret"
		default:
			if _, ok := suppressed[record.ContentHash]; ok {
				reason = "suppressed"
			}
		}
		if reason != "" {
			dropped = append(dropped, DroppedRecord{ID: record.ID, Reason: reason})
			continue
		}
		eligible = append(eligible, record)
	}
	return eligible, dropped, nil
}

func (s *Store) loadActiveSuppressionHashes(ctx context.Context, db *sql.DB, now time.Time) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `
SELECT content_hash FROM memory_suppressions
WHERE scope = ? AND project_key = ?
  AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
`, string(ScopeProject), s.cfg.ProjectKey, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out[hash] = struct{}{}
	}
	return out, rows.Err()
}

func appendWhyNotNoMatch(selection *Selection, records []Record, query string) {
	if selection == nil || strings.TrimSpace(query) == "" {
		return
	}
	selected := make(map[string]struct{}, len(selection.IDs))
	for _, id := range selection.IDs {
		selected[id] = struct{}{}
	}
	explained := make(map[string]struct{}, len(selection.Dropped))
	for _, item := range selection.Dropped {
		explained[item.ID] = struct{}{}
	}
	for _, record := range records {
		if _, ok := selected[record.ID]; ok {
			continue
		}
		if _, ok := explained[record.ID]; ok {
			continue
		}
		selection.Dropped = append(selection.Dropped, DroppedRecord{ID: record.ID, Reason: "no_match"})
	}
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

func appendCoreSelectionID(ids []string, core *CoreMemory) []string {
	if core == nil {
		return ids
	}
	ids = append(ids, core.ID)
	return uniqueStrings(ids)
}

func (s *Store) loadInjectableRecords(ctx context.Context, db *sql.DB, now time.Time) ([]Record, error) {
	freshnessPredicate := "freshness_status = 'fresh'"
	if s.cfg.StaleBehavior == "warn" {
		// staleBehavior=warn is deliberately narrow: high-risk kinds
		// (fact/reference plus decisions) never enter ordinary recall merely
		// because they are old.
		freshnessPredicate = `(freshness_status = 'fresh' OR
  (freshness_status = 'stale' AND kind IN ('preference', 'workflow', 'pitfall', 'open_question')))`
	}
	scopePredicate, scopeArgs, err := s.memoryScopeVisibility(ctx, "", ScopePermissionRead, now)
	if err != nil {
		return nil, err
	}
	args := append([]interface{}(nil), scopeArgs...)
	args = append(args, now.UTC().Format(time.RFC3339Nano))
	rows, err := db.QueryContext(ctx, `
SELECT id, kind, text, normalized_text, tags_json, confidence, pinned,
       source_kind, source_session_id, created_at, updated_at, last_used_at,
       use_count
FROM memory_entries
WHERE `+scopePredicate+` AND status = 'active' AND `+freshnessPredicate+`
  AND sensitivity != 'private'
  AND archived = 0
  AND (deleted_at IS NULL OR deleted_at = '')
  AND (valid_until IS NULL OR valid_until = '' OR valid_until > ?)
ORDER BY pinned DESC, CASE WHEN freshness_status = 'stale' THEN 1 ELSE 0 END,
         use_count DESC, updated_at DESC
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, 32)
	for rows.Next() {
		record, err := scanInjectableRecord(rows)
		if err != nil {
			return nil, err
		}
		if !s.cfg.DisableSecretFilter && memoryredact.DetectSecret(record.Text) != "" {
			continue
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

const (
	// Injection remains bounded even when the project has thousands of active
	// records. Pinned/manual items are loaded first as a stable-prefix floor;
	// query recall is then served by the FTS index (or a bounded recent list
	// when the query is empty).
	maxInjectablePinnedCandidates = 100
	maxInjectableManualCandidates = 100
	maxInjectableRecallCandidates = 200
)

func (s *Store) loadInjectableCandidates(
	ctx context.Context, db *sql.DB, opts InjectOptions, now time.Time,
) ([]Record, error) {
	_ = db // ListRecords resolves the same bounded, project-scoped database.
	pinned, err := s.ListRecords(ctx, RecordFilter{
		Status: StatusActive, PinnedOnly: true, Limit: maxInjectablePinnedCandidates,
	})
	if err != nil {
		return nil, err
	}
	manual, err := s.ListRecords(ctx, RecordFilter{
		Status: StatusActive, SourceKind: "manual", Limit: maxInjectableManualCandidates,
	})
	if err != nil {
		return nil, err
	}
	recallFilter := RecordFilter{
		Status: StatusActive, Limit: maxInjectableRecallCandidates,
	}
	if query := strings.TrimSpace(opts.Query); query != "" {
		recallFilter.Query = query
	}
	recall, err := s.ListRecords(ctx, recallFilter)
	if err != nil {
		return nil, err
	}

	merged := make([]Record, 0, len(pinned.Items)+len(manual.Items)+len(recall.Items))
	seen := make(map[string]struct{}, len(merged))
	appendRecords := func(items []Record) {
		for _, record := range items {
			if _, exists := seen[record.ID]; exists {
				continue
			}
			seen[record.ID] = struct{}{}
			merged = append(merged, record)
		}
	}
	// Preserve the stable-prefix order that the previous full scan exposed:
	// pinned first, then manual records, then query/recent recall candidates.
	sort.SliceStable(pinned.Items, func(i, j int) bool {
		left, right := pinned.Items[i], pinned.Items[j]
		if left.UseCount != right.UseCount {
			return left.UseCount > right.UseCount
		}
		return left.UpdatedAt.After(right.UpdatedAt)
	})
	sort.SliceStable(manual.Items, func(i, j int) bool {
		left, right := manual.Items[i], manual.Items[j]
		if left.UseCount != right.UseCount {
			return left.UseCount > right.UseCount
		}
		return left.UpdatedAt.After(right.UpdatedAt)
	})
	appendRecords(pinned.Items)
	appendRecords(manual.Items)
	appendRecords(recall.Items)

	eligible, _, err := s.classifyInjectableRecords(ctx, db, merged, now)
	if err != nil {
		return nil, err
	}
	return eligible, nil
}

// scanInjectableRecord is the bounded projection used by the per-turn recall
// selector. It intentionally omits provenance JSON and lifecycle fields that
// neither scoring nor prompt rendering reads. Full records remain available
// through GetRecord and the diagnostic injection path.
func scanInjectableRecord(rows *sql.Rows) (Record, error) {
	var (
		record                                   Record
		id, kind, text, normalizedText, tagsJSON sql.NullString
		sourceKind, sourceSessionID              sql.NullString
		createdAt, updatedAt, lastUsedAt         sql.NullString
		confidence                               float64
		pinned                                   int
		useCount                                 int64
	)
	if err := rows.Scan(
		&id, &kind, &text, &normalizedText, &tagsJSON, &confidence, &pinned,
		&sourceKind, &sourceSessionID, &createdAt, &updatedAt, &lastUsedAt,
		&useCount,
	); err != nil {
		return Record{}, err
	}
	record = Record{
		ID: id.String, Kind: kind.String, Text: text.String,
		NormalizedText: normalizedText.String, Confidence: confidence,
		Pinned: pinned != 0, SourceKind: sourceKind.String,
		SourceSessionID: sourceSessionID.String,
		CreatedAt:       parseTime(createdAt.String),
		UpdatedAt:       parseTime(updatedAt.String),
		LastUsedAt:      parseTime(lastUsedAt.String),
		UseCount:        int(useCount),
	}
	_ = json.Unmarshal([]byte(tagsJSON.String), &record.Tags)
	return record, nil
}

func (s *Store) hydrateSelectionRecords(
	ctx context.Context,
	db *sql.DB,
	selection *Selection,
) error {
	if selection == nil {
		return nil
	}
	ids := make([]string, 0, len(selection.Stable)+len(selection.Recall))
	seen := make(map[string]struct{}, len(selection.Stable)+len(selection.Recall))
	for _, record := range selection.Stable {
		if _, ok := seen[record.ID]; ok || strings.TrimSpace(record.ID) == "" {
			continue
		}
		seen[record.ID] = struct{}{}
		ids = append(ids, record.ID)
	}
	for _, record := range selection.Recall {
		if _, ok := seen[record.ID]; ok || strings.TrimSpace(record.ID) == "" {
			continue
		}
		seen[record.ID] = struct{}{}
		ids = append(ids, record.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	scopePredicate, scopeArgs, err := s.memoryScopeVisibility(ctx, "", ScopePermissionRead, time.Now().UTC())
	if err != nil {
		return err
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+len(scopeArgs))
	args = append(args, scopeArgs...)
	for index, id := range ids {
		placeholders[index] = "?"
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, scope, project_key, kind, status, text, normalized_text, tags_json,
       confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
       source_session_generation,
       source_turn_id, source_refs_json, content_hash, supersedes_id, revision,
       created_at, updated_at, last_used_at, use_count, valid_from, valid_until,
       freshness_status, last_verified_at, revalidate_after, stale_reason,
       deleted_at, metadata_json, title, summary, canonical_id, superseded_by, archived,
       tenant_id, owner_id, agent_id, task_id, scope_kind, scope_key
 FROM memory_entries
WHERE `+scopePredicate+` AND id IN (`+strings.Join(placeholders, ", ")+`)
`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	fullByID := make(map[string]Record, len(ids))
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return scanErr
		}
		fullByID[record.ID] = record
	}
	if err := rows.Err(); err != nil {
		return err
	}
	hydrate := func(records []Record) {
		for index, selected := range records {
			full, ok := fullByID[selected.ID]
			if !ok {
				continue
			}
			// Preserve the already fitted text; all other fields come from the
			// authoritative row.
			full.Text = selected.Text
			records[index] = full
		}
	}
	hydrate(selection.Stable)
	hydrate(selection.Recall)
	return nil
}

// warnInjectableKind is the staleBehavior=warn allowlist. High-risk kinds
// (fact/reference) and durable decisions are intentionally absent: an old
// decision must be revalidated, not rendered with a warning label.
func warnInjectableKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "preference", "workflow", "pitfall", "open_question":
		return true
	default:
		return false
	}
}
