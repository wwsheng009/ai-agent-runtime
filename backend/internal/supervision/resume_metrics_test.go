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

	recovered, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "claim-recovered",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-3",
		ParentEpisodeID: "ep-3",
		ClaimedAt:       now.Add(-10 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, won)
	db, err := store.dbOrErr()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		UPDATE supervision_resume_claims SET claimed_at = ? WHERE claim_key = ?
	`, formatSupervisionTime(now.Add(-10*time.Minute)), recovered.ClaimKey)
	require.NoError(t, err)
	_, err = store.RecoverStaleResumeClaims(ctx, now.Add(-time.Minute))
	require.NoError(t, err)

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
	require.Equal(t, 2, metrics.Submitted)
	require.Equal(t, 1, metrics.Recovered)
	require.Equal(t, 1, metrics.Released)
	require.Equal(t, 1, metrics.ModelTurns)
	require.Equal(t, 1, metrics.NotificationOnly)
	require.Equal(t, 1, metrics.Stale)
	require.Equal(t, 1, metrics.Observed)
}
