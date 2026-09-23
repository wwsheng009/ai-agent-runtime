package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDeriveNotifyKeyIsStableAndIdentityScoped pins the notify_key contract of
// doc 6.6 / plan C2-4 #15: notify_key = hash(turn_id, obligation_id,
// event_kind, terminal_epoch|progress_seq). A retried delivery must reuse the
// same key, and two different events must never collide.
func TestDeriveNotifyKeyIsStableAndIdentityScoped(t *testing.T) {
	base := DeriveNotifyKey("turn-1", "batch-1", WakeEventTerminal, 7)
	require.NotEmpty(t, base)
	require.Equal(t, base, DeriveNotifyKey("turn-1", "batch-1", WakeEventTerminal, 7), "the key is a pure function of the identity")
	require.Equal(t, base, DeriveNotifyKey(" turn-1 ", " batch-1 ", WakeEventTerminal, 7), "identity fields are trimmed")

	for name, other := range map[string]string{
		"turn":           DeriveNotifyKey("turn-2", "batch-1", WakeEventTerminal, 7),
		"obligation":     DeriveNotifyKey("turn-1", "batch-2", WakeEventTerminal, 7),
		"event kind":     DeriveNotifyKey("turn-1", "batch-1", WakeEventProgress, 7),
		"terminal epoch": DeriveNotifyKey("turn-1", "batch-1", WakeEventTerminal, 8),
	} {
		require.NotEqual(t, base, other, "%s must change the key", name)
	}

	// A missing kind degrades to the lifecycle family instead of collapsing
	// all untyped events onto one key.
	require.Equal(t,
		DeriveNotifyKey("turn-1", "batch-1", WakeEventLifecycle, 0),
		DeriveNotifyKey("turn-1", "batch-1", "", 0))

	// Legacy wakes without any structured identity keep an empty key, which is
	// what preserves the coalescing-only behavior (zero-value compatibility).
	require.Empty(t, DeriveNotifyKey("", "", "", 3))
	require.Empty(t, DeriveNotifyKey("", "", WakeEventProgress, 0))
}

// TestWakeDeliveredLedgerRoundTripAndPrune pins the durable half of the
// idempotency rule (AC-P1-4a/e): the delivered key outlives the wake row, is
// idempotent on repeat writes, and is prunable for retention (C4-2).
func TestWakeDeliveredLedgerRoundTripAndPrune(t *testing.T) {
	store := newTestStore(t, "wake-delivered-ledger")
	ctx := context.Background()

	require.False(t, mustDelivered(t, store, "nk-1"), "an unknown key is not delivered")

	row := WakeDelivered{
		NotifyKey:             "nk-1",
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeID:                "wake-1",
		TurnID:                "turn-1",
		EventKind:             WakeEventTerminal,
		ObligationID:          "batch-1",
		EventSeq:              7,
		DeliveredBy:           "wake_consumer",
	}
	require.NoError(t, store.MarkWakeDelivered(ctx, row))
	require.True(t, mustDelivered(t, store, "nk-1"))

	// Re-recording is a no-op, not an error: the second delivery attempt must
	// stay silent (AC-P1-4b).
	require.NoError(t, store.MarkWakeDelivered(ctx, row))
	require.True(t, mustDelivered(t, store, "nk-1"))

	// An empty key is never recorded: legacy wakes keep coalescing-only dedup.
	require.NoError(t, store.MarkWakeDelivered(ctx, WakeDelivered{}))
	require.False(t, mustDelivered(t, store, ""))

	pruned, err := store.PruneWakeDelivered(ctx, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, pruned)
	require.False(t, mustDelivered(t, store, "nk-1"), "pruned keys stop suppressing")
}

// TestScheduleWakeSuppressesReplayedTerminalEvent covers AC-P1-4a end to end:
// the same terminal projection delivered once must never start a second
// resume, even though the wake row itself is gone after delivery.
func TestScheduleWakeSuppressesReplayedTerminalEvent(t *testing.T) {
	store := newTestStore(t, "wake-notify-key-replay")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	deliveries := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			deliveries++
			return nil
		},
	}
	projection := LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	}

	first, err := ProjectLifecycle(ctx, store, scheduler, projection)
	require.NoError(t, err)
	require.NotZero(t, first.EventSeq, "the store allocates the notification cursor used as terminal_epoch")
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, 1, deliveries)

	// The idempotent replay re-uses the same notification (and therefore the
	// same seq): re-projecting the same state must not schedule a new wake, and
	// an explicit re-schedule must report suppression instead of persisting.
	replayed, err := ProjectLifecycle(ctx, store, scheduler, projection)
	require.NoError(t, err)
	require.Equal(t, first.EventSeq, replayed.EventSeq, "an idempotent replay keeps the cursor")
	result, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "timeout",
		NotificationSeq:       first.EventSeq,
		ObligationID:          "child-1",
		EventKind:             lifecycleWakeEventKind(first.SupervisionState),
		EventSeq:              first.EventSeq,
	})
	require.NoError(t, err)
	require.True(t, result.Suppressed, "a replayed terminal event must be suppressed")
	require.NotEmpty(t, result.NotifyKey)
	require.Empty(t, result.WakeID, "a suppressed event persists no wake to release")
	require.Equal(t, WakeEventTerminal, lifecycleWakeEventKind(first.SupervisionState),
		"a timed_out projection belongs to the terminal family")

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "the replay must not create a pending wake")

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, 1, deliveries, "the replay must not start a second resume")
}

