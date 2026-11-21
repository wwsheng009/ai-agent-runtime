package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// ErrJobLeaseLost means the caller no longer owns the live claim attempt for a
// running job. A lease cannot be revived after expiry or another claim.
var ErrJobLeaseLost = errors.New("memorystore: job lease is no longer owned")

// EnsureJob creates an idempotent memory job. If the unique
// (kind, scope, project_key, job_key) already exists, the existing row is
// returned so callers can skip succeeded/running work.
func (s *Store) EnsureJob(ctx context.Context, job MemoryJob) (MemoryJob, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryJob{}, err
	}
	if strings.TrimSpace(job.Kind) == "" || strings.TrimSpace(job.JobKey) == "" {
		return MemoryJob{}, fmt.Errorf("memorystore: job kind and key are required")
	}
	if job.ID == "" {
		job.ID = "job_" + uuid.NewString()
	}
	if job.Scope == "" {
		job.Scope = ScopeProject
	}
	if strings.TrimSpace(job.ProjectKey) == "" {
		job.ProjectKey = s.cfg.ProjectKey
	}
	if job.Status == "" {
		job.Status = JobStatusPending
	}
	if job.Kind == "phase1_extract/v2" && !job.RepairOnly {
		// A receipt-backed v2 job is only executable when its complete source
		// identity is present.  Keep malformed rows out of the claim path
		// rather than silently treating them as a legacy session extraction.
		hasIdentity := strings.TrimSpace(job.SessionID) != "" ||
			job.SessionGeneration != 0 || strings.TrimSpace(job.CaptureReceiptID) != "" ||
			job.SourceStartSeq != 0 || job.SourceEndSeq != 0 ||
			job.SourceRevision != 0 || strings.TrimSpace(job.SourceHash) != ""
		if hasIdentity && (strings.TrimSpace(job.SessionID) == "" ||
			strings.TrimSpace(job.CaptureReceiptID) == "" || job.SourceStartSeq <= 0 ||
			job.SessionGeneration <= 0 || job.SourceEndSeq < job.SourceStartSeq || job.SourceRevision <= 0 ||
			strings.TrimSpace(job.SourceHash) == "") {
			job.RepairOnly = true
		}
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MemoryJob{}, fmt.Errorf("memorystore: begin ensure job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, job.ProjectKey); err != nil {
		return MemoryJob{}, err
	}
	if err := checkJobPurgeFenceIdentityTx(ctx, tx, job.ProjectKey, job.Kind,
		job.JobKey, job.SessionID, job.SessionGeneration, job.CaptureReceiptID); err != nil {
		return MemoryJob{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, priority, attempt,
    max_attempts, retry_at, last_error, input_watermark, created_at, updated_at,
    session_id, session_generation, capture_receipt_id, source_start_seq,
    source_end_seq, source_revision, source_hash, available_at, deadline_at,
    heartbeat_at, deferred_until, dead_letter_at, dead_letter_reason, repair_only
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, scope, project_key, job_key) DO NOTHING
`, job.ID, job.Kind, job.JobKey, string(job.Scope), job.ProjectKey, string(job.Status),
		job.Priority, job.Attempt, job.MaxAttempts, nullableTime(job.RetryAt),
		nullableString(job.LastError), nullableString(job.InputWatermark),
		job.CreatedAt.UTC().Format(time.RFC3339Nano), job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(job.SessionID), job.SessionGeneration, strings.TrimSpace(job.CaptureReceiptID),
		job.SourceStartSeq, job.SourceEndSeq, job.SourceRevision, strings.TrimSpace(job.SourceHash),
		nullableTime(job.AvailableAt), nullableTime(job.DeadlineAt), nullableTime(job.HeartbeatAt),
		nullableTime(job.DeferredUntil), nullableTime(job.DeadLetterAt),
		nullableString(job.DeadLetterReason), boolInt(job.RepairOnly))
	if err != nil {
		return MemoryJob{}, fmt.Errorf("memorystore: ensure job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MemoryJob{}, fmt.Errorf("memorystore: commit ensure job: %w", err)
	}
	return s.getJobByKey(ctx, job.Kind, job.Scope, job.ProjectKey, job.JobKey)
}

func (s *Store) getJobByKey(ctx context.Context, kind string, scope Scope, projectKey, jobKey string) (MemoryJob, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryJob{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, kind, job_key, scope, project_key, status, priority,
       COALESCE(worker_id, ''), COALESCE(lease_until, ''), attempt, max_attempts,
       COALESCE(retry_at, ''), COALESCE(started_at, ''), COALESCE(finished_at, ''),
       COALESCE(last_error, ''), COALESCE(input_watermark, ''),
       COALESCE(last_success_watermark, ''), created_at, updated_at,
       COALESCE(session_id, ''), session_generation, COALESCE(capture_receipt_id, ''),
       source_start_seq, source_end_seq, source_revision, COALESCE(source_hash, ''),
       COALESCE(available_at, ''), COALESCE(deadline_at, ''), COALESCE(heartbeat_at, ''),
       COALESCE(deferred_until, ''), COALESCE(dead_letter_at, ''),
       COALESCE(dead_letter_reason, ''), repair_only
FROM memory_jobs WHERE kind = ? AND scope = ? AND project_key = ? AND job_key = ?
`, kind, string(scope), projectKey, jobKey)
	return scanMemoryJobRow(row)
}

