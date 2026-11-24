package memorystore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateCoreCandidateForWorkerCommitsCandidateAndJobTogether(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-worker-commit",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{
		Text: "Worker-owned Core candidate source.",
	})
	require.NoError(t, err)
	queued, err := store.EnqueueCoreRebuild(ctx, 3)
	require.NoError(t, err)
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "core-worker", Kind: "core_rebuild", JobKey: queued.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1, Lease: time.Minute,
		Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	core, err := store.CreateCoreCandidateForWorker(ctx, CoreMemoryCandidateOptions{
		Content:   "A worker commit must leave a reviewable candidate.",
		SourceIDs: []string{source.ID},
	}, claimed[0], "core-v1:worker-test")
	require.NoError(t, err)
	require.Equal(t, CoreStatusCandidate, core.Status)

	job, err := store.GetJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusSucceeded, job.Status)
	require.Equal(t, "core-v1:worker-test", job.LastSuccessWatermark)
	candidates, err := store.ListCore(ctx, CoreMemoryFilter{
		Status: CoreStatusCandidate, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, core.ID, candidates[0].ID)
}

func TestCreateCoreCandidateForWorkerRejectsExpiredLeaseWithoutWritingCandidate(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-worker-fence",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{
		Text: "The source remains safe but the worker lease expires.",
	})
	require.NoError(t, err)
	queued, err := store.EnqueueCoreRebuild(ctx, 3)
	require.NoError(t, err)
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "core-worker", Kind: "core_rebuild", JobKey: queued.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1, Lease: time.Minute,
		Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	expired := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `
UPDATE memory_jobs SET lease_until = ?, updated_at = ?
WHERE id = ? AND project_key = ?
`, expired, expired, claimed[0].ID, store.ProjectKey())
	require.NoError(t, err)

	_, err = store.CreateCoreCandidateForWorker(ctx, CoreMemoryCandidateOptions{
		Content:   "This write must be rejected after lease loss.",
		SourceIDs: []string{source.ID},
	}, claimed[0], "core-v1:expired-worker-test")
	require.ErrorIs(t, err, ErrJobLeaseLost)
	require.Contains(t, err.Error(), "no longer owned")

	candidates, err := store.ListCore(ctx, CoreMemoryFilter{
		Status: CoreStatusCandidate, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, candidates)
	job, err := store.GetJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusRunning, job.Status)
}

func TestCoreMemoryCandidateActivationAndArchive(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "core-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	version, err := store.SchemaVersion()
	require.NoError(t, err)
	require.Equal(t, 33, version)

	source, err := store.Append(AppendNoteOptions{
		Text: "The runtime keeps memory writes behind a durable receipt.",
		Kind: "decision", SessionID: "session-core",
	})
	require.NoError(t, err)

	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:     "Durable memory writes require a capture receipt before extraction.",
		SourceIDs:   []string{source.ID, source.ID},
		GeneratedBy: "test:core",
		TokenBudget: 80,
	})
	require.NoError(t, err)
	require.Equal(t, CoreStatusCandidate, candidate.Status)
	require.Equal(t, int64(1), candidate.Version)
	require.Equal(t, []string{source.ID}, candidate.SourceIDs)
	require.LessOrEqual(t, candidate.TokenEstimate, 80)

	_, err = store.GetCore(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)

	active, err := store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)
	require.Equal(t, CoreStatusActive, active.Status)
	require.Equal(t, "admin:test", active.ApprovedBy)

	current, err := store.GetCore(ctx)
	require.NoError(t, err)
	require.Equal(t, active.ID, current.ID)

	second, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "Memory extraction must remain bounded and replayable.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Version)
	_, err = store.ActivateCore(ctx, second.ID, "admin:test")
	require.NoError(t, err)

	previous, err := store.ListCore(ctx, CoreMemoryFilter{
		Status: CoreStatusStale, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, previous, 1)
	require.Equal(t, active.ID, previous[0].ID)

	require.NoError(t, store.ArchiveCore(ctx, second.ID, "admin:test", "replace with reviewed core"))
	_, err = store.GetCore(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)

	db, err := store.ensureV2()
	require.NoError(t, err)
	var events int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_events
WHERE project_key = ? AND memory_id = ? AND event_type LIKE 'core_%'
`, store.ProjectKey(), second.ID).Scan(&events))
	require.Equal(t, 3, events)
}

func TestCoreMemoryRejectsUnscopedOrUnsafeCandidates(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "core-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	source, err := store.Append(AppendNoteOptions{Text: "safe source"})
	require.NoError(t, err)

	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "candidate with secret-shaped audit reason",
		SourceIDs: []string{source.ID},
		Reason:    "copy sk-123456789012345678901234",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret-shaped core reason")

	audited, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "safe candidate for archive reason test",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	err = store.ArchiveCore(ctx, audited.ID, "admin:test", "copy sk-123456789012345678901234")
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret-shaped core archive reason")
	candidates, err := store.ListCore(ctx, CoreMemoryFilter{Status: CoreStatusCandidate})
	require.NoError(t, err)
	require.Len(t, candidates, 1, "rejected audit reason must not archive the candidate")
	require.Equal(t, audited.ID, candidates[0].ID)

	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content: "missing evidence",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "source")

	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "api_key=supersecretvalue123",
		SourceIDs: []string{source.ID},
	})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "core content"))

	expired, err := store.Append(AppendNoteOptions{Text: "expired source"})
	require.NoError(t, err)
	expiredStatus := StatusExpired
	_, err = store.UpdateRecord(ctx, expired.ID, RecordPatch{Status: &expiredStatus})
	require.NoError(t, err)
	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "core must not use expired evidence",
		SourceIDs: []string{expired.ID},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not eligible")

	stale, err := store.Append(AppendNoteOptions{Text: "stale source"})
	require.NoError(t, err)
	db, err := store.ensureV2()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = ?
WHERE id = ? AND project_key = ?
`, FreshnessStale, stale.ID, store.ProjectKey())
	require.NoError(t, err)
	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "core must not use stale evidence",
		SourceIDs: []string{stale.ID},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not eligible")

	private, err := store.Append(AppendNoteOptions{Text: "private source"})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_entries SET sensitivity = 'private'
