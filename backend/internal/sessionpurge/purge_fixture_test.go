package sessionpurge

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type purgeFixture struct {
	paths Paths
	now   time.Time
}

func newPurgeFixture(t *testing.T) purgeFixture {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	scheduleDir := filepath.Join(root, "data", "schedule")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o755))
	require.NoError(t, os.MkdirAll(scheduleDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "session-artifacts", "old-session"), 0o755))

	paths := Paths{
		History:        filepath.Join(root, "session_history.sqlite"),
		Cells:          filepath.Join(root, "session_cells.sqlite"),
		Runtime:        filepath.Join(runtimeDir, "session_runtime.sqlite"),
		UsageAnalytics: filepath.Join(runtimeDir, "usage_analytics.sqlite"),
		Artifacts:      filepath.Join(runtimeDir, "artifacts.sqlite"),
		AgentControl:   filepath.Join(runtimeDir, "agent_control.sqlite"),
		Background:     filepath.Join(runtimeDir, "background.sqlite"),
		Schedule:       filepath.Join(scheduleDir, "schedule.sqlite"),
		Team:           filepath.Join(runtimeDir, "team_store.sqlite"),
	}

	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	old := now.Add(-60 * 24 * time.Hour).Format(time.RFC3339Nano)
	oldNano := now.Add(-60 * 24 * time.Hour).UnixNano()
	recent := now.Add(-time.Hour).Format(time.RFC3339Nano)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)

	createSQL(t, paths.History, []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, updated_at TEXT NOT NULL, expires_at TEXT)`,
		`CREATE TABLE session_messages (session_id TEXT, seq INTEGER, created_at TEXT)`,
		`CREATE TABLE session_prompt_messages (session_id TEXT, position INTEGER, created_at TEXT)`,
	})
	execSQL(t, paths.History, `
		INSERT INTO sessions (id, updated_at, expires_at) VALUES
		 ('old-session', ?, NULL),
		 ('protected-session', ?, NULL),
		 ('leased-session', ?, NULL),
		 ('background-protected', ?, NULL),
		 ('schedule-protected', ?, NULL),
		 ('team-protected', ?, NULL),
		 ('lease-protected', ?, NULL),
		 ('agent-parent', ?, NULL),
		 ('old-team', ?, NULL),
		 ('recent-session', ?, NULL)
	`, old, old, old, old, old, old, old, old, old, recent)
	execSQL(t, paths.History, `INSERT INTO session_messages (session_id, seq, created_at) VALUES ('old-session', 1, ?), ('protected-session', 1, ?), ('old-team', 1, ?)`, old, old, old)
	execSQL(t, paths.History, `INSERT INTO session_prompt_messages (session_id, position, created_at) VALUES ('old-session', 1, ?), ('protected-session', 1, ?)`, old, old)

	createSQL(t, paths.Cells, []string{
		`CREATE TABLE session_cells (session_id TEXT, cell_id INTEGER, revision INTEGER, created_at INTEGER)`,
	})
	execSQL(t, paths.Cells, `INSERT INTO session_cells (session_id, cell_id, revision, created_at) VALUES
		('old-session', 1, 1, ?),
		('orphan-cells', 1, 1, ?),
		('recent-session', 1, 1, ?)`, oldNano, oldNano, oldNano)

	createSQL(t, paths.Runtime, []string{
		`CREATE TABLE session_runtime_state (session_id TEXT PRIMARY KEY, status TEXT, updated_at TEXT)`,
		`CREATE TABLE session_events (session_id TEXT, created_at TEXT)`,
		`CREATE TABLE session_tool_receipts (session_id TEXT, created_at TEXT)`,
		`CREATE TABLE cache_requests (session_id TEXT, created_at TEXT)`,
		`CREATE TABLE agent_control_mailbox_records (session_id TEXT, created_at TEXT)`,
		`CREATE TABLE session_actor_leases (session_id TEXT PRIMARY KEY, expires_at TEXT, heartbeat_at TEXT)`,
	})
	execSQL(t, paths.Runtime, `INSERT INTO session_runtime_state (session_id, status, updated_at) VALUES
		('old-session', 'idle', ?),
		('protected-session', 'running', ?),
		('orphan-runtime', 'idle', ?),
		('recent-orphan', 'idle', ?)`, old, recent, old, recent)
	execSQL(t, paths.Runtime, `INSERT INTO session_events (session_id, created_at) VALUES
		('old-session', ?), ('orphan-runtime', ?), ('recent-orphan', ?)`, old, old, recent)
	execSQL(t, paths.Runtime, `INSERT INTO session_tool_receipts (session_id, created_at) VALUES ('old-session', ?), ('orphan-runtime', ?)`, old, old)
	execSQL(t, paths.Runtime, `INSERT INTO cache_requests (session_id, created_at) VALUES ('old-session', ?)`, old)
	execSQL(t, paths.Runtime, `INSERT INTO agent_control_mailbox_records (session_id, created_at) VALUES ('old-session', ?)`, old)
	execSQL(t, paths.Runtime, `INSERT INTO session_actor_leases (session_id, expires_at, heartbeat_at) VALUES ('leased-session', ?, ?)`, future, old)

	createSQL(t, paths.UsageAnalytics, []string{
		`CREATE TABLE usage_requests (session_id TEXT)`,
		`CREATE TABLE usage_sessions (session_id TEXT PRIMARY KEY, updated_at_unix_nano INTEGER, started_at_unix_nano INTEGER, ended_at_unix_nano INTEGER)`,
		`CREATE TABLE usage_turns (session_id TEXT)`,
		`CREATE TABLE usage_tool_calls (session_id TEXT)`,
		`CREATE TABLE usage_session_turn_keys (session_id TEXT)`,
		`CREATE TABLE usage_lsp_requests (session_id TEXT)`,
		`CREATE TABLE usage_lsp_first_publish (session_id TEXT)`,
		`CREATE TABLE usage_render_fence_drops (session_id TEXT)`,
		`CREATE TABLE analytics_legacy_llm_requests (session_id TEXT)`,
		`CREATE TABLE analytics_legacy_turns (session_id TEXT)`,
		`CREATE TABLE analytics_legacy_sessions (session_id TEXT)`,
		`CREATE TABLE usage_routes (session_id TEXT, child_session_id TEXT)`,
		`CREATE TABLE usage_subagents (parent_session_id TEXT, child_session_id TEXT)`,
	})
	execSQL(t, paths.UsageAnalytics, `INSERT INTO usage_sessions (session_id, updated_at_unix_nano) VALUES
		('old-session', ?), ('orphan-usage', ?), ('recent-session', ?)`,
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixNano(),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixNano(),
		time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).UnixNano())
	for _, statement := range []string{
		`INSERT INTO usage_requests (session_id) VALUES ('old-session'), ('orphan-usage')`,
		`INSERT INTO usage_turns (session_id) VALUES ('old-session')`,
		`INSERT INTO usage_tool_calls (session_id) VALUES ('old-session')`,
		`INSERT INTO usage_session_turn_keys (session_id) VALUES ('old-session')`,
		`INSERT INTO usage_lsp_requests (session_id) VALUES ('old-session')`,
		`INSERT INTO usage_lsp_first_publish (session_id) VALUES ('old-session')`,
		`INSERT INTO usage_render_fence_drops (session_id) VALUES ('old-session')`,
		`INSERT INTO analytics_legacy_llm_requests (session_id) VALUES ('old-session')`,
		`INSERT INTO analytics_legacy_turns (session_id) VALUES ('old-session')`,
		`INSERT INTO analytics_legacy_sessions (session_id) VALUES ('old-session')`,
		`INSERT INTO usage_routes (session_id, child_session_id) VALUES ('old-session', '')`,
		`INSERT INTO usage_subagents (parent_session_id, child_session_id) VALUES ('parent-keep', 'old-session')`,
		`INSERT INTO usage_subagents (parent_session_id, child_session_id) VALUES ('old-session', 'recent-child')`,
	} {
		execSQL(t, paths.UsageAnalytics, statement)
	}

	createSQL(t, paths.Artifacts, []string{
		`CREATE TABLE artifacts (id TEXT PRIMARY KEY, session_id TEXT, created_at TEXT)`,
		`CREATE TABLE checkpoints (id TEXT PRIMARY KEY, session_id TEXT, created_at TEXT)`,
		`CREATE TABLE checkpoint_files (id TEXT PRIMARY KEY, checkpoint_id TEXT, before_blob_id TEXT, after_blob_id TEXT)`,
		`CREATE TABLE blobs (id TEXT PRIMARY KEY)`,
		`CREATE TABLE memory_entries (session_id TEXT, created_at TEXT)`,
	})
	execSQL(t, paths.Artifacts, `INSERT INTO artifacts (id, session_id, created_at) VALUES ('art-old', 'old-session', ?), ('art-orphan', 'orphan-artifact', ?), ('art-recent', 'recent-session', ?)`, old, old, recent)
	execSQL(t, paths.Artifacts, `INSERT INTO checkpoints (id, session_id, created_at) VALUES ('cp-old', 'old-session', ?), ('cp-recent', 'recent-session', ?)`, old, recent)
	execSQL(t, paths.Artifacts, `INSERT INTO checkpoint_files (id, checkpoint_id, before_blob_id, after_blob_id) VALUES
		('cf-old', 'cp-old', 'blob-old', 'blob-shared'),
		('cf-recent', 'cp-recent', 'blob-shared', NULL)`)
	execSQL(t, paths.Artifacts, `INSERT INTO blobs (id) VALUES ('blob-old'), ('blob-shared')`)
	execSQL(t, paths.Artifacts, `INSERT INTO memory_entries (session_id, created_at) VALUES ('old-session', ?)`, old)

	createSQL(t, paths.AgentControl, []string{
		`CREATE TABLE agent_control_agents (agent_id TEXT PRIMARY KEY, session_id TEXT, parent_session_id TEXT, root_session_id TEXT, status TEXT, closed_at TEXT, updated_at TEXT)`,
		`CREATE TABLE agent_control_agent_wake_events (agent_id TEXT, session_id TEXT, root_session_id TEXT, parent_session_id TEXT, created_at TEXT)`,
		`CREATE TABLE agent_control_global_mailbox_records (session_id TEXT, created_at TEXT)`,
	})
	execSQL(t, paths.AgentControl, `INSERT INTO agent_control_agents (agent_id, session_id, parent_session_id, root_session_id, status, closed_at, updated_at) VALUES
		('a-old', 'old-session', '', '', 'closed', ?, ?),
		('a-child', 'agent-child', 'agent-parent', 'agent-parent', 'active', NULL, ?)`, old, old, recent)
	execSQL(t, paths.AgentControl, `INSERT INTO agent_control_agent_wake_events (agent_id, session_id, root_session_id, parent_session_id, created_at) VALUES
		('a-old', 'old-session', 'old-session', '', ?),
		('a-child', 'agent-child', 'agent-parent', 'agent-parent', ?)`, old, recent)
	execSQL(t, paths.AgentControl, `INSERT INTO agent_control_global_mailbox_records (session_id, created_at) VALUES ('old-session', ?)`, old)

	createSQL(t, paths.Background, []string{
		`CREATE TABLE background_jobs (id TEXT PRIMARY KEY, session_id TEXT, status TEXT, created_at TEXT, started_at TEXT, finished_at TEXT)`,
		`CREATE TABLE background_job_events (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT, created_at TEXT)`,
	})
	execSQL(t, paths.Background, `INSERT INTO background_jobs (id, session_id, status, created_at, started_at, finished_at) VALUES
		('job-old', 'old-session', 'completed', ?, ?, ?),
		('job-running', 'background-protected', 'running', ?, ?, NULL)`, old, old, old, old, old)
	execSQL(t, paths.Background, `INSERT INTO background_job_events (job_id, created_at) VALUES ('job-old', ?), ('job-running', ?)`, old, old)

	createSQL(t, paths.Schedule, []string{
		`CREATE TABLE schedule_tasks (id TEXT PRIMARY KEY, session_id TEXT, created_by_session_id TEXT, status TEXT, lease_expires_at TEXT, updated_at TEXT)`,
		`CREATE TABLE schedule_pending_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT, schedule_id TEXT, created_at TEXT)`,
		`CREATE TABLE schedule_job_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT, schedule_id TEXT)`,
		`CREATE TABLE schedule_delivery_history (schedule_id TEXT, seq INTEGER)`,
		`CREATE TABLE schedule_delivery_intents (occurrence_key TEXT PRIMARY KEY, schedule_id TEXT)`,
	})
	execSQL(t, paths.Schedule, `INSERT INTO schedule_tasks (id, session_id, created_by_session_id, status, lease_expires_at, updated_at) VALUES
		('task-old', 'old-session', 'old-session', 'inactive', NULL, ?),
		('task-protected', 'schedule-protected', 'schedule-protected', 'active', NULL, ?)`, old, old)
	execSQL(t, paths.Schedule, `INSERT INTO schedule_pending_messages (session_id, schedule_id, created_at) VALUES ('old-session', 'task-old', ?), ('schedule-protected', 'task-protected', ?)`, old, old)
	execSQL(t, paths.Schedule, `INSERT INTO schedule_job_runs (session_id, schedule_id) VALUES ('old-session', 'task-old'), ('schedule-protected', 'task-protected')`)
	execSQL(t, paths.Schedule, `INSERT INTO schedule_delivery_history (schedule_id, seq) VALUES ('task-old', 1), ('task-protected', 1)`)
	execSQL(t, paths.Schedule, `INSERT INTO schedule_delivery_intents (occurrence_key, schedule_id) VALUES ('occ-old', 'task-old'), ('occ-protected', 'task-protected')`)

	createSQL(t, paths.Team, []string{
		`CREATE TABLE teams (id TEXT PRIMARY KEY, lead_session_id TEXT, status TEXT, updated_at TEXT)`,
		`CREATE TABLE teammates (id TEXT PRIMARY KEY, team_id TEXT, session_id TEXT, state TEXT, updated_at TEXT)`,
		`CREATE TABLE team_events (id INTEGER PRIMARY KEY AUTOINCREMENT, team_id TEXT)`,
		`CREATE TABLE team_task_signals (id INTEGER PRIMARY KEY AUTOINCREMENT, team_id TEXT)`,
		`CREATE TABLE agent_control_task_graph_events (id INTEGER PRIMARY KEY AUTOINCREMENT, team_id TEXT)`,
		`CREATE TABLE agent_control_task_dependencies (workflow TEXT, team_id TEXT, task_id TEXT, depends_on_id TEXT)`,
		`CREATE TABLE team_path_claims (id TEXT PRIMARY KEY, team_id TEXT)`,
		`CREATE TABLE team_mailbox_receipts (message_id TEXT, team_id TEXT, agent_id TEXT)`,
		`CREATE TABLE agent_control_task_records (workflow TEXT, task_id TEXT, team_id TEXT, session_id TEXT)`,
		`CREATE TABLE agent_control_wake_events (id INTEGER PRIMARY KEY AUTOINCREMENT, team_id TEXT, session_id TEXT)`,
		`CREATE TABLE agent_control_mailbox_records (session_id TEXT, team_id TEXT)`,
		`CREATE TABLE orchestrator_owner_leases (team_id TEXT PRIMARY KEY, owner_id TEXT, lease_until TEXT)`,
	})
	execSQL(t, paths.Team, `INSERT INTO teams (id, lead_session_id, status, updated_at) VALUES
		('team-old', 'old-team', 'done', ?),
		('team-protected', 'team-protected', 'active', ?),
		('team-lease-protected', 'lease-protected', 'done', ?)`, old, old, old)
	execSQL(t, paths.Team, `INSERT INTO teammates (id, team_id, session_id, state, updated_at) VALUES
		('mate-old', 'team-old', 'old-team', 'idle', ?),
		('mate-recent', 'team-old', 'team-old__recent', 'idle', ?),
		('mate-protected', 'team-protected', 'team-protected', 'idle', ?)`, old, recent, old)
	execSQL(t, paths.Team, `INSERT INTO team_events (team_id) VALUES ('team-old'), ('team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO team_task_signals (team_id) VALUES ('team-old'), ('team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO agent_control_task_graph_events (team_id) VALUES ('team-old'), ('team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO agent_control_task_dependencies (workflow, team_id, task_id, depends_on_id) VALUES ('w', 'team-old', 't1', 't2'), ('w', 'team-protected', 't1', 't2')`)
	execSQL(t, paths.Team, `INSERT INTO team_path_claims (id, team_id) VALUES ('pc-old', 'team-old'), ('pc-protected', 'team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO team_mailbox_receipts (message_id, team_id, agent_id) VALUES ('m-old', 'team-old', 'a'), ('m-protected', 'team-protected', 'a')`)
	execSQL(t, paths.Team, `INSERT INTO agent_control_task_records (workflow, task_id, team_id, session_id) VALUES ('w', 't-old', 'team-old', 'old-team'), ('w', 't-protected', 'team-protected', 'team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO agent_control_wake_events (team_id, session_id) VALUES ('team-old', 'old-team'), ('team-protected', 'team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO agent_control_mailbox_records (session_id) VALUES ('old-team'), ('team-protected')`)
	execSQL(t, paths.Team, `INSERT INTO orchestrator_owner_leases (team_id, owner_id, lease_until) VALUES ('team-old', 'owner-old', ?), ('team-lease-protected', 'owner-active', ?)`, old, future)

	return purgeFixture{paths: paths, now: now}
}

func createSQL(t *testing.T, path string, statements []string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		_, err := db.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func execSQL(t *testing.T, path, statement string, args ...interface{}) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(statement, args...)
	require.NoError(t, err, statement)
}

func queryInt(t *testing.T, path, query string, args ...interface{}) int64 {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var value int64
	require.NoError(t, db.QueryRow(query, args...).Scan(&value))
	return value
}
