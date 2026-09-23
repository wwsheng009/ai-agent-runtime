package subagentbatch

import (
	"context"
	"time"
)

// StoreConfig controls persistence settings for the batch store.
type StoreConfig struct {
	// Path is the on-disk SQLite file path. Empty means in-memory.
	Path string
	// DSN overrides Path when set (advanced).
	DSN string
}

// BatchUpdate is a CAS-friendly mutation: the callback must be monotonic and
// idempotent; the store bumps Version and writes a new UpdatedAt.
type BatchUpdate func(*SubagentBatch)

// TaskUpdate is a CAS-friendly mutation on a task record.
type TaskUpdate func(*SubagentTaskRecord)

// BatchFilter selects batches for listing/recovery.
type BatchFilter struct {
	ParentSessionID string
	RootScopeID     string
	Status          []BatchStatus
	ExecutionMode   []ExecutionMode
	Limit           int
}

// BatchStore persists the durable subagent batch control plane
// (plan §4.1 control plane). Implementations must provide version/CAS and
// idempotency so late events, recovery and cancel requests cannot overwrite
// each other.
type BatchStore interface {
	// CreateBatch inserts a batch and its task records atomically. When
	// IdempotencyKey is set and an existing batch holds the same key under the
	// same parent session, the existing batch is returned with found=true and
	// nothing is inserted.
	CreateBatch(ctx context.Context, batch *SubagentBatch, tasks []SubagentTaskRecord) (created bool, err error)

	// GetBatch returns one batch by id.
	GetBatch(ctx context.Context, batchID string) (*SubagentBatch, error)

	// ListBatches returns batches matching the filter (empty filter = all,
	// newest first).
	ListBatches(ctx context.Context, filter BatchFilter) ([]SubagentBatch, error)

	// UpdateBatch applies a CAS update. expectedVersion -1 skips the CAS guard.
	UpdateBatch(ctx context.Context, batchID string, expectedVersion int64, update BatchUpdate) (*SubagentBatch, error)

	// GetTask returns one task record.
	GetTask(ctx context.Context, batchID, taskID string) (*SubagentTaskRecord, error)

	// ListTasks returns task records for a batch in order_index order.
	ListTasks(ctx context.Context, batchID string) ([]SubagentTaskRecord, error)

	// UpdateTask applies a CAS update to one task.
	UpdateTask(ctx context.Context, batchID, taskID string, expectedVersion int64, update TaskUpdate) (*SubagentTaskRecord, error)

	// UpdateTasks applies updates to many tasks; used when the whole cohort
	// changes together. Each update is keyed by task id.
	UpdateTasks(ctx context.Context, batchID string, updates map[string]TaskUpdate) error

	// RecordTaskResult persists a task result capsule and updates the task's
	// terminal status atomically.
	RecordTaskResult(ctx context.Context, batchID, taskID string, expectedVersion int64, status TaskStatus, result *TaskResult) error

	// Recoverable returns batches that are not terminal and may need resume or
	// orphan classification after process restart.
	Recoverable(ctx context.Context, limit int) ([]SubagentBatch, error)

	// IsDurable reports whether the store survives a process restart. An
	// in-memory store answers false, and the I9 durability gate (design §6.13)
	// must then degrade supervised suspension to the legacy synchronous path
	// instead of returning a batch handle whose state dies with the process.
	IsDurable() bool

	// ParkTurnSuspension upserts the §6.12 parked-turn record for one parent
	// turn. Callers must check IsDurable first: on a non-durable store the
	// record cannot outlive the process and must not be written at all.
	ParkTurnSuspension(ctx context.Context, record *TurnSuspension) error

	// GetTurnSuspension returns one parked-turn record; ok=false when absent.
	GetTurnSuspension(ctx context.Context, sessionID, turnID string) (*TurnSuspension, bool, error)

	// ClearTurnSuspension removes a parked-turn record (resume or abort).
	ClearTurnSuspension(ctx context.Context, sessionID, turnID string) error

	Close() error
}

// Clock is an injectable time source for deterministic tests.
type Clock func() time.Time

// Now is the default clock.
func Now() time.Time { return time.Now().UTC() }
