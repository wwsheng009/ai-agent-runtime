package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeDreamAnalyzer struct {
	result DreamAnalysis
	err    error
	calls  int
}

func (a *fakeDreamAnalyzer) AnalyzeDream(context.Context, []Episode) (DreamAnalysis, error) {
	a.calls++
	return a.result, a.err
}

func TestTaskCompletionEpisodeIsDurableSourceBoundedAndIdempotent(t *testing.T) {
	store := newScopedTestStore(t, "g4-episodes", "")
	ctx := context.Background()
	input := EpisodeInput{
		TaskID:         "task-1",
		SessionID:      "session-1",
		Problem:        "Implement a durable episode pipeline",
		Actions:        []string{"write schema", "write tests"},
		Result:         "episode candidate persisted",
		Outcome:        EpisodeOutcomeSuccess,
		SourceEventIDs: []string{"event-1", "event-2"},
		EndedAt:        time.Now().UTC(),
	}
	episode, err := store.CreateEpisode(ctx, input)
	require.NoError(t, err)
	require.NotEmpty(t, episode.ID)
	require.NotEmpty(t, episode.MemoryID)
	require.Equal(t, EpisodeOutcomeSuccess, episode.Outcome)
	require.Equal(t, []string{"event-1", "event-2"}, episode.SourceEventIDs)

	replay, err := store.CreateEpisode(ctx, input)
	require.NoError(t, err)
	require.Equal(t, episode.ID, replay.ID, "same durable event must be idempotent")
	require.Equal(t, episode.MemoryID, replay.MemoryID)

	record, err := store.GetRecord(ctx, episode.MemoryID)
	require.NoError(t, err)
	require.Equal(t, "episodic", record.Kind)
	require.Equal(t, StatusCandidate, record.Status)
	require.Equal(t, "task_completion", record.SourceKind)
	require.Contains(t, record.Text, "Implement a durable episode pipeline")

	promoted, err := store.PromoteEpisode(ctx, episode.ID)
	require.NoError(t, err)
	active, err := store.GetRecord(ctx, promoted.MemoryID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, active.Status)

	_, err = store.CreateEpisode(ctx, EpisodeInput{
		TaskID: "task-2", SessionID: "session-1", Problem: "missing source",
		Outcome: EpisodeOutcomeFailed,
	})
	require.ErrorContains(t, err, "source")
}

