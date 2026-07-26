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
