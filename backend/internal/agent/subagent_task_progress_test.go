package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// M1 (P0-1a) task progress write-back: TaskProgressInterval enables throttled
// LastProgressAt stamps for background batches while their worker runs. These
// tests pin the contract from §3.1/§7.2/ADR-1:
//   - interval 0 (factory default) writes nothing (pre-M1 behavior);
//   - only background batches participate;
//   - the store stamp is the throttle state (≤1 write/task/interval);
//   - CAS/terminal-fence rejections are dropped without errors or events;
//   - forced state-transition stamps are not suppressed by the window.
//
// These tests use the real SQLite store end to end: scanTaskRow hydrates
// TaskDeadline/StartedAt/FinishedAt/LastProgressAt (guarded by
// sqlite_store_test.go), so the throttle state and every durable stamp are
// observable through the ordinary read path.
func progressTestStore(t testing.TB) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(nil)
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedRunningBatch persists a running background batch plus running tasks with
// version 1 and no progress stamp yet.
func seedRunningBatch(t testing.TB, store subagentbatch.BatchStore, batchID string, taskIDs ...string) {
	t.Helper()
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		ParentSessionID: "parent-" + batchID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       len(taskIDs),
		RunningCount:    len(taskIDs),
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		BatchDeadline:   now.Add(time.Hour),
		Version:         1,
	}
	records := make([]subagentbatch.SubagentTaskRecord, len(taskIDs))
	for i, taskID := range taskIDs {
		records[i] = subagentbatch.SubagentTaskRecord{
			TaskID: taskID, BatchID: batchID, Status: subagentbatch.TaskRunning,
			Role: "researcher", OrderIndex: i, UpdatedAt: now, Version: 1,
		}
	}
	created, err := store.CreateBatch(context.Background(), batch, records)
	if err != nil || !created {
		t.Fatalf("CreateBatch(%s): created=%v err=%v", batchID, created, err)
	}
}

func mustGetTask(t testing.TB, store subagentbatch.BatchStore, batchID, taskID string) *subagentbatch.SubagentTaskRecord {
	t.Helper()
	task, err := store.GetTask(context.Background(), batchID, taskID)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s/%s): task=%+v err=%v", batchID, taskID, task, err)
	}
	return task
}

// TestSubagentTaskProgressWritebackDisabledByDefault pins the factory default:
// with a zero interval nothing is written (started transition, refresh and
// settle path all stay silent), and every counter stays zero.
func TestSubagentTaskProgressWritebackDisabledByDefault(t *testing.T) {
	store := progressTestStore(t)
	exec := &fakeExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		results: []SubagentResult{{ID: "t1", Success: true, Summary: "done"}},
	}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	c.executor = exec
	c.deadline = time.Minute

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-default",
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

	if task := mustGetTask(t, store, batch.BatchID, "t1"); task.LastProgressAt != nil {
		t.Fatalf("LastProgressAt = %v, want nil with interval 0 (write-back disabled)", task.LastProgressAt)
	}
	if counts := c.TaskProgressWriteCounts(); counts.Writes != 0 {
		t.Fatalf("Writes = %d, want 0 while running with interval 0", counts.Writes)
	}

	close(exec.release)
	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not finish")
	}
	waitTerminal(t, store, batch.BatchID)

	if task := mustGetTask(t, store, batch.BatchID, "t1"); task.LastProgressAt != nil {
		t.Errorf("terminal task LastProgressAt = %v, want nil with interval 0", task.LastProgressAt)
	}
	if got := c.TaskProgressWriteCounts(); got != (SubagentTaskProgressCounts{}) {
		t.Errorf("counts = %+v, want all zero", got)
	}
}