// ClaimJobs atomically claims pending jobs. Expired running leases are returned
// to pending first so a crashed worker cannot strand a session forever.
func (s *Store) ClaimJobs(ctx context.Context, opts ClaimJobOptions) ([]MemoryJob, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lease := opts.Lease
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	projectKey := strings.TrimSpace(opts.ProjectKey)
	if projectKey == "" {
		projectKey = s.cfg.ProjectKey
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return nil, fmt.Errorf("memorystore: begin claim jobs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, projectKey); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'pending', worker_id = NULL, lease_until = NULL,
    deadline_at = NULL, heartbeat_at = NULL, updated_at = ?
WHERE project_key = ? AND status = 'running'
  AND lease_until IS NOT NULL AND lease_until != '' AND lease_until <= ?
`, now.Format(time.RFC3339Nano), projectKey, now.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("memorystore: reclaim expired jobs: %w", err)
	}
	args := []interface{}{string(JobStatusPending), now.Format(time.RFC3339Nano), projectKey}
	where := []string{"status = ?", "(retry_at IS NULL OR retry_at = '' OR retry_at <= ?)", "project_key = ?"}
	if strings.TrimSpace(opts.Kind) != "" {
		where = append(where, "kind = ?")
		args = append(args, strings.TrimSpace(opts.Kind))
	}
	if strings.TrimSpace(opts.JobKey) != "" {
		where = append(where, "job_key = ?")
		args = append(args, strings.TrimSpace(opts.JobKey))
	}
	if strings.TrimSpace(opts.Kind) == "phase1_extract/v2" {
		where = append(where,
			"kind = 'phase1_extract/v2'",
			"repair_only = 0",
			"(capture_receipt_id = '' OR (session_id != '' AND source_start_seq > 0 AND source_end_seq >= source_start_seq AND source_revision > 0 AND source_hash != ''))",
			"(available_at IS NULL OR available_at = '' OR available_at <= ?)",
			"(deferred_until IS NULL OR deferred_until = '' OR deferred_until <= ?)",
		)
		args = append(args, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	} else {
		// Legacy consumers must never claim a receipt-backed row.
		where = append(where, "COALESCE(capture_receipt_id, '') = ''")
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `
 SELECT id, kind, job_key, scope, project_key, status, priority,
        COALESCE(worker_id, ''), COALESCE(lease_until, ''), attempt, max_attempts,
        COALESCE(retry_at, ''), COALESCE(started_at, ''), COALESCE(finished_at, ''),
        COALESCE(last_error, ''), COALESCE(input_watermark, ''),
        COALESCE(last_success_watermark, ''), created_at, updated_at,
        COALESCE(session_id, ''), session_generation, COALESCE(capture_receipt_id, ''),
        source_start_seq, source_end_seq, source_revision, COALESCE(source_hash, ''),
        COALESCE(available_at, ''), COALESCE(deadline_at, ''), COALESCE(heartbeat_at, ''),
        COALESCE(deferred_until, ''), COALESCE(dead_letter_at, ''),
        COALESCE(dead_letter_reason, ''), repair_only
FROM memory_jobs
WHERE `+strings.Join(where, " AND ")+`
ORDER BY priority DESC, created_at ASC LIMIT ?
`, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: select claim candidates: %w", err)
	}
	var candidates []MemoryJob
	for rows.Next() {
		job, scanErr := scanMemoryJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, job)
	}
	_ = rows.Close()
	claimed := make([]MemoryJob, 0, len(candidates))
	for _, job := range candidates {
		if err := checkJobPurgeFenceByIDTx(ctx, tx, job.ID, projectKey); err != nil {
			if errors.Is(err, ErrSessionPurgeGenerationMismatch) {
				_, _ = tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'cancelled', worker_id = NULL, lease_until = NULL,
    last_error = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND status = 'pending'
`, err.Error(), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), job.ID)
				continue
			}
			return nil, err
		}
		leaseUntil := now.Add(lease).UTC().Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'running', worker_id = ?, lease_until = ?, attempt = attempt + 1,
    started_at = COALESCE(started_at, ?), heartbeat_at = ?,
    deadline_at = ?, updated_at = ?
