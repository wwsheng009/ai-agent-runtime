package subagentbatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// sqliteBatchStore persists the subagent batch control plane in SQLite.
type sqliteBatchStore struct {
	cfg      StoreConfig
	path     string
	dsn      string
	openOnce sync.Once
	openMu   sync.RWMutex
	openErr  error
	closed   bool
	db       *sql.DB
	ownsDB   bool
}

var _ BatchStore = (*sqliteBatchStore)(nil)

var batchMigrations = []migrate.Migration{
	{
		Version: 1,
		Name:    "subagent_batches_and_tasks",
		UpSQL: `
CREATE TABLE IF NOT EXISTS subagent_batches (
	batch_id           TEXT PRIMARY KEY,
	root_scope_id      TEXT NOT NULL DEFAULT '',
	parent_session_id  TEXT NOT NULL DEFAULT '',
	parent_turn_id     TEXT NOT NULL DEFAULT '',
	parent_tool_call_id TEXT NOT NULL DEFAULT '',
	trace_id           TEXT NOT NULL DEFAULT '',
	execution_mode     TEXT NOT NULL DEFAULT 'wait',
	status             TEXT NOT NULL DEFAULT 'queued',
	idempotency_key    TEXT NOT NULL DEFAULT '',
	task_count         INTEGER NOT NULL DEFAULT 0,
	queued_count       INTEGER NOT NULL DEFAULT 0,
	running_count      INTEGER NOT NULL DEFAULT 0,
	completed_count    INTEGER NOT NULL DEFAULT 0,
	failed_count       INTEGER NOT NULL DEFAULT 0,
	canceled_count     INTEGER NOT NULL DEFAULT 0,
	timed_out_count    INTEGER NOT NULL DEFAULT 0,
	created_at         TEXT NOT NULL,
	started_at         TEXT NULL,
	updated_at         TEXT NOT NULL,
	finished_at        TEXT NULL,
	batch_deadline     TEXT NULL,
	cancel_requested_at TEXT NULL,
	cancel_reason      TEXT NOT NULL DEFAULT '',
	owner_id           TEXT NOT NULL DEFAULT '',
	fencing_token      TEXT NOT NULL DEFAULT '',
	heartbeat_at       TEXT NULL,
	result_summary     TEXT NULL,
	error_class        TEXT NOT NULL DEFAULT '',
	error_detail       TEXT NOT NULL DEFAULT '',
	version            INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_subagent_batches_parent ON subagent_batches(parent_session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_subagent_batches_status ON subagent_batches(status);
CREATE INDEX IF NOT EXISTS idx_subagent_batches_idem ON subagent_batches(idempotency_key, parent_session_id);

CREATE TABLE IF NOT EXISTS subagent_tasks (
	task_id          TEXT NOT NULL,
	batch_id         TEXT NOT NULL,
	parent_task_id   TEXT NOT NULL DEFAULT '',
	dependency_ids   TEXT NOT NULL DEFAULT '[]',
	child_session_id TEXT NOT NULL DEFAULT '',
	role             TEXT NOT NULL DEFAULT '',
	difficulty       TEXT NOT NULL DEFAULT '',
	read_only        INTEGER NOT NULL DEFAULT 0,
	status           TEXT NOT NULL DEFAULT 'pending',
	order_index      INTEGER NOT NULL DEFAULT 0,
	attempt          INTEGER NOT NULL DEFAULT 0,
	task_deadline    TEXT NULL,
	started_at       TEXT NULL,
	updated_at       TEXT NOT NULL,
	finished_at      TEXT NULL,
	last_progress_at TEXT NULL,
	spec_json        TEXT NOT NULL DEFAULT '{}',
	result_json      TEXT NULL,
	artifact_ref     TEXT NOT NULL DEFAULT '',
	error_class      TEXT NOT NULL DEFAULT '',
	error_code       TEXT NOT NULL DEFAULT '',
	version          INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (batch_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_subagent_tasks_status ON subagent_tasks(batch_id, status);
CREATE INDEX IF NOT EXISTS idx_subagent_tasks_session ON subagent_tasks(child_session_id);
`,
	},
}

