package subagentbatch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
