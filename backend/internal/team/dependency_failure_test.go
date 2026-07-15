package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReliabilityEvalTeamDependencyFailurePropagation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID: "mate-blocked", TeamID: teamID, State: TeammateStateBlocked,
	})
	require.NoError(t, err)

	createTask := func(task Task) {
		t.Helper()
		task.TeamID = teamID
		_, createErr := store.CreateTask(ctx, task)
		require.NoError(t, createErr)
	}
	createTask(Task{ID: "task-success", Status: TaskStatusDone, Summary: "preserved result"})
	createTask(Task{ID: "task-root", Status: TaskStatusFailed, Summary: "member timeout"})
	createTask(Task{ID: "task-child", Status: TaskStatusPending})
	assignee := "mate-blocked"
	createTask(Task{ID: "task-grandchild", Status: TaskStatusBlocked, Assignee: &assignee})
	require.NoError(t, store.AddTaskDependency(ctx, "task-child", "task-root"))
	require.NoError(t, store.AddTaskDependency(ctx, "task-grandchild", "task-child"))
	require.NoError(t, store.CreatePathClaims(ctx, []PathClaim{{
		TeamID: teamID, TaskID: "task-grandchild", OwnerAgentID: assignee,
		Path: "src/runtime.go", Mode: PathClaimWrite, LeaseUntil: time.Now().UTC().Add(time.Minute),
	}}))

	result, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store: store, IgnoreBusyTeammates: true,
	}, teamID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, TeamStatusPartiallyCompleted, result.Status)
	require.Contains(t, result.Summary, "preserved result")

	child, err := store.GetTask(ctx, "task-child")
	require.NoError(t, err)
	require.Equal(t, TaskStatusFailed, child.Status)
	require.Contains(t, child.Summary, "task-root failed")
	require.Contains(t, child.Summary, "member timeout")
	grandchild, err := store.GetTask(ctx, "task-grandchild")
	require.NoError(t, err)
	require.Equal(t, TaskStatusFailed, grandchild.Status)
	require.Contains(t, grandchild.Summary, "task-child failed")
	require.Nil(t, grandchild.Assignee)
	require.Nil(t, grandchild.LeaseUntil)

	mate, err := store.GetTeammate(ctx, assignee)
	require.NoError(t, err)
	require.Equal(t, TeammateStateIdle, mate.State)
	claims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, claims)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID: teamID, EventType: TaskDependencyFailedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "task-child", events[0].Payload["task_id"])
	require.Equal(t, "task-grandchild", events[1].Payload["task_id"])
	graphEvents, err := store.ListTaskGraphEvents(ctx, TaskGraphEventFilter{
		TeamID: teamID, EventType: TaskDependencyFailedEvent,
	})
	require.NoError(t, err)
	require.Len(t, graphEvents, 2)

	repeated, err := ReconcileFailedTaskDependencies(ctx, store, teamID)
	require.NoError(t, err)
	require.Empty(t, repeated)
	eventsAfterReplay, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID: teamID, EventType: TaskDependencyFailedEvent,
	})
	require.NoError(t, err)
	require.Len(t, eventsAfterReplay, 2)
}
