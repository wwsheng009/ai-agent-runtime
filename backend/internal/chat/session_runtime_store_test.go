package chat

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/team"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteRuntimeStorePersistsCurrentRunMeta(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-current-run-meta-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-1",
		Status:    SessionWaitingInput,
		CurrentRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:        "team-1",
				AgentID:       "mate-1",
				CurrentTaskID: "task-1",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.CurrentRunMeta)
	require.NotNil(t, loaded.CurrentRunMeta.Team)
	assert.Equal(t, "team-1", loaded.CurrentRunMeta.Team.TeamID)
	assert.Equal(t, "mate-1", loaded.CurrentRunMeta.Team.AgentID)
	assert.Equal(t, "task-1", loaded.CurrentRunMeta.Team.CurrentTaskID)
}

func TestSQLiteRuntimeStorePersistsAmbientRunMeta(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-ambient-run-meta-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-ambient-1",
		Status:    SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:        "team-ambient",
				AgentID:       "lead",
				CurrentTaskID: "",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-ambient-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.AmbientRunMeta)
	require.NotNil(t, loaded.AmbientRunMeta.Team)
	assert.Equal(t, "team-ambient", loaded.AmbientRunMeta.Team.TeamID)
	assert.Equal(t, "lead", loaded.AmbientRunMeta.Team.AgentID)
	assert.Equal(t, SessionIdle, loaded.Status)
}

func TestSQLiteRuntimeStoreMigratesCurrentRunMetaColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL
		);

		CREATE TABLE session_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			type TEXT NOT NULL,
			trace_id TEXT,
			agent_name TEXT,
			tool_name TEXT,
			payload_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(session_id, seq)
		);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-2",
		Status:    SessionRunning,
		CurrentRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:        "team-2",
				AgentID:       "mate-2",
				CurrentTaskID: "task-2",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-2")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.CurrentRunMeta)
	require.NotNil(t, loaded.CurrentRunMeta.Team)
	assert.Equal(t, "team-2", loaded.CurrentRunMeta.Team.TeamID)
	assert.Equal(t, "mate-2", loaded.CurrentRunMeta.Team.AgentID)
	assert.Equal(t, "task-2", loaded.CurrentRunMeta.Team.CurrentTaskID)

	_, err = os.Stat(dbPath)
	require.NoError(t, err)
}

func TestSQLiteRuntimeStoreMigratesAmbientRunMetaColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z'),
			(6, 'session_tool_receipts_created_at_unix_nano', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB
		);

		CREATE TABLE session_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			type TEXT NOT NULL,
			trace_id TEXT,
			agent_name TEXT,
			tool_name TEXT,
			payload_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(session_id, seq)
		);

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			created_at_unix_nano INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, tool_call_id)
		);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-ambient-2",
		Status:    SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:  "team-ambient-2",
				AgentID: "lead",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-ambient-2")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.AmbientRunMeta)
	require.NotNil(t, loaded.AmbientRunMeta.Team)
	assert.Equal(t, "team-ambient-2", loaded.AmbientRunMeta.Team.TeamID)
}