// NewSQLiteBatchStore creates a SQLite-backed batch store.
func NewSQLiteBatchStore(cfg *StoreConfig) (BatchStore, error) {
	cfgCopy := StoreConfig{}
	if cfg != nil {
		cfgCopy = *cfg
	}
	dsn, path, err := resolveBatchDSN(&cfgCopy)
	if err != nil {
		return nil, err
	}
	store := &sqliteBatchStore{
		cfg:  cfgCopy,
		path: path,
		dsn:  dsn,
	}
	if path == "" || batchMemoryDSN(dsn) {
		if err := store.ensure(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Path returns the on-disk path when configured.
func (s *sqliteBatchStore) Path() string {
	return strings.TrimSpace(s.path)
}

func (s *sqliteBatchStore) Close() error {
	if s == nil {
		return nil
	}
	s.openMu.Lock()
	s.closed = true
	db := s.db
	s.db = nil
	s.openMu.Unlock()
	if db != nil && s.ownsDB {
		return db.Close()
	}
	return nil
}

func (s *sqliteBatchStore) ensure() error {
	if s == nil {
		return fmt.Errorf("subagentbatch store is not initialized")
	}
	s.openMu.RLock()
	if s.closed {
		s.openMu.RUnlock()
		return fmt.Errorf("subagentbatch store is closed")
	}
	if s.db != nil || s.openErr != nil {
		err := s.openErr
		s.openMu.RUnlock()
		return err
	}
	s.openMu.RUnlock()

	s.openOnce.Do(func() {
		if pathKey := strings.TrimSpace(s.path); pathKey != "" {
			if err := ensureBatchStoreDirectory(pathKey); err != nil {
				s.openErr = err
				return
			}
		}
		var db *sql.DB
		var err error
		if batchMemoryDSN(s.dsn) {
			db, err = sql.Open("sqlite3", s.dsn)
			if err == nil {
				db.SetMaxOpenConns(1)
				db.SetMaxIdleConns(1)
			}
		} else {
			// 共享文件库统一并发基线：WAL + busy_timeout + 单连接池 + 锁重试。
			db, err = sqliteutil.OpenFile(s.dsn, true)
		}
		if err != nil {
			s.openErr = fmt.Errorf("open subagentbatch db: %w", err)
			return
		}
		if err := db.PingContext(context.Background()); err != nil {
			s.openErr = fmt.Errorf("ping subagentbatch db: %w", err)
			_ = db.Close()
			return
		}
		if err := migrate.Apply(context.Background(), db, batchMigrations); err != nil {
			s.openErr = fmt.Errorf("migrate subagentbatch db: %w", err)
			_ = db.Close()
			return
		}
		s.openMu.Lock()
		s.db = db
		s.ownsDB = true
		s.openMu.Unlock()
	})
	s.openMu.RLock()
	err := s.openErr
	s.openMu.RUnlock()
	return err
}

func (s *sqliteBatchStore) dbc(ctx context.Context) (*sql.DB, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.openMu.RLock()
	db := s.db
	s.openMu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("subagentbatch store is not opened")
	}
	return db, nil
}

// --- Create ---

func (s *sqliteBatchStore) CreateBatch(ctx context.Context, batch *SubagentBatch, tasks []SubagentTaskRecord) (bool, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return false, err
	}
	if batch == nil {
		return false, fmt.Errorf("subagentbatch: batch is nil")
	}
	if batch.BatchID == "" {
		batch.BatchID = NewID("batch")
	}
	if batch.Status == "" {
		batch.Status = BatchQueued
	}
	if batch.ExecutionMode == "" {
		batch.ExecutionMode = ExecutionModeWait
	}
	now := Now()
	batch.CreatedAt = now
	batch.UpdatedAt = now
	batch.HeartbeatAt = now
	if batch.Version == 0 {
		batch.Version = 1
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("subagentbatch: begin create batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if key := strings.TrimSpace(batch.IdempotencyKey); key != "" {
		var existing string
		err := tx.QueryRowContext(ctx,
			`SELECT batch_id FROM subagent_batches
			 WHERE idempotency_key = ? AND parent_session_id = ? LIMIT 1`,
			key, batch.ParentSessionID,
		).Scan(&existing)
		if err == nil && existing != "" {
			// Idempotent replay: surface the existing durable batch as the
			// authoritative handle instead of a fresh never-persisted id.
			existingBatch, loadErr := scanBatchRow(tx.QueryRowContext(ctx,
				`SELECT batch_id, root_scope_id, parent_session_id, parent_turn_id,
				        parent_tool_call_id, trace_id, execution_mode, status,
				        idempotency_key, task_count, queued_count, running_count,
				        completed_count, failed_count, canceled_count, timed_out_count,
				        created_at, started_at, updated_at, finished_at, batch_deadline,
				        cancel_requested_at, cancel_reason, owner_id, fencing_token,
				        heartbeat_at, result_summary, error_class, error_detail, version
				 FROM subagent_batches WHERE batch_id = ?`, existing))
			if loadErr != nil {
				// Never hand the caller a phantom (never-inserted) batch with a
				// fresh id; a failed replay must surface so the caller can recover.
				return false, fmt.Errorf("subagentbatch: idempotent replay %q: load existing batch: %w", key, loadErr)
			}
			*batch = *existingBatch
			return false, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return false, fmt.Errorf("subagentbatch: idempotency check: %w", err)
		}
	}

	if err := insertBatchRow(ctx, tx, batch); err != nil {
		return false, err
	}
	for i := range tasks {
		task := &tasks[i]
		task.BatchID = batch.BatchID
		if task.OrderIndex == 0 {
			task.OrderIndex = i + 1
		}
		if task.Status == "" {
			if len(task.DependencyIDs) > 0 {
				task.Status = TaskPending
			} else {
				task.Status = TaskReady
			}
		}
		task.UpdatedAt = now
		if task.Version == 0 {
			task.Version = 1
		}
		if err := insertTaskRow(ctx, tx, task, now); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("subagentbatch: commit create batch: %w", err)
	}
	return true, nil
}

func insertBatchRow(ctx context.Context, tx *sql.Tx, b *SubagentBatch) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO subagent_batches (
			batch_id, root_scope_id, parent_session_id, parent_turn_id,
			parent_tool_call_id, trace_id, execution_mode, status,
			idempotency_key, task_count, queued_count, running_count,
			completed_count, failed_count, canceled_count, timed_out_count,
			created_at, started_at, updated_at, finished_at, batch_deadline,
			cancel_requested_at, cancel_reason, owner_id, fencing_token,
			heartbeat_at, result_summary, error_class, error_detail, version
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		b.BatchID, b.RootScopeID, b.ParentSessionID, b.ParentTurnID,
		b.ParentToolCallID, b.TraceID, string(b.ExecutionMode), string(b.Status),
		b.IdempotencyKey, b.TaskCount, b.QueuedCount, b.RunningCount,
		b.CompletedCount, b.FailedCount, b.CanceledCount, b.TimedOutCount,
		formatBatchTime(b.CreatedAt), formatNullableBatchTime(b.StartedAt), formatBatchTime(b.UpdatedAt),
		formatNullableBatchTime(b.FinishedAt), formatTimeOrNil(b.BatchDeadline),
		formatNullableBatchTime(b.CancelRequestedAt), b.CancelReason,
		b.OwnerID, b.FencingToken, formatBatchTime(b.HeartbeatAt),
		nil, b.ErrorClass, b.ErrorDetail, b.Version,
	)
	if err != nil {
		return fmt.Errorf("subagentbatch: insert batch: %w", err)
	}
	return nil
}

func insertTaskRow(ctx context.Context, tx *sql.Tx, t *SubagentTaskRecord, now time.Time) error {
	spec := t.Spec
	if len(spec) == 0 {
		spec = []byte(`{}`)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO subagent_tasks (
			task_id, batch_id, parent_task_id, dependency_ids, child_session_id,
			role, difficulty, read_only, status, order_index, attempt,
			task_deadline, started_at, updated_at, finished_at, last_progress_at,
			spec_json, result_json, artifact_ref, error_class, error_code, version
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		t.TaskID, t.BatchID, t.ParentTaskID, batchJSONList(t.DependencyIDs), t.ChildSessionID,
		t.Role, t.Difficulty, boolInt(t.ReadOnly), string(t.Status), t.OrderIndex, t.Attempt,
		formatTimeOrNil(t.TaskDeadline), formatNullableBatchTime(t.StartedAt),
		formatBatchTime(t.UpdatedAt), formatNullableBatchTime(t.FinishedAt),
		formatNullableBatchTime(t.LastProgressAt),
		string(spec), nil, t.ArtifactRef, t.ErrorClass, t.ErrorCode, t.Version,
	)
	if err != nil {
		return fmt.Errorf("subagentbatch: insert task: %w", err)
	}
	return nil
}

// --- Read ---

func (s *sqliteBatchStore) GetBatch(ctx context.Context, batchID string) (*SubagentBatch, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, err
	}
	batch, err := scanBatchRow(db.QueryRowContext(ctx,
		`SELECT batch_id, root_scope_id, parent_session_id, parent_turn_id,
		        parent_tool_call_id, trace_id, execution_mode, status,
		        idempotency_key, task_count, queued_count, running_count,
		        completed_count, failed_count, canceled_count, timed_out_count,
		        created_at, started_at, updated_at, finished_at, batch_deadline,
		        cancel_requested_at, cancel_reason, owner_id, fencing_token,
		        heartbeat_at, result_summary, error_class, error_detail, version
		 FROM subagent_batches WHERE batch_id = ?`, batchID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return batch, nil
}

func (s *sqliteBatchStore) ListBatches(ctx context.Context, filter BatchFilter) ([]SubagentBatch, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, err
	}
	where := make([]string, 0, 4)
	args := make([]interface{}, 0, 8)
	if strings.TrimSpace(filter.ParentSessionID) != "" {
		where = append(where, "parent_session_id = ?")
		args = append(args, filter.ParentSessionID)
	}
	if strings.TrimSpace(filter.RootScopeID) != "" {
		where = append(where, "root_scope_id = ?")
		args = append(args, filter.RootScopeID)
	}
	if len(filter.Status) > 0 {
		placeholders := make([]string, 0, len(filter.Status))
		for _, st := range filter.Status {
			placeholders = append(placeholders, "?")
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(filter.ExecutionMode) > 0 {
		placeholders := make([]string, 0, len(filter.ExecutionMode))
		for _, m := range filter.ExecutionMode {
			placeholders = append(placeholders, "?")
			args = append(args, string(m))
		}
		where = append(where, "execution_mode IN ("+strings.Join(placeholders, ",")+")")
	}
	query := `SELECT batch_id, root_scope_id, parent_session_id, parent_turn_id,
	          parent_tool_call_id, trace_id, execution_mode, status,
	          idempotency_key, task_count, queued_count, running_count,
	          completed_count, failed_count, canceled_count, timed_out_count,
	          created_at, started_at, updated_at, finished_at, batch_deadline,
	          cancel_requested_at, cancel_reason, owner_id, fencing_token,
	          heartbeat_at, result_summary, error_class, error_detail, version
	          FROM subagent_batches`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("subagentbatch: list batches: %w", err)
	}
	defer rows.Close()
	var out []SubagentBatch
	for rows.Next() {
		batch, err := scanBatchRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *batch)
	}
	return out, rows.Err()
}

func (s *sqliteBatchStore) Recoverable(ctx context.Context, limit int) ([]SubagentBatch, error) {
	return s.ListBatches(ctx, BatchFilter{
		Status: []BatchStatus{BatchQueued, BatchRunning, BatchPartiallyCompleted},
		Limit:  limit,
	})
}

// --- Update (CAS) ---

func (s *sqliteBatchStore) UpdateBatch(ctx context.Context, batchID string, expectedVersion int64, update BatchUpdate) (*SubagentBatch, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("subagentbatch: begin update batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, err := scanBatchRow(tx.QueryRowContext(ctx,
		`SELECT batch_id, root_scope_id, parent_session_id, parent_turn_id,
		        parent_tool_call_id, trace_id, execution_mode, status,
		        idempotency_key, task_count, queued_count, running_count,
		        completed_count, failed_count, canceled_count, timed_out_count,
		        created_at, started_at, updated_at, finished_at, batch_deadline,
		        cancel_requested_at, cancel_reason, owner_id, fencing_token,
		        heartbeat_at, result_summary, error_class, error_detail, version
		 FROM subagent_batches WHERE batch_id = ?`, batchID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("subagentbatch: batch %q not found", batchID)
		}
		return nil, err
	}
	if expectedVersion >= 0 && current.Version != expectedVersion {
		return nil, &VersionConflictError{
			Kind:     "batch",
			ID:       batchID,
			Expected: expectedVersion,
			Actual:   current.Version,
		}
	}
	from := current.Status
	update(current)
	if err := ValidateBatchTransition(from, current.Status); err != nil {
		return nil, err
	}
	current.UpdatedAt = Now()
	if current.Version == 0 {
		current.Version = 1
	}
	current.Version++
	if err := overwriteBatchRow(ctx, tx, batchID, expectedVersion, current); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("subagentbatch: commit update batch: %w", err)
	}
	return current, nil
}

