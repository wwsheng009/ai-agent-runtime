package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestManagerDispatchesByPriorityWithinCapacity(t *testing.T) {
	ctx := context.Background()
	var (
		mu     sync.Mutex
		events []JobEvent
	)
	manager := NewManager(Config{
		MaxConcurrentJobs: 1,
		EventHandler: func(event JobEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})
	defer func() {
		require.NoError(t, manager.Close())
	}()

	blocker, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{
		Command:  shellDelayCommand(350*time.Millisecond, "blocker"),
		Priority: 0,
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, blocker.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	low, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{
		Command:  shellEchoCommand("low"),
		Priority: 1,
	})
	require.NoError(t, err)

	high, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{
		Command:  shellEchoCommand("high"),
		Priority: 10,
	})
	require.NoError(t, err)

	require.NoError(t, waitForJobStatus(ctx, manager, blocker.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)))
	require.NoError(t, waitForJobStatus(ctx, manager, low.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)))
	require.NoError(t, waitForJobStatus(ctx, manager, high.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)))

	mu.Lock()
	recorded := append([]JobEvent(nil), events...)
	mu.Unlock()

	runningOrder := make([]string, 0, 3)
	for _, event := range recorded {
		if event.Type != "running" {
			continue
		}
		runningOrder = append(runningOrder, event.JobID)
	}
	require.GreaterOrEqual(t, len(runningOrder), 3)
	require.Equal(t, []string{blocker.ID, high.ID, low.ID}, runningOrder[:3])
}

func TestManagerRecoversPendingAndMarksInterruptedRunningJobsOrphaned(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)

	pendingLogPath := filepath.Join(logDir, "job_pending.log")
	runningLogPath := filepath.Join(logDir, "job_running.log")
	require.NoError(t, os.WriteFile(pendingLogPath, []byte{}, 0o644))
	require.NoError(t, os.WriteFile(runningLogPath, []byte("partial-output\n"), 0o644))

	pendingJob := Job{
		ID:        "job_pending",
		SessionID: "session-1",
		Kind:      "shell",
		Command:   shellEchoCommand("pending"),
		Priority:  5,
		Status:    StatusPending,
		CreatedAt: time.Now().Add(-2 * time.Second).UTC(),
		LogPath:   pendingLogPath,
		Metadata: map[string]interface{}{
			// Detached recovery on Windows can take a few seconds to start the
			// helper process chain, so keep the timeout generous enough to avoid
			// flaking under full-suite load.
			"timeout_sec": 15,
		},
	}
	require.NoError(t, store.SaveJob(ctx, pendingJob))

	startedAt := time.Now().Add(-1 * time.Second).UTC()
	runningJob := Job{
		ID:        "job_running",
		SessionID: "session-1",
		Kind:      "shell",
		Command:   shellDelayCommand(500*time.Millisecond, "running"),
		Priority:  1,
		Status:    StatusRunning,
		CreatedAt: time.Now().Add(-3 * time.Second).UTC(),
		StartedAt: &startedAt,
		LogPath:   runningLogPath,
	}
	require.NoError(t, store.SaveJob(ctx, runningJob))
	require.NoError(t, store.Close())

	manager := NewManager(Config{
		StorePath:         storePath,
		LogDir:            logDir,
		MaxConcurrentJobs: 1,
	})
	defer func() {
		require.NoError(t, manager.Close())
	}()

	require.NoError(t, waitForJobStatus(ctx, manager, pendingJob.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)))

	recoveredRunning, err := manager.GetJob(ctx, runningJob.ID)
	require.NoError(t, err)
	require.NotNil(t, recoveredRunning)
	require.Equal(t, StatusOrphaned, recoveredRunning.Status)
	require.Contains(t, recoveredRunning.Message, "restarted before job")

	pendingEvents, err := manager.ListEvents(ctx, pendingJob.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(pendingEvents), "recovered_queued")

	runningEvents, err := manager.ListEvents(ctx, runningJob.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(runningEvents), "orphaned")
}

