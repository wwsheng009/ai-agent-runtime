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

func TestDefaultConfigCapsRerunRecoveryAttempts(t *testing.T) {
	cfg := DefaultConfig()
	require.Equal(t, 3, cfg.RecoveryMaxAttempts)
	require.False(t, cfg.RecoverPendingOnStart)
	require.Equal(t, 60*time.Second, cfg.LeaseTTL)
	require.Equal(t, 10*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, 30*time.Minute, cfg.QueueTimeout)
	require.Equal(t, 30*time.Second, cfg.OrphanReaperInterval)
	require.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute}, cfg.RecoveryBackoffSchedule)
}

func TestCompleteJobTreatsNonZeroExitAsCompleted(t *testing.T) {
	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()

	managed := &managedJob{
		info: Job{
			ID:       "job-nonzero",
			Status:   StatusRunning,
			Metadata: map[string]interface{}{"error_code": string(runtimeerrors.ErrToolExecution)},
		},
		output: newOutputBuffer(1024),
	}
	manager.completeJob(managed, 7)

	job := managed.snapshot()
	require.NotNil(t, job)
	require.Equal(t, StatusCompleted, job.Status)
	require.NotNil(t, job.ExitCode)
	require.Equal(t, 7, *job.ExitCode)
	require.Equal(t, "command exited with code 7", job.Message)
	require.Equal(t, true, job.Metadata["non_zero_exit"])
	_, hasErrorCode := job.Metadata["error_code"]
	require.False(t, hasErrorCode)

	result := decorateTaskOutputResult(TaskOutputResult{Status: string(job.Status), ExitCode: job.ExitCode}, *job)
	require.Equal(t, string(StatusCompleted), result.Status)
	require.NotNil(t, result.ExitCode)
	require.Equal(t, 7, *result.ExitCode)
	require.Equal(t, "command exited with code 7", result.Message)
	require.Empty(t, result.ErrorCode)
}

func TestManagedJobSnapshotDoesNotShareMutableState(t *testing.T) {
	startedAt := time.Now().UTC()
	finishedAt := startedAt.Add(time.Second)
	wantStartedAt := startedAt
	wantFinishedAt := finishedAt
	exitCode := 0
	managed := &managedJob{info: Job{
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		ExitCode:   &exitCode,
		Metadata: map[string]interface{}{
			"state":  "queued",
			"nested": map[string]interface{}{"attempt": 1},
			"items":  []interface{}{map[string]interface{}{"status": "pending"}},
		},
	}}

	snapshot := managed.snapshot()
	require.NotNil(t, snapshot)

	managed.mu.Lock()
	managed.info.Metadata["state"] = "running"
	managed.info.Metadata["nested"].(map[string]interface{})["attempt"] = 2
	managed.info.Metadata["items"].([]interface{})[0].(map[string]interface{})["status"] = "done"
	*managed.info.StartedAt = startedAt.Add(time.Minute)
	*managed.info.FinishedAt = finishedAt.Add(time.Minute)
	*managed.info.ExitCode = 1
	managed.mu.Unlock()

	require.Equal(t, "queued", snapshot.Metadata["state"])
	require.Equal(t, 1, snapshot.Metadata["nested"].(map[string]interface{})["attempt"])
	require.Equal(t, "pending", snapshot.Metadata["items"].([]interface{})[0].(map[string]interface{})["status"])
	require.Equal(t, wantStartedAt, *snapshot.StartedAt)
	require.Equal(t, wantFinishedAt, *snapshot.FinishedAt)
	require.Equal(t, 0, *snapshot.ExitCode)
}

func TestPendingCandidatesDoNotChargeRecoveryBackoffAgainstExecutionCapacity(t *testing.T) {
	manager := &Manager{
		maxConcurrentJobs: 1,
		jobs: map[string]*managedJob{
			"recovering": {
				info: Job{
					ID:     "recovering",
					Status: StatusRunning,
					Metadata: map[string]interface{}{
						backgroundMetaNextRecoveryAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano),
					},
				},
				scheduled: true,
			},
			"pending": {
				info: Job{ID: "pending", Status: StatusPending},
			},
		},
	}

	capacity, pending := manager.pendingCandidates()
	require.Equal(t, 1, capacity)
	require.Len(t, pending, 1)
	require.Equal(t, "pending", pending[0].info.ID)
}

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