// TestSubagentTaskProgressWritebackIsBackgroundOnly pins ADR-1: a wait mode
// batch never writes progress even when the interval is enabled.
func TestSubagentTaskProgressWritebackIsBackgroundOnly(t *testing.T) {
	store := progressTestStore(t)
	exec := &fakeExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		results: []SubagentResult{{ID: "t1", Success: true, Summary: "done"}},
	}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store:                store,
		TaskProgressInterval: 5 * time.Millisecond,
	})
	c.executor = exec
	c.deadline = time.Minute

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-wait",
		ExecutionMode:   subagentbatch.ExecutionModeWait,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "g"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker never started")
	}

	// Hold the worker across several refresh windows; a background batch would
	// have stamped several times by now.
	time.Sleep(40 * time.Millisecond)
	if task := mustGetTask(t, store, batch.BatchID, "t1"); task.LastProgressAt != nil {
		t.Fatalf("wait-mode task got a progress stamp: %v", task.LastProgressAt)
	}

	close(exec.release)
	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not finish")
	}
	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCompleted {
		t.Fatalf("terminal status = %s, want completed", term.Status)
	}
	if task := mustGetTask(t, store, batch.BatchID, "t1"); task.LastProgressAt != nil {
		t.Errorf("settled wait-mode task LastProgressAt = %v, want nil", task.LastProgressAt)
	}
	if got := c.TaskProgressWriteCounts(); got != (SubagentTaskProgressCounts{}) {
		t.Errorf("counts = %+v, want all zero for wait mode", got)
	}
}

// TestSubagentTaskProgressRefresherWritesDuringBackgroundRun exercises the real
// worker flow: the started transition stamps, the refresher advances the stamp
// while the worker runs, and the settle path stamps again before recording the
// result.
func TestSubagentTaskProgressRefresherWritesDuringBackgroundRun(t *testing.T) {
	store := progressTestStore(t)
	exec := &fakeExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		results: []SubagentResult{{ID: "t1", Success: true, Summary: "done"}},
	}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store:                store,
		TaskProgressInterval: 15 * time.Millisecond,
	})
	c.executor = exec
	c.deadline = time.Minute

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-run",
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

	startedTask := mustGetTask(t, store, batch.BatchID, "t1")
	if startedTask.LastProgressAt == nil {
		t.Fatalf("started transition must force a progress stamp (counts=%+v)", c.TaskProgressWriteCounts())
	}
	startedStamp := *startedTask.LastProgressAt
	if counts := c.TaskProgressWriteCounts(); counts.Writes < 1 {
		t.Fatalf("Writes = %d, want >= 1 after the started transition", counts.Writes)
	}

	// The refresher must advance the stamp while the worker is still running.
	deadline := time.Now().Add(5 * time.Second)
	advanced := false
	for time.Now().Before(deadline) {
		cur := mustGetTask(t, store, batch.BatchID, "t1")
		if cur.LastProgressAt != nil && cur.LastProgressAt.After(startedStamp) {
			advanced = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !advanced {
		t.Fatalf("refresher never advanced LastProgressAt beyond %v", startedStamp)
	}

	close(exec.release)
	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not finish")
	}
	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCompleted {
		t.Fatalf("terminal status = %s, want completed", term.Status)
	}
	finalTask := mustGetTask(t, store, batch.BatchID, "t1")
	if finalTask.LastProgressAt == nil || finalTask.LastProgressAt.Before(startedStamp) {
		t.Fatalf("settled LastProgressAt = %v, want >= %v", finalTask.LastProgressAt, startedStamp)
	}
	counts := c.TaskProgressWriteCounts()
	if counts.Writes < 2 {
		t.Errorf("Writes = %d, want >= 2 (started + at least one refresh/settle stamp)", counts.Writes)
	}
	if counts.Errors != 0 || counts.ConflictsDropped != 0 {
		t.Errorf("counts = %+v, want no errors/conflicts", counts)
	}
}

