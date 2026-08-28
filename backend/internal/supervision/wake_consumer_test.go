package supervision

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWakeConsumer_DeliversOneParentTurnPerDrain verifies the happy path:
// pending critical wakes drain into exactly one Deliver call and the claims
// are resolved so later events for the same dedup key coalesce again
// (doc 6.5 rules 1/5).
func TestWakeConsumer_DeliversOneParentTurnPerDrain(t *testing.T) {
	store := newTestStore(t, "wake-consumer-deliver")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
	}

	var mu sync.Mutex
	var deliveries []string
	consumer.Deliver = func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error {
		mu.Lock()
		deliveries = append(deliveries, parentSessionID+"|"+strconv.Itoa(len(digest.Items)))
		mu.Unlock()
		return nil
	}

	// Two children fail before any drain => one aggregated parent turn.
	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)
	_, err = ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-2",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))

	mu.Lock()
	require.Len(t, deliveries, 1, "two critical wakes must coalesce into one parent turn")
	require.Equal(t, "root-session-1|2", deliveries[0], "wake digest must include both triggering notifications")
	mu.Unlock()

	// Wakes are resolved: a drain now finds nothing to do.
	claimed, digest, err := scheduler.DrainRunnable(ctx, "root-session-1", "", "root-session-1", func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true })
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.Nil(t, digest)
}

// TestWakeConsumer_ParentBusyKeepsWakeDurable verifies doc 6.5 rule 2: a
// busy parent must not receive a second concurrent turn; the wake stays
// durable until a later runnable transition.
func TestWakeConsumer_ParentBusyKeepsWakeDurable(t *testing.T) {
	store := newTestStore(t, "wake-consumer-busy")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	runnable := true
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return runnable },
	}
	delivered := false
	consumer.Deliver = func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error {
		delivered = true
		return nil
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)

	runnable = false
	err = consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1")
	require.ErrorIs(t, err, ErrWakeParentBusy)
	require.False(t, delivered, "busy parent must not receive a turn")

	// Parent becomes idle: the same runnable transition drains the wake.
	runnable = true
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.True(t, delivered)

	claimed, _, err := scheduler.DrainRunnable(ctx, "root-session-1", "", "root-session-1", func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true })
	require.NoError(t, err)
	require.Empty(t, claimed, "wake must be resolved after delivery")
}

func TestWakeConsumer_AcknowledgedPendingWakeDoesNotStartParentTurn(t *testing.T) {
	store := newTestStore(t, "wake-consumer-acknowledged")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	delivered := false
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, *Digest, []string) error {
			delivered = true
			return nil
		},
	}

	notification, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)
	acknowledged, err := store.AcknowledgeNotification(ctx, notification.NotificationID, time.Now().UTC(), notification.Version)
	require.NoError(t, err)
	require.True(t, acknowledged)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.False(t, delivered, "an acknowledged stale wake must not submit the fixed auto-wake prompt")

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Empty(t, pending, "the stale durable wake must still be resolved")
}

// TestWakeConsumer_DeliveryFailureReleasesClaims verifies that a failed
// delivery does not wedge the dedup key: claims are released and the
// notification stays durable in the inbox (preflight fallback, doc 6.5).
func TestWakeConsumer_DeliveryFailureReleasesClaims(t *testing.T) {
	store := newTestStore(t, "wake-consumer-fail")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error {
			return errors.New("turn queue full")
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)

	err = consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1")
	require.ErrorContains(t, err, "turn queue full")

	// The same dedup key is usable again: a fresh event coalesces into a
	// fresh wake and the next drain can deliver.
	_, err = ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-2",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)

	delivered := false
	consumer.Deliver = func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error {
		delivered = true
		return nil
	}
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.True(t, delivered)
}

// TestWakeConsumer_NoWakesNoDelivery verifies the consumer is a cheap no-op
// at runnable transitions that have no pending wakes.
func TestWakeConsumer_NoWakesNoDelivery(t *testing.T) {
	store := newTestStore(t, "wake-consumer-none")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	delivered := false
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error {
			delivered = true
			return nil
		},
	}
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.False(t, delivered)
	require.NoError(t, consumer.MaybeWakeParent(ctx, "", "", "root-session-1"))
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", ""))
	require.False(t, delivered)
}

// TestWakeConsumer_RateLimitKeepsWakeDurable verifies doc 6.5 rule 4: the
// auto-turn budget is enforced per root scope and a rate-limited wake stays
// durable for the next window / natural turn.
func TestWakeConsumer_RateLimitKeepsWakeDurable(t *testing.T) {
	store := newTestStore(t, "wake-consumer-ratelimit")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error {
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))

	// Second critical event within the window: rate limited, wake kept.
	_, err = ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-2",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)
	err = consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1")
	require.ErrorIs(t, err, ErrWakeRateLimited)

	// The wake remains claimable once the budget allows (a fresh scheduler
	// instance shares the durable store but has its own in-memory budget).
	fresh := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 5,
	})
	claimed, digest, err := fresh.DrainRunnable(ctx, "root-session-1", "", "root-session-1", func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true })
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NotNil(t, digest)
}
