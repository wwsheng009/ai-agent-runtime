package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestLeaseManagerReclaimExpiredTasks(t *testing.T) {
	ctx := context.Background()
	store, teamID, taskID, claim := setupFencedTask(t, false)
	leaseUntil := time.Now().UTC().Add(-1 * time.Minute)
	_, err := NewAgentControlTaskRegistry(store).RenewAgentControlTaskLease(ctx, agentcontrol.TaskLeaseRenewRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		LeaseUntil:   leaseUntil,
		FencingToken: claim.FencingToken,
	})
	require.NoError(t, err)

	manager := NewLeaseManager(store, nil)
	manager.ReclaimGrace = 10 * time.Millisecond
	reclaimed, err := manager.ReclaimExpiredTasks(ctx, teamID, time.Now().UTC(), 0, false)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)

	// Phase 1: the expired task is marked reclaim_pending but keeps its
	// assignee and lease so a healthy runner can win its lease back.
	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReclaimPending, updated.Status)
	require.NotNil(t, updated.Assignee)
	require.NotNil(t, updated.LeaseUntil)

	// Phase 2: after the reclaim grace window the task returns to ready.
	time.Sleep(2 * manager.ReclaimGrace)
	reclaimed, err = manager.ReclaimExpiredTasks(ctx, teamID, time.Now().UTC(), 0, false)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)

	updated, err = store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReady, updated.Status)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)
	require.Equal(t, 1, updated.RetryCount)
}

func TestLeaseManagerReclaimGraceProtectsHealthyTasks(t *testing.T) {
	ctx := context.Background()
	store, teamID, taskID, claim := setupFencedTask(t, false)
	leaseUntil := time.Now().UTC().Add(-1 * time.Minute)
	_, err := NewAgentControlTaskRegistry(store).RenewAgentControlTaskLease(ctx, agentcontrol.TaskLeaseRenewRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		LeaseUntil:   leaseUntil,
		FencingToken: claim.FencingToken,
	})
	require.NoError(t, err)

	manager := NewLeaseManager(store, nil)
	// A long grace window means the reclaim is not re-assigned yet even after
	// multiple sweeps: a briefly-locked healthy runner keeps its task.
	manager.ReclaimGrace = time.Hour
	reclaimed, err := manager.ReclaimExpiredTasks(ctx, teamID, time.Now().UTC(), 0, false)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)

	reclaimed, err = manager.ReclaimExpiredTasks(ctx, teamID, time.Now().UTC(), 0, false)
	require.NoError(t, err)
	require.Empty(t, reclaimed, "task still inside reclaim grace window must not be re-queued")

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReclaimPending, updated.Status)
}
