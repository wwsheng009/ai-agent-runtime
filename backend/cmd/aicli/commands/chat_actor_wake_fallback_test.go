package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
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
