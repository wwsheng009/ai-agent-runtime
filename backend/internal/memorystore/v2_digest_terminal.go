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

// CloseDeadLetterDigest closes one receipt-backed digest after its process job
// has been explicitly moved to dead_letter. This is the operator recovery path
// for deterministic extraction failures: it does not replay, reset attempts, or
// synthesize a successful extraction. It only marks the digest terminal and
// records an operator audit row.
//
// The method is idempotent for already terminal digests and refuses:
//   - digests without a capture receipt or process job;
//   - jobs that are not dead_letter;
//   - digests that already have an output receipt.
func (s *Store) CloseDeadLetterDigest(ctx context.Context, digestID, reason string) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return false, err
	}
	digestID = strings.TrimSpace(digestID)
	if digestID == "" {
		return false, fmt.Errorf("%w: digest id is required", ErrDigestReplayIneligible)
	}
	reason, _ = redactOperatorReason(reason)
	if strings.TrimSpace(reason) == "" {
		reason = "operator closed dead-letter digest"
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return false, fmt.Errorf("memorystore: begin dead-letter digest close: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return false, err
	}

	var status, receiptID, jobID string
	err = tx.QueryRowContext(ctx, `
SELECT status, COALESCE(capture_receipt_id, ''), COALESCE(process_job_id, '')
FROM memory_turn_digests
WHERE tenant_id = ? AND project_key = ? AND id = ?
`, s.tenantID(), s.cfg.ProjectKey, digestID).Scan(&status, &receiptID, &jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: %s", ErrDigestNotFound, digestID)
	}
	if err != nil {
		return false, fmt.Errorf("memorystore: read dead-letter digest: %w", err)
	}
	switch TurnDigestStatus(status) {
	case TurnDigestDone, TurnDigestSkipped, TurnDigestInvalidated:
		return false, nil
	}
	if receiptID == "" || jobID == "" {
		return false, fmt.Errorf("%w: digest has no receipt-backed process job", ErrDigestReplayIneligible)
	}

	var jobStatus, jobReceiptID string
	err = tx.QueryRowContext(ctx, `
SELECT status, COALESCE(capture_receipt_id, '') FROM memory_jobs
WHERE tenant_id = ? AND project_key = ? AND id = ?
`, s.tenantID(), s.cfg.ProjectKey, jobID).Scan(&jobStatus, &jobReceiptID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: digest process job not found", ErrDigestReplayIneligible)
	}
	if err != nil {
		return false, fmt.Errorf("memorystore: read dead-letter digest job: %w", err)
	}
	if JobStatus(jobStatus) != JobStatusDeadLetter {
		return false, fmt.Errorf("%w: digest process job is %s, not dead_letter", ErrDigestReplayIneligible, jobStatus)
	}
	if jobReceiptID != receiptID {
		return false, fmt.Errorf("%w: digest process job receipt identity changed", ErrDigestReplayIneligible)
	}

	var outputReceipts int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_turn_output_receipts
WHERE tenant_id = ? AND project_key = ? AND capture_receipt_id = ?
`, s.tenantID(), s.cfg.ProjectKey, receiptID).Scan(&outputReceipts); err != nil {
		return false, fmt.Errorf("memorystore: check dead-letter digest output receipt: %w", err)
	}
	if outputReceipts > 0 {
		return false, fmt.Errorf("%w: digest already has an output receipt", ErrDigestReplayIneligible)
	}

	res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_digests
SET status = 'invalidated',
    rollup_status = 'skipped',
    rollup_window_id = NULL,
    rolled_up_at = NULL,
    processed_at = ?,
    last_error = ?,
    updated_at = ?
WHERE tenant_id = ? AND project_key = ? AND id = ?
  AND process_job_id = ?
  AND status IN ('pending', 'processing', 'failed', 'deferred')
`, nowText, reason, nowText, s.tenantID(), s.cfg.ProjectKey, digestID, jobID)
	if err != nil {
		return false, fmt.Errorf("memorystore: close dead-letter digest: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, fmt.Errorf("%w: digest state changed concurrently", ErrDigestReplayIneligible)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_job_operator_audit(
    id, project_key, job_id, action, reason, previous_status, new_status, created_at
) VALUES (?, ?, ?, 'close_digest', ?, ?, 'invalidated', ?)
`, uuid.NewString(), s.cfg.ProjectKey, jobID, reason, status, nowText); err != nil {
		return false, fmt.Errorf("memorystore: audit dead-letter digest close: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("memorystore: commit dead-letter digest close: %w", err)
	}
	return true, nil
}
