package subagentbatch

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
)

func newTestStore(t *testing.T) BatchStore {
	t.Helper()
	store, err := NewSQLiteBatchStore(nil)
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleBatch(t *testing.T, idemKey, parentSession string) (*SubagentBatch, []SubagentTaskRecord) {
	t.Helper()
	now := Now()
	batch := &SubagentBatch{
		BatchID:         NewID("batch"),
		RootScopeID:     "scope-1",
		ParentSessionID: parentSession,
		ParentTurnID:    "turn-1",
		TraceID:         "trace-1",
		ExecutionMode:   ExecutionModeBackground,
		Status:          BatchQueued,
		IdempotencyKey:  idemKey,
		TaskCount:       2,
		QueuedCount:     2,
		CreatedAt:       now,
		UpdatedAt:       now,
		BatchDeadline:   now.Add(timeHour),
		Version:         1,
	}
	tasks := []SubagentTaskRecord{
		{
			TaskID:     "task-a",
			BatchID:    batch.BatchID,
			Role:       "writer",
			Difficulty: "easy",
			Status:     TaskReady,
			OrderIndex: 1,
			Spec:       []byte(`{"id":"task-a","role":"writer"}`),
			UpdatedAt:  now,
			Version:    1,
		},
		{
			TaskID:        "task-b",
			BatchID:       batch.BatchID,
			ParentTaskID:  "task-a",
			DependencyIDs: []string{"task-a"},
			Role:          "reader",
			Difficulty:    "normal",
			Status:        TaskPending,
			OrderIndex:    2,
			Spec:          []byte(`{"id":"task-b","role":"reader"}`),
			UpdatedAt:     now,
			Version:       1,
		},
	}
	return batch, tasks
}

func TestCreateGetAndRecoverable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "idem-1", "session-1")

	created, err := store.CreateBatch(ctx, batch, tasks)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if !created {
		t.Fatalf("CreateBatch created = false, want true")
	}

	got, err := store.GetBatch(ctx, batch.BatchID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if got == nil {
		t.Fatalf("GetBatch returned nil")
	}
	if got.BatchID != batch.BatchID || got.ExecutionMode != ExecutionModeBackground {
		t.Errorf("batch round-trip mismatch: %+v", got)
	}
	if got.TaskCount != 2 || got.QueuedCount != 2 {
		t.Errorf("task counts mismatch: %+v", got)
	}

	gotTasks, err := store.ListTasks(ctx, batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(gotTasks) != 2 {
		t.Fatalf("ListTasks len = %d, want 2", len(gotTasks))
	}
	// ordering by order_index
	if gotTasks[0].TaskID != "task-a" || gotTasks[1].TaskID != "task-b" {
		t.Errorf("ListTasks ordering mismatch: %+v", gotTasks)
	}
	if !strings.EqualFold(gotTasks[1].DependencyIDs[0], "task-a") {
		t.Errorf("dependency round-trip mismatch: %+v", gotTasks[1].DependencyIDs)
	}

	recs, err := store.Recoverable(ctx, 10)
	if err != nil {
		t.Fatalf("Recoverable: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.BatchID == batch.BatchID {
			found = true
		}
	}
	if !found {
		t.Errorf("Recoverable did not include queued batch")
	}

	// missing batch returns (nil, nil)
	missing, err := store.GetBatch(ctx, "batch_does_not_exist")
	if err != nil {
		t.Fatalf("GetBatch(missing): %v", err)
	}
	if missing != nil {
		t.Errorf("GetBatch(missing) = %v, want nil", missing)
	}
}

func TestIdempotentReplayReturnsExisting(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "shared-key", "session-9")

	created, err := store.CreateBatch(ctx, batch, tasks)
	if err != nil || !created {
		t.Fatalf("first CreateBatch created=%v err=%v", created, err)
	}
	firstID := batch.BatchID

	// Replay with a brand-new batch struct carrying the same key+parent.
	dup, dupTasks := sampleBatch(t, "shared-key", "session-9")
	created, err = store.CreateBatch(ctx, dup, dupTasks)
	if err != nil {
		t.Fatalf("replay CreateBatch: %v", err)
	}
	if created {
		t.Fatalf("replay CreateBatch created = true, want idempotent false")
	}
	if dup.BatchID != firstID {
		t.Errorf("replay returned batch id %q, want existing %q", dup.BatchID, firstID)
	}

	// A different parent session with the same key must create its own batch.
	other, otherTasks := sampleBatch(t, "shared-key", "session-other")
	created, err = store.CreateBatch(ctx, other, otherTasks)
	if err != nil || !created {
		t.Fatalf("other-parent CreateBatch created=%v err=%v", created, err)
	}
	if other.BatchID == firstID {
		t.Errorf("different parent must not collide on idempotency key")
	}
}

