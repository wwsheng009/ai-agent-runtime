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
	"crypto/sha256"
	"encoding/json"
	"errors"
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

// BatchTerminalDeliveryStatus distinguishes a durable mailbox write from a
// compatibility fallback. A failed delivery remains recoverable from the
// terminal BatchStore record and must never be reported as delivered.
type BatchTerminalDeliveryStatus string

const (
	BatchTerminalDeliveryPersisted BatchTerminalDeliveryStatus = "persisted"
	BatchTerminalDeliveryFallback  BatchTerminalDeliveryStatus = "fallback"
	BatchTerminalDeliveryFailed    BatchTerminalDeliveryStatus = "failed"
)

// BatchTerminalNotification is the host-neutral terminal outbox item produced
// after the batch and all task results have reached durable terminal state.
type BatchTerminalNotification struct {
	Batch       subagentbatch.SubagentBatch
	EventType   string
	DeliveryKey string
	Payload     map[string]interface{}
}

// BatchTerminalDelivery is returned by a host terminal sink. DeliveryKey may
// be echoed by the sink when its durable mailbox uses a different canonical
// identifier; empty preserves the coordinator's deterministic key.
type BatchTerminalDelivery struct {
	Status           BatchTerminalDeliveryStatus
	DeliveryKey      string
	AlreadyDelivered bool
	Err              error
}

// BatchTerminalReplayResult summarizes one durable recovery scan. Failed
// deliveries remain in the terminal batch ledger and are safe to retry.
type BatchTerminalReplayResult struct {
	Scanned   int
	Delivered int
	Duplicate int
	Failed    int
}

// BatchTerminalSink writes the terminal control-plane notification (normally
// to the parent session mailbox). It runs before the runtime display mirror.
type BatchTerminalSink func(ctx context.Context, notification BatchTerminalNotification) BatchTerminalDelivery

// SubagentBatchCoordinatorConfig wires a coordinator to its durable store,
// execution kernel and event sink.
type SubagentBatchCoordinatorConfig struct {
	Store                   subagentbatch.BatchStore
	Scheduler               *SubagentScheduler
	Emitter                 BatchEmitter
	TerminalSink            BatchTerminalSink
	LifecycleProjector      BatchLifecycleProjector
	DefaultDeadline         time.Duration // applied when a request carries no deadline
	TerminalDeliveryTimeout time.Duration
	HeartbeatInterval       time.Duration
}

// subagentExecutor is the kernel abstraction the coordinator drives. It is a
// small seam over *SubagentScheduler so tests can inject a fake executor and
// assert on durable batch lifecycle without spawning real child sessions.
type subagentExecutor interface {
	RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error)
}

// SubagentBatchCoordinator persists and drives background subagent batches.
type SubagentBatchCoordinator struct {
	store              subagentbatch.BatchStore
	executor           subagentExecutor
	emitter            BatchEmitter
	sink               BatchTerminalSink
	lifecycleProjector BatchLifecycleProjector
	deadline           time.Duration
	deliveryTimeout    time.Duration
	heartbeatEvery     time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	owned   map[string]struct{}
	// abandoned is set after shutdown times out. It is intentionally separate
	// from owned: the worker may still unwind after its durable batch has been
	// orphaned, and late scheduler callbacks must not write task/result rows
	// back into a terminal record.
	abandoned map[string]struct{}
	workers   sync.WaitGroup
	stateMu   sync.Mutex
	closed    bool
	ownerID   string
	writeMu   sync.RWMutex

	terminalMu        sync.Mutex
	terminalDelivered map[string]struct{}
	emitterMu         sync.RWMutex
	lifecycleMu       sync.RWMutex
}

// NewSubagentBatchCoordinator constructs a coordinator from a config. The
// store must be non-nil for StartBackground to work.
func NewSubagentBatchCoordinator(cfg SubagentBatchCoordinatorConfig) *SubagentBatchCoordinator {
	if cfg.DefaultDeadline <= 0 {
		cfg.DefaultDeadline = 30 * time.Minute
	}
	if cfg.TerminalDeliveryTimeout <= 0 {
		cfg.TerminalDeliveryTimeout = 5 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = time.Minute
	}
	scheduler := cfg.Scheduler
	return &SubagentBatchCoordinator{
		store:              cfg.Store,
		executor:           scheduler,
		emitter:            cfg.Emitter,
		sink:               cfg.TerminalSink,
		lifecycleProjector: cfg.LifecycleProjector,
		deadline:           cfg.DefaultDeadline,
		deliveryTimeout:    cfg.TerminalDeliveryTimeout,
		heartbeatEvery:     cfg.HeartbeatInterval,
		cancels:            make(map[string]context.CancelFunc),
		owned:              make(map[string]struct{}),
		abandoned:          make(map[string]struct{}),
		terminalDelivered:  make(map[string]struct{}),
		ownerID:            subagentbatch.NewID("coordinator"),
	}
}

// SetLifecycleProjector installs or replaces the host lifecycle adapter. It
// is safe to call while workers are running; the callback is snapshotted before
// invocation and never called while coordinator write/terminal locks are held.
func (c *SubagentBatchCoordinator) SetLifecycleProjector(projector BatchLifecycleProjector) {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	c.lifecycleProjector = projector
	c.lifecycleMu.Unlock()
}

func (c *SubagentBatchCoordinator) lifecycleProjectorSnapshot() BatchLifecycleProjector {
	if c == nil {
		return nil
	}
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	return c.lifecycleProjector
}

func (c *SubagentBatchCoordinator) coordinatorOwnerID() string {
	if c == nil {
		return ""
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if strings.TrimSpace(c.ownerID) == "" {
		c.ownerID = subagentbatch.NewID("coordinator")
	}
	return c.ownerID
}

// SetTerminalSink installs the durable parent-notification sink. Hosts may use
// this after lazy coordinator creation without replacing the batch store.
func (c *SubagentBatchCoordinator) SetTerminalSink(sink BatchTerminalSink) {
	if c == nil {
		return
	}
	c.terminalMu.Lock()
	c.sink = sink
	c.terminalMu.Unlock()
}

// SetScheduler replaces the execution kernel without changing the durable
// store. Hosts that share a coordinator across actors call this before a new
// batch starts so the current actor remains the scheduler parent.
func (c *SubagentBatchCoordinator) SetScheduler(scheduler *SubagentScheduler) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.executor = scheduler
	c.mu.Unlock()
}

// SetTerminalSinkAndReplay is safe to call repeatedly. The durable mailbox
// message id is the cross-process idempotency boundary; the in-memory marker
// only avoids re-running a sink within this coordinator instance. It installs
// a sink and performs a bounded terminal recovery scan without racing replay
// against sink configuration.
func (c *SubagentBatchCoordinator) SetTerminalSinkAndReplay(ctx context.Context, sink BatchTerminalSink, parentSessionID string, limit int) (BatchTerminalReplayResult, error) {
	c.SetTerminalSink(sink)
	return c.ReplayTerminalDeliveries(ctx, parentSessionID, limit)
}

// SetEmitter replaces the display-mirror sink without changing the durable
// store or scheduler. Hosts use this when a coordinator is shared/injected.
func (c *SubagentBatchCoordinator) SetEmitter(emitter BatchEmitter) {
	if c == nil {
		return
	}
	c.emitterMu.Lock()
	c.emitter = emitter
	c.emitterMu.Unlock()
}

// Store exposes the durable control plane (used by hosts for recovery).
func (c *SubagentBatchCoordinator) Store() subagentbatch.BatchStore {
	if c == nil {
		return nil
	}
	return c.store
}

