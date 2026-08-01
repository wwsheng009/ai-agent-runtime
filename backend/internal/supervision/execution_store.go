package supervision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRunNotFound is returned when an execution run row does not exist.
var ErrRunNotFound = errors.New("execution run not found")

// ErrRunConflict is returned when a CAS update fails because the run changed.
var ErrRunConflict = errors.New("execution run version conflict")

// RunProgressEvent is the unified progress recorder input (doc 5.4). Seq is
// optional: zero lets the store assign progress_seq = MAX+1, while a caller
// supplied seq is used for out-of-order dedup.
type RunProgressEvent struct {
	RunID string
	Kind  string
	Seq   int64
}

// CompletionOutboxEntry is a pending terminal completion notification for the
// parent mailbox (doc 7.4). IdempotencyKey is
// "subagent_completion:<run_id>:<terminal_version>".
type CompletionOutboxEntry struct {
	OutboxID        string
	RunID           string
	SessionID       string
	ParentSessionID string
	RootSessionID   string
	Status          string
	IdempotencyKey  string
	PayloadJSON     string
	Attempts        int
	LastError       string
	DeliveredAt     *time.Time
	ParentMailboxSeq int64
	CreatedAt       time.Time
}

// ExecutionRunStore persists execution runs and the completion outbox.
type ExecutionRunStore interface {
	// CreateExecutionRun inserts a new run. Returns false when the run_id
	// already exists (caller may treat as idempotent success).
	CreateExecutionRun(ctx context.Context, run ExecutionRun) (bool, error)
	// UpdateExecutionRunCAS conditionally updates the run row; expectedVersion
	// must match version. Returns ErrRunConflict on mismatch.
	UpdateExecutionRunCAS(ctx context.Context, run ExecutionRun, expectedVersion int64) (bool, error)
	// GetExecutionRun loads one run; ErrRunNotFound when absent.
	GetExecutionRun(ctx context.Context, runID string) (*ExecutionRun, error)
	// ListActiveExecutionRuns lists non-terminal runs in status order.
	ListActiveExecutionRuns(ctx context.Context, limit int) ([]ExecutionRun, error)
	// ListExecutionRunsBySession lists runs for a session, newest first.
	ListExecutionRunsBySession(ctx context.Context, sessionID string, limit int) ([]ExecutionRun, error)
	// RecordExecutionProgress updates last_progress_at/progress_seq for an
	// active run. Returns false when the run is terminal or unknown.
	RecordExecutionProgress(ctx context.Context, event RunProgressEvent, now time.Time) (bool, error)
	// RequestExecutionCancel CAS-transitions an active run to
	// cancel_requested with a cancel deadline. Returns false when the run is
	// already canceling/terminal or unknown.
	RequestExecutionCancel(ctx context.Context, runID, source string, grace time.Duration, now time.Time) (bool, error)
	// MarkExecutionRunTerminal CAS-transitions an active run to a terminal
	// status. Terminal->terminal writes are idempotent (returns true).
	MarkExecutionRunTerminal(ctx context.Context, runID, status, errorCode, resultRef string, now time.Time) (bool, error)
	// EnqueueCompletionOutbox inserts an outbox entry; false when the
	// idempotency key already exists.
	EnqueueCompletionOutbox(ctx context.Context, entry CompletionOutboxEntry) (bool, error)
	// ListUndeliveredOutbox lists outbox entries not yet delivered, oldest
	// first.
	ListUndeliveredOutbox(ctx context.Context, limit int) ([]CompletionOutboxEntry, error)
	// MarkOutboxDelivered records the parent mailbox sequence.
	MarkOutboxDelivered(ctx context.Context, outboxID string, parentMailboxSeq int64, now time.Time) (bool, error)
	// MarkOutboxFailed records a delivery failure and increments attempts.
	MarkOutboxFailed(ctx context.Context, outboxID, errText string, now time.Time) (bool, error)
}

var _ ExecutionRunStore = (*SQLiteSupervisionStore)(nil)

