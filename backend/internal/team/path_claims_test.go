package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreDeleteExpiredPathClaims(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	expired := time.Now().UTC().Add(-2 * time.Minute)
	active := time.Now().UTC().Add(2 * time.Minute)

	err = store.CreatePathClaims(ctx, []PathClaim{
		{
			TeamID:       teamID,
			TaskID:       "task-expired",
			OwnerAgentID: "mate-a",
			Path:         "a.txt",
			Mode:         PathClaimWrite,
			LeaseUntil:   expired,
		},
		{
			TeamID:       teamID,
			TaskID:       "task-active",
			OwnerAgentID: "mate-b",
			Path:         "b.txt",
			Mode:         PathClaimRead,
			LeaseUntil:   active,
		},
	})
	require.NoError(t, err)

	deleted, err := store.DeleteExpiredPathClaims(ctx, teamID, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)

	claims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "task-active", claims[0].TaskID)
}

func TestSQLiteStoreClaimTaskWithPathClaimsSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "write file",
		Status:     TaskStatusReady,
		WritePaths: []string{"src/file.txt"},
	})
	require.NoError(t, err)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	claimed, err := store.ClaimTaskWithPathClaims(ctx, *task, "mate-a", time.Now().UTC().Add(5*time.Minute), "workspace")
	require.NoError(t, err)
	require.True(t, claimed)

	updatedTask, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updatedTask)
	require.Equal(t, TaskStatusRunning, updatedTask.Status)
	require.NotNil(t, updatedTask.Assignee)
	require.Equal(t, "mate-a", *updatedTask.Assignee)

	claims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "workspace/src/file.txt", claims[0].Path)
	require.Equal(t, PathClaimWrite, claims[0].Mode)

	mate, err := store.GetTeammate(ctx, "mate-a")
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, TeammateStateBusy, mate.State)
}

func TestSQLiteStoreClaimTaskWithPathClaimsConflictLeavesTaskReady(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateBusy})
	require.NoError(t, err)

	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "conflicting write",
		Status:     TaskStatusReady,
		WritePaths: []string{"src"},
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	err = store.CreatePathClaims(ctx, []PathClaim{
		{
			TeamID:       teamID,
			TaskID:       "task-existing",
			OwnerAgentID: "mate-b",
			Path:         "workspace/src/file.txt",
			Mode:         PathClaimWrite,
			LeaseUntil:   time.Now().UTC().Add(5 * time.Minute),
		},
	})
	require.NoError(t, err)

	claimed, err := store.ClaimTaskWithPathClaims(ctx, *task, "mate-a", time.Now().UTC().Add(5*time.Minute), "workspace")
	require.NoError(t, err)
	require.False(t, claimed)

	updatedTask, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updatedTask)
	require.Equal(t, TaskStatusReady, updatedTask.Status)
	require.Nil(t, updatedTask.Assignee)

	claims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "task-existing", claims[0].TaskID)

	mate, err := store.GetTeammate(ctx, "mate-a")
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, TeammateStateIdle, mate.State)
}
