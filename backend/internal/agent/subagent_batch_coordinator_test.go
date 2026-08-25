package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

	// done, when non-nil, is closed once RunChildren returns so tests can wait
	// for the worker (and thus finalizeBatch) to finish before asserting the
	// durable task/batch states.
	done chan struct{}

	mu      sync.Mutex
	seenOpt SubagentRunOptions
}

type stubbornExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (f *stubbornExecutor) RunChildren(_ context.Context, _ SubagentRunOptions, _ []SubagentTask) ([]SubagentResult, error) {
	close(f.started)
	<-f.release
	return nil, nil
}

func (f *fakeExecutor) RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	f.mu.Lock()
	f.seenOpt = options
	f.mu.Unlock()
	if f.done != nil {
		defer close(f.done)
	}
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
		done:    make(chan struct{}),
	}
	var mu sync.Mutex
	var events []string
	terminalCanceled := make(chan struct{})
	emitter := func(evt string, payload map[string]interface{}) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt)
		// The terminal canceled event emitted by finalizeBatch is the dedup
		// target: it must appear exactly once and carry the cancel metadata.
		if evt == "subagent.batch.canceled" && payload["status"] == "canceled" && payload["cancel_reason"] == "user abort" {
			select {
			case <-terminalCanceled:
			default:
				close(terminalCanceled)
			}
		}
	}
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		emitter:  emitter,
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

	// Wait until the worker has returned and finalizeBatch has persisted before
	// asserting on the durable task state.
	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not finish after cancel")
	}
	// M4: the single terminal canceled event must be emitted by finalizeBatch.
	select {
	case <-terminalCanceled:
	case <-time.After(5 * time.Second):
		t.Fatalf("terminal canceled event never emitted")
	}

	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCanceled {
		t.Errorf("terminal status = %s, want canceled", term.Status)
	}
	if term.CancelRequestedAt == nil || term.CancelReason != "user abort" {
		t.Errorf("cancel metadata not persisted: reason=%q requested=%v", term.CancelReason, term.CancelRequestedAt)
	}

	// Tasks that never produced a report must be durably classified as canceled,
	// not failed, so the task records agree with the terminal batch status and
	// CanceledCount is reflected in the summary.
	recs, err := store.ListTasks(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != subagentbatch.TaskCanceled {
		t.Errorf("task records = %+v, want one canceled task", recs)
	}

	// L3: a canceled batch must still persist its final ResultSummary even
	// though Cancel() made the batch terminal before finalize ran.
	if len(term.ResultSummary) == 0 {
		t.Errorf("canceled batch did not persist a result summary")
	} else {
		var sum subagentbatch.BatchSummary
		if err := json.Unmarshal(term.ResultSummary, &sum); err != nil {
			t.Errorf("unmarshal result summary: %v", err)
		} else if sum.Status != subagentbatch.BatchCanceled || sum.CanceledCount != 1 {
			t.Errorf("result summary = %+v, want canceled with CanceledCount=1", sum)
		}
	}

	// Cancel is idempotent for terminal batches (no error).
	if err := c.Cancel(context.Background(), batch.BatchID, "again"); err != nil {
		t.Errorf("second Cancel: %v", err)
	}

	// M4: subagent.batch.canceled must be emitted exactly once (before the fix,
	// both Cancel() and finalizeBatch emitted it, yielding two same-name events).
	mu.Lock()
	canceledEvents := 0
	for _, evt := range events {
		if evt == "subagent.batch.canceled" {
			canceledEvents++
		}
	}
	mu.Unlock()
	if canceledEvents != 1 {
		t.Errorf("subagent.batch.canceled emitted %d times, want exactly one", canceledEvents)
	}
}