// ReplayTerminalDeliveries scans durable terminal batches and retries their
// parent-mailbox notification. It is intended for host startup after the sink
// is configured. Mailbox message-id idempotency makes replay safe even when a
// previous process committed the row but crashed before recording success.
func (c *SubagentBatchCoordinator) ReplayTerminalDeliveries(ctx context.Context, parentSessionID string, limit int) (BatchTerminalReplayResult, error) {
	var result BatchTerminalReplayResult
	if c == nil || c.store == nil {
		return result, fmt.Errorf("subagent batch coordinator is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	filter := subagentbatch.BatchFilter{
		ParentSessionID: strings.TrimSpace(parentSessionID),
		Status: []subagentbatch.BatchStatus{
			subagentbatch.BatchCompleted,
			subagentbatch.BatchFailed,
			subagentbatch.BatchCanceled,
			subagentbatch.BatchTimedOut,
			subagentbatch.BatchOrphaned,
		},
		ExecutionMode: []subagentbatch.ExecutionMode{subagentbatch.ExecutionModeBackground},
		Limit:         limit,
	}
	batches, err := c.store.ListBatches(ctx, filter)
	if err != nil {
		return result, err
	}
	var deliveryErrors []error
	for i := range batches {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(errors.Join(deliveryErrors...), err)
		}
		batch := batches[i]
		result.Scanned++
		eventType, deliveryKey, payload := terminalNotificationFromBatch(&batch)
		if projectionErr := c.projectTerminalLifecycle(ctx, &batch, eventType, nil); projectionErr != nil {
			result.Failed++
			deliveryErrors = append(deliveryErrors, fmt.Errorf("batch %s: lifecycle projection: %w", batch.BatchID, projectionErr))
			payload["supervision_projection_error"] = projectionErr.Error()
		}
		if strings.TrimSpace(batch.ParentSessionID) == "" {
			result.Failed++
			deliveryErrors = append(deliveryErrors, fmt.Errorf("batch %s: parent session id is empty", batch.BatchID))
			continue
		}
		delivery := c.deliverTerminalOnce(ctx, &batch, eventType, deliveryKey, payload)
		if delivery.Err != nil || delivery.Status == BatchTerminalDeliveryFailed {
			result.Failed++
			deliveryErr := delivery.Err
			if deliveryErr == nil {
				deliveryErr = fmt.Errorf("terminal sink reported failed delivery")
			}
			deliveryErrors = append(deliveryErrors, fmt.Errorf("batch %s: %w", batch.BatchID, deliveryErr))
			continue
		}
		if delivery.AlreadyDelivered {
			result.Duplicate++
			continue
		}
		result.Delivered++
		if strings.TrimSpace(delivery.DeliveryKey) != "" {
			payload["delivery_key"] = strings.TrimSpace(delivery.DeliveryKey)
		}
		payload["mailbox_delivery_status"] = string(delivery.Status)
		payload["replayed"] = true
		c.emit(eventType, payload)
	}
	return result, errors.Join(deliveryErrors...)
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
	c.stateMu.Lock()
	closed := c.closed
	c.stateMu.Unlock()
	if closed {
		return nil, fmt.Errorf("subagent batch coordinator is closed")
	}
	c.coordinatorOwnerID()
	if len(tasks) == 0 {
		return nil, fmt.Errorf("subagent batch requires at least one task")
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	mode := opts.ExecutionMode
	if mode == "" {
		mode = subagentbatch.ExecutionModeBackground
	}
	opts.ExecutionMode = mode
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
		if batch.Status.Terminal() {
			eventType, _, _ := terminalNotificationFromBatch(batch)
			// A caller may replay an already-terminal idempotency key after a
			// process crashed between terminal persistence and projection. Do
			// not return the handle without giving the host projection another
			// at-least-once opportunity.
			_ = c.projectTerminalLifecycle(parentCtx, batch, eventType, nil)
			return batch, nil
		}
		// A replay can arrive after a process crashed between durable creation
		// and worker admission. Do not leave a queued record permanently stuck;
		// only this coordinator may claim the newly admitted worker below.
		if batch.Status != subagentbatch.BatchQueued {
			return batch, nil
		}
		// Never re-admit a queued replay from the caller's in-memory request.
		// The request may be retried with a different task list or deadline after
		// the durable CreateBatch committed but before worker admission. Loading
		// the task specs and metadata from the existing row makes the idempotency
		// key a true replay boundary rather than merely a duplicate-batch guard.
		durableTasks, loadErr := c.loadDurableTasks(parentCtx, batch.BatchID)
		if loadErr != nil {
			return batch, fmt.Errorf("subagent batch: load existing tasks: %w", loadErr)
		}
		durableOpts := opts
		durableOpts.TraceID = batch.TraceID
		durableOpts.ParentSessionID = batch.ParentSessionID
		durableOpts.ParentTurnID = batch.ParentTurnID
		durableOpts.ParentToolCallID = batch.ParentToolCallID
		durableOpts.RootScopeID = batch.RootScopeID
		durableOpts.ExecutionMode = batch.ExecutionMode
		durableOpts.IdempotencyKey = batch.IdempotencyKey
		durableOpts.BatchDeadline = batch.BatchDeadline
		if err := c.admitExistingBatch(parentCtx, batch, durableOpts, durableTasks, batch.BatchDeadline); err != nil {
			return batch, err
		}
		return batch, nil
	}

	// The worker must outlive the parent turn, but stay bounded by the batch
	// deadline. Detaching from the parent context avoids killing queued work
	// when the caller returns.
	workerCtx, cancel := context.WithCancel(agentWithoutCancel(parentCtx))
	if !deadline.IsZero() {
		var deadlineCancel context.CancelFunc
		workerCtx, deadlineCancel = context.WithDeadline(workerCtx, deadline)
		baseCancel := cancel
		cancel = func() {
			deadlineCancel()
			baseCancel()
		}
	}

	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		cancel()
		_ = c.markBatchOrphaned(context.Background(), batch.BatchID, "coordinator closed before worker admission")
		return batch, fmt.Errorf("subagent batch coordinator is closed")
	}
	c.workers.Add(1)
	c.mu.Lock()
	if c.owned == nil {
		c.owned = make(map[string]struct{})
	}
	if c.cancels == nil {
		c.cancels = make(map[string]context.CancelFunc)
	}
	c.owned[batch.BatchID] = struct{}{}
	c.cancels[batch.BatchID] = cancel
	c.mu.Unlock()
	c.stateMu.Unlock()

	go func() {
		defer c.workers.Done()
		defer cancel()
		c.runBatch(workerCtx, batch.BatchID, opts, tasks)
	}()
	return batch, nil
}

func (c *SubagentBatchCoordinator) admitExistingBatch(parentCtx context.Context, batch *subagentbatch.SubagentBatch, opts BatchStartOptions, tasks []SubagentTask, deadline time.Time) error {
	if c == nil || batch == nil {
		return fmt.Errorf("subagent batch coordinator is not configured")
	}
	c.coordinatorOwnerID()
	c.mu.Lock()
	if _, alreadyOwned := c.owned[batch.BatchID]; alreadyOwned {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	workerCtx, cancel := context.WithCancel(agentWithoutCancel(parentCtx))
	if !deadline.IsZero() {
		var deadlineCancel context.CancelFunc
		workerCtx, deadlineCancel = context.WithDeadline(workerCtx, deadline)
		baseCancel := cancel
		cancel = func() {
			deadlineCancel()
			baseCancel()
		}
	}
	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		cancel()
		return fmt.Errorf("subagent batch coordinator is closed")
	}
	c.mu.Lock()
	if c.owned == nil {
		c.owned = make(map[string]struct{})
	}
	if c.cancels == nil {
		c.cancels = make(map[string]context.CancelFunc)
	}
	if _, alreadyOwned := c.owned[batch.BatchID]; alreadyOwned {
		c.mu.Unlock()
		c.stateMu.Unlock()
		cancel()
		return nil
	}
	c.workers.Add(1)
	c.owned[batch.BatchID] = struct{}{}
	c.cancels[batch.BatchID] = cancel
	c.mu.Unlock()
	c.stateMu.Unlock()
	go func() {
		defer c.workers.Done()
		defer cancel()
		c.runBatch(workerCtx, batch.BatchID, opts, tasks)
	}()
	return nil
}

