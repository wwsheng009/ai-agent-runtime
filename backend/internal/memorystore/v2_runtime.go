package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

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
	_, err = db.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, priority, attempt,
    max_attempts, retry_at, last_error, input_watermark, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, job.ID, job.Kind, job.JobKey, string(job.Scope), job.ProjectKey, string(job.Status),
		job.Priority, job.Attempt, job.MaxAttempts, nullableTime(job.RetryAt),
		nullableString(job.LastError), nullableString(job.InputWatermark),
		job.CreatedAt.UTC().Format(time.RFC3339Nano), job.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return MemoryJob{}, fmt.Errorf("memorystore: ensure job: %w", err)
	}
	return s.getJobByKey(ctx, job.Kind, job.Scope, job.ProjectKey, job.JobKey)
}

func (s *Store) getJobByKey(ctx context.Context, kind string, scope Scope, projectKey, jobKey string) (MemoryJob, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryJob{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, kind, job_key, scope, project_key, status, priority, COALESCE(worker_id, ''),
       COALESCE(lease_until, ''), attempt, max_attempts, COALESCE(retry_at, ''),
       COALESCE(started_at, ''), COALESCE(finished_at, ''), COALESCE(last_error, ''),
       COALESCE(input_watermark, ''), COALESCE(last_success_watermark, ''),
       created_at, updated_at
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
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return nil, fmt.Errorf("memorystore: begin claim jobs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'pending', worker_id = NULL, lease_until = NULL, updated_at = ?
WHERE status = 'running' AND lease_until IS NOT NULL AND lease_until != '' AND lease_until <= ?
`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("memorystore: reclaim expired jobs: %w", err)
	}
	args := []interface{}{string(JobStatusPending), now.Format(time.RFC3339Nano)}
	where := []string{"status = ?", "(retry_at IS NULL OR retry_at = '' OR retry_at <= ?)"}
	if strings.TrimSpace(opts.Kind) != "" {
		where = append(where, "kind = ?")
		args = append(args, strings.TrimSpace(opts.Kind))
	}
	if strings.TrimSpace(opts.JobKey) != "" {
		where = append(where, "job_key = ?")
		args = append(args, strings.TrimSpace(opts.JobKey))
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `
SELECT id, kind, job_key, scope, project_key, status, priority, COALESCE(worker_id, ''),
       COALESCE(lease_until, ''), attempt, max_attempts, COALESCE(retry_at, ''),
       COALESCE(started_at, ''), COALESCE(finished_at, ''), COALESCE(last_error, ''),
       COALESCE(input_watermark, ''), COALESCE(last_success_watermark, ''),
       created_at, updated_at
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
		leaseUntil := now.Add(lease).UTC().Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status = 'running', worker_id = ?, lease_until = ?, attempt = attempt + 1,
    started_at = COALESCE(started_at, ?), updated_at = ?
WHERE id = ? AND status = 'pending'
`, strings.TrimSpace(opts.WorkerID), leaseUntil, now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), job.ID)
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
		job.Attempt++
		job.UpdatedAt = now
		claimed = append(claimed, job)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memorystore: commit claim jobs: %w", err)
	}
	return claimed, nil
}

func (s *Store) CompleteJob(ctx context.Context, id, watermark string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `
UPDATE memory_jobs SET status = 'succeeded', worker_id = NULL, lease_until = NULL,
    finished_at = ?, last_success_watermark = ?, updated_at = ?
WHERE id = ?
`, now.Format(time.RFC3339Nano), nullableString(watermark), now.Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) FailJob(ctx context.Context, id, errText string, retryAt time.Time) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	var attempt, maxAttempts int
	if err := db.QueryRowContext(ctx, `SELECT attempt, max_attempts FROM memory_jobs WHERE id = ?`, id).Scan(&attempt, &maxAttempts); err != nil {
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
	_, err = db.ExecContext(ctx, `
UPDATE memory_jobs SET status = ?, worker_id = NULL, lease_until = NULL,
    retry_at = ?, finished_at = ?, last_error = ?, updated_at = ?
WHERE id = ?
`, string(status), nullableTime(retryAt), finishedAt, nullableString(errText),
		now.Format(time.RFC3339Nano), id)
	return err
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
	record := Record{
		ID:               "mem_" + uuid.NewString(),
		Scope:            ScopeProject,
		ProjectKey:       projectKey,
		Kind:             kind,
		Status:           StatusCandidate,
		Text:             text,
		NormalizedText:   normalizeMemoryText(text),
		Tags:             normalizeTags(candidate.Tags),
		Confidence:       candidate.Confidence,
		Pinned:           false,
		RetentionClass:   firstNonEmpty(candidate.RetentionClass, "standard"),
		SourceKind:       "session_extract",
		SourceSessionID:  strings.TrimSpace(candidate.SessionID),
		SourceTurnID:     strings.TrimSpace(candidate.TurnID),
		SourceRefs:       []string{"session:" + strings.TrimSpace(candidate.SessionID)},
		SourceHash:       candidate.SourceHash,
		SourceRevision:   candidate.SourceRevision,
		SourceUpdatedAt:  candidate.SourceUpdatedAt,
		SourceObservedAt: now,
		SourceMessageIDs: append([]string(nil), candidate.SourceMessageIDs...),
		ContentHash:      memoryContentHash(string(ScopeProject), projectKey, kind, text),
		Revision:         1,
		CreatedAt:        now,
		UpdatedAt:        now,
		Freshness:        FreshnessFresh,
		LastVerifiedAt:   now,
		RevalidateAfter:  candidate.RevalidateAfter,
		Metadata: map[string]interface{}{
			"model":       candidate.Model,
			"token_usage": candidate.TokenUsage,
			"sensitivity": firstNonEmpty(candidate.Sensitivity, "internal"),
		},
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

// reviveCandidate treats a repeated extraction of the same content from the
// same session as a revalidation. Without this, the content-hash unique index
// would keep the old candidate stale forever after a source/hash change.
func (s *Store) reviveCandidate(ctx context.Context, db *sql.DB, record Record) (Record, bool, error) {
	var existingID, existingSession string
	var existingRevision int64
	err := db.QueryRowContext(ctx, `
SELECT id, COALESCE(source_session_id, ''), revision FROM memory_entries
WHERE project_key = ? AND content_hash = ? AND status IN ('candidate', 'active')
LIMIT 1
`, record.ProjectKey, record.ContentHash).Scan(&existingID, &existingSession, &existingRevision)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if existingSession != strings.TrimSpace(record.SourceSessionID) {
		return Record{}, false, nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET freshness_status = 'fresh', stale_reason = NULL, last_verified_at = ?,
    revalidate_after = ?, updated_at = ?, revision = revision + 1
WHERE id = ?
`, now.Format(time.RFC3339Nano), nullableTime(record.RevalidateAfter),
		now.Format(time.RFC3339Nano), existingID); err != nil {
		return Record{}, false, err
	}
	record.ID = existingID
	record.Revision = existingRevision + 1
	if err := insertSourceTx(ctx, tx, record, record.SourceSessionID); err != nil {
		return Record{}, false, err
	}
	if err := insertEventTx(ctx, tx, existingID, "revalidated", "agent", record.SourceSessionID, record.Revision, map[string]interface{}{
		"reason": "same_session_reextract",
	}); err != nil {
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
	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT e.id
FROM memory_entries e
JOIN memory_sources src ON src.memory_id = e.id
WHERE e.project_key = ? AND e.source_session_id = ? AND e.source_kind = 'session_extract'
  AND e.status IN ('candidate', 'active')
  AND (
    (src.source_hash != '' AND src.source_hash != ?)
    OR (src.source_revision IS NOT NULL AND src.source_revision != ?)
  )
`, projectKey, sessionID, strings.TrimSpace(newHash), newRevision)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = 'stale', stale_reason = 'source_changed',
    updated_at = ? WHERE id = ?
`, now.Format(time.RFC3339Nano), id); err != nil {
			return 0, err
		}
		if err := insertEventTx(ctx, tx, id, "stale_marked", "system", sessionID, 0, map[string]interface{}{
			"reason": "source_changed",
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
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
		priority, attempt, maxAttempts   int
	)
	if err := rows.Scan(&id, &kind, &key, &scope, &project, &status, &priority, &worker,
		&lease, &attempt, &maxAttempts, &retry, &started, &finished, &lastError,
		&inputWatermark, &successWatermark, &createdAt, &updatedAt); err != nil {
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
	rows, err := db.QueryContext(ctx, `
SELECT id, kind, job_key, scope, project_key, status, priority, COALESCE(worker_id, ''),
       COALESCE(lease_until, ''), attempt, max_attempts, COALESCE(retry_at, ''),
       COALESCE(started_at, ''), COALESCE(finished_at, ''), COALESCE(last_error, ''),
       COALESCE(input_watermark, ''), COALESCE(last_success_watermark, ''),
       created_at, updated_at
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