func overwriteBatchRow(ctx context.Context, tx *sql.Tx, batchID string, expectedVersion int64, b *SubagentBatch) error {
	var res sql.Result
	var err error
	resultSummary := b.ResultSummary
	if len(resultSummary) == 0 {
		resultSummary = nil
	}
	if expectedVersion >= 0 {
		res, err = tx.ExecContext(ctx, `
			UPDATE subagent_batches SET
				root_scope_id=?, parent_session_id=?, parent_turn_id=?,
				parent_tool_call_id=?, trace_id=?, execution_mode=?, status=?,
				idempotency_key=?, task_count=?, queued_count=?, running_count=?,
				completed_count=?, failed_count=?, canceled_count=?, timed_out_count=?,
				created_at=?, started_at=?, updated_at=?, finished_at=?, batch_deadline=?,
				cancel_requested_at=?, cancel_reason=?, owner_id=?, fencing_token=?,
				heartbeat_at=?, result_summary=?, error_class=?, error_detail=?, version=version+1
			 WHERE batch_id = ? AND version = ?`,
			b.RootScopeID, b.ParentSessionID, b.ParentTurnID,
			b.ParentToolCallID, b.TraceID, string(b.ExecutionMode), string(b.Status),
			b.IdempotencyKey, b.TaskCount, b.QueuedCount, b.RunningCount,
			b.CompletedCount, b.FailedCount, b.CanceledCount, b.TimedOutCount,
			formatBatchTime(b.CreatedAt), formatNullableBatchTime(b.StartedAt), formatBatchTime(b.UpdatedAt),
			formatNullableBatchTime(b.FinishedAt), formatTimeOrNil(b.BatchDeadline),
			formatNullableBatchTime(b.CancelRequestedAt), b.CancelReason,
			b.OwnerID, b.FencingToken, formatBatchTime(b.HeartbeatAt),
			resultSummary, b.ErrorClass, b.ErrorDetail, batchID, expectedVersion,
		)
	} else {
		res, err = tx.ExecContext(ctx, `
			UPDATE subagent_batches SET
				root_scope_id=?, parent_session_id=?, parent_turn_id=?,
				parent_tool_call_id=?, trace_id=?, execution_mode=?, status=?,
				idempotency_key=?, task_count=?, queued_count=?, running_count=?,
				completed_count=?, failed_count=?, canceled_count=?, timed_out_count=?,
				created_at=?, started_at=?, updated_at=?, finished_at=?, batch_deadline=?,
				cancel_requested_at=?, cancel_reason=?, owner_id=?, fencing_token=?,
				heartbeat_at=?, result_summary=?, error_class=?, error_detail=?, version=version+1
			 WHERE batch_id = ?`,
			b.RootScopeID, b.ParentSessionID, b.ParentTurnID,
			b.ParentToolCallID, b.TraceID, string(b.ExecutionMode), string(b.Status),
			b.IdempotencyKey, b.TaskCount, b.QueuedCount, b.RunningCount,
			b.CompletedCount, b.FailedCount, b.CanceledCount, b.TimedOutCount,
			formatBatchTime(b.CreatedAt), formatNullableBatchTime(b.StartedAt), formatBatchTime(b.UpdatedAt),
			formatNullableBatchTime(b.FinishedAt), formatTimeOrNil(b.BatchDeadline),
			formatNullableBatchTime(b.CancelRequestedAt), b.CancelReason,
			b.OwnerID, b.FencingToken, formatBatchTime(b.HeartbeatAt),
			resultSummary, b.ErrorClass, b.ErrorDetail, batchID,
		)
	}
	if err != nil {
		return fmt.Errorf("subagentbatch: update batch %q: %w", batchID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("subagentbatch: rows affected %q: %w", batchID, err)
	}
	if affected == 0 {
		return &VersionConflictError{
			Kind:     "batch",
			ID:       batchID,
			Expected: expectedVersion,
		}
	}
	return nil
}

func (s *sqliteBatchStore) UpdateTask(ctx context.Context, batchID, taskID string, expectedVersion int64, update TaskUpdate) (*SubagentTaskRecord, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("subagentbatch: begin update task: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	batch, err := getBatchTx(ctx, tx, batchID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("subagentbatch: batch %q not found", batchID)
		}
		return nil, err
	}
	record, err := s.getTaskTx(ctx, tx, batchID, taskID)
	if err != nil {
		return nil, err
	}
	if expectedVersion >= 0 && record.Version != expectedVersion {
		return nil, &VersionConflictError{
			Kind:     "task",
			ID:       taskID,
			Expected: expectedVersion,
			Actual:   record.Version,
		}
	}
	from := record.Status
	update(record)
	if err := ValidateTaskTransition(from, record.Status); err != nil {
		return nil, err
	}
	if err := validateTaskWriteAfterBatchTerminal(batch, from, record.Status); err != nil {
		return nil, err
	}
	record.UpdatedAt = Now()
	if record.Version == 0 {
		record.Version = 1
	}
	record.Version++
	if err := overwriteTaskRow(ctx, tx, batchID, taskID, expectedVersion, record); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("subagentbatch: commit update task: %w", err)
	}
	return record, nil
}