WHERE id = ? AND status = 'pending'
`, strings.TrimSpace(opts.WorkerID), leaseUntil, now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), leaseUntil, now.Format(time.RFC3339Nano), job.ID)
		if err != nil {
			return nil, fmt.Errorf("memorystore: claim job %s: %w", job.ID, err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			continue
		}
		job.Status = JobStatusRunning
		job.WorkerID = strings.TrimSpace(opts.WorkerID)
		job.LeaseUntil = now.Add(lease).UTC()
		job.HeartbeatAt = now
		job.DeadlineAt = now.Add(lease).UTC()
		job.Attempt++
		job.UpdatedAt = now
		claimed = append(claimed, job)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memorystore: commit claim jobs: %w", err)
	}
	return claimed, nil
}

// HeartbeatJobForWorkerAttempt extends a live claim only when the exact owner
// and attempt still hold an unexpired lease. Expired or reassigned work is
// never revived by a late heartbeat.
func (s *Store) HeartbeatJobForWorkerAttempt(
	ctx context.Context,
	id, workerID string,
	attempt int,
	lease time.Duration,
) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	workerID = strings.TrimSpace(workerID)
	if id == "" || workerID == "" || attempt <= 0 || lease <= 0 {
		return fmt.Errorf("memorystore: heartbeat requires job, worker, positive attempt and lease")
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin job heartbeat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	if err := checkJobPurgeFenceByIDTx(ctx, tx, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(lease).UTC()
	nowText := now.Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET lease_until = ?, deadline_at = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? AND project_key = ? AND status = 'running'
  AND worker_id = ? AND attempt = ?
  AND lease_until IS NOT NULL AND lease_until != '' AND lease_until > ?
`, leaseUntil.Format(time.RFC3339Nano), leaseUntil.Format(time.RFC3339Nano),
		nowText, nowText, id, s.cfg.ProjectKey, workerID, attempt, nowText)
	if err != nil {
		return fmt.Errorf("memorystore: heartbeat job %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: job=%s worker=%s attempt=%d",
			ErrJobLeaseLost, id, workerID, attempt)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memorystore: commit job heartbeat: %w", err)
	}
	return nil
}

func (s *Store) CompleteJob(ctx context.Context, id, watermark string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin complete job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkJobPurgeFenceByIDTx(ctx, tx, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_jobs SET status = 'succeeded', worker_id = NULL, lease_until = NULL,
    deadline_at = NULL, heartbeat_at = NULL,
    finished_at = ?, last_success_watermark = ?, updated_at = ?
WHERE id = ?
  AND project_key = ?
`, now.Format(time.RFC3339Nano), nullableString(watermark), now.Format(time.RFC3339Nano), id, s.cfg.ProjectKey); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteJobForWorker only lets the worker that currently owns the running
// lease complete it. A stale worker that lost its lease gets a conflict.
func (s *Store) CompleteJobForWorker(ctx context.Context, id, workerID, watermark string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin complete job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	if err := checkJobPurgeFenceByIDTx(ctx, tx, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs SET status = 'succeeded', worker_id = NULL, lease_until = NULL,
    deadline_at = NULL, heartbeat_at = NULL,
    finished_at = ?, last_success_watermark = ?, updated_at = ?
WHERE id = ? AND project_key = ? AND status = 'running' AND worker_id = ?
`, now.Format(time.RFC3339Nano), nullableString(watermark), now.Format(time.RFC3339Nano),
		id, s.cfg.ProjectKey, strings.TrimSpace(workerID))
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("memorystore: job %s is no longer owned by worker %s", id, workerID)
	}
	return tx.Commit()
}

func (s *Store) FailJob(ctx context.Context, id, errText string, retryAt time.Time) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin fail job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkJobPurgeFenceByIDTx(ctx, tx, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	var attempt, maxAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempt, max_attempts FROM memory_jobs WHERE id = ? AND project_key = ?`,
		id, s.cfg.ProjectKey).Scan(&attempt, &maxAttempts); err != nil {
		return err
	}
	now := time.Now().UTC()
	status := JobStatusPending
	finishedAt := interface{}(nil)
	if attempt >= maxAttempts {
		status = JobStatusFailed
		finishedAt = now.Format(time.RFC3339Nano)
		retryAt = time.Time{}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_jobs SET status = ?, worker_id = NULL, lease_until = NULL,
    deadline_at = NULL, heartbeat_at = NULL,
    retry_at = ?, finished_at = ?, last_error = ?, updated_at = ?
WHERE id = ?
  AND project_key = ?
`, string(status), nullableTime(retryAt), finishedAt, nullableString(errText),
		now.Format(time.RFC3339Nano), id, s.cfg.ProjectKey); err != nil {
		return err
	}
	return tx.Commit()
}

