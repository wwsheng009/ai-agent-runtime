package team

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestSQLiteStoreBlockTask(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	assignee := "mate-1"
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "blocked-task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
		Version:    7,
	})
	require.NoError(t, err)

	err = store.BlockTask(ctx, taskID, "waiting for review")
	require.NoError(t, err)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusBlocked, task.Status)
	assert.Equal(t, "waiting for review", task.Summary)
	assert.Nil(t, task.LeaseUntil)
	require.NotNil(t, task.Assignee)
	assert.Equal(t, assignee, *task.Assignee)
	assert.Equal(t, int64(8), task.Version)
}

func TestSQLiteStoreGetAndListTasksUseAgentControlRecordsWithoutLegacyTaskMirror(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	taskID, err := store.CreateAgentControlTaskRecord(ctx, Task{
		TeamID:   teamID,
		Title:    "agent-control-primary",
		Goal:     "read from AgentControl",
		Status:   TaskStatusReady,
		Priority: 4,
		Summary:  "agent-control summary",
	})
	require.NoError(t, err)

	assertSQLiteTableMissing(t, store.db, "team_tasks")

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusReady, task.Status)
	assert.Equal(t, "agent-control summary", task.Summary)
	assert.Equal(t, 4, task.Priority)

	tasks, err := store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusReady},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, taskID, tasks[0].ID)
	assert.Equal(t, "agent-control-primary", tasks[0].Title)
}

func TestSQLiteStoreClaimTaskDoesNotRequireLegacyTaskMirror(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "planner",
		TeamID:    teamID,
		Name:      "Planner",
		SessionID: "planner-session",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateAgentControlTaskRecord(ctx, Task{
		TeamID:  teamID,
		Title:   "claim from AgentControl",
		Status:  TaskStatusReady,
		Version: 3,
	})
	require.NoError(t, err)

	assertSQLiteTableMissing(t, store.db, "team_tasks")

	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	claimed, err := store.ClaimTask(ctx, taskID, "planner", leaseUntil, 3)
	require.NoError(t, err)
	require.True(t, claimed)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusRunning, task.Status)
	assert.Equal(t, int64(4), task.Version)
	require.NotNil(t, task.Assignee)
	assert.Equal(t, "planner", *task.Assignee)
	require.NotNil(t, task.LeaseUntil)
	assert.WithinDuration(t, leaseUntil, *task.LeaseUntil, time.Second)

	records, err := store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID: teamID,
		Status: []string{string(TaskStatusRunning)},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "planner-session", records[0].SessionID)
	assert.Equal(t, agentcontrol.TeamTeammatePath(teamID, "planner", "Planner", "planner-session"), records[0].Path)
}

func TestSQLiteStoreWithImmediateTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	errBoom := store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (
				id, workspace_id, lead_session_id, status, strategy, max_teammates, max_writers, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "team-tx", "", "", string(TeamStatusActive), "", 0, 0, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
		require.NoError(t, err)
		return assert.AnError
	})
	require.ErrorIs(t, errBoom, assert.AnError)

	teamRecord, err := store.GetTeam(ctx, "team-tx")
	require.NoError(t, err)
	assert.Nil(t, teamRecord)
}

func TestSQLiteStoreRepairsMailboxGlobalProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	messageID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      "info",
		Body:      "repair team backlink",
		Metadata: map[string]interface{}{
			"purpose": "repair-test",
		},
	})
	require.NoError(t, err)

	records, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(0), records[0].GlobalSeq)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	repaired, err := store.RepairAgentControlMailboxProjection(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), repaired)

	globalRecords, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, globalRecords, 1)
	require.Equal(t, messageID, globalRecords[0].MessageID)

	records, err = store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, globalRecords[0].Seq, records[0].GlobalSeq)
}

func TestSQLiteStoreRepairsMailboxLocalProjectionFromGlobal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	globalRecord, err := globalStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		Scope:     agentcontrol.MailboxScopeTeam,
		TeamID:    teamID,
		TeamSeq:   3,
		MessageID: "global-only-team",
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      "info",
		Body:      "global only team",
		Metadata: map[string]interface{}{
			"purpose": "local-repair",
		},
		CreatedAt: time.Unix(31, 0).UTC(),
	})
	require.NoError(t, err)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Empty(t, localRecords)

	repaired, err := store.RepairAgentControlMailboxLocalProjection(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), repaired)

	localRecords, err = store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	require.Equal(t, globalRecord.Seq, localRecords[0].GlobalSeq)
	require.Equal(t, int64(3), localRecords[0].TeamSeq)

	repaired, err = store.RepairAgentControlMailboxLocalProjection(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), repaired)
}