func TestManagerStartupInterruptsPendingAndOrphansDeadRunningJobs(t *testing.T) {
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

	// Startup must not resurrect pending work whose owning process is gone:
	// the record becomes a terminal "interrupted" job (2026-09-28).
	interrupted, err := manager.GetJob(ctx, pendingJob.ID)
	require.NoError(t, err)
	require.NotNil(t, interrupted)
	require.Equal(t, StatusInterrupted, interrupted.Status)
	require.Contains(t, interrupted.Message, "not auto-resumed")

	pendingEvents, err := manager.ListEvents(ctx, pendingJob.ID, 0, 0)
	require.NoError(t, err)
	pendingTypes := eventTypes(pendingEvents)
	require.Contains(t, pendingTypes, "recovered_pending_ignored")
	require.NotContains(t, pendingTypes, "recovered_queued")
	require.NotContains(t, pendingTypes, "process_created")

	recoveredRunning, err := manager.GetJob(ctx, runningJob.ID)
	require.NoError(t, err)
	require.NotNil(t, recoveredRunning)
	require.Equal(t, StatusOrphaned, recoveredRunning.Status)
	require.Contains(t, recoveredRunning.Message, "restarted before job")

	runningEvents, err := manager.ListEvents(ctx, runningJob.ID, 0, 0)
	require.NoError(t, err)
	runningTypes := eventTypes(runningEvents)
	require.Contains(t, runningTypes, "orphaned")
	require.NotContains(t, runningTypes, "recovered_requeued")
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

func TestManagerOrphansDeadRunningRerunJobWithoutRequeue(t *testing.T) {
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
		StorePath:               storePath,
		LogDir:                  logDir,
		MaxConcurrentJobs:       1,
		RecoveryBackoffSchedule: []time.Duration{time.Millisecond},
	})
	defer func() {
		require.NoError(t, manager.Close())
	}()

	// Rerun policy no longer re-queues on startup: a running record whose
	// process is gone becomes terminal "orphaned" instead (2026-09-28).
	recovered, err := manager.GetJob(ctx, runningJob.ID)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, StatusOrphaned, recovered.Status)
	require.Contains(t, recovered.Message, "restarted before job")
	require.Equal(t, RestartPolicyRerun, recovered.RestartPolicy)

	events, err := manager.ListEvents(ctx, runningJob.ID, 0, 0)
	require.NoError(t, err)
	recoveredTypes := eventTypes(events)
	require.Contains(t, recoveredTypes, "orphaned")
	require.NotContains(t, recoveredTypes, "recovered_requeued")
	require.NotContains(t, recoveredTypes, "process_created")

	output, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: runningJob.ID, Offset: 0})
	require.NoError(t, err)
	require.Contains(t, output.Output, "partial-output")
	require.NotContains(t, output.Output, "rerun")
}

