package memorystore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RunDreamPipeline is the bounded, auditable G4 fallback pipeline. It reads
// only durable Episode projections and emits a reviewable patch; it never
// writes memory_entries directly. A model-backed analyzer can replace the
// deterministic reflection later without changing the patch/apply contract.
func (s *Store) RunDreamPipeline(ctx context.Context, opts DreamRunOptions) (DreamRun, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return DreamRun{}, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 800
	}
	if maxTokens > 4000 {
		maxTokens = 4000
	}
	createdBy := strings.TrimSpace(opts.CreatedBy)
	if createdBy == "" {
		createdBy = "admin"
	}
	db, err := s.ensureV2()
	if err != nil {
		return DreamRun{}, err
	}
	episodes, err := s.ListEpisodes(ctx, EpisodeFilter{Limit: limit})
	if err != nil {
		return DreamRun{}, err
	}
	sourceHash := ""
	if len(episodes) >= 2 {
		sourceHash = DreamSourceHash(episodes)
		if !opts.Shadow && !opts.Force {
			existingRunID, lookupErr := lookupDreamSourceRun(ctx, db, scope, sourceHash)
			if lookupErr != nil {
				return DreamRun{}, lookupErr
			}
			if existingRunID != "" {
				return s.GetDreamRun(ctx, existingRunID)
			}
		}
	}
	now := time.Now().UTC()
	run := DreamRun{
		ID: "dream_" + uuid.NewString(), TenantID: scope.TenantID,
		ProjectKey: scope.ProjectKey, Status: DreamRunRunning,
		InputCount: len(episodes), Shadow: opts.Shadow, StartedAt: now, CreatedAt: now,
	}
	if err := s.insertDreamRun(ctx, db, run); err != nil {
		return DreamRun{}, err
	}

	if len(episodes) < 2 {
		if err := s.finishDreamRun(ctx, db, run, DreamRunSucceeded, 0, 0, "", "", ""); err != nil {
			return DreamRun{}, err
		}
		return s.GetDreamRun(ctx, run.ID)
	}
	content := deterministicDreamReflection(episodes)
	model := "deterministic"
	analysisWarning := ""
	if opts.Analyzer != nil {
		analysis, analysisErr := opts.Analyzer.AnalyzeDream(ctx, episodes)
		if analysisErr != nil {
			analysisWarning = "dream analyzer fallback: " + analysisErr.Error()
		} else if strings.TrimSpace(analysis.Text) == "" {
			analysisWarning = "dream analyzer fallback: empty reflection"
		} else if sanitized := sanitizePatchText(analysis.Text); sanitized == "" {
			analysisWarning = "dream analyzer fallback: unsafe reflection"
		} else {
			content = sanitized
			model = firstNonEmpty(strings.TrimSpace(analysis.Model), "llm")
		}
	}
	content = truncateTextToTokenBudget(content, maxTokens)
	tokenCost := EstimateTokens(content)
	if opts.Shadow {
		if err := s.finishDreamRun(ctx, db, run, DreamRunSucceeded, 0, tokenCost, analysisWarning, content, model); err != nil {
			return DreamRun{}, err
		}
		return s.GetDreamRun(ctx, run.ID)
	}
	patch, err := s.CreatePatch(ctx, PatchInput{
		ProjectKey: scope.ProjectKey, TenantID: scope.TenantID,
		Operation: PatchOperationAdd, ProposedContent: content,
		ProposedMetadata: map[string]interface{}{
			"kind": "reflective", "tags": []string{"dreaming", "reflection"},
			"source_kind": "dreaming", "sensitivity": "internal",
			"dreaming_source_hash": sourceHash,
			"dream_model":          model,
		},
		Reason:    "dreaming:" + sourceHash + ": cross-episode reflection",
		Evidence:  episodeMemoryIDs(episodes),
		RiskLevel: PatchRiskMedium, CreatedBy: createdBy,
	})
	if err != nil {
		_ = s.finishDreamRun(ctx, db, run, DreamRunFailed, 0, 0, err.Error(), "", "")
		return s.GetDreamRun(ctx, run.ID)
	}
	_ = patch
	if err := insertDreamSource(ctx, db, scope, sourceHash, run.ID, patch.ID); err != nil {
		_ = s.finishDreamRun(ctx, db, run, DreamRunFailed, 0, 0, err.Error(), "", "")
		return s.GetDreamRun(ctx, run.ID)
	}
	if err := s.finishDreamRun(ctx, db, run, DreamRunSucceeded, 1, tokenCost, analysisWarning, content, model); err != nil {
		return DreamRun{}, err
	}
	return s.GetDreamRun(ctx, run.ID)
}