func TestUpdateBatchCAS(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-1")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	// stale version -> conflict
	_, err := store.UpdateBatch(ctx, batch.BatchID, batch.Version+99, func(b *SubagentBatch) {
		b.Status = BatchRunning
	})
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("UpdateBatch(stale) err = %v, want VersionConflictError", err)
	}

	// correct version -> applied, version bumps
	updated, err := store.UpdateBatch(ctx, batch.BatchID, batch.Version, func(b *SubagentBatch) {
		b.Status = BatchRunning
		b.OwnerID = "session-1"
	})
	if err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	if updated.Version != batch.Version+1 {
		t.Errorf("version = %d, want %d", updated.Version, batch.Version+1)
	}
	if updated.Status != BatchRunning || updated.OwnerID != "session-1" {
		t.Errorf("update not applied: %+v", updated)
	}

	// illegal transition -> error, store unchanged. Use a fresh queued batch so
	// the from-state is verifiably non-terminal.
	bad, badTasks := sampleBatch(t, "", "session-bad")
	if _, err := store.CreateBatch(ctx, bad, badTasks); err != nil {
		t.Fatalf("CreateBatch(bad): %v", err)
	}
	if _, err := store.UpdateBatch(ctx, bad.BatchID, -1, func(b *SubagentBatch) {
		b.Status = BatchCompleted
	}); err == nil {
		t.Errorf("UpdateBatch(queued->completed) = nil, want invalid transition error")
	}
	badGot, _ := store.GetBatch(ctx, bad.BatchID)
	if badGot.Status != BatchQueued {
		t.Errorf("store mutated despite invalid transition: status = %s", badGot.Status)
	}

	// first batch is running now
	got, _ := store.GetBatch(ctx, batch.BatchID)
	if got.Status != BatchRunning {
		t.Errorf("batch status = %s, want running", got.Status)
	}
}

func TestUpdateTaskCASAndResult(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-1")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	taskA, err := store.GetTask(ctx, batch.BatchID, "task-a")
	if err != nil || taskA == nil {
		t.Fatalf("GetTask: err=%v task=%+v", err, taskA)
	}

	// stale task version -> conflict
	_, err = store.UpdateTask(ctx, batch.BatchID, "task-a", taskA.Version+5, func(t *SubagentTaskRecord) {
		t.Status = TaskRunning
	})
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("UpdateTask(stale) err = %v, want VersionConflictError", err)
	}

	// correct version -> running
	run, err := store.UpdateTask(ctx, batch.BatchID, "task-a", taskA.Version, func(t *SubagentTaskRecord) {
		t.Status = TaskRunning
	})
	if err != nil {
		t.Fatalf("UpdateTask(running): %v", err)
	}
	if run.Status != TaskRunning || run.Version != taskA.Version+1 {
		t.Errorf("UpdateTask not applied: %+v", run)
	}

	// RecordTaskResult -> terminal + result capsule
	err = store.RecordTaskResult(ctx, batch.BatchID, "task-a", run.Version, TaskSucceeded, &TaskResult{
		TaskID:   "task-a",
		Role:     "writer",
		Success:  true,
		Summary:  "done",
		Findings: []string{"f1"},
	})
	if err != nil {
		t.Fatalf("RecordTaskResult: %v", err)
	}
	finalTask, err := store.GetTask(ctx, batch.BatchID, "task-a")
	if err != nil {
		t.Fatalf("GetTask(final): %v", err)
	}
	if finalTask.Status != TaskSucceeded {
		t.Errorf("task status = %s, want succeeded", finalTask.Status)
	}
	if finalTask.ResultSummary == nil || !strings.Contains(string(finalTask.ResultSummary), "done") {
		t.Errorf("result summary not persisted: %s", finalTask.ResultSummary)
	}
	if finalTask.Version != run.Version+1 {
		t.Errorf("version after result = %d, want %d", finalTask.Version, run.Version+1)
	}

	// terminal task rejects further transition
	err = store.RecordTaskResult(ctx, batch.BatchID, "task-a", finalTask.Version, TaskFailed, &TaskResult{TaskID: "task-a", Error: "late"})
	if err == nil {
		t.Errorf("RecordTaskResult on terminal task should error")
	}
}

