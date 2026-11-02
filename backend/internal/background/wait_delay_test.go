package background

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// TestManagerSettlesWaitDelayWhenGrandchildHoldsPipe is the remote-shell
// regression for the classic "shell already exited but the tool never returns"
// hang. cmd.exe starts a 30s child with `start /b`; the child inherits the
// job's stdout/stderr pipe. WaitDelay must settle the job with the root's real
// exit code instead of leaving it running forever.
func TestManagerSettlesWaitDelayWhenGrandchildHoldsPipe(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the inherited-pipe reproducer uses cmd.exe start /b")
	}

	ctx := context.Background()
	dir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(dir, "background.db"),
		LogDir:            filepath.Join(dir, "logs"),
		MaxConcurrentJobs: 1,
		WaitDelay:         500 * time.Millisecond,
		// Remote targets disable the detached runner; exercise the same
		// in-process launcher that remote shell uses.
		DisableDetached: true,
	})
	defer func() { require.NoError(t, manager.Close()) }()
	require.Equal(t, 500*time.Millisecond, manager.waitDelay)

	job, err := manager.SubmitShell(ctx, "session-waitdelay", BackgroundTaskArgs{
		Command: `cmd.exe /c start /b ping -n 6 127.0.0.1`,
	})
	require.NoError(t, err)

	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusCompleted, backgroundTestTimeout(10*time.Second)))

	settled, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, settled)
	require.Equal(t, StatusCompleted, settled.Status)
	require.NotNil(t, settled.ExitCode)
	require.Equal(t, 0, *settled.ExitCode)
	require.Equal(t, true, settled.Metadata["wait_delay_used"])
	require.Equal(t, int64(500), settled.Metadata["wait_delay_ms"])
}

// TestNewManagerResolvesDefaultWaitDelay ties the zero config value to the
// shared executor default (or AICLI_SHELL_WAIT_DELAY when set).
func TestNewManagerResolvesDefaultWaitDelay(t *testing.T) {
	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	require.Equal(t, runtimeexecutor.ResolveProcessWaitDelay(), manager.waitDelay)
}
