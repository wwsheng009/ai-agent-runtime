// SubagentBatchCoordinator runs durable, background-able spawn_subagents
// batches. It is the host-side driver for the host-neutral
// internal/subagentbatch control plane: it persists a batch + task records,
// detaches a worker from the parent turn, and runs the existing
// SubagentScheduler as the execution kernel, mirroring every transition back
// into the BatchStore so lifecycle events can be recovered after restart.
//
// Design contract: docs/plan/spawn-subagents-async-supervisor-plan.md
// sections 4.2 (coordinator), 4.3 (model), 4.4 (execution modes) and 6.1
// (decoupling execution from the parent call stack).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// BatchStartOptions describes the durable metadata of one background batch.
type BatchStartOptions struct {
	TraceID          string
	ParentSessionID  string
	ParentTurnID     string
	ParentToolCallID string
	RootScopeID      string
	Depth            int
	ExecutionMode    subagentbatch.ExecutionMode
	IdempotencyKey   string
	BatchDeadline    time.Time
}

// BatchEmitter publishes a host-neutral lifecycle event. The loop wires it to
// its runtime event bus; tests can use it to assert on emitted transitions.
type BatchEmitter func(eventType string, payload map[string]interface{})

// SubagentBatchCoordinatorConfig wires a coordinator to its durable store,
// execution kernel and event sink.
type SubagentBatchCoordinatorConfig struct {
	Store           subagentbatch.BatchStore
	Scheduler       *SubagentScheduler
	Emitter         BatchEmitter
	DefaultDeadline time.Duration // applied when a request carries no deadline
}

// subagentExecutor is the kernel abstraction the coordinator drives. It is a
// small seam over *SubagentScheduler so tests can inject a fake executor and
// assert on durable batch lifecycle without spawning real child sessions.
type subagentExecutor interface {
	RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error)
}

// SubagentBatchCoordinator persists and drives background subagent batches.
type SubagentBatchCoordinator struct {
	store    subagentbatch.BatchStore
	executor subagentExecutor
	emitter  BatchEmitter
	deadline time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// NewSubagentBatchCoordinator constructs a coordinator from a config. The
// store must be non-nil for StartBackground to work.
func NewSubagentBatchCoordinator(cfg SubagentBatchCoordinatorConfig) *SubagentBatchCoordinator {
	if cfg.DefaultDeadline <= 0 {
		cfg.DefaultDeadline = 30 * time.Minute
	}
	scheduler := cfg.Scheduler
	return &SubagentBatchCoordinator{
		store:    cfg.Store,
		executor: scheduler,
		emitter:  cfg.Emitter,
		deadline: cfg.DefaultDeadline,
		cancels:  make(map[string]context.CancelFunc),
	}
}

// Store exposes the durable control plane (used by hosts for recovery).
func (c *SubagentBatchCoordinator) Store() subagentbatch.BatchStore {
	if c == nil {
		return nil
	}
	return c.store
}

// StartBackground persists a batch (and its task records) and returns
// immediately with the durable handle; the worker keeps running after the
// parent turn returns. When IdempotencyKey is set and an existing batch holds
// the same key under the same parent session, the existing batch is returned
// and no duplicate batch is created.
func (c *SubagentBatchCoordinator) StartBackground(parentCtx context.Context, opts BatchStartOptions, tasks []SubagentTask) (*subagentbatch.SubagentBatch, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("subagent batch coordinator is not configured")
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("subagent batch requires at least one task")
	}

	mode := opts.ExecutionMode
	if mode == "" {
		mode = subagentbatch.ExecutionModeBackground
	}
	now := subagentbatch.Now()
	deadline := opts.BatchDeadline
	if deadline.IsZero() {
		deadline = now.Add(c.deadline)
	}

	batch := &subagentbatch.SubagentBatch{
		BatchID:          subagentbatch.NewID("batch"),
		RootScopeID:      opts.RootScopeID,
		ParentSessionID:  opts.ParentSessionID,
		ParentTurnID:     opts.ParentTurnID,
		ParentToolCallID: opts.ParentToolCallID,
		TraceID:          opts.TraceID,
		ExecutionMode:    mode,
		Status:           subagentbatch.BatchQueued,
		IdempotencyKey:   opts.IdempotencyKey,
		TaskCount:        len(tasks),
		QueuedCount:      len(tasks),
		CreatedAt:        now,
		UpdatedAt:        now,
		BatchDeadline:    deadline,
		HeartbeatAt:      now,
		Version:          1,
	}

	records := make([]subagentbatch.SubagentTaskRecord, len(tasks))
	for i, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			taskID = subagentbatch.NewID("task")
			// Keep the in-memory task in sync with the durable record so the
			// worker (prepareTasks) and finalizeBatch resolve the same taskID.
			// Without this, an empty-ID task would be stored under a random id
			// but finalized under "task_<i>", making every RecordTaskResult
			// write fail with "task not found".
			tasks[i].ID = taskID
		}
		specBytes, err := json.Marshal(taskToBatchSpec(task, taskID))
		if err != nil {
			return nil, fmt.Errorf("subagent batch: encode task spec: %w", err)
		}
		records[i] = subagentbatch.SubagentTaskRecord{
			TaskID:        taskID,
			BatchID:       batch.BatchID,
			ParentTaskID:  firstDependency(task.DependsOn),
			DependencyIDs: task.DependsOn,
			Role:          task.Role,
			Difficulty:    task.Difficulty,
			ReadOnly:      task.ReadOnly,
			Status:        subagentbatch.TaskPending,
			OrderIndex:    i,
			Spec:          specBytes,
			UpdatedAt:     now,
			Version:       1,
		}
	}

	created, err := c.store.CreateBatch(parentCtx, batch, records)
	if err != nil {
		return nil, fmt.Errorf("subagent batch: create batch: %w", err)
	}
	if !created {
		// Idempotent replay: the existing batch is the durable truth.
		return batch, nil
	}

	// The worker must outlive the parent turn, but stay bounded by the batch
	// deadline. Detaching from the parent context avoids killing queued work
	// when the caller returns.
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(parentCtx))
	if !deadline.IsZero() {
		var deadlineCancel context.CancelFunc
		workerCtx, deadlineCancel = context.WithDeadline(workerCtx, deadline)
		_ = deadlineCancel // retained by workerCtx; single cancel path below
	}

	c.mu.Lock()
	c.cancels[batch.BatchID] = cancel
	c.mu.Unlock()

	go c.runBatch(workerCtx, batch.BatchID, opts, tasks)
	return batch, nil
}

