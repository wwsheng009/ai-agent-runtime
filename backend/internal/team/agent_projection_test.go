package team

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestAgentProjectionFindsTeammateAndActiveTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		SessionID: "session-1",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:       "task-ready",
		TeamID:   teamID,
		Status:   TaskStatusReady,
		Assignee: stringPtr("member-1"),
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:       "task-running",
		TeamID:   teamID,
		Status:   TaskStatusRunning,
		Assignee: stringPtr("member-1"),
	})
	require.NoError(t, err)

	record, teammate, err := FindTeammateBySession(ctx, store, "session-1")
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotNil(t, teammate)
	require.Equal(t, teamID, record.ID)
	require.Equal(t, "member-1", teammate.ID)

	task, err := ActiveTaskForAssignee(ctx, store, teamID, "member-1")
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, "task-running", task.ID)
}

func TestAgentControlTaskRecordsProjectTeamTasks(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Member One",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	parentTaskID := "task-parent"
	assignee := "member-1"
	_, err = store.CreateTask(ctx, Task{
		ID:           "task-child",
		TeamID:       teamID,
		ParentTaskID: &parentTaskID,
		Title:        " Inspect docs ",
		Status:       TaskStatusRunning,
		Priority:     9,
		Assignee:     &assignee,
		Summary:      " in progress ",
	})
	require.NoError(t, err)

	records, err := AgentControlTaskRecords(ctx, store, teamID)
	require.NoError(t, err)
	require.Len(t, records, 1)

	record := records[0]
	require.Equal(t, "task-child", record.ID)
	require.Equal(t, "spawn_team", record.Workflow)
	require.Equal(t, teamID, record.TeamID)
	require.Equal(t, parentTaskID, record.ParentTaskID)
	require.Equal(t, "member-1", record.Assignee)
	require.Equal(t, "session-1", record.SessionID)
	require.Equal(t, "/root/teams/team-1/member-1", record.Path)
	require.Equal(t, "Inspect docs", record.Title)
	require.Equal(t, "in progress", record.Summary)
	require.Equal(t, "running", record.Status)
	require.Equal(t, 9, record.Priority)
	require.False(t, record.CreatedAt.IsZero())
	require.False(t, record.UpdatedAt.IsZero())

	active, err := ActiveAgentControlTaskRecordForAssignee(ctx, store, teamID, "member-1")
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, record.ID, active.ID)
	require.Equal(t, record.Path, active.Path)
	require.Equal(t, record.Status, active.Status)

	registry := NewAgentControlTaskRegistry(store)
	filtered, err := registry.ListAgentControlTasks(ctx, agentcontrol.TaskFilter{
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		TeamID:     teamID,
		Assignee:   "member-1",
		Status:     []string{string(TaskStatusRunning)},
		PathPrefix: "/root/teams/team-1",
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, record.ID, filtered[0].ID)

	unsupported, err := registry.ListAgentControlTasks(ctx, agentcontrol.TaskFilter{Workflow: agentcontrol.WorkflowSpawnAgent})
	require.NoError(t, err)
	require.Empty(t, unsupported)
}

