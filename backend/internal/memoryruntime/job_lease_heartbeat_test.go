package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestWithJobLeaseHeartbeatCancelsWhenClaimIsReassigned(t *testing.T) {
	ctx := context.Background()
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	job, err := store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "session:heartbeat-reassign:hash",
		ProjectKey: store.ProjectKey(), MaxAttempts: 3,
	})
	require.NoError(t, err)
	claimed, err := store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: "worker-a", Kind: job.Kind, JobKey: job.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1, Now: time.Now().UTC(),
		Lease: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	err = withJobLeaseHeartbeat(ctx, store, 300*time.Millisecond, claimed[0],
		func(actionCtx context.Context) error {
			time.Sleep(40 * time.Millisecond)
			reclaimed, claimErr := store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
				WorkerID: "worker-b", Kind: job.Kind, JobKey: job.JobKey,
				ProjectKey: store.ProjectKey(), Limit: 1,
				Now: time.Now().UTC().Add(2 * time.Minute), Lease: time.Minute,
			})
			require.NoError(t, claimErr)
			require.Len(t, reclaimed, 1)
			select {
			case <-actionCtx.Done():
				return actionCtx.Err()
			case <-time.After(2 * time.Second):
				return nil
			}
		})
	require.ErrorIs(t, err, memorystore.ErrJobLeaseLost)
}