func TestRecordTaskResultRejectsLateWriteAfterBatchOrphaned(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-late-worker")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	task, err := store.GetTask(ctx, batch.BatchID, "task-a")
	if err != nil || task == nil {
		t.Fatalf("GetTask: task=%+v err=%v", task, err)
	}
	task, err = store.UpdateTask(ctx, batch.BatchID, task.TaskID, task.Version, func(t *SubagentTaskRecord) {
		t.Status = TaskRunning
	})
	if err != nil {
		t.Fatalf("UpdateTask(running): %v", err)
	}
	if _, err := store.UpdateBatch(ctx, batch.BatchID, batch.Version, func(b *SubagentBatch) {
		b.Status = BatchOrphaned
	}); err != nil {
		t.Fatalf("UpdateBatch(orphaned): %v", err)
	}

	err = store.RecordTaskResult(ctx, batch.BatchID, task.TaskID, task.Version, TaskSucceeded, &TaskResult{
		TaskID:  task.TaskID,
		Success: true,
		Summary: "late result",
	})
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("late RecordTaskResult err = %v, want batch VersionConflictError", err)
	}
	unchanged, err := store.GetTask(ctx, batch.BatchID, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask(after late result): %v", err)
	}
	if unchanged.Status != TaskRunning || unchanged.Version != task.Version {
		t.Fatalf("late result mutated task: before=%+v after=%+v", task, unchanged)
	}
}

func TestTaskWritesRejectLateProgressAfterBatchOrphaned(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-late-progress")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	task, err := store.GetTask(ctx, batch.BatchID, "task-a")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	task, err = store.UpdateTask(ctx, batch.BatchID, task.TaskID, task.Version, func(t *SubagentTaskRecord) {
		t.Status = TaskRunning
	})
	if err != nil {
		t.Fatalf("UpdateTask(running): %v", err)
	}
	current, err := store.GetBatch(ctx, batch.BatchID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if _, err := store.UpdateBatch(ctx, batch.BatchID, current.Version, func(b *SubagentBatch) {
		b.Status = BatchOrphaned
	}); err != nil {
		t.Fatalf("UpdateBatch(orphaned): %v", err)
	}

	now := Now()
	_, err = store.UpdateTask(ctx, batch.BatchID, task.TaskID, task.Version, func(t *SubagentTaskRecord) {
		t.LastProgressAt = &now
	})
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("late UpdateTask err = %v, want batch VersionConflictError", err)
	}
	unchanged, err := store.GetTask(ctx, batch.BatchID, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask(after late progress): %v", err)
	}
	if unchanged.Status != TaskRunning || unchanged.Version != task.Version || unchanged.LastProgressAt != nil {
		t.Fatalf("late progress mutated task: before=%+v after=%+v", task, unchanged)
	}
}