func TestCancelQueuedBatchBeforeWorkerAdmissionFinalizesOnce(t *testing.T) {
	store := testStore(t)
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		ParentSessionID: "parent-queued-cancel",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchQueued,
		TaskCount:       2,
		QueuedCount:     2,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		BatchDeadline:   now.Add(time.Hour),
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "queued-a", BatchID: batch.BatchID, Status: subagentbatch.TaskPending, OrderIndex: 0, UpdatedAt: now, Version: 1},
		{TaskID: "queued-b", BatchID: batch.BatchID, Status: subagentbatch.TaskReady, OrderIndex: 1, UpdatedAt: now, Version: 1},
	}
	if created, err := store.CreateBatch(context.Background(), batch, tasks); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}

	var mu sync.Mutex
	var events []string
	var deliveries int
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		Emitter: func(eventType string, _ map[string]interface{}) {
			mu.Lock()
			events = append(events, eventType)
			mu.Unlock()
		},
		TerminalSink: func(_ context.Context, notification BatchTerminalNotification) BatchTerminalDelivery {
			mu.Lock()
			deliveries++
			mu.Unlock()
			return BatchTerminalDelivery{
				Status:      BatchTerminalDeliveryPersisted,
				DeliveryKey: notification.DeliveryKey,
			}
		},
	})
	// Admission registers a cancel function before the worker can claim the
	// durable row. Exercise that narrow state explicitly: Cancel must still
	// finalize the unowned queued batch rather than assuming a worker will do
	// it later.
	c.mu.Lock()
	c.cancels[batch.BatchID] = func() {}
	c.mu.Unlock()

	if err := c.Cancel(context.Background(), batch.BatchID, "queued abort"); err != nil {
		t.Fatalf("Cancel queued batch: %v", err)
	}
	got, err := store.GetBatch(context.Background(), batch.BatchID)
	if err != nil || got == nil || got.Status != subagentbatch.BatchCanceled {
		t.Fatalf("batch after cancel: got=%+v err=%v", got, err)
	}
	records, err := store.ListTasks(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("task count = %d, want 2", len(records))
	}
	for _, record := range records {
		if record.Status != subagentbatch.TaskCanceled {
			t.Errorf("task %s status = %s, want canceled", record.TaskID, record.Status)
		}
	}
	if got.CanceledCount != 2 || got.QueuedCount != 0 || got.RunningCount != 0 {
		t.Errorf("batch counts = queued=%d running=%d canceled=%d, want 0/0/2", got.QueuedCount, got.RunningCount, got.CanceledCount)
	}

	if err := c.Cancel(context.Background(), batch.BatchID, "duplicate abort"); err != nil {
		t.Fatalf("duplicate Cancel: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	canceledEvents := 0
	for _, eventType := range events {
		if eventType == "subagent.batch.canceled" {
			canceledEvents++
		}
	}
	if canceledEvents != 1 || deliveries != 1 {
		t.Errorf("terminal events=%d deliveries=%d, want exactly one each; events=%v", canceledEvents, deliveries, events)
	}
}

func TestStartBackgroundDeadlineMarksTaskTimedOut(t *testing.T) {
	store := testStore(t)
	exec := &fakeExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
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
		BatchDeadline:   time.Now().Add(150 * time.Millisecond),
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "g"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}

	// Wait for the worker to observe the deadline before asserting, so the
	// task is not still mid-transition.
	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker never started")
	}

	// Wait for the worker to observe the deadline and finalize before asserting.
	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not finish after deadline")
	}

	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchTimedOut {
		t.Errorf("terminal status = %s, want timed_out", term.Status)
	}
	recs, err := store.ListTasks(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != subagentbatch.TaskTimedOut {
		t.Errorf("task records = %+v, want one timed_out task", recs)
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

func TestRecoverStaleBatchMarksOrphanedAndDeliversTerminal(t *testing.T) {
	store := testStore(t)
	now := subagentbatch.Now().Add(-time.Hour)
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		ParentSessionID: "parent-recovery",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		QueuedCount:     1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		BatchDeadline:   subagentbatch.Now().Add(time.Hour),
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{{
		TaskID: "task-recovery", BatchID: batch.BatchID, Status: subagentbatch.TaskRunning,
		Role: "reviewer", OrderIndex: 0, UpdatedAt: now, Version: 1,
	}}
	created, err := store.CreateBatch(context.Background(), batch, tasks)
	if err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}
	var delivered []agentTerminalTestDelivery
	var projected []BatchTerminalLifecycle
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		LifecycleProjector: func(_ context.Context, event BatchTerminalLifecycle) error {
			projected = append(projected, event)
			return nil
		},
		TerminalSink: func(_ context.Context, notification BatchTerminalNotification) BatchTerminalDelivery {
			delivered = append(delivered, agentTerminalTestDelivery{EventType: notification.EventType, Key: notification.DeliveryKey})
			return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted, DeliveryKey: notification.DeliveryKey}
		},
	})
	changed, err := c.RecoverStaleBatches(context.Background(), 0, "parent-recovery", 10)
	if err != nil || changed != 1 {
		t.Fatalf("RecoverStaleBatches: changed=%d err=%v", changed, err)
	}
	got, err := store.GetBatch(context.Background(), batch.BatchID)
	if err != nil || got.Status != subagentbatch.BatchOrphaned {
		t.Fatalf("recovered batch: got=%+v err=%v", got, err)
	}
	if len(delivered) != 1 || delivered[0].EventType != "subagent.batch.orphaned" {
		t.Fatalf("terminal delivery = %#v", delivered)
	}
	if len(projected) != 1 || projected[0].Status != subagentbatch.BatchOrphaned {
		t.Fatalf("terminal projection = %#v", projected)
	}
}