// FailJobForWorker is the lease-owner-safe retry path. If the lease expired
// and another worker claimed the job, the stale worker's failure is rejected
// instead of overwriting the new owner's state.
func (s *Store) FailJobForWorker(ctx context.Context, id, workerID, errText string, retryAt time.Time) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin fail job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	if err := checkJobPurgeFenceByIDTx(ctx, tx, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = CASE WHEN attempt >= max_attempts THEN 'failed' ELSE 'pending' END,
    worker_id = NULL,
    lease_until = NULL,
    deadline_at = NULL,
    heartbeat_at = NULL,
    retry_at = CASE WHEN attempt >= max_attempts THEN NULL ELSE ? END,
    finished_at = CASE WHEN attempt >= max_attempts THEN ? ELSE NULL END,
    last_error = ?,
    updated_at = ?
WHERE id = ? AND project_key = ? AND status = 'running' AND worker_id = ?
`, nullableTime(retryAt), now.Format(time.RFC3339Nano), nullableString(errText),
		now.Format(time.RFC3339Nano), id, s.cfg.ProjectKey, strings.TrimSpace(workerID))
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("memorystore: job %s is no longer owned by worker %s", id, workerID)
	}
	return tx.Commit()
}

// FailJobForWorkerAttempt is the fenced retry path for workers that retain a
// claimed job across a lease boundary. The attempt is the claim incarnation,
// so reusing a worker ID after reclaim cannot let a stale worker clear the new
// owner's lease.
func (s *Store) FailJobForWorkerAttempt(ctx context.Context, id, workerID string, attempt int, errText string, retryAt time.Time) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	if attempt <= 0 {
		return fmt.Errorf("memorystore: job %s has invalid claim attempt %d", id, attempt)
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin fail job attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	if err := checkJobPurgeFenceByIDTx(ctx, tx, id, s.cfg.ProjectKey); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = CASE WHEN attempt >= max_attempts THEN 'failed' ELSE 'pending' END,
    worker_id = NULL,
    lease_until = NULL,
    deadline_at = NULL,
    heartbeat_at = NULL,
    retry_at = CASE WHEN attempt >= max_attempts THEN NULL ELSE ? END,
    finished_at = CASE WHEN attempt >= max_attempts THEN ? ELSE NULL END,
    last_error = ?,
    updated_at = ?
WHERE id = ? AND project_key = ? AND status = 'running' AND worker_id = ? AND attempt = ?
`, nullableTime(retryAt), now.Format(time.RFC3339Nano), nullableString(errText),
		now.Format(time.RFC3339Nano), id, s.cfg.ProjectKey, strings.TrimSpace(workerID), attempt)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("memorystore: job %s claim attempt %d is no longer owned by worker %s", id, attempt, workerID)
	}
	return tx.Commit()
}

// InsertCandidate writes one session-extracted candidate plus source/event
// provenance. It never promotes a candidate to active.
func (s *Store) InsertCandidate(ctx context.Context, candidate Candidate) (Record, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	text := strings.TrimSpace(candidate.Text)
	if text == "" {
		return Record{}, fmt.Errorf("memorystore: candidate text is required")
	}
	projectKey := strings.TrimSpace(candidate.ProjectKey)
	if projectKey == "" {
		projectKey = s.cfg.ProjectKey
	}
	kind := strings.TrimSpace(candidate.Kind)
	if kind == "" {
		return Record{}, fmt.Errorf("memorystore: candidate kind is required")
	}
	now := time.Now().UTC()
	sourceKind := firstNonEmpty(candidate.SourceKind, "session_extract")
	sourceRefs := append([]string(nil), candidate.SourceRefs...)
	if len(sourceRefs) == 0 {
		sourceRefs = []string{"session:" + strings.TrimSpace(candidate.SessionID)}
	}
	metadata := map[string]interface{}{
		"model":       candidate.Model,
		"token_usage": candidate.TokenUsage,
		"sensitivity": firstNonEmpty(candidate.Sensitivity, "internal"),
	}
	for key, value := range candidate.Metadata {
		metadata[key] = value
	}
	record := Record{
		ID:                      "mem_" + uuid.NewString(),
		Scope:                   ScopeProject,
		ProjectKey:              projectKey,
		Kind:                    kind,
		Status:                  StatusCandidate,
		Text:                    text,
		NormalizedText:          normalizeMemoryText(text),
		Tags:                    normalizeTags(candidate.Tags),
		Confidence:              candidate.Confidence,
		Sensitivity:             normalizeSensitivity(firstNonEmpty(candidate.Sensitivity, "internal")),
		Pinned:                  false,
		RetentionClass:          firstNonEmpty(candidate.RetentionClass, "standard"),
		SourceKind:              sourceKind,
		SourceSessionID:         strings.TrimSpace(candidate.SessionID),
		SourceSessionGeneration: candidate.SessionGeneration,
		SourceTurnID:            strings.TrimSpace(candidate.TurnID),
		SourceWindow:            strings.TrimSpace(candidate.SourceWindow),
		SourceRefs:              sourceRefs,
		SourceHash:              candidate.SourceHash,
		SourceRevision:          candidate.SourceRevision,
		SourceUpdatedAt:         candidate.SourceUpdatedAt,
		SourceObservedAt:        now,
		SourceMessageIDs:        append([]string(nil), candidate.SourceMessageIDs...),
		ContentHash:             memoryContentHash(string(ScopeProject), projectKey, kind, text),
		Revision:                1,
		CreatedAt:               now,
		UpdatedAt:               now,
		Freshness:               FreshnessFresh,
		LastVerifiedAt:          now,
		RevalidateAfter:         candidate.RevalidateAfter,
		Metadata:                metadata,
	}
	if err := s.insertRecordTx(ctx, db, record, "agent", candidate.SessionID); err != nil {
		if isDuplicateMemoryError(err) {
			if revived, ok, reviveErr := s.reviveCandidate(ctx, db, record); reviveErr != nil {
				return Record{}, reviveErr
			} else if ok {
				return revived, nil
			}
		}
		return Record{}, err
	}
	return s.GetRecord(ctx, record.ID)
}

