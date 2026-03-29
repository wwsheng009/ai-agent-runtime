package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreListTasksParentFilter(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	parentID := "task-parent"
	_, err = store.CreateTask(ctx, Task{
		ID:     parentID,
		TeamID: teamID,
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	parentRef := parentID
	_, err = store.CreateTask(ctx, Task{
		ID:           "task-child",
		TeamID:       teamID,
		ParentTaskID: &parentRef,
		Status:       TaskStatusReady,
	})
	require.NoError(t, err)

	parentFilter := parentID
	children, err := store.ListTasks(ctx, TaskFilter{
		TeamID:       teamID,
		ParentTaskID: &parentFilter,
	})
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, "task-child", children[0].ID)

	rootFilter := ""
	roots, err := store.ListTasks(ctx, TaskFilter{
		TeamID:       teamID,
		ParentTaskID: &rootFilter,
	})
	require.NoError(t, err)
	require.Len(t, roots, 1)
	require.Equal(t, parentID, roots[0].ID)
}
