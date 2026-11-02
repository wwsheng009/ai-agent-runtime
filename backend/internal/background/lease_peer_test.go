package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLivePeerJobIsNotOrphanedBeforeFirstHeartbeat locks the initial lease
// publication. Two runtime instances can share one jobs.root (the remote-tools
// default is per-user). If the row is saved without a lease and the second
// instance starts before the first 10s heartbeat, startup recovery used to
// treat the live job as orphaned.
func TestLivePeerJobIsNotOrphanedBeforeFirstHeartbeat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "background.db")
	logDir := filepath.Join(dir, "logs")

	owner := NewManager(Config{
		StorePath:         storePath,
		LogDir:            logDir,
		MaxConcurrentJobs: 1,
		DisableDetached:   true,
	})
	defer func() { require.NoError(t, owner.Close()) }()

	job, err := owner.SubmitShell(ctx, "session-live-peer", BackgroundTaskArgs{
		Command: "sleep 30",
	})
	require.NoError(t, err)

	// Wait until the durable row is running; recovery only considers
	// pending/running rows.
	require.Eventually(t, func() bool {
		stored, getErr := owner.store.GetJob(ctx, job.ID)
		return getErr == nil && stored != nil && stored.Status == StatusRunning
	}, backgroundTestTimeout(10*time.Second), 50*time.Millisecond)
	stored, err := owner.store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.LeaseExpiresAt, "job row must carry a lease from the moment it is persisted")
	require.True(t, time.Now().UTC().Before(*stored.LeaseExpiresAt))

	peer := NewManager(Config{
		StorePath:         storePath,
		LogDir:            logDir,
		MaxConcurrentJobs: 1,
		DisableDetached:   true,
	})
	defer func() { require.NoError(t, peer.Close()) }()

	observed, err := peer.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, observed)
	require.Equal(t, StatusRunning, observed.Status,
		"a live peer's job must not be rewritten as orphaned/interrupted")
	require.Equal(t, owner.instanceID, observed.OwnerInstanceID)

	_, _ = owner.CancelJob(ctx, job.ID)
}