func (s *sqliteBatchStore) getTaskTx(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}, batchID, taskID string) (*SubagentTaskRecord, error) {
	return scanTaskRow(q.QueryRowContext(ctx, `
		SELECT task_id, batch_id, parent_task_id, dependency_ids, child_session_id,
		       role, difficulty, read_only, status, order_index, attempt,
		       task_deadline, started_at, updated_at, finished_at, last_progress_at,
		       spec_json, result_json, artifact_ref, error_class, error_code, version
		FROM subagent_tasks WHERE batch_id = ? AND task_id = ?`, batchID, taskID))
}

func getBatchTx(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}, batchID string) (*SubagentBatch, error) {
	return scanBatchRow(q.QueryRowContext(ctx, `
		SELECT batch_id, root_scope_id, parent_session_id, parent_turn_id,
		       parent_tool_call_id, trace_id, execution_mode, status,
		       idempotency_key, task_count, queued_count, running_count,
		       completed_count, failed_count, canceled_count, timed_out_count,
		       created_at, started_at, updated_at, finished_at, batch_deadline,
		       cancel_requested_at, cancel_reason, owner_id, fencing_token,
		       heartbeat_at, result_summary, error_class, error_detail, version
		FROM subagent_batches WHERE batch_id = ?`, batchID))
}