// DreamSourceExists reports whether this evidence set already produced a
// reviewable Dreaming patch. Shadow runs intentionally do not register a
// source because they produce no patch.
func (s *Store) DreamSourceExists(ctx context.Context, sourceHash string) (bool, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return false, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return false, err
	}
	runID, err := lookupDreamSourceRun(ctx, db, scope, strings.TrimSpace(sourceHash))
	return runID != "", err
}

func lookupDreamSourceRun(ctx context.Context, db *sql.DB, scope ScopeContext, sourceHash string) (string, error) {
	sourceHash = strings.TrimSpace(sourceHash)
	if sourceHash == "" {
		return "", nil
	}
	var runID string
	err := db.QueryRowContext(ctx, `
SELECT dream_run_id FROM memory_dream_sources
WHERE tenant_id = ? AND project_key = ? AND source_hash = ?
ORDER BY created_at DESC, dream_run_id DESC LIMIT 1
`, scope.TenantID, scope.ProjectKey, sourceHash).Scan(&runID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memorystore: lookup dream source: %w", err)
	}
	return strings.TrimSpace(runID), nil
}

func insertDreamSource(
	ctx context.Context, db *sql.DB, scope ScopeContext,
	sourceHash, runID, patchID string,
) error {
	if _, err := db.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_dream_sources (
    tenant_id, project_key, source_hash, dream_run_id, patch_id, created_at
) VALUES (?, ?, ?, ?, ?, ?)
`, scope.TenantID, scope.ProjectKey, strings.TrimSpace(sourceHash),
		strings.TrimSpace(runID), strings.TrimSpace(patchID),
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("memorystore: record dream source: %w", err)
	}
	return nil
}

// DreamSourceHash identifies the exact bounded Episode evidence set supplied to
// one Dreaming reflection. Maintenance uses it to coalesce runs even after a
// prior proposal was approved or rejected.
func DreamSourceHash(episodes []Episode) string {
	ids := episodeMemoryIDs(episodes)
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *Store) GetDreamRun(ctx context.Context, id string) (DreamRun, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return DreamRun{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return DreamRun{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, tenant_id, project_key, status, COALESCE(window_start, ''),
       COALESCE(window_end, ''), input_count, patch_count, token_cost,
       COALESCE(reflection_text, ''), COALESCE(model, ''), shadow, error,
       COALESCE(started_at, ''), COALESCE(finished_at, ''), created_at
FROM memory_dream_runs
WHERE id = ? AND tenant_id = ? AND project_key = ?
`, strings.TrimSpace(id), scope.TenantID, scope.ProjectKey)
	return scanDreamRun(row)
}

