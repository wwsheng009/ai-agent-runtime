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

// ReplayDigest re-materializes an orphaned receipt-backed digest job. It never
// reads transcript text: the canonical receipt identity in the memory store is
// the only source used to reconstruct the bounded phase-1 job.
//
// A digest that already has a live/pending job is left untouched. A completed
// digest is also an idempotent no-op. Failed/dead-letter jobs remain under the
// normal job-operator retry path rather than being silently replayed here.
func (s *Store) ReplayDigest(
	ctx context.Context, digestID string, capture TurnCapture, reason string,
) error {
	_, err := s.replayDigest(ctx, digestID, capture, reason, false)
	return err
}

// ReplayDigestWithResult is the result-bearing variant used by recovery
// reporting and tests that need the safe classification.
func (s *Store) ReplayDigestWithResult(
	ctx context.Context, digestID string, capture TurnCapture, reason string,
) (DigestReplayResult, error) {
	return s.replayDigest(ctx, digestID, capture, reason, false)
}

// ReplayDigestDryRun classifies one digest without changing a digest, job,
// receipt, audit, or operator state. It is intended for recovery reports.
func (s *Store) ReplayDigestDryRun(
	ctx context.Context, digestID string, capture TurnCapture,
) (DigestReplayResult, error) {
	return s.replayDigest(ctx, digestID, capture, "", true)
}

// ListDigestReplayCandidateIDs returns a bounded, oldest-first set of nonterminal
// digests for a read-only recovery report. It includes legacy rows without a
// capture receipt so they can be explicitly classified as ineligible.
func (s *Store) ListDigestReplayCandidateIDs(ctx context.Context, limit int) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, `
SELECT id
FROM memory_turn_digests
WHERE project_key = ? AND status IN ('pending', 'processing', 'failed', 'deferred')
ORDER BY digest_seq ASC
LIMIT ?
`, s.cfg.ProjectKey, limit)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list digest replay candidates: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("memorystore: scan digest replay candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memorystore: read digest replay candidates: %w", err)
	}
	return ids, nil
}

type digestReplayRow struct {
	ID                string
	ProjectKey        string
	SessionID         string
	SessionGeneration int64
	TurnID            string
	MessageSeqStart   int64
	MessageSeqEnd     int64
	CaptureReceiptID  string
	SourceRevision    int64
	RangeHash         string
	Status            TurnDigestStatus
	ProcessJobID      string
	RollupStatus      TurnDigestRollupStatus
	BindingReceiptID  string
	BindingSessionID  string
	BindingTurnID     string
	BindingGeneration int64
}

type digestReplayJob struct {
	ID                string
	Kind              string
	JobKey            string
	Status            JobStatus
	SessionID         string
	SessionGeneration int64
	CaptureReceiptID  string
	SourceStartSeq    int64
	SourceEndSeq      int64
	SourceRevision    int64
	SourceHash        string
	LeaseUntil        string
}