// validateTaskWriteAfterBatchTerminal enforces the durable write fence used by
// recovery and shutdown. Once a batch is terminal, an old scheduler callback
// must not refresh task progress or replace a task result. Explicit
// cancellation is the sole exception: a task which was still in flight may
// settle as canceled (or with its eventual result) while the batch is
// canceled. Pending/ready tasks are allowed to move to canceled so the
// unowned-cancel path can close their rows.
func validateTaskWriteAfterBatchTerminal(batch *SubagentBatch, from, to TaskStatus) error {
	if batch == nil || !batch.Status.Terminal() {
		return nil
	}
	if batch.Status == BatchCanceled && !from.Terminal() && to == TaskCanceled {
		return nil
	}
	return terminalBatchWriteConflict(batch)
}

func terminalBatchWriteConflict(batch *SubagentBatch) error {
	if batch == nil {
		return nil
	}
	return &VersionConflictError{
		Kind:     "batch",
		ID:       batch.BatchID,
		Expected: -1,
		Actual:   batch.Version,
	}
}

func overwriteTaskRow(ctx context.Context, tx *sql.Tx, batchID, taskID string, expectedVersion int64, t *SubagentTaskRecord) error {
	result := t.ResultSummary
	if len(result) == 0 {
		result = nil
	}
	var res sql.Result
	var err error
	if expectedVersion >= 0 {
		res, err = tx.ExecContext(ctx, `
			UPDATE subagent_tasks SET
				parent_task_id=?, dependency_ids=?, child_session_id=?, role=?,
				difficulty=?, read_only=?, status=?, order_index=?, attempt=?,
				task_deadline=?, started_at=?, updated_at=?, finished_at=?, last_progress_at=?,
				spec_json=?, result_json=?, artifact_ref=?, error_class=?, error_code=?,
				version=version+1
			 WHERE batch_id = ? AND task_id = ? AND version = ?`,
			t.ParentTaskID, batchJSONList(t.DependencyIDs), t.ChildSessionID, t.Role,
			t.Difficulty, boolInt(t.ReadOnly), string(t.Status), t.OrderIndex, t.Attempt,
			formatTimeOrNil(t.TaskDeadline), formatNullableBatchTime(t.StartedAt),
			formatBatchTime(t.UpdatedAt), formatNullableBatchTime(t.FinishedAt),
			formatNullableBatchTime(t.LastProgressAt),
			string(t.Spec), result, t.ArtifactRef, t.ErrorClass, t.ErrorCode,
			batchID, taskID, expectedVersion,
		)
	} else {
		res, err = tx.ExecContext(ctx, `
			UPDATE subagent_tasks SET
				parent_task_id=?, dependency_ids=?, child_session_id=?, role=?,
				difficulty=?, read_only=?, status=?, order_index=?, attempt=?,
				task_deadline=?, started_at=?, updated_at=?, finished_at=?, last_progress_at=?,
				spec_json=?, result_json=?, artifact_ref=?, error_class=?, error_code=?,
				version=version+1
			 WHERE batch_id = ? AND task_id = ?`,
			t.ParentTaskID, batchJSONList(t.DependencyIDs), t.ChildSessionID, t.Role,
			t.Difficulty, boolInt(t.ReadOnly), string(t.Status), t.OrderIndex, t.Attempt,
			formatTimeOrNil(t.TaskDeadline), formatNullableBatchTime(t.StartedAt),
			formatBatchTime(t.UpdatedAt), formatNullableBatchTime(t.FinishedAt),
			formatNullableBatchTime(t.LastProgressAt),
			string(t.Spec), result, t.ArtifactRef, t.ErrorClass, t.ErrorCode,
			batchID, taskID,
		)
	}
	if err != nil {
		return fmt.Errorf("subagentbatch: update task %q: %w", taskID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("subagentbatch: rows affected task %q: %w", taskID, err)
	}
	if affected == 0 {
		return &VersionConflictError{
			Kind:     "task",
			ID:       taskID,
			Expected: expectedVersion,
		}
	}
	return nil
}

func (s *sqliteBatchStore) UpdateTasks(ctx context.Context, batchID string, updates map[string]TaskUpdate) error {
	db, err := s.dbc(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("subagentbatch: begin update tasks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := getBatchTx(ctx, tx, batchID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("subagentbatch: batch %q not found", batchID)
		}
		return err
	}
	for taskID, update := range updates {
		record, err := s.getTaskTx(ctx, tx, batchID, taskID)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("subagentbatch: task %q not found", taskID)
			}
			return err
		}
		from := record.Status
		update(record)
		if err := ValidateTaskTransition(from, record.Status); err != nil {
			return err
		}
		if err := validateTaskWriteAfterBatchTerminal(batch, from, record.Status); err != nil {
			return err
		}
		record.UpdatedAt = Now()
		record.Version++
		if err := overwriteTaskRow(ctx, tx, batchID, taskID, record.Version-1, record); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("subagentbatch: commit update tasks: %w", err)
	}
	return nil
}