// PromoteCandidate promotes a fresh candidate to active after consolidation.
// It is deliberately separate from the manual UpdateRecord path so audit
// events retain actor=consolidation instead of being mislabeled as user edits.
func (s *Store) PromoteCandidate(ctx context.Context, id string) (Record, error) {
	record, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Status != StatusCandidate {
		return Record{}, fmt.Errorf("memorystore: record %s is not a candidate", id)
	}
	if record.Freshness != FreshnessFresh || !record.DeletedAt.IsZero() {
		return Record{}, fmt.Errorf("memorystore: record %s is not fresh and cannot be promoted", id)
	}
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, record.ProjectKey); err != nil {
		return Record{}, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET status = 'active', revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = 'candidate'
`, now.Format(time.RFC3339Nano), record.ID, s.cfg.ProjectKey, record.Revision)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: candidate %s changed concurrently", record.ID)
	}
	if err := insertEventTx(ctx, tx, record.ProjectKey, record.ID, "promoted", "consolidation", record.SourceSessionID, record.Revision+1, map[string]interface{}{
		"reason": "phase2_consolidation",
	}); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(ctx, id)
}

// SupersedeRecord resolves one lower-priority conflict. The losing record is
// retained as a superseded revision and the winning record gets the
// supersedes_id chain link when it does not already point at an older claim.
func (s *Store) SupersedeRecord(ctx context.Context, id, supersededBy, reason string) (Record, error) {
	id = strings.TrimSpace(id)
	supersededBy = strings.TrimSpace(supersededBy)
	if id == "" || supersededBy == "" || id == supersededBy {
		return Record{}, fmt.Errorf("memorystore: loser and winner ids are required and must differ")
	}
	loser, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	winner, err := s.GetRecord(ctx, supersededBy)
	if err != nil {
		return Record{}, err
	}
	if loser.Status != StatusActive || winner.Status != StatusActive {
		return Record{}, fmt.Errorf("memorystore: supersede requires two active records")
	}
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, loser.ProjectKey); err != nil {
		return Record{}, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET status = 'superseded', revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status = 'active'
`, now.Format(time.RFC3339Nano), loser.ID, s.cfg.ProjectKey, loser.Revision)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: record %s changed concurrently", loser.ID)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET supersedes_id = ?
WHERE id = ? AND project_key = ?
  AND (supersedes_id IS NULL OR supersedes_id = '')