func (s *Store) replayDigest(
	ctx context.Context,
	digestID string,
	capture TurnCapture,
	reason string,
	dryRun bool,
) (DigestReplayResult, error) {
	if s == nil {
		return DigestReplayResult{}, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return DigestReplayResult{}, err
	}
	digestID = strings.TrimSpace(digestID)
	if digestID == "" {
		return DigestReplayResult{}, fmt.Errorf("%w: digest id is required", ErrDigestReplayIneligible)
	}
	reason, _ = redactOperatorReason(reason)
	if strings.TrimSpace(reason) == "" {
		reason = "operator replay"
	}

	txOptions := sqliteutil.WriteTxOptions
	if dryRun {
		txOptions = &sql.TxOptions{ReadOnly: true}
	}
	tx, err := db.BeginTx(ctx, txOptions)
	if err != nil {
		return DigestReplayResult{}, fmt.Errorf("memorystore: begin digest replay: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, row, err := s.loadDigestReplayTx(ctx, tx, digestID)
	if err != nil {
		return result, err
	}
	result.DryRun = dryRun
	if err := validateMaintenanceLeaseTx(ctx, tx, row.ProjectKey); err != nil {
		result.Classification = DigestReplayBlockedMaintenance
		result.Reason = "maintenance_lease"
		if dryRun && errors.Is(err, ErrMaintenanceLeaseLost) {
			return result, nil
		}
		return result, err
	}

	if row.Status == TurnDigestDone ||
		row.Status == TurnDigestSkipped ||
		row.Status == TurnDigestInvalidated {
		result.Classification = DigestReplayAlreadyProcessed
		result.Reason = "digest_terminal"
		if dryRun {
			return result, nil
		}
		return result, nil
	}

	if row.CaptureReceiptID == "" {
		result.Classification = DigestReplayMissingReceipt
		result.Reason = "capture_receipt"
		return result, replayDigestClassificationError(result, nil, dryRun)
	}
	capture.ProjectKey = strings.TrimSpace(capture.ProjectKey)
	if capture.ProjectKey == "" {
		capture.ProjectKey = s.cfg.ProjectKey
	}
	if !turnCaptureIdentityMatches(row, capture) {
		result.Classification = DigestReplayInvalidIdentity
		result.Reason = "canonical_receipt_identity"
		return result, replayDigestClassificationError(result, nil, dryRun)
	}
	if row.SessionID == "" || row.SessionGeneration <= 0 || row.TurnID == "" ||
		row.MessageSeqStart <= 0 || row.MessageSeqEnd < row.MessageSeqStart ||
		row.SourceRevision <= 0 || row.RangeHash == "" {
		result.Classification = DigestReplayInvalidIdentity
		result.Reason = "digest_identity"
		return result, replayDigestClassificationError(result, nil, dryRun)
	}
	if row.BindingReceiptID == "" {
		result.Classification = DigestReplayMissingReceipt
		result.Reason = "capture_generation_binding"
		return result, replayDigestClassificationError(result, nil, dryRun)
	}
	if row.BindingReceiptID != row.CaptureReceiptID ||
		row.BindingSessionID != row.SessionID ||
		row.BindingTurnID != row.TurnID ||
		row.BindingGeneration != row.SessionGeneration {
		result.Classification = DigestReplayInvalidIdentity
		result.Reason = "capture_generation_binding"
		return result, replayDigestClassificationError(result, nil, dryRun)
	}
	if err := checkSessionPurgeGenerationTx(ctx, tx, row.ProjectKey,
		row.SessionID, row.SessionGeneration); err != nil {
		if errors.Is(err, ErrSessionPurgeGenerationMismatch) {
			result.Classification = DigestReplayStaleGeneration
			result.Reason = "purge_generation"
			if dryRun {
				return result, nil
			}
		}
		return result, err
	}

	processed, err := hasTurnCaptureOutputReceiptTx(ctx, tx, row, capture)
	if err != nil {
		return result, err
	}
	if processed {
		result.Classification = DigestReplayAlreadyProcessed
		result.Reason = "output_receipt"
		return result, nil
	}
	jobKey := TurnCaptureJobKey(capture)

	var job *digestReplayJob
	if row.ProcessJobID != "" {
		job, err = loadDigestReplayJobByIDTx(ctx, tx, row.ProjectKey, row.ProcessJobID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, fmt.Errorf("memorystore: read digest replay job: %w", err)
		}
	}
	if job == nil {
		job, err = loadDigestReplayJobByKeyTx(ctx, tx, row.ProjectKey, jobKey)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, fmt.Errorf("memorystore: read digest replay job key: %w", err)
		}
	}
	if job != nil {
		result.JobID = job.ID
		if !digestReplayJobIdentityMatches(*job, capture) {
			result.Classification = DigestReplayInvalidIdentity
			result.Reason = "job_identity"
			return result, replayDigestClassificationError(result, nil, dryRun)
		}
		switch job.Status {
		case JobStatusSucceeded:
			result.Classification = DigestReplayAlreadyProcessed
			result.Reason = "job_succeeded"
			if dryRun {
				return result, nil
			}
			return result, nil
		case JobStatusPending, JobStatusRunning:
			result.Classification = DigestReplayAlreadyQueued
			result.Reason = "job_present"
			if dryRun || row.ProcessJobID == job.ID {
				return result, nil
			}
			// The stable job key found a pending job, but the digest link was
			// lost. Repair only that durable identity link; do not reset the
			// job or invoke extraction.
			if err := linkDigestReplayJobTx(ctx, tx, row, job.ID); err != nil {
				return result, err
			}
			if err := tx.Commit(); err != nil {
				return result, fmt.Errorf("memorystore: commit digest replay link: %w", err)
			}
			return result, nil
		default:
			result.Classification = DigestReplayNotOrphaned
			result.Reason = "job_requires_operator_retry"
			return result, replayDigestClassificationError(result, nil, dryRun)
		}
	}

	result.Classification = DigestReplayEligible
	result.Reason = "orphaned_digest"
	if dryRun {
		return result, nil
	}

	job, err = insertDigestReplayJobTx(ctx, tx, capture, jobKey)
	if err != nil {
		return result, err
	}
	result.JobID = job.ID
	if err := linkDigestReplayJobTx(ctx, tx, row, job.ID); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_job_operator_audit(
    id, project_key, job_id, action, reason, previous_status, new_status, created_at
) VALUES (?, ?, ?, 'replay_digest', ?, 'orphaned', 'pending', ?)
`, uuid.NewString(), row.ProjectKey, job.ID, reason, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return result, fmt.Errorf("memorystore: audit digest replay: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("memorystore: commit digest replay: %w", err)
	}
	result.Classification = DigestReplayApplied
	result.Reason = "orphaned_digest_requeued"
	return result, nil
}

func (s *Store) loadDigestReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	digestID string,
) (DigestReplayResult, digestReplayRow, error) {
	var row digestReplayRow
	var scope, status, rollupStatus string
	err := tx.QueryRowContext(ctx, `
SELECT d.id, d.project_key, COALESCE(d.session_id, ''), d.session_generation,
       COALESCE(d.turn_id, ''), d.message_seq_start, d.message_seq_end,
       COALESCE(d.capture_receipt_id, ''), d.source_revision,
       COALESCE(d.range_hash, ''), d.status, COALESCE(d.process_job_id, ''),
       COALESCE(d.rollup_status, 'pending'),
       COALESCE(g.capture_receipt_id, ''), COALESCE(g.session_id, ''),
       COALESCE(g.turn_id, ''), COALESCE(g.generation, 0), COALESCE(d.scope, '')
FROM memory_turn_digests d
LEFT JOIN memory_turn_capture_generations g
  ON g.project_key = d.project_key AND g.capture_receipt_id = d.capture_receipt_id
WHERE d.project_key = ? AND d.id = ?
`, s.cfg.ProjectKey, digestID).Scan(
		&row.ID, &row.ProjectKey, &row.SessionID, &row.SessionGeneration,
		&row.TurnID, &row.MessageSeqStart, &row.MessageSeqEnd,
		&row.CaptureReceiptID, &row.SourceRevision, &row.RangeHash,
		&status, &row.ProcessJobID, &rollupStatus,
		&row.BindingReceiptID, &row.BindingSessionID, &row.BindingTurnID,
		&row.BindingGeneration, &scope,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DigestReplayResult{}, digestReplayRow{},
			fmt.Errorf("%w: %s", ErrDigestNotFound, digestID)
	}
	if err != nil {
		return DigestReplayResult{}, digestReplayRow{},
			fmt.Errorf("memorystore: read digest for replay: %w", err)
	}
	row.Status = TurnDigestStatus(status)
	row.RollupStatus = TurnDigestRollupStatus(rollupStatus)
	result := DigestReplayResult{
		DigestID:          row.ID,
		SessionID:         row.SessionID,
		SessionGeneration: row.SessionGeneration,
		CaptureReceiptID:  row.CaptureReceiptID,
	}
	return result, row, nil
}

func turnCaptureIdentityMatches(row digestReplayRow, capture TurnCapture) bool {
	return capture.ID != "" &&
		capture.ID == row.CaptureReceiptID &&
		capture.ProjectKey == row.ProjectKey &&
		capture.SessionID == row.SessionID &&
		capture.SessionGeneration == row.SessionGeneration &&
		capture.TurnID == row.TurnID &&
		capture.MessageSeqStart == row.MessageSeqStart &&
		capture.MessageSeqEnd == row.MessageSeqEnd &&
		capture.CanonicalRevision == row.SourceRevision &&
		capture.RangeHash == row.RangeHash &&
		capture.MessageSeqStart > 0 &&
		capture.MessageSeqEnd >= capture.MessageSeqStart &&
		capture.SessionGeneration > 0 &&
		capture.CanonicalRevision > 0 &&
		strings.TrimSpace(capture.RangeHash) != ""
}

func hasTurnCaptureOutputReceiptTx(
	ctx context.Context,
	tx *sql.Tx,
	row digestReplayRow,
	capture TurnCapture,
) (bool, error) {
	var sessionID, turnID, receiptID, digestID, jobID, rangeHash string
	var revision int64
	err := tx.QueryRowContext(ctx, `
SELECT session_id, turn_id, capture_receipt_id, digest_id, job_id,
       source_revision, range_hash
FROM memory_turn_output_receipts
WHERE project_key = ? AND capture_receipt_id = ?
`, row.ProjectKey, row.CaptureReceiptID).Scan(
		&sessionID, &turnID, &receiptID, &digestID, &jobID, &revision, &rangeHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memorystore: read turn output receipt for replay: %w", err)
	}
	if sessionID != capture.SessionID || turnID != capture.TurnID ||
		receiptID != capture.ID || revision != capture.CanonicalRevision ||
		rangeHash != capture.RangeHash || digestID != row.ID || jobID == "" {
		return false, fmt.Errorf("%w: output receipt identity", ErrDigestReplayIneligible)
	}
	return true, nil
}

func loadDigestReplayJobByIDTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey, jobID string,
) (*digestReplayJob, error) {
	return scanDigestReplayJob(tx.QueryRowContext(ctx, `
SELECT id, kind, job_key, status, COALESCE(session_id, ''), session_generation,
       COALESCE(capture_receipt_id, ''), source_start_seq, source_end_seq,
       source_revision, COALESCE(source_hash, ''), COALESCE(lease_until, '')
FROM memory_jobs
WHERE project_key = ? AND id = ?
`, projectKey, jobID))
}

func loadDigestReplayJobByKeyTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey, jobKey string,
) (*digestReplayJob, error) {
	return scanDigestReplayJob(tx.QueryRowContext(ctx, `
SELECT id, kind, job_key, status, COALESCE(session_id, ''), session_generation,
       COALESCE(capture_receipt_id, ''), source_start_seq, source_end_seq,
       source_revision, COALESCE(source_hash, ''), COALESCE(lease_until, '')
FROM memory_jobs
WHERE project_key = ? AND kind = 'phase1_extract/v2' AND scope = 'project' AND job_key = ?
`, projectKey, jobKey))
}

func scanDigestReplayJob(scanner interface{ Scan(...interface{}) error }) (*digestReplayJob, error) {
	var job digestReplayJob
	var status string
	if err := scanner.Scan(
		&job.ID, &job.Kind, &job.JobKey, &status, &job.SessionID,
		&job.SessionGeneration, &job.CaptureReceiptID, &job.SourceStartSeq,
		&job.SourceEndSeq, &job.SourceRevision, &job.SourceHash, &job.LeaseUntil,
	); err != nil {
		return nil, err
	}
	job.Status = JobStatus(status)
	return &job, nil
}

func digestReplayJobIdentityMatches(job digestReplayJob, capture TurnCapture) bool {
	return job.Kind == "phase1_extract/v2" &&
		job.JobKey == TurnCaptureJobKey(capture) &&
		job.SessionID == capture.SessionID &&
		job.SessionGeneration == capture.SessionGeneration &&
		job.CaptureReceiptID == capture.ID &&
		job.SourceStartSeq == capture.MessageSeqStart &&
		job.SourceEndSeq == capture.MessageSeqEnd &&
		job.SourceRevision == capture.CanonicalRevision &&
		job.SourceHash == capture.RangeHash
}

func insertDigestReplayJobTx(
	ctx context.Context,
	tx *sql.Tx,
	capture TurnCapture,
	jobKey string,
) (*digestReplayJob, error) {
	now := time.Now().UTC()
	jobID := "job_" + uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, priority, attempt,
    max_attempts, retry_at, last_error, input_watermark, created_at, updated_at,
    session_id, session_generation, capture_receipt_id, source_start_seq,
    source_end_seq, source_revision, source_hash, available_at, deadline_at,
    heartbeat_at, deferred_until, dead_letter_at, dead_letter_reason, repair_only
) VALUES (?, 'phase1_extract/v2', ?, 'project', ?, 'pending', ?, 0, 3,
           NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, 0)
ON CONFLICT(kind, scope, project_key, job_key) DO NOTHING
`, jobID, jobKey, capture.ProjectKey, JobPriorityPhase1ExtractV2,
		capture.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		capture.SessionID, capture.SessionGeneration, capture.ID,
		capture.MessageSeqStart, capture.MessageSeqEnd, capture.CanonicalRevision,
		capture.RangeHash, now.Format(time.RFC3339Nano),
		now.Add(DefaultMemoryJobTimeout).Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("memorystore: insert digest replay job: %w", err)
	}
	job, err := loadDigestReplayJobByKeyTx(ctx, tx, capture.ProjectKey, jobKey)
	if err != nil {
		return nil, fmt.Errorf("memorystore: resolve digest replay job: %w", err)
	}
	if !digestReplayJobIdentityMatches(*job, capture) {
		return nil, fmt.Errorf("%w: replay job identity", ErrDigestReplayIneligible)
	}
	return job, nil
}

func linkDigestReplayJobTx(ctx context.Context, tx *sql.Tx, row digestReplayRow, jobID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_digests
SET status = 'pending', process_job_id = ?, last_error = NULL,
    processed_at = NULL, rollup_status = 'pending',
    rollup_window_id = NULL, rolled_up_at = NULL, updated_at = ?
WHERE project_key = ? AND id = ? AND capture_receipt_id = ?
  AND status IN ('pending', 'processing', 'failed')
`, jobID, now, row.ProjectKey, row.ID, row.CaptureReceiptID)
	if err != nil {
		return fmt.Errorf("memorystore: link digest replay job: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrJobConflict
	}
	return nil
}

func replayDigestClassificationError(
	result DigestReplayResult,
	cause error,
	dryRun bool,
) error {
	if dryRun {
		return nil
	}
	if cause != nil {
		return cause
	}
	if result.Classification == DigestReplayStaleGeneration {
		return ErrSessionPurgeGenerationMismatch
	}
	return fmt.Errorf("%w: %s (%s)", ErrDigestReplayIneligible,
		result.DigestID, result.Reason)
}
