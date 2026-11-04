package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetricsSnapshotResumeAdmissionLedger(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "resume-admission-metrics")
	now := time.Now().UTC()

	submitted, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-submitted",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-1",
		ParentEpisodeID: "ep-1",
		ClaimedAt:       now,
	})
	require.NoError(t, err)
	require.True(t, won)
	_, err = store.MarkResumeSubmitted(ctx, submitted.ClaimKey, "turn-model-1", now, submitted.Version)
	require.NoError(t, err)

	released, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-released",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-2",
		ParentEpisodeID: "ep-2",
		ClaimedAt:       now,
	})
	require.NoError(t, err)
	require.True(t, won)
	_, err = store.ReleaseResumeClaim(ctx, released.ClaimKey, released.Version)
	require.NoError(t, err)

	recoveredSubmitted, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-recovered-submitted",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-3",
		ParentEpisodeID: "ep-3",
		ClaimedAt:       now,
	})
	require.NoError(t, err)
	require.True(t, won)
	applied, err := store.MarkResumeRecovered(ctx, recoveredSubmitted.ClaimKey, "turn-model-3", ResumeClaimRecoverySubmitted, now, recoveredSubmitted.Version)
	require.NoError(t, err)
	require.True(t, applied)

	recoveredUnsubmitted, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-recovered-unsubmitted",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-4",
		ParentEpisodeID: "ep-4",
		ClaimedAt:       now,
	})
	require.NoError(t, err)
	require.True(t, won)
	applied, err = store.MarkResumeRecovered(ctx, recoveredUnsubmitted.ClaimKey, "", ResumeClaimRecoveryUnsubmitted, now, recoveredUnsubmitted.Version)
	require.NoError(t, err)
	require.True(t, applied)

	recoveryPending, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-recovery-pending",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-5",
		ParentEpisodeID: "ep-5",
		ClaimedAt:       now,
	})
	require.NoError(t, err)
	require.True(t, won)
	applied, err = store.MarkResumeRecovered(ctx, recoveryPending.ClaimKey, "", ResumeClaimRecoveryPending, now, recoveryPending.Version)
	require.NoError(t, err)
	require.True(t, applied)

	// Legacy bulk recovery has no parent-state evidence. It must remain
	// duplicate-safe while surfacing its uncertainty separately.
	legacyUnknown, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-recovered-unknown",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-6",
		ParentEpisodeID: "ep-6",
		ClaimedAt:       now.Add(-10 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, won)
	_, err = store.RecoverStaleResumeClaims(ctx, now.Add(-time.Minute))
	require.NoError(t, err)
	require.NotEmpty(t, legacyUnknown.ClaimKey)

	for _, consumption := range []string{"model_turn", "notification_only", "stale", "observed"} {
		require.NoError(t, store.MarkWakeDelivered(ctx, WakeDelivered{
			NotifyKey:   "nk-" + consumption,
			RootScopeID: "root-1",
			DeliveredAt: now,
			Consumption: consumption,
		}))
	}

	snapshot, err := CollectMetricsSnapshot(ctx, store, MetricsSnapshotOptions{})
	require.NoError(t, err)
	metrics := snapshot.ResumeAdmission
	require.Equal(t, 3, metrics.Submitted)
	require.Equal(t, 2, metrics.Released)
	require.Equal(t, 1, metrics.Claimed)
	require.Equal(t, 3, metrics.Recovered)
	require.Equal(t, 1, metrics.RecoveredSubmitted)
	require.Equal(t, 1, metrics.RecoveredUnsubmitted)
	require.Equal(t, 1, metrics.RecoveryPending)
	require.Equal(t, 1, metrics.RecoveredUnknown)
	require.Equal(t, 1, metrics.ModelTurns)
	require.Equal(t, 1, metrics.NotificationOnly)
	require.Equal(t, 1, metrics.Stale)
	require.Equal(t, 1, metrics.Observed)
}
