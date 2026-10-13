package subagentbatch

import (
	"context"
	"testing"
)

// TestCountLiveTasksExcludesTerminal pins the live-slot boundary: only
// non-terminal task rows hold a slot, so a settled task frees it implicitly.
func TestCountLiveTasksExcludesTerminal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := Now()

	batch := &SubagentBatch{
		BatchID:         NewID("batch"),
		RootScopeID:     "scope-live",
		ParentSessionID: "sess-live",
		ExecutionMode:   ExecutionModeBackground,
		Status:          BatchRunning,
		TaskCount:       5,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	statuses := []TaskStatus{TaskPending, TaskReady, TaskRunning, TaskSucceeded, TaskFailed}
	tasks := make([]SubagentTaskRecord, 0, len(statuses))
	for i, status := range statuses {
		tasks = append(tasks, SubagentTaskRecord{
			TaskID:     NewID("task"),
			BatchID:    batch.BatchID,
			Status:     status,
			OrderIndex: i,
			UpdatedAt:  now,
			Version:    1,
		})
	}
	if created, err := store.CreateBatch(ctx, batch, tasks); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}

	counter, ok := store.(LiveTaskCounter)
	if !ok {
		t.Fatal("sqlite batch store must implement LiveTaskCounter")
	}

	got, err := counter.CountLiveTasks(ctx, LiveTaskFilter{RootScopeID: "scope-live"})
	if err != nil {
		t.Fatalf("CountLiveTasks: %v", err)
	}
	if got != 3 {
		t.Fatalf("live tasks = %d, want 3 (pending+ready+running)", got)
	}

	// Parent-session filter has the same result for this batch.
	got, err = counter.CountLiveTasks(ctx, LiveTaskFilter{ParentSessionID: "sess-live"})
	if err != nil {
		t.Fatalf("CountLiveTasks(parent): %v", err)
	}
	if got != 3 {
		t.Fatalf("live tasks by parent = %d, want 3", got)
	}

	// A different scope is isolated.
	got, err = counter.CountLiveTasks(ctx, LiveTaskFilter{RootScopeID: "other-scope"})
	if err != nil {
		t.Fatalf("CountLiveTasks(other): %v", err)
	}
	if got != 0 {
		t.Fatalf("live tasks for unrelated scope = %d, want 0", got)
	}

	// An empty filter counts across the whole store.
	got, err = counter.CountLiveTasks(ctx, LiveTaskFilter{})
	if err != nil {
		t.Fatalf("CountLiveTasks(all): %v", err)
	}
	if got != 3 {
		t.Fatalf("live tasks (all) = %d, want 3", got)
	}
}

// TestCountLiveTasksSumsAcrossBatches pins that the count spans every batch of
// the scope, which is what makes the ceiling a per-scope (not per-batch) budget.
func TestCountLiveTasksSumsAcrossBatches(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := Now()
	counter := store.(LiveTaskCounter)

	for i := 0; i < 3; i++ {
		batch := &SubagentBatch{
			BatchID:         NewID("batch"),
			RootScopeID:     "scope-sum",
			ParentSessionID: "sess-sum",
			ExecutionMode:   ExecutionModeBackground,
			Status:          BatchRunning,
			TaskCount:       2,
			CreatedAt:       now,
			UpdatedAt:       now,
			Version:         1,
		}
		tasks := []SubagentTaskRecord{
			{TaskID: NewID("task"), BatchID: batch.BatchID, Status: TaskRunning, OrderIndex: 0, UpdatedAt: now, Version: 1},
			{TaskID: NewID("task"), BatchID: batch.BatchID, Status: TaskSucceeded, OrderIndex: 1, UpdatedAt: now, Version: 1},
		}
		if created, err := store.CreateBatch(ctx, batch, tasks); err != nil || !created {
			t.Fatalf("CreateBatch %d: created=%v err=%v", i, created, err)
		}
	}

	got, err := counter.CountLiveTasks(ctx, LiveTaskFilter{RootScopeID: "scope-sum"})
	if err != nil {
		t.Fatalf("CountLiveTasks: %v", err)
	}
	if got != 3 {
		t.Fatalf("live tasks across 3 batches = %d, want 3", got)
	}
}
