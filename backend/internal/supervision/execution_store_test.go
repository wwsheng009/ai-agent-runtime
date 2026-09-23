package supervision

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testExecutionRunStore(t *testing.T, name string) *SQLiteSupervisionStore {
	t.Helper()
	store, err := NewSQLiteSupervisionStore(&StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleExecutionRun(runID string) ExecutionRun {
	now := time.Now().UTC()
	deadline := now.Add(30 * time.Minute)
	progressDeadline := now.Add(5 * time.Minute)
	return ExecutionRun{
		RunID:               runID,
		Kind:                RunKindAgentRun,
		Workflow:            RunWorkflowSpawnAgent,
		RootSessionID:       "root-session",
		ParentSessionID:     "parent-session",
		SessionID:           "child-1",
		AgentID:             "child-1",
		Attempt:             1,
		Status:              RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           now,
		LastHeartbeatAt:     now,
		LastProgressAt:      now,
		ProgressSeq:         1,
		ExecutionDeadlineAt: &deadline,
		ProgressDeadlineAt:  &progressDeadline,
		MaxAttempts:         1,
		FencingToken:        1,
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

func TestExecutionRunStore_CreateGetAndCAS(t *testing.T) {
	store := testExecutionRunStore(t, "run-create")
	ctx := context.Background()
	run := sampleExecutionRun("run_1")

	created, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.True(t, created)

	// Idempotent create returns false, not an error.
	created, err = store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.False(t, created)

	got, err := store.GetExecutionRun(ctx, "run_1")
	require.NoError(t, err)
	require.Equal(t, RunStatusRunning, got.Status)
	require.Equal(t, int64(1), got.Version)
	require.Equal(t, "root-session", got.RootSessionID)
	require.Equal(t, "child-1", got.SessionID)
	require.False(t, got.ExecutionDeadlineAt.IsZero() || got.ExecutionDeadlineAt == nil)

	// CAS with stale version conflicts.
	stale := *got
	stale.Version = 1
	stale.Status = RunStatusCancelRequested
	ok, err := store.UpdateExecutionRunCAS(ctx, stale, 5)
	require.ErrorIs(t, err, ErrRunConflict)
	require.False(t, ok)

	// CAS with correct version succeeds and bumps version.
	ok, err = store.UpdateExecutionRunCAS(ctx, stale, 1)
	require.NoError(t, err)
	require.True(t, ok)
	got, err = store.GetExecutionRun(ctx, "run_1")
	require.NoError(t, err)
	require.Equal(t, int64(2), got.Version)
	require.Equal(t, RunStatusCancelRequested, got.Status)
}

func TestExecutionRunStore_MissingRun(t *testing.T) {
	store := testExecutionRunStore(t, "run-missing")
	ctx := context.Background()
	_, err := store.GetExecutionRun(ctx, "run_nope")
	require.ErrorIs(t, err, ErrRunNotFound)
}

func TestExecutionRunStore_RecordProgressMonotonicAndTerminalGuard(t *testing.T) {
	store := testExecutionRunStore(t, "run-progress")
	ctx := context.Background()
	run := sampleExecutionRun("run_2")
	_, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)

	now := time.Now().UTC()
	ok, err := store.RecordExecutionProgress(ctx, RunProgressEvent{RunID: "run_2", Kind: "tool_call_end"}, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)

	// Out-of-order seq must not move progress_seq backwards.
	ok, err = store.RecordExecutionProgress(ctx, RunProgressEvent{RunID: "run_2", Kind: "late", Seq: 1}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	got, err := store.GetExecutionRun(ctx, "run_2")
	require.NoError(t, err)
	require.Equal(t, int64(2), got.ProgressSeq)

	// Terminal run rejects progress.
	ok, err = store.MarkExecutionRunTerminal(ctx, "run_2", RunStatusSucceeded, "", "result-ref", now)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.RecordExecutionProgress(ctx, RunProgressEvent{RunID: "run_2", Kind: "late"}, now)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestExecutionRunStore_RequestCancelAndTerminal(t *testing.T) {
	store := testExecutionRunStore(t, "run-cancel")
	ctx := context.Background()
	run := sampleExecutionRun("run_3")
	_, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)

	now := time.Now().UTC()
	ok, err := store.RequestExecutionCancel(ctx, "run_3", "execution_deadline", 15*time.Second, now)
	require.NoError(t, err)
	require.True(t, ok)

	got, err := store.GetExecutionRun(ctx, "run_3")
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "execution_deadline", got.CancelSource)
	require.NotNil(t, got.CancelDeadlineAt)
	require.False(t, got.CancelDeadlineAt.Before(now.Add(10*time.Second)))

	// Second cancel request is a no-op (already canceling).
	ok, err = store.RequestExecutionCancel(ctx, "run_3", "progress_stalled", 15*time.Second, now)
	require.NoError(t, err)
	require.False(t, ok)

	// Terminal transition from cancel_requested works.
	ok, err = store.MarkExecutionRunTerminal(ctx, "run_3", RunStatusTimedOut, "execution_deadline", "", now.Add(30*time.Second))
	require.NoError(t, err)
	require.True(t, ok)

	// Terminal->terminal is idempotent.
	ok, err = store.MarkExecutionRunTerminal(ctx, "run_3", RunStatusFailed, "", "", now.Add(40*time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	got, err = store.GetExecutionRun(ctx, "run_3")
	require.NoError(t, err)
	require.Equal(t, RunStatusTimedOut, got.Status)
}

func TestExecutionRunStore_ListActiveAndBySession(t *testing.T) {
	store := testExecutionRunStore(t, "run-list")
	ctx := context.Background()

	runA := sampleExecutionRun("run_a")
	runA.SessionID = "child-a"
	runB := sampleExecutionRun("run_b")
	runB.SessionID = "child-b"
	runB.Status = RunStatusQueued
	runC := sampleExecutionRun("run_c")
	runC.SessionID = "child-c"
	_, err := store.CreateExecutionRun(ctx, runA)
	require.NoError(t, err)
	_, err = store.CreateExecutionRun(ctx, runB)
	require.NoError(t, err)
	_, err = store.CreateExecutionRun(ctx, runC)
	require.NoError(t, err)

	_, err = store.MarkExecutionRunTerminal(ctx, "run_c", RunStatusSucceeded, "", "", time.Now().UTC())
	require.NoError(t, err)

	active, err := store.ListActiveExecutionRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, active, 2)
	statuses := map[string]bool{}
	for _, run := range active {
		statuses[run.RunID] = true
	}
	require.True(t, statuses["run_a"])
	require.True(t, statuses["run_b"])

	bySession, err := store.ListExecutionRunsBySession(ctx, "child-a", 5)
	require.NoError(t, err)
	require.Len(t, bySession, 1)
	require.Equal(t, "run_a", bySession[0].RunID)
}

func TestCompletionOutbox_EnqueueDeliverAndRedeliver(t *testing.T) {
	store := testExecutionRunStore(t, "outbox")
	ctx := context.Background()
	now := time.Now().UTC()

	payload, err := MarshalOutboxPayloadJSON(map[string]interface{}{
		"agent_id": "child-1",
		"status":   "succeeded",
	})
	require.NoError(t, err)

	entry := CompletionOutboxEntry{
		OutboxID:        "outbox_1",
		RunID:           "run_9",
		SessionID:       "child-1",
		ParentSessionID: "parent-session",
		RootSessionID:   "root-session",
		Status:          RunStatusSucceeded,
		IdempotencyKey:  "subagent_completion:run_9:3",
		PayloadJSON:     payload,
		CreatedAt:       now,
	}
	created, err := store.EnqueueCompletionOutbox(ctx, entry)
	require.NoError(t, err)
	require.True(t, created)

	// Same idempotency key is a no-op.
	created, err = store.EnqueueCompletionOutbox(ctx, entry)
	require.NoError(t, err)
	require.False(t, created)

	pending, err := store.ListUndeliveredOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "run_9", pending[0].RunID)

	// Simulate failed delivery then successful delivery.
	ok, err := store.MarkOutboxFailed(ctx, "outbox_1", "parent mailbox not found", now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.MarkOutboxDelivered(ctx, "outbox_1", 42, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, ok)

	pending, err = store.ListUndeliveredOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 0)

	// Second deliver is a no-op.
	ok, err = store.MarkOutboxDelivered(ctx, "outbox_1", 43, now.Add(3*time.Second))
	require.NoError(t, err)
	require.False(t, ok)
}

func TestExecutionRunStore_TerminalWriteRejectsInvalidStatus(t *testing.T) {
	store := testExecutionRunStore(t, "run-invalid")
	ctx := context.Background()
	run := sampleExecutionRun("run_4")
	_, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	_, err = store.MarkExecutionRunTerminal(ctx, "run_4", "still_running", "", "", time.Now().UTC())
	require.Error(t, err)
}

func TestExecutionRunStore_CancelThenConflictOnStaleTerminal(t *testing.T) {
	store := testExecutionRunStore(t, "run-fence")
	ctx := context.Background()
	run := sampleExecutionRun("run_5")
	_, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)

	// Unknown run: terminal write returns conflict.
	_, err = store.MarkExecutionRunTerminal(ctx, "run_missing", RunStatusFailed, "", "", time.Now().UTC())
	require.ErrorIs(t, err, ErrRunConflict)
	require.False(t, errors.Is(err, ErrRunNotFound))
}

// C1-3 / AC-P0-3a + AC-P0-3b: the turn/extension ledger fields round-trip
// through the store and the additive migration is safe to re-run.
func TestExecutionRunStore_TurnLedgerFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervision.db")
	ctx := context.Background()
	store, err := NewSQLiteSupervisionStore(&StoreConfig{Path: path})
	require.NoError(t, err)

	run := sampleExecutionRun("run_turn_ledger")
	window := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	run.TurnID = "turn-42"
	run.DeclaredBudget = 20 * time.Minute
	run.DecisionWindowUntil = &window
	created, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.True(t, created)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "turn-42", got.TurnID)
	require.Equal(t, 20*time.Minute, got.DeclaredBudget)
	require.Zero(t, got.ExtensionCount)
	require.Zero(t, got.ExtendedTotal)
	require.NotNil(t, got.DecisionWindowUntil)
	require.WithinDuration(t, window, *got.DecisionWindowUntil, time.Second)

	// Extension accounting is written through the CAS path (I5 counters).
	extendedDeadline := got.ExecutionDeadlineAt.Add(15 * time.Minute)
	updated := *got
	updated.ExtensionCount = 2
	updated.ExtendedTotal = 15 * time.Minute
	updated.ExecutionDeadlineAt = &extendedDeadline
	ok, err := store.UpdateExecutionRunCAS(ctx, updated, got.Version)
	require.NoError(t, err)
	require.True(t, ok)

	after, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, 2, after.ExtensionCount)
	require.Equal(t, 15*time.Minute, after.ExtendedTotal)
	require.WithinDuration(t, extendedDeadline, *after.ExecutionDeadlineAt, time.Second)

	// Reopening re-applies the migration chain; it must be idempotent
	// (AC-P0-3a) and the new columns must read back unchanged.
	require.NoError(t, store.Close())
	reopened, err := NewSQLiteSupervisionStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	again, err := reopened.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "turn-42", again.TurnID)
	require.Equal(t, 20*time.Minute, again.DeclaredBudget)
	require.Equal(t, 2, again.ExtensionCount)
	require.Equal(t, 15*time.Minute, again.ExtendedTotal)

	// A row written before the migration (only the legacy columns are set) must
	// scan back with zero values for the new fields instead of failing.
	db, err := reopened.dbOrErr()
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `INSERT INTO supervision_execution_runs (
		run_id, kind, workflow, root_session_id, parent_session_id, parent_run_id,
		session_id, agent_id, attempt, status, owner_id, started_at,
		last_heartbeat_at, last_progress_at, progress_seq, cancel_source,
		max_attempts, fencing_token, result_ref, error_code, version,
		created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"run_legacy", RunKindAgentRun, RunWorkflowSpawnAgent, "root-session",
		"parent-session", "", "child-legacy", "child-legacy", 1, RunStatusRunning,
		"host-1", formatRunTime(now), formatRunTime(now), formatRunTime(now), 3,
		"", 1, 1, "", "", 1,
		formatRunTime(now), formatRunTime(now))
	require.NoError(t, err)

	legacy, err := reopened.GetExecutionRun(ctx, "run_legacy")
	require.NoError(t, err)
	require.Empty(t, legacy.TurnID)
	require.Zero(t, legacy.DeclaredBudget)
	require.Zero(t, legacy.ExtensionCount)
	require.Zero(t, legacy.ExtendedTotal)
	require.Nil(t, legacy.DecisionWindowUntil)
}