func TestReplayTerminalDeliveryRetriesAfterSinkFailure(t *testing.T) {
	store := testStore(t)
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		ParentSessionID: "parent-replay",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		TaskCount:       1,
		CompletedCount:  1,
		CreatedAt:       now,
		UpdatedAt:       now,
		FinishedAt:      &now,
		HeartbeatAt:     now,
		BatchDeadline:   now.Add(time.Hour),
		Version:         1,
	}
	if created, err := store.CreateBatch(context.Background(), batch, []subagentbatch.SubagentTaskRecord{{
		TaskID: "task-replay", BatchID: batch.BatchID, Status: subagentbatch.TaskSucceeded,
		Role: "researcher", OrderIndex: 0, UpdatedAt: now, Version: 1,
	}}); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}
	var attempts int
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		TerminalSink: func(_ context.Context, _ BatchTerminalNotification) BatchTerminalDelivery {
			attempts++
			if attempts == 1 {
				return BatchTerminalDelivery{Status: BatchTerminalDeliveryFailed, Err: fmt.Errorf("temporary mailbox outage")}
			}
			return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted}
		},
	})
	first, err := c.ReplayTerminalDeliveries(context.Background(), "parent-replay", 10)
	if err == nil || first.Failed != 1 || attempts != 1 {
		t.Fatalf("first replay = %+v attempts=%d err=%v", first, attempts, err)
	}
	second, err := c.ReplayTerminalDeliveries(context.Background(), "parent-replay", 10)
	if err != nil || second.Delivered != 1 || attempts != 2 {
		t.Fatalf("second replay = %+v attempts=%d err=%v", second, attempts, err)
	}
}

func TestRecoverStaleBatchesLeavesFreshBatchWithoutDeadlineAlone(t *testing.T) {
	store := testStore(t)
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		ParentSessionID: "parent-fresh",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		Version:         1,
	}
	if created, err := store.CreateBatch(context.Background(), batch, []subagentbatch.SubagentTaskRecord{{
		TaskID: "task-fresh", BatchID: batch.BatchID, Status: subagentbatch.TaskRunning,
		Role: "researcher", OrderIndex: 0, UpdatedAt: now, Version: 1,
	}}); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	changed, err := c.RecoverStaleBatches(context.Background(), time.Minute, "parent-fresh", 10)
	if err != nil || changed != 0 {
		t.Fatalf("RecoverStaleBatches: changed=%d err=%v", changed, err)
	}
	got, err := store.GetBatch(context.Background(), batch.BatchID)
	if err != nil || got == nil || got.Status != subagentbatch.BatchRunning {
		t.Fatalf("fresh batch changed: got=%+v err=%v", got, err)
	}
}