`, loser.ID, winner.ID, s.cfg.ProjectKey); err != nil {
		return Record{}, err
	}
	if err := insertEventTx(ctx, tx, loser.ProjectKey, loser.ID, "superseded", "consolidation", loser.SourceSessionID, loser.Revision+1, map[string]interface{}{
		"reason": strings.TrimSpace(reason), "superseded_by": winner.ID,
	}); err != nil {
		return Record{}, err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, loser.ID, "record_superseded"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(ctx, id)
}

// reviveCandidate treats a repeated extraction of the same content from the
// same session as a revalidation. Without this, the content-hash unique index
// would keep the old candidate stale forever after a source/hash change.
func (s *Store) reviveCandidate(ctx context.Context, db *sql.DB, record Record) (Record, bool, error) {
	var existingID, existingSession string
	var existingGeneration, existingRevision int64
	err := db.QueryRowContext(ctx, `
 SELECT id, COALESCE(source_session_id, ''), source_session_generation, revision
 FROM memory_entries
 WHERE project_key = ? AND content_hash = ? AND status IN ('candidate', 'active')
 LIMIT 1
 `, record.ProjectKey, record.ContentHash).Scan(
		&existingID, &existingSession, &existingGeneration, &existingRevision,
	)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if existingSession != strings.TrimSpace(record.SourceSessionID) ||
		existingGeneration != record.SourceSessionGeneration {
		return Record{}, false, nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, record.ProjectKey); err != nil {
		return Record{}, false, err
	}
	if err := checkRecordSessionPurgeFenceTx(ctx, tx, record); err != nil {
		return Record{}, false, err
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET freshness_status = 'fresh', stale_reason = NULL, last_verified_at = ?,
    revalidate_after = ?, updated_at = ?, revision = revision + 1,
    confidence = CASE WHEN confidence < ? THEN ? ELSE confidence END
WHERE id = ? AND revision = ?
`, now.Format(time.RFC3339Nano), nullableTime(record.RevalidateAfter),
		now.Format(time.RFC3339Nano), record.Confidence, record.Confidence, existingID, existingRevision)
	if err != nil {
		return Record{}, false, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, false, fmt.Errorf("memorystore: record %s changed concurrently", existingID)
	}
	record.ID = existingID
	record.Revision = existingRevision + 1
	if err := insertSourceTx(ctx, tx, record, record.SourceSessionID); err != nil {
		return Record{}, false, err
	}
	if err := insertEventTx(ctx, tx, record.ProjectKey, existingID, "revalidated", "agent", record.SourceSessionID, record.Revision, map[string]interface{}{
		"reason": "same_session_reextract",
	}); err != nil {
		return Record{}, false, err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, existingID, "candidate_revalidated"); err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, err
	}
	revived, err := s.GetRecord(ctx, existingID)
	return revived, true, err
}

// MarkSessionSourceChanged marks older derived records from the same session
// stale when the transcript hash/revision moved. Candidate status is retained;
// only freshness changes.
func (s *Store) MarkSessionSourceChanged(ctx context.Context, sessionID, projectKey, newHash string, newRevision int64) (int, error) {
	return s.markSourceChanged(ctx, sessionID, projectKey, nil, "", "source_changed", newHash, newRevision)
}

// MarkSessionSourceChangedForGeneration limits stale marking to one canonical
// session incarnation.
func (s *Store) MarkSessionSourceChangedForGeneration(ctx context.Context, sessionID, projectKey string, generation int64, newHash string, newRevision int64) (int, error) {
	return s.markSourceChanged(ctx, sessionID, projectKey, &generation, "", "source_changed", newHash, newRevision)
}

// MarkWindowSourceChanged is the per-window stale check. It only compares
// sources recorded for the same canonical window, so appending a new window
// never marks candidates from older windows stale.
func (s *Store) MarkWindowSourceChanged(ctx context.Context, sessionID, projectKey, sourceWindow, newHash string, newRevision int64) (int, error) {
	sourceWindow = strings.TrimSpace(sourceWindow)
	if sourceWindow == "" {
		return 0, nil
	}
	return s.markSourceChanged(ctx, sessionID, projectKey, nil, sourceWindow, "window_source_changed", newHash, newRevision)
}

// MarkWindowSourceChangedForGeneration limits stale marking to one canonical
// session incarnation and one source window.
func (s *Store) MarkWindowSourceChangedForGeneration(ctx context.Context, sessionID, projectKey string, generation int64, sourceWindow, newHash string, newRevision int64) (int, error) {
	sourceWindow = strings.TrimSpace(sourceWindow)
	if sourceWindow == "" {
		return 0, nil
	}
	return s.markSourceChanged(ctx, sessionID, projectKey, &generation, sourceWindow, "window_source_changed", newHash, newRevision)
}

// MarkSessionSourcesStale conservatively invalidates every memory record
// derived from a session, including window-scoped sources. It is used when the
// canonical transcript was rewritten (compaction) or truncated (backtrack) and
// the affected window ranges cannot be resolved precisely yet. Pending digest
// invalidation is handled separately by the runtime outbox.
func (s *Store) MarkSessionSourcesStale(ctx context.Context, sessionID, projectKey, reason string) (int, error) {
	return s.markSessionSourcesStale(ctx, sessionID, projectKey, nil, reason)
}

// MarkSessionSourcesStaleForGeneration conservatively invalidates only records
// from one canonical session incarnation.
func (s *Store) MarkSessionSourcesStaleForGeneration(ctx context.Context, sessionID, projectKey string, generation int64, reason string) (int, error) {
	return s.markSessionSourcesStale(ctx, sessionID, projectKey, &generation, reason)
}

