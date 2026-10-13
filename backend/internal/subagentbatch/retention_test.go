package subagentbatch

import (
	"context"
	"testing"
	"time"
)

func seedBatch(t *testing.T, store BatchStore, scope string, status BatchStatus, finished *time.Time) string {
	t.Helper()
	now := Now()
	batch := &SubagentBatch{
		BatchID:         NewID("batch"),
		RootScopeID:     scope,
		ParentSessionID: "sess-" + scope,
		ExecutionMode:   ExecutionModeBackground,
		Status:          status,
		TaskCount:       1,
		CreatedAt:       now,
		UpdatedAt:       now,
		FinishedAt:      finished,
		Version:         1,
	}
	tasks := []SubagentTaskRecord{{
		TaskID:    NewID("task"),
		BatchID:   batch.BatchID,
		Status:    TaskSucceeded,
		UpdatedAt: now,
		Version:   1,
	}}
	if created, err := store.CreateBatch(context.Background(), batch, tasks); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}
	return batch.BatchID
}

// TestPruneTerminalBatchesDeletesOnlyOldTerminal pins the retention boundary: a
// terminal batch older than the cutoff is deleted with its tasks, while recent
// terminal batches and still-running batches are untouched.
func TestPruneTerminalBatchesDeletesOnlyOldTerminal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pruner, ok := store.(TerminalBatchPruner)
	if !ok {
		t.Fatal("sqlite batch store must implement TerminalBatchPruner")
	}

	now := Now()
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-10 * time.Minute)

	oldID := seedBatch(t, store, "scope-prune", BatchCompleted, &old)
	recentID := seedBatch(t, store, "scope-prune", BatchFailed, &recent)
	liveID := seedBatch(t, store, "scope-prune", BatchRunning, nil)

	deleted, err := pruner.PruneTerminalBatches(ctx, now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("PruneTerminalBatches: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	if b, err := store.GetBatch(ctx, oldID); err != nil || b != nil {
		t.Fatalf("old terminal batch must be pruned, got batch=%v err=%v", b, err)
	}
	if b, err := store.GetBatch(ctx, recentID); err != nil || b == nil {
		t.Fatalf("recent terminal batch must survive, got batch=%v err=%v", b, err)
	}
	if b, err := store.GetBatch(ctx, liveID); err != nil || b == nil {
		t.Fatalf("running batch must survive, got batch=%v err=%v", b, err)
	}

	// The pruned batch's task rows are gone too (no orphans).
	tasks, err := store.ListTasks(ctx, oldID)
	if err != nil {
		t.Fatalf("ListTasks(pruned): %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("pruned batch left %d task rows behind", len(tasks))
	}
}

// TestPruneTerminalBatchesNoOpBoundaries pins the no-op guards.
func TestPruneTerminalBatchesNoOpBoundaries(t *testing.T) {
	store := newTestStore(t)
	pruner := store.(TerminalBatchPruner)
	if n, err := pruner.PruneTerminalBatches(context.Background(), time.Time{}, 10); err != nil || n != 0 {
		t.Fatalf("zero cutoff must be a no-op, got n=%d err=%v", n, err)
	}
	if n, err := pruner.PruneTerminalBatches(context.Background(), Now(), 0); err != nil || n != 0 {
		t.Fatalf("empty store must be a no-op, got n=%d err=%v", n, err)
	}
}
