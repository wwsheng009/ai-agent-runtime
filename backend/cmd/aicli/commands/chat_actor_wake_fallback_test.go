package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// projectScanWake 复刻扫描侧的真实产物：critical + action_required 的生命周期
// 通知会排一条 durable wake（ProjectLifecycle 内部按 LifecycleWakeScheduled
// 判定），父会话随后需要被 drain 唤醒。
func projectScanWake(t *testing.T, host *localChatRuntimeHost) {
	t.Helper()
	_, err := supervision.ProjectLifecycle(context.Background(), host.Supervision.Store, host.Supervision.Wakes, supervision.LifecycleProjection{
		RootScopeID:           localProgressCheckTestSession,
		TargetParentSessionID: localProgressCheckTestSession,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "run-stalled-1",
		EventType:             "progress_stalled",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionStalled,
		Reason:                "progress stalled past the escalation threshold",
	})
	require.NoError(t, err)
}

// TestLocalWakeFallbackDeliversIdleParentWake is the 2026-10-04 deadlock
// regression: an idle parent whose only pending wake came from a standalone
// scan must be woken by the default-on fallback sweep.
func TestLocalWakeFallbackDeliversIdleParentWake(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()
	projectScanWake(t, host)

	attempted, err := host.runLocalSupervisionWakeFallbackOnce(ctx)
	require.NoError(t, err)
	require.True(t, attempted, "a pending scan wake must produce a drain attempt")
	require.Equal(t, 1, *deliveries, "the idle parent must be woken by the fallback sweep")
	requireNoPendingWake(t, host, "the delivered wake must be claimed and released")
}

// TestLocalWakeFallbackKeepsWakeWhenParentBusy pins the durable semantics: a
// busy parent keeps the wake pending for the next sweep instead of losing it.
func TestLocalWakeFallbackKeepsWakeWhenParentBusy(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	host.supervisionWake.Runnable = func(context.Context, string, string, string) bool { return false }
	ctx := context.Background()
	projectScanWake(t, host)

	attempted, err := host.runLocalSupervisionWakeFallbackOnce(ctx)
	require.NoError(t, err)
	require.False(t, attempted)
	require.Zero(t, *deliveries)

	pending, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		TargetParentSessionID: localProgressCheckTestSession,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, pending, "a busy parent keeps the wake durable for the next sweep")
}

// TestLocalWakeFallbackNoopWithoutPendingWakes keeps the no-pending path
// allocation-free: one bounded probe, no consumer call, no wake writes.
func TestLocalWakeFallbackNoopWithoutPendingWakes(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)

	attempted, err := host.runLocalSupervisionWakeFallbackOnce(context.Background())
	require.NoError(t, err)
	require.False(t, attempted)
	require.Zero(t, *deliveries)
}

// TestLocalWakeFallbackIntervalResolution pins the config contract: unset
// means default-on, negative disables, positive clamps to the floor.
func TestLocalWakeFallbackIntervalResolution(t *testing.T) {
	require.Equal(t, supervision.DefaultWakeFallbackInterval, localWakeFallbackInterval(supervision.Config{}))
	require.Zero(t, localWakeFallbackInterval(supervision.Config{WakeFallbackInterval: -time.Second}))
	require.Equal(t, 5*time.Minute, localWakeFallbackInterval(supervision.Config{WakeFallbackInterval: 5 * time.Minute}))
	require.Equal(t, supervision.MinWakeFallbackInterval, localWakeFallbackInterval(supervision.Config{WakeFallbackInterval: time.Second}))
}

