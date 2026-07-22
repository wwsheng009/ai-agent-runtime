package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestBuildWindowsDetachedRunnerContentUsesDetectedShellAndCwd(t *testing.T) {
	shell := runtimeexecutor.Shell{Path: `C:\Program Files\PowerShell\7\pwsh.exe`, Type: runtimeexecutor.ShellTypePwsh}

	content := buildWindowsDetachedRunnerContent(
		shell,
		"git status",
		`E:\projects\ai\ai-agent-runtime`,
		`C:\logs\job.log`,
		`C:\logs\job.status`,
	)

	require.Contains(t, content, `$shellPath = 'C:\Program Files\PowerShell\7\pwsh.exe'`)
	require.Contains(t, content, `$shellArgs = @('-NoProfile', '-Command', 'git status')`)
	require.Contains(t, content, `Set-Location -LiteralPath 'E:\projects\ai\ai-agent-runtime'`)
	require.Contains(t, content, `[System.IO.File]::WriteAllText('C:\logs\job.status'`)
	require.Contains(t, content, `$env:PATH = "$systemRoot\System32\WindowsPowerShell\v1.0`)
	require.Contains(t, content, `Start-Job -ArgumentList $heartbeatPath, $ownerPid`)
	require.Contains(t, content, `$heartbeatPath = 'C:\logs\job.status.heartbeat'`)
	require.Contains(t, content, `$scriptExitCode = 1`)
	require.NotContains(t, content, `cmd.exe /D /S /C`)
}

func TestDetachedLaunchRetriesTransientLauncherFailures(t *testing.T) {
	original := detachedShellLauncher
	defer func() { detachedShellLauncher = original }()

	attempts := 0
	detachedShellLauncher = func(command, cwd, logPath string) (*detachedLaunch, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("temporary process creation failure")
		}
		return &detachedLaunch{PID: 42, StatusPath: "status", RunnerPath: "runner", HeartbeatPath: "heartbeat"}, nil
	}

	manager := NewManager(Config{LaunchMaxAttempts: 3, RetryBackoff: time.Millisecond})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		ctx:  context.Background(),
		info: Job{ID: "job-retry", Command: "echo ok", Metadata: map[string]interface{}{}},
	}

	launch, err := manager.launchDetachedShellWithRetry(context.Background(), managed)
	require.NoError(t, err)
	require.Equal(t, 42, launch.PID)
	require.Equal(t, 3, attempts)
	require.Equal(t, 3, managed.info.Metadata["launch_attempt"])
	result := decorateTaskOutputResult(TaskOutputResult{}, managed.info)
	require.Equal(t, 3, result.LaunchAttempt)
	require.Equal(t, 3, result.LaunchMaxAttempts)
}

func TestDetachedLaunchDoesNotRetryAmbiguousPostStartFailure(t *testing.T) {
	original := detachedShellLauncher
	defer func() { detachedShellLauncher = original }()

	attempts := 0
	detachedShellLauncher = func(command, cwd, logPath string) (*detachedLaunch, error) {
		attempts++
		return nil, &detachedLaunchError{err: errors.New("launcher result is ambiguous"), retryable: false}
	}

	manager := NewManager(Config{LaunchMaxAttempts: 3, RetryBackoff: time.Millisecond})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		ctx:  context.Background(),
		info: Job{ID: "job-no-duplicate", Command: "side-effecting-command", Metadata: map[string]interface{}{}},
	}

	_, err := manager.launchDetachedShellWithRetry(context.Background(), managed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "after 1 attempt(s)")
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, managed.info.Metadata["launch_attempt"])
	require.Equal(t, 3, managed.info.Metadata["launch_max_attempts"])
}

func TestDetachedLaunchFailureUsesStructuredProcessStartCode(t *testing.T) {
	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		info:   Job{ID: "job-launch-failed", Status: StatusRunning, Metadata: map[string]interface{}{}},
		output: newOutputBuffer(1024),
	}
	manager.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, errors.New("launcher unavailable"))

	job := managed.snapshot()
	require.NotNil(t, job)
	require.Equal(t, StatusFailed, job.Status)
	result := decorateTaskOutputResult(TaskOutputResult{}, *job)
	require.Equal(t, string(runtimeerrors.ErrProcessStartFailed), result.ErrorCode)
}