func TestCanceledBatchAllowsUnownedTaskCancellationButRejectsLateResult(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-cancel-fence")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	current, err := store.GetBatch(ctx, batch.BatchID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if _, err := store.UpdateBatch(ctx, batch.BatchID, current.Version, func(b *SubagentBatch) {
		b.Status = BatchCanceled
	}); err != nil {
		t.Fatalf("UpdateBatch(canceled): %v", err)
	}
	task, err := store.GetTask(ctx, batch.BatchID, "task-a")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	task, err = store.UpdateTask(ctx, batch.BatchID, task.TaskID, task.Version, func(t *SubagentTaskRecord) {
		t.Status = TaskCanceled
	})
	if err != nil {
		t.Fatalf("UpdateTask(cancel pending): %v", err)
	}
	err = store.RecordTaskResult(ctx, batch.BatchID, task.TaskID, task.Version, TaskSucceeded, &TaskResult{
		TaskID:  task.TaskID,
		Success: true,
	})
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("late result after task cancellation err = %v, want batch VersionConflictError", err)
	}
	unchanged, err := store.GetTask(ctx, batch.BatchID, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask(after late result): %v", err)
	}
	if unchanged.Status != TaskCanceled || unchanged.Version != task.Version {
		t.Fatalf("late result mutated canceled task: before=%+v after=%+v", task, unchanged)
	}
}

func TestUpdateTasksValidatesTransitions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-1")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	// Legal transitions on both rows are applied atomically.
	if err := store.UpdateTasks(ctx, batch.BatchID, map[string]TaskUpdate{
		"task-a": func(t *SubagentTaskRecord) { t.Status = TaskRunning },
		"task-b": func(t *SubagentTaskRecord) { t.Status = TaskReady },
	}); err != nil {
		t.Fatalf("UpdateTasks(legal): %v", err)
	}
	for id, want := range map[string]TaskStatus{"task-a": TaskRunning, "task-b": TaskReady} {
		got, _ := store.GetTask(ctx, batch.BatchID, id)
		if got == nil || got.Status != want {
			t.Errorf("task %s status = %v, want %s", id, got, want)
		}
	}

	// An illegal transition is rejected and the whole transaction rolls back.
	err := store.UpdateTasks(ctx, batch.BatchID, map[string]TaskUpdate{
		"task-b": func(t *SubagentTaskRecord) { t.Status = TaskSucceeded },
	})
	if err == nil {
		t.Fatalf("UpdateTasks(pending->succeeded) = nil, want invalid transition error")
	}
	if got, _ := store.GetTask(ctx, batch.BatchID, "task-b"); got == nil || got.Status != TaskReady {
		t.Errorf("task-b status = %v, want TaskReady (unchanged after rollback)", got)
	}
}

func TestListBatchesFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-1")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	byParent, err := store.ListBatches(ctx, BatchFilter{ParentSessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListBatches(parent): %v", err)
	}
	if len(byParent) != 1 {
		t.Errorf("ListBatches(parent) len = %d, want 1", len(byParent))
	}

	byStatus, err := store.ListBatches(ctx, BatchFilter{Status: []BatchStatus{BatchRunning}})
	if err != nil {
		t.Fatalf("ListBatches(status): %v", err)
	}
	if len(byStatus) != 0 {
		t.Errorf("ListBatches(running) len = %d, want 0", len(byStatus))
	}

	byMode, err := store.ListBatches(ctx, BatchFilter{ExecutionMode: []ExecutionMode{ExecutionModeBackground}})
	if err != nil {
		t.Fatalf("ListBatches(mode): %v", err)
	}
	if len(byMode) != 1 {
		t.Errorf("ListBatches(background) len = %d, want 1", len(byMode))
	}
}

func TestFileBackedStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagent-batches.db")
	store, err := NewSQLiteBatchStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore(file): %v", err)
	}
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-file")
	if _, err := store.CreateBatch(ctx, batch, tasks); err != nil {
		t.Fatalf("CreateBatch(file): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the same file and read the batch back.
	store2, err := NewSQLiteBatchStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = store2.Close() }()
	got, err := store2.GetBatch(ctx, batch.BatchID)
	if err != nil || got == nil {
		t.Fatalf("GetBatch after reopen: err=%v got=%v", err, got)
	}
	if got.Status != BatchQueued {
		t.Errorf("status after reopen = %s, want queued", got.Status)
	}
}