// TestLocalWakeFallbackLifecycleGated proves the sweep is lifecycle-bound (no
// stray goroutine after Close) and that a negative interval opts out.
func TestLocalWakeFallbackLifecycleGated(t *testing.T) {
	t.Run("default starts a lifecycle-bound sweep", func(t *testing.T) {
		host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		host.lifecycleCtx = ctx

		host.startLocalSupervisionWakeFallback()
		require.NotNil(t, host.wakeFallbackStop, "the default config keeps the fallback on")

		host.stopLocalSupervisionWakeFallback()
		done := make(chan struct{})
		go func() {
			host.asyncWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the wake fallback goroutine did not exit after stop")
		}
	})

	t.Run("negative interval disables the sweep", func(t *testing.T) {
		host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
		host.supervisionConfig = supervision.Config{WakeFallbackInterval: -time.Second}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		host.lifecycleCtx = ctx

		host.startLocalSupervisionWakeFallback()
		require.Nil(t, host.wakeFallbackStop, "a negative interval must not register a loop")
	})
}

// parkWaitFeedbackFixture seeds one parked turn with a running team obligation
// (the shape spawn_team produces) so the fallback feedback gates can be
// exercised in isolation.
func parkWaitFeedbackFixture(t *testing.T, host *localChatRuntimeHost) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := host.TeamStore.CreateTeam(ctx, team.Team{
		ID:        "team-wait",
		Status:    team.TeamStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-wait",
		SessionID:     localProgressCheckTestSession,
		RootScopeID:   localProgressCheckTestSession,
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-wait")},
		ParkedAt:      now.Add(-time.Hour),
	}))
}

func pendingWaitFeedbackWakes(t *testing.T, host *localChatRuntimeHost) []supervision.WakePending {
	t.Helper()
	pending, err := host.Supervision.Store.ListWakePending(context.Background(), supervision.WakeFilter{
		TargetParentSessionID: localProgressCheckTestSession,
		UnclaimedOnly:         true,
		Limit:                 16,
	})
	require.NoError(t, err)
	return pending
}

// TestMaybeScheduleWaitFeedbackSchedulesOnceWhilePending pins the sweep edge: a
// parked turn with pending obligations gets one bounded feedback wake, and a
// no-change plateau does not repeat it (no heartbeat spam).
func TestMaybeScheduleWaitFeedbackSchedulesOnceWhilePending(t *testing.T) {
	host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()
	parkWaitFeedbackFixture(t, host)

	host.maybeScheduleWaitFeedback(ctx, localProgressCheckTestSession)
	pending := pendingWaitFeedbackWakes(t, host)
	require.Len(t, pending, 1)
	require.Equal(t, supervision.WakeReasonWaitFeedback, pending[0].WakeReason)
	require.Equal(t, "turn-wait", pending[0].TurnID)

	host.maybeScheduleWaitFeedback(ctx, localProgressCheckTestSession)
	require.Len(t, pendingWaitFeedbackWakes(t, host), 1,
		"no-change plateaus must not send repeated heartbeats")
}

// TestMaybeScheduleWaitFeedbackSilentWhenBusyOrUserFirst pins the two
// anti-race gates: an active run (wait_agent lives inside the run) and a
// queued user input both keep the fallback fully silent.
func TestMaybeScheduleWaitFeedbackSilentWhenBusyOrUserFirst(t *testing.T) {
	ctx := context.Background()

	t.Run("active run stays silent", func(t *testing.T) {
		host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
		parkWaitFeedbackFixture(t, host)
		require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
			SessionID: localProgressCheckTestSession,
			Status:    runtimechat.SessionRunning,
		}))
		host.maybeScheduleWaitFeedback(ctx, localProgressCheckTestSession)
		require.Empty(t, pendingWaitFeedbackWakes(t, host),
			"an active run (or an in-flight wait_agent window) must keep the fallback silent")
	})

	t.Run("queued user input wins", func(t *testing.T) {
		host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
		parkWaitFeedbackFixture(t, host)
		host.BaseSession.setQueuedInputDrainActive(true)
		host.maybeScheduleWaitFeedback(ctx, localProgressCheckTestSession)
		require.Empty(t, pendingWaitFeedbackWakes(t, host),
			"a queued user input owns the next episode; the fallback must not race it")
	})
}
