package background

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestSubmitShellCapturesWorkspaceShellRuntime(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses cmd.exe/PowerShell shell identity")
	}
	ctx := context.Background()
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	runtimeA := &runtimeexecutor.ShellRuntime{
		Shell: runtimeexecutor.Shell{
			Path:    "cmd.exe",
			Type:    runtimeexecutor.ShellTypeCmd,
			ID:      runtimeexecutor.ShellIDCmd,
			Dialect: runtimeexecutor.ShellDialectCmd,
		},
		Delivery: runtimeexecutor.ScriptDeliveryStdin,
	}
	job, err := manager.SubmitShell(
		toolctx.WithShellRuntime(ctx, runtimeA),
		"session-shell-capture",
		BackgroundTaskArgs{Command: "echo ok"},
	)
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDCmd, job.Metadata["shell_id"])
	require.Equal(t, "cmd.exe", job.Metadata["shell_path"])

	// A later global/workspace change must not rewrite the submitted job's
	// captured identity; execution resolves the managed job runtime.
	manager.mu.RLock()
	managed := manager.jobs[job.ID]
	manager.mu.RUnlock()
	require.NotNil(t, managed)
	require.NotNil(t, managed.shellRuntime)
	require.Equal(t, runtimeexecutor.ShellIDCmd, managed.shellRuntime.Shell.ID)

	done, timedOut, err := manager.WaitJob(ctx, job.ID, 10*time.Second)
	require.NoError(t, err)
	require.False(t, timedOut)
	require.NotNil(t, done)
	require.Equal(t, StatusCompleted, done.Status)
}
