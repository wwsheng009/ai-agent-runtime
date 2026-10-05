package supervision

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestWaitFeedbackTrackerCadence pins the confirmed fallback rhythm: a progress
// delta is due after the small floor; with no progress at most ONE escalation
// is sent after the silence window; after that the tracker stays quiet until
// the ledger actually moves again (no repeated no-change heartbeats).
func TestWaitFeedbackTrackerCadence(t *testing.T) {
	tracker := NewWaitFeedbackTracker()
	key := "parent-1|turn-1"
	parked := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	// Freshly parked, nothing finished: silent until the silence escalation.
	require.False(t, tracker.Due(key, 0, parked, parked.Add(time.Second)))
	require.False(t, tracker.Due(key, 0, parked, parked.Add(WaitFeedbackSilence-time.Second)))
	require.True(t, tracker.Due(key, 0, parked, parked.Add(WaitFeedbackSilence)))

	// First observed delta: due once the floor passed (start anchored at park).
	require.False(t, tracker.Due(key, 1, parked, parked.Add(WaitFeedbackDeltaFloor-time.Second)))
	require.True(t, tracker.Due(key, 1, parked, parked.Add(WaitFeedbackDeltaFloor)))
	tracker.Mark(key, 1, parked.Add(WaitFeedbackDeltaFloor))

	// No further progress: silent until the single escalation.
	require.False(t, tracker.Due(key, 1, parked, parked.Add(WaitFeedbackDeltaFloor+time.Minute)))
	require.True(t, tracker.Due(key, 1, parked, parked.Add(WaitFeedbackDeltaFloor+WaitFeedbackSilence)))
	tracker.Mark(key, 1, parked.Add(WaitFeedbackDeltaFloor+WaitFeedbackSilence))

	// Escalated once: no more no-change wakes, even much later.
	require.False(t, tracker.Due(key, 1, parked, parked.Add(time.Hour)))

	// A new delta re-arms the feedback edge.
	require.True(t, tracker.Due(key, 2, parked, parked.Add(time.Hour)))
	tracker.Mark(key, 2, parked.Add(time.Hour))
	require.False(t, tracker.Due(key, 2, parked, parked.Add(time.Hour+time.Minute)))
	require.True(t, tracker.Due(key, 2, parked, parked.Add(time.Hour+WaitFeedbackSilence)))

	// Reset clears the cadence (turn settled / interrupted / new turn).
	tracker.Reset(key)
	require.False(t, tracker.Due(key, 0, parked, parked.Add(time.Second)))
	require.True(t, tracker.Due(key, 0, parked, parked.Add(WaitFeedbackSilence)))
}

// TestScheduleWaitFeedbackWakeOnlyWhilePending pins the sweep edge: one wake is
// scheduled for a parked turn with pending obligations (subject to the tracker),
// while all-terminal records are left to the settlement edge, and an escalated
// plateau does not repeat.
func TestScheduleWaitFeedbackWakeOnlyWhilePending(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "wait-feedback-wake")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	batches, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batches.Close() })

	parked := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-wait",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-running")},
		ParkedAt:      parked,
	}))

	teams := stubTeamObligationResolver{"team-running": false, "team-done": true}
	tracker := NewWaitFeedbackTracker()

	scheduled, err := ScheduleWaitFeedbackWake(ctx, batches, nil, teams, scheduler, tracker, "parent-1", "parent-1")
	require.NoError(t, err)
	require.True(t, scheduled, "a long-parked pending turn must receive one feedback wake")

	wakes, err := store.ListWakePending(ctx, WakeFilter{
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
		Limit:                 8,
	})
	require.NoError(t, err)
	require.Len(t, wakes, 1)
	require.Equal(t, WakeReasonWaitFeedback, wakes[0].WakeReason)
	require.Equal(t, "turn-wait", wakes[0].TurnID)

	// Same plateau, escalation already spent: no second no-change wake.
	scheduled, err = ScheduleWaitFeedbackWake(ctx, batches, nil, teams, scheduler, tracker, "parent-1", "parent-1")
	require.NoError(t, err)
	require.False(t, scheduled, "no-change plateaus must not send repeated heartbeats")

	// All-terminal records belong to the settlement edge, not the fallback.
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-wait",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-done")},
		ParkedAt:      parked,
	}))
	scheduled, err = ScheduleWaitFeedbackWake(ctx, batches, nil, teams, scheduler, tracker, "parent-1", "parent-1")
	require.NoError(t, err)
	require.False(t, scheduled, "the terminal edge owns that wake; the fallback must not duplicate it")
}

// TestWakeConsumer_WaitFeedbackWakeDeliversRollup pins the parked-parent twin of
// the progress check: a wait_feedback wake has no lifecycle notification, so its
// digest content is the rollup itself; it must start the observation turn
// instead of being released silently (which would make the fallback mute).
func TestWakeConsumer_WaitFeedbackWakeDeliversRollup(t *testing.T) {
	store, scheduler, consumer, probe := newProgressWakeHarness(t, "wake-consumer-wait-feedback", 1)
	ctx := context.Background()

	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            WakeReasonWaitFeedback,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, 1, probe.deliveries,
		"a wait-feedback digest carries the rollup and must start the observation turn")
	require.NotNil(t, probe.lastDigest)
	require.NotEmpty(t, probe.lastDigest.Progress, "the wake digest must carry the rollup")

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "the delivered feedback wake is consumed, not left dangling")

	other := scheduler.BudgetState(ctx, "root-session-1", WakeBudgetClassOther)
	require.Equal(t, 1, other.Used, "the observation turn spends the bounded other-class claim")
}

// TestWakeScheduler_WaitFeedbackWithoutRollupStaysSilent keeps the no-content
// guarantee: without a rollup projection a wait-feedback wake must stay silent
// and budget-neutral, exactly like the progress check does.
func TestWakeScheduler_WaitFeedbackWithoutRollupStaysSilent(t *testing.T) {
	store := newTestStore(t, "wake-scheduler-wait-feedback-unwired")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})

	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            WakeReasonWaitFeedback,
	})
	require.NoError(t, err)

	claimed, digest, err := scheduler.DrainRunnable(ctx, "root-session-1", "", "root-session-1",
		func(context.Context, string, string, string) bool { return true })
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.False(t, digestDeliverable(claimed, digest),
		"without the rollup a wait-feedback wake has no content and must stay silent")
	require.True(t, scheduler.AllowAutoWake(ctx, "root-session-1", WakeBudgetClassOther, time.Now().UTC()),
		"a wake that never became a turn must not spend the window")
}
