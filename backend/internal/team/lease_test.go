package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLeaseManagerReclaimExpiredTasks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	assignee := "mate-a"
	leaseUntil := time.Now().UTC().Add(-1 * time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "expired",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	manager := NewLeaseManager(store, nil)
	reclaimed, err := manager.ReclaimExpiredTasks(ctx, teamID, time.Now().UTC(), 0, false)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReady, updated.Status)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)
	require.Equal(t, 1, updated.RetryCount)
}
