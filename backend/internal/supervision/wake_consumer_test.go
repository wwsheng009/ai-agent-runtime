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

type failWakeLedgerStore struct {
	Store
	mu         sync.Mutex
	failAtCall int
	calls      int
	attempts   map[string]int
	err        error
}

func (s *failWakeLedgerStore) MarkWakeDelivered(ctx context.Context, delivered WakeDelivered) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.attempts == nil {
		s.attempts = make(map[string]int)
	}
	s.attempts[delivered.NotifyKey]++
	if s.failAtCall > 0 && s.calls == s.failAtCall {
		return s.err
	}
	return s.Store.MarkWakeDelivered(ctx, delivered)
}

// CommitWakeConsumptionOwned is the atomic ledger+resolve path used by live
// wake delivery. Count each ledger item before delegating the whole batch so a
// fault injected in the middle of a batch verifies that the underlying
// transaction rolls back every item.
func (s *failWakeLedgerStore) CommitWakeConsumptionOwned(
	ctx context.Context,
	wakes []WakePending,
	claimedBy string,
	validAfter, consumedAt time.Time,
	consumption, reason string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, wake := range wakes {
		if wake.NotifyKey == "" {
			continue
		}
		s.calls++
		s.attempts[wake.NotifyKey]++
		if s.failAtCall > 0 && s.calls == s.failAtCall {
			return s.err
		}
	}
	return s.Store.CommitWakeConsumptionOwned(ctx, wakes, claimedBy, validAfter, consumedAt, consumption, reason)
}

func (s *failWakeLedgerStore) attemptsFor(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts[key]
}

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
	consumer.Deliver = func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
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
	consumer.Deliver = func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
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

func TestWakeConsumer_AcceptedDeliverySchedulesMarkerOnlyRetry(t *testing.T) {
	store := newTestStore(t, "wake-consumer-accepted-marker")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	const (
		parent = "parent-accepted-marker"
		turnID = "turn-accepted-marker"
		root   = "root-accepted-marker"
	)
	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "accepted-marker-child",
		EventType:             "child.failed",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTerminated,
		TurnID:                turnID,
	})
	require.NoError(t, err)

	deliveries := 0
	consumer := &WakeConsumer{
		Wakes: scheduler,
		Runnable: func(context.Context, string, string, string) bool {
			return true
		},
		Deliver: func(context.Context, string, string, *Digest, []string) error {
			deliveries++
			return NewAcceptedDeliveryError(errors.New("marker failed"), true)
		},
		OnAcceptedDeliveryError: func(ctx context.Context, parentSessionID, rootScopeID string, _ *Digest, wakes []WakePending, _ *ResumeContext, _ error) error {
			require.Len(t, wakes, 1)
			_, err := scheduler.ScheduleWake(ctx, WakeRequest{
				RootScopeID:           rootScopeID,
				TargetParentSessionID: parentSessionID,
				TurnID:                turnID,
				ParentTurnID:          turnID,
				WakeReason:            ResultsConsumptionRetryWakeReason(turnID),
			})
			return err
		},
	}

	err = consumer.MaybeWakeParent(ctx, parent, "", root)
	require.ErrorContains(t, err, "marker failed")
	require.Equal(t, 1, deliveries)

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.True(t, IsResultsConsumptionRetryWake(pending[0].WakeReason))
	require.Equal(t, turnID, pending[0].ParentTurnID)
}