func TestShutdownTimeoutOrphansOnlyOwnedBatches(t *testing.T) {
	store := testStore(t)
	otherNow := subagentbatch.Now()
	other := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		ParentSessionID: "other-parent",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		CreatedAt:       otherNow,
		UpdatedAt:       otherNow,
		HeartbeatAt:     otherNow,
		BatchDeadline:   otherNow.Add(time.Hour),
		Version:         1,
	}
	if created, err := store.CreateBatch(context.Background(), other, []subagentbatch.SubagentTaskRecord{{
		TaskID: "other-task", BatchID: other.BatchID, Status: subagentbatch.TaskRunning,
		Role: "reader", UpdatedAt: otherNow, Version: 1,
	}}); err != nil || !created {
		t.Fatalf("CreateBatch(other): created=%v err=%v", created, err)
	}
	exec := &stubbornExecutor{started: make(chan struct{}), release: make(chan struct{})}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	c.executor = exec
	owned, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "owned-parent", ExecutionMode: subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "owned-task", Role: "reader", Goal: "block"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(time.Second):
		t.Fatal("owned worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Shutdown(ctx, "test shutdown"); err == nil {
		t.Fatal("Shutdown timeout = nil, want context error")
	}
	ownedAfter, err := store.GetBatch(context.Background(), owned.BatchID)
	if err != nil || ownedAfter == nil || ownedAfter.Status != subagentbatch.BatchOrphaned {
		t.Fatalf("owned batch after shutdown: got=%+v err=%v", ownedAfter, err)
	}
	otherAfter, err := store.GetBatch(context.Background(), other.BatchID)
	if err != nil || otherAfter == nil || otherAfter.Status != subagentbatch.BatchRunning {
		t.Fatalf("unowned batch changed: got=%+v err=%v", otherAfter, err)
	}
	taskBefore, err := store.GetTask(context.Background(), owned.BatchID, "owned-task")
	if err != nil || taskBefore == nil {
		t.Fatalf("owned task before late worker release: task=%+v err=%v", taskBefore, err)
	}
	close(exec.release)
	c.workers.Wait()
	taskAfter, err := store.GetTask(context.Background(), owned.BatchID, "owned-task")
	if err != nil || taskAfter == nil {
		t.Fatalf("owned task after late worker release: task=%+v err=%v", taskAfter, err)
	}
	if taskAfter.Status != taskBefore.Status || taskAfter.Version != taskBefore.Version {
		t.Fatalf("late worker mutated orphaned task: before=%+v after=%+v", taskBefore, taskAfter)
	}
}

func TestLateFinalizeAfterRecoveryDoesNotEmitTaskLifecycleEvents(t *testing.T) {
	store := testStore(t)
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		ParentSessionID: "parent-late-finalize",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		BatchDeadline:   now.Add(time.Hour),
		OwnerID:         "old-coordinator",
		FencingToken:    "old-coordinator/1",
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{{
		TaskID: "late-task", BatchID: batch.BatchID, Status: subagentbatch.TaskRunning,
		OrderIndex: 0, UpdatedAt: now, Version: 1,
	}}
	if created, err := store.CreateBatch(context.Background(), batch, tasks); err != nil || !created {
		t.Fatalf("CreateBatch: created=%v err=%v", created, err)
	}
	current, err := store.GetBatch(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if _, err := store.UpdateBatch(context.Background(), batch.BatchID, current.Version, func(b *subagentbatch.SubagentBatch) {
		b.Status = subagentbatch.BatchOrphaned
	}); err != nil {
		t.Fatalf("UpdateBatch(orphaned): %v", err)
	}

	var mu sync.Mutex
	var events []string
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		Emitter: func(eventType string, _ map[string]interface{}) {
			mu.Lock()
			events = append(events, eventType)
			mu.Unlock()
		},
	})
	c.finalizeBatch(context.Background(), batch.BatchID, BatchStartOptions{
		ParentSessionID: "parent-late-finalize",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "late-task", Role: "reader"}}, []SubagentResult{{
		SessionID: "late-child", Success: true, Summary: "late",
	}}, nil)

	mu.Lock()
	defer mu.Unlock()
	for _, eventType := range events {
		if eventType == "subagent.task.completed" || eventType == "subagent.batch.progress" {
			t.Fatalf("late finalize emitted %q: events=%v", eventType, events)
		}
	}
	got, err := store.GetBatch(context.Background(), batch.BatchID)
	if err != nil || got == nil || got.Status != subagentbatch.BatchOrphaned {
		t.Fatalf("batch after late finalize: got=%+v err=%v", got, err)
	}
}

type agentTerminalTestDelivery struct {
	EventType string
	Key       string
}

func containsEvent(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