// loadDurableTasks reconstructs the scheduler-facing task slice from the
// persisted task specs. It is used only for idempotent admission replay; a
// retry must not execute a caller-supplied task definition that differs from
// the already committed batch.
func (c *SubagentBatchCoordinator) loadDurableTasks(ctx context.Context, batchID string) ([]SubagentTask, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("subagent batch coordinator is not configured")
	}
	records, err := c.store.ListTasks(ctx, batchID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("batch %s has no durable tasks", batchID)
	}
	tasks := make([]SubagentTask, 0, len(records))
	for _, record := range records {
		var spec subagentbatch.TaskSpec
		if len(record.Spec) == 0 {
			return nil, fmt.Errorf("task %s has no durable spec", record.TaskID)
		}
		if err := json.Unmarshal(record.Spec, &spec); err != nil {
			return nil, fmt.Errorf("decode task %s spec: %w", record.TaskID, err)
		}
		tasks = append(tasks, subagentTaskFromBatchSpec(record, spec))
	}
	return tasks, nil
}

// Get returns a batch by id from the durable store.
func (c *SubagentBatchCoordinator) Get(ctx context.Context, batchID string) (*subagentbatch.SubagentBatch, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("subagent batch coordinator is not configured")
	}
	return c.store.GetBatch(ctx, batchID)
}

// Cancel requests cancellation of a running/queued batch: it durably marks the
// batch canceled and signals the worker context; pending tasks are recorded as
// canceled by the worker and the single terminal subagent.batch.canceled event
// is emitted by finalizeBatch (with cancel_reason/cancel_requested_at), so
// Cancel itself does not emit a duplicate terminal event. Cancel is idempotent
// for terminal batches (no-op).
func (c *SubagentBatchCoordinator) Cancel(ctx context.Context, batchID, reason string) error {
	if c == nil || c.store == nil {
		return fmt.Errorf("subagent batch coordinator is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := subagentbatch.Now()
	changed := false
	updated, err := c.store.UpdateBatch(ctx, batchID, -1, func(b *subagentbatch.SubagentBatch) {
		if b.Status.Terminal() {
			return
		}
		changed = true
		b.Status = subagentbatch.BatchCanceled
		b.CancelRequestedAt = &now
		b.CancelReason = reason
		b.FinishedAt = &now
		b.UpdatedAt = now
	})
	if err != nil {
		return fmt.Errorf("subagent batch: cancel %s: %w", batchID, err)
	}
	c.mu.Lock()
	cancel := c.cancels[batchID]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	unowned := updated != nil &&
		updated.Status == subagentbatch.BatchCanceled &&
		strings.TrimSpace(updated.OwnerID) == "" &&
		strings.TrimSpace(updated.FencingToken) == ""
	if changed && unowned {
		// A queued batch can be canceled in the narrow window after durable
		// creation but before worker admission. The cancel function may already
		// be registered even though the worker has not claimed the durable row;
		// no worker will run finalizeBatch in that case, so close its task
		// records and deliver the terminal notification here.
		if err := c.finalizeUnownedCanceledBatch(ctx, updated, reason); err != nil {
			return fmt.Errorf("subagent batch: finalize cancel %s: %w", batchID, err)
		}
	}
	return nil
}

func (c *SubagentBatchCoordinator) finalizeUnownedCanceledBatch(ctx context.Context, batch *subagentbatch.SubagentBatch, reason string) error {
	if c == nil || c.store == nil || batch == nil {
		return nil
	}
	records, err := c.store.ListTasks(ctx, batch.BatchID)
	if err != nil {
		return err
	}
	var taskErrors []error
	for _, record := range records {
		if record.Status.Terminal() {
			continue
		}
		taskID := record.TaskID
		_, updateErr := c.store.UpdateTask(ctx, batch.BatchID, taskID, record.Version, func(task *subagentbatch.SubagentTaskRecord) {
			if task.Status.Terminal() {
				return
			}
			now := subagentbatch.Now()
			task.Status = subagentbatch.TaskCanceled
			task.ErrorClass = "canceled"
			task.ErrorCode = "batch_canceled"
			task.FinishedAt = &now
			task.UpdatedAt = now
		})
		if updateErr != nil {
			var conflict *subagentbatch.VersionConflictError
			if !errors.As(updateErr, &conflict) {
				taskErrors = append(taskErrors, fmt.Errorf("task %s: %w", taskID, updateErr))
			}
		}
	}
	if len(taskErrors) > 0 {
		return errors.Join(taskErrors...)
	}
	latest, err := c.store.GetBatch(ctx, batch.BatchID)
	if err != nil {
		return err
	}
	if latest == nil || latest.Status != subagentbatch.BatchCanceled {
		return nil
	}
	records, err = c.store.ListTasks(ctx, batch.BatchID)
	if err != nil {
		return err
	}
	queued, running, completed, failed, canceled, timedOut := subagentbatch.Counts(records)
	now := subagentbatch.Now()
	summary := subagentbatch.BatchSummary{
		BatchID:        latest.BatchID,
		Status:         subagentbatch.BatchCanceled,
		TaskCount:      latest.TaskCount,
		CompletedCount: completed,
		FailedCount:    failed,
		CanceledCount:  canceled,
		TimedOutCount:  timedOut,
		ElapsedMillis:  elapsedMillis(latest.CreatedAt, now),
		ErrorClass:     "canceled",
		CreatedAt:      latest.CreatedAt,
		FinishedAt:     now,
	}
	summaryJSON, _ := json.Marshal(summary)
	updated, err := c.store.UpdateBatch(ctx, latest.BatchID, latest.Version, func(current *subagentbatch.SubagentBatch) {
		if current.Status != subagentbatch.BatchCanceled {
			return
		}
		current.ResultSummary = summaryJSON
		current.ErrorClass = "canceled"
		current.ErrorDetail = reason
		current.QueuedCount = queued
		current.RunningCount = running
		current.CompletedCount = completed
		current.FailedCount = failed
		current.CanceledCount = canceled
		current.TimedOutCount = timedOut
		current.FinishedAt = latest.FinishedAt
		current.HeartbeatAt = now
	})
	if err != nil {
		var conflict *subagentbatch.VersionConflictError
		if errors.As(err, &conflict) {
			return nil
		}
		return err
	}
	if updated == nil || updated.Status != subagentbatch.BatchCanceled {
		return nil
	}
	eventType, deliveryKey, payload := terminalNotificationFromBatch(updated)
	if projectionErr := c.projectTerminalLifecycle(ctx, updated, eventType, nil); projectionErr != nil {
		payload["supervision_projection_error"] = projectionErr.Error()
	}
	delivery := c.deliverTerminalOnce(ctx, updated, eventType, deliveryKey, payload)
	if strings.TrimSpace(delivery.DeliveryKey) != "" {
		payload["delivery_key"] = strings.TrimSpace(delivery.DeliveryKey)
	}
	payload["mailbox_delivery_status"] = string(delivery.Status)
	if delivery.Err != nil {
		payload["mailbox_delivery_error"] = delivery.Err.Error()
	}
	if !delivery.AlreadyDelivered {
		c.emit(eventType, payload)
	}
	return nil
}

// Shutdown stops workers owned by this coordinator and waits for their durable
// finalization. If a worker does not honor cancellation before ctx expires,
// its batch is marked orphaned so a later process can replay a terminal
// notification instead of observing a permanently running record.
func (c *SubagentBatchCoordinator) Shutdown(ctx context.Context, reason string) error {
	if c == nil || c.store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.stateMu.Lock()
	c.closed = true
	c.stateMu.Unlock()
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.cancels))
	for _, cancel := range c.cancels {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		c.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.abandonOwnedAndMarkOrphaned(persistCtx, reason); err != nil {
			return errors.Join(ctx.Err(), err)
		}
		return ctx.Err()
	}
}