func TestWakeConsumer_MarkerOnlyRepairBypassesRunnableAndModelDelivery(t *testing.T) {
	store := newTestStore(t, "wake-consumer-marker-only")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	const (
		parent = "parent-marker-only"
		turnID = "turn-marker-only"
		root   = "root-marker-only"
	)
	retryWake, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		ParentTurnID:          turnID,
		TurnID:                turnID,
		WakeReason:            ResultsConsumptionRetryWakeReason(turnID),
	})
	require.NoError(t, err)

	modelDeliveries := 0
	repairs := 0
	runnableChecks := 0
	repairErr := errors.New("marker storage temporarily unavailable")
	consumer := &WakeConsumer{
		Wakes: scheduler,
		Runnable: func(context.Context, string, string, string) bool {
			runnableChecks++
			return false
		},
		Deliver: func(context.Context, string, string, *Digest, []string) error {
			modelDeliveries++
			return nil
		},
		DeliverResultsConsumptionRetry: func(context.Context, string, string, string) error {
			repairs++
			return repairErr
		},
	}

	err = consumer.MaybeWakeParent(ctx, parent, "", root)
	require.ErrorIs(t, err, repairErr)
	require.Equal(t, 0, runnableChecks, "marker repair must not wait for the parent actor to become runnable")
	require.Equal(t, 0, modelDeliveries, "marker repair must never submit a model prompt")
	require.Equal(t, 1, repairs)
	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed marker repair must keep its durable retry wake")
	require.Equal(t, retryWake.WakeID, pending[0].WakeID)

	consumer.DeliverResultsConsumptionRetry = func(context.Context, string, string, string) error {
		repairs++
		return nil
	}
	require.NoError(t, consumer.MaybeWakeParent(ctx, parent, "", root))
	require.Equal(t, 2, repairs)
	require.Equal(t, 0, modelDeliveries)
	pending, err = store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending)
	require.True(t, mustDelivered(t, store, retryWake.NotifyKey), "successful control-plane repair must be durably recorded")
	db, err := store.dbOrErr()
	require.NoError(t, err)
	var consumption, reason string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT consumption, reason FROM supervision_wake_delivered WHERE notify_key = ?`,
		retryWake.NotifyKey,
	).Scan(&consumption, &reason))
	require.Equal(t, "results_consumption_repair", consumption)
	require.Equal(t, "marker_repaired", reason)
}

func TestWakeConsumer_AcceptedFenceRetriesMarkerRepairWithoutPrompt(t *testing.T) {
	store := newTestStore(t, "wake-consumer-accepted-fence-retry")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	const (
		parent = "parent-accepted-fence"
		turnID = "turn-accepted-fence"
		root   = "root-accepted-fence"
	)
	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:                "accepted-fence-wake",
		RootScopeID:           root,
		TargetParentSessionID: parent,
		ParentTurnID:          turnID,
		WakeReason:            "child.failed",
		NotifyKey:             "accepted-fence-notify",
		DeliveryState:         WakeDeliveryStateAccepted,
		MarkerRepairRequired:  true,
		DedupKey:              "accepted-fence-dedup",
	}))

	modelDeliveries := 0
	markerRepairs := 0
	repairErr := errors.New("marker write failed")
	consumer := &WakeConsumer{
		Wakes: scheduler,
		Runnable: func(context.Context, string, string, string) bool {
			return false
		},
		Deliver: func(context.Context, string, string, *Digest, []string) error {
			modelDeliveries++
			return nil
		},
		DeliverResultsConsumptionRetry: func(context.Context, string, string, string) error {
			markerRepairs++
			if markerRepairs == 1 {
				return repairErr
			}
			return nil
		},
	}

	require.ErrorIs(t, consumer.MaybeWakeParent(ctx, parent, "", root), repairErr)
	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "failed marker repair must retain its accepted fence")
	require.Equal(t, WakeDeliveryStateAccepted, pending[0].DeliveryState)
	require.True(t, pending[0].MarkerRepairRequired)

	require.NoError(t, consumer.MaybeWakeParent(ctx, parent, "", root))
	require.Equal(t, 0, modelDeliveries, "accepted prompt must never be resubmitted")
	require.Equal(t, 2, markerRepairs, "the marker-only operation must be retryable")
	pending, err = store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending)
	require.True(t, mustDelivered(t, store, "accepted-fence-notify"))
}

func TestWakeConsumer_SubmittingFenceAfterRestartIsNotReplayedOrRepaired(t *testing.T) {
	store := newTestStore(t, "wake-consumer-submitting-restart")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	const (
		parent = "parent-submitting-restart"
		root   = "root-submitting-restart"
		wakeID = "submitting-restart-wake"
	)
	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:                wakeID,
		RootScopeID:           root,
		TargetParentSessionID: parent,
		ParentTurnID:          "turn-submitting-restart",
		WakeReason:            "child.failed",
		DeliveryState:         WakeDeliveryStateSubmitting,
		MarkerRepairRequired:  true,
		DedupKey:              "submitting-restart-dedup",
	}))

	modelDeliveries := 0
	markerRepairs := 0
	consumer := &WakeConsumer{
		Wakes: scheduler,
		Runnable: func(context.Context, string, string, string) bool {
			return true
		},
		Deliver: func(context.Context, string, string, *Digest, []string) error {
			modelDeliveries++
			return nil
		},
		DeliverResultsConsumptionRetry: func(context.Context, string, string, string) error {
			markerRepairs++
			return nil
		},
	}

	require.NoError(t, consumer.MaybeWakeParent(ctx, parent, "", root))
	require.Equal(t, 0, modelDeliveries)
	require.Equal(t, 0, markerRepairs, "ambiguous submitting state has no acceptance verdict")
	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           root,
		TargetParentSessionID: parent,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, wakeID, pending[0].WakeID)
	require.Equal(t, WakeDeliveryStateSubmitting, pending[0].DeliveryState)
	require.True(t, pending[0].MarkerRepairRequired)
}

func TestWakeConsumer_AtomicDeliveryLedgerFailureRetriesWholeBatch(t *testing.T) {
	ctx := context.Background()
	baseStore := newTestStore(t, "wake-consumer-partial-ledger")
	ledgerErr := errors.New("injected ledger write failure")
	store := &failWakeLedgerStore{
		Store:      baseStore,
		failAtCall: 2,
		attempts:   make(map[string]int),
		err:        ledgerErr,
	}
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	for index, subjectID := range []string{"partial-ledger-child-1", "partial-ledger-child-2"} {
		_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
			RootScopeID:           "root-partial-ledger",
			TargetParentSessionID: "parent-partial-ledger",
			SubjectKind:           SubjectAgentRun,
			SubjectID:             subjectID,
			EventType:             []string{"child.failed", "child.exception"}[index],
			Severity:              SeverityCritical,
			SupervisionState:      SupervisionTerminated,
		})
		require.NoError(t, err)
	}
	pending, err := baseStore.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-partial-ledger",
		TargetParentSessionID: "parent-partial-ledger",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 2)
	keys := []string{pending[0].NotifyKey, pending[1].NotifyKey}

	modelDeliveries := 0
	consumer := &WakeConsumer{
		Wakes: scheduler,
		Runnable: func(context.Context, string, string, string) bool {
			return true
		},
		Deliver: func(context.Context, string, string, *Digest, []string) error {
			modelDeliveries++
			return nil
		},
	}
	err = consumer.MaybeWakeParent(ctx, "parent-partial-ledger", "", "root-partial-ledger")
	require.ErrorIs(t, err, ledgerErr)
	require.Equal(t, 1, modelDeliveries)
	pending, err = baseStore.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-partial-ledger",
		TargetParentSessionID: "parent-partial-ledger",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 2, "ledger failure must not resolve any accepted wake")
	for _, item := range pending {
		require.Equal(t, WakeDeliveryStateAccepted, item.DeliveryState)
	}
	for _, key := range keys {
		require.False(t, mustDelivered(t, baseStore, key), "an atomic ledger failure must roll back every key")
	}

	// The injected failure was one-shot. The accepted-fence recovery drain
	// must backfill the whole transaction without resubmitting the parent
	// prompt.
	require.NoError(t, consumer.MaybeWakeParent(ctx, "parent-partial-ledger", "", "root-partial-ledger"))
	require.Equal(t, 1, modelDeliveries)
	for _, key := range keys {
		require.True(t, mustDelivered(t, baseStore, key), "all notification keys must eventually be durable")
	}
	require.Equal(t, 2, store.attemptsFor(keys[0]), "the atomic batch must retry the first key after rollback")
	require.Equal(t, 2, store.attemptsFor(keys[1]), "the atomic batch must retry the second key after rollback")
	pending, err = baseStore.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-partial-ledger",
		TargetParentSessionID: "parent-partial-ledger",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestWakeConsumer_AcknowledgedPendingWakeDoesNotStartParentTurn(t *testing.T) {
	store := newTestStore(t, "wake-consumer-acknowledged")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	delivered := false
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *Digest, []string) error {
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
	var firstWakeIDs []string
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			firstWakeIDs = append([]string(nil), wakeIDs...)
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
	require.Len(t, firstWakeIDs, 1)

	// The failed submission releases the claim but does not resolve the wake.
	// The next runnable point can retry this exact durable row without needing a
	// fresh lifecycle event.
	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, firstWakeIDs[0], pending[0].WakeID)

	var retryWakeIDs []string
	consumer.Deliver = func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
		retryWakeIDs = append([]string(nil), wakeIDs...)
		return nil
	}
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, firstWakeIDs, retryWakeIDs, "retry must deliver the same wake id")
	pending, err = store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "a successful retry resolves the original wake")
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
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
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
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
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