func TestManagerReturnsStableJobNotFoundCode(t *testing.T) {
	manager := NewManager(DefaultConfig())
	defer func() { require.NoError(t, manager.Close()) }()

	_, err := manager.GetJob(context.Background(), "job_missing")
	require.Error(t, err)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound))
	_, err = manager.ReadOutput(context.Background(), TaskOutputArgs{JobID: "job_missing"})
	require.Error(t, err)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound))
}

func TestManagerPrunesExpiredTerminalJobsAndOwnedArtifacts(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	logPath := filepath.Join(logDir, "job_expired.log")
	require.NoError(t, os.WriteFile(logPath, []byte("old output"), 0o644))

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	finishedAt := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, store.SaveJob(ctx, Job{
		ID: "job_expired", SessionID: "session-1", Status: StatusCompleted,
		CreatedAt: finishedAt.Add(-time.Minute), FinishedAt: &finishedAt, LogPath: logPath,
	}))
	require.NoError(t, store.Close())

	manager := NewManager(Config{StorePath: storePath, LogDir: logDir, Retention: time.Hour})
	defer func() { require.NoError(t, manager.Close()) }()
	_, err = manager.GetJob(ctx, "job_expired")
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound))
	_, statErr := os.Stat(logPath)
	require.True(t, os.IsNotExist(statErr))
}

func TestManagerRecoversRunningJobWithRerunPolicy(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)

	logPath := filepath.Join(logDir, "job_rerun.log")
	require.NoError(t, os.WriteFile(logPath, []byte("partial-output\n"), 0o644))

	startedAt := time.Now().Add(-1 * time.Second).UTC()
	runningJob := Job{
		ID:        "job_rerun",
		SessionID: "session-1",
		Kind:      "shell",
		Command:   shellEchoCommand("rerun"),
		Priority:  3,
		Status:    StatusRunning,
		CreatedAt: time.Now().Add(-3 * time.Second).UTC(),
		StartedAt: &startedAt,
		LogPath:   logPath,
		Metadata: map[string]interface{}{
			"restart_policy": string(RestartPolicyRerun),
		},
	}
	require.NoError(t, store.SaveJob(ctx, runningJob))
	require.NoError(t, store.Close())

	manager := NewManager(Config{
		StorePath:         storePath,
		LogDir:            logDir,
		MaxConcurrentJobs: 1,
	})
	defer func() {
		require.NoError(t, manager.Close())
	}()

	require.NoError(t, waitForJobStatus(ctx, manager, runningJob.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)))

	recovered, err := manager.GetJob(ctx, runningJob.ID)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, StatusCompleted, recovered.Status)
	require.Equal(t, RestartPolicyRerun, recovered.RestartPolicy)

	output, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: runningJob.ID, Offset: 0})
	require.NoError(t, err)
	require.Contains(t, output.Output, "partial-output")
	require.Contains(t, output.Output, "rerun")

	events, err := manager.ListEvents(ctx, runningJob.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(events), "recovered_requeued")
}

func TestManagerRecoversDetachedRunningJobAcrossRestart(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	manager := NewManager(Config{
		StorePath:         storePath,
		LogDir:            logDir,
		MaxConcurrentJobs: 1,
	})

	job, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{
		Command: shellDelayCommand(1500*time.Millisecond, "continued"),
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	require.NoError(t, manager.Close())

	recoveredManager := NewManager(Config{
		StorePath:         storePath,
		LogDir:            logDir,
		MaxConcurrentJobs: 1,
	})
	defer func() {
		require.NoError(t, recoveredManager.Close())
	}()

	require.NoError(t, waitForJobStatus(ctx, recoveredManager, job.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)))

	recovered, err := recoveredManager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, StatusCompleted, recovered.Status)

	output, err := recoveredManager.ReadOutput(ctx, TaskOutputArgs{JobID: job.ID, Offset: 0})
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(output.Output, "continued"))

	events, err := recoveredManager.ListEvents(ctx, job.ID, 0, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, strings.Count(strings.Join(eventTypes(events), ","), "running"), 2)
}