func TestDetachedHeartbeatMonitorUsesObservedProgressInsteadOfWallClockModTime(t *testing.T) {
	heartbeatPath := filepath.Join(t.TempDir(), "job.heartbeat")
	require.NoError(t, os.WriteFile(heartbeatPath, []byte("tick-1"), 0o644))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(heartbeatPath, old, old))

	manager := NewManager(Config{HeartbeatTimeout: 30 * time.Second})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{info: Job{Metadata: map[string]interface{}{backgroundMetaHeartbeatPath: heartbeatPath}}}
	startedAt := time.Now()
	monitor := newDetachedHeartbeatMonitor(startedAt)

	require.False(t, manager.detachedHeartbeatStalledWithMonitor(managed, monitor, startedAt))
	require.True(t, manager.detachedHeartbeatStalledWithMonitor(managed, monitor, startedAt.Add(31*time.Second)))
	require.NoError(t, os.WriteFile(heartbeatPath, []byte("tick-2"), 0o644))
	require.False(t, manager.detachedHeartbeatStalledWithMonitor(managed, monitor, startedAt.Add(32*time.Second)))
}

func TestDetachedProcessIdentityRejectsZombieAndPIDReuse(t *testing.T) {
	metadata := map[string]interface{}{backgroundMetaProcessIdentity: "original"}
	require.True(t, detachedProcessMatches(metadata, processHealth{Running: true, Identity: "original"}))
	require.False(t, detachedProcessMatches(metadata, processHealth{Running: true, Identity: "replacement"}))
	require.False(t, detachedProcessMatches(metadata, processHealth{Running: true, Zombie: true, Identity: "original"}))
	require.False(t, detachedProcessMatches(metadata, processHealth{}))
}

func TestDetachedWatchdogFailurePreservesStructuredDiagnosis(t *testing.T) {
	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		info: Job{
			ID:       "job-stalled",
			Status:   StatusRunning,
			Metadata: map[string]interface{}{},
		},
		output: newOutputBuffer(1024),
	}

	manager.markDetachedWatchdogFailure(managed, watchdogStateStalled, "heartbeat stalled")
	job := managed.snapshot()
	require.NotNil(t, job)
	require.Equal(t, StatusTimedOut, job.Status)

	result := decorateTaskOutputResult(TaskOutputResult{}, *job)
	require.Equal(t, "TOOL_TIMEOUT", result.ErrorCode)
	require.Equal(t, watchdogStateStalled, result.WatchdogState)
	require.Equal(t, watchdogCodeProcessStalled, result.WatchdogErrorCode)
}

func TestDetachedWatchdogErrorCodesDistinguishProcessFailures(t *testing.T) {
	require.Equal(t, watchdogCodeProcessStalled, detachedWatchdogErrorCode(watchdogStateStalled))
	require.Equal(t, watchdogCodeProcessZombie, detachedWatchdogErrorCode(watchdogStateZombie))
	require.Equal(t, watchdogCodePIDReused, detachedWatchdogErrorCode(watchdogStatePIDReused))
	require.Equal(t, watchdogCodeProcessLost, detachedWatchdogErrorCode(watchdogStateMissing))
}

func TestDetachedInfrastructureFailureRequeuesRerunJobWithBackoff(t *testing.T) {
	manager := NewManager(Config{
		RecoveryMaxAttempts:     2,
		RecoveryBackoffSchedule: []time.Duration{time.Millisecond, 2 * time.Millisecond},
	})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:            "job-recover",
			Status:        StatusRunning,
			RestartPolicy: RestartPolicyRerun,
			Metadata:      map[string]interface{}{},
		},
		request: BackgroundTaskArgs{RestartPolicy: RestartPolicyRerun},
		output:  newOutputBuffer(1024),
	}

	require.True(t, manager.scheduleDetachedRecovery(managed, watchdogStateZombie, "runner became zombie"))
	job := managed.snapshot()
	require.Equal(t, StatusPending, job.Status)
	require.Equal(t, 1, job.Metadata[backgroundMetaRecoveryAttempt])
	require.Equal(t, 2, job.Metadata[backgroundMetaRecoveryMax])
	result := decorateTaskOutputResult(TaskOutputResult{}, *job)
	require.Equal(t, 1, result.RecoveryAttempt)
	require.Equal(t, 2, result.RecoveryMaxAttempts)

	managed.mu.Lock()
	managed.info.Status = StatusRunning
	managed.mu.Unlock()
	require.True(t, manager.scheduleDetachedRecovery(managed, watchdogStateMissing, "runner disappeared"))
	require.Equal(t, 2, managed.snapshot().Metadata[backgroundMetaRecoveryAttempt])

	managed.mu.Lock()
	managed.info.Status = StatusRunning
	managed.mu.Unlock()
	require.False(t, manager.scheduleDetachedRecovery(managed, watchdogStateMissing, "retry budget exhausted"))
}