func (s *Store) markSessionSourcesStale(ctx context.Context, sessionID, projectKey string, generation *int64, reason string) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	if strings.TrimSpace(projectKey) == "" {
		projectKey = s.cfg.ProjectKey
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "source_invalidated"
	}
	query := `
SELECT DISTINCT e.id, e.revision
FROM memory_entries e
WHERE e.project_key = ? AND e.source_session_id = ?
  AND e.source_kind IN ('session_extract', 'turn_window')
  AND e.status IN ('candidate', 'active')
  AND e.freshness_status != 'stale'
`
	args := []interface{}{projectKey, sessionID}
	if generation != nil {
		query += ` AND e.source_session_generation = ?`
		args = append(args, *generation)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	type staleRecord struct {
		id       string
		revision int64
	}
	var stale []staleRecord
	for rows.Next() {
		var item staleRecord
		if err := rows.Scan(&item.id, &item.revision); err != nil {
			_ = rows.Close()
			return 0, err
		}
		stale = append(stale, item)
	}
	_ = rows.Close()
	if len(stale) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, projectKey); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, item := range stale {
		res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = 'stale', stale_reason = ?,
    revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?
`, reason, now.Format(time.RFC3339Nano), item.id, item.revision)
		if err != nil {
			return 0, err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			continue
		}
		if err := insertEventTx(ctx, tx, s.cfg.ProjectKey, item.id, "stale_marked", "system", sessionID, item.revision+1, map[string]interface{}{
			"reason": reason,
		}); err != nil {
			return 0, err
		}
		if err := s.markSummariesStaleForRecordTx(ctx, tx, item.id, reason); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

func (s *Store) markSourceChanged(ctx context.Context, sessionID, projectKey string, generation *int64, sourceWindow, reason, newHash string, newRevision int64) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	if strings.TrimSpace(projectKey) == "" {
		projectKey = s.cfg.ProjectKey
	}
	query := `
SELECT DISTINCT e.id, e.revision
FROM memory_entries e
JOIN memory_sources src ON src.memory_id = e.id
WHERE e.project_key = ? AND e.source_session_id = ?
  AND e.source_kind IN ('session_extract', 'turn_window')
  AND e.status IN ('candidate', 'active')
  AND (
    (src.source_hash != '' AND src.source_hash != ?)
    OR (src.source_revision IS NOT NULL AND src.source_revision != ?)
  )`
	args := []interface{}{projectKey, sessionID, strings.TrimSpace(newHash), newRevision}
	if generation != nil {
		query += ` AND e.source_session_generation = ? AND src.source_session_generation = ?`
		args = append(args, *generation, *generation)
	}
	if strings.TrimSpace(sourceWindow) == "" {
		query += ` AND (src.source_window IS NULL OR src.source_window = '')`
	} else {
		query += ` AND src.source_window = ?`
		args = append(args, strings.TrimSpace(sourceWindow))
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	type staleRecord struct {
		id       string
		revision int64
	}
	var stale []staleRecord
	for rows.Next() {
		var item staleRecord
		if err := rows.Scan(&item.id, &item.revision); err != nil {
			_ = rows.Close()
			return 0, err
		}
		stale = append(stale, item)
	}
	_ = rows.Close()
	if len(stale) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, projectKey); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, item := range stale {
		res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = 'stale', stale_reason = ?,
    revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?
`, reason, now.Format(time.RFC3339Nano), item.id, item.revision)
		if err != nil {
			return 0, err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			continue
		}
		if err := insertEventTx(ctx, tx, s.cfg.ProjectKey, item.id, "stale_marked", "system", sessionID, item.revision+1, map[string]interface{}{
			"reason": reason,
		}); err != nil {
			return 0, err
		}
		if err := s.markSummariesStaleForRecordTx(ctx, tx, item.id, reason); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

type jobRows interface {
	Scan(dest ...interface{}) error
}

func scanMemoryJobRow(row *sql.Row) (MemoryJob, error) {
	return scanMemoryJob(row)
}

