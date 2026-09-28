package background

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCancelKillsWholeProcessTree cancels a running job whose command spawned a
// long-lived descendant and asserts the runner and the descendant are both
// gone: single-pid kills used to leave tree members behind holding ports
// (2026-09-28, P2).
func TestCancelKillsWholeProcessTree(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "child.pid")
	command := treeSpawnCommand(t, tempDir, pidFile)

	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	job, err := manager.SubmitShell(ctx, "session-tree", BackgroundTaskArgs{Command: command})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusRunning, backgroundTestTimeout(20*time.Second)))

	descendantPID := waitForRecordedPID(t, pidFile, backgroundTestTimeout(10*time.Second))
	require.False(t, processMatchesGone(descendantPID, ""), "descendant should be alive before cancel")

	running, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	runnerPID, ok := detachedPID(running.Metadata)
	require.True(t, ok)
	require.NotEqual(t, runnerPID, descendantPID)

	cancelled, err := manager.CancelJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, cancelled.Status)

	deadline := time.Now().Add(backgroundTestTimeout(15 * time.Second))
	for time.Now().Before(deadline) && (!processMatchesGone(runnerPID, "") || !processMatchesGone(descendantPID, "")) {
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, processMatchesGone(runnerPID, ""), "runner pid %d survived cancel", runnerPID)
	require.True(t, processMatchesGone(descendantPID, ""), "descendant pid %d survived cancel", descendantPID)

	events, err := manager.ListEvents(ctx, job.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(events), "kill_verified")
}

// TestOrphanReaperKillsLeftoverProcessOfTerminalJob verifies the reaper path:
// a terminal job still holding a live process (its parent chain is gone, so no
// tree walk can find it) is killed and the cleanup is recorded (2026-09-28,
// P2).
func TestOrphanReaperKillsLeftoverProcessOfTerminalJob(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	leftover := startSleepProcess(t)
	identity := inspectProcess(leftover.Pid).Identity

	finishedAt := time.Now().UTC()
	require.NoError(t, manager.store.SaveJob(ctx, Job{
		ID:              "job_reap",
		SessionID:       "session-reap",
		Kind:            "shell",
		Command:         "echo reap",
		Status:          StatusCancelled,
		CreatedAt:       time.Now().Add(-time.Minute).UTC(),
		FinishedAt:      &finishedAt,
		OwnerInstanceID: manager.instanceID,
		Metadata: map[string]interface{}{
			backgroundMetaPID:             leftover.Pid,
			backgroundMetaProcessIdentity: identity,
		},
	}))
	manager.registerKillWatch("job_reap", leftover.Pid, identity, 1, "test leftover")

	manager.runOrphanReaper()

	require.True(t, processMatchesGone(leftover.Pid, identity), "reaper must kill the leftover process")
	events, err := manager.ListEvents(ctx, "job_reap", 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(events), "kill_verified")
}

// TestRunningJobRecordsProcessGroup pins the process-tree bookkeeping used by
// cross-instance kills and the reaper (2026-09-28, P2).
func TestRunningJobRecordsProcessGroup(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()

	job, err := manager.SubmitShell(ctx, "session-group", BackgroundTaskArgs{
		Command: shellDelayCommand(2*time.Second, "group"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	running, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, running)
	pid, ok := detachedPID(running.Metadata)
	require.True(t, ok)
	group, ok := intMetadataValue(running.Metadata, backgroundMetaProcessGroup)
	require.True(t, ok, "process_group metadata should be recorded")
	require.Equal(t, pid, group)
}

func startSleepProcess(t *testing.T) *os.Process {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command(windowsPowerShellHost(), "-NoProfile", "-Command", "Start-Sleep -Seconds 120")
	} else {
		cmd = exec.Command("/bin/sh", "-c", "sleep 120")
	}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = terminateProcess(cmd.Process.Pid) })
	return cmd.Process
}

// treeSpawnCommand returns shell script text that spawns a detached 120s child
// and records its pid. The child gets file-backed stdio so it does not keep the
// runner's log pipe open.
func treeSpawnCommand(t *testing.T, dir, pidFile string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		host := windowsPowerShellHost()
		outFile := filepath.Join(dir, "child.out")
		errFile := filepath.Join(dir, "child.err")
		return fmt.Sprintf(`$p = Start-Process -FilePath '%s' -ArgumentList @('-NoProfile','-Command','Start-Sleep -Seconds 120') -WindowStyle Hidden -RedirectStandardOutput '%s' -RedirectStandardError '%s' -PassThru
Set-Content -Path '%s' -Value $p.Id
`, host, outFile, errFile, pidFile)
	}
	return fmt.Sprintf("sleep 120 &\necho $! > '%s'\n", pidFile)
}

func waitForRecordedPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid file %s was not written within %s", path, timeout)
	return 0
}
