package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// fakeExecutor is a minimal subagentExecutor seam for coordinator tests. It
// fires the scheduler's task lifecycle events and returns canned results,
// letting us assert on durable batch lifecycle without spawning real children.
type fakeExecutor struct {
	results []SubagentResult
	runErr  error

	// started is closed once RunChildren reaches the point where cancel can
	// take effect; used by the cancel test to sequence the worker.
	started chan struct{}

	// release, when non-nil, makes RunChildren block until ctx is done or the
	// channel is closed. Used to simulate a long-running worker.
	release chan struct{}

	mu      sync.Mutex
	seenOpt SubagentRunOptions
}

func (f *fakeExecutor) RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	f.mu.Lock()
	f.seenOpt = options
	f.mu.Unlock()
	for _, task := range tasks {
		options.notifyTaskEvent(task.ID, "started")
	}
	if f.started != nil {
		close(f.started)
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
		case <-f.release:
		}
	}
	for _, task := range tasks {
		options.notifyTaskEvent(task.ID, "completed")
	}
	return f.results, f.runErr
}

func testStore(t *testing.T) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(nil)
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func waitTerminal(t *testing.T, store subagentbatch.BatchStore, batchID string) *subagentbatch.SubagentBatch {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := store.GetBatch(context.Background(), batchID)
		if err != nil {
			t.Fatalf("GetBatch(%s): %v", batchID, err)
		}
		if b != nil && b.Status.Terminal() {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("batch %s did not reach terminal state in time", batchID)
	return nil
}

func TestStartBackgroundRunsToTerminal(t *testing.T) {
	store := testStore(t)
	exec := &fakeExecutor{
		results: []SubagentResult{
			{ID: "t1", Role: "researcher", SessionID: "child-1", Success: true, Summary: "investigated"},
		},
	}
	var mu sync.Mutex
	var events []string
	emitter := func(evt string, _ map[string]interface{}) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	}

	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		emitter:  emitter,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		TraceID:         "trace-1",
		ParentSessionID: "session-1",
		ParentTurnID:    "turn-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "investigate"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	if batch.BatchID == "" {
		t.Fatalf("StartBackground returned empty batch id")
	}
	if batch.ExecutionMode != subagentbatch.ExecutionModeBackground {
		t.Errorf("execution mode = %s, want background", batch.ExecutionMode)
	}

	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCompleted {
		t.Errorf("terminal status = %s, want completed", term.Status)
	}
	if term.CompletedCount != 1 {
		t.Errorf("completed_count = %d, want 1", term.CompletedCount)
	}

	// The worker must have mirrored status into the store and persisted results.
	recs, err := store.ListTasks(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != subagentbatch.TaskSucceeded {
		t.Errorf("task records = %+v, want one succeeded task", recs)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"subagent.batch.started", "subagent.task.started", "subagent.task.completed", "subagent.batch.completed"} {
		if !containsEvent(events, want) {
			t.Errorf("events %v missing %s", events, want)
		}
	}
}

func TestStartBackgroundCancel(t *testing.T) {
	store := testStore(t)
	exec := &fakeExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "g"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}

	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker never started")
	}

	if err := c.Cancel(context.Background(), batch.BatchID, "user abort"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCanceled {
		t.Errorf("terminal status = %s, want canceled", term.Status)
	}
	if term.CancelRequestedAt == nil || term.CancelReason != "user abort" {
		t.Errorf("cancel metadata not persisted: reason=%q requested=%v", term.CancelReason, term.CancelRequestedAt)
	}

	// Cancel is idempotent for terminal batches (no error).
	if err := c.Cancel(context.Background(), batch.BatchID, "again"); err != nil {
		t.Errorf("second Cancel: %v", err)
	}
}

func TestStartBackgroundAfterError(t *testing.T) {
	store := testStore(t)
	exec := &fakeExecutor{runErr: context.DeadlineExceeded}
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}
	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "g"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchFailed {
		t.Errorf("terminal status = %s, want failed", term.Status)
	}
}

func TestStartBackgroundIdempotency(t *testing.T) {
	store := testStore(t)
	exec := &fakeExecutor{
		results: []SubagentResult{{ID: "t1", Success: true}},
	}
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}
	opts := BatchStartOptions{
		ParentSessionID: "session-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		IdempotencyKey:  "idem-42",
	}
	tasks := []SubagentTask{{ID: "t1", Role: "researcher", Goal: "g"}}

	first, err := c.StartBackground(context.Background(), opts, tasks)
	if err != nil {
		t.Fatalf("first StartBackground: %v", err)
	}
	replay, err := c.StartBackground(context.Background(), opts, tasks)
	if err != nil {
		t.Fatalf("replay StartBackground: %v", err)
	}
	if replay.BatchID != first.BatchID {
		t.Errorf("replay batch id %q != first %q", replay.BatchID, first.BatchID)
	}
	// Only one durable batch exists for the key.
	batches, err := store.ListBatches(context.Background(), subagentbatch.BatchFilter{
		ParentSessionID: "session-1",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Errorf("ListBatches len = %d, want 1 (idempotent replay)", len(batches))
	}
}

func TestStartBackgroundWithoutExecutor(t *testing.T) {
	store := testStore(t)
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: nil,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}
	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "g"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	// No executor: runTasksWithProgress returns an error, batch must fail fast
	// and still reach a terminal state instead of wedging queued.
	term := waitTerminal(t, store, batch.BatchID)
	if !term.Status.Terminal() {
		t.Errorf("batch status = %s, want terminal", term.Status)
	}
}

func containsEvent(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