// TestSubagentTaskProgressRefreshThrottleCoalescesWithinWindow pins the §7.2
// bound: one write per task per interval. A nil stamp is written once; a fresh
// stamp coalesces further refreshes; only an expired window writes again. Row
// versions prove the writes really committed to the SQLite store.
func TestSubagentTaskProgressRefreshThrottleCoalescesWithinWindow(t *testing.T) {
	store := progressTestStore(t)
	seedRunningBatch(t, store, "batch-throttle", "t1")
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	ctx := context.Background()
	interval := time.Second

	c.refreshBatchTaskProgress(ctx, "batch-throttle", interval)
	first := mustGetTask(t, store, "batch-throttle", "t1")
	if first.LastProgressAt == nil {
		t.Fatalf("first refresh did not stamp a nil LastProgressAt (counts=%+v)", c.TaskProgressWriteCounts())
	}
	firstStamp := *first.LastProgressAt
	if first.Version <= 1 {
		t.Fatalf("task version = %d, want > 1 after a committed stamp", first.Version)
	}

	// Same window: two more ticks must coalesce (zero additional writes).
	c.refreshBatchTaskProgress(ctx, "batch-throttle", interval)
	c.refreshBatchTaskProgress(ctx, "batch-throttle", interval)
	coalesced := mustGetTask(t, store, "batch-throttle", "t1")
	if coalesced.LastProgressAt == nil || !coalesced.LastProgressAt.Equal(firstStamp) {
		t.Fatalf("LastProgressAt moved inside the throttle window: %v -> %v", firstStamp, coalesced.LastProgressAt)
	}
	if coalesced.Version != first.Version {
		t.Fatalf("coalesced refresh still wrote: version %d -> %d", first.Version, coalesced.Version)
	}
	counts := c.TaskProgressWriteCounts()
	if counts.Writes != 1 || counts.WindowSkipped != 2 {
		t.Fatalf("counts after coalescing = %+v, want writes=1 window_skipped=2", counts)
	}

	// Expire the window; exactly one more write happens.
	stale := firstStamp.Add(-2 * interval)
	if _, err := store.UpdateTask(ctx, "batch-throttle", "t1", coalesced.Version, func(record *subagentbatch.SubagentTaskRecord) {
		record.LastProgressAt = &stale
	}); err != nil {
		t.Fatalf("force stale stamp: %v", err)
	}
	c.refreshBatchTaskProgress(ctx, "batch-throttle", interval)
	refreshed := mustGetTask(t, store, "batch-throttle", "t1")
	if refreshed.LastProgressAt == nil || !refreshed.LastProgressAt.After(stale) {
		t.Fatalf("expired window did not refresh: %v", refreshed.LastProgressAt)
	}
	if refreshed.Version <= coalesced.Version+1 {
		t.Fatalf("refreshed version = %d, want > %d", refreshed.Version, coalesced.Version+1)
	}
	counts = c.TaskProgressWriteCounts()
	if counts.Writes != 2 {
		t.Fatalf("Writes = %d, want 2 after window expiry", counts.Writes)
	}
}