func TestManagerRecoverPendingOnStartKeepsLegacyRequeue(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	require.NoError(t, store.SaveJob(ctx, Job{
		ID:        "job_pending_optin",
		SessionID: "session-1",
		Kind:      "shell",
		Command:   shellEchoCommand("pending-optin"),
		Status:    StatusPending,
		CreatedAt: time.Now().Add(-time.Second).UTC(),
		LogPath:   filepath.Join(logDir, "job_pending_optin.log"),
		Metadata:  map[string]interface{}{"timeout_sec": 15},
	}))
	require.NoError(t, store.Close())

	manager := NewManager(Config{
		StorePath:             storePath,
		LogDir:                logDir,
		MaxConcurrentJobs:     1,
		RecoverPendingOnStart: true,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	require.NoError(t, waitForJobStatus(ctx, manager, "job_pending_optin", StatusCompleted, backgroundTestTimeout(20*time.Second)))
	events, err := manager.ListEvents(ctx, "job_pending_optin", 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(events), "recovered_queued")
}

func TestCancelJobThroughStoreSurvivesStaleWriter(t *testing.T) {
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
	defer func() { require.NoError(t, manager.Close()) }()

	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:        "job_stale_writer",
			SessionID: "session-stale",
			Kind:      "shell",
			Command:   shellEchoCommand("stale"),
			Status:    StatusRunning,
			CreatedAt: time.Now().Add(-time.Minute).UTC(),
		},
		output: newOutputBuffer(1024),
	}
	require.NoError(t, manager.store.SaveJob(ctx, managed.info))
	manager.mu.Lock()
	manager.jobs[managed.info.ID] = managed
	manager.mu.Unlock()

	// Simulate another instance cancelling the job through the shared store.
	stored, err := manager.store.GetJob(ctx, managed.info.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	finishedAt := time.Now().UTC()
	exitCode := -1
	stored.Status = StatusCancelled
	stored.Message = "cancelled"
	stored.ExitCode = &exitCode
	stored.FinishedAt = &finishedAt
	updated, err := manager.updateStoredJobCAS(ctx, *stored, stored.StateVersion)
	require.NoError(t, err)
	require.True(t, updated)

	// The stale writer must not overwrite the terminal state, nor record a
	// bogus terminal event.
	manager.orphanJob(managed, "stale watchdog write")

	final, err := manager.store.GetJob(ctx, managed.info.ID)
	require.NoError(t, err)
	require.NotNil(t, final)
	require.Equal(t, StatusCancelled, final.Status)

	snapshot := managed.snapshot()
	require.NotNil(t, snapshot)
	require.Equal(t, StatusCancelled, snapshot.Status)

	events, err := manager.ListEvents(ctx, managed.info.ID, 0, 0)
	require.NoError(t, err)
	require.NotContains(t, eventTypes(events), "orphaned")
}

func TestCancelJobFallsBackToStoreWhenNotInMemory(t *testing.T) {
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
	defer func() { require.NoError(t, manager.Close()) }()

	// A job created by another runtime instance is visible in the store but not
	// in this manager's memory; CancelJob must fall back to the store instead of
	// reporting JOB_NOT_FOUND (2026-09-28).
	stale := Job{
		ID:        "job_store_only",
		SessionID: "session-store-only",
		Kind:      "shell",
		Command:   shellDelayCommand(time.Minute, "store-only"),
		Status:    StatusRunning,
		CreatedAt: time.Now().Add(-time.Minute).UTC(),
		LogPath:   filepath.Join(logDir, "job_store_only.log"),
	}
	require.NoError(t, manager.store.SaveJob(ctx, stale))

	cancelled, err := manager.CancelJob(ctx, stale.ID)
	require.NoError(t, err)
	require.NotNil(t, cancelled)
	require.Equal(t, StatusCancelled, cancelled.Status)

	final, err := manager.store.GetJob(ctx, stale.ID)
	require.NoError(t, err)
	require.NotNil(t, final)
	require.Equal(t, StatusCancelled, final.Status)
}

func TestManagerSkipsPendingJobOwnedByLivePeer(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	lease := time.Now().Add(time.Minute).UTC()
	require.NoError(t, store.SaveJob(ctx, Job{
		ID:              "job_peer",
		SessionID:       "session-peer",
		Kind:            "shell",
		Command:         shellEchoCommand("peer"),
		Status:          StatusPending,
		CreatedAt:       time.Now().Add(-time.Second).UTC(),
		LogPath:         filepath.Join(logDir, "job_peer.log"),
		OwnerInstanceID: "inst_peer",
		LeaseExpiresAt:  &lease,
		Metadata:        map[string]interface{}{"timeout_sec": 15},
	}))
	require.NoError(t, store.Close())

	manager := NewManager(Config{StorePath: storePath, LogDir: logDir, MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()

	// A live peer still owns this job: it must not be recovered, dispatched or
	// rewritten here (2026-09-28).
	job, err := manager.GetJob(ctx, "job_peer")
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusPending, job.Status)
	require.Equal(t, "inst_peer", job.OwnerInstanceID)

	events, err := manager.ListEvents(ctx, "job_peer", 0, 0)
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestManagerInterruptsPendingJobWithExpiredLease(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "background.db")
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	expiredLease := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, store.SaveJob(ctx, Job{
		ID:              "job_dead_owner",
		SessionID:       "session-dead",
		Kind:            "shell",
		Command:         shellEchoCommand("dead-owner"),
		Status:          StatusPending,
		CreatedAt:       time.Now().Add(-time.Minute).UTC(),
		LogPath:         filepath.Join(logDir, "job_dead_owner.log"),
		OwnerInstanceID: "inst_dead",
		LeaseExpiresAt:  &expiredLease,
		Metadata:        map[string]interface{}{"timeout_sec": 15},
	}))
	require.NoError(t, store.Close())

	manager := NewManager(Config{StorePath: storePath, LogDir: logDir, MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()

	job, err := manager.GetJob(ctx, "job_dead_owner")
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusInterrupted, job.Status)

	events, err := manager.ListEvents(ctx, "job_dead_owner", 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(events), "recovered_pending_ignored")
}

func TestManagerExpiresQueuedJobPastDeadline(t *testing.T) {
	// 队列 deadline 自提交时刻起算，包含「提交 → dispatcher 认领 → 进程启动」
	// 的派发链延迟。150ms 在冷启动/慢盘/杀软扫描新库文件（Windows）环境下会
	// 稳定误杀**首个** job（last status=expired, "never dispatched"），而作者
	// 的 Linux 机器上派发链短于此窗口故全绿。1s 仍远小于 10s 观察窗口，
	// 「排队超 deadline → expired」的被测语义不变；生产默认 QueueTimeout 30m
	// 远大于派发链，不受此竞态影响（见 manager.go expireOverdueQueuedJobs 的
	// scheduled 豁免注释）。
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
		MonitorInterval:   50 * time.Millisecond,
		QueueTimeout:      time.Second,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	blocker, err := manager.SubmitShell(ctx, "session-deadline", BackgroundTaskArgs{
		Command: shellDelayCommand(3*time.Second, "blocker"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, blocker.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	queued, err := manager.SubmitShell(ctx, "session-deadline", BackgroundTaskArgs{
		Command: shellEchoCommand("never-runs"),
	})
	require.NoError(t, err)
	require.NotNil(t, queued.DeadlineAt)
	require.NoError(t, waitForJobStatus(ctx, manager, queued.ID, StatusExpired, backgroundTestTimeout(10*time.Second)))

	expired, err := manager.GetJob(ctx, queued.ID)
	require.NoError(t, err)
	require.NotNil(t, expired)
	require.Equal(t, StatusExpired, expired.Status)
	require.Contains(t, expired.Message, "queue deadline exceeded")

	events, err := manager.ListEvents(ctx, queued.ID, 0, 0)
	require.NoError(t, err)
	expiredTypes := eventTypes(events)
	require.Contains(t, expiredTypes, "expired")
	require.NotContains(t, expiredTypes, "process_created")

	_, _ = manager.CancelJob(ctx, blocker.ID)
}

func TestDispatchSkipsJobCancelledInStore(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:              "job_dispatch_cancel",
			SessionID:       "session-dispatch",
			Kind:            "shell",
			Command:         shellEchoCommand("never"),
			Status:          StatusPending,
			CreatedAt:       time.Now().Add(-time.Second).UTC(),
			OwnerInstanceID: manager.instanceID,
		},
		output: newOutputBuffer(1024),
	}
	require.NoError(t, manager.store.SaveJob(ctx, managed.info))
	manager.mu.Lock()
	manager.jobs[managed.info.ID] = managed
	manager.mu.Unlock()

	// Another instance cancels the job in the shared store after it was queued
	// locally; the dispatcher must re-read before starting it (2026-09-28).
	stored, err := manager.store.GetJob(ctx, managed.info.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	stored.Status = StatusCancelled
	updated, err := manager.updateStoredJobCAS(ctx, *stored, stored.StateVersion)
	require.NoError(t, err)
	require.True(t, updated)

	manager.dispatchPending()

	snapshot := managed.snapshot()
	require.NotNil(t, snapshot)
	require.Equal(t, StatusCancelled, snapshot.Status)
	managed.mu.RLock()
	scheduled := managed.scheduled
	managed.mu.RUnlock()
	require.False(t, scheduled)

	events, err := manager.ListEvents(ctx, managed.info.ID, 0, 0)
	require.NoError(t, err)
	require.NotContains(t, eventTypes(events), "process_created")
}

func TestPauseAndResumeQueuedJob(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
		MonitorInterval:   50 * time.Millisecond,
		QueueTimeout:      10 * time.Second,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	blocker, err := manager.SubmitShell(ctx, "session-pause", BackgroundTaskArgs{
		Command: shellDelayCommand(3*time.Second, "blocker"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, blocker.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	queued, err := manager.SubmitShell(ctx, "session-pause", BackgroundTaskArgs{
		Command: shellEchoCommand("held"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, queued.Status)
	require.NotNil(t, queued.DeadlineAt)

	paused, err := manager.PauseJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPaused, paused.Status)
	require.Nil(t, paused.DeadlineAt, "a paused job must not keep its queue deadline")

	// The slot frees while the job is paused: it must stay held, not dispatch.
	time.Sleep(300 * time.Millisecond)
	held, err := manager.GetJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPaused, held.Status)

	resumed, err := manager.ResumeJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, resumed.Status)
	require.NotNil(t, resumed.DeadlineAt, "resume starts a fresh queue window")
	require.NoError(t, waitForJobStatus(ctx, manager, queued.ID, StatusCompleted, backgroundTestTimeout(15*time.Second)))

	events, err := manager.ListEvents(ctx, queued.ID, 0, 0)
	require.NoError(t, err)
	types := eventTypes(events)
	require.Contains(t, types, "paused")
	require.Contains(t, types, "resumed")
}

func TestPauseRunningJobIsRejected(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()

	job, err := manager.SubmitShell(ctx, "session-pause-running", BackgroundTaskArgs{
		Command: shellDelayCommand(3*time.Second, "running"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	_, err = manager.PauseJob(ctx, job.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot pause")

	current, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, current.Status)
	_, _ = manager.CancelJob(ctx, job.ID)
}

func TestAbandonPausedJob(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	blocker, err := manager.SubmitShell(ctx, "session-abandon", BackgroundTaskArgs{
		Command: shellDelayCommand(3*time.Second, "blocker"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, blocker.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	queued, err := manager.SubmitShell(ctx, "session-abandon", BackgroundTaskArgs{
		Command: shellEchoCommand("never"),
	})
	require.NoError(t, err)
	_, err = manager.PauseJob(ctx, queued.ID)
	require.NoError(t, err)

	abandoned, err := manager.AbandonJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusAbandoned, abandoned.Status)
	require.NotNil(t, abandoned.FinishedAt)
	require.True(t, IsTerminalStatus(abandoned.Status))

	_, err = manager.ResumeJob(ctx, queued.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already finished")

	events, err := manager.ListEvents(ctx, queued.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(events), "abandoned")
	_, _ = manager.CancelJob(ctx, blocker.ID)
}

func TestRequeueTerminalJobCreatesNewJob(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	original, err := manager.SubmitShell(ctx, "session-requeue", BackgroundTaskArgs{
		Command: shellEchoCommand("first"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, original.ID, StatusCompleted, backgroundTestTimeout(15*time.Second)))

	requeued, err := manager.RequeueJob(ctx, original.ID)
	require.NoError(t, err)
	require.NotNil(t, requeued)
	require.NotEqual(t, original.ID, requeued.ID)
	require.Equal(t, original.SessionID, requeued.SessionID)

	newJob, err := manager.GetJob(ctx, requeued.ID)
	require.NoError(t, err)
	require.NotNil(t, newJob)
	from, ok := stringMetadataValue(newJob.Metadata, "requeued_from")
	require.True(t, ok)
	require.Equal(t, original.ID, from)

	oldEvents, err := manager.ListEvents(ctx, original.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(oldEvents), "requeued")
	newEvents, err := manager.ListEvents(ctx, requeued.ID, 0, 0)
	require.NoError(t, err)
	require.Contains(t, eventTypes(newEvents), "requeued_from")
	require.NoError(t, waitForJobStatus(ctx, manager, requeued.ID, StatusCompleted, backgroundTestTimeout(15*time.Second)))
}

func TestDispatchSkipsJobPausedByPeer(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:              "job_peer_paused",
			SessionID:       "session-peer-paused",
			Kind:            "shell",
			Command:         shellEchoCommand("never"),
			Status:          StatusPending,
			CreatedAt:       time.Now().Add(-time.Second).UTC(),
			OwnerInstanceID: manager.instanceID,
		},
		output: newOutputBuffer(1024),
	}
	require.NoError(t, manager.store.SaveJob(ctx, managed.info))
	manager.mu.Lock()
	manager.jobs[managed.info.ID] = managed
	manager.mu.Unlock()

	stored, err := manager.store.GetJob(ctx, managed.info.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	stored.Status = StatusPaused
	stored.DeadlineAt = nil
	updated, err := manager.updateStoredJobCAS(ctx, *stored, stored.StateVersion)
	require.NoError(t, err)
	require.True(t, updated)

	manager.dispatchPending()

	snapshot := managed.snapshot()
	require.NotNil(t, snapshot)
	require.Equal(t, StatusPaused, snapshot.Status)
	events, err := manager.ListEvents(ctx, managed.info.ID, 0, 0)
	require.NoError(t, err)
	require.NotContains(t, eventTypes(events), "process_created")
}

func TestReconcilePausedJobAfterPeerResume(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
		MonitorInterval:   50 * time.Millisecond,
		QueueTimeout:      10 * time.Second,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	blocker, err := manager.SubmitShell(ctx, "session-reconcile", BackgroundTaskArgs{
		Command: shellDelayCommand(3*time.Second, "blocker"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, blocker.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	queued, err := manager.SubmitShell(ctx, "session-reconcile", BackgroundTaskArgs{
		Command: shellEchoCommand("resumed-by-peer"),
	})
	require.NoError(t, err)
	_, err = manager.PauseJob(ctx, queued.ID)
	require.NoError(t, err)

	// A peer resumes the job straight in the store: the owner must converge on
	// the next watchdog tick instead of holding the job forever (P3).
	stored, err := manager.store.GetJob(ctx, queued.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	queuedAt := time.Now().UTC()
	deadline := queuedAt.Add(10 * time.Second)
	stored.Status = StatusPending
	stored.QueuedAt = &queuedAt
	stored.DeadlineAt = &deadline
	updated, err := manager.updateStoredJobCAS(ctx, *stored, stored.StateVersion)
	require.NoError(t, err)
	require.True(t, updated)

	manager.reconcilePausedJobs()

	current, err := manager.GetJob(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, current.Status)
	require.NoError(t, waitForJobStatus(ctx, manager, queued.ID, StatusCompleted, backgroundTestTimeout(15*time.Second)))
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

	if err := waitForJobStatus(ctx, recoveredManager, job.ID, StatusCompleted, backgroundTestTimeout(20*time.Second)); err != nil {
		recovered, _ := recoveredManager.GetJob(ctx, job.ID)
		if recovered != nil {
			logData, _ := os.ReadFile(recovered.LogPath)
			statusPath, _ := stringMetadataValue(recovered.Metadata, backgroundMetaStatusPath)
			statusData, _ := os.ReadFile(statusPath)
			runnerPath, _ := stringMetadataValue(recovered.Metadata, backgroundMetaRunnerPath)
			runnerData, _ := os.ReadFile(runnerPath)
			t.Logf("recovery diagnostics: metadata=%v log=%q status=%q runner=%q", recovered.Metadata, string(logData), string(statusData), string(runnerData))
		}
		require.NoError(t, err)
	}

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

func TestDetachedRunnerRefreshesHeartbeatWhileCommandIsQuiet(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(tempDir, "background.db"),
		LogDir:            filepath.Join(tempDir, "logs"),
		MaxConcurrentJobs: 1,
		HeartbeatTimeout:  10 * time.Second,
	})
	defer func() { require.NoError(t, manager.Close()) }()

	job, err := manager.SubmitShell(ctx, "session-heartbeat", BackgroundTaskArgs{
		Command: shellDelayCommand(4*time.Second, "heartbeat-finished"),
	})
	require.NoError(t, err)
	defer func() {
		current, getErr := manager.GetJob(context.Background(), job.ID)
		if getErr == nil && current != nil && !isTerminalStatus(current.Status) {
			_, _ = manager.CancelJob(context.Background(), job.ID)
		}
	}()
	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	running, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	heartbeatPath, ok := stringMetadataValue(running.Metadata, backgroundMetaHeartbeatPath)
	require.True(t, ok)
	var first []byte
	heartbeatDeadline := time.Now().Add(backgroundTestTimeout(5 * time.Second))
	for time.Now().Before(heartbeatDeadline) {
		first, err = os.ReadFile(heartbeatPath)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.NoError(t, err)
	require.NotEmpty(t, first)

	heartbeatDeadline = time.Now().Add(backgroundTestTimeout(3 * time.Second))
	advanced := false
	for time.Now().Before(heartbeatDeadline) {
		second, readErr := os.ReadFile(heartbeatPath)
		if readErr == nil && string(second) != string(first) {
			advanced = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.True(t, advanced, "heartbeat content did not advance")
	_, err = manager.CancelJob(ctx, job.ID)
	require.NoError(t, err)
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

func shellExitCommand(code int) string {
	return fmt.Sprintf("exit %d", code)
}

func TestManagerCompletesShellJobWithNonZeroExit(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()

	job, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{
		Command: shellExitCommand(3),
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusCompleted, backgroundTestTimeout(15*time.Second)))

	got, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, StatusCompleted, got.Status)
	require.NotNil(t, got.ExitCode)
	require.Equal(t, 3, *got.ExitCode)
	require.Equal(t, true, got.Metadata["non_zero_exit"])
	_, hasErrorCode := got.Metadata["error_code"]
	require.False(t, hasErrorCode)

	output, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: job.ID})
	require.NoError(t, err)
	require.Equal(t, string(StatusCompleted), output.Status)
	require.NotNil(t, output.ExitCode)
	require.Equal(t, 3, *output.ExitCode)
	require.Empty(t, output.ErrorCode)
}

func sanitizeTestLabel(label string) string {
	if label == "" {
		return "job"
	}
	return label
}

func TestPanicInRunJobFailsJobAndReleasesSlot(t *testing.T) {
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	defer manager.Close()
	ctx := context.Background()

	original := manager.currentRunJobImpl()
	manager.setRunJobImpl(func(m *Manager, managed *managedJob) { panic("boom") })
	defer manager.setRunJobImpl(original)

	job, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{Command: shellEchoCommand("panic")})
	require.NoError(t, err)
	require.NotNil(t, job)

	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusFailed, backgroundTestTimeout(5*time.Second)))

	current, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Contains(t, current.Message, "panicked")
	require.Equal(t, string(runtimeerrors.ErrToolBrokerFailure), current.Metadata["error_code"])

	// The panicked goroutine must not leak its scheduling slot.
	manager.setRunJobImpl(original)
	second, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{Command: shellEchoCommand("after-panic")})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, second.ID, StatusCompleted, backgroundTestTimeout(5*time.Second)))
}

func TestWatchdogReclaimsStuckScheduledJob(t *testing.T) {
	manager := NewManager(Config{
		MaxConcurrentJobs: 1,
		MonitorInterval:   20 * time.Millisecond,
		// The watchdog treats a scheduled-but-not-running job as stuck after
		// HeartbeatTimeout. The blocked fake runner is reclaimed immediately,
		// but the follow-up job uses the real runner and a cold shell spawn
		// (several hundred milliseconds on Windows) must not be mistaken for
		// a stuck scheduler.
		HeartbeatTimeout: 2 * time.Second,
	})
	defer manager.Close()
	ctx := context.Background()

	original := manager.currentRunJobImpl()
	// Simulate a worker goroutine that never starts the process: it blocks
	// until the watchdog cancels the job context.
	manager.setRunJobImpl(func(m *Manager, managed *managedJob) { <-managed.ctx.Done() })
	defer manager.setRunJobImpl(original)

	job, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{Command: shellEchoCommand("stuck")})
	require.NoError(t, err)
	require.NotNil(t, job)

	require.NoError(t, waitForJobStatus(ctx, manager, job.ID, StatusFailed, backgroundTestTimeout(5*time.Second)))

	current, err := manager.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Contains(t, current.Message, "scheduler stuck")
	require.Equal(t, string(runtimeerrors.ErrToolBrokerFailure), current.Metadata["error_code"])

	// The reclaimed slot accepts new work.
	manager.setRunJobImpl(original)
	second, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{Command: shellEchoCommand("after-stuck")})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, second.ID, StatusCompleted, backgroundTestTimeout(5*time.Second)))
}

func TestReadOutputQueueDiagnostics(t *testing.T) {
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	defer manager.Close()
	ctx := context.Background()

	blocking, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{
		Command: shellDelayCommand(60*time.Second, "blocking"),
	})
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, blocking.ID, StatusRunning, backgroundTestTimeout(10*time.Second)))

	queued, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{Command: shellEchoCommand("queued")})
	require.NoError(t, err)
	tail, err := manager.SubmitShell(ctx, "session-1", BackgroundTaskArgs{Command: shellEchoCommand("tail")})
	require.NoError(t, err)

	// The only slot is occupied: both new jobs must be pending and diagnosable.
	queuedOut, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: queued.ID})
	require.NoError(t, err)
	require.Equal(t, string(StatusPending), queuedOut.Status)
	require.Equal(t, 1, queuedOut.QueuePosition)
	require.Equal(t, 1, queuedOut.ActiveJobs)
	require.Equal(t, 1, queuedOut.MaxConcurrent)
	require.Equal(t, "saturated", queuedOut.SchedulerState)
	require.Contains(t, queuedOut.NextAction, "saturated")

	tailOut, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: tail.ID})
	require.NoError(t, err)
	require.Equal(t, string(StatusPending), tailOut.Status)
	require.Equal(t, 2, tailOut.QueuePosition)
	// Saturation is the primary signal even for queued jobs behind it.
	require.Equal(t, "saturated", tailOut.SchedulerState)
	require.Contains(t, tailOut.NextAction, "saturated")

	// Cancel the blocker: the queue drains in FIFO order.
	_, err = manager.CancelJob(ctx, blocking.ID)
	require.NoError(t, err)
	require.NoError(t, waitForJobStatus(ctx, manager, queued.ID, StatusCompleted, backgroundTestTimeout(10*time.Second)))
	require.NoError(t, waitForJobStatus(ctx, manager, tail.ID, StatusCompleted, backgroundTestTimeout(10*time.Second)))

	doneOut, err := manager.ReadOutput(ctx, TaskOutputArgs{JobID: queued.ID})
	require.NoError(t, err)
	require.Equal(t, string(StatusCompleted), doneOut.Status)
	require.Zero(t, doneOut.QueuePosition)
	require.Empty(t, doneOut.SchedulerState)
	require.Empty(t, doneOut.NextAction)
}

