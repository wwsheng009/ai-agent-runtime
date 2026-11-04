package supervision

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWakeClaimRenewalHeartbeat verifies the consumer keeps a claimed row warm
// while delivery is in flight. The clock is injectable so the assertion is
// deterministic: after advancing the service clock, a heartbeat tick must move
// claimed_at forward without bumping the claim version.
func TestWakeClaimRenewalHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "wake-claim-renew-heartbeat")
	now := time.Now().UTC()
	var clockNanos atomic.Int64
	clockNanos.Store(now.UnixNano())
	service := NewResumeAdmissionService(ResumeAdmissionConfig{
		Claims:          store,
		Now:             func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() },
		ClaimStaleAfter: 60 * time.Millisecond,
	})
	claim, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "heartbeat-claim",
		RootScopeID:     "root-heartbeat",
		ParentSessionID: "root-heartbeat",
		ParentTurnID:    "turn-heartbeat",
		ParentEpisodeID: "ep-heartbeat",
		ClaimedAt:       now.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, won)

	consumer := &WakeConsumer{Admission: service}
	stop := consumer.startClaimRenewal(map[string]ResumeClaim{claim.ClaimKey: claim})
	require.NotNil(t, stop)
	t.Cleanup(stop)

	renewedAt := now.Add(time.Minute)
	clockNanos.Store(renewedAt.UnixNano())
	require.Eventually(t, func() bool {
		stored, readErr := store.GetResumeClaim(ctx, claim.ClaimKey)
		return readErr == nil && stored != nil && !stored.ClaimedAt.Before(renewedAt)
	}, 2*time.Second, 5*time.Millisecond, "the claimed row must receive a heartbeat update")

	stored, err := store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, claim.Version, stored.Version, "heartbeat must keep the holder's CAS token valid")
	require.Equal(t, ResumeClaimStateClaimed, stored.State)

	stop()
	stop() // idempotent
	require.NoError(t, service.MarkSubmitted(ctx, claim, "turn-heartbeat-model"))
	stored, err = store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, ResumeClaimStateSubmitted, stored.State)
}
