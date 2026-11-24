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
// claimed_at forward and publish a new fencing version.
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
	claims := map[string]ResumeClaim{claim.ClaimKey: claim}
	stop := consumer.startClaimRenewal(claims)
	require.NotNil(t, stop)
	t.Cleanup(stop)

	renewedAt := now.Add(time.Minute)
	clockNanos.Store(renewedAt.UnixNano())
	require.Eventually(t, func() bool {
		stored, readErr := store.GetResumeClaim(ctx, claim.ClaimKey)
		return readErr == nil && stored != nil &&
			!stored.ClaimedAt.Before(renewedAt) &&
			stored.Version > claim.Version
	}, 2*time.Second, 5*time.Millisecond, "the claimed row must receive a heartbeat update")

	stored, err := store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Greater(t, stored.Version, claim.Version, "heartbeat must advance the fencing token")
	require.Equal(t, ResumeClaimStateClaimed, stored.State)

	stop()
	stop() // idempotent
	current, ok := claims[claim.ClaimKey]
	require.True(t, ok)
	require.Equal(t, stored.Version, current.Version)
	require.ErrorIs(t, service.MarkSubmitted(ctx, claim, "stale-heartbeat-model"), ErrResumeClaimFenceLost)
	stored, err = store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, ResumeClaimStateClaimed, stored.State, "the pre-renewal token must be fenced out")
	require.NoError(t, service.MarkSubmitted(ctx, current, "turn-heartbeat-model"))
	stored, err = store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, ResumeClaimStateSubmitted, stored.State)
}

func TestWakeClaimRenewalKeepsOrdinaryWakeLeaseWarm(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "wake-lease-heartbeat")
	now := time.Now().UTC()
	var clockNanos atomic.Int64
	clockNanos.Store(now.UnixNano())
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		WakeClaimStaleAfter: 60 * time.Millisecond,
	})
	scheduler.now = func() time.Time {
		return time.Unix(0, clockNanos.Load()).UTC()
	}
	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:      "wake-lease-heartbeat",
		RootScopeID: "root-wake-lease-heartbeat",
		WakeReason:  "child.failed",
		DedupKey:    "root-wake-lease-heartbeat|wake-lease-heartbeat",
	}))
	owner := "wake-lease-owner"
	claimed, err := store.ClaimWakePending(ctx, "wake-lease-heartbeat", owner, now)
	require.NoError(t, err)
	require.True(t, claimed)

	wakes := []WakePending{{WakeID: "wake-lease-heartbeat", ClaimedBy: owner}}
	consumer := &WakeConsumer{Wakes: scheduler}
	deliveryCtx, stop := consumer.startWakeClaimRenewal(ctx, wakes)
	t.Cleanup(stop)

	renewedAt := now.Add(30 * time.Millisecond)
	clockNanos.Store(renewedAt.UnixNano())
	require.Eventually(t, func() bool {
		rows, readErr := store.ListWakePending(ctx, WakeFilter{
			RootScopeID: "root-wake-lease-heartbeat",
		})
		return readErr == nil && len(rows) == 1 && rows[0].ClaimedAt != nil &&
			!rows[0].ClaimedAt.Before(renewedAt)
	}, time.Second, 5*time.Millisecond, "heartbeat must advance claimed_at")
	require.NoError(t, deliveryCtx.Err(), "a successful wake renewal must keep delivery alive")
	stop()
}

type wakeRenewalFailureStore struct {
	Store
}

func (s wakeRenewalFailureStore) RenewWakePending(context.Context, []string, string, time.Time, time.Time) (int64, error) {
	return 0, nil
}

func TestWakeClaimRenewalFailureCancelsDeliveryContext(t *testing.T) {
	ctx := context.Background()
	base := newTestStore(t, "wake-lease-heartbeat-failure")
	store := wakeRenewalFailureStore{Store: base}
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		WakeClaimStaleAfter: 30 * time.Millisecond,
	})
	consumer := &WakeConsumer{Wakes: scheduler}
	wakes := []WakePending{{WakeID: "lost-wake", ClaimedBy: "lost-owner"}}
	deliveryCtx, stop := consumer.startWakeClaimRenewal(ctx, wakes)
	t.Cleanup(stop)

	require.Eventually(t, func() bool {
		return deliveryCtx.Err() != nil
	}, time.Second, 5*time.Millisecond, "a missing renewed lease must cancel host delivery")
	stop()
}

func TestWakeConsumerRejectsLateDeliveryAfterWakeLeaseLoss(t *testing.T) {
	ctx := context.Background()
	base := newTestStore(t, "wake-lease-late-delivery")
	store := wakeRenewalFailureStore{Store: base}
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		WakeClaimStaleAfter: 300 * time.Millisecond,
	})
	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-wake-late-delivery",
		TargetParentSessionID: "parent-wake-late-delivery",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-wake-late-delivery",
		EventType:             "child.failed",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTerminated,
	})
	require.NoError(t, err)

	deliverStarted := make(chan struct{})
	resultCh := make(chan error, 1)
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(deliveryCtx context.Context, _, _ string, _ *Digest, _ []string) error {
			close(deliverStarted)
			<-deliveryCtx.Done()
			return nil
		},
	}
	go func() {
		resultCh <- consumer.MaybeWakeParent(ctx, "parent-wake-late-delivery", "", "root-wake-late-delivery")
	}()
	select {
	case <-deliverStarted:
	case err := <-resultCh:
		require.ErrorIs(t, err, ErrWakeClaimFenceLost)
		return
	case <-time.After(2 * time.Second):
		t.Fatal("wake delivery did not start")
	}
	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, ErrWakeClaimFenceLost)
	case <-time.After(2 * time.Second):
		t.Fatal("wake consumer did not stop after lease loss")
	}

	pending, err := base.ListWakePending(ctx, WakeFilter{
		RootScopeID: "root-wake-late-delivery",
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, WakeDeliveryStateSubmitting, pending[0].DeliveryState)
}