// abandonOwnedAndMarkOrphaned establishes a write barrier before changing
// owned rows to orphaned. This closes the shutdown race where a stubborn
// scheduler returns after the timeout and its late finalize/progress callback
// otherwise writes task rows after the batch has become terminal.
func (c *SubagentBatchCoordinator) abandonOwnedAndMarkOrphaned(ctx context.Context, reason string) error {
	c.writeMu.Lock()
	c.mu.Lock()
	if c.abandoned == nil {
		c.abandoned = make(map[string]struct{})
	}
	for batchID := range c.owned {
		c.abandoned[batchID] = struct{}{}
	}
	c.mu.Unlock()
	defer c.writeMu.Unlock()
	return c.markOwnedOrphaned(ctx, reason)
}

func (c *SubagentBatchCoordinator) acquireBatchWrite(batchID string) (func(), bool) {
	if c == nil {
		return func() {}, false
	}
	c.writeMu.RLock()
	c.mu.Lock()
	_, abandoned := c.abandoned[batchID]
	c.mu.Unlock()
	if abandoned {
		c.writeMu.RUnlock()
		return func() {}, false
	}
	return c.writeMu.RUnlock, true
}

func (c *SubagentBatchCoordinator) markBatchOrphaned(ctx context.Context, batchID, reason string) error {
	batch, err := c.store.GetBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if batch == nil || batch.Status.Terminal() {
		return nil
	}
	return c.convergeBatchTerminal(ctx, batch, subagentbatch.BatchOrphaned, reason)
}

func (c *SubagentBatchCoordinator) markOwnedOrphaned(ctx context.Context, reason string) error {
	c.mu.Lock()
	batchIDs := make([]string, 0, len(c.owned))
	for batchID := range c.owned {
		batchIDs = append(batchIDs, batchID)
	}
	c.mu.Unlock()
	var errs []error
	for _, batchID := range batchIDs {
		batch, err := c.store.GetBatch(ctx, batchID)
		if err != nil {
			errs = append(errs, fmt.Errorf("batch %s: %w", batchID, err))
			continue
		}
		if batch == nil || batch.Status.Terminal() {
			continue
		}
		if err := c.convergeBatchTerminal(ctx, batch, subagentbatch.BatchOrphaned, reason); err != nil {
			errs = append(errs, fmt.Errorf("batch %s: %w", batchID, err))
		}
	}
	return errors.Join(errs...)
}

// convergeBatchTerminal is the recovery/shutdown counterpart of finalizeBatch.
// It never fabricates task results; it only closes the batch and emits one
// durable terminal notification from the resulting record.
func (c *SubagentBatchCoordinator) convergeBatchTerminal(ctx context.Context, batch *subagentbatch.SubagentBatch, status subagentbatch.BatchStatus, reason string) error {
	if c == nil || c.store == nil || batch == nil {
		return fmt.Errorf("subagent batch terminal convergence is not configured")
	}
	now := subagentbatch.Now()
	updated, err := c.store.UpdateBatch(ctx, batch.BatchID, batch.Version, func(current *subagentbatch.SubagentBatch) {
		if current.Status.Terminal() {
			return
		}
		current.Status = status
		current.ErrorClass = subagentbatch.CanonicalErrorClass(errors.New(reason))
		current.ErrorDetail = reason
		if status == subagentbatch.BatchCanceled {
			current.CancelReason = reason
			current.CancelRequestedAt = &now
		}
		current.UpdatedAt = now
		current.FinishedAt = &now
		current.HeartbeatAt = now
		current.RunningCount = 0
		current.QueuedCount = 0
	})
	if err != nil {
		var conflict *subagentbatch.VersionConflictError
		if errors.As(err, &conflict) {
			// A live worker, cancel request, or another recovery scanner advanced
			// this row after our snapshot. Never let stale recovery overwrite the
			// newer owner/state; a later bounded scan can reassess fresh durable
			// state if it is still recoverable.
			return nil
		}
		return err
	}
	if updated == nil || !updated.Status.Terminal() {
		return nil
	}
	eventType, deliveryKey, payload := terminalNotificationFromBatch(updated)
	if projectionErr := c.projectTerminalLifecycle(ctx, updated, eventType, nil); projectionErr != nil {
		payload["supervision_projection_error"] = projectionErr.Error()
	}
	delivery := c.deliverTerminalOnce(ctx, updated, eventType, deliveryKey, payload)
	if delivery.Err != nil {
		return delivery.Err
	}
	if delivery.AlreadyDelivered {
		return nil
	}
	if strings.TrimSpace(delivery.DeliveryKey) != "" {
		payload["delivery_key"] = strings.TrimSpace(delivery.DeliveryKey)
	}
	payload["mailbox_delivery_status"] = string(delivery.Status)
	payload["recovered"] = true
	c.emit(eventType, payload)
	return nil
}

// RecoverStaleBatches converges non-terminal records left by a previous
// process. Fresh records are left alone so opening another actor in the same
// host cannot orphan a live worker. A zero staleAfter intentionally makes all
// recoverable rows eligible.
func (c *SubagentBatchCoordinator) RecoverStaleBatches(ctx context.Context, staleAfter time.Duration, parentSessionID string, limit int) (int, error) {
	if c == nil || c.store == nil {
		return 0, fmt.Errorf("subagent batch coordinator is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	batches, err := c.store.ListBatches(ctx, subagentbatch.BatchFilter{
		ParentSessionID: strings.TrimSpace(parentSessionID),
		Status:          []subagentbatch.BatchStatus{subagentbatch.BatchQueued, subagentbatch.BatchRunning, subagentbatch.BatchPartiallyCompleted},
		ExecutionMode:   []subagentbatch.ExecutionMode{subagentbatch.ExecutionModeBackground},
		Limit:           limit,
	})
	if err != nil {
		return 0, err
	}
	cutoff := subagentbatch.Now().Add(-staleAfter)
	changed := 0
	for i := range batches {
		batch := batches[i]
		last := batch.HeartbeatAt
		if last.IsZero() {
			last = batch.UpdatedAt
		}
		deadlineActive := !batch.BatchDeadline.IsZero() && batch.BatchDeadline.After(subagentbatch.Now())
		if staleAfter > 0 && last.After(cutoff) && (deadlineActive || batch.BatchDeadline.IsZero()) {
			continue
		}
		reason := "stale worker after restart"
		status := subagentbatch.BatchOrphaned
		if !batch.BatchDeadline.IsZero() && !batch.BatchDeadline.After(subagentbatch.Now()) {
			reason = "batch deadline exceeded during recovery"
			status = subagentbatch.BatchTimedOut
		}
		beforeVersion := batch.Version
		if err := c.convergeBatchTerminal(ctx, &batch, status, reason); err != nil {
			return changed, err
		}
		current, err := c.store.GetBatch(ctx, batch.BatchID)
		if err != nil {
			return changed, err
		}
		if current != nil && current.Version != beforeVersion && current.Status.Terminal() {
			changed++
		}
	}
	return changed, nil
}