func (s *Store) ListDreamRuns(ctx context.Context, limit int) ([]DreamRun, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return nil, err
	}
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
	rows, err := db.QueryContext(ctx, `
SELECT id, tenant_id, project_key, status, COALESCE(window_start, ''),
       COALESCE(window_end, ''), input_count, patch_count, token_cost,
       COALESCE(reflection_text, ''), COALESCE(model, ''), shadow, error,
       COALESCE(started_at, ''), COALESCE(finished_at, ''), created_at
FROM memory_dream_runs
WHERE tenant_id = ? AND project_key = ?
ORDER BY created_at DESC, id DESC
LIMIT ?
`, scope.TenantID, scope.ProjectKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]DreamRun, 0, limit)
	for rows.Next() {
		run, scanErr := scanDreamRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// PruneShadowDreamRuns removes terminal shadow runs whose retained reflection
// preview has exceeded its retention window. Patched Dreaming runs remain
// auditable until their patch retention policy is defined separately.
func (s *Store) PruneShadowDreamRuns(ctx context.Context, before time.Time) (int64, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return 0, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}
	res, err := db.ExecContext(ctx, `
DELETE FROM memory_dream_runs
WHERE tenant_id = ? AND project_key = ? AND shadow = 1
  AND status IN ('succeeded', 'failed', 'partial')
  AND created_at < ?
`, scope.TenantID, scope.ProjectKey, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("memorystore: prune shadow dream runs: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) insertDreamRun(ctx context.Context, db *sql.DB, run DreamRun) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO memory_dream_runs (
    id, tenant_id, project_key, status, window_start, window_end,
    input_count, patch_count, token_cost, error, started_at, finished_at, created_at, shadow
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, run.ID, run.TenantID, run.ProjectKey, string(run.Status),
		nullableTime(run.WindowStart), nullableTime(run.WindowEnd), run.InputCount,
		run.PatchCount, run.TokenCost, run.Error, nullableTime(run.StartedAt),
		nullableTime(run.FinishedAt), run.CreatedAt.Format(time.RFC3339Nano), boolInt(run.Shadow))
	if err != nil {
		return fmt.Errorf("memorystore: insert dream run: %w", err)
	}
	return nil
}

func (s *Store) finishDreamRun(
	ctx context.Context, db *sql.DB, run DreamRun, status DreamRunStatus,
	patchCount, tokenCost int, errorText, reflectionText, model string,
) error {
	finishedAt := time.Now().UTC()
	_, err := db.ExecContext(ctx, `
UPDATE memory_dream_runs
SET status = ?, input_count = ?, patch_count = ?, token_cost = ?,
    reflection_text = ?, model = ?, error = ?, finished_at = ?
WHERE id = ?
`, string(status), run.InputCount, patchCount, tokenCost,
		sanitizePatchText(reflectionText), sanitizePatchText(model), sanitizePatchText(errorText),
		finishedAt.Format(time.RFC3339Nano), run.ID)
	return err
}

type dreamRunScanner interface {
	Scan(dest ...interface{}) error
}

func scanDreamRun(row dreamRunScanner) (DreamRun, error) {
	var (
		run                                                   DreamRun
		id, tenantID, projectKey                              sql.NullString
		status, windowStart, windowEnd                        sql.NullString
		errorText, reflectionText, model, startedAt, finished sql.NullString
		createdAt                                             sql.NullString
		inputCount, patchCount, tokens, shadow                int
	)
	if err := row.Scan(
		&id, &tenantID, &projectKey, &status, &windowStart, &windowEnd,
		&inputCount, &patchCount, &tokens, &reflectionText, &model,
		&shadow, &errorText, &startedAt, &finished, &createdAt,
	); err != nil {
		return DreamRun{}, err
	}
	run = DreamRun{
		ID: id.String, TenantID: tenantID.String, ProjectKey: projectKey.String,
		Status: DreamRunStatus(status.String), WindowStart: parseTime(windowStart.String),
		WindowEnd: parseTime(windowEnd.String), InputCount: inputCount,
		PatchCount: patchCount, TokenCost: tokens, ReflectionText: reflectionText.String,
		Model: model.String, Shadow: shadow != 0, Error: errorText.String,
		StartedAt: parseTime(startedAt.String), FinishedAt: parseTime(finished.String),
		CreatedAt: parseTime(createdAt.String),
	}
	return run, nil
}

func deterministicDreamReflection(episodes []Episode) string {
	var builder strings.Builder
	builder.WriteString("Reflective observation from ")
	fmt.Fprintf(&builder, "%d durable episodes:\n", len(episodes))
	limit := len(episodes)
	if limit > 5 {
		limit = 5
	}
	for index := 0; index < limit; index++ {
		episode := episodes[index]
		fmt.Fprintf(&builder, "- %s: %s", episode.Outcome, episode.Problem)
		if episode.Result != "" {
			builder.WriteString(" -> ")
			builder.WriteString(episode.Result)
		}
		if episode.FailureReason != "" {
			builder.WriteString(" [failure: ")
			builder.WriteString(episode.FailureReason)
			builder.WriteString("]")
		}
		builder.WriteString("\n")
	}
	if len(episodes) > limit {
		fmt.Fprintf(&builder, "- ... %d more episodes\n", len(episodes)-limit)
	}
	builder.WriteString("Review this reflection before applying it to active memory.")
	return sanitizePatchText(builder.String())
}

func episodeMemoryIDs(episodes []Episode) []string {
	ids := make([]string, 0, len(episodes))
	for _, episode := range episodes {
		if strings.TrimSpace(episode.MemoryID) != "" {
			ids = append(ids, episode.MemoryID)
		}
	}
	return ids
}
