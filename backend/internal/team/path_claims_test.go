package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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

func TestPathClaimManagerAcquireSuccessAndRelease(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	manager := NewPathClaimManager(store, "workspace")

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	claims, err := manager.Acquire(ctx, teamID, "task-1", "mate-a", []string{"docs"}, []string{"src/file.txt"}, time.Now().UTC().Add(5*time.Minute))
	require.NoError(t, err)
	require.Len(t, claims, 2)

	listed, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, listed, 2)

	require.NoError(t, manager.Release(ctx, "task-1"))
	listed, err = store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, listed)
}

func TestPathClaimManagerAcquireConflict(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	manager := NewPathClaimManager(store, "workspace")

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = manager.Acquire(ctx, teamID, "task-existing", "mate-b", nil, []string{"src/file.txt"}, time.Now().UTC().Add(5*time.Minute))
	require.NoError(t, err)

	_, err = manager.Acquire(ctx, teamID, "task-new", "mate-a", nil, []string{"src"}, time.Now().UTC().Add(5*time.Minute))
	require.Error(t, err)
	var conflict *PathClaimConflictsError
	require.ErrorAs(t, err, &conflict)
	require.NotEmpty(t, conflict.Conflicts)

	listed, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "task-existing", listed[0].TaskID)
}

func TestReleaseTaskPathClaimsPrefersManager(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	manager := NewPathClaimManager(store, "workspace")

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = manager.Acquire(ctx, teamID, "task-1", "mate-a", nil, []string{"a.txt"}, time.Now().UTC().Add(5*time.Minute))
	require.NoError(t, err)

	require.NoError(t, ReleaseTaskPathClaims(ctx, manager, store, "task-1"))
	listed, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, listed)

	_, err = manager.Acquire(ctx, teamID, "task-2", "mate-a", nil, []string{"b.txt"}, time.Now().UTC().Add(5*time.Minute))
	require.NoError(t, err)
	require.NoError(t, ReleaseTaskPathClaims(ctx, nil, store, "task-2"))
	listed, err = store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, listed)
}

func TestAgentControlTaskRegistryWithClaimsReleasesOnTerminal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	manager := NewPathClaimManager(store, "workspace")

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateBusy})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "done work",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	_, err = manager.Acquire(ctx, teamID, taskID, "mate-a", nil, []string{"out.txt"}, time.Now().UTC().Add(5*time.Minute))
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store).WithClaims(manager)
	_, err = registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		Status:     string(TaskStatusDone),
		Summary:    "finished",
		TeammateID: "mate-a",
	})
	require.NoError(t, err)

	listed, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, listed)
}