func (s *sqliteBatchStore) RecordTaskResult(ctx context.Context, batchID, taskID string, expectedVersion int64, status TaskStatus, result *TaskResult) error {
	db, err := s.dbc(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("subagentbatch: begin record result: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// A worker may outlive recovery/shutdown and arrive after its batch has
	// already been converged to a terminal state. Reject late writes for every
	// terminal status except canceled: an in-flight task is still allowed to
	// settle after an explicit cancel, while orphaned/completed/failed/timed-out
	// rows must remain immutable.
	batch, err := getBatchTx(ctx, tx, batchID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("subagentbatch: batch %q not found", batchID)
		}
		return err
	}
	if batch.Status.Terminal() && batch.Status != BatchCanceled {
		return &VersionConflictError{
			Kind:     "batch",
			ID:       batchID,
			Expected: -1,
			Actual:   batch.Version,
		}
	}

	record, err := s.getTaskTx(ctx, tx, batchID, taskID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("subagentbatch: task %q not found", taskID)
		}
		return err
	}
	if expectedVersion >= 0 && record.Version != expectedVersion {
		return &VersionConflictError{Kind: "task", ID: taskID, Expected: expectedVersion, Actual: record.Version}
	}
	if status == "" {
		if result != nil && result.Success {
			status = TaskSucceeded
		} else if result != nil {
			status = TaskFailed
		}
	}
	if batch.Status == BatchCanceled && record.Status.Terminal() {
		// A cancellation can race with a worker that already settled this
		// task. Treat an identical terminal report as an idempotent no-op, but
		// never let a late report replace the durable terminal outcome.
		if status == record.Status {
			return nil
		}
		return terminalBatchWriteConflict(batch)
	}
	if err := ValidateTaskTransition(record.Status, status); err != nil {
		return err
	}
	now := Now()
	var payload []byte
	if result != nil {
		payload, err = json.Marshal(result)
		if err != nil {
			return fmt.Errorf("subagentbatch: marshal result: %w", err)
		}
	}
	record.Status = status
	record.ResultSummary = payload
	record.UpdatedAt = now
	record.FinishedAt = &now
	if record.ErrorClass == "" && result != nil && result.Error != "" {
		record.ErrorClass = CanonicalErrorClass(fmt.Errorf("%s", result.Error))
		record.ErrorCode = result.Error
	}
	if result != nil && result.ArtifactRef != "" {
		record.ArtifactRef = result.ArtifactRef
	}
	record.Version++
	if err := overwriteTaskRow(ctx, tx, batchID, taskID, expectedVersion, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("subagentbatch: commit record result: %w", err)
	}
	return nil
}

