package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestExecutionSupervisor_StatsSnapshotTracksScanAndDispatch covers plan §P0-4
// 可见性: the operator snapshot must expose the effective config, cumulative
// scan/decision counters, the last scan's decisions and the completion-outbox
// flush — all without touching the store from the render path.
func TestExecutionSupervisor_StatsSnapshotTracksScanAndDispatch(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	dispatcher := &fakeDispatcher{failFirst: map[string]bool{}}
	supervisor, _ := newTestExecutionSupervisor(t, "sup-stats", ExecutionSupervisorConfig{
		Enabled: true,
		Mode:    "observe",
	}, nil, dispatcher)
	supervisor.Now = func() time.Time { return now }

	require.Equal(t, ExecutionSupervisorStats{}, (*ExecutionSupervisor)(nil).Stats(),
		"a nil supervisor renders the zero snapshot instead of panicking")
	idle := supervisor.Stats()
	require.True(t, idle.Enabled)
	require.Equal(t, "observe", idle.Mode)
	require.Equal(t, 5*time.Second, idle.ScanInterval)
	require.Equal(t, 30*time.Minute, idle.ExecutionTimeout)
	require.False(t, idle.LoopRunning)
	require.Zero(t, idle.Scans)
	require.True(t, idle.LastScanAt.IsZero())
	require.Empty(t, idle.LastScanDecisions)

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:        RunWorkflowSpawnAgent,
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-stats",
		ProgressTimeout: time.Minute,
	})
	require.NoError(t, err)
	// Terminal completion enqueues a durable outbox row; the next scan flushes it.
	require.NoError(t, supervisor.CompleteRun(ctx, run.RunID, RunStatusSucceeded, "", "", map[string]string{"status": "succeeded"}))

	now = now.Add(time.Minute)
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions, "terminal runs leave the active set")
	stats := supervisor.Stats()
	require.EqualValues(t, 1, stats.Scans)
	require.Zero(t, stats.Decisions)
	require.Equal(t, now, stats.LastScanAt)
	require.Empty(t, stats.LastScanError)
	require.Equal(t, 1, stats.LastDispatchDelivered)
	require.Zero(t, stats.LastDispatchFailed)
	require.Equal(t, now, stats.LastDispatchAt)

	// A stalled active run surfaces as one observe-mode decision.
	stalled, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:        RunWorkflowSpawnAgent,
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-stalled",
		ProgressTimeout: time.Minute,
	})
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)

	stats = supervisor.Stats()
	require.EqualValues(t, 2, stats.Scans)
	require.EqualValues(t, 1, stats.Decisions)
	require.Zero(t, stats.Enforced, "observe mode never counts an enforcement action")
	require.Len(t, stats.LastScanDecisions, 1)
	require.Equal(t, stalled.RunID, stats.LastScanDecisions[0].RunID)
	require.Equal(t, "progress_stalled", stats.LastScanDecisions[0].Decision)
	require.Equal(t, "none_observe", stats.LastScanDecisions[0].ActionTaken)
}

// TestExecutionSupervisor_StatsCountsEnforcementAndCapsDecisions keeps the
// enforce path visible in the snapshot and bounds the retained decision slice.
func TestExecutionSupervisor_StatsCountsEnforcementAndCapsDecisions(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	interrupter := &fakeInterrupter{}
	supervisor, _ := newTestExecutionSupervisor(t, "sup-stats-enforce", ExecutionSupervisorConfig{
		Enabled: true,
		Mode:    "enforce",
	}, interrupter, nil)
	supervisor.Now = func() time.Time { return now }

	const runs = 7
	for i := 0; i < runs; i++ {
		_, err := supervisor.StartRun(ctx, RunSpec{
			Workflow:        RunWorkflowSpawnAgent,
			RootSessionID:   "root-session",
			ParentSessionID: "parent-session",
			SessionID:       "child-" + string(rune('a'+i)),
			ProgressTimeout: time.Minute,
		})
		require.NoError(t, err)
	}
	now = now.Add(2 * time.Minute)
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, runs)

	stats := supervisor.Stats()
	require.EqualValues(t, runs, stats.Decisions)
	require.EqualValues(t, runs, stats.Enforced, "reporting is an action taken")
	require.Len(t, stats.LastScanDecisions, maxSupervisorStatsDecisions)
	require.Zero(t, interrupter.count(), "change #1: the report tier must not cancel")

	// The decision window expires without a parent decision: now the fallback
	// interrupts every run and the enforcement counter doubles.
	now = now.Add(13 * time.Minute)
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, runs)
	require.Equal(t, runs, interrupter.count())

	stats = supervisor.Stats()
	require.EqualValues(t, 2*runs, stats.Decisions)
	require.EqualValues(t, 2*runs, stats.Enforced)
}

// TestExecutionSupervisor_StatsLoopLifecycle guards the RunLoop half of the
// snapshot: the debug surface must be able to tell "built" from "running".
func TestExecutionSupervisor_StatsLoopLifecycle(t *testing.T) {
	supervisor, _ := newTestExecutionSupervisor(t, "sup-stats-loop", ExecutionSupervisorConfig{
		Enabled:      true,
		Mode:         "observe",
		ScanInterval: time.Hour, // keep the loop idle for the assertion window
	}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		supervisor.RunLoop(ctx)
	}()
	require.Eventually(t, func() bool { return supervisor.Stats().LoopRunning }, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.False(t, supervisor.Stats().LoopRunning)
}