// TestWakeConsumerDropsDeliveredWakeRow covers the delivery-time half of
// AC-P1-4a: a wake row that survived a crash after the delivery was recorded
// is dropped (and released) instead of starting a duplicate resume.
func TestWakeConsumerDropsDeliveredWakeRow(t *testing.T) {
	store := newTestStore(t, "wake-notify-key-delivered-row")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	delivered := false
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			delivered = true
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

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NotEmpty(t, pending[0].NotifyKey, "the lifecycle projection must carry a notify key")

	// Simulate "delivered, then crashed before releasing the claim": the wake
	// row is still pending while the ledger already knows the key.
	require.NoError(t, scheduler.RecordWakeDelivery(ctx, pending))

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.False(t, delivered, "an already delivered key must not start a second resume")

	pending, err = store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "the stale row is released so the coalescing slot frees up")
}

// TestScheduleWakeRetryReusesNotifyKey covers AC-P1-4b: a failed delivery is
// never recorded, so the retry carries the same notify key, reaches the parent
// again, and only the successful delivery suppresses later replays.
func TestScheduleWakeRetryReusesNotifyKey(t *testing.T) {
	store := newTestStore(t, "wake-notify-key-retry")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	attempts := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			attempts++
			if attempts == 1 {
				// The first delivery fails: the idempotency key must stay
				// unclaimed so the retry is not swallowed.
				return errors.New("turn queue full")
			}
			return nil
		},
	}
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

	err = consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1")
	require.ErrorContains(t, err, "turn queue full")
	require.Equal(t, 1, attempts)

	// Retry: the same event is projected again (the inbox keeps the same seq),
	// so the wake must be schedulable and deliverable a second time.
	result, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "timeout",
		NotificationSeq:       1,
		ObligationID:          "child-1",
		EventKind:             WakeEventTerminal,
		EventSeq:              1,
	})
	require.NoError(t, err)
	require.False(t, result.Suppressed, "a failed delivery must not suppress the retry")
	require.NotEmpty(t, result.NotifyKey)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, 2, attempts, "the retry reuses the same notify key and reaches the parent")

	// Only now is the key recorded: a later replay is suppressed.
	delivered, err := store.IsWakeDelivered(ctx, result.NotifyKey)
	require.NoError(t, err)
	require.True(t, delivered, "a successful delivery records the key")
	replay, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "timeout",
		ObligationID:          "child-1",
		EventKind:             WakeEventTerminal,
		EventSeq:              1,
	})
	require.NoError(t, err)
	require.True(t, replay.Suppressed)
	require.Equal(t, 2, attempts)
}

// TestScheduleWakeWithoutIdentityKeepsCoalescingOnly pins the compatibility
// rule of plan §7: wakes that carry no structured identity are not touched by
// the idempotency ledger and keep collapsing into one pending row.
func TestScheduleWakeWithoutIdentityKeepsCoalescingOnly(t *testing.T) {
	store := newTestStore(t, "wake-notify-key-legacy")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	first, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "critical_lifecycle",
	})
	require.NoError(t, err)
	require.Empty(t, first.NotifyKey)
	require.False(t, first.Suppressed)

	second, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "critical_lifecycle",
	})
	require.NoError(t, err)
	require.False(t, second.Suppressed, "legacy wakes are never suppressed by the delivery ledger")
	require.True(t, second.Coalesced, "legacy wakes keep coalescing into one pending row")
	require.Empty(t, second.NotifyKey)

	// Recording a delivery for a keyless wake must be a no-op.
	require.NoError(t, scheduler.RecordWakeDelivery(ctx, []WakePending{{WakeID: first.WakeID, RootScopeID: "root-session-1"}}))
	require.False(t, mustDelivered(t, store, first.WakeID))
}

// TestFilterDeliveredWakesKeepsKeylessAndUnreadableWakes pins the guard rails
// of the delivery-time filter: a wake must never be lost because the ledger is
// unreadable, and keyless wakes are always kept.
func TestFilterDeliveredWakesKeepsKeylessAndUnreadableWakes(t *testing.T) {
	store := newTestStore(t, "wake-notify-key-filter")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	require.NoError(t, scheduler.RecordWakeDelivery(ctx, []WakePending{{NotifyKey: "nk-delivered", RootScopeID: "root-session-1"}}))

	kept := scheduler.FilterDeliveredWakes(ctx, []WakePending{
		{NotifyKey: "nk-delivered", RootScopeID: "root-session-1"},
		{NotifyKey: "nk-fresh", RootScopeID: "root-session-1"},
		{RootScopeID: "root-session-1"},
	})
	require.Len(t, kept, 2)
	require.Equal(t, "nk-fresh", kept[0].NotifyKey)
	require.Empty(t, kept[1].NotifyKey)

	var nilScheduler *WakeScheduler
	require.Len(t, nilScheduler.FilterDeliveredWakes(ctx, []WakePending{{NotifyKey: "nk-delivered"}}), 1,
		"an unwired scheduler must not drop wakes")
}

func mustDelivered(t *testing.T, store Store, key string) bool {
	t.Helper()
	delivered, err := store.IsWakeDelivered(context.Background(), key)
	require.NoError(t, err)
	return delivered
}