// TestSubagentTaskProgressLateWritesAreDroppedSilently pins the CAS/terminal
// fence: writes landing after the task or the batch turned terminal are counted
// as conflicts, never as errors, never emit events, and leave the row
// untouched (version unchanged).
func TestSubagentTaskProgressLateWritesAreDroppedSilently(t *testing.T) {
	store := progressTestStore(t)
	seedRunningBatch(t, store, "batch-late-task", "t1")
	seedRunningBatch(t, store, "batch-late-batch", "t2")
	ctx := context.Background()

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

	// Task terminal: the pre-terminal version is stale and the CAS rejects.
	task := mustGetTask(t, store, "batch-late-task", "t1")
	if err := store.RecordTaskResult(ctx, "batch-late-task", "t1", task.Version, subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID: "t1", Success: true,
	}); err != nil {
		t.Fatalf("RecordTaskResult: %v", err)
	}
	terminalTask := mustGetTask(t, store, "batch-late-task", "t1")
	c.writeTaskProgress(ctx, "batch-late-task", "t1", task.Version, subagentbatch.Now())
	after := mustGetTask(t, store, "batch-late-task", "t1")
	if after.Version != terminalTask.Version {
		t.Fatalf("late task write mutated the row: version %d -> %d", terminalTask.Version, after.Version)
	}

	// Batch terminal: the durable batch fence rejects the late stamp.
	batchTask := mustGetTask(t, store, "batch-late-batch", "t2")
	batch, err := store.GetBatch(ctx, "batch-late-batch")
	if err != nil || batch == nil {
		t.Fatalf("GetBatch(batch-late-batch): batch=%+v err=%v", batch, err)
	}
	if _, err := store.UpdateBatch(ctx, "batch-late-batch", batch.Version, func(b *subagentbatch.SubagentBatch) {
		b.Status = subagentbatch.BatchCompleted
	}); err != nil {
		t.Fatalf("UpdateBatch(completed): %v", err)
	}
	c.writeTaskProgress(ctx, "batch-late-batch", "t2", batchTask.Version, subagentbatch.Now())
	if afterBatch := mustGetTask(t, store, "batch-late-batch", "t2"); afterBatch.Version != batchTask.Version {
		t.Fatalf("late batch-fenced write mutated the row: version %d -> %d", batchTask.Version, afterBatch.Version)
	}

	counts := c.TaskProgressWriteCounts()
	if counts.Writes != 0 || counts.ConflictsDropped != 2 || counts.Errors != 0 {
		t.Fatalf("counts = %+v, want writes=0 conflicts_dropped=2 errors=0", counts)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("silent drop emitted events: %v", events)
	}
}

// failingListTasksStore injects a store failure without touching the read path
// used elsewhere.
type failingListTasksStore struct {
	subagentbatch.BatchStore
	err error
}

func (s failingListTasksStore) ListTasks(context.Context, string) ([]subagentbatch.SubagentTaskRecord, error) {
	return nil, s.err
}

// TestSubagentTaskProgressErrorsAreCountedAndEmissionThrottled pins the
// observability contract: non-conflict failures land in Errors, and the
// best-effort subagent.batch.progress event is rate-limited so a failing store
// cannot storm the bus.
func TestSubagentTaskProgressErrorsAreCountedAndEmissionThrottled(t *testing.T) {
	base := progressTestStore(t)
	store := failingListTasksStore{BatchStore: base, err: errors.New("sqlite busy")}
	var mu sync.Mutex
	var progressEvents int
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		Emitter: func(eventType string, _ map[string]interface{}) {
			if eventType != "subagent.batch.progress" {
				return
			}
			mu.Lock()
			progressEvents++
			mu.Unlock()
		},
	})

	ctx := context.Background()
	c.refreshBatchTaskProgress(ctx, "batch-err", time.Second)
	c.refreshBatchTaskProgress(ctx, "batch-err", time.Second)

	if counts := c.TaskProgressWriteCounts(); counts.Errors != 2 {
		t.Fatalf("Errors = %d, want 2", counts.Errors)
	}
	mu.Lock()
	emitted := progressEvents
	mu.Unlock()
	if emitted != 1 {
		t.Fatalf("emitted subagent.batch.progress events = %d, want 1 (rate limited)", emitted)
	}
}

// TestSubagentTaskProgressWriteCountsNilReceiver keeps the /debug accessor
// nil-safe.
func TestSubagentTaskProgressWriteCountsNilReceiver(t *testing.T) {
	var c *SubagentBatchCoordinator
	if got := c.TaskProgressWriteCounts(); got != (SubagentTaskProgressCounts{}) {
		t.Fatalf("nil receiver counts = %+v, want zero", got)
	}
}

