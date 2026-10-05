package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestAPIWaitFeedbackSweepIntervalResolution pins the host-side interpretation
// of wake_fallback_interval for the decoupled feedback sweep: unset means the
// default-on 60s cadence, negative explicitly disables, and a positive value is
// clamped to the 15s floor (byte-for-byte the CLI localWakeFallbackInterval
// semantics).
func TestAPIWaitFeedbackSweepIntervalResolution(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset defaults on", 0, supervision.DefaultWakeFallbackInterval},
		{"negative disables", -time.Second, 0},
		{"sub-floor clamps", 5 * time.Second, supervision.MinWakeFallbackInterval},
		{"floor preserved", supervision.MinWakeFallbackInterval, supervision.MinWakeFallbackInterval},
		{"explicit interval passes through", 3 * time.Minute, 3 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, apiWaitFeedbackSweepInterval(supervision.Config{WakeFallbackInterval: tc.in}))
		})
	}
}

// TestAPIWaitFeedbackSweepLoopLifecycle pins the decoupling: configuring the
// host starts the feedback loop by default while the progress check stays
// opt-in, and a negative wake_fallback_interval converges the loop again.
func TestAPIWaitFeedbackSweepLoopLifecycle(t *testing.T) {
	handler, _ := newAPIProgressCheckFixture(t, "api-wait-feedback-loop", subagentbatch.BatchRunning)
	t.Cleanup(handler.StopSupervisionWaitFeedbackSweep)
	t.Cleanup(handler.StopSupervisionProgressCheck)

	handler.SetSupervisionConfig(supervision.Config{})
	require.Nil(t, handler.supervisionProgressCheckStop, "progress check remains opt-in")
	require.NotNil(t, handler.supervisionWaitFeedbackStop, "wait feedback sweep must be on by default")

	handler.SetSupervisionConfig(supervision.Config{WakeFallbackInterval: -time.Second})
	require.Nil(t, handler.supervisionWaitFeedbackStop, "a negative interval must converge the loop")

	handler.SetSupervisionConfig(supervision.Config{WakeFallbackInterval: 5 * time.Second})
	require.NotNil(t, handler.supervisionWaitFeedbackStop, "a sub-floor interval still runs at the clamped cadence")
	handler.StopSupervisionWaitFeedbackSweep() // idempotent when already stopped
}

// parkAPIWaitFeedbackSuspension seeds one parked turn on the fixture parent with
// a team obligation (the shape spawn_team produces), parked long enough that the
// cadence tracker is due for its first feedback pass.
func parkAPIWaitFeedbackSuspension(t *testing.T, handler *Handler, turnID string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, handler.getSubagentBatchStore().ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        turnID,
		SessionID:     apiProgressCheckParentSession,
		RootScopeID:   apiProgressCheckParentSession,
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-api-wait")},
		ParkedAt:      now.Add(-time.Hour),
	}))
}

// TestAPIWaitFeedbackSweepSchedulesForParkedTurn is the decoupling payload: a
// parked turn with pending obligations gets exactly one feedback wake, drained
// through the normal wake path, without any progress_check_interval configured.
// A no-change plateau must not repeat the heartbeat.
func TestAPIWaitFeedbackSweepSchedulesForParkedTurn(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-wait-feedback-schedule", subagentbatch.BatchRunning)
	ctx := context.Background()
	parkAPIWaitFeedbackSuspension(t, handler, "turn-api-wait")

	scheduled, err := handler.runSupervisionWaitFeedbackSweepOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, scheduled, "a parked turn with pending obligations must produce one feedback wake")
	require.Equal(t, 1, *deliveries, "the feedback wake must be drained through the normal wake path")
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "the delivered feedback wake is claimed")

	scheduled, err = handler.runSupervisionWaitFeedbackSweepOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, scheduled, "a no-change plateau must not send repeated heartbeats")
}

// TestAPIWaitFeedbackSweepSkipsBusyParent keeps the busy gate: an executing
// parent owns its turn, so the fallback stays silent and schedules nothing.
func TestAPIWaitFeedbackSweepSkipsBusyParent(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-wait-feedback-busy", subagentbatch.BatchRunning)
	ctx := context.Background()
	require.NoError(t, handler.sessionRuntimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: apiProgressCheckParentSession,
		Status:    chat.SessionRunning,
	}))
	parkAPIWaitFeedbackSuspension(t, handler, "turn-api-wait-busy")

	scheduled, err := handler.runSupervisionWaitFeedbackSweepOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, scheduled, "an executing parent must keep the fallback silent")
	require.Zero(t, *deliveries)
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "a skipped sweep leaves no stale wake behind")
}

// TestAPIWaitFeedbackSweepDefersToPendingWake mirrors the pending-wake gate: a
// lifecycle wake already in flight owns the next episode, so feedback must not
// race a duplicate.
func TestAPIWaitFeedbackSweepDefersToPendingWake(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-wait-feedback-pending", subagentbatch.BatchRunning)
	ctx := context.Background()
	parkAPIWaitFeedbackSuspension(t, handler, "turn-api-wait-pending")
	_, err := handler.getSupervisionWakeScheduler().ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           apiProgressCheckParentSession,
		TargetParentSessionID: apiProgressCheckParentSession,
		WakeReason:            supervision.WakeReasonLifecycleFailed,
	})
	require.NoError(t, err)

	scheduled, err := handler.runSupervisionWaitFeedbackSweepOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, scheduled, "a pending lifecycle wake wins over the fallback")
	require.Zero(t, *deliveries)
}

// TestAPIWaitFeedbackSweepFailQuietUnwired pins the degradation paths: an
// unwired control plane (missing scheduler / supervision store / batch store)
// must skip the sweep silently instead of erroring or panicking.
func TestAPIWaitFeedbackSweepFailQuietUnwired(t *testing.T) {
	ctx := context.Background()

	t.Run("no wake scheduler", func(t *testing.T) {
		handler, _ := newAPIProgressCheckFixture(t, "api-wait-feedback-noscheduler", subagentbatch.BatchRunning)
		parkAPIWaitFeedbackSuspension(t, handler, "turn-api-wait-noscheduler")
		handler.SetSupervisionWakeScheduler(nil)
		scheduled, err := handler.runSupervisionWaitFeedbackSweepOnce(ctx)
		require.NoError(t, err)
		require.Zero(t, scheduled)
	})

	t.Run("no supervision store", func(t *testing.T) {
		handler, _ := newAPIProgressCheckFixture(t, "api-wait-feedback-nostore", subagentbatch.BatchRunning)
		parkAPIWaitFeedbackSuspension(t, handler, "turn-api-wait-nostore")
		handler.SetSupervisionStore(nil)
		scheduled, err := handler.runSupervisionWaitFeedbackSweepOnce(ctx)
		require.NoError(t, err)
		require.Zero(t, scheduled)
	})

	t.Run("no batch store", func(t *testing.T) {
		handler, _ := newAPIProgressCheckFixture(t, "api-wait-feedback-nobatch", subagentbatch.BatchRunning)
		parkAPIWaitFeedbackSuspension(t, handler, "turn-api-wait-nobatch")
		handler.SetSubagentBatchStore(nil)
		scheduled, err := handler.runSupervisionWaitFeedbackSweepOnce(ctx)
		require.NoError(t, err)
		require.Zero(t, scheduled)
	})
}