// Get returns a batch by id from the durable store.
func (c *SubagentBatchCoordinator) Get(ctx context.Context, batchID string) (*subagentbatch.SubagentBatch, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("subagent batch coordinator is not configured")
	}
	return c.store.GetBatch(ctx, batchID)
}

// Cancel requests cancellation of a running/queued batch: it durably marks the
// batch canceled, signals the worker context, and leaves pending tasks to be
// recorded as canceled by the worker. Cancel is idempotent for terminal
// batches (no-op).
func (c *SubagentBatchCoordinator) Cancel(ctx context.Context, batchID, reason string) error {
	if c == nil || c.store == nil {
		return fmt.Errorf("subagent batch coordinator is not configured")
	}
	now := subagentbatch.Now()
	_, err := c.store.UpdateBatch(ctx, batchID, -1, func(b *subagentbatch.SubagentBatch) {
		if b.Status.Terminal() {
			return
		}
		b.Status = subagentbatch.BatchCanceled
		b.CancelRequestedAt = &now
		b.CancelReason = reason
		b.FinishedAt = &now
		b.UpdatedAt = now
	})
	c.mu.Lock()
	if cancel := c.cancels[batchID]; cancel != nil {
		cancel()
	}
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("subagent batch: cancel %s: %w", batchID, err)
	}
	c.emit("subagent.batch.canceled", map[string]interface{}{
		"batch_id":      batchID,
		"cancel_reason": reason,
	})
	return nil
}

// runBatch is the detached worker goroutine for one batch.
func (c *SubagentBatchCoordinator) runBatch(ctx context.Context, batchID string, opts BatchStartOptions, tasks []SubagentTask) {
	defer c.forgetCancel(batchID)

	now := subagentbatch.Now()
	if _, err := c.store.UpdateBatch(ctx, batchID, -1, func(b *subagentbatch.SubagentBatch) {
		if b.Status.Terminal() || b.Status == subagentbatch.BatchRunning {
			return
		}
		b.Status = subagentbatch.BatchRunning
		b.StartedAt = &now
		b.HeartbeatAt = now
		b.OwnerID = opts.ParentSessionID
		b.FencingToken = fmt.Sprintf("%s/%d", opts.ParentSessionID, now.UnixNano())
	}); err != nil {
		c.emit("subagent.batch.failed", map[string]interface{}{
			"batch_id":    batchID,
			"error":       err.Error(),
			"error_class": subagentbatch.CanonicalErrorClass(err),
		})
		c.finalizeBatch(ctx, batchID, opts, tasks, nil, err)
		return
	}
	c.emit("subagent.batch.started", map[string]interface{}{
		"batch_id":            batchID,
		"parent_session_id":   opts.ParentSessionID,
		"parent_turn_id":      opts.ParentTurnID,
		"parent_tool_call_id": opts.ParentToolCallID,
		"trace_id":            opts.TraceID,
		"execution_mode":      string(opts.ExecutionMode),
		"task_count":          len(tasks),
	})

	reports, runErr := c.runTasksWithProgress(ctx, batchID, opts, tasks)
	c.finalizeBatch(ctx, batchID, opts, tasks, reports, runErr)
}

