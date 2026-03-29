package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreListTaskDependents(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	parentID := "task-parent"
	childID := "task-child"
	_, err = store.CreateTask(ctx, Task{
		ID:     parentID,
		TeamID: teamID,
		Status: TaskStatusReady,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:     childID,
		TeamID: teamID,
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	require.NoError(t, store.AddTaskDependency(ctx, childID, parentID))

	dependents, err := store.ListTaskDependents(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, dependents, 1)
	require.Equal(t, childID, dependents[0])
}

func TestSQLiteStoreListTeamIDs(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID1, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	teamID2, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	ids, err := store.ListTeamIDs(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ids), 2)
	require.Contains(t, ids, teamID1)
	require.Contains(t, ids, teamID2)
}