// runBatch is the detached worker goroutine for one batch.
func (c *SubagentBatchCoordinator) runBatch(ctx context.Context, batchID string, opts BatchStartOptions, tasks []SubagentTask) {
	defer c.forgetCancel(batchID)

	now := subagentbatch.Now()
	ownerID := c.coordinatorOwnerID()
	current, err := c.store.GetBatch(ctx, batchID)
	if err != nil {
		// Cancellation can happen before the worker gets as far as its
		// durable claim. The worker context is then already canceled, so a
		// context-aware read fails even though the batch row is now terminal.
		// Re-read detached before entering finalizeBatch; otherwise the late
		// worker would write synthetic task results without an owner and leave
		// the terminal cancellation without its summary/outbox delivery.
		detached := agentWithoutCancel(ctx)
		if durable, readErr := c.store.GetBatch(detached, batchID); readErr == nil &&
			(durable == nil || durable.Status.Terminal()) {
			return
		}
		c.finalizeBatch(ctx, batchID, opts, tasks, nil, err)
		return
	}
	if current == nil || current.Status.Terminal() {
		return
	}
	unlockWrite, writable := c.acquireBatchWrite(batchID)
	if !writable {
		return
	}
	updated, err := c.store.UpdateBatch(ctx, batchID, current.Version, func(b *subagentbatch.SubagentBatch) {
		if b.Status.Terminal() {
			return
		}
		if b.Status != subagentbatch.BatchQueued && b.Status != subagentbatch.BatchRunning {
			return
		}
		if b.Status == subagentbatch.BatchRunning && b.OwnerID != ownerID {
			return
		}
		b.Status = subagentbatch.BatchRunning
		if b.StartedAt == nil {
			b.StartedAt = &now
		}
		b.HeartbeatAt = now
		b.OwnerID = ownerID
		b.FencingToken = fmt.Sprintf("%s/%d", ownerID, now.UnixNano())
	})
	unlockWrite()
	if err != nil {
		var conflict *subagentbatch.VersionConflictError
		if errors.As(err, &conflict) {
			// A sibling coordinator won the durable claim. It owns execution and
			// will perform terminal finalization; this worker must not fabricate a
			// second result set or terminal notification.
			return
		}
		// finalizeBatch persists the terminal state and emits the single
		// terminal lifecycle event; there is no separate early failure event.
		c.finalizeBatch(ctx, batchID, opts, tasks, nil, err)
		return
	}
	if updated == nil || updated.Status.Terminal() || updated.Status != subagentbatch.BatchRunning || updated.OwnerID != ownerID {
		return
	}
	claimToken := updated.FencingToken
	stopHeartbeat := c.startHeartbeat(ctx, batchID, ownerID, claimToken)
	defer stopHeartbeat()
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

// startHeartbeat renews the durable owner lease while the scheduler is
// running. Recovery uses HeartbeatAt as its stale-worker fence, so a one-time
// claim timestamp is insufficient for batches that legitimately run longer
// than the restart grace period.
func (c *SubagentBatchCoordinator) startHeartbeat(ctx context.Context, batchID, ownerID, fencingToken string) func() {
	interval := c.heartbeatEvery
	if interval <= 0 {
		interval = time.Minute
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				heartbeatTimeout := interval
				if heartbeatTimeout > 5*time.Second {
					heartbeatTimeout = 5 * time.Second
				}
				persistCtx, cancel := context.WithTimeout(context.Background(), heartbeatTimeout)
				c.renewHeartbeat(persistCtx, batchID, ownerID, fencingToken)
				cancel()
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

func (c *SubagentBatchCoordinator) renewHeartbeat(ctx context.Context, batchID, ownerID, fencingToken string) {
	unlockWrite, writable := c.acquireBatchWrite(batchID)
	if !writable {
		return
	}
	defer unlockWrite()
	current, err := c.store.GetBatch(ctx, batchID)
	if err != nil || current == nil || current.Status.Terminal() || current.OwnerID != ownerID || current.FencingToken != fencingToken {
		return
	}
	_, _ = c.store.UpdateBatch(ctx, batchID, current.Version, func(batch *subagentbatch.SubagentBatch) {
		if batch.Status.Terminal() || batch.OwnerID != ownerID || batch.FencingToken != fencingToken {
			return
		}
		batch.HeartbeatAt = subagentbatch.Now()
	})
}

// runTasksWithProgress runs the tasks through the scheduler, mirroring task
// transitions (started) into the store along the way. Result persistence is
// deferred to finalizeBatch so the worker writes the cohort atomically and
// avoids racing per-task CAS from parallel wave goroutines.
func (c *SubagentBatchCoordinator) runTasksWithProgress(ctx context.Context, batchID string, opts BatchStartOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	c.mu.Lock()
	executor := c.executor
	c.mu.Unlock()
	if executor == nil {
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
	return executor.RunChildren(ctx, runOpts, tasks)
}

// recordTaskEvent mirrors a single task lifecycle transition into the store.
func (c *SubagentBatchCoordinator) recordTaskEvent(ctx context.Context, batchID, taskID, event string) {
	now := subagentbatch.Now()
	switch event {
	case "started":
		unlockWrite, writable := c.acquireBatchWrite(batchID)
		if !writable {
			return
		}
		_, err := c.store.UpdateTask(ctx, batchID, taskID, -1, func(t *subagentbatch.SubagentTaskRecord) {
			if t.Status.Terminal() {
				return
			}
			t.Status = subagentbatch.TaskRunning
			t.StartedAt = &now
			t.UpdatedAt = now
		})
		unlockWrite()
		if err == nil {
			c.emit("subagent.task.started", map[string]interface{}{
				"batch_id": batchID,
				"task_id":  taskID,
			})
		}
	}
}

// prepareTaskResult advances a task to a legal source state and records its
// terminal result using an exact task-version CAS. The older -1 write path was
// able to turn pending/ready rows directly into failed (an illegal transition)
// and could overwrite a result committed by a sibling owner. Keeping the
// read/promote/result sequence behind the coordinator write barrier also makes
// local lifecycle emission correspond to the durable mutation that succeeded.
func (c *SubagentBatchCoordinator) prepareTaskResult(ctx context.Context, batchID, taskID string, status subagentbatch.TaskStatus, result *subagentbatch.TaskResult) (alreadyTerminal bool, err error) {
	if c == nil || c.store == nil {
		return false, fmt.Errorf("subagent batch coordinator is not configured")
	}
	unlockWrite, writable := c.acquireBatchWrite(batchID)
	if !writable {
		return false, &subagentbatch.VersionConflictError{Kind: "batch", ID: batchID, Expected: -1}
	}
	defer unlockWrite()

	batch, err := c.store.GetBatch(ctx, batchID)
	if err != nil {
		return false, err
	}
	if batch == nil {
		return false, fmt.Errorf("subagent batch %q not found", batchID)
	}
	if batch.Status.Terminal() && batch.Status != subagentbatch.BatchCanceled {
		return false, &subagentbatch.VersionConflictError{Kind: "batch", ID: batchID, Expected: -1, Actual: batch.Version}
	}

	task, err := c.store.GetTask(ctx, batchID, taskID)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, fmt.Errorf("subagent task %q not found", taskID)
	}
	if task.Status.Terminal() {
		if task.Status == status && (batch.Status != subagentbatch.BatchCanceled || status == subagentbatch.TaskCanceled) {
			return true, nil
		}
		return false, &subagentbatch.VersionConflictError{Kind: "batch", ID: batchID, Expected: -1, Actual: batch.Version}
	}

	version := task.Version
	if status == subagentbatch.TaskSucceeded || status == subagentbatch.TaskFailed {
		switch task.Status {
		case subagentbatch.TaskPending, subagentbatch.TaskReady:
			updated, updateErr := c.store.UpdateTask(ctx, batchID, taskID, version, func(current *subagentbatch.SubagentTaskRecord) {
				if current.Status.Terminal() {
					return
				}
				now := subagentbatch.Now()
				current.Status = subagentbatch.TaskRunning
				if current.StartedAt == nil {
					current.StartedAt = &now
				}
				current.UpdatedAt = now
			})
			if updateErr != nil {
				return false, updateErr
			}
			if updated == nil {
				return false, fmt.Errorf("subagent task %q promotion returned nil", taskID)
			}
			version = updated.Version
		case subagentbatch.TaskRunning:
		default:
			return false, fmt.Errorf("subagent task %q cannot settle from %s", taskID, task.Status)
		}
	}
	if err := c.store.RecordTaskResult(ctx, batchID, taskID, version, status, result); err != nil {
		return false, err
	}
	return false, nil
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
	// The worker context may already be canceled or past its deadline by the
	// time finalization runs, so durable writes must use a detached context;
	// otherwise RecordTaskResult/UpdateBatch fail inside dead transactions and
	// the ledger never reflects the terminal task/batch states. Classification
	// (abortReason and terminalBatchStatus) still uses the original ctx.
	persistCtx := agentWithoutCancel(ctx)
	var unlockWrite func()
	var writable bool
	unlockWrite, writable = c.acquireBatchWrite(batchID)
	if !writable {
		return
	}
	unlockWrite()
	statuses := make(map[string]string, len(tasks))
	var criticalErrors []string
	resultWriteBlocked := false
	unreported := false

	for i, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			taskID = "task_" + fmt.Sprint(i)
		}
		var result subagentResultForStore
		var ts subagentbatch.TaskStatus
		result.TaskID = taskID
		result.Role = task.Role
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
			unreported = true
			switch abortReason {
			case batchAbortCanceled:
				result.Error = "batch canceled before task ran"
				ts = subagentbatch.TaskCanceled
			case batchAbortTimedOut:
				result.Error = "batch deadline exceeded before task ran"
				ts = subagentbatch.TaskTimedOut
			default:
				result.Error = "batch aborted before task ran"
				// The durable preflight below promotes pending/ready rows through
				// running before recording failed. A scheduler may already have
				// emitted started for this same zero-report slot.
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
		alreadyTerminal, prepareErr := c.prepareTaskResult(persistCtx, batchID, taskID, ts, taskResult)
		if prepareErr != nil {
			var conflict *subagentbatch.VersionConflictError
			if errors.As(prepareErr, &conflict) && conflict.Kind == "batch" {
				resultWriteBlocked = true
			} else {
				c.emit("subagent.batch.progress", map[string]interface{}{
					"batch_id":    batchID,
					"task_id":     taskID,
					"error":       prepareErr.Error(),
					"error_class": subagentbatch.CanonicalErrorClass(prepareErr),
				})
			}
			continue
		}
		if alreadyTerminal {
			continue
		}
		statuses[taskID] = string(ts)
		c.emit("subagent.task.completed", map[string]interface{}{
			"batch_id": batchID,
			"task_id":  taskID,
			"status":   string(ts),
		})
	}
	if resultWriteBlocked {
		// A durable batch fence won while task results were being flushed. Do not
		// derive a terminal summary from a partial cohort or emit any lifecycle
		// events from this stale worker.
		return
	}
	if unreported && abortReason == batchAbortNone && runErr == nil {
		runErr = errors.New("batch aborted before task ran")
	}

	unlockWrite, writable = c.acquireBatchWrite(batchID)
	if !writable {
		return
	}
	batch, err := c.store.GetBatch(persistCtx, batchID)
	if err != nil || batch == nil {
		batch = &subagentbatch.SubagentBatch{BatchID: batchID, CreatedAt: now}
	}
	records, listErr := c.store.ListTasks(persistCtx, batchID)
	unlockWrite()
	if listErr != nil {
		c.emit("subagent.batch.progress", map[string]interface{}{
			"batch_id":    batchID,
			"error":       listErr.Error(),
			"error_class": subagentbatch.CanonicalErrorClass(listErr),
		})
		return
	}
	_, _, completed, failed, canceled, timedOut := subagentbatch.Counts(records)
	if len(records) != len(tasks) {
		c.emit("subagent.batch.progress", map[string]interface{}{
			"batch_id":    batchID,
			"error":       fmt.Sprintf("durable task count %d does not match requested count %d", len(records), len(tasks)),
			"error_class": "durability",
		})
		return
	}
	for _, record := range records {
		if !record.Status.Terminal() {
			c.emit("subagent.batch.progress", map[string]interface{}{
				"batch_id":    batchID,
				"task_id":     record.TaskID,
				"error":       fmt.Sprintf("task remains non-terminal during finalization: %s", record.Status),
				"error_class": "durability",
			})
			return
		}
	}

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
	// Re-read the row after task result writes and finalize only if this worker
	// still owns the durable fencing token. A cancel/recovery/sibling worker may
	// have advanced the row while the scheduler was running; unguarded -1 CAS
	// would otherwise allow a late worker to resurrect or rewrite a terminal row.
	unlockWrite, writable = c.acquireBatchWrite(batchID)
	if !writable {
		return
	}
	latest, latestErr := c.store.GetBatch(persistCtx, batchID)
	unlockWrite()
	if latestErr != nil || latest == nil || latest.OwnerID != c.coordinatorOwnerID() || strings.TrimSpace(latest.FencingToken) == "" || (latest.Status.Terminal() && latest.Status != subagentbatch.BatchCanceled) {
		return
	}
	unlockWrite, writable = c.acquireBatchWrite(batchID)
	if !writable {
		return
	}
	persistedBatch, updateErr := c.store.UpdateBatch(persistCtx, batchID, latest.Version, func(b *subagentbatch.SubagentBatch) {
		if b.OwnerID != c.coordinatorOwnerID() || b.FencingToken != latest.FencingToken {
			return
		}
		// Cancel() durably marks a batch canceled (a terminal state) before the
		// worker finalizes, so an owner-matched canceled batch still needs its
		// final summary/counts persisted; only the status flip is skipped once the
		// batch is already terminal, avoiding a terminal->terminal re-transition.
		if !b.Status.Terminal() {
			b.Status = status
		}
		b.ResultSummary = summaryJSON
		b.ErrorClass = errorClass
		b.ErrorDetail = errorDetail
		b.UpdatedAt = now
		b.FinishedAt = &finishedAt
		b.HeartbeatAt = now
		// Keep the durable count columns consistent with the summary so
		// recovery/dashboards never see a completed batch with zero counts.
		// Every durable task was verified terminal above. TaskSkipped is a
		// terminal state too, but it has no dedicated batch counter; therefore
		// queued_count must still be zero rather than counting skipped rows as
		// queued work.
		b.QueuedCount = len(tasks) - (completed + failed + canceled + timedOut)
		b.RunningCount = 0
		b.CompletedCount = completed
		b.FailedCount = failed
		b.CanceledCount = canceled
		b.TimedOutCount = timedOut
	})
	unlockWrite()
	if updateErr != nil {
		// Another owner may have converged the row between our read and CAS
		// update. Re-read the durable terminal record and let its mailbox
		// idempotency boundary decide whether a notification is still needed;
		// never emit from the stale non-terminal snapshot.
		latestAfter, readErr := c.store.GetBatch(persistCtx, batchID)
		if readErr != nil || latestAfter == nil || !latestAfter.Status.Terminal() {
			return
		}
		persistedBatch = latestAfter
		batch = latestAfter
		runErr = nil
		status = latestAfter.Status
		summary.Status = status
		if len(latestAfter.ResultSummary) > 0 {
			var durableSummary subagentbatch.BatchSummary
			if json.Unmarshal(latestAfter.ResultSummary, &durableSummary) == nil && durableSummary.BatchID != "" {
				summary = durableSummary
			}
		}
	} else if persistedBatch == nil || !persistedBatch.Status.Terminal() {
		// A successful CAS must return the updated terminal row. Treat any
		// anomalous/partial store response as non-deliverable rather than
		// fabricating a terminal event from the pre-finalization snapshot.
		return
	} else {
		status = persistedBatch.Status
		summary.Status = status
	}

	c.emitTerminalEvent(persistCtx, persistedBatch, batchID, status, opts, summary, runErr, batch.CancelReason, batch.CancelRequestedAt)
}

// terminalBatchStatus decides the durable terminal status for the batch. Order
// matters: the worker context's own outcome wins (an expired deadline yields
// timed_out, an explicit cancel yields canceled — whichever fired on the
// context first), then run errors, then derived per-task counts (canceled,
// timed_out, failed), and finally all-succeeded (completed).
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

func (c *SubagentBatchCoordinator) emitTerminalEvent(ctx context.Context, batch *subagentbatch.SubagentBatch, batchID string, status subagentbatch.BatchStatus, opts BatchStartOptions, summary subagentbatch.BatchSummary, runErr error, cancelReason string, cancelRequestedAt *time.Time) {
	payload := terminalPayload(batch, batchID, status, opts, summary)
	if runErr != nil {
		payload["error"] = runErr.Error()
		payload["error_class"] = summary.ErrorClass
	}
	eventType := batchTerminalEventType(status)
	if status == subagentbatch.BatchCanceled {
		// The cancel metadata only lives on the batch record, not on the
		// summary; carry it here so the single terminal event keeps the reason
		// the (now removed) premature Cancel() event used to carry.
		if cancelReason != "" {
			payload["cancel_reason"] = cancelReason
		}
		if cancelRequestedAt != nil {
			payload["cancel_requested_at"] = cancelRequestedAt
		}
	}
	deliveryKey := batchTerminalDeliveryKey(opts.ParentSessionID, batchID, status)
	payload["delivery_key"] = deliveryKey
	payload["display_mirror"] = true
	payload["mirror_source"] = "subagent_batch_terminal_mailbox"
	if projectionErr := c.projectTerminalLifecycle(ctx, batch, eventType, &summary); projectionErr != nil {
		payload["supervision_projection_error"] = projectionErr.Error()
	}
	delivery := c.deliverTerminalOnce(ctx, batch, eventType, deliveryKey, payload)
	if strings.TrimSpace(delivery.DeliveryKey) != "" {
		payload["delivery_key"] = strings.TrimSpace(delivery.DeliveryKey)
	}
	payload["mailbox_delivery_status"] = string(delivery.Status)
	if delivery.Err != nil {
		payload["mailbox_delivery_error"] = delivery.Err.Error()
	}
	if delivery.AlreadyDelivered {
		return
	}
	c.emit(eventType, payload)
}

func terminalPayload(batch *subagentbatch.SubagentBatch, batchID string, status subagentbatch.BatchStatus, opts BatchStartOptions, summary subagentbatch.BatchSummary) map[string]interface{} {
	parentSessionID := opts.ParentSessionID
	parentTurnID := opts.ParentTurnID
	parentToolCallID := opts.ParentToolCallID
	traceID := opts.TraceID
	rootScopeID := opts.RootScopeID
	executionMode := opts.ExecutionMode
	if batch != nil {
		if strings.TrimSpace(parentSessionID) == "" {
			parentSessionID = batch.ParentSessionID
		}
		if strings.TrimSpace(parentTurnID) == "" {
			parentTurnID = batch.ParentTurnID
		}
		if strings.TrimSpace(parentToolCallID) == "" {
			parentToolCallID = batch.ParentToolCallID
		}
		if strings.TrimSpace(traceID) == "" {
			traceID = batch.TraceID
		}
		if strings.TrimSpace(rootScopeID) == "" {
			rootScopeID = batch.RootScopeID
		}
		if executionMode == "" {
			executionMode = batch.ExecutionMode
		}
	}
	return map[string]interface{}{
		"batch_id":            batchID,
		"parent_session_id":   parentSessionID,
		"parent_turn_id":      parentTurnID,
		"parent_tool_call_id": parentToolCallID,
		"trace_id":            traceID,
		"root_scope_id":       rootScopeID,
		"execution_mode":      string(executionMode),
		"status":              string(status),
		"task_count":          summary.TaskCount,
		"completed_count":     summary.CompletedCount,
		"failed_count":        summary.FailedCount,
		"canceled_count":      summary.CanceledCount,
		"timed_out_count":     summary.TimedOutCount,
		"elapsed_ms":          summary.ElapsedMillis,
	}
}

func terminalNotificationFromBatch(batch *subagentbatch.SubagentBatch) (string, string, map[string]interface{}) {
	if batch == nil {
		return "subagent.batch.failed", "", map[string]interface{}{}
	}
	var summary subagentbatch.BatchSummary
	if len(batch.ResultSummary) > 0 {
		_ = json.Unmarshal(batch.ResultSummary, &summary)
	}
	if summary.BatchID == "" {
		summary.BatchID = batch.BatchID
	}
	if summary.Status == "" {
		summary.Status = batch.Status
	}
	if summary.TaskCount == 0 {
		summary.TaskCount = batch.TaskCount
	}
	if summary.CompletedCount == 0 {
		summary.CompletedCount = batch.CompletedCount
	}
	if summary.FailedCount == 0 {
		summary.FailedCount = batch.FailedCount
	}
	if summary.CanceledCount == 0 {
		summary.CanceledCount = batch.CanceledCount
	}
	if summary.TimedOutCount == 0 {
		summary.TimedOutCount = batch.TimedOutCount
	}
	payload := terminalPayload(batch, batch.BatchID, batch.Status, BatchStartOptions{}, summary)
	if batch.ErrorDetail != "" {
		payload["error"] = batch.ErrorDetail
		payload["error_class"] = batch.ErrorClass
	}
	if batch.CancelReason != "" {
		payload["cancel_reason"] = batch.CancelReason
	}
	if batch.CancelRequestedAt != nil {
		payload["cancel_requested_at"] = batch.CancelRequestedAt
	}
	eventType := batchTerminalEventType(batch.Status)
	deliveryKey := batchTerminalDeliveryKey(batch.ParentSessionID, batch.BatchID, batch.Status)
	payload["delivery_key"] = deliveryKey
	payload["display_mirror"] = true
	payload["mirror_source"] = "subagent_batch_terminal_mailbox"
	return eventType, deliveryKey, payload
}

// projectTerminalLifecycle is deliberately best-effort. The batch row has
// already reached a durable terminal state before this function is called;
// replay scans invoke it again so a transient host/supervision outage is
// recoverable without changing mailbox or tool-result semantics.
func (c *SubagentBatchCoordinator) projectTerminalLifecycle(ctx context.Context, batch *subagentbatch.SubagentBatch, eventType string, summary *subagentbatch.BatchSummary) error {
	projector := c.lifecycleProjectorSnapshot()
	if projector == nil || batch == nil || !batch.Status.Terminal() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	projectionTimeout := c.deliveryTimeout
	if projectionTimeout <= 0 {
		projectionTimeout = 5 * time.Second
	}
	projectionCtx, projectionCancel := context.WithTimeout(agentWithoutCancel(ctx), projectionTimeout)
	defer projectionCancel()
	var resolved subagentbatch.BatchSummary
	if summary != nil {
		resolved = *summary
	} else if len(batch.ResultSummary) > 0 {
		_ = json.Unmarshal(batch.ResultSummary, &resolved)
	}
	if resolved.BatchID == "" {
		resolved.BatchID = batch.BatchID
	}
	if resolved.Status == "" {
		resolved.Status = batch.Status
	}
	if resolved.TaskCount == 0 {
		resolved.TaskCount = batch.TaskCount
	}
	if resolved.CompletedCount == 0 {
		resolved.CompletedCount = batch.CompletedCount
	}
	if resolved.FailedCount == 0 {
		resolved.FailedCount = batch.FailedCount
	}
	if resolved.CanceledCount == 0 {
		resolved.CanceledCount = batch.CanceledCount
	}
	if resolved.TimedOutCount == 0 {
		resolved.TimedOutCount = batch.TimedOutCount
	}
	errorDetail := batch.ErrorDetail
	if errorDetail == "" && len(resolved.CriticalErrors) > 0 {
		errorDetail = strings.Join(resolved.CriticalErrors, "; ")
	}
	return projector(projectionCtx, BatchTerminalLifecycle{
		BatchID:          batch.BatchID,
		RootScopeID:      batch.RootScopeID,
		ParentSessionID:  batch.ParentSessionID,
		ParentTurnID:     batch.ParentTurnID,
		ParentToolCallID: batch.ParentToolCallID,
		TraceID:          batch.TraceID,
		ExecutionMode:    batch.ExecutionMode,
		Status:           batch.Status,
		EventType:        eventType,
		SubjectVersion:   batch.Version,
		TaskCount:        resolved.TaskCount,
		CompletedCount:   resolved.CompletedCount,
		FailedCount:      resolved.FailedCount,
		CanceledCount:    resolved.CanceledCount,
		TimedOutCount:    resolved.TimedOutCount,
		ErrorClass:       firstNonEmptyString(batch.ErrorClass, resolved.ErrorClass),
		Error:            errorDetail,
	}.normalized())
}

func batchTerminalEventType(status subagentbatch.BatchStatus) string {
	switch status {
	case subagentbatch.BatchFailed:
		return "subagent.batch.failed"
	case subagentbatch.BatchCanceled:
		return "subagent.batch.canceled"
	case subagentbatch.BatchTimedOut:
		return "subagent.batch.timed_out"
	case subagentbatch.BatchOrphaned:
		return "subagent.batch.orphaned"
	default:
		return "subagent.batch.completed"
	}
}

func batchTerminalDeliveryKey(parentSessionID, batchID string, status subagentbatch.BatchStatus) string {
	raw := strings.Join([]string{
		strings.TrimSpace(parentSessionID),
		strings.TrimSpace(batchID),
		strings.TrimSpace(string(status)),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("subagent_batch_terminal_%x", sum[:16])
}

func (c *SubagentBatchCoordinator) deliverTerminalOnce(ctx context.Context, batch *subagentbatch.SubagentBatch, eventType, deliveryKey string, payload map[string]interface{}) BatchTerminalDelivery {
	failed := func(err error) BatchTerminalDelivery {
		return BatchTerminalDelivery{Status: BatchTerminalDeliveryFailed, DeliveryKey: deliveryKey, Err: err}
	}
	if c == nil {
		return failed(fmt.Errorf("subagent batch coordinator is not configured"))
	}
	c.terminalMu.Lock()
	defer c.terminalMu.Unlock()
	if c.terminalDelivered == nil {
		c.terminalDelivered = make(map[string]struct{})
	}
	if _, ok := c.terminalDelivered[deliveryKey]; ok {
		return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted, DeliveryKey: deliveryKey, AlreadyDelivered: true}
	}
	if c.sink == nil {
		return failed(fmt.Errorf("subagent batch terminal sink is not configured"))
	}
	if batch == nil || !batch.Status.Terminal() {
		return failed(fmt.Errorf("subagent batch terminal state was not persisted before delivery"))
	}
	deliveryCtx := ctx
	if deliveryCtx == nil {
		deliveryCtx = context.Background()
	}
	timeout := c.deliveryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deliveryCtx, cancel := context.WithTimeout(deliveryCtx, timeout)
	defer cancel()
	delivery := c.sink(deliveryCtx, BatchTerminalNotification{
		Batch:       *batch,
		EventType:   eventType,
		DeliveryKey: deliveryKey,
		Payload:     cloneBatchPayload(payload),
	})
	if strings.TrimSpace(delivery.DeliveryKey) == "" {
		delivery.DeliveryKey = deliveryKey
	}
	if delivery.Err != nil {
		delivery.Status = BatchTerminalDeliveryFailed
		return delivery
	}
	if delivery.Status != BatchTerminalDeliveryPersisted && delivery.Status != BatchTerminalDeliveryFallback {
		return failed(fmt.Errorf("subagent batch terminal sink returned invalid delivery status %q", delivery.Status))
	}
	c.terminalDelivered[deliveryKey] = struct{}{}
	return delivery
}

func cloneBatchPayload(payload map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func (c *SubagentBatchCoordinator) emit(eventType string, payload map[string]interface{}) {
	if c == nil {
		return
	}
	c.emitterMu.RLock()
	emitter := c.emitter
	c.emitterMu.RUnlock()
	if emitter != nil {
		emitter(eventType, payload)
	}
}

func (c *SubagentBatchCoordinator) forgetCancel(batchID string) {
	c.mu.Lock()
	delete(c.cancels, batchID)
	delete(c.owned, batchID)
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

func subagentTaskFromBatchSpec(record subagentbatch.SubagentTaskRecord, spec subagentbatch.TaskSpec) SubagentTask {
	taskID := strings.TrimSpace(record.TaskID)
	if taskID == "" {
		taskID = strings.TrimSpace(spec.ID)
	}
	patches := make([]FilePatch, 0, len(spec.Patches))
	for _, patch := range spec.Patches {
		patches = append(patches, FilePatch{
			Path:               patch.Path,
			Diff:               patch.Diff,
			Summary:            patch.Summary,
			ApplyStatus:        patch.ApplyStatus,
			AppliedBy:          patch.AppliedBy,
			VerificationStatus: patch.VerificationStatus,
			VerifiedBy:         patch.VerifiedBy,
			ArtifactRefs:       patch.ArtifactRefs,
		})
	}
	return SubagentTask{
		ID:                    taskID,
		Role:                  spec.Role,
		Goal:                  spec.Goal,
		Difficulty:            spec.Difficulty,
		DifficultyRationale:   spec.DifficultyRationale,
		Provider:              spec.Provider,
		Model:                 spec.Model,
		ReasoningEffort:       spec.ReasoningEffort,
		ToolsWhitelist:        append([]string(nil), spec.ToolsWhitelist...),
		DependsOn:             append([]string(nil), spec.DependsOn...),
		PatchContext:          patches,
		BudgetTokens:          spec.BudgetTokens,
		TimeoutSec:            spec.TimeoutSec,
		ReadOnly:              spec.ReadOnly,
		CompletionRequirement: spec.CompletionRequirement,
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