// runTasksWithProgress runs the tasks through the scheduler, mirroring task
// transitions (started) into the store along the way. Result persistence is
// deferred to finalizeBatch so the worker writes the cohort atomically and
// avoids racing per-task CAS from parallel wave goroutines.
func (c *SubagentBatchCoordinator) runTasksWithProgress(ctx context.Context, batchID string, opts BatchStartOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	if c.executor == nil {
		return nil, fmt.Errorf("subagent batch: scheduler is not configured")
	}
	runOpts := SubagentRunOptions{
		TraceID:          opts.TraceID,
		ParentSessionID:  opts.ParentSessionID,
		ParentToolCallID: opts.ParentToolCallID,
		Depth:            opts.Depth,
		OnTaskEvent: func(taskID, event string) {
			c.recordTaskEvent(ctx, batchID, taskID, event)
		},
	}
	return c.executor.RunChildren(ctx, runOpts, tasks)
}

// recordTaskEvent mirrors a single task lifecycle transition into the store.
func (c *SubagentBatchCoordinator) recordTaskEvent(ctx context.Context, batchID, taskID, event string) {
	now := subagentbatch.Now()
	switch event {
	case "started":
		if _, err := c.store.UpdateTask(ctx, batchID, taskID, -1, func(t *subagentbatch.SubagentTaskRecord) {
			if t.Status.Terminal() {
				return
			}
			t.Status = subagentbatch.TaskRunning
			t.StartedAt = &now
			t.UpdatedAt = now
		}); err == nil {
			c.emit("subagent.task.started", map[string]interface{}{
				"batch_id": batchID,
				"task_id":  taskID,
			})
		}
	}
}

// batchAbortReason captures why a worker stopped before producing reports for
// every task, so unfinished tasks can be durably classified instead of being
// recorded as generic failures.
type batchAbortReason int

const (
	batchAbortNone batchAbortReason = iota
	batchAbortCanceled
	batchAbortTimedOut
)

// reportProduced reports whether a SubagentResult carries real child output.
// The scheduler returns one result per started task, but a zero slot here means
// the task never ran; such tasks must be classified from the batch-level abort
// reason rather than treated as failures (TaskPending -> TaskFailed is not even
// a legal transition).
func reportProduced(r SubagentResult) bool {
	return r.Success || r.Error != "" || r.Summary != "" || r.SessionID != "" ||
		len(r.Findings) > 0 || len(r.Patches) > 0
}