// BenchmarkSubagentTaskProgressRefreshCoalescing measures one refresher tick
// over 50 running tasks whose LastProgressAt is already fresh: every task is
// window-skipped so the store sees zero writes per tick. Together with the
// throttle unit test this demonstrates the §7.2 bound (≤1 write per task per
// interval) rather than per-tick write amplification.
func BenchmarkSubagentTaskProgressRefreshCoalescing(b *testing.B) {
	store := progressTestStore(b)
	const taskCount = 50
	taskIDs := make([]string, taskCount)
	for i := range taskIDs {
		taskIDs[i] = fmt.Sprintf("bench-task-%02d", i)
	}
	seedRunningBatch(b, store, "batch-bench", taskIDs...)
	now := subagentbatch.Now()
	for _, taskID := range taskIDs {
		record := mustGetTask(b, store, "batch-bench", taskID)
		if _, err := store.UpdateTask(context.Background(), "batch-bench", taskID, record.Version, func(record *subagentbatch.SubagentTaskRecord) {
			record.LastProgressAt = &now
		}); err != nil {
			b.Fatalf("seed progress stamp: %v", err)
		}
	}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.refreshBatchTaskProgress(ctx, "batch-bench", time.Hour)
	}
	b.StopTimer()

	counts := c.TaskProgressWriteCounts()
	if counts.Writes != 0 {
		b.Fatalf("coalesced refresh wrote %d rows, want 0", counts.Writes)
	}
	if want := int64(taskCount) * int64(b.N); counts.WindowSkipped != want {
		b.Fatalf("WindowSkipped = %d, want %d", counts.WindowSkipped, want)
	}
}

// --- M3 (§3.4 改动 3): result-capsule write guarantees ---------------------

// TestSubagentTaskResultCapsuleCarriesChildSessionAndUsage pins the two
// producer gaps M3 closes: the settle path must write the child session onto
// the task row itself (ChildSessionID had no producer, so session-keyed reads
// could only fall back to the capsule's TaskResult.SessionID) and copy the
// child's usage total into the durable capsule. A report without usage or
// without a session must stay a quiet no-op instead of panicking or writing
// empty stamps.
func TestSubagentTaskResultCapsuleCarriesChildSessionAndUsage(t *testing.T) {
	store := progressTestStore(t)
	exec := &fakeExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		results: []SubagentResult{
			{
				ID:        "t1",
				SessionID: "child-session-1",
				Success:   true,
				Summary:   "done",
				Usage:     &types.TokenUsage{PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12},
			},
			{ID: "t2", Success: true, Summary: "no usage"},
		},
	}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	c.executor = exec
	c.deadline = time.Minute

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-m3",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "t1", Role: "researcher", Goal: "g"},
		{ID: "t2", Role: "researcher", Goal: "g"},
	})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker never started")
	}
	close(exec.release)
	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not finish")
	}
	waitTerminal(t, store, batch.BatchID)

	first := mustGetTask(t, store, batch.BatchID, "t1")
	if first.Status != subagentbatch.TaskSucceeded {
		t.Fatalf("t1 status = %s, want succeeded", first.Status)
	}
	if first.ChildSessionID != "child-session-1" {
		t.Fatalf("t1 ChildSessionID = %q, want %q (the row column is what session-keyed reads use)",
			first.ChildSessionID, "child-session-1")
	}
	capsule := decodeStoredTaskResult(t, first.ResultSummary)
	if capsule.SessionID != "child-session-1" {
		t.Errorf("capsule SessionID = %q, want child-session-1", capsule.SessionID)
	}
	if capsule.UsageTotal != 12 {
		t.Errorf("capsule UsageTotal = %d, want 12 (usageTotal convention from SubagentResult.Usage)", capsule.UsageTotal)
	}

	// t2 carries neither usage nor a child session: no panic, no stamps.
	second := mustGetTask(t, store, batch.BatchID, "t2")
	if second.Status != subagentbatch.TaskSucceeded {
		t.Fatalf("t2 status = %s, want succeeded", second.Status)
	}
	if second.ChildSessionID != "" {
		t.Errorf("t2 ChildSessionID = %q, want empty without a report session", second.ChildSessionID)
	}
	if got := decodeStoredTaskResult(t, second.ResultSummary).UsageTotal; got != 0 {
		t.Errorf("t2 UsageTotal = %d, want 0 without usage", got)
	}
}