WHERE id = ? AND project_key = ?
`, private.ID, store.ProjectKey())
	require.NoError(t, err)
	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "core must not use private evidence",
		SourceIDs: []string{private.ID},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not eligible")

	reviewSource, err := store.Append(AppendNoteOptions{Text: "source changes after review"})
	require.NoError(t, err)
	reviewed, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "activation must revalidate evidence",
		SourceIDs: []string{reviewSource.ID},
	})
	require.NoError(t, err)
	reviewStatus := StatusExpired
	_, err = store.UpdateRecord(ctx, reviewSource.ID, RecordPatch{Status: &reviewStatus})
	require.NoError(t, err)
	_, err = store.ActivateCore(ctx, reviewed.ID, "admin:test")
	require.ErrorIs(t, err, ErrCoreSourceIneligible)

	_, err = store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		ScopeKind: ScopeUser, Content: "wrong scope", SourceIDs: []string{source.ID},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "project scope")
}

func TestActiveCoreReadRevalidatesItsEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "core-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{Text: "active source before invalidation"})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "Reviewed active core.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	_, err = store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)

	current, err := store.GetCore(ctx)
	require.NoError(t, err)
	require.Equal(t, candidate.ID, current.ID)

	expired := StatusExpired
	_, err = store.UpdateRecord(ctx, source.ID, RecordPatch{Status: &expired})
	require.NoError(t, err)
	_, err = store.GetCore(ctx)
	require.ErrorIs(t, err, ErrCoreSourceIneligible)
}

func TestSelectForInjectV2IncludesActiveCoreWithinProjectMemoryBudget(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-selection",
		InjectionMode: "notes", PinnedBudgetRatio: 0.2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{
		Text: "The reviewed core source is safe and project-scoped.",
		Kind: "decision",
	})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:     "Keep the memory context bounded and treat it as historical data.",
		SourceIDs:   []string{source.ID},
		TokenBudget: 80,
	})
	require.NoError(t, err)
	active, err := store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "bounded context", Limit: 5, TokenBudget: 120,
	})
	require.NoError(t, err)
	require.NotNil(t, selection.Core)
	require.Equal(t, active.ID, selection.Core.ID)
	require.Contains(t, selection.IDs, active.ID)
	require.GreaterOrEqual(t, selection.TokenEstimate,
		EstimateTokens(active.Content))
	require.LessOrEqual(t, selection.TokenEstimate, 120)
}

func TestSelectForInjectV2SkipsCoreAfterSourceInvalidation(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-invalidation",
		InjectionMode: "notes",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{Text: "source is valid at activation"})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "This core must stop being injected when evidence expires.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	_, err = store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)

	expired := StatusExpired
	_, err = store.UpdateRecord(ctx, source.ID, RecordPatch{Status: &expired})
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "evidence expires", Limit: 5, TokenBudget: 120,
	})
	require.NoError(t, err)
	require.Nil(t, selection.Core)
	require.NotContains(t, selection.IDs, candidate.ID)
}

func TestSelectForInjectV2DropsOversizedCoreDeterministically(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-budget",
		InjectionMode: "notes",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{Text: "budget source"})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "A reviewed core that is intentionally too large for this turn budget.",
		SourceIDs: []string{source.ID}, TokenBudget: 80,
	})
	require.NoError(t, err)
	_, err = store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "intentionally too large", Limit: 5, TokenBudget: 12,
	})
	require.NoError(t, err)
	require.Nil(t, selection.Core)
	require.LessOrEqual(t, selection.TokenEstimate, 12)
	foundDrop := false
	for _, dropped := range selection.Dropped {
		if dropped.ID == candidate.ID && dropped.Reason == "core_budget" {
			foundDrop = true
		}
	}
	require.True(t, foundDrop, "oversized core should have an explicit bounded drop reason")
}

func TestSelectForInjectV2BudgetsCoreSummaryAndPinnedTogether(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-combined-budget",
		InjectionMode: "hybrid", PinnedBudgetRatio: 0.2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, err = store.Append(AppendNoteOptions{Text: "Pinned project invariant."})
	require.NoError(t, err)
	summarySource := seedActiveDerivedMemory(t, store, "Reusable memory summary evidence.", "decision")
	require.NoError(t, store.createSummary(ctx, Summary{
		Revision: 1, Text: "Stable summary remains behind the core prefix.",
	}, []string{summarySource.ID}))
	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "Keep reviewed core information first in the project prefix.",
		SourceIDs: []string{summarySource.ID},
	})
	require.NoError(t, err)
	_, err = store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "memory project", Limit: 5, TokenBudget: 200,
	})
	require.NoError(t, err)
	require.NotNil(t, selection.Core)
	require.NotNil(t, selection.Summary)
	require.NotEmpty(t, selection.Stable, "the pinned record should retain its stable allocation")
	require.LessOrEqual(t, selection.TokenEstimate, 200)
}