// finalizeBatch persists per-task results, computes the terminal batch status
// and summary, and emits the matching terminal lifecycle event.
func (c *SubagentBatchCoordinator) finalizeBatch(ctx context.Context, batchID string, opts BatchStartOptions, tasks []SubagentTask, reports []SubagentResult, runErr error) {
	now := subagentbatch.Now()
	if reports == nil {
		reports = make([]SubagentResult, len(tasks))
	}
	// Classify tasks that never produced a report by the batch-level reason the
	// worker stopped: an explicit cancel maps them to TaskCanceled, a deadlined
	// worker to TaskTimedOut, so durable task records and the terminal batch
	// status agree. A plain "failed" would not even be a legal transition from
	// TaskPending.
	abortReason := batchAbortNone
	switch {
	case ctx.Err() == context.Canceled:
		abortReason = batchAbortCanceled
	case ctx.Err() == context.DeadlineExceeded:
		abortReason = batchAbortTimedOut
	}
	statuses := make(map[string]string, len(tasks))
	var criticalErrors []string

	for i, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			taskID = "task_" + fmt.Sprint(i)
		}
		var result subagentResultForStore
		var ts subagentbatch.TaskStatus
		if i < len(reports) && reportProduced(reports[i]) {
			r := reports[i]
			result = subagentResultForStore{
				TaskID:    taskID,
				Role:      task.Role,
				SessionID: r.SessionID,
				Success:   r.Success && r.Error == "",
				Summary:   r.Summary,
				Findings:  r.Findings,
				Patches:   batchFilePatchesToSpec(r.Patches),
				Error:     r.Error,
			}
			if r.Success && r.Error == "" {
				ts = subagentbatch.TaskSucceeded
			} else {
				ts = subagentbatch.TaskFailed
				criticalErrors = append(criticalErrors, compactTaskError(taskID, r.Error))
			}
		} else {
			switch abortReason {
			case batchAbortCanceled:
				result.Error = "batch canceled before task ran"
				ts = subagentbatch.TaskCanceled
			case batchAbortTimedOut:
				result.Error = "batch deadline exceeded before task ran"
				ts = subagentbatch.TaskTimedOut
			default:
				result.Error = "batch aborted before task ran"
				ts = subagentbatch.TaskFailed
				criticalErrors = append(criticalErrors, compactTaskError(taskID, result.Error))
			}
		}
		taskResult := &subagentbatch.TaskResult{
			TaskID:      result.TaskID,
			Role:        result.Role,
			SessionID:   result.SessionID,
			Success:     result.Success,
			Summary:     result.Summary,
			Findings:    result.Findings,
			Patches:     result.Patches,
			Error:       result.Error,
			ArtifactRef: result.ArtifactRef,
		}
		if err := c.store.RecordTaskResult(ctx, batchID, taskID, -1, ts, taskResult); err != nil {
			c.emit("subagent.batch.progress", map[string]interface{}{
				"batch_id":    batchID,
				"task_id":     taskID,
				"error":       err.Error(),
				"error_class": subagentbatch.CanonicalErrorClass(err),
			})
		}
		statuses[taskID] = string(ts)
		c.emit("subagent.task.completed", map[string]interface{}{
			"batch_id": batchID,
			"task_id":  taskID,
			"status":   string(ts),
		})
	}

	batch, err := c.store.GetBatch(ctx, batchID)
	if err != nil || batch == nil {
		batch = &subagentbatch.SubagentBatch{BatchID: batchID, CreatedAt: now}
	}
	records, _ := c.store.ListTasks(ctx, batchID)
	_, _, completed, failed, canceled, timedOut := subagentbatch.Counts(records)

	status, errorClass := c.terminalBatchStatus(ctx, batch, runErr, failed, canceled, timedOut, completed)
	summary := subagentbatch.BatchSummary{
		BatchID:        batchID,
		Status:         status,
		TaskCount:      len(tasks),
		CompletedCount: completed,
		FailedCount:    failed,
		CanceledCount:  canceled,
		TimedOutCount:  timedOut,
		ElapsedMillis:  elapsedMillis(batch.CreatedAt, now),
		ErrorClass:     errorClass,
		CriticalErrors: compactStrings(criticalErrors),
		TaskStatuses:   statuses,
		CreatedAt:      batch.CreatedAt,
		FinishedAt:     now,
	}
	summaryJSON, _ := json.Marshal(summary)

	errorDetail := ""
	if runErr != nil {
		errorDetail = runErr.Error()
	}
	finishedAt := now
	_, updateErr := c.store.UpdateBatch(ctx, batchID, -1, func(b *subagentbatch.SubagentBatch) {
		if b.Status.Terminal() {
			return
		}
		b.Status = status
		b.ResultSummary = summaryJSON
		b.ErrorClass = errorClass
		b.ErrorDetail = errorDetail
		b.UpdatedAt = now
		b.FinishedAt = &finishedAt
		b.HeartbeatAt = now
		// Keep the durable count columns consistent with the summary so
		// recovery/dashboards never see a completed batch with zero counts.
		b.QueuedCount = len(tasks) - (completed + failed + canceled + timedOut)
		b.RunningCount = 0
		b.CompletedCount = completed
		b.FailedCount = failed
		b.CanceledCount = canceled
		b.TimedOutCount = timedOut
	})
	if updateErr != nil && runErr == nil {
		runErr = updateErr
		errorClass = subagentbatch.CanonicalErrorClass(updateErr)
		status = subagentbatch.BatchFailed
	}

	c.emitTerminalEvent(ctx, batchID, status, opts, summary, runErr)
}

// terminalBatchStatus decides the durable terminal status for the batch. Order
// matters: an explicit cancel wins, then deadline (timed_out), then failures,
// then all-succeeded (completed).
func (c *SubagentBatchCoordinator) terminalBatchStatus(ctx context.Context, batch *subagentbatch.SubagentBatch, runErr error, failed, canceled, timedOut, completed int) (subagentbatch.BatchStatus, string) {
	if ctx.Err() == context.DeadlineExceeded {
		return subagentbatch.BatchTimedOut, "deadline"
	}
	if batch != nil && batch.CancelRequestedAt != nil {
		return subagentbatch.BatchCanceled, "canceled"
	}
	if runErr != nil {
		return subagentbatch.BatchFailed, subagentbatch.CanonicalErrorClass(runErr)
	}
	if canceled > 0 {
		return subagentbatch.BatchCanceled, "canceled"
	}
	if timedOut > 0 {
		return subagentbatch.BatchTimedOut, "timeout"
	}
	if failed > 0 {
		return subagentbatch.BatchFailed, "task_failure"
	}
	return subagentbatch.BatchCompleted, ""
}