func TestSQLiteStoreInsertMailCanCommitGlobalAndLocalInOneTx(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(dir, "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(dir, "agent-control.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	messageID, err := store.InsertMail(ctx, MailMessage{
		ID:        "atomic-team-mailbox",
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      "info",
		Body:      "atomic team mailbox",
	})
	require.NoError(t, err)
	require.Equal(t, "atomic-team-mailbox", messageID)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	require.Greater(t, localRecords[0].GlobalSeq, int64(0))
	globalRecords, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:  agentcontrol.MailboxScopeTeam,
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, globalRecords, 1)
	require.Equal(t, globalRecords[0].Seq, localRecords[0].GlobalSeq)
	require.Equal(t, "atomic-team-mailbox", globalRecords[0].MessageID)
}

func TestSQLiteStoreTaskSignalsPersistSequenceAndWake(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	watch, unwatch := store.WatchTaskSignals(ctx, teamID)
	defer unwatch()
	controlWatch, unwatchControl := store.WatchAgentControlTaskSignals(ctx, "spawn_team", teamID)
	defer unwatchControl()

	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "ready task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	select {
	case signal := <-watch:
		assert.Equal(t, int64(1), signal.Seq)
		assert.Equal(t, teamID, signal.TeamID)
		assert.Equal(t, taskID, signal.TaskID)
		assert.Equal(t, TaskSignalTaskCreated, signal.Kind)
		assert.Equal(t, TaskStatusReady, signal.Status)
	case <-time.After(time.Second):
		t.Fatal("expected task signal watcher wake")
	}

	select {
	case signal := <-controlWatch:
		assert.Equal(t, int64(1), signal.Seq)
		assert.Equal(t, int64(1), signal.TeamSeq)
		assert.Equal(t, "spawn_team", signal.Workflow)
		assert.Equal(t, teamID, signal.TeamID)
		assert.Equal(t, taskID, signal.TaskID)
		assert.Equal(t, TaskSignalTaskCreated, signal.Kind)
		assert.Equal(t, TaskStatusReady, signal.Status)
	case <-time.After(time.Second):
		t.Fatal("expected AgentControl task wake signal")
	}

	seq, err := store.LastTaskSignalSeq(ctx, teamID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)

	controlSeq, err := store.LastAgentControlTaskSignalSeq(ctx, "spawn_team", teamID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), controlSeq)

	assertSQLiteTableMissing(t, store.db, "agent_control_task_wake_signals")

	messages, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	assert.Empty(t, messages)
}

func TestSQLiteStoreAgentControlTaskRecordsMirrorTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "planner",
		TeamID:    teamID,
		Name:      "Planner",
		SessionID: "planner-session",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)

	assignee := "planner"
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "mirrored task",
		Status:   TaskStatusPending,
		Assignee: &assignee,
		Priority: 7,
	})
	require.NoError(t, err)

	records, err := store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
		Assignee: "planner",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, taskID, records[0].ID)
	assert.Equal(t, TaskStatusPending, TaskStatus(records[0].Status))
	assert.Equal(t, "planner-session", records[0].SessionID)
	assert.Equal(t, agentcontrol.TeamTeammatePath(teamID, "planner", "Planner", "planner-session"), records[0].Path)

	_, err = store.MarkReadyTasks(ctx, teamID)
	require.NoError(t, err)
	records, err = store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID: teamID,
		Status: []string{string(TaskStatusReady)},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, taskID, records[0].ID)

	claimed, err := store.ClaimTask(ctx, taskID, "planner", time.Now().UTC().Add(time.Minute), 0)
	require.NoError(t, err)
	require.True(t, claimed)
	records, err = store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID:     teamID,
		PathPrefix: "/root/teams/" + teamID,
		Status:     []string{string(TaskStatusRunning)},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, taskID, records[0].ID)

	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "planner",
		TeamID:    teamID,
		Name:      "Planner",
		SessionID: "planner-new-session",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)
	records, err = store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID:   teamID,
		Assignee: "planner",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "planner-new-session", records[0].SessionID)
	assert.Equal(t, agentcontrol.TeamTeammatePath(teamID, "planner", "Planner", "planner-new-session"), records[0].Path)

	require.NoError(t, store.UpdateTaskStatus(ctx, taskID, TaskStatusDone, "mirror done"))
	records, err = store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID: teamID,
		Status: []string{string(TaskStatusDone)},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, taskID, records[0].ID)
	assert.Equal(t, "mirror done", records[0].Summary)
}

func TestSQLiteStoreMarkReadyTasksEmitsTaskSignal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "pending task",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)
	startSeq, err := store.LastTaskSignalSeq(ctx, teamID)
	require.NoError(t, err)

	changed, err := store.MarkReadyTasks(ctx, teamID)
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)

	seq, err := store.LastTaskSignalSeq(ctx, teamID)
	require.NoError(t, err)
	assert.Equal(t, startSeq+1, seq)
}

func TestSQLiteStorePendingTaskCreationDoesNotEmitTaskSignal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "pending task",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)

	seq, err := store.LastTaskSignalSeq(ctx, teamID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq)
}