// timeHour allows the sample helper to express a deadline without extra imports.
const timeHour = time.Hour

// TestTaskTypeSubjectColumnsMigrateLegacyDatabase pins the old-database path
// (plan §6.4 B-2): a store file whose schema stopped at migration v1 has no
// task_type/task_subject columns. Opening it with the current code must add the
// columns, read pre-existing rows back with empty strings (byte-identical to
// today), and accept the new fields through the normal CAS write path.
func TestTaskTypeSubjectColumnsMigrateLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-subagent-batches.db")

	// Build a v1-era database exactly as an older release would have left it:
	// v1 recorded in schema_migrations plus a task row written without the new
	// columns.
	legacy, err := sql.Open("sqlite3", batchDSNOptions(path))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	legacy.SetMaxOpenConns(1)
	if err := migrate.Apply(ctx, legacy, batchMigrations[:1]); err != nil {
		t.Fatalf("apply v1 schema: %v", err)
	}
	stamp := Now().UTC().Format(time.RFC3339Nano)
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO subagent_batches (batch_id, status, execution_mode, created_at, updated_at, heartbeat_at, version)
		VALUES ('batch-legacy', 'queued', 'wait', ?, ?, ?, 1)`, stamp, stamp, stamp); err != nil {
		t.Fatalf("insert legacy batch: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO subagent_tasks (task_id, batch_id, role, difficulty, status, order_index, updated_at, spec_json, version)
		VALUES ('task-legacy', 'batch-legacy', 'writer', 'hard', 'ready', 1, ?, '{"id":"task-legacy","role":"writer","difficulty":"hard"}', 1)`,
		stamp); err != nil {
		t.Fatalf("insert legacy task: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := NewSQLiteBatchStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()

	got, err := store.GetTask(ctx, "batch-legacy", "task-legacy")
	if err != nil || got == nil {
		t.Fatalf("GetTask(legacy): task=%+v err=%v", got, err)
	}
	if got.TaskType != "" || got.TaskSubject != "" {
		t.Fatalf("legacy row task_type/task_subject = %q/%q, want empty strings", got.TaskType, got.TaskSubject)
	}
	if got.Role != "writer" || got.Difficulty != "hard" {
		t.Fatalf("legacy row role/difficulty = %q/%q, want writer/hard", got.Role, got.Difficulty)
	}

	// The migrated columns are writable through the standard CAS path.
	updated, err := store.UpdateTask(ctx, "batch-legacy", "task-legacy", got.Version, func(record *SubagentTaskRecord) {
		record.TaskType = "implement"
		record.TaskSubject = "legacy row gains routing audit fields"
	})
	if err != nil {
		t.Fatalf("UpdateTask(migrated row): %v", err)
	}
	if updated.TaskType != "implement" || updated.TaskSubject != "legacy row gains routing audit fields" {
		t.Fatalf("UpdateTask returned task_type/task_subject = %q/%q", updated.TaskType, updated.TaskSubject)
	}
	again, err := store.GetTask(ctx, "batch-legacy", "task-legacy")
	if err != nil || again == nil {
		t.Fatalf("GetTask(after update): task=%+v err=%v", again, err)
	}
	if again.TaskType != "implement" || again.TaskSubject != "legacy row gains routing audit fields" {
		t.Fatalf("task_type/task_subject after update = %q/%q", again.TaskType, again.TaskSubject)
	}
}

// TestTaskRecordReadProjectionRoundTripsWriteColumns pins the read projection
// against the write projection: every column serialized by
// insertTaskRow/overwriteTaskRow must land back on SubagentTaskRecord through
// both GetTask and ListTasks. Before the fix the nullable time columns
// (task_deadline/started_at/finished_at/last_progress_at) were scanned into
// locals and dropped, so LastProgressAt write-back and FinishedAt-based result
// projections were invisible to every reader.
func TestTaskRecordReadProjectionRoundTripsWriteColumns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	batch, tasks := sampleBatch(t, "", "session-task-projection")
	base := Now()
	deadline := base.Add(30 * time.Minute)
	started := base.Add(time.Second)
	finished := base.Add(2 * time.Second)
	progress := base.Add(3 * time.Second)

	tasks[0].ParentTaskID = "task-parent"
	tasks[0].DependencyIDs = []string{"dep-a", "dep-b"}
	tasks[0].ChildSessionID = "child-session-a"
	tasks[0].TaskType = "implement"
	tasks[0].TaskSubject = "add task_type/task_subject passthrough"
	tasks[0].ReadOnly = true
	tasks[0].Attempt = 3
	tasks[0].TaskDeadline = deadline
	tasks[0].StartedAt = &started
	tasks[0].FinishedAt = &finished
	tasks[0].LastProgressAt = &progress

	if created, err := store.CreateBatch(ctx, batch, tasks); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}

	got, err := store.GetTask(ctx, batch.BatchID, tasks[0].TaskID)
	if err != nil || got == nil {
		t.Fatalf("GetTask: task=%+v err=%v", got, err)
	}
	assertTaskRecordProjection(t, "GetTask", *got, tasks[0])

	listed, err := store.ListTasks(ctx, batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListTasks len = %d, want 2", len(listed))
	}
	var listedFirst SubagentTaskRecord
	for _, record := range listed {
		if record.TaskID == tasks[0].TaskID {
			listedFirst = record
		}
	}
	if listedFirst.TaskID == "" {
		t.Fatalf("ListTasks did not return %q: %+v", tasks[0].TaskID, listed)
	}
	assertTaskRecordProjection(t, "ListTasks", listedFirst, tasks[0])

	// The sibling row must keep its NULL/zero values: the projection must not
	// leak stamps across rows.
	sibling := listed[1]
	if sibling.TaskID != "task-b" {
		t.Fatalf("ListTasks ordering mismatch: %+v", listed)
	}
	if sibling.TaskDeadline != (time.Time{}) || sibling.StartedAt != nil || sibling.FinishedAt != nil || sibling.LastProgressAt != nil {
		t.Fatalf("task-b nullable time columns = deadline=%v started=%v finished=%v progress=%v, want zero/nil", sibling.TaskDeadline, sibling.StartedAt, sibling.FinishedAt, sibling.LastProgressAt)
	}
	if sibling.TaskType != "" || sibling.TaskSubject != "" {
		t.Fatalf("task-b task_type/task_subject = %q/%q, want empty (omitted fields stay absent)", sibling.TaskType, sibling.TaskSubject)
	}

	// UpdateTask writes LastProgressAt, and the stamp must be visible to the
	// very next read (this is what the M1 throttled write-back relies on).
	visible := progress.Add(time.Second)
	updated, err := store.UpdateTask(ctx, batch.BatchID, got.TaskID, got.Version, func(record *SubagentTaskRecord) {
		record.LastProgressAt = &visible
	})
	if err != nil {
		t.Fatalf("UpdateTask(last progress): %v", err)
	}
	if updated == nil || updated.LastProgressAt == nil || !updated.LastProgressAt.Equal(visible) {
		t.Fatalf("UpdateTask returned progress = %+v, want %v", updated.LastProgressAt, visible)
	}
	again, err := store.GetTask(ctx, batch.BatchID, got.TaskID)
	if err != nil {
		t.Fatalf("GetTask(after UpdateTask): %v", err)
	}
	if again.LastProgressAt == nil || !again.LastProgressAt.Equal(visible) {
		t.Fatalf("LastProgressAt after UpdateTask = %v, want %v", again.LastProgressAt, visible)
	}
	// Unrelated columns survive the same write.
	if again.StartedAt == nil || !again.StartedAt.Equal(started) {
		t.Fatalf("StartedAt after UpdateTask = %v, want %v", again.StartedAt, started)
	}
	if again.FinishedAt == nil || !again.FinishedAt.Equal(finished) {
		t.Fatalf("FinishedAt after UpdateTask = %v, want %v", again.FinishedAt, finished)
	}
	if !again.TaskDeadline.Equal(deadline) {
		t.Fatalf("TaskDeadline after UpdateTask = %v, want %v", again.TaskDeadline, deadline)
	}
	if again.TaskType != "implement" || again.TaskSubject != "add task_type/task_subject passthrough" {
		t.Fatalf("task_type/task_subject after UpdateTask = %q/%q, want the CreateBatch values", again.TaskType, again.TaskSubject)
	}
}