func TestJobQueueDiagnosticsOrdering(t *testing.T) {
	// Bare manager: no live dispatcher/watchdog racing the synthetic jobs.
	manager := &Manager{
		jobs:              make(map[string]*managedJob),
		maxConcurrentJobs: 3,
	}

	base := time.Now().UTC()
	mk := func(id string, status JobStatus, scheduled bool, priority int) *managedJob {
		return &managedJob{
			ctx:       context.Background(),
			info:      Job{ID: id, Status: status, Priority: priority, CreatedAt: base},
			scheduled: scheduled,
			output:    newOutputBuffer(1024),
		}
	}
	manager.mu.Lock()
	manager.jobs["running"] = mk("running", StatusRunning, false, 0)
	manager.jobs["b"] = mk("b", StatusPending, false, 0)
	manager.jobs["a"] = mk("a", StatusPending, false, 0)
	manager.jobs["high"] = mk("high", StatusPending, false, 5)
	manager.mu.Unlock()

	pos, active, max := manager.jobQueueDiagnostics("high")
	require.Equal(t, 1, pos)
	require.Equal(t, 1, active)
	require.Equal(t, 3, max)

	pos, _, _ = manager.jobQueueDiagnostics("a")
	require.Equal(t, 2, pos)
	pos, _, _ = manager.jobQueueDiagnostics("b")
	require.Equal(t, 3, pos)
	pos, _, _ = manager.jobQueueDiagnostics("running")
	require.Zero(t, pos)
}
