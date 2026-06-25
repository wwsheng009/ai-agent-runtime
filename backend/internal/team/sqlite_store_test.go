package team

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

type failingGlobalMailboxWriter struct{}

func (failingGlobalMailboxWriter) AppendGlobalMailboxRecord(context.Context, string, agentcontrol.MailboxRecord) (int64, error) {
	return 0, fmt.Errorf("global mailbox writer unavailable")
}

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
		TeamID:              teamID,
		Title:               "agent-control-primary",
		Goal:                "read from AgentControl",
		Difficulty:          TaskDifficultyHard,
		DifficultyRationale: "Requires cross-table persistence.",
		Status:              TaskStatusReady,
		Priority:            4,
		Summary:             "agent-control summary",
	})
	require.NoError(t, err)

	assertSQLiteTableMissing(t, store.db, "team_tasks")

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusReady, task.Status)
	assert.Equal(t, "agent-control summary", task.Summary)
	assert.Equal(t, 4, task.Priority)
	assert.Equal(t, TaskDifficultyHard, task.Difficulty)
	assert.Equal(t, "Requires cross-table persistence.", task.DifficultyRationale)

	tasks, err := store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusReady},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, taskID, tasks[0].ID)
	assert.Equal(t, "agent-control-primary", tasks[0].Title)
	assert.Equal(t, TaskDifficultyHard, tasks[0].Difficulty)
	assert.Equal(t, "Requires cross-table persistence.", tasks[0].DifficultyRationale)

	records, err := store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID: teamID,
		Status: []string{string(TaskStatusReady)},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, TaskDifficultyHard, records[0].Difficulty)
	assert.Equal(t, "Requires cross-table persistence.", records[0].DifficultyRationale)
}

func TestSQLiteStoreMigratesAgentControlTaskDifficultyMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "team-v20.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE agent_control_task_records (
			workflow TEXT NOT NULL,
			task_id TEXT NOT NULL,
			team_id TEXT NOT NULL,
			parent_task_id TEXT,
			assignee TEXT,
			session_id TEXT,
			agent_path TEXT,
			title TEXT,
			summary TEXT,
			status TEXT,
			priority INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			goal TEXT,
			inputs_json TEXT NOT NULL DEFAULT '[]',
			read_paths_json TEXT NOT NULL DEFAULT '[]',
			write_paths_json TEXT NOT NULL DEFAULT '[]',
			deliverables_json TEXT NOT NULL DEFAULT '[]',
			lease_until TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			result_ref TEXT,
			version INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (workflow, task_id)
		);
		INSERT INTO agent_control_task_records (
			workflow, task_id, team_id, parent_task_id, assignee, session_id, agent_path,
			title, summary, status, priority, created_at, updated_at, goal, inputs_json,
			read_paths_json, write_paths_json, deliverables_json, lease_until, retry_count,
			result_ref, version
		) VALUES (
			'spawn_team', 'legacy-task', 'team-legacy', NULL, NULL, NULL, NULL,
			'legacy title', 'legacy summary', 'pending', 4, '2026-06-21T00:00:00Z',
			'2026-06-21T00:00:00Z', 'legacy goal', '[]', '[]', '[]', '[]', NULL, 0, NULL, 1
		);
	`)
	require.NoError(t, err)
	for version := 1; version <= 20; version++ {
		_, err = db.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, name, applied_at)
			VALUES (?, ?, ?)
		`, version, fmt.Sprintf("legacy_%d", version), "2026-06-21T00:00:00Z")
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	store, err := NewSQLiteStore(&StoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "difficulty"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "difficulty_rationale"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_provider"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_model"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_reasoning_effort"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_source"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_warnings_json"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "fallback_used"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "fallback_reason"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_resolved_at"))
	require.True(t, sqliteColumnExists(t, store.db, "agent_control_task_records", "route_attempt"))

	legacy, err := store.GetTask(ctx, "legacy-task")
	require.NoError(t, err)
	require.NotNil(t, legacy)
	assert.Equal(t, "legacy title", legacy.Title)
	assert.Equal(t, "", legacy.Difficulty)
	assert.Equal(t, "", legacy.DifficultyRationale)

	records, err := store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		TeamID: "team-legacy",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "", records[0].Difficulty)
	assert.Equal(t, "", records[0].DifficultyRationale)
	assert.Equal(t, "", records[0].RouteProvider)
	assert.Equal(t, "", records[0].RouteModel)
	assert.Equal(t, "", records[0].RouteReasoningEffort)
	assert.Equal(t, "", records[0].RouteSource)
	assert.Empty(t, records[0].RouteWarnings)
	assert.False(t, records[0].FallbackUsed)
	assert.Equal(t, "", records[0].FallbackReason)
	assert.True(t, records[0].RouteResolvedAt.IsZero())
	assert.Equal(t, 0, records[0].RouteAttempt)

	_, err = store.CreateAgentControlTaskRecord(ctx, Task{
		ID:                  "new-task",
		TeamID:              "team-legacy",
		Title:               "new task",
		Difficulty:          TaskDifficultyExpert,
		DifficultyRationale: "Written after migration.",
		Status:              TaskStatusPending,
	})
	require.NoError(t, err)
	created, err := store.GetTask(ctx, "new-task")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, TaskDifficultyExpert, created.Difficulty)
	assert.Equal(t, "Written after migration.", created.DifficultyRationale)
}

func sqliteColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk))
		if name == column {
			return true
		}
	}
	require.NoError(t, rows.Err())
	return false
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

func TestSQLiteStoreWriteThroughFailureKeepsLocalProjectionRepairable(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	store.SetGlobalMailboxWriter(failingGlobalMailboxWriter{})

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	messageID, err := store.InsertMail(ctx, MailMessage{
		ID:        "mailbox-write-through-failure-team",
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      "info",
		Body:      "repairable team local row",
	})
	require.NoError(t, err)
	require.Equal(t, "mailbox-write-through-failure-team", messageID)
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
	require.Equal(t, "mailbox-write-through-failure-team", globalRecords[0].MessageID)
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

func TestSQLiteStoreMailboxProjectionStatus(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.sqlite")})
	require.NoError(t, err)
	defer store.Close()

	status := store.AgentControlMailboxProjectionStatus()
	require.Equal(t, agentcontrol.MailboxProjectionModeLocalOnly, status.Mode)
	require.Equal(t, "global_writer_not_configured", status.Reason)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global.sqlite"),
	})
	require.NoError(t, err)
	defer globalStore.Close()
	store.SetGlobalMailboxWriter(globalStore)
	status = store.AgentControlMailboxProjectionStatus()
	require.Equal(t, agentcontrol.MailboxProjectionModeTransactional, status.Mode)
	require.True(t, status.Transactional)
	require.Equal(t, "global_registry_attachable", status.Reason)
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