// TestSubagentTaskResultSettleKeepsExistingSessionAndFencedOutcome pins the
// CAS/fence discipline of the M3 backfill: a row that already carries a child
// session is never overwritten by a later report, and re-settling an already
// terminal task cannot replace the durable outcome (same status is an
// idempotent no-op, a different status is a version conflict).
func TestSubagentTaskResultSettleKeepsExistingSessionAndFencedOutcome(t *testing.T) {
	store := progressTestStore(t)
	seedRunningBatch(t, store, "batch-m3-fence", "t1")
	ctx := context.Background()

	row := mustGetTask(t, store, "batch-m3-fence", "t1")
	if _, err := store.UpdateTask(ctx, "batch-m3-fence", "t1", row.Version, func(record *subagentbatch.SubagentTaskRecord) {
		record.ChildSessionID = "child-original"
	}); err != nil {
		t.Fatalf("preset ChildSessionID: %v", err)
	}

	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	alreadyTerminal, err := c.prepareTaskResult(ctx, "batch-m3-fence", "t1", subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID:    "t1",
		SessionID: "child-other",
		Success:   true,
		Summary:   "first",
	}, false)
	if err != nil || alreadyTerminal {
		t.Fatalf("prepareTaskResult(first) = alreadyTerminal=%v err=%v, want false/nil", alreadyTerminal, err)
	}

	settled := mustGetTask(t, store, "batch-m3-fence", "t1")
	if settled.Status != subagentbatch.TaskSucceeded {
		t.Fatalf("status = %s, want succeeded", settled.Status)
	}
	if settled.ChildSessionID != "child-original" {
		t.Fatalf("ChildSessionID = %q, want the pre-existing child-original (never overwrite)", settled.ChildSessionID)
	}
	if capsule := decodeStoredTaskResult(t, settled.ResultSummary); capsule.SessionID != "child-other" || capsule.Summary != "first" {
		t.Fatalf("capsule = %+v, want session child-other / summary first", capsule)
	}
	settledVersion := settled.Version
	settledSummary := string(settled.ResultSummary)

	alreadyTerminal, err = c.prepareTaskResult(ctx, "batch-m3-fence", "t1", subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID: "t1", SessionID: "child-late", Success: true, Summary: "second",
	}, false)
	if err != nil {
		t.Fatalf("prepareTaskResult(idempotent) err = %v, want nil", err)
	}
	if !alreadyTerminal {
		t.Fatalf("prepareTaskResult(idempotent) alreadyTerminal = false, want true")
	}
	unchanged := mustGetTask(t, store, "batch-m3-fence", "t1")
	if unchanged.Version != settledVersion || string(unchanged.ResultSummary) != settledSummary || unchanged.ChildSessionID != "child-original" {
		t.Fatalf("idempotent settle mutated the row: %+v", unchanged)
	}

	_, err = c.prepareTaskResult(ctx, "batch-m3-fence", "t1", subagentbatch.TaskFailed, &subagentbatch.TaskResult{
		TaskID: "t1", SessionID: "child-late", Success: false, Summary: "third", Error: "late failure",
	}, false)
	var conflict *subagentbatch.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("prepareTaskResult(conflicting status) err = %v, want VersionConflictError", err)
	}
	after := mustGetTask(t, store, "batch-m3-fence", "t1")
	if after.Version != settledVersion || string(after.ResultSummary) != settledSummary || after.Status != subagentbatch.TaskSucceeded {
		t.Fatalf("fenced settle mutated the row: %+v", after)
	}
}

func decodeStoredTaskResult(t testing.TB, payload []byte) subagentbatch.TaskResult {
	t.Helper()
	if len(payload) == 0 {
		t.Fatalf("task result payload is empty")
	}
	var capsule subagentbatch.TaskResult
	if err := json.Unmarshal(payload, &capsule); err != nil {
		t.Fatalf("decode TaskResult %s: %v", payload, err)
	}
	return capsule
}