const (
	executionRunColumns = `run_id, kind, workflow, root_session_id, parent_session_id,
		parent_run_id, session_id, agent_id, attempt, status, owner_id,
		owner_lease_until, started_at, last_heartbeat_at, last_progress_at,
		progress_seq, execution_deadline_at, progress_deadline_at,
		approval_deadline_at, cancel_requested_at, cancel_deadline_at,
		cancel_source, finished_at, max_attempts, fencing_token, result_ref,
		error_code, version, created_at, updated_at`
)

// CreateExecutionRun inserts a new run; false when run_id already exists.
func (s *SQLiteSupervisionStore) CreateExecutionRun(ctx context.Context, run ExecutionRun) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	run = run.Normalize()
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	query := `INSERT INTO supervision_execution_runs (` + executionRunColumns + `)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err = db.ExecContext(ctx, query,
		run.RunID, run.Kind, run.Workflow, run.RootSessionID, run.ParentSessionID,
		run.ParentRunID, run.SessionID, run.AgentID, run.Attempt, run.Status, run.OwnerID,
		runTimeSQL(run.OwnerLeaseUntil), formatRunTime(run.StartedAt),
		formatRunTime(run.LastHeartbeatAt), formatRunTime(run.LastProgressAt),
		run.ProgressSeq, runTimeSQL(run.ExecutionDeadlineAt),
		runTimeSQL(run.ProgressDeadlineAt), runTimeSQL(run.ApprovalDeadlineAt),
		runTimeSQL(run.CancelRequestedAt), runTimeSQL(run.CancelDeadlineAt),
		run.CancelSource, runTimeSQL(run.FinishedAt), run.MaxAttempts,
		run.FencingToken, run.ResultRef, run.ErrorCode, run.Version,
		formatRunTime(run.CreatedAt), formatRunTime(run.UpdatedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return false, nil
		}
		return false, fmt.Errorf("create execution run: %w", err)
	}
	return true, nil
}

// UpdateExecutionRunCAS conditionally updates the run row.
func (s *SQLiteSupervisionStore) UpdateExecutionRunCAS(ctx context.Context, run ExecutionRun, expectedVersion int64) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	run = run.Normalize()
	now := time.Now().UTC()
	run.UpdatedAt = now
	run.Version = expectedVersion + 1
	query := `UPDATE supervision_execution_runs SET
		kind=?, workflow=?, root_session_id=?, parent_session_id=?, parent_run_id=?,
		session_id=?, agent_id=?, attempt=?, status=?, owner_id=?,
		owner_lease_until=?, started_at=?, last_heartbeat_at=?, last_progress_at=?,
		progress_seq=?, execution_deadline_at=?, progress_deadline_at=?,
		approval_deadline_at=?, cancel_requested_at=?, cancel_deadline_at=?,
		cancel_source=?, finished_at=?, max_attempts=?, fencing_token=?,
		result_ref=?, error_code=?, version=?, updated_at=?
		WHERE run_id=? AND version=?`
	result, err := db.ExecContext(ctx, query,
		run.Kind, run.Workflow, run.RootSessionID, run.ParentSessionID, run.ParentRunID,
		run.SessionID, run.AgentID, run.Attempt, run.Status, run.OwnerID,
		runTimeSQL(run.OwnerLeaseUntil), formatRunTime(run.StartedAt),
		formatRunTime(run.LastHeartbeatAt), formatRunTime(run.LastProgressAt),
		run.ProgressSeq, runTimeSQL(run.ExecutionDeadlineAt),
		runTimeSQL(run.ProgressDeadlineAt), runTimeSQL(run.ApprovalDeadlineAt),
		runTimeSQL(run.CancelRequestedAt), runTimeSQL(run.CancelDeadlineAt),
		run.CancelSource, runTimeSQL(run.FinishedAt), run.MaxAttempts,
		run.FencingToken, run.ResultRef, run.ErrorCode, run.Version,
		formatRunTime(run.UpdatedAt), run.RunID, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("update execution run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, ErrRunConflict
	}
	return true, nil
}

// GetExecutionRun loads one run.
func (s *SQLiteSupervisionStore) GetExecutionRun(ctx context.Context, runID string) (*ExecutionRun, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, ErrRunNotFound
	}
	query := `SELECT ` + executionRunColumns + ` FROM supervision_execution_runs WHERE run_id=?`
	run, err := scanExecutionRun(db.QueryRowContext(ctx, query, runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return &run, nil
}

// ListActiveExecutionRuns lists non-terminal runs, deadline order first.
func (s *SQLiteSupervisionStore) ListActiveExecutionRuns(ctx context.Context, limit int) ([]ExecutionRun, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	placeholders := make([]string, 0, len(runTerminalStatuses))
	args := make([]interface{}, 0, len(runTerminalStatuses))
	for status := range runTerminalStatuses {
		placeholders = append(placeholders, "?")
		args = append(args, status)
	}
	query := `SELECT ` + executionRunColumns + ` FROM supervision_execution_runs
		WHERE status NOT IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY execution_deadline_at ASC, created_at ASC LIMIT ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list active execution runs: %w", err)
	}
	defer rows.Close()
	var runs []ExecutionRun
	for rows.Next() {
		run, err := scanExecutionRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ListExecutionRunsBySession lists runs for a session, newest first.
func (s *SQLiteSupervisionStore) ListExecutionRunsBySession(ctx context.Context, sessionID string, limit int) ([]ExecutionRun, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	query := `SELECT ` + executionRunColumns + ` FROM supervision_execution_runs
		WHERE session_id=? ORDER BY created_at DESC LIMIT ?`
	rows, err := db.QueryContext(ctx, query, strings.TrimSpace(sessionID), limit)
	if err != nil {
		return nil, fmt.Errorf("list execution runs by session: %w", err)
	}
	defer rows.Close()
	var runs []ExecutionRun
	for rows.Next() {
		run, err := scanExecutionRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// RecordExecutionProgress updates progress for an active run. Progress seq is
// monotonic: a caller-supplied seq below the stored one is ignored (out-of-order
// dedup, doc 5.4).
func (s *SQLiteSupervisionStore) RecordExecutionProgress(ctx context.Context, event RunProgressEvent, now time.Time) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	runID := strings.TrimSpace(event.RunID)
	if runID == "" {
		return false, fmt.Errorf("run_id is required")
	}
	if event.Seq > 0 {
		query := `UPDATE supervision_execution_runs SET
			last_progress_at=?, progress_seq=MAX(progress_seq, ?), updated_at=?
			WHERE run_id=? AND status NOT IN (` + strings.Join(repeatQuestionMarks(len(runTerminalStatuses)), ",") + `)`
		args := make([]interface{}, 0, 4+len(runTerminalStatuses))
		args = append(args, formatRunTime(now), event.Seq, formatRunTime(now), runID)
		for status := range runTerminalStatuses {
			args = append(args, status)
		}
		result, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			return false, fmt.Errorf("record execution progress: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		return affected > 0, nil
	}
	query := `UPDATE supervision_execution_runs SET
		last_progress_at=?, progress_seq=progress_seq+1, updated_at=?
		WHERE run_id=? AND status NOT IN (` + strings.Join(repeatQuestionMarks(len(runTerminalStatuses)), ",") + `)`
	args := make([]interface{}, 0, 3+len(runTerminalStatuses))
	args = append(args, formatRunTime(now), formatRunTime(now), runID)
	for status := range runTerminalStatuses {
		args = append(args, status)
	}
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("record execution progress: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// RequestExecutionCancel CAS-transitions an active run to cancel_requested.
func (s *SQLiteSupervisionStore) RequestExecutionCancel(ctx context.Context, runID, source string, grace time.Duration, now time.Time) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if grace <= 0 {
		grace = 15 * time.Second
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, fmt.Errorf("run_id is required")
	}
	cancelDeadline := now.Add(grace)
	query := `UPDATE supervision_execution_runs SET
		status=?, cancel_requested_at=?, cancel_deadline_at=?, cancel_source=?,
		updated_at=?, version=version+1
		WHERE run_id=? AND status IN (?,?,?,?,?)`
	result, err := db.ExecContext(ctx, query,
		RunStatusCancelRequested, formatRunTime(now), formatRunTime(cancelDeadline),
		strings.TrimSpace(source), formatRunTime(now), runID,
		RunStatusQueued, RunStatusRunning, RunStatusWaitingApproval, RunStatusWaitingInput, RunStatusCanceling)
	if err != nil {
		return false, fmt.Errorf("request execution cancel: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// MarkExecutionRunTerminal CAS-transitions an active run to a terminal status.
// Terminal->terminal writes are idempotent.
func (s *SQLiteSupervisionStore) MarkExecutionRunTerminal(ctx context.Context, runID, status, errorCode, resultRef string, now time.Time) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	runID = strings.TrimSpace(runID)
	status = strings.TrimSpace(status)
	if runID == "" || !RunStatusTerminal(status) {
		return false, fmt.Errorf("invalid terminal transition: run=%q status=%q", runID, status)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// Idempotent terminal->terminal: same status (or any terminal) already set.
	var existingStatus string
	err = db.QueryRowContext(ctx, `SELECT status FROM supervision_execution_runs WHERE run_id=?`, runID).Scan(&existingStatus)
	if err == nil && RunStatusTerminal(existingStatus) {
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	query := `UPDATE supervision_execution_runs SET
		status=?, finished_at=?, error_code=?, result_ref=?, updated_at=?,
		version=version+1
		WHERE run_id=? AND status NOT IN (` + strings.Join(repeatQuestionMarks(len(runTerminalStatuses)), ",") + `)`
	args := make([]interface{}, 0, 6+len(runTerminalStatuses))
	args = append(args, status, formatRunTime(now), strings.TrimSpace(errorCode), strings.TrimSpace(resultRef), formatRunTime(now), runID)
	for terminal := range runTerminalStatuses {
		args = append(args, terminal)
	}
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("mark execution run terminal: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, ErrRunConflict
	}
	return true, nil
}

// EnqueueCompletionOutbox inserts an outbox entry; false when the idempotency
// key already exists.
func (s *SQLiteSupervisionStore) EnqueueCompletionOutbox(ctx context.Context, entry CompletionOutboxEntry) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	entry.OutboxID = strings.TrimSpace(entry.OutboxID)
	entry.RunID = strings.TrimSpace(entry.RunID)
	entry.IdempotencyKey = strings.TrimSpace(entry.IdempotencyKey)
	if entry.OutboxID == "" || entry.RunID == "" || entry.IdempotencyKey == "" {
		return false, fmt.Errorf("outbox id, run id and idempotency key are required")
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	query := `INSERT INTO supervision_completion_outbox (
		outbox_id, run_id, session_id, parent_session_id, root_session_id, status,
		idempotency_key, payload_json, attempts, last_error, delivered_at,
		parent_mailbox_seq, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err = db.ExecContext(ctx, query,
		entry.OutboxID, entry.RunID, entry.SessionID, entry.ParentSessionID, entry.RootSessionID,
		entry.Status, entry.IdempotencyKey, entry.PayloadJSON, entry.Attempts, entry.LastError,
		runTimeSQL(entry.DeliveredAt), entry.ParentMailboxSeq, formatRunTime(entry.CreatedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue completion outbox: %w", err)
	}
	return true, nil
}

// ListUndeliveredOutbox lists not-yet-delivered outbox entries, oldest first.
func (s *SQLiteSupervisionStore) ListUndeliveredOutbox(ctx context.Context, limit int) ([]CompletionOutboxEntry, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT
		outbox_id, run_id, session_id, parent_session_id, root_session_id, status,
		idempotency_key, payload_json, attempts, last_error, delivered_at,
		parent_mailbox_seq, created_at
		FROM supervision_completion_outbox
		WHERE delivered_at IS NULL
		ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list undelivered outbox: %w", err)
	}
	defer rows.Close()
	var entries []CompletionOutboxEntry
	for rows.Next() {
		var entry CompletionOutboxEntry
		var deliveredAt sql.NullString
		var createdAt string
		if err := rows.Scan(&entry.OutboxID, &entry.RunID, &entry.SessionID, &entry.ParentSessionID,
			&entry.RootSessionID, &entry.Status, &entry.IdempotencyKey, &entry.PayloadJSON,
			&entry.Attempts, &entry.LastError, &deliveredAt, &entry.ParentMailboxSeq, &createdAt); err != nil {
			return nil, err
		}
		entry.DeliveredAt = parseRunTimePtr(deliveredAt.String)
		entry.CreatedAt = parseRunTime(createdAt)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// MarkOutboxDelivered records the parent mailbox sequence.
func (s *SQLiteSupervisionStore) MarkOutboxDelivered(ctx context.Context, outboxID string, parentMailboxSeq int64, now time.Time) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := db.ExecContext(ctx, `UPDATE supervision_completion_outbox
		SET delivered_at=?, parent_mailbox_seq=?, last_error=''
		WHERE outbox_id=? AND delivered_at IS NULL`, formatRunTime(now), parentMailboxSeq, strings.TrimSpace(outboxID))
	if err != nil {
		return false, fmt.Errorf("mark outbox delivered: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// MarkOutboxFailed records a delivery failure and increments attempts.
func (s *SQLiteSupervisionStore) MarkOutboxFailed(ctx context.Context, outboxID, errText string, now time.Time) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := db.ExecContext(ctx, `UPDATE supervision_completion_outbox
		SET attempts=attempts+1, last_error=?
		WHERE outbox_id=? AND delivered_at IS NULL`, strings.TrimSpace(errText), strings.TrimSpace(outboxID))
	if err != nil {
		return false, fmt.Errorf("mark outbox failed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanExecutionRun(row rowScanner) (ExecutionRun, error) {
	var run ExecutionRun
	var ownerLeaseUntil, executionDeadlineAt, progressDeadlineAt, approvalDeadlineAt sql.NullString
	var cancelRequestedAt, cancelDeadlineAt, finishedAt sql.NullString
	var startedAt, lastHeartbeatAt, lastProgressAt, createdAt, updatedAt string
	err := row.Scan(&run.RunID, &run.Kind, &run.Workflow, &run.RootSessionID, &run.ParentSessionID,
		&run.ParentRunID, &run.SessionID, &run.AgentID, &run.Attempt, &run.Status, &run.OwnerID,
		&ownerLeaseUntil, &startedAt, &lastHeartbeatAt, &lastProgressAt,
		&run.ProgressSeq, &executionDeadlineAt, &progressDeadlineAt,
		&approvalDeadlineAt, &cancelRequestedAt, &cancelDeadlineAt,
		&run.CancelSource, &finishedAt, &run.MaxAttempts, &run.FencingToken, &run.ResultRef,
		&run.ErrorCode, &run.Version, &createdAt, &updatedAt)
	if err != nil {
		return ExecutionRun{}, err
	}
	run.OwnerLeaseUntil = parseRunTimePtr(ownerLeaseUntil.String)
	run.StartedAt = parseRunTime(startedAt)
	run.LastHeartbeatAt = parseRunTime(lastHeartbeatAt)
	run.LastProgressAt = parseRunTime(lastProgressAt)
	run.ExecutionDeadlineAt = parseRunTimePtr(executionDeadlineAt.String)
	run.ProgressDeadlineAt = parseRunTimePtr(progressDeadlineAt.String)
	run.ApprovalDeadlineAt = parseRunTimePtr(approvalDeadlineAt.String)
	run.CancelRequestedAt = parseRunTimePtr(cancelRequestedAt.String)
	run.CancelDeadlineAt = parseRunTimePtr(cancelDeadlineAt.String)
	run.FinishedAt = parseRunTimePtr(finishedAt.String)
	run.CreatedAt = parseRunTime(createdAt)
	run.UpdatedAt = parseRunTime(updatedAt)
	return run, nil
}

func repeatQuestionMarks(n int) []string {
	marks := make([]string, n)
	for i := range marks {
		marks[i] = "?"
	}
	return marks
}

// isSQLiteConstraint reports whether the error is a SQLite UNIQUE/PRIMARY KEY
// constraint violation.
func isSQLiteConstraint(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") ||
		strings.Contains(text, "constraint failed") ||
		strings.Contains(text, "primary key")
}

// MarshalOutboxPayloadJSON serializes an arbitrary completion payload.
func MarshalOutboxPayloadJSON(payload interface{}) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