func TestResumeDetachedRecoveryKeepsPersistedAttemptAndWaitsUntilScheduledTime(t *testing.T) {
	manager := NewManager(Config{RecoveryMaxAttempts: -1})
	defer func() { require.NoError(t, manager.Close()) }()
	nextRecoveryAt := time.Now().Add(15 * time.Millisecond).UTC()
	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:            "job-resume-recovery",
			Status:        StatusRunning,
			RestartPolicy: RestartPolicyRerun,
			Metadata: map[string]interface{}{
				backgroundMetaRecoveryAttempt: 4,
				backgroundMetaRecoveryMax:     -1,
				backgroundMetaNextRecoveryAt:  nextRecoveryAt.Format(time.RFC3339Nano),
			},
		},
		request: BackgroundTaskArgs{RestartPolicy: RestartPolicyRerun},
		output:  newOutputBuffer(1024),
	}

	require.True(t, manager.resumeDetachedRecovery(managed, "manager restarted"))
	job := managed.snapshot()
	require.Equal(t, StatusPending, job.Status)
	require.Equal(t, 4, job.Metadata[backgroundMetaRecoveryAttempt])
	_, hasNextRecovery := job.Metadata[backgroundMetaNextRecoveryAt]
	require.False(t, hasNextRecovery)
}

func TestTaskOutputExposesLiveProcessAndProgressTelemetry(t *testing.T) {
	tempDir := t.TempDir()
	heartbeatPath := filepath.Join(tempDir, "job.heartbeat")
	logPath := filepath.Join(tempDir, "job.log")
	require.NoError(t, os.WriteFile(heartbeatPath, []byte("tick"), 0o644))
	require.NoError(t, os.WriteFile(logPath, []byte("progress"), 0o644))
	health := inspectProcess(os.Getpid())
	metadata := map[string]interface{}{
		backgroundMetaPID:           os.Getpid(),
		backgroundMetaHeartbeatPath: heartbeatPath,
	}
	if health.Identity != "" {
		metadata[backgroundMetaProcessIdentity] = health.Identity
	}

	result := decorateTaskOutputResult(TaskOutputResult{}, Job{LogPath: logPath, Metadata: metadata})
	require.Equal(t, "running", result.ProcessState)
	require.NotEmpty(t, result.LastOutputAt)
	require.GreaterOrEqual(t, result.HeartbeatAgeMs, int64(0))
	require.GreaterOrEqual(t, result.QuietForMs, int64(0))
}

func TestWriteUnixDetachedRunnerUsesDetectedShellAndRecordsCwdFailure(t *testing.T) {
	tempDir := t.TempDir()
	runnerPath := filepath.Join(tempDir, "runner.sh")
	logPath := filepath.Join(tempDir, "job.log")
	statusPath := filepath.Join(tempDir, "job.status")
	shell := runtimeexecutor.Shell{Path: "/opt/zsh", Type: runtimeexecutor.ShellTypeZsh}

	require.NoError(t, writeUnixDetachedRunner(runnerPath, shell, "git status", "/tmp/work dir", logPath, statusPath))
	data, err := os.ReadFile(runnerPath)
	require.NoError(t, err)
	content := string(data)

	require.Contains(t, content, "if ! cd '/tmp/work dir'; then")
	require.Contains(t, content, "printf 'failed to change directory: %s\\n' '/tmp/work dir'")
	require.Contains(t, content, "printf \"%s\" \"1\" > '"+statusPath+"'")
	require.Contains(t, content, "'/opt/zsh' '-c' 'git status' >> '"+logPath+"' 2>&1")
}