func assertTaskRecordProjection(t *testing.T, source string, got, want SubagentTaskRecord) {
	t.Helper()
	if got.TaskID != want.TaskID || got.BatchID != want.BatchID || got.ParentTaskID != want.ParentTaskID {
		t.Errorf("%s: identity mismatch got=(%s/%s parent=%s) want=(%s/%s parent=%s)",
			source, got.TaskID, got.BatchID, got.ParentTaskID, want.TaskID, want.BatchID, want.ParentTaskID)
	}
	if strings.Join(got.DependencyIDs, ",") != strings.Join(want.DependencyIDs, ",") {
		t.Errorf("%s: DependencyIDs = %v, want %v", source, got.DependencyIDs, want.DependencyIDs)
	}
	if got.ChildSessionID != want.ChildSessionID {
		t.Errorf("%s: ChildSessionID = %q, want %q", source, got.ChildSessionID, want.ChildSessionID)
	}
	if got.Role != want.Role || got.Difficulty != want.Difficulty {
		t.Errorf("%s: role/difficulty = %q/%q, want %q/%q", source, got.Role, got.Difficulty, want.Role, want.Difficulty)
	}
	if got.TaskType != want.TaskType || got.TaskSubject != want.TaskSubject {
		t.Errorf("%s: task_type/task_subject = %q/%q, want %q/%q", source, got.TaskType, got.TaskSubject, want.TaskType, want.TaskSubject)
	}
	if got.ReadOnly != want.ReadOnly {
		t.Errorf("%s: ReadOnly = %v, want %v", source, got.ReadOnly, want.ReadOnly)
	}
	if got.Status != want.Status || got.OrderIndex != want.OrderIndex || got.Attempt != want.Attempt {
		t.Errorf("%s: status/order/attempt = %s/%d/%d, want %s/%d/%d",
			source, got.Status, got.OrderIndex, got.Attempt, want.Status, want.OrderIndex, want.Attempt)
	}
	if !got.TaskDeadline.Equal(want.TaskDeadline) {
		t.Errorf("%s: TaskDeadline = %v, want %v", source, got.TaskDeadline, want.TaskDeadline)
	}
	assertNullableTaskTime(t, source, "StartedAt", got.StartedAt, want.StartedAt)
	assertNullableTaskTime(t, source, "FinishedAt", got.FinishedAt, want.FinishedAt)
	assertNullableTaskTime(t, source, "LastProgressAt", got.LastProgressAt, want.LastProgressAt)
	// CreateBatch overwrites UpdatedAt with its own clock, so only non-zero is
	// asserted here; the UpdateTask write path is checked separately below.
	if got.UpdatedAt.IsZero() {
		t.Errorf("%s: UpdatedAt is zero", source)
	}
	if string(got.Spec) != string(want.Spec) {
		t.Errorf("%s: Spec = %s, want %s", source, got.Spec, want.Spec)
	}
	if got.ArtifactRef != want.ArtifactRef || got.ErrorClass != want.ErrorClass || got.ErrorCode != want.ErrorCode {
		t.Errorf("%s: artifact/error projection mismatch got=%+v want=%+v", source, got, want)
	}
	if got.Version != want.Version {
		t.Errorf("%s: Version = %d, want %d", source, got.Version, want.Version)
	}
}

func assertNullableTaskTime(t *testing.T, source, field string, got, want *time.Time) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("%s: %s = %v, want %v", source, field, got, want)
		return
	}
	if got != nil && !got.Equal(*want) {
		t.Errorf("%s: %s = %v, want %v", source, field, got, want)
	}
}
