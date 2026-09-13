package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestChatDebugSupervisionWatchdogReportsWiringStates covers the P0-4 周边
// visibility requirement: /debug must explain whether local children are under
// deadline supervision and why not, instead of silently rendering nothing.
func TestChatDebugSupervisionWatchdogReportsWiringStates(t *testing.T) {
	unwired := chatDebugExecutionSupervisorText(context.Background(), nil)
	require.Contains(t, unwired, "执行看门狗（P0-4）：未接线")
	require.Contains(t, unwired, "没有本地运行时宿主")

	hostWithoutPlane := &localChatRuntimeHost{}
	bare := newChatDebugSupervisionSession(hostWithoutPlane, "parent-session")
	noPlane := chatDebugExecutionSupervisorText(context.Background(), bare)
	require.Contains(t, noPlane, "执行看门狗（P0-4）：未接线")
	require.Contains(t, noPlane, "durable supervision control plane")
	require.Contains(t, noPlane, "不受 deadline / progress / approval 巡检")
	require.Contains(t, noPlane, localExecutionSupervisorModeEnv)

	// A ready-but-unbuilt host renders the thresholds without starting the
	// background loop (the debug path must stay read-only).
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	ready := chatDebugExecutionSupervisorText(context.Background(), session)
	require.Contains(t, ready, "已就绪（尚未启动）")
	require.Contains(t, ready, "mode=observe")
	require.Contains(t, ready, "scan=5s")
	require.Contains(t, ready, "execution_deadline=30m0s")
	require.Contains(t, ready, "progress_deadline=5m0s")
	require.Contains(t, ready, "approval_deadline=1h0m0s")
	require.Nil(t, host.peekLocalExecutionSupervisor(), "rendering state must not build the watchdog")
	require.Nil(t, host.executionSupervisorCtx, "rendering state must not start the scan loop")
}

// TestChatDebugSupervisionWatchdogReportsRunningSupervisor drives the built
// state end to end: the snapshot lists the loop, the scan counters, the last
// decision and the durable run/outbox view.
func TestChatDebugSupervisionWatchdogReportsRunningSupervisor(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.lifecycleCtx, host.lifecycleCancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		if host.lifecycleCancel != nil {
			host.lifecycleCancel()
		}
	})
	session := newChatDebugSupervisionSession(host, "parent-session")

	supervisor := host.getLocalExecutionSupervisor()
	require.NotNil(t, supervisor)
	require.Eventually(t, func() bool { return supervisor.Stats().LoopRunning }, 2*time.Second, 10*time.Millisecond)
	// The test host has no session event store, so the mailbox dispatcher is
	// wired here to exercise the completion-outbox half of the snapshot.
	supervisor.Dispatcher = toolbroker.CompletionDispatchFunc(func(context.Context, supervision.CompletionOutboxEntry) (int64, error) {
		return 42, nil
	})

	run, err := supervisor.StartRun(context.Background(), supervision.RunSpec{
		Kind:            "agent",
		Workflow:        supervision.RunWorkflowSpawnAgent,
		RootSessionID:   "parent-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-watchdog",
	})
	require.NoError(t, err)
	require.NoError(t, supervisor.CompleteRun(context.Background(), run.RunID, supervision.RunStatusSucceeded, "", "", nil))
	_, err = supervisor.ScanOnce(context.Background())
	require.NoError(t, err)

	text := chatDebugExecutionSupervisorText(context.Background(), session)
	require.Contains(t, text, "执行看门狗（P0-4）：运行中")
	require.Contains(t, text, "循环=running")
	require.Contains(t, text, "mode=observe")
	require.Contains(t, text, "累计：scans=")
	require.Contains(t, text, "最近扫描：")
	require.Contains(t, text, "最近完成出件：")
	require.Contains(t, text, "delivered=1 failed=0")
	require.Contains(t, text, "store：未终态 run=0（本会话 0）")
	require.Contains(t, text, "未投递完成出件=0")
}

// TestChatDebugSupervisionWatchdogBypassesNotificationStore guards the routing:
// the watchdog snapshot must not fail with "no supervision store" when the
// host only lacks the notification store, and it must be reachable through the
// /debug supervision dispatcher.
func TestChatDebugSupervisionWatchdogBypassesNotificationStore(t *testing.T) {
	session := newChatDebugSupervisionSession(&localChatRuntimeHost{}, "parent-session")
	text, err := handleChatDebugSupervisionCommand(session, "supervision watchdog")
	require.NoError(t, err, "watchdog must not require the notification store")
	require.Contains(t, text, "执行看门狗（P0-4）：未接线")

	_, err = handleChatDebugSupervisionCommand(session, "supervision list")
	require.Error(t, err, "the notification subcommands keep requiring the store")
	require.Contains(t, err.Error(), "supervision store")
}

// TestChatDebugSupervisionWatchdogUsageIsAdvertised keeps the help text in sync
// with the routed subcommands.
func TestChatDebugSupervisionWatchdogUsageIsAdvertised(t *testing.T) {
	usage := chatDebugSupervisionUsageText()
	require.Contains(t, usage, "/debug supervision watchdog")
	for _, subcommand := range []string{"watchdog", "supervisor", "execution-supervisor"} {
		require.True(t, chatDebugSupervisorSubcommand(subcommand))
	}
	require.False(t, chatDebugSupervisorSubcommand("list"))
}
