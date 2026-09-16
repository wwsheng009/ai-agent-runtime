package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// selfCheckTestScheduler builds the shape that reaches the self-check in
// production: a single bounded unit, so the next failure wake is deferred by
// the exhausted class budget.
func selfCheckTestScheduler(store Store, selfCheckPerWindow int) *WakeScheduler {
	return NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		SelfCheckPerWindow:   selfCheckPerWindow,
	})
}

func selfCheckTestConsumer(t *testing.T, scheduler *WakeScheduler, runnable ParentRunnable, deliver func(*Digest) error) *WakeConsumer {
	t.Helper()
	return &WakeConsumer{
		Wakes:    scheduler,
		Runnable: runnable,
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			require.Empty(t, wakeIDs, "a self-check turn must not claim wake ids")
			return deliver(digest)
		},
	}
}

// TestSelfCheck_DisabledByDefault verifies plan P1-6 方案 4 is opt-in: without
// a configured allowance the turn-end hook never starts an extra parent turn,
// so historical behavior is unchanged.
func TestSelfCheck_DisabledByDefault(t *testing.T) {
	store := newTestStore(t, "self-check-disabled")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})
	projectTestWake(t, store, scheduler, "child-1", WakeReasonExecutionFailed, SeverityCritical)

	delivered := 0
	consumer := selfCheckTestConsumer(t, scheduler, nil, func(*Digest) error {
		delivered++
		return nil
	})
	started, err := consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.False(t, started)
	require.Zero(t, delivered)
	require.False(t, scheduler.AllowSelfCheck(wakeBudgetTestRoot))
}

// TestSelfCheck_DeliversOneTurnPerWindowAfterRateLimit verifies the core
// 方案 4 contract: a deferred (rate-limited) wake gives the parent exactly one
// extra digest-only turn per window, the allowance prevents a self-check turn
// from recursing, and the durable wake is still kept for the next window.
func TestSelfCheck_DeliversOneTurnPerWindowAfterRateLimit(t *testing.T) {
	store := newTestStore(t, "self-check-window")
	ctx := context.Background()
	scheduler := selfCheckTestScheduler(store, 1)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }

	// The first failure spends the whole failure-class budget.
	projectTestWake(t, store, scheduler, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	claimed, digest, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NotNil(t, digest)

	// The second failure is deferred: this is the state the self-check exists
	// for (the parent is about to go idle with an undelivered digest).
	projectTestWake(t, store, scheduler, "child-2", WakeReasonExecutionFailed, SeverityCritical)
	_, _, err = drainTestWakes(scheduler)
	require.ErrorIs(t, err, ErrWakeRateLimited)

	delivered := make([]string, 0, 2)
	consumer := selfCheckTestConsumer(
		t,
		scheduler,
		func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		func(digest *Digest) error {
			require.NotEmpty(t, digest.Text)
			delivered = append(delivered, digest.Text)
			return nil
		},
	)

	started, err := consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.True(t, started)
	require.Len(t, delivered, 1)

	// Same window: the allowance is spent, so a self-check turn that ends
	// again cannot spawn another one.
	started, err = consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.False(t, started)
	require.Len(t, delivered, 1)

	// The budget only defers: the wake is still durable for the next window.
	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: wakeBudgetTestRoot, UnclaimedOnly: true})
	require.NoError(t, err)
	require.NotEmpty(t, pending)

	// The next window grants one more self-check.
	now = now.Add(2 * time.Hour)
	started, err = consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.True(t, started)
	require.Len(t, delivered, 2)
}

// TestSelfCheck_RequiresPendingWakeAndRunnableParent verifies the two cheap
// guards: nothing pending means no turn, and a busy parent keeps the wake for
// the next runnable point instead of queueing a concurrent turn.
func TestSelfCheck_RequiresPendingWakeAndRunnableParent(t *testing.T) {
	store := newTestStore(t, "self-check-guards")
	ctx := context.Background()
	scheduler := selfCheckTestScheduler(store, 1)

	delivered := 0
	consumer := selfCheckTestConsumer(
		t,
		scheduler,
		func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		func(*Digest) error {
			delivered++
			return nil
		},
	)

	started, err := consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.False(t, started, "no pending wake means nothing to self-check")
	require.Zero(t, delivered)

	projectTestWake(t, store, scheduler, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	busy := selfCheckTestConsumer(
		t,
		scheduler,
		func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return false },
		func(*Digest) error {
			delivered++
			return nil
		},
	)
	started, err = busy.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.False(t, started, "a busy parent must not queue a second turn")
	require.Zero(t, delivered)

	// The forced (still rate-limited) variant of the same guard: the parent is
	// runnable but the pending row is claimed, so there is nothing to deliver.
	claimed, _, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	started, err = consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.False(t, started, "delivered wakes must not be re-injected")
	require.Zero(t, delivered)
}

// TestSelfCheck_ResolvesStaleWakeWithoutSpendingAllowance verifies the cleanup
// path: when the notification was resolved while its wake stayed durable,
// there is nothing to inject, so the self-check drops the stale row instead of
// retrying an empty turn every window.
func TestSelfCheck_ResolvesStaleWakeWithoutSpendingAllowance(t *testing.T) {
	store := newTestStore(t, "self-check-stale")
	ctx := context.Background()
	scheduler := selfCheckTestScheduler(store, 1)

	notification, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           wakeBudgetTestRoot,
		TargetParentSessionID: wakeBudgetTestRoot,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             WakeReasonExecutionFailed,
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)
	ok, err := store.ResolveNotification(ctx, notification.NotificationID, ResolutionRecovered, time.Now().UTC(), notification.Version)
	require.NoError(t, err)
	require.True(t, ok)

	delivered := 0
	consumer := selfCheckTestConsumer(
		t,
		scheduler,
		func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		func(*Digest) error {
			delivered++
			return nil
		},
	)
	started, err := consumer.MaybeSelfCheckParent(ctx, wakeBudgetTestRoot, "", wakeBudgetTestRoot)
	require.NoError(t, err)
	require.False(t, started, "an empty digest must not start a content-free turn")
	require.Zero(t, delivered)

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: wakeBudgetTestRoot, UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "the stale wake is dropped instead of retried every window")
	require.True(t, scheduler.AllowSelfCheck(wakeBudgetTestRoot), "the stale path spent no allowance")
}