func scanMemoryJob(rows jobRows) (MemoryJob, error) {
	var (
		job                              MemoryJob
		id, kind, key, scope, project    sql.NullString
		status, worker, lease, retry     sql.NullString
		started, finished, lastError     sql.NullString
		inputWatermark, successWatermark sql.NullString
		createdAt, updatedAt             sql.NullString
		sessionID, receiptID, sourceHash sql.NullString
		availableAt, deadlineAt          sql.NullString
		heartbeatAt, deferredUntil       sql.NullString
		deadLetterAt, deadLetterReason   sql.NullString
		sessionGeneration                sql.NullInt64
		sourceStartSeq, sourceEndSeq     sql.NullInt64
		sourceRevision                   sql.NullInt64
		repairOnly                       sql.NullInt64
		priority, attempt, maxAttempts   int
	)
	if err := rows.Scan(&id, &kind, &key, &scope, &project, &status, &priority, &worker,
		&lease, &attempt, &maxAttempts, &retry, &started, &finished, &lastError,
		&inputWatermark, &successWatermark, &createdAt, &updatedAt,
		&sessionID, &sessionGeneration, &receiptID, &sourceStartSeq, &sourceEndSeq,
		&sourceRevision, &sourceHash, &availableAt, &deadlineAt, &heartbeatAt,
		&deferredUntil, &deadLetterAt, &deadLetterReason, &repairOnly); err != nil {
		return MemoryJob{}, err
	}
	job = MemoryJob{
		ID: id.String, Kind: kind.String, JobKey: key.String, Scope: Scope(scope.String),
		ProjectKey: project.String, Status: JobStatus(status.String), Priority: priority,
		WorkerID: worker.String, LeaseUntil: parseTime(lease.String), Attempt: attempt,
		MaxAttempts: maxAttempts, RetryAt: parseTime(retry.String),
		StartedAt: parseTime(started.String), FinishedAt: parseTime(finished.String),
		LastError: lastError.String, InputWatermark: inputWatermark.String,
		LastSuccessWatermark: successWatermark.String,
		CreatedAt:            parseTime(createdAt.String), UpdatedAt: parseTime(updatedAt.String),
		SessionID:         sessionID.String,
		SessionGeneration: sessionGeneration.Int64,
		CaptureReceiptID:  receiptID.String,
		SourceStartSeq:    sourceStartSeq.Int64,
		SourceEndSeq:      sourceEndSeq.Int64,
		SourceRevision:    sourceRevision.Int64,
		SourceHash:        sourceHash.String,
		AvailableAt:       parseTime(availableAt.String),
		DeadlineAt:        parseTime(deadlineAt.String),
		HeartbeatAt:       parseTime(heartbeatAt.String),
		DeferredUntil:     parseTime(deferredUntil.String),
		DeadLetterAt:      parseTime(deadLetterAt.String),
		DeadLetterReason:  deadLetterReason.String,
		RepairOnly:        repairOnly.Int64 != 0,
	}
	return job, nil
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]MemoryJob, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, kind, job_key, scope, project_key, status, priority,
       COALESCE(worker_id, ''), COALESCE(lease_until, ''), attempt, max_attempts,
       COALESCE(retry_at, ''), COALESCE(started_at, ''), COALESCE(finished_at, ''),
       COALESCE(last_error, ''), COALESCE(input_watermark, ''),
       COALESCE(last_success_watermark, ''), created_at, updated_at,
       COALESCE(session_id, ''), session_generation, COALESCE(capture_receipt_id, ''),
       source_start_seq, source_end_seq, source_revision, COALESCE(source_hash, ''),
       COALESCE(available_at, ''), COALESCE(deadline_at, ''), COALESCE(heartbeat_at, ''),
       COALESCE(deferred_until, ''), COALESCE(dead_letter_at, ''),
       COALESCE(dead_letter_reason, ''), repair_only
FROM memory_jobs WHERE project_key = ? ORDER BY created_at DESC LIMIT ?
`, s.cfg.ProjectKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]MemoryJob, 0, limit)
	for rows.Next() {
		job, err := scanMemoryJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// CancelStaleBaseExtractionJobs cancels pending base-session extraction jobs
// whose transcript hash no longer matches the current canonical hash. Window
// and byte-page jobs are intentionally excluded because their job keys carry
// range/page hashes rather than the current session hash.
func (s *Store) CancelStaleBaseExtractionJobs(ctx context.Context, sessionID, currentHash, reason string) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("memorystore: store is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	currentHash = strings.TrimSpace(currentHash)
	if sessionID == "" || currentHash == "" {
		return 0, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	keepKey := "session:" + sessionID + ":" + currentHash
	res, err := db.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'cancelled', worker_id = NULL, lease_until = NULL,
    retry_at = NULL, finished_at = ?, last_error = ?, updated_at = ?
WHERE project_key = ? AND kind = 'phase1_extract' AND status = 'pending'
  AND job_key GLOB 'session:' || ? || ':*'
  AND job_key NOT GLOB 'session:' || ? || ':window:*'
  AND job_key NOT GLOB 'session:' || ? || ':byte-pages-v1:*'
  AND job_key != ?
`, now, strings.TrimSpace(reason), now, s.cfg.ProjectKey,
		sessionID, sessionID, sessionID, keepKey)
	if err != nil {
		return 0, fmt.Errorf("memorystore: cancel stale base extraction jobs: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
