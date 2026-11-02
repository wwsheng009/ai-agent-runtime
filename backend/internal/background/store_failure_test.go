package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type failingSaveStore struct {
	Store
}

func (f failingSaveStore) SaveJob(context.Context, Job) error {
	return errors.New("disk full")
}

// TestSubmitShellFailsClosedWhenJobCannotBePersisted locks the remote-target
// invariant: the durable store is the job registry, so returning a job id that
// was never persisted would guarantee a later JOB_NOT_FOUND.
func TestSubmitShellFailsClosedWhenJobCannotBePersisted(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(Config{
		StorePath:         filepath.Join(dir, "background.db"),
		LogDir:            filepath.Join(dir, "logs"),
		MaxConcurrentJobs: 1,
	})
	realStore := manager.store
	require.NotNil(t, realStore)
	manager.store = failingSaveStore{Store: realStore}
	defer func() {
		manager.store = realStore
		require.NoError(t, manager.Close())
	}()

	_, err := manager.SubmitShell(context.Background(), "session-persist", BackgroundTaskArgs{
		Command: shellEchoCommand("never-persisted"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "persist background job")

	manager.mu.RLock()
	remaining := len(manager.jobs)
	manager.mu.RUnlock()
	require.Zero(t, remaining, "a failed persist must not leave a dispatchable in-memory job")

	entries, readErr := os.ReadDir(filepath.Join(dir, "logs"))
	if readErr != nil {
		require.True(t, os.IsNotExist(readErr), "unexpected log dir error: %v", readErr)
		return
	}
	require.Empty(t, entries, "a failed persist must remove the job log it created")
}
