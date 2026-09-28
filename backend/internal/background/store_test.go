package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewSQLiteStore_PathBackedIsLazyUntilFirstUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "background.sqlite")

	store, err := NewSQLiteStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.False(t, store.Opened())
	require.Equal(t, path, store.Path())
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "lazy open must not create the sqlite file early")

	// Bootstrap-style empty reads must not force the first open.
	jobs, err := store.ListJobs(context.Background(), JobFilter{Status: []JobStatus{StatusPending, StatusRunning}})
	require.NoError(t, err)
	require.Empty(t, jobs)
	require.False(t, store.Opened())
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))

	pruned, err := store.PruneJobs(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, pruned)
	require.False(t, store.Opened())
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))

	require.NoError(t, store.SaveJob(context.Background(), Job{
		ID:        "job_lazy",
		SessionID: "session-lazy",
		Kind:      "shell",
		Status:    StatusPending,
		Command:   "echo lazy",
		CreatedAt: time.Now().UTC(),
	}))
	require.True(t, store.Opened())
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestNewManager_PathBackedStoreStaysLazyOnBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime", "background.sqlite")
	logDir := filepath.Join(dir, "runtime", "background_logs")

	manager := NewManager(Config{
		StorePath: path,
		LogDir:    logDir,
	})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	store, ok := manager.store.(*SQLiteStore)
	require.True(t, ok)
	require.False(t, store.Opened())
	require.Equal(t, path, store.Path())
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "manager bootstrap must not create background.sqlite early")
	_, err = os.Stat(logDir)
	require.True(t, os.IsNotExist(err), "manager bootstrap must not create background_logs early")
}

func TestSQLiteStorePersistsJobsAndEventsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "runtime", "background.sqlite")
	createdAt := time.Date(2026, 5, 12, 8, 30, 0, 0, time.UTC)
	startedAt := createdAt.Add(time.Second)
	finishedAt := createdAt.Add(2 * time.Second)
	exitCode := 0

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)

	job := Job{
		ID:         "job_shared",
		SessionID:  "session-shared",
		Kind:       "shell",
		Command:    "echo shared",
		Cwd:        ".",
		Priority:   9,
		Status:     StatusCompleted,
		Message:    "done",
		CreatedAt:  createdAt,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		ExitCode:   &exitCode,
		LogPath:    filepath.Join(filepath.Dir(storePath), "background_logs", "job_shared.log"),
		Metadata: map[string]interface{}{
			"client":         "runtime-server",
			"restart_policy": string(RestartPolicyRerun),
		},
	}
	require.NoError(t, store.SaveJob(ctx, job))
	require.NoError(t, store.AppendEvent(ctx, job.ID, "running", map[string]interface{}{"worker": "server"}))
	require.NoError(t, store.AppendEvent(ctx, job.ID, "completed", map[string]interface{}{"exit_code": exitCode}))
	require.NoError(t, store.Close())

	reopened, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, reopened.Close())
	}()

	loaded, err := reopened.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "session-shared", loaded.SessionID)
	require.Equal(t, StatusCompleted, loaded.Status)
	require.Equal(t, "done", loaded.Message)
	require.Equal(t, 9, loaded.Priority)
	require.Equal(t, RestartPolicyRerun, loaded.RestartPolicy)
	require.Equal(t, "runtime-server", loaded.Metadata["client"])
	require.NotNil(t, loaded.ExitCode)
	require.Equal(t, exitCode, *loaded.ExitCode)

	jobs, err := reopened.ListJobs(ctx, JobFilter{SessionID: "session-shared", Limit: 10})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, job.ID, jobs[0].ID)

	events, err := reopened.ListEvents(ctx, job.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, int64(1), events[0].Seq)
	require.Equal(t, "running", events[0].Type)
	require.Equal(t, "server", events[0].Payload["worker"])
	require.Equal(t, int64(2), events[1].Seq)
	require.Equal(t, "completed", events[1].Type)
	require.Equal(t, float64(0), events[1].Payload["exit_code"])

	eventsAfterFirst, err := reopened.ListEvents(ctx, job.ID, 1, 10)
	require.NoError(t, err)
	require.Len(t, eventsAfterFirst, 1)
	require.Equal(t, "completed", eventsAfterFirst[0].Type)
}

