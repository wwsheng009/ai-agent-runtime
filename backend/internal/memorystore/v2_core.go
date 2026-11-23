package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

var (
	ErrCoreSourceIneligible = errors.New("memorystore: active core sources are no longer eligible")
	ErrCoreNotCandidate     = errors.New("memorystore: core is not an activatable candidate")
)

const (
	CoreStatusCandidate = "candidate"
	CoreStatusActive    = "active"
	CoreStatusStale     = "stale"
	CoreStatusArchived  = "archived"

	defaultCoreTokenBudget = 1200
	maxCoreTokenBudget     = 4000
	maxCoreContentRunes    = 16000
	maxCoreSourceIDs       = 64
	maxCoreActorRunes      = 200
)

// CoreMemory is the durable, reviewable project memory prefix. It is kept
// separate from memory_entries so a generated core can never silently replace
// ordinary records or the active project summary.
type CoreMemory struct {
	ID            string    `json:"id"`
	ProjectKey    string    `json:"project_key,omitempty"`
	ScopeKind     Scope     `json:"scope_kind"`
	ScopeKey      string    `json:"scope_key"`
	Content       string    `json:"content"`
	Version       int64     `json:"version"`
	TokenEstimate int       `json:"token_estimate"`
	Status        string    `json:"status"`
	SourceIDs     []string  `json:"source_ids,omitempty"`
	GeneratedBy   string    `json:"generated_by,omitempty"`
	ApprovedBy    string    `json:"approved_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// CoreMemoryCandidateOptions describes one deterministic candidate proposal.
// Model-generated callers must still provide the evidence ids; this API does
// not permit a model to write an active core directly.
type CoreMemoryCandidateOptions struct {
	ScopeKind   Scope
	ScopeKey    string
	Content     string
	SourceIDs   []string
	GeneratedBy string
	TokenBudget int
	Reason      string
}

type CoreMemoryFilter struct {
	ScopeKind Scope
	ScopeKey  string
	Status    string
	Limit     int
}

func (s *Store) normalizeCoreScope(scopeKind Scope, scopeKey string) (Scope, string, error) {
	if s == nil {
		return "", "", fmt.Errorf("memorystore: store is nil")
	}
	scopeKind = Scope(strings.ToLower(strings.TrimSpace(string(scopeKind))))
	if scopeKind == "" {
		scopeKind = ScopeProject
	}
	if scopeKind != ScopeProject {
		return "", "", fmt.Errorf("memorystore: core currently supports project scope only")
	}
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		scopeKey = strings.TrimSpace(s.cfg.ProjectKey)
	}
	if scopeKey == "" || scopeKey != strings.TrimSpace(s.cfg.ProjectKey) {
		return "", "", fmt.Errorf("memorystore: core scope key must match the current project")
	}
	return scopeKind, scopeKey, nil
}

func normalizeCoreCandidate(opts CoreMemoryCandidateOptions) (CoreMemoryCandidateOptions, error) {
	opts.Content = strings.TrimSpace(opts.Content)
	if opts.Content == "" {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core content is required")
	}
	if utf8.RuneCountInString(opts.Content) > maxCoreContentRunes {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core content exceeds %d runes", maxCoreContentRunes)
	}
	if rule := memoryredact.DetectSecret(opts.Content); rule != "" {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: refusing to store secret-shaped core content (%s)", rule)
	}
	if opts.TokenBudget <= 0 {
		opts.TokenBudget = defaultCoreTokenBudget
	}
	if opts.TokenBudget > maxCoreTokenBudget {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core token budget exceeds %d", maxCoreTokenBudget)
	}
	if EstimateTokens(opts.Content) > opts.TokenBudget {
		opts.Content = truncateCoreToBudget(opts.Content, opts.TokenBudget)
	}
	if opts.Content == "" {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core content is empty after token budgeting")
	}
	actor := strings.TrimSpace(opts.GeneratedBy)
	if actor == "" {
		actor = "system:core_rebuild"
	}
	if utf8.RuneCountInString(actor) > maxCoreActorRunes {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core generator identity is too long")
	}
	opts.GeneratedBy = actor
	ids := make([]string, 0, len(opts.SourceIDs))
	seen := make(map[string]struct{}, len(opts.SourceIDs))
	for _, raw := range opts.SourceIDs {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 256 {
			return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core source id is invalid")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > maxCoreSourceIDs {
			return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core source list exceeds %d ids", maxCoreSourceIDs)
		}
	}
	if len(ids) == 0 {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core requires at least one source id")
	}
	opts.SourceIDs = ids
	opts.Reason = strings.TrimSpace(opts.Reason)
	if opts.Reason == "" {
		opts.Reason = "core candidate generated from reviewed sources"
	}
	if utf8.RuneCountInString(opts.Reason) > 500 {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: core reason is too long")
	}
	if rule := memoryredact.DetectSecret(opts.Reason); rule != "" {
		return CoreMemoryCandidateOptions{}, fmt.Errorf("memorystore: refusing to store secret-shaped core reason (%s)", rule)
	}
	return opts, nil
}

func truncateCoreToBudget(text string, budget int) string {
	if budget <= 0 || EstimateTokens(text) <= budget {
		return strings.TrimSpace(text)
	}
	runes := []rune(strings.TrimSpace(text))
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		candidate := strings.TrimSpace(string(runes[:mid])) + "…"
		if EstimateTokens(candidate) <= budget {
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

func validateCoreSourcesTx(ctx context.Context, tx *sql.Tx, projectKey string, sourceIDs []string) error {
	for _, sourceID := range sourceIDs {
		var status, sensitivity, text, freshness string
		var archived int
		var validUntil, deletedAt sql.NullString
		err := tx.QueryRowContext(ctx, `
SELECT status, sensitivity, text, archived, freshness_status, valid_until, deleted_at
FROM memory_entries
WHERE id = ? AND project_key = ? AND scope = ?
`, sourceID, projectKey, string(ScopeProject)).Scan(
			&status, &sensitivity, &text, &archived, &freshness, &validUntil, &deletedAt,
		)
		if err == sql.ErrNoRows {
			return fmt.Errorf("memorystore: core source %q is not in the current project", sourceID)
		}
		if err != nil {
			return fmt.Errorf("memorystore: validate core source %q: %w", sourceID, err)
		}
		if archived != 0 ||
			!strings.EqualFold(strings.TrimSpace(status), string(StatusActive)) ||
			!strings.EqualFold(strings.TrimSpace(freshness), string(FreshnessFresh)) ||
			strings.TrimSpace(deletedAt.String) != "" ||
			strings.EqualFold(strings.TrimSpace(sensitivity), "private") {
			return fmt.Errorf("memorystore: core source %q is not eligible", sourceID)
		}
		if strings.TrimSpace(validUntil.String) != "" {
			expiry := parseTime(validUntil.String)
			if expiry.IsZero() || !expiry.After(time.Now().UTC()) {
				return fmt.Errorf("memorystore: core source %q is not eligible", sourceID)
			}
		}
		if rule := memoryredact.DetectSecret(text); rule != "" {
			return fmt.Errorf("memorystore: core source %q contains secret-shaped content", sourceID)
		}
	}
	return nil
}

// CreateCoreCandidate appends a candidate core version. It validates that every
// source id belongs to the current project and is agent-safe; source ids are
// never accepted as an unverified string list.
func (s *Store) CreateCoreCandidate(ctx context.Context, opts CoreMemoryCandidateOptions) (CoreMemory, error) {
	if !s.v2Configured() {
		return CoreMemory{}, errV2Unavailable
	}
	scopeKind, scopeKey, err := s.normalizeCoreScope(opts.ScopeKind, opts.ScopeKey)
	if err != nil {
		return CoreMemory{}, err
	}
	opts.ScopeKind, opts.ScopeKey = scopeKind, scopeKey
	opts, err = normalizeCoreCandidate(opts)
	if err != nil {
		return CoreMemory{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return CoreMemory{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: begin core candidate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	core, err := s.createCoreCandidateTx(ctx, tx, opts)
	if err != nil {
		return CoreMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: commit core candidate: %w", err)
	}
	return core, nil
}

// CreateCoreCandidateForWorker writes the candidate and completes its
// core_rebuild job in one transaction. A crash can therefore leave either
// (a) a retryable job and no candidate, or (b) one durable candidate and a
// succeeded job, but never duplicate candidates from a retry.
func (s *Store) CreateCoreCandidateForWorker(
	ctx context.Context,
	opts CoreMemoryCandidateOptions,
	job MemoryJob,
	watermark string,
) (CoreMemory, error) {
	if !s.v2Configured() {
		return CoreMemory{}, errV2Unavailable
	}
	if strings.TrimSpace(job.ID) == "" || job.Kind != "core_rebuild" ||
		strings.TrimSpace(job.WorkerID) == "" || job.Attempt <= 0 {
		return CoreMemory{}, fmt.Errorf("memorystore: core candidate commit requires a claimed core_rebuild job")
	}
	scopeKind, scopeKey, err := s.normalizeCoreScope(opts.ScopeKind, opts.ScopeKey)
	if err != nil {
		return CoreMemory{}, err
	}
	opts.ScopeKind, opts.ScopeKey = scopeKind, scopeKey
	opts, err = normalizeCoreCandidate(opts)
	if err != nil {
		return CoreMemory{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return CoreMemory{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: begin core rebuild commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return CoreMemory{}, err
	}
	if err := checkJobPurgeFenceByIDTx(ctx, tx, job.ID, s.cfg.ProjectKey); err != nil {
		return CoreMemory{}, err
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	var owned int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_jobs
WHERE id = ? AND project_key = ? AND kind = 'core_rebuild'
  AND status = 'running' AND worker_id = ? AND attempt = ?
  AND COALESCE(lease_until, '') > ? AND COALESCE(deadline_at, '') > ?
`, job.ID, s.cfg.ProjectKey, strings.TrimSpace(job.WorkerID), job.Attempt, nowText, nowText).Scan(&owned); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: validate core rebuild lease: %w", err)
	}
	if owned != 1 {
		return CoreMemory{}, fmt.Errorf("%w: core rebuild job %s is no longer owned", ErrJobLeaseLost, job.ID)
	}

	core, err := s.createCoreCandidateTx(ctx, tx, opts)
	if err != nil {
		return CoreMemory{}, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'succeeded', worker_id = NULL, lease_until = NULL,
    deadline_at = NULL, heartbeat_at = NULL, finished_at = ?,
    last_success_watermark = ?, updated_at = ?
WHERE id = ? AND project_key = ? AND kind = 'core_rebuild'
  AND status = 'running' AND worker_id = ? AND attempt = ?
  AND COALESCE(lease_until, '') > ? AND COALESCE(deadline_at, '') > ?
`, nowText, nullableString(watermark), nowText,
		job.ID, s.cfg.ProjectKey, strings.TrimSpace(job.WorkerID), job.Attempt, nowText, nowText)
	if err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: complete core rebuild job: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return CoreMemory{}, fmt.Errorf("%w: core rebuild job %s is no longer owned", ErrJobLeaseLost, job.ID)
	}
	if err := tx.Commit(); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: commit core rebuild candidate: %w", err)
	}
	return core, nil
}

func (s *Store) createCoreCandidateTx(
	ctx context.Context,
	tx *sql.Tx,
	opts CoreMemoryCandidateOptions,
) (CoreMemory, error) {
	if err := validateCoreSourcesTx(ctx, tx, s.cfg.ProjectKey, opts.SourceIDs); err != nil {
		return CoreMemory{}, err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(version), 0) + 1
FROM memory_core
WHERE project_key = ? AND scope_kind = ? AND scope_key = ?
`, s.cfg.ProjectKey, string(opts.ScopeKind), opts.ScopeKey).Scan(&version); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: allocate core version: %w", err)
	}
	sourceJSON, _ := json.Marshal(opts.SourceIDs)
	now := nowText()
	core := CoreMemory{
		ID: "core_" + uuid.NewString(), ProjectKey: s.cfg.ProjectKey,
		ScopeKind: opts.ScopeKind, ScopeKey: opts.ScopeKey, Content: opts.Content,
		Version: version, TokenEstimate: EstimateTokens(opts.Content),
		Status: CoreStatusCandidate, SourceIDs: append([]string(nil), opts.SourceIDs...),
		GeneratedBy: opts.GeneratedBy, CreatedAt: parseTime(now), UpdatedAt: parseTime(now),
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO memory_core (
    id, project_key, scope_kind, scope_key, content, version, token_estimate,
    status, source_ids_json, generated_by, approved_by, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)
`, core.ID, core.ProjectKey, string(core.ScopeKind), core.ScopeKey, core.Content,
		core.Version, core.TokenEstimate, core.Status, string(sourceJSON),
		core.GeneratedBy, now, now)
	if err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: insert core candidate: %w", err)
	}
	if err := insertEventTx(ctx, tx, s.cfg.ProjectKey, core.ID, "core_candidate_created",
		core.GeneratedBy, "", core.Version, map[string]interface{}{
			"scope_kind": string(core.ScopeKind), "scope_key": core.ScopeKey,
			"source_ids": opts.SourceIDs, "reason": opts.Reason,
		}); err != nil {
		return CoreMemory{}, err
	}
	return core, nil
}

// GetActiveCore returns the one active core for the current project. A missing
// active core is reported as sql.ErrNoRows, allowing read-only callers to
// distinguish "not generated yet" from a database failure.
func (s *Store) GetActiveCore(ctx context.Context, scopeKind Scope, scopeKey string) (CoreMemory, error) {
	scopeKind, scopeKey, err := s.normalizeCoreScope(scopeKind, scopeKey)
	if err != nil {
		return CoreMemory{}, err
	}
	core, err := s.getCore(ctx, scopeKind, scopeKey, CoreStatusActive)
	if err != nil {
		return CoreMemory{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return CoreMemory{}, err
	}
	tx, err := sqliteutil.BeginReadTx(ctx, db)
	if err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: begin active core source validation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateCoreSourcesTx(ctx, tx, s.cfg.ProjectKey, core.SourceIDs); err != nil {
		return CoreMemory{}, fmt.Errorf("%w: %v", ErrCoreSourceIneligible, err)
	}
	// Rollback (via the deferred call above) is the correct finish for this
	// validation-only read, so there is no read-only COMMIT to attempt.
	// sqliteutil.ReadTxOptions also avoids the win7compat v0.22 driver's broken
	// read-only COMMIT/ROLLBACK reset, which would otherwise poison the pooled
	// connection with query_only mode.
	return core, nil
}

// GetCore is the compatibility name used by callers that only need the
// current active project core.
func (s *Store) GetCore(ctx context.Context) (CoreMemory, error) {
	return s.GetActiveCore(ctx, ScopeProject, "")
}

func (s *Store) getCore(ctx context.Context, scopeKind Scope, scopeKey, status string) (CoreMemory, error) {
	if !s.v2Configured() {
		return CoreMemory{}, errV2Unavailable
	}
	db, err := s.ensureV2()
	if err != nil {
		return CoreMemory{}, err
	}
	return scanCoreMemory(db.QueryRowContext(ctx, `
SELECT id, project_key, scope_kind, scope_key, content, version, token_estimate,
       status, source_ids_json, generated_by, approved_by, created_at, updated_at
FROM memory_core
WHERE project_key = ? AND scope_kind = ? AND scope_key = ? AND status = ?
ORDER BY version DESC
LIMIT 1
`, s.cfg.ProjectKey, string(scopeKind), scopeKey, status))
}

// ListCore returns bounded core history for the current project. An empty
// status means active only; callers must explicitly request candidate/stale or
// archived rows.
func (s *Store) ListCore(ctx context.Context, filter CoreMemoryFilter) ([]CoreMemory, error) {
	if !s.v2Configured() {
		return nil, errV2Unavailable
	}
	scopeKind, scopeKey, err := s.normalizeCoreScope(filter.ScopeKind, filter.ScopeKey)
	if err != nil {
		return nil, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(filter.Status))
	if status == "" {
		status = CoreStatusActive
	}
	switch status {
	case CoreStatusCandidate, CoreStatusActive, CoreStatusStale, CoreStatusArchived:
	default:
		return nil, fmt.Errorf("memorystore: invalid core status %q", status)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, project_key, scope_kind, scope_key, content, version, token_estimate,
       status, source_ids_json, generated_by, approved_by, created_at, updated_at
FROM memory_core
WHERE project_key = ? AND scope_kind = ? AND scope_key = ? AND status = ?
ORDER BY version DESC
LIMIT ?
`, s.cfg.ProjectKey, string(scopeKind), scopeKey, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cores := make([]CoreMemory, 0, limit)
	for rows.Next() {
		core, err := scanCoreMemory(rows)
		if err != nil {
			return nil, err
		}
		cores = append(cores, core)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cores, nil
}

// ActivateCore is the explicit review gate. It never overwrites the active
// row: the previous active version becomes stale and the candidate becomes the
// sole active version in one transaction.
func (s *Store) ActivateCore(ctx context.Context, id, approvedBy string) (CoreMemory, error) {
	if !s.v2Configured() {
		return CoreMemory{}, errV2Unavailable
	}
	id = strings.TrimSpace(id)
	approvedBy = strings.TrimSpace(approvedBy)
	if id == "" || approvedBy == "" {
		return CoreMemory{}, fmt.Errorf("memorystore: core activation requires id and approver")
	}
	if utf8.RuneCountInString(approvedBy) > maxCoreActorRunes {
		return CoreMemory{}, fmt.Errorf("memorystore: core approver identity is too long")
	}
	db, err := s.ensureV2()
	if err != nil {
		return CoreMemory{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return CoreMemory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	core, err := scanCoreMemory(tx.QueryRowContext(ctx, `
SELECT id, project_key, scope_kind, scope_key, content, version, token_estimate,
       status, source_ids_json, generated_by, approved_by, created_at, updated_at
FROM memory_core
WHERE id = ? AND project_key = ?
`, id, s.cfg.ProjectKey))
	if err != nil {
		return CoreMemory{}, err
	}
	if core.Status == CoreStatusActive {
		if err := validateCoreSourcesTx(ctx, tx, s.cfg.ProjectKey, core.SourceIDs); err != nil {
			return CoreMemory{}, fmt.Errorf("%w: %v", ErrCoreSourceIneligible, err)
		}
		if err := tx.Commit(); err != nil {
			return CoreMemory{}, fmt.Errorf("memorystore: finish active core validation: %w", err)
		}
		return core, nil
	}
	if core.Status != CoreStatusCandidate {
		return CoreMemory{}, ErrCoreNotCandidate
	}
	if err := validateCoreSourcesTx(ctx, tx, s.cfg.ProjectKey, core.SourceIDs); err != nil {
		return CoreMemory{}, fmt.Errorf("%w: %v", ErrCoreSourceIneligible, err)
	}
	now := nowText()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_core
SET status = ?, updated_at = ?
WHERE project_key = ? AND scope_kind = ? AND scope_key = ? AND status = ?
`, CoreStatusStale, now, s.cfg.ProjectKey, string(core.ScopeKind), core.ScopeKey, CoreStatusActive); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: stale previous core: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_core
SET status = ?, approved_by = ?, updated_at = ?
WHERE id = ? AND project_key = ? AND status = ?
`, CoreStatusActive, approvedBy, now, id, s.cfg.ProjectKey, CoreStatusCandidate); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: activate core: %w", err)
	}
	if err := insertEventTx(ctx, tx, s.cfg.ProjectKey, id, "core_activated",
		approvedBy, "", core.Version, map[string]interface{}{
			"previous_status": core.Status, "approved_by": approvedBy,
		}); err != nil {
		return CoreMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: commit core activation: %w", err)
	}
	core.Status = CoreStatusActive
	core.ApprovedBy = approvedBy
	core.UpdatedAt = parseTime(now)
	return core, nil
}

// ArchiveCore removes a core from injection without deleting its history.
func (s *Store) ArchiveCore(ctx context.Context, id, actor, reason string) error {
	if !s.v2Configured() {
		return errV2Unavailable
	}
	id, actor, reason = strings.TrimSpace(id), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if id == "" || actor == "" {
		return fmt.Errorf("memorystore: core archive requires id and actor")
	}
	if utf8.RuneCountInString(actor) > maxCoreActorRunes {
		return fmt.Errorf("memorystore: core actor identity is too long")
	}
	if reason == "" {
		reason = "core archived by operator"
	}
	if utf8.RuneCountInString(reason) > 500 {
		return fmt.Errorf("memorystore: core archive reason is too long")
	}
	if rule := memoryredact.DetectSecret(reason); rule != "" {
		return fmt.Errorf("memorystore: refusing to store secret-shaped core archive reason (%s)", rule)
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
	var core CoreMemory
	core, err = scanCoreMemory(tx.QueryRowContext(ctx, `
SELECT id, project_key, scope_kind, scope_key, content, version, token_estimate,
       status, source_ids_json, generated_by, approved_by, created_at, updated_at
FROM memory_core
WHERE id = ? AND project_key = ?
`, id, s.cfg.ProjectKey))
	if err != nil {
		return err
	}
	if core.Status == CoreStatusArchived {
		return nil
	}
	now := nowText()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_core SET status = ?, updated_at = ?
WHERE id = ? AND project_key = ?
`, CoreStatusArchived, now, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	return func() error {
		if err := insertEventTx(ctx, tx, s.cfg.ProjectKey, id, "core_archived",
			actor, "", core.Version, map[string]interface{}{"reason": reason}); err != nil {
			return err
		}
		return tx.Commit()
	}()
}

func scanCoreMemory(row rowScanner) (CoreMemory, error) {
	var (
		core                                                CoreMemory
		scopeKind, sourceJSON, generatedBy, approvedBy      sql.NullString
		createdAt, updatedAt, projectKey, scopeKey, content sql.NullString
		status, id                                          sql.NullString
		version, tokenEstimate                              int64
	)
	if err := row.Scan(&id, &projectKey, &scopeKind, &scopeKey, &content,
		&version, &tokenEstimate, &status, &sourceJSON, &generatedBy,
		&approvedBy, &createdAt, &updatedAt); err != nil {
		return CoreMemory{}, err
	}
	if err := json.Unmarshal([]byte(sourceJSON.String), &core.SourceIDs); err != nil {
		return CoreMemory{}, fmt.Errorf("memorystore: decode core source ids: %w", err)
	}
	if core.SourceIDs == nil {
		core.SourceIDs = []string{}
	}
	core.ID, core.ProjectKey, core.ScopeKind, core.ScopeKey = id.String, projectKey.String,
		Scope(scopeKind.String), scopeKey.String
	core.Content, core.Version, core.TokenEstimate = content.String, version, int(tokenEstimate)
	core.Status, core.GeneratedBy, core.ApprovedBy = status.String, generatedBy.String, approvedBy.String
	core.CreatedAt, core.UpdatedAt = parseTime(createdAt.String), parseTime(updatedAt.String)
	return core, nil
}