func TestAgentControlTaskRegistryWatchesTaskWake(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := registry.WatchAgentControlTaskWake(watchCtx, agentcontrol.TaskWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	defer unwatch()

	taskID, err := store.CreateTask(ctx, Task{
		ID:     "task-wake",
		TeamID: teamID,
		Title:  "Wake task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	select {
	case event := <-wake:
		require.Equal(t, int64(1), event.Seq)
		require.Equal(t, agentcontrol.WorkflowSpawnTeam, event.Workflow)
		require.Equal(t, teamID, event.TeamID)
		require.Equal(t, taskID, event.TaskID)
		require.Equal(t, TaskSignalTaskCreated, event.Kind)
		require.Equal(t, string(TaskStatusReady), event.Status)
	case <-time.After(time.Second):
		t.Fatal("expected AgentControl task wake event")
	}

	var registryRows int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_control_wake_events WHERE team_id = ? AND kind = ?
	`, teamID, agentcontrol.WakeKindTask).Scan(&registryRows))
	require.Equal(t, 1, registryRows)

	assertSQLiteTableMissing(t, store.db, "agent_control_task_wake_signals")

	seq, err := registry.LastAgentControlTaskWakeSeq(ctx, agentcontrol.TaskWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	unsupportedSeq, err := registry.LastAgentControlTaskWakeSeq(ctx, agentcontrol.TaskWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnAgent,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), unsupportedSeq)
}

func TestAgentControlTaskRegistryCreatesTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        assignee,
		TeamID:    teamID,
		Name:      "Member One",
		SessionID: "session-1",
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.CreateAgentControlTask(ctx, agentcontrol.TaskCreateRequest{
		ID:           "task-create",
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		TeamID:       teamID,
		Title:        " Inspect docs ",
		Goal:         "Review docs",
		Status:       string(TaskStatusReady),
		Priority:     7,
		Assignee:     assignee,
		ReadPaths:    []string{"docs"},
		WritePaths:   []string{"docs/plan"},
		Deliverables: []string{"summary"},
		Summary:      "new task",
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, "task-create", record.ID)
	require.Equal(t, "ready", record.Status)
	require.Equal(t, "Inspect docs", record.Title)
	require.Equal(t, "/root/teams/team-1/member-1", record.Path)

	created, err := store.GetTask(ctx, "task-create")
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, TaskStatusReady, created.Status)
	require.NotNil(t, created.Assignee)
	require.Equal(t, assignee, *created.Assignee)

	var (
		goalRaw         string
		inputsRaw       string
		readPathsRaw    string
		writePathsRaw   string
		deliverablesRaw string
		versionRaw      int64
	)
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT goal, inputs_json, read_paths_json, write_paths_json, deliverables_json, version
		FROM agent_control_task_records
		WHERE workflow = ? AND task_id = ?
	`, agentcontrol.WorkflowSpawnTeam, "task-create").Scan(&goalRaw, &inputsRaw, &readPathsRaw, &writePathsRaw, &deliverablesRaw, &versionRaw))
	require.Equal(t, "Review docs", goalRaw)
	require.Equal(t, "[]", inputsRaw)
	require.Equal(t, `["docs"]`, readPathsRaw)
	require.Equal(t, `["docs/plan"]`, writePathsRaw)
	require.Equal(t, `["summary"]`, deliverablesRaw)
	require.EqualValues(t, 1, versionRaw)

	_, err = registry.CreateAgentControlTask(ctx, agentcontrol.TaskCreateRequest{
		Workflow: agentcontrol.WorkflowSpawnAgent,
		TeamID:   teamID,
		Title:    "bad workflow",
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryUpdatesTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	taskID, err := store.CreateTask(ctx, Task{
		ID:       "task-update",
		TeamID:   teamID,
		Title:    "Old title",
		Status:   TaskStatusPending,
		Priority: 1,
	})
	require.NoError(t, err)

	title := "New title"
	status := string(TaskStatusReady)
	priority := 7
	summary := "patched through agentcontrol"
	readPaths := []string{"docs", "backend"}
	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTask(ctx, agentcontrol.TaskUpdateRequest{
		ID:        taskID,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    teamID,
		Title:     &title,
		Status:    &status,
		Priority:  &priority,
		Assignee:  &assignee,
		ReadPaths: &readPaths,
		Summary:   &summary,
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "ready", record.Status)
	require.Equal(t, assignee, record.Assignee)
	require.Equal(t, "New title", record.Title)
	require.Equal(t, "patched through agentcontrol", record.Summary)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReady, updated.Status)
	require.Equal(t, 7, updated.Priority)
	require.NotNil(t, updated.Assignee)
	require.Equal(t, assignee, *updated.Assignee)
	require.Equal(t, []string{"docs", "backend"}, updated.ReadPaths)
}

func TestAgentControlTaskRegistryCreatesTaskDependency(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	dependencyID, err := store.CreateTask(ctx, Task{
		ID:     "task-dependency",
		TeamID: teamID,
		Title:  "Dependency",
		Status: TaskStatusDone,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:     "task-dependent",
		TeamID: teamID,
		Title:  "Dependent",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	err = registry.CreateAgentControlTaskDependency(ctx, agentcontrol.TaskDependencyCreateRequest{
		Workflow:    agentcontrol.WorkflowSpawnTeam,
		TeamID:      teamID,
		TaskID:      taskID,
		DependsOnID: dependencyID,
	})
	require.NoError(t, err)
	deps, err := store.ListTaskDependencies(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, []string{dependencyID}, deps)

	err = registry.CreateAgentControlTaskDependency(ctx, agentcontrol.TaskDependencyCreateRequest{
		Workflow:    agentcontrol.WorkflowSpawnAgent,
		TeamID:      teamID,
		TaskID:      taskID,
		DependsOnID: dependencyID,
	})
	require.Error(t, err)

	otherTeamID, err := store.CreateTeam(ctx, Team{ID: "team-2"})
	require.NoError(t, err)
	otherTaskID, err := store.CreateTask(ctx, Task{
		ID:     "task-other-team",
		TeamID: otherTeamID,
		Title:  "Other team task",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)
	err = registry.CreateAgentControlTaskDependency(ctx, agentcontrol.TaskDependencyCreateRequest{
		Workflow:    agentcontrol.WorkflowSpawnTeam,
		TeamID:      teamID,
		TaskID:      otherTaskID,
		DependsOnID: dependencyID,
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryMarksReadyTasks(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	dependencyID, err := store.CreateTask(ctx, Task{
		ID:     "task-ready-dependency",
		TeamID: teamID,
		Title:  "Dependency",
		Status: TaskStatusDone,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:     "task-ready-dependent",
		TeamID: teamID,
		Title:  "Dependent",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, store.AddTaskDependency(ctx, taskID, dependencyID))

	registry := NewAgentControlTaskRegistry(store)
	changed, err := registry.MarkAgentControlTasksReady(ctx, agentcontrol.TaskReadyRequest{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)

	records, err := registry.ListAgentControlTasks(ctx, agentcontrol.TaskFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
		Status:   []string{string(TaskStatusReady)},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, taskID, records[0].ID)

	_, err = registry.MarkAgentControlTasksReady(ctx, agentcontrol.TaskReadyRequest{
		Workflow: agentcontrol.WorkflowSpawnAgent,
		TeamID:   teamID,
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryListsTaskDependencies(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	dependencyID, err := store.CreateTask(ctx, Task{
		ID:     "task-dependency",
		TeamID: teamID,
		Title:  "Dependency",
		Status: TaskStatusDone,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:     "task-dependent",
		TeamID: teamID,
		Title:  "Dependent",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, store.AddTaskDependency(ctx, taskID, dependencyID))

	registry := NewAgentControlTaskRegistry(store)
	records, err := registry.ListAgentControlTaskDependencies(ctx, agentcontrol.TaskDependencyFilter{
		Workflow:          agentcontrol.WorkflowSpawnTeam,
		TeamID:            teamID,
		TaskID:            taskID,
		DependsOnID:       dependencyID,
		IncludeDependents: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotEmpty(t, records[0].ID)
	require.Equal(t, agentcontrol.WorkflowSpawnTeam, records[0].Workflow)
	require.Equal(t, teamID, records[0].TeamID)
	require.Equal(t, taskID, records[0].TaskID)
	require.Equal(t, dependencyID, records[0].DependsOnID)
	require.False(t, records[0].CreatedAt.IsZero())

	dependents, err := registry.ListAgentControlTaskDependencies(ctx, agentcontrol.TaskDependencyFilter{
		Workflow:          agentcontrol.WorkflowSpawnTeam,
		TeamID:            teamID,
		DependsOnID:       dependencyID,
		IncludeDependents: true,
	})
	require.NoError(t, err)
	require.Len(t, dependents, 1)
	require.NotEmpty(t, dependents[0].ID)
	require.Equal(t, taskID, dependents[0].TaskID)
	require.False(t, dependents[0].CreatedAt.IsZero())

	graphEvents, err := registry.ListAgentControlTaskGraphEvents(ctx, agentcontrol.TaskGraphEventFilter{
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    teamID,
		EventType: TaskDependencyCreatedEvent,
	})
	require.NoError(t, err)
	require.Len(t, graphEvents, 1)
	require.Equal(t, int64(1), graphEvents[0].Seq)
	require.Equal(t, int64(1), graphEvents[0].TeamSeq)
	require.Equal(t, agentcontrol.WorkflowSpawnTeam, graphEvents[0].Workflow)
	require.Equal(t, teamID, graphEvents[0].TeamID)
	require.Equal(t, TaskDependencyCreatedEvent, graphEvents[0].EventType)
	require.Equal(t, taskID, graphEvents[0].TaskID)
	require.Equal(t, dependencyID, graphEvents[0].DependsOnID)
	require.NotEmpty(t, graphEvents[0].DependencyID)
	require.False(t, graphEvents[0].CreatedAt.IsZero())

	unsupported, err := registry.ListAgentControlTaskDependencies(ctx, agentcontrol.TaskDependencyFilter{
		Workflow:    agentcontrol.WorkflowSpawnAgent,
		TaskID:      taskID,
		DependsOnID: dependencyID,
	})
	require.NoError(t, err)
	require.Empty(t, unsupported)
}

func TestAgentControlTaskRegistryListTaskGraphEventsUsesGlobalCursor(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

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

	registry := NewAgentControlTaskRegistry(store)
	events, err := registry.ListAgentControlTaskGraphEvents(ctx, agentcontrol.TaskGraphEventFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, teamID1, events[0].TeamID)
	require.Equal(t, teamID2, events[1].TeamID)
	require.Equal(t, int64(1), events[0].TeamSeq)
	require.Equal(t, int64(1), events[1].TeamSeq)
	require.Greater(t, events[1].Seq, events[0].Seq)
	require.Equal(t, TaskDependencyCreatedEvent, events[0].EventType)

	afterFirst, err := registry.ListAgentControlTaskGraphEvents(ctx, agentcontrol.TaskGraphEventFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		AfterSeq: events[0].Seq,
	})
	require.NoError(t, err)
	require.Len(t, afterFirst, 1)
	require.Equal(t, events[1].Seq, afterFirst[0].Seq)
	require.Equal(t, teamID2, afterFirst[0].TeamID)
}

func TestAgentControlTaskRegistryUpdatesTaskStatus(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        assignee,
		TeamID:    teamID,
		Name:      "Member One",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:       "task-status",
		TeamID:   teamID,
		Title:    "Check status",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTaskStatus(ctx, agentcontrol.TaskStatusUpdateRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusBlocked),
		Summary:  "waiting on dependency",
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "blocked", record.Status)
	require.Equal(t, "waiting on dependency", record.Summary)
	require.Equal(t, "/root/teams/team-1/member-1", record.Path)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusBlocked, updated.Status)
	require.Equal(t, "waiting on dependency", updated.Summary)

	_, err = registry.UpdateAgentControlTaskStatus(ctx, agentcontrol.TaskStatusUpdateRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnAgent,
		Status:   string(TaskStatusReady),
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryCancelledUpdatesReleaseLease(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	statusTaskID, err := store.CreateTask(ctx, Task{
		ID:         "task-status-cancel",
		TeamID:     teamID,
		Title:      "Cancel through status",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)
	updateTaskID, err := store.CreateTask(ctx, Task{
		ID:         "task-update-cancel",
		TeamID:     teamID,
		Title:      "Cancel through update",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTaskStatus(ctx, agentcontrol.TaskStatusUpdateRequest{
		ID:       statusTaskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusCancelled),
		Summary:  "status cancelled",
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, "cancelled", record.Status)
	require.Empty(t, record.Assignee)
	require.Equal(t, "status cancelled", record.Summary)

	statusTask, err := store.GetTask(ctx, statusTaskID)
	require.NoError(t, err)
	require.NotNil(t, statusTask)
	require.Equal(t, TaskStatusCancelled, statusTask.Status)
	require.Nil(t, statusTask.Assignee)
	require.Nil(t, statusTask.LeaseUntil)

	cancelled := string(TaskStatusCancelled)
	reassigned := "member-2"
	record, err = registry.UpdateAgentControlTask(ctx, agentcontrol.TaskUpdateRequest{
		ID:       updateTaskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   &cancelled,
		Assignee: &reassigned,
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, "cancelled", record.Status)
	require.Empty(t, record.Assignee)

	updateTask, err := store.GetTask(ctx, updateTaskID)
	require.NoError(t, err)
	require.NotNil(t, updateTask)
	require.Equal(t, TaskStatusCancelled, updateTask.Status)
	require.Nil(t, updateTask.Assignee)
	require.Nil(t, updateTask.LeaseUntil)
}

func TestAgentControlTaskRegistryTerminalStatusUpdatesReleaseLease(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	statusTaskID, err := store.CreateTask(ctx, Task{
		ID:         "task-status-done",
		TeamID:     teamID,
		Title:      "Done through status",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)
	updateTaskID, err := store.CreateTask(ctx, Task{
		ID:         "task-update-failed",
		TeamID:     teamID,
		Title:      "Failed through update",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTaskStatus(ctx, agentcontrol.TaskStatusUpdateRequest{
		ID:       statusTaskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusDone),
		Summary:  "status done",
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, "done", record.Status)
	require.Empty(t, record.Assignee)
	require.Equal(t, "status done", record.Summary)

	statusTask, err := store.GetTask(ctx, statusTaskID)
	require.NoError(t, err)
	require.NotNil(t, statusTask)
	require.Equal(t, TaskStatusDone, statusTask.Status)
	require.Nil(t, statusTask.Assignee)
	require.Nil(t, statusTask.LeaseUntil)

	failed := string(TaskStatusFailed)
	reassigned := "member-2"
	record, err = registry.UpdateAgentControlTask(ctx, agentcontrol.TaskUpdateRequest{
		ID:       updateTaskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   &failed,
		Assignee: &reassigned,
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, "failed", record.Status)
	require.Empty(t, record.Assignee)

	updateTask, err := store.GetTask(ctx, updateTaskID)
	require.NoError(t, err)
	require.NotNil(t, updateTask)
	require.Equal(t, TaskStatusFailed, updateTask.Status)
	require.Nil(t, updateTask.Assignee)
	require.Nil(t, updateTask.LeaseUntil)
}

func TestAgentControlTaskRegistryClaimsTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        assignee,
		TeamID:    teamID,
		Name:      "Member One",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:     "task-claim",
		TeamID: teamID,
		Title:  "Claim task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	registry := NewAgentControlTaskRegistry(store)
	record, claimed, err := registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:              taskID,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		Assignee:        assignee,
		LeaseUntil:      leaseUntil,
		ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "running", record.Status)
	require.Equal(t, "/root/teams/team-1/member-1", record.Path)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusRunning, updated.Status)
	require.NotNil(t, updated.Assignee)
	require.Equal(t, assignee, *updated.Assignee)
	require.NotNil(t, updated.LeaseUntil)
	require.WithinDuration(t, leaseUntil, *updated.LeaseUntil, time.Second)

	_, _, err = registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnAgent,
		Assignee:   assignee,
		LeaseUntil: leaseUntil,
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryClaimsTaskWithPathClaims(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     assignee,
		TeamID: teamID,
		State:  TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:         "task-claim-paths",
		TeamID:     teamID,
		Title:      "Claim paths",
		Status:     TaskStatusReady,
		WritePaths: []string{"src/file.txt"},
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	registry := NewAgentControlTaskRegistry(store)
	record, claimed, err := registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:              taskID,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		Assignee:        assignee,
		LeaseUntil:      leaseUntil,
		ExpectedVersion: task.Version,
		WritePaths:      task.WritePaths,
		UsePathClaims:   true,
		WorkspaceRoot:   "workspace",
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "running", record.Status)

	pathClaims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, pathClaims, 1)
	require.Equal(t, "workspace/src/file.txt", pathClaims[0].Path)
	require.Equal(t, PathClaimWrite, pathClaims[0].Mode)

	mate, err := store.GetTeammate(ctx, assignee)
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, TeammateStateBusy, mate.State)
}

func TestAgentControlTaskRegistryUpdatesTerminalTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     assignee,
		TeamID: teamID,
		State:  TeammateStateBusy,
	})
	require.NoError(t, err)
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		ID:         "task-terminal",
		TeamID:     teamID,
		Title:      "Terminal task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)
	resultRef := "artifact://task-terminal"

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		Status:     string(TaskStatusDone),
		Summary:    "finished through terminal seam",
		ResultRef:  &resultRef,
		TeammateID: assignee,
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "done", record.Status)
	require.Equal(t, "finished through terminal seam", record.Summary)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusDone, updated.Status)
	require.Equal(t, "finished through terminal seam", updated.Summary)
	require.NotNil(t, updated.ResultRef)
	require.Equal(t, resultRef, *updated.ResultRef)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)

	mate, err := store.GetTeammate(ctx, assignee)
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, TeammateStateIdle, mate.State)

	_, err = registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnAgent,
		Status:   string(TaskStatusDone),
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryBlocksTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     assignee,
		TeamID: teamID,
		State:  TeammateStateBusy,
	})
	require.NoError(t, err)
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		ID:         "task-block",
		TeamID:     teamID,
		Title:      "Block task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.BlockAgentControlTask(ctx, agentcontrol.TaskBlockRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		Summary:    "waiting on review",
		TeammateID: assignee,
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "blocked", record.Status)
	require.Equal(t, "waiting on review", record.Summary)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusBlocked, updated.Status)
	require.Equal(t, "waiting on review", updated.Summary)
	require.NotNil(t, updated.Assignee)
	require.Equal(t, assignee, *updated.Assignee)
	require.Nil(t, updated.LeaseUntil)

	mate, err := store.GetTeammate(ctx, assignee)
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, TeammateStateBlocked, mate.State)

	_, err = registry.BlockAgentControlTask(ctx, agentcontrol.TaskBlockRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnAgent,
		Summary:  "bad workflow",
	})
	require.Error(t, err)
}

func TestAgentControlTaskRegistryReleasesTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        assignee,
		TeamID:    teamID,
		Name:      "Member One",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	leaseUntil := time.Now().UTC().Add(time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		ID:         "task-release",
		TeamID:     teamID,
		Title:      "Release task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusReady),
		Summary:  "released for retry",
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "ready", record.Status)
	require.Equal(t, "released for retry", record.Summary)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReady, updated.Status)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)
	require.Equal(t, "released for retry", updated.Summary)
}

func TestAgentControlTaskRegistryRetriesTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		ID:         "task-retry",
		TeamID:     teamID,
		Title:      "Retry task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.RetryAgentControlTask(ctx, agentcontrol.TaskRetryRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusReady),
		Summary:  "retry through agentcontrol",
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "ready", record.Status)
	require.Equal(t, "retry through agentcontrol", record.Summary)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, TaskStatusReady, updated.Status)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)
	require.Equal(t, 1, updated.RetryCount)
	require.Equal(t, "retry through agentcontrol", updated.Summary)
}

func TestAgentControlTaskRegistryRenewsTaskLease(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        assignee,
		TeamID:    teamID,
		Name:      "Member One",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	initialLease := time.Now().UTC().Add(time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		ID:         "task-renew",
		TeamID:     teamID,
		Title:      "Renew task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &initialLease,
	})
	require.NoError(t, err)

	renewedLease := time.Now().UTC().Add(5 * time.Minute)
	registry := NewAgentControlTaskRegistry(store)
	record, err := registry.RenewAgentControlTaskLease(ctx, agentcontrol.TaskLeaseRenewRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		LeaseUntil: renewedLease,
	})
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, taskID, record.ID)
	require.Equal(t, "running", record.Status)
	require.Equal(t, "/root/teams/team-1/member-1", record.Path)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.LeaseUntil)
	require.WithinDuration(t, renewedLease, *updated.LeaseUntil, time.Second)
	require.NotNil(t, updated.Assignee)
	require.Equal(t, assignee, *updated.Assignee)
}