func TestEpisodeOutcomesAllowDuplicateTextAcrossTasks(t *testing.T) {
	store := newScopedTestStore(t, "g4-episode-hash", "")
	ctx := context.Background()
	first, err := store.CreateEpisode(ctx, EpisodeInput{
		TaskID: "task-a", SessionID: "session-a", Problem: "same problem",
		Result: "same result", Outcome: EpisodeOutcomeSuccess,
		SourceEventIDs: []string{"event-a"}, EndedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	second, err := store.CreateEpisode(ctx, EpisodeInput{
		TaskID: "task-b", SessionID: "session-b", Problem: "same problem",
		Result: "same result", Outcome: EpisodeOutcomeFailed,
		FailureReason:  "different task failed",
		SourceEventIDs: []string{"event-b"}, EndedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NotEqual(t, first.MemoryID, second.MemoryID)
	require.Equal(t, EpisodeOutcomeFailed, second.Outcome)
	require.Contains(t, second.FailureReason, "different task")
}

func TestPatchAddApplyRollbackAndAudit(t *testing.T) {
	store := newScopedTestStore(t, "g4-patch-add", "")
	ctx := context.Background()
	patch, err := store.CreatePatch(ctx, PatchInput{
		Operation: PatchOperationAdd, ProposedContent: "A reviewed reflective memory.",
		ProposedMetadata: map[string]interface{}{"kind": "reflective", "tags": []string{"dreaming"}},
		Reason:           "dreaming reflection",
		Evidence:         []string{"episode-1", "episode-2"},
		RiskLevel:        PatchRiskMedium, CreatedBy: "dreamer",
	})
	require.NoError(t, err)
	require.Equal(t, PatchStatusProposed, patch.Status)
	require.True(t, patchVisibleToScope(patch, ScopeContext{ProjectKey: store.ProjectKey()}))

	_, err = store.ApplyPatch(ctx, patch.ID, PatchApplyOptions{AutoApproveLowRisk: true, Actor: "admin"})
	require.ErrorContains(t, err, "approved")

	_, err = store.ApprovePatch(ctx, patch.ID, "reviewer", "medium-risk reflection reviewed")
	require.NoError(t, err)
	applied, err := store.ApplyPatch(ctx, patch.ID, PatchApplyOptions{Actor: "admin"})
	require.NoError(t, err)
	require.Equal(t, PatchStatusApplied, applied.Status)
	require.Len(t, applied.AppliedRecordIDs, 1)
	record, err := store.GetRecord(ctx, applied.AppliedRecordIDs[0])
	require.NoError(t, err)
	require.Equal(t, "reflective", record.Kind)
	require.Equal(t, StatusActive, record.Status)

	var auditCount int
	db, err := store.ensureV2()
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(1) FROM memory_events WHERE memory_id = ? AND event_type = 'patch_applied'`, patch.ID).Scan(&auditCount))
	require.Equal(t, 1, auditCount)

	rolledBack, err := store.RollbackPatch(ctx, patch.ID, "reviewer", "undo reflection")
	require.NoError(t, err)
	require.Equal(t, PatchStatusRolledBack, rolledBack.Status)
	record, err = store.GetRecord(ctx, applied.AppliedRecordIDs[0])
	require.NoError(t, err)
	require.Equal(t, StatusDeleted, record.Status)
}

func TestPatchUpdateAndRollbackRestoresVersionedContent(t *testing.T) {
	store := newScopedTestStore(t, "g4-patch-update", "")
	ctx := context.Background()
	original, err := store.Append(AppendNoteOptions{Text: "original patch target", Kind: "reference"})
	require.NoError(t, err)
	base, err := store.GetRecord(ctx, original.ID)
	require.NoError(t, err)

	patch, err := store.CreatePatch(ctx, PatchInput{
		Operation: PatchOperationUpdate, TargetIDs: []string{base.ID},
		ProposedContent: "updated patch target",
		Reason:          "update reviewed target",
		Evidence:        []string{"episode-3"},
		RiskLevel:       PatchRiskMedium, ExpectedRevisions: map[string]int64{base.ID: base.Revision},
		CreatedBy: "dreamer",
	})
	require.NoError(t, err)
	_, err = store.ApprovePatch(ctx, patch.ID, "reviewer", "reviewed update")
	require.NoError(t, err)
	applied, err := store.ApplyPatch(ctx, patch.ID, PatchApplyOptions{Actor: "admin"})
	require.NoError(t, err)
	updated, err := store.GetRecord(ctx, base.ID)
	require.NoError(t, err)
	require.Equal(t, "updated patch target", updated.Text)
	require.Greater(t, updated.Revision, base.Revision)

	_, err = store.RollbackPatch(ctx, applied.ID, "reviewer", "restore old target")
	require.NoError(t, err)
	restored, err := store.GetRecord(ctx, base.ID)
	require.NoError(t, err)
	require.Equal(t, "original patch target", restored.Text)
}

func TestPatchRejectDoesNotMutateMemory(t *testing.T) {
	store := newScopedTestStore(t, "g4-patch-reject", "")
	ctx := context.Background()
	patch, err := store.CreatePatch(ctx, PatchInput{
		Operation: PatchOperationAdd, ProposedContent: "should not be applied",
		Reason: "rejection test", Evidence: []string{"episode-reject"},
		RiskLevel: PatchRiskHigh, CreatedBy: "dreamer",
	})
	require.NoError(t, err)
	rejected, err := store.RejectPatch(ctx, patch.ID, "reviewer", "not safe")
	require.NoError(t, err)
	require.Equal(t, PatchStatusRejected, rejected.Status)
	_, err = store.GetRecord(ctx, patch.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestDreamPipelineCreatesReviewablePatchFromEpisodes(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream", "")
	ctx := context.Background()
	for _, taskID := range []string{"dream-task-a", "dream-task-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "dream-session", Problem: "recurring problem " + taskID,
			Result: "recurring result", Outcome: EpisodeOutcomeSuccess,
			SourceEventIDs: []string{"dream-event-" + taskID}, EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	run, err := store.RunDreamPipeline(ctx, DreamRunOptions{Limit: 10, CreatedBy: "dreamer"})
	require.NoError(t, err)
	require.Equal(t, DreamRunSucceeded, run.Status)
	require.GreaterOrEqual(t, run.InputCount, 2)
	require.Equal(t, 1, run.PatchCount)

	patches, err := store.ListPatches(ctx, PatchFilter{Status: PatchStatusProposed, Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, patches)
	require.Equal(t, PatchOperationAdd, patches[0].Operation)
	require.Equal(t, PatchRiskMedium, patches[0].RiskLevel)
	require.Contains(t, patches[0].ProposedContent, "durable episodes")
	require.NotEmpty(t, patches[0].Evidence)
	require.NotEmpty(t, patches[0].ProposedMetadata["dreaming_source_hash"])
	require.Contains(t, patches[0].Reason, "dreaming:")

	runs, err := store.ListDreamRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
}

func TestDreamPipelineHonorsTokenBudget(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-budget", "")
	ctx := context.Background()
	longText := strings.Repeat("bounded reflection evidence ", 120)
	for _, taskID := range []string{"budget-task-a", "budget-task-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "budget-session", Problem: longText,
			Result: longText, Outcome: EpisodeOutcomeSuccess,
			SourceEventIDs: []string{"budget-event-" + taskID}, EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	run, err := store.RunDreamPipeline(ctx, DreamRunOptions{
		Limit: 10, MaxTokens: 80, CreatedBy: "budgeter",
	})
	require.NoError(t, err)
	require.Equal(t, DreamRunSucceeded, run.Status)
	require.LessOrEqual(t, run.TokenCost, 80)

	patches, err := store.ListPatches(ctx, PatchFilter{Status: PatchStatusProposed, Limit: 10})
	require.NoError(t, err)
	require.Len(t, patches, 1)
	require.NotEmpty(t, patches[0].ProposedContent)
	require.LessOrEqual(t, EstimateTokens(patches[0].ProposedContent), 80)
}

func TestDreamPipelineUsesBoundedAnalyzer(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-analyzer", "")
	ctx := context.Background()
	for _, taskID := range []string{"analyzer-a", "analyzer-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "analyzer-session", Problem: "analyzer problem",
			Outcome: EpisodeOutcomeSuccess, SourceEventIDs: []string{"analyzer-event-" + taskID},
			EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	analyzer := &fakeDreamAnalyzer{result: DreamAnalysis{
		Text: "## Reflection\n\nUse the verified deployment workflow.", Model: "test-model",
	}}
	run, err := store.RunDreamPipeline(ctx, DreamRunOptions{
		Limit: 10, MaxTokens: 80, CreatedBy: "analyzer", Analyzer: analyzer,
	})
	require.NoError(t, err)
	require.Equal(t, DreamRunSucceeded, run.Status)
	require.Empty(t, run.Error)
	require.Equal(t, 1, analyzer.calls)
	patches, err := store.ListPatches(ctx, PatchFilter{Status: PatchStatusProposed, Limit: 10})
	require.NoError(t, err)
	require.Len(t, patches, 1)
	require.Contains(t, patches[0].ProposedContent, "verified deployment workflow")
	require.Equal(t, "test-model", patches[0].ProposedMetadata["dream_model"])
}

func TestDreamPipelineFallsBackWhenAnalyzerFails(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-analyzer-fallback", "")
	ctx := context.Background()
	for _, taskID := range []string{"fallback-a", "fallback-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "fallback-session", Problem: "fallback problem",
			Outcome: EpisodeOutcomeSuccess, SourceEventIDs: []string{"fallback-event-" + taskID},
			EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	_, err := store.RunDreamPipeline(ctx, DreamRunOptions{
		Limit: 10, CreatedBy: "analyzer",
		Analyzer: &fakeDreamAnalyzer{err: errors.New("provider unavailable")},
	})
	require.NoError(t, err)
	patches, err := store.ListPatches(ctx, PatchFilter{Status: PatchStatusProposed, Limit: 10})
	require.NoError(t, err)
	require.Len(t, patches, 1)
	require.Equal(t, "deterministic", patches[0].ProposedMetadata["dream_model"])
	runs, err := store.ListDreamRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Contains(t, runs[0].Error, "dream analyzer fallback")
}

func TestDreamPipelineShadowRecordsReflectionWithoutPatch(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-shadow", "")
	ctx := context.Background()
	for _, taskID := range []string{"shadow-a", "shadow-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "shadow-session", Problem: "shadow problem",
			Outcome: EpisodeOutcomeSuccess, SourceEventIDs: []string{"shadow-event-" + taskID},
			EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	run, err := store.RunDreamPipeline(ctx, DreamRunOptions{
		Limit: 10, CreatedBy: "shadow", Shadow: true, MaxTokens: 120,
		Analyzer: &fakeDreamAnalyzer{result: DreamAnalysis{
			Text: "## Shadow reflection\n\nReuse the verified recovery path.", Model: "shadow-model",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, DreamRunSucceeded, run.Status)
	require.Zero(t, run.PatchCount)
	require.True(t, run.Shadow)
	require.Contains(t, run.ReflectionText, "verified recovery path")
	require.Equal(t, "shadow-model", run.Model)
	patches, err := store.ListPatches(ctx, PatchFilter{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, patches, "shadow mode must not enqueue review patches")
}

func TestDreamPipelineDedupesSourceAcrossCallsAndForceBypasses(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-source-registry", "")
	ctx := context.Background()
	for _, taskID := range []string{"source-a", "source-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "source-session", Problem: "source problem",
			Outcome: EpisodeOutcomeSuccess, SourceEventIDs: []string{"source-event-" + taskID},
			EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	first, err := store.RunDreamPipeline(ctx, DreamRunOptions{Limit: 10, CreatedBy: "first"})
	require.NoError(t, err)
	second, err := store.RunDreamPipeline(ctx, DreamRunOptions{Limit: 10, CreatedBy: "second"})
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "same evidence set must reuse the existing run")
	runs, err := store.ListDreamRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, mustListDreamPatches(t, store), 1)

	forced, err := store.RunDreamPipeline(ctx, DreamRunOptions{Limit: 10, CreatedBy: "forced", Force: true})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, forced.ID)
	runs, err = store.ListDreamRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Len(t, mustListDreamPatches(t, store), 2)
}

func TestDreamSourceRegistryBackfillFromLegacyPatch(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-legacy-backfill", "")
	ctx := context.Background()
	patch, err := store.CreatePatch(ctx, PatchInput{
		Operation: PatchOperationAdd, ProposedContent: "legacy reflection",
		ProposedMetadata: map[string]interface{}{
			"kind": "reflective", "source_kind": "dreaming",
			"dreaming_source_hash": "legacy-source-hash",
			"dream_model":          "legacy-model",
		},
		Reason:    "dreaming:legacy-source-hash: cross-episode reflection",
		Evidence:  []string{"mem_legacy"},
		RiskLevel: PatchRiskMedium, CreatedBy: "legacy",
	})
	require.NoError(t, err)
	exists, err := store.DreamSourceExists(ctx, "legacy-source-hash")
	require.NoError(t, err)
	require.False(t, exists)

	var backfillSQL string
	for _, migration := range memoryMigrations() {
		if migration.Version == 33 {
			backfillSQL = migration.UpSQL
			break
		}
	}
	require.NotEmpty(t, backfillSQL)
	_, err = store.db.ExecContext(ctx, backfillSQL)
	require.NoError(t, err)
	exists, err = store.DreamSourceExists(ctx, "legacy-source-hash")
	require.NoError(t, err)
	require.True(t, exists)

	run, err := store.GetDreamRun(ctx, "dream_legacy_"+patch.ID)
	require.NoError(t, err)
	require.Equal(t, DreamRunSucceeded, run.Status)
	require.Equal(t, 1, run.PatchCount)
	require.Equal(t, "legacy reflection", run.ReflectionText)
	require.Equal(t, "legacy-model", run.Model)
}

func TestPruneShadowDreamRunsKeepsPatchedRuns(t *testing.T) {
	store := newScopedTestStore(t, "g4-dream-shadow-retention", "")
	ctx := context.Background()
	for _, taskID := range []string{"retention-a", "retention-b"} {
		_, err := store.CreateEpisode(ctx, EpisodeInput{
			TaskID: taskID, SessionID: "retention-session", Problem: "retention problem",
			Outcome: EpisodeOutcomeSuccess, SourceEventIDs: []string{"retention-event-" + taskID},
			EndedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	oldShadow, err := store.RunDreamPipeline(ctx, DreamRunOptions{
		Limit: 10, CreatedBy: "shadow-old", Shadow: true,
	})
	require.NoError(t, err)
	recentShadow, err := store.RunDreamPipeline(ctx, DreamRunOptions{
		Limit: 10, CreatedBy: "shadow-recent", Shadow: true,
	})
	require.NoError(t, err)
	patched, err := store.RunDreamPipeline(ctx, DreamRunOptions{Limit: 10, CreatedBy: "patched"})
	require.NoError(t, err)

	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `UPDATE memory_dream_runs SET created_at = ? WHERE id IN (?, ?)`,
		old, oldShadow.ID, patched.ID)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE memory_dream_runs SET created_at = ? WHERE id = ?`,
		recent, recentShadow.ID)
	require.NoError(t, err)

	deleted, err := store.PruneShadowDreamRuns(ctx, time.Now().UTC().Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	runs, err := store.ListDreamRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	ids := map[string]bool{runs[0].ID: true, runs[1].ID: true}
	require.True(t, ids[recentShadow.ID])
	require.True(t, ids[patched.ID])
	require.False(t, ids[oldShadow.ID])
}

func mustListDreamPatches(t *testing.T, store *Store) []MemoryPatch {
	t.Helper()
	patches, err := store.ListPatches(context.Background(), PatchFilter{Limit: 20})
	require.NoError(t, err)
	return patches
}

func TestEpisodePartialOutcomeAndFailedWarning(t *testing.T) {
	store := newScopedTestStore(t, "g4-episode-outcome", "")
	ctx := context.Background()
	partial, err := store.CreateEpisode(ctx, EpisodeInput{
		TaskID: "partial-task", SessionID: "partial-session",
		Problem: "partially completed task", Result: "one of two goals",
		Outcome: EpisodeOutcomePartial, SourceEventIDs: []string{"partial-event"},
		EndedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, EpisodeOutcomePartial, partial.Outcome)

	failed, err := store.CreateEpisode(ctx, EpisodeInput{
		TaskID: "failed-task", SessionID: "failed-session",
		Problem: "failed task", FailureReason: "compile failed",
		Outcome: EpisodeOutcomeFailed, SourceEventIDs: []string{"failed-event"},
		EndedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	record, err := store.GetRecord(ctx, failed.MemoryID)
	require.NoError(t, err)
	require.Equal(t, "episodic", record.Kind)
	require.Contains(t, record.Text, "compile failed")
	require.NotEqual(t, "procedure", record.Kind)
}

func TestLowRiskPatchAutoApplyAndFTSVisibility(t *testing.T) {
	store := newScopedTestStore(t, "g4-patch-low-risk", "")
	ctx := context.Background()
	patch, err := store.CreatePatch(ctx, PatchInput{
		Operation:        PatchOperationAdd,
		ProposedContent:  "AzureOrchid is the reviewed low risk retrieval marker.",
		ProposedMetadata: map[string]interface{}{"kind": "reference"},
		Reason:           "low risk reference addition", Evidence: []string{"low-risk-event"},
		RiskLevel: PatchRiskLow, CreatedBy: "dreamer",
	})
	require.NoError(t, err)
	applied, err := store.ApplyPatch(ctx, patch.ID, PatchApplyOptions{
		AutoApproveLowRisk: true, Actor: "admin",
	})
	require.NoError(t, err)
	require.Equal(t, PatchStatusApplied, applied.Status)
	require.Len(t, applied.AppliedRecordIDs, 1)

	hits, err := store.Search(SearchOptions{Query: "AzureOrchid", Limit: 5})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, applied.AppliedRecordIDs[0], hits[0].Note.ID)
}

func TestPatchScopeIsolation(t *testing.T) {
	store := newScopedTestStore(t, "g4-patch-scope", "")
	aliceCtx := WithMemoryScope(context.Background(), ScopeContext{OwnerID: "alice"})
	patch, err := store.CreatePatch(aliceCtx, PatchInput{
		Operation: PatchOperationAdd, ProposedContent: "alice scoped patch",
		Reason: "scope isolation", Evidence: []string{"alice-event"},
		RiskLevel: PatchRiskMedium, ScopeKind: ScopeUser, CreatedBy: "dreamer",
	})
	require.NoError(t, err)
	require.Equal(t, ScopeUser, patch.ScopeKind)
	require.Equal(t, "alice", patch.ScopeKey)
	_, err = store.GetPatch(context.Background(), patch.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	visible, err := store.GetPatch(aliceCtx, patch.ID)
	require.NoError(t, err)
	require.Equal(t, patch.ID, visible.ID)
}