func TestUpdateJobCASEnforcesVersionAndTerminalGuard(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "runtime", "background.sqlite")

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.SaveJob(ctx, Job{
		ID:        "job_cas",
		SessionID: "session-cas",
		Kind:      "shell",
		Command:   "echo cas",
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}))

	loaded, err := store.GetJob(ctx, "job_cas")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, int64(0), loaded.StateVersion)

	// 1) CAS with the current version lands and bumps the version.
	running := *loaded
	running.Status = StatusRunning
	updated, err := store.UpdateJobCAS(ctx, running, loaded.StateVersion)
	require.NoError(t, err)
	require.True(t, updated)

	// 2) A stale writer carrying the old version is rejected.
	stale := *loaded
	stale.Status = StatusRunning
	updated, err = store.UpdateJobCAS(ctx, stale, loaded.StateVersion)
	require.NoError(t, err)
	require.False(t, updated)

	current, err := store.GetJob(ctx, "job_cas")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, int64(1), current.StateVersion)

	// 3) A legitimate transition to a terminal state lands.
	completed := *current
	completed.Status = StatusCompleted
	updated, err = store.UpdateJobCAS(ctx, completed, current.StateVersion)
	require.NoError(t, err)
	require.True(t, updated)

	// 4) Once terminal, even a write with the right version is absorbed.
	final, err := store.GetJob(ctx, "job_cas")
	require.NoError(t, err)
	require.NotNil(t, final)
	require.Equal(t, StatusCompleted, final.Status)
	overwrite := *final
	overwrite.Status = StatusOrphaned
	updated, err = store.UpdateJobCAS(ctx, overwrite, final.StateVersion)
	require.NoError(t, err)
	require.False(t, updated)

	after, err := store.GetJob(ctx, "job_cas")
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Equal(t, StatusCompleted, after.Status)
	require.Equal(t, final.StateVersion, after.StateVersion)
}

func TestSQLiteStoreRuntimeInstancesAndJobOwnership(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "runtime", "background.sqlite")

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	startedAt := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, store.UpsertRuntimeInstance(ctx, RuntimeInstance{
		ID: "inst_a", PID: os.Getpid(), Host: "host-a", StartedAt: startedAt, State: "running",
	}))

	lease := time.Now().Add(time.Minute).UTC()
	queuedAt := time.Now().UTC()
	deadline := time.Now().Add(30 * time.Minute).UTC()
	require.NoError(t, store.SaveJob(ctx, Job{
		ID: "job_owned", SessionID: "session-owned", Kind: "shell", Command: "echo owned",
		Status: StatusPending, CreatedAt: time.Now().UTC(),
		OwnerInstanceID: "inst_a",
		LeaseExpiresAt:  &lease,
		QueuedAt:        &queuedAt,
		DeadlineAt:      &deadline,
	}))

	loaded, err := store.GetJob(ctx, "job_owned")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "inst_a", loaded.OwnerInstanceID)
	require.NotNil(t, loaded.LeaseExpiresAt)
	require.WithinDuration(t, lease, *loaded.LeaseExpiresAt, time.Second)
	require.NotNil(t, loaded.QueuedAt)
	require.NotNil(t, loaded.DeadlineAt)
	require.WithinDuration(t, deadline, *loaded.DeadlineAt, time.Second)

	renewed := time.Now().Add(2 * time.Minute).UTC()
	affected, err := store.RenewJobLeases(ctx, "inst_a", renewed)
	require.NoError(t, err)
	require.EqualValues(t, 1, affected)
	reloaded, err := store.GetJob(ctx, "job_owned")
	require.NoError(t, err)
	require.NotNil(t, reloaded.LeaseExpiresAt)
	require.WithinDuration(t, renewed, *reloaded.LeaseExpiresAt, time.Second)
	require.Equal(t, int64(0), reloaded.StateVersion, "lease renewal must not bump state_version")

	affected, err = store.ReleaseJobLeases(ctx, "inst_a", time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, affected)
	released, err := store.GetJob(ctx, "job_owned")
	require.NoError(t, err)
	require.NotNil(t, released.LeaseExpiresAt)
	require.True(t, released.LeaseExpiresAt.Before(time.Now().UTC()), "released lease must read as expired")

	instances, err := store.ListRuntimeInstances(ctx)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "inst_a", instances[0].ID)
	require.Equal(t, os.Getpid(), instances[0].PID)
	require.Equal(t, "running", instances[0].State)

	require.NoError(t, store.MarkRuntimeInstanceStopped(ctx, "inst_a", time.Now().UTC()))
	instances, err = store.ListRuntimeInstances(ctx)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "stopped", instances[0].State)
}