func (c *SubagentBatchCoordinator) emitTerminalEvent(ctx context.Context, batchID string, status subagentbatch.BatchStatus, opts BatchStartOptions, summary subagentbatch.BatchSummary, runErr error) {
	payload := map[string]interface{}{
		"batch_id":          batchID,
		"parent_session_id": opts.ParentSessionID,
		"execution_mode":    string(opts.ExecutionMode),
		"status":            string(status),
		"task_count":        summary.TaskCount,
		"completed_count":   summary.CompletedCount,
		"failed_count":      summary.FailedCount,
		"elapsed_ms":        summary.ElapsedMillis,
	}
	if runErr != nil {
		payload["error"] = runErr.Error()
		payload["error_class"] = summary.ErrorClass
	}
	eventType := "subagent.batch.completed"
	switch status {
	case subagentbatch.BatchFailed:
		eventType = "subagent.batch.failed"
	case subagentbatch.BatchCanceled:
		eventType = "subagent.batch.canceled"
	case subagentbatch.BatchTimedOut:
		eventType = "subagent.batch.timed_out"
	}
	c.emit(eventType, payload)
}

func (c *SubagentBatchCoordinator) emit(eventType string, payload map[string]interface{}) {
	if c == nil || c.emitter == nil {
		return
	}
	c.emitter(eventType, payload)
}

func (c *SubagentBatchCoordinator) forgetCancel(batchID string) {
	c.mu.Lock()
	delete(c.cancels, batchID)
	c.mu.Unlock()
}

// --- helpers ------------------------------------------------------------

// subagentResultForStore is the host-side bridge between a scheduler report
// and a durable TaskResult capsule.
type subagentResultForStore struct {
	TaskID      string
	Role        string
	SessionID   string
	Success     bool
	Summary     string
	Findings    []string
	Patches     []subagentbatch.PatchSpec
	Error       string
	ArtifactRef string
}

func taskToBatchSpec(t SubagentTask, taskID string) subagentbatch.TaskSpec {
	patches := make([]subagentbatch.PatchSpec, 0, len(t.PatchContext))
	for _, p := range t.PatchContext {
		patches = append(patches, subagentbatch.PatchSpec{
			Path:               p.Path,
			Diff:               p.Diff,
			Summary:            p.Summary,
			ApplyStatus:        p.ApplyStatus,
			AppliedBy:          p.AppliedBy,
			VerificationStatus: p.VerificationStatus,
			VerifiedBy:         p.VerifiedBy,
			ArtifactRefs:       p.ArtifactRefs,
		})
	}
	return subagentbatch.TaskSpec{
		ID:                    taskID,
		Role:                  t.Role,
		Goal:                  t.Goal,
		Difficulty:            t.Difficulty,
		DifficultyRationale:   t.DifficultyRationale,
		Provider:              t.Provider,
		Model:                 t.Model,
		ReasoningEffort:       t.ReasoningEffort,
		ToolsWhitelist:        t.ToolsWhitelist,
		DependsOn:             t.DependsOn,
		Patches:               patches,
		BudgetTokens:          t.BudgetTokens,
		TimeoutSec:            t.TimeoutSec,
		ReadOnly:              t.ReadOnly,
		CompletionRequirement: t.CompletionRequirement,
	}
}

func firstDependency(deps []string) string {
	if len(deps) == 0 {
		return ""
	}
	return deps[0]
}

func batchFilePatchesToSpec(patches []FilePatch) []subagentbatch.PatchSpec {
	out := make([]subagentbatch.PatchSpec, 0, len(patches))
	for _, p := range patches {
		out = append(out, subagentbatch.PatchSpec{
			Path:               p.Path,
			Diff:               p.Diff,
			Summary:            p.Summary,
			ApplyStatus:        p.ApplyStatus,
			AppliedBy:          p.AppliedBy,
			VerificationStatus: p.VerificationStatus,
			VerifiedBy:         p.VerifiedBy,
			ArtifactRefs:       p.ArtifactRefs,
		})
	}
	return out
}

func compactTaskError(taskID, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return taskID + ": error"
	}
	if len(message) > 240 {
		message = message[:240] + "..."
	}
	return taskID + ": " + message
}

func compactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func elapsedMillis(start time.Time, end time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return d.Milliseconds()
}
