package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