// VersionConflictError reports a CAS mismatch.
type VersionConflictError struct {
	Kind     string
	ID       string
	Expected int64
	Actual   int64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("subagentbatch: %s %q version conflict: expected %d actual %d", e.Kind, e.ID, e.Expected, e.Actual)
}

// --- DSN / helpers ---

func ensureBatchStoreDirectory(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create subagentbatch store dir: %w", err)
	}
	return nil
}

func batchMemoryDSN(dsn string) bool {
	return strings.Contains(strings.ToLower(dsn), "mode=memory")
}

func resolveBatchDSN(cfg *StoreConfig) (string, string, error) {
	if cfg == nil {
		cfg = &StoreConfig{}
	}
	if path := strings.TrimSpace(cfg.Path); path != "" {
		return batchDSNOptions(path), path, nil
	}
	if dsn := strings.TrimSpace(cfg.DSN); dsn != "" {
		if batchMemoryDSN(dsn) || strings.HasPrefix(strings.ToLower(dsn), "file:") {
			return batchDSNOptions(dsn), "", nil
		}
		return batchDSNOptions(dsn), "", nil
	}
	return batchDSNOptions(fmt.Sprintf("file:subagentbatch-%d?mode=memory&cache=shared", time.Now().UnixNano())), "", nil
}

func batchDSNOptions(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn
	}
	if !strings.HasPrefix(strings.ToLower(dsn), "file:") && !strings.Contains(dsn, "?") {
		dsn = "file:" + dsn
	}
	if !strings.Contains(strings.ToLower(dsn), "_txlock=") {
		if strings.Contains(dsn, "?") {
			dsn += "&"
		} else {
			dsn += "?"
		}
		dsn += "_txlock=immediate"
	}
	return dsn
}

