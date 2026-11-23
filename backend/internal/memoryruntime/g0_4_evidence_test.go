package memoryruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// TestG0ActiveSessionCaptureReceiptExtraction proves that a frozen turn range
// may be extracted before its containing session ends. The resulting memory
// remains attributable to the capture source and can be selected for injection
// into a different session.
func TestG0ActiveSessionCaptureReceiptExtraction(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, extractor, capture := newMaterializedCaptureRuntime(t, 0, true)
	runtime.Config.TurnDigestStoreText = true
	activeAt := runtime.Clock()
	source.fakeSource.sessions[0].LastActivityAt = activeAt

	result, err := runtime.RunPendingTurnCaptureReceiptsBounded(ctx, TriggerIdle, 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed, "active receipt extraction result: %+v", result)
	require.Equal(t, 1, extractor.calls)
	require.Equal(t, "active", source.fakeSource.sessions[0].State)
	require.Less(t, runtime.Clock().Sub(activeAt), runtime.Config.MinIdle,
		"the active session must not be idle for this receipt-backed path")
	require.Zero(t, source.readCalls, "receipt-backed extraction must read only the closed canonical window")

	candidates, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 10})
	require.NoError(t, err)
	require.Len(t, candidates.Items, 1)
	require.Equal(t, capture.SessionID, candidates.Items[0].SourceSessionID)
	require.Equal(t, capture.SessionGeneration, candidates.Items[0].SourceSessionGeneration)
	require.Equal(t, "session_extract", candidates.Items[0].SourceKind)
	require.Equal(t, capture.TurnID, candidates.Items[0].SourceTurnID)

	consolidated, err := NewConsolidator(store, runtime.Config).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, consolidated.Promoted)
	selection, err := store.SelectForInjectV2(memorystore.InjectOptions{
		Query: "durable capture", Limit: 5, TokenBudget: 900, SessionID: "new-session",
	})
	require.NoError(t, err)
	require.NotEmpty(t, selection.IDs)
	require.Contains(t, selection.IDs, candidates.Items[0].ID)
	// The selection above is the injection boundary; this fixture leaves recall
	// tracing disabled, so no trace row is expected.
}

// TestG0CrashRecoveryAfterCaptureReceipt exercises the post-commit crash
// boundary: a fresh Runtime drains the same materialized receipt and a replay
// is idempotent. A receipt from a reused session incarnation is quarantined
// rather than read against the new transcript.
func TestG0CrashRecoveryAfterCaptureReceipt(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, extractor, capture := newMaterializedCaptureRuntime(t, 0, true)
	runtime.Config.TurnDigestRecoveryOnStartup = true

	restarted := New(store, source, extractor, fakeModes{}, runtime.Config)
	restarted.Clock = runtime.Clock
	first, err := restarted.RunPendingTurnCaptureReceipts(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Equal(t, 1, first.Processed)
	require.Equal(t, 1, extractor.calls)

	second, err := restarted.RunPendingTurnCaptureReceipts(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Zero(t, second.Processed)
	require.Equal(t, 1, extractor.calls, "replaying a committed receipt must not invoke extraction again")
	jobs, err := store.ListJobs(ctx, 20)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, memorystore.JobStatusSucceeded, jobs[0].Status)

	source.receipt.State = "captured"
	source.fakeSource.sessions[0].SessionGeneration = capture.SessionGeneration + 1
	materialized, err := restarted.MaterializeTurnCaptureReceipts(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, materialized)
	require.Equal(t, "deferred", source.receipt.State)
	require.Contains(t, source.receipt.LastError, "session_generation_mismatch")
}

// TestG0StatusExplainsBacklog checks that a pending extraction is visible in
// the operational backlog counters, including its distinct session count.
func TestG0StatusExplainsBacklog(t *testing.T) {
	ctx := context.Background()
	runtime, store, _, _, _ := newMaterializedCaptureRuntime(t, 0, false)
	_, err := store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "session:capture-session:backlog-hash",
		ProjectKey: store.ProjectKey(), SessionID: "capture-session", MaxAttempts: 3,
	})
	require.NoError(t, err)

	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.PendingExtractionJobs)
	require.Equal(t, 1, status.PendingExtractionSessions)
	require.Equal(t, 1, status.ExtractionJobCounts["pending"])
	require.Equal(t, 1, status.JobsByKind["phase1_extract"][string(memorystore.JobStatusPending)])
	require.Equal(t, runtime.Config.ProjectKey, store.ProjectKey())
}

func TestGenerationDisabledMaterializesReceiptWithoutExtraction(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, extractor, capture := newMaterializedCaptureRuntime(t, 0, true)
	runtime.Config.GenerationDisabled = true
	result, err := runtime.Run(ctx, TriggerIdle)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	require.Equal(t, "materialized", source.receipt.State)
	digest, err := store.GetTurnDigestByCaptureReceipt(ctx, source.receipt.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestPending, digest.Status)
	require.Equal(t, capture.TurnID, digest.TurnID)
	require.Zero(t, source.readCalls, "generation-disabled must not read transcript")
}

func TestModeOffDoesNotConsumeMaterializedReceipt(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, extractor, _ := newMaterializedCaptureRuntime(t, 0, true)
	runtime.Config.Mode = "off"
	result, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	require.Equal(t, "materialized", source.receipt.State)
	digest, err := store.GetTurnDigestByCaptureReceipt(ctx, source.receipt.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestPending, digest.Status)
	require.Zero(t, source.readCalls)
}
