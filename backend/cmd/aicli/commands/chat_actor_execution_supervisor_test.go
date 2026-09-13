package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestLocalExecutionSupervisorWiresDurableRunsAndStops covers plan §P0-4: the
// CLI local host builds a supervisor over the same durable control plane, the
// loop is lifecycle-bound, and Close stops it.
func TestLocalExecutionSupervisorWiresDurableRunsAndStops(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.lifecycleCtx, host.lifecycleCancel = context.WithCancel(context.Background())

	supervisor := host.getLocalExecutionSupervisor()
	require.NotNil(t, supervisor)
	require.True(t, supervisor.Config.Enabled)
	require.Equal(t, "observe", supervisor.Config.Mode, "CLI default keeps local children running")
	require.Same(t, supervisor, host.getLocalExecutionSupervisor(), "supervisor is built once")

	run, err := supervisor.StartRun(context.Background(), supervision.RunSpec{
		Kind:            "agent",
		Workflow:        "spawn_agent",
		RootSessionID:   "parent-local-1",
		ParentSessionID: "parent-local-1",
		SessionID:       "child-local-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.RunID)
	require.Equal(t, "child-local-1", run.SessionID)

	stored, err := supervisor.Store.GetExecutionRun(context.Background(), run.RunID)
	require.NoError(t, err)
	require.Equal(t, run.RunID, stored.RunID)

	require.NotNil(t, host.executionSupervisorCtx)
	host.Close()
	require.ErrorIs(t, host.executionSupervisorCtx.Err(), context.Canceled)
}

// TestLocalExecutionSupervisorModeOverride keeps the enforce path reachable
// without changing the conservative default.
func TestLocalExecutionSupervisorModeOverride(t *testing.T) {
	t.Setenv(localExecutionSupervisorModeEnv, "enforce")
	require.Equal(t, "enforce", localExecutionSupervisorMode())

	t.Setenv(localExecutionSupervisorModeEnv, " Enforce ")
	require.Equal(t, "enforce", localExecutionSupervisorMode())

	t.Setenv(localExecutionSupervisorModeEnv, "unexpected")
	require.Equal(t, "observe", localExecutionSupervisorMode())

	t.Setenv(localExecutionSupervisorModeEnv, "enforce")
	host := newLocalSupervisionTestHost(t)
	host.lifecycleCtx, host.lifecycleCancel = context.WithCancel(context.Background())
	supervisor := host.getLocalExecutionSupervisor()
	require.NotNil(t, supervisor)
	require.Equal(t, "enforce", supervisor.Config.Mode)

	// The supervision config thresholds flow into the watchdog.
	cfg := localExecutionSupervisorConfig(supervision.Config{})
	require.Equal(t, 30*time.Minute, cfg.DefaultExecutionTimeout)
	require.Equal(t, 5*time.Minute, cfg.DefaultProgressTimeout)
	host.Close()
}

// TestLocalExecutionSupervisorWithoutControlPlaneStaysNil preserves the
// previous spawn behavior when no durable supervision plane is available.
func TestLocalExecutionSupervisorWithoutControlPlaneStaysNil(t *testing.T) {
	host := &localChatRuntimeHost{}
	require.Nil(t, host.getLocalExecutionSupervisor())
	require.Nil(t, (*localChatRuntimeHost)(nil).getLocalExecutionSupervisor())
}
