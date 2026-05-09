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
	assertSQLiteTableMissing(t, store.db, "team_task_dependencies")

	dependents, err := store.ListTaskDependents(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, dependents, 1)
	require.Equal(t, childID, dependents[0])
}

func TestSQLiteStoreListTaskDependencyRecords(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	parentID := "task-parent"
	childID := "task-child"
	followupID := "task-followup"
	for _, id := range []string{parentID, childID, followupID} {
		_, err = store.CreateTask(ctx, Task{
			ID:     id,
			TeamID: teamID,
			Status: TaskStatusReady,
		})
		require.NoError(t, err)
	}

	require.NoError(t, store.AddTaskDependency(ctx, childID, parentID))
	require.NoError(t, store.AddTaskDependency(ctx, followupID, childID))
	assertSQLiteTableMissing(t, store.db, "team_task_dependencies")

	var mirrored int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_control_task_dependencies WHERE team_id = ?
	`, teamID).Scan(&mirrored))
	require.Equal(t, 2, mirrored)

	records, err := store.ListTaskDependencyRecords(ctx, TaskDependencyFilter{
		TaskID:            childID,
		IncludeDependents: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.NotEmpty(t, records[0].ID)
	require.Equal(t, childID, records[0].TaskID)
	require.Equal(t, parentID, records[0].DependsOnID)
	require.False(t, records[0].CreatedAt.IsZero())
	require.NotEmpty(t, records[1].ID)
	require.Equal(t, followupID, records[1].TaskID)
	require.Equal(t, childID, records[1].DependsOnID)
	require.False(t, records[1].CreatedAt.IsZero())

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID, EventType: TaskDependencyCreatedEvent})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, int64(1), events[0].Seq)
	require.Equal(t, TaskDependencyCreatedEvent, events[0].Type)
	require.Equal(t, childID, events[0].Payload["task_id"])
	require.Equal(t, parentID, events[0].Payload["depends_on_id"])
	require.NotEmpty(t, events[0].Payload["dependency_id"])

	require.NoError(t, store.AddTaskDependency(ctx, childID, parentID))
	eventsAfterDuplicate, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID, EventType: TaskDependencyCreatedEvent})
	require.NoError(t, err)
	require.Len(t, eventsAfterDuplicate, 2)
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_control_task_dependencies WHERE team_id = ?
	`, teamID).Scan(&mirrored))
	require.Equal(t, 2, mirrored)
}

func TestSQLiteStoreListTaskGraphEventsUsesGlobalCursor(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID1, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	teamID2, err := store.CreateTeam(ctx, Team{ID: "team-2"})
	require.NoError(t, err)

	createDependency := func(teamID, parentID, childID string) {
		t.Helper()
		_, err := store.CreateTask(ctx, Task{
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
	}

	createDependency(teamID1, "task-parent-1", "task-child-1")
	createDependency(teamID2, "task-parent-2", "task-child-2")

	var mirrored int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_control_task_graph_events
	`).Scan(&mirrored))
	require.Equal(t, 2, mirrored)

	events, err := store.ListTaskGraphEvents(ctx, TaskGraphEventFilter{
		EventType: TaskDependencyCreatedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "spawn_team", events[0].Workflow)
	require.Equal(t, teamID1, events[0].TeamID)
	require.Equal(t, teamID2, events[1].TeamID)
	require.Equal(t, int64(1), events[0].TeamSeq)
	require.Equal(t, int64(1), events[1].TeamSeq)
	require.Greater(t, events[1].Seq, events[0].Seq)
	require.Equal(t, TaskDependencyCreatedEvent, events[0].Type)
	require.Equal(t, "task-child-1", events[0].Payload["task_id"])
	require.Equal(t, "task-parent-1", events[0].Payload["depends_on_id"])

	afterFirst, err := store.ListTaskGraphEvents(ctx, TaskGraphEventFilter{AfterSeq: events[0].Seq})
	require.NoError(t, err)
	require.Len(t, afterFirst, 1)
	require.Equal(t, events[1].Seq, afterFirst[0].Seq)
	require.Equal(t, teamID2, afterFirst[0].TeamID)

	teamOnly, err := store.ListTaskGraphEvents(ctx, TaskGraphEventFilter{
		TeamID:    teamID2,
		EventType: "task.*",
	})
	require.NoError(t, err)
	require.Len(t, teamOnly, 1)
	require.Equal(t, events[1].Seq, teamOnly[0].Seq)
	require.Equal(t, teamID2, teamOnly[0].TeamID)
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
