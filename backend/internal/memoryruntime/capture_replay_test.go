package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestPreviewDigestReplayRecoveryReportsSafeBoundedClassifications(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, _, capture := newMaterializedCaptureRuntime(t, 0, true)
	legacy, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		ProjectKey: store.ProjectKey(), SessionID: "legacy-session",
		SessionGeneration: 1, TurnID: "legacy-turn",
		Status: memorystore.TurnDigestPending, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	jobsBefore, err := store.ListJobs(ctx, 100)
	require.NoError(t, err)
	digestBefore, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	marksBefore := source.markCalls
	validationsBefore := source.validationCalls

	report, err := runtime.PreviewDigestReplayRecovery(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 2, report.Count)
	require.Equal(t, 10, report.Limit)
	require.Equal(t, 1, report.ByClass[memorystore.DigestReplayAlreadyQueued])
	require.Equal(t, 1, report.ByClass[memorystore.DigestReplayMissingReceipt])
	require.Len(t, report.Items, 2)
	for _, item := range report.Items {
		require.True(t, item.DryRun)
		require.NotContains(t, item.Reason, "provider")
		require.NotContains(t, item.Reason, "canonical range changed")
	}
	require.Equal(t, digestBefore.ID, report.Items[0].DigestID,
		"oldest-first reporting should retain stable digest ordering")
	require.Equal(t, legacy.ID, report.Items[1].DigestID)
	require.Equal(t, "materialized", source.receipt.State,
		"preview must not acknowledge or mutate a canonical receipt")
	require.Equal(t, marksBefore, source.markCalls)
	require.Equal(t, validationsBefore+1, source.validationCalls)

	jobsAfter, err := store.ListJobs(ctx, 100)
	require.NoError(t, err)
	digestAfter, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Equal(t, jobsBefore, jobsAfter)
	require.Equal(t, digestBefore.Status, digestAfter.Status)
	require.Equal(t, digestBefore.ProcessJobID, digestAfter.ProcessJobID)
}

func TestPreviewDigestReplayUsesCurrentSessionGeneration(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, _, capture := newMaterializedCaptureRuntime(t, 0, true)
	source.fakeSource.sessions[0].SessionGeneration = capture.SessionGeneration + 1

	result, err := runtime.PreviewDigestReplay(ctx, "td_"+capture.ID)
	require.Error(t, err)
	require.Empty(t, result.Classification,
		"unknown digest IDs must not be guessed from a receipt id")

	digest, err := store.GetTurnDigestByCaptureReceipt(ctx, capture.ID)
	require.NoError(t, err)
	result, err = runtime.PreviewDigestReplay(ctx, digest.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.DigestReplayStaleGeneration, result.Classification)
	require.True(t, result.DryRun)
}

func TestReplayDigestUsesMaintenanceLeaseAndPreservesQueuedJob(t *testing.T) {
	ctx := context.Background()
	runtime, store, _, _, capture := newMaterializedCaptureRuntime(t, 0, true)
	digest, err := store.GetTurnDigestByCaptureReceipt(ctx, capture.ID)
	require.NoError(t, err)
	jobsBefore, err := store.ListJobs(ctx, 20)
	require.NoError(t, err)

	result, err := runtime.ReplayDigest(ctx, digest.ID, "operator replay")
	require.NoError(t, err)
	require.Equal(t, memorystore.DigestReplayAlreadyQueued, result.Classification)
	require.Equal(t, digest.ID, result.DigestID)
	require.NotEmpty(t, result.JobID)

	jobsAfter, err := store.ListJobs(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, jobsBefore, jobsAfter, "an already queued digest must not duplicate or reset its job")
	require.NoError(t, store.ValidateMaintenanceLeaseContext(ctx))
}

func TestReplayDigestRefusesToRaceMaintenanceOwner(t *testing.T) {
	ctx := context.Background()
	runtime, store, _, _, capture := newMaterializedCaptureRuntime(t, 0, true)
	digest, err := store.GetTurnDigestByCaptureReceipt(ctx, capture.ID)
	require.NoError(t, err)
	jobsBefore, err := store.ListJobs(ctx, 20)
	require.NoError(t, err)

	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "maintenance-owner", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	defer func() { _ = store.ReleaseMaintenanceLease(context.Background(), lease) }()

	result, err := runtime.ReplayDigest(ctx, digest.ID, "operator replay")
	require.ErrorIs(t, err, memorystore.ErrMaintenanceLeaseBusy)
	require.Equal(t, memorystore.DigestReplayBlockedMaintenance, result.Classification)

	jobsAfter, err := store.ListJobs(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, jobsBefore, jobsAfter, "a blocked replay must not mutate the job backlog")
}