func formatBatchTime(value time.Time) string {
	if value.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatNullableBatchTime(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseBatchTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func parseNullableBatchTime(raw sql.NullString) *time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	t := parseBatchTime(raw.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeFromPtr(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// formatTimeOrNil serializes a non-pointer time.Time, returning NULL when zero.
func formatTimeOrNil(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func batchJSONList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func parseBatchJSONList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

type singleRow interface {
	Scan(dest ...interface{}) error
}

func scanBatchRow(s singleRow) (*SubagentBatch, error) {
	var (
		b             SubagentBatch
		executionMode string
		status        string
		startedAt     sql.NullString
		finishedAt    sql.NullString
		batchDeadline sql.NullString
		cancelReqAt   sql.NullString
		heartbeatAt   string
		resultSummary sql.NullString
		createdAt     string
		updatedAt     string
	)
	err := s.Scan(
		&b.BatchID, &b.RootScopeID, &b.ParentSessionID, &b.ParentTurnID,
		&b.ParentToolCallID, &b.TraceID, &executionMode, &status,
		&b.IdempotencyKey, &b.TaskCount, &b.QueuedCount, &b.RunningCount,
		&b.CompletedCount, &b.FailedCount, &b.CanceledCount, &b.TimedOutCount,
		&createdAt, &startedAt, &updatedAt, &finishedAt, &batchDeadline,
		&cancelReqAt, &b.CancelReason, &b.OwnerID, &b.FencingToken,
		&heartbeatAt, &resultSummary, &b.ErrorClass, &b.ErrorDetail, &b.Version,
	)
	if err != nil {
		return nil, err
	}
	b.ExecutionMode = ExecutionMode(executionMode)
	b.Status = BatchStatus(status)
	b.CreatedAt = parseBatchTime(createdAt)
	b.UpdatedAt = parseBatchTime(updatedAt)
	b.StartedAt = parseNullableBatchTime(startedAt)
	b.FinishedAt = parseNullableBatchTime(finishedAt)
	b.BatchDeadline = timeFromPtr(parseNullableBatchTime(batchDeadline))
	b.CancelRequestedAt = parseNullableBatchTime(cancelReqAt)
	b.HeartbeatAt = parseBatchTime(heartbeatAt)
	if resultSummary.Valid && resultSummary.String != "" {
		b.ResultSummary = []byte(resultSummary.String)
	}
	return &b, nil
}

func scanTaskRow(s singleRow) (*SubagentTaskRecord, error) {
	var (
		t              SubagentTaskRecord
		depIDs         string
		startedAt      sql.NullString
		finishedAt     sql.NullString
		updatedAt      sql.NullString
		taskDeadline   sql.NullString
		lastProgressAt sql.NullString
		specJSON       string
		resultJSON     sql.NullString
	)
	err := s.Scan(
		&t.TaskID, &t.BatchID, &t.ParentTaskID, &depIDs, &t.ChildSessionID,
		&t.Role, &t.Difficulty, &t.ReadOnly, &t.Status, &t.OrderIndex, &t.Attempt,
		&taskDeadline, &startedAt, &updatedAt, &finishedAt, &lastProgressAt,
		&specJSON, &resultJSON, &t.ArtifactRef, &t.ErrorClass, &t.ErrorCode, &t.Version,
	)
	if err != nil {
		return nil, err
	}
	t.DependencyIDs = parseBatchJSONList(depIDs)
	t.UpdatedAt = parseBatchTime(updatedAt.String)
	t.Spec = []byte(specJSON)
	if resultJSON.Valid && resultJSON.String != "" {
		t.ResultSummary = []byte(resultJSON.String)
	}
	return &t, nil
}

func (s *sqliteBatchStore) GetTask(ctx context.Context, batchID, taskID string) (*SubagentTaskRecord, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, err
	}
	task, err := scanTaskRow(db.QueryRowContext(ctx, `
		SELECT task_id, batch_id, parent_task_id, dependency_ids, child_session_id,
		       role, difficulty, read_only, status, order_index, attempt,
		       task_deadline, started_at, updated_at, finished_at, last_progress_at,
		       spec_json, result_json, artifact_ref, error_class, error_code, version
		FROM subagent_tasks WHERE batch_id = ? AND task_id = ?`, batchID, taskID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return task, nil
}

func (s *sqliteBatchStore) ListTasks(ctx context.Context, batchID string) ([]SubagentTaskRecord, error) {
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT task_id, batch_id, parent_task_id, dependency_ids, child_session_id,
		       role, difficulty, read_only, status, order_index, attempt,
		       task_deadline, started_at, updated_at, finished_at, last_progress_at,
		       spec_json, result_json, artifact_ref, error_class, error_code, version
		FROM subagent_tasks WHERE batch_id = ? ORDER BY order_index ASC`, batchID)
	if err != nil {
		return nil, fmt.Errorf("subagentbatch: list tasks: %w", err)
	}
	defer rows.Close()
	var out []SubagentTaskRecord
	for rows.Next() {
		task, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *task)
	}
	return out, rows.Err()
}
