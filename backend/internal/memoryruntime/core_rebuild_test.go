package memoryruntime

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestCoreRebuilderCreatesReviewableCandidateFromPinnedAndRecentUsage(t *testing.T) {
	ctx := context.Background()
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-rebuild-runtime",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	activeSource, err := store.Append(memorystore.AppendNoteOptions{
		Text: "The active Core remains unchanged until an administrator reviews a candidate.",
	})
	require.NoError(t, err)
	activeCandidate, err := store.CreateCoreCandidate(ctx, memorystore.CoreMemoryCandidateOptions{
		Content:   "Keep the active project memory stable until review.",
		SourceIDs: []string{activeSource.ID},
	})
	require.NoError(t, err)
	active, err := store.ActivateCore(ctx, activeCandidate.ID, "admin:test")
	require.NoError(t, err)

	used := false
	derived, err := store.Append(memorystore.AppendNoteOptions{
		Text:   "Recent derived evidence should be eligible for a bounded Core proposal.",
		Kind:   "decision",
		Pinned: &used,
	})
	require.NoError(t, err)
	_, err = store.RecordExposure(ctx, memorystore.Exposure{
		MemoryIDs: []string{derived.ID, derived.ID},
		SessionID: "session-core-rebuild",
		UseKind:   memorystore.UsageCited,
		UsedAt:    time.Now().UTC(),
	})
	require.NoError(t, err)

	result, err := NewCoreRebuilder(store, Config{
		Mode: "on", WorkerID: "core-rebuild-test",
		CoreRebuildMaxRecords: 4, CoreRebuildMaxUsageRows: 10,
		CoreRebuildTokenBudget: 400,
	}).Run(ctx)
	require.NoError(t, err)
	require.NotNil(t, result.Candidate)
	require.Equal(t, memorystore.CoreStatusCandidate, result.Candidate.Status)
	require.Contains(t, result.Candidate.Content, "active Core remains unchanged")
	require.Contains(t, result.Candidate.Content, "## Stable pinned records")
	require.Contains(t, result.Candidate.Content, "## Recent derived evidence")
	require.Contains(t, result.Candidate.Content, "Recent derived evidence")
	require.Contains(t, result.Candidate.SourceIDs, activeSource.ID)
	require.Contains(t, result.Candidate.SourceIDs, derived.ID)
	require.GreaterOrEqual(t, result.UsageRows, 1)

	job, err := store.GetJob(ctx, result.JobID)
	require.NoError(t, err)
	require.Equal(t, memorystore.JobStatusSucceeded, job.Status)
	require.NotEmpty(t, job.LastSuccessWatermark)

	current, err := store.GetCore(ctx)
	require.NoError(t, err)
	require.Equal(t, active.ID, current.ID)
	require.Equal(t, memorystore.CoreStatusActive, current.Status)
	_, err = store.ListCore(ctx, memorystore.CoreMemoryFilter{
		Status: memorystore.CoreStatusActive, Limit: 10,
	})
	require.NoError(t, err)
}

func TestCoreRebuilderCompletesWithoutCandidateWhenNoSafeSourcesExist(t *testing.T) {
	ctx := context.Background()
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-rebuild-empty",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	unpinned := false
	_, err = store.Append(memorystore.AppendNoteOptions{
		Text:   "This record is not pinned and has not been used recently.",
		Pinned: &unpinned,
	})
	require.NoError(t, err)

	result, err := NewCoreRebuilder(store, Config{
		Mode: "on", WorkerID: "core-rebuild-empty-test",
	}).Run(ctx)
	require.NoError(t, err)
	require.Nil(t, result.Candidate)
	require.Equal(t, "no_safe_sources", result.Skipped)

	job, err := store.GetJob(ctx, result.JobID)
	require.NoError(t, err)
	require.Equal(t, memorystore.JobStatusSucceeded, job.Status)
	candidates, err := store.ListCore(ctx, memorystore.CoreMemoryFilter{
		Status: memorystore.CoreStatusCandidate, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, candidates)
	_, err = store.GetCore(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestCoreRebuilderSkipsExpiredUsageSource(t *testing.T) {
	ctx := context.Background()
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-rebuild-expired",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	unpinned := false
	source, err := store.Append(memorystore.AppendNoteOptions{
		Text:   "Expired usage evidence must not become a Core source.",
		Pinned: &unpinned,
	})
	require.NoError(t, err)
	_, err = store.RecordExposure(ctx, memorystore.Exposure{
		MemoryIDs: []string{source.ID}, SessionID: "session-expired",
		UseKind: memorystore.UsageRead, UsedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	expired := memorystore.StatusExpired
	_, err = store.UpdateRecord(ctx, source.ID, memorystore.RecordPatch{Status: &expired})
	require.NoError(t, err)

	result, err := NewCoreRebuilder(store, Config{
		Mode: "on", WorkerID: "core-rebuild-expired-test",
	}).Run(ctx)
	require.NoError(t, err)
	require.Nil(t, result.Candidate)
	require.Equal(t, "no_safe_sources", result.Skipped)
}