func TestSQLiteRuntimeStorePersistsPendingTool(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-pending-tool-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-pending-tool",
		Status:    SessionWaitingInput,
		PendingTool: &PendingToolInvocation{
			ToolCallID: "toolcall_pending_1",
			ToolName:   "ask_user_question",
			ArgsJSON:   []byte(`{"prompt":"Need confirmation","required":true}`),
			CreatedAt:  time.Now().UTC(),
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-pending-tool")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.PendingTool)
	assert.Equal(t, "toolcall_pending_1", loaded.PendingTool.ToolCallID)
	assert.Equal(t, "ask_user_question", loaded.PendingTool.ToolName)
	assert.JSONEq(t, `{"prompt":"Need confirmation","required":true}`, string(loaded.PendingTool.ArgsJSON))
}

func TestSQLiteRuntimeStorePersistsToolReceipt(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-tool-receipt-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	receipt := ToolExecutionReceipt{
		SessionID:   "session-receipt",
		ToolCallID:  "tool_receipt_1",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"stored receipt","tool_call_id":"tool_receipt_1","metadata":{}}`),
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.SaveToolReceipt(ctx, receipt))

	loaded, err := store.GetToolReceipt(ctx, "session-receipt", "tool_receipt_1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "team_echo", loaded.ToolName)
	assert.JSONEq(t, `{"role":"tool","content":"stored receipt","tool_call_id":"tool_receipt_1","metadata":{}}`, string(loaded.MessageJSON))

	require.NoError(t, store.DeleteToolReceipt(ctx, "session-receipt", "tool_receipt_1"))
	loaded, err = store.GetToolReceipt(ctx, "session-receipt", "tool_receipt_1")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestSQLiteRuntimeStoreListsToolReceiptsByRecency(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-tool-receipt-list-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	first := ToolExecutionReceipt{
		SessionID:   "session-receipt-list",
		ToolCallID:  "tool_receipt_old",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"old","tool_call_id":"tool_receipt_old","metadata":{}}`),
		CreatedAt:   time.Now().UTC().Add(-1 * time.Minute),
	}
	second := ToolExecutionReceipt{
		SessionID:   "session-receipt-list",
		ToolCallID:  "tool_receipt_new",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"new","tool_call_id":"tool_receipt_new","metadata":{}}`),
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.SaveToolReceipt(ctx, first))
	require.NoError(t, store.SaveToolReceipt(ctx, second))

	receipts, err := store.ListToolReceipts(ctx, "session-receipt-list", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	assert.Equal(t, "tool_receipt_new", receipts[0].ToolCallID)
	assert.Equal(t, "tool_receipt_old", receipts[1].ToolCallID)
}

func TestSQLiteRuntimeStoreListsToolReceiptsByRecencyWithMixedTimestampPrecision(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-tool-receipt-mixed-precision-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	base := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   "session-receipt-mixed-precision",
		ToolCallID:  "tool_receipt_whole_second",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"whole-second","tool_call_id":"tool_receipt_whole_second","metadata":{}}`),
		CreatedAt:   base,
	}))
	require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   "session-receipt-mixed-precision",
		ToolCallID:  "tool_receipt_fractional",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"fractional","tool_call_id":"tool_receipt_fractional","metadata":{}}`),
		CreatedAt:   base.Add(100 * time.Millisecond),
	}))

	receipts, err := store.ListToolReceipts(ctx, "session-receipt-mixed-precision", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	assert.Equal(t, "tool_receipt_fractional", receipts[0].ToolCallID)
	assert.Equal(t, "tool_receipt_whole_second", receipts[1].ToolCallID)
}

func TestSQLiteRuntimeStoreMigratesToolReceiptOrderingColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB
		);

		CREATE TABLE session_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			type TEXT NOT NULL,
			trace_id TEXT,
			agent_name TEXT,
			tool_name TEXT,
			payload_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(session_id, seq)
		);

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (session_id, tool_call_id)
		);

		INSERT INTO session_tool_receipts (session_id, tool_call_id, tool_name, message_json, created_at) VALUES
			('session-receipt-migration', 'tool_receipt_whole_second', 'team_echo', '{"role":"tool","content":"whole-second","tool_call_id":"tool_receipt_whole_second","metadata":{}}', '2026-03-15T10:00:00Z'),
			('session-receipt-migration', 'tool_receipt_fractional', 'team_echo', '{"role":"tool","content":"fractional","tool_call_id":"tool_receipt_fractional","metadata":{}}', '2026-03-15T10:00:00.1Z');
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	receipts, err := store.ListToolReceipts(context.Background(), "session-receipt-migration", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	assert.Equal(t, "tool_receipt_fractional", receipts[0].ToolCallID)
	assert.Equal(t, "tool_receipt_whole_second", receipts[1].ToolCallID)
}