func TestManagerPersistsTimeoutBudgetAndStructuredTimeoutOutcome(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{
		MaxConcurrentJobs: 1,
		DefaultTimeout:    100 * time.Millisecond,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	explicit, err := manager.SubmitShell(ctx, "session-timeout", BackgroundTaskArgs{
		Command:    shellEchoCommand("explicit-timeout"),
		TimeoutSec: 600,
	})
	require.NoError(t, err)
	require.Equal(t, int64(600000), explicit.Metadata["timeout_requested_ms"])
	require.Equal(t, int64(600000), explicit.Metadata["timeout_effective_ms"])
	require.Equal(t, "tool_argument", explicit.Metadata["timeout_source"])

	timedOut, err := manager.SubmitShell(ctx, "session-timeout", BackgroundTaskArgs{
		Command: shellDelayCommand(500*time.Millisecond, "too-late"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, timedOut.ID, StatusTimedOut, backgroundTestTimeout(10*time.Second)))

	job, err := manager.GetJob(ctx, timedOut.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimeerrors.ErrToolTimeout), job.Metadata["error_code"])
	require.Equal(t, int64(100), job.Metadata["timeout_effective_ms"])
	require.Equal(t, "tool_default", job.Metadata["timeout_source"])

	output, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: timedOut.ID})
	require.NoError(t, err)
	require.Equal(t, string(StatusTimedOut), output.Status)
	require.Equal(t, string(runtimeerrors.ErrToolTimeout), output.ErrorCode)
	require.Equal(t, int64(100), output.TimeoutEffectiveMs)
	require.Equal(t, "tool_default", output.TimeoutSource)
}

func TestReliabilityEvalBackgroundTimeoutRetrySucceeds(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{
		MaxConcurrentJobs: 1,
		DefaultTimeout:    100 * time.Millisecond,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	first, err := manager.SubmitShell(ctx, "session-retry", BackgroundTaskArgs{
		Command: shellDelayCommand(500*time.Millisecond, "too-late"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, first.ID, StatusTimedOut, backgroundTestTimeout(10*time.Second)))

	retry, err := manager.SubmitShell(ctx, "session-retry", BackgroundTaskArgs{
		Command:    shellEchoCommand("retry-succeeded"),
		TimeoutSec: 5,
	})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, retry.ID)
	require.NoError(t, waitForJobStatus(ctx, manager, retry.ID, StatusCompleted, backgroundTestTimeout(10*time.Second)))

	firstAfterRetry, err := manager.GetJob(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, StatusTimedOut, firstAfterRetry.Status)
	require.Equal(t, string(runtimeerrors.ErrToolTimeout), firstAfterRetry.Metadata["error_code"])
	retryOutput, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: retry.ID})
	require.NoError(t, err)
	require.Equal(t, string(StatusCompleted), retryOutput.Status)
	require.Contains(t, retryOutput.Output, "retry-succeeded")
}

func waitForJobStatus(ctx context.Context, manager *Manager, jobID string, status JobStatus, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastStatus JobStatus
	var lastMessage string
	for time.Now().Before(deadline) {
		job, err := manager.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if job != nil && job.Status == status {
			return nil
		}
		if job != nil {
			lastStatus = job.Status
			lastMessage = job.Message
		}
		time.Sleep(25 * time.Millisecond)
	}
	if lastMessage != "" {
		return fmt.Errorf("job %s did not reach status %s within %s (last status=%s, message=%q)", jobID, status, timeout, lastStatus, lastMessage)
	}
	return fmt.Errorf("job %s did not reach status %s within %s (last status=%s)", jobID, status, timeout, lastStatus)
}

func backgroundTestTimeout(base time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return base * 2
	}
	return base
}

func eventTypes(events []JobEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

func shellEchoCommand(label string) string {
	label = sanitizeTestLabel(label)
	if runtime.GOOS == "windows" {
		return "echo " + label
	}
	return fmt.Sprintf("printf '%s\\n'", label)
}

func shellDelayCommand(delay time.Duration, label string) string {
	label = sanitizeTestLabel(label)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`powershell -NoProfile -Command "Start-Sleep -Milliseconds %d; Write-Output %s"`, delay.Milliseconds(), label)
	}
	return fmt.Sprintf("sleep %.3f; printf '%s\\n'", delay.Seconds(), label)
}

func sanitizeTestLabel(label string) string {
	if label == "" {
		return "job"
	}
	return label
}
