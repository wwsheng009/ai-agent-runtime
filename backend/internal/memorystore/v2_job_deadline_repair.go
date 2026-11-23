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

// RepairMissingJobDeadlines classifies or repairs bounded historical jobs whose
// absolute deadline was not persisted by an older writer. It derives the
// deadline only from the durable created_at value; malformed rows are reported
// as skipped rather than assigned a guessed deadline.
func (s *Store) RepairMissingJobDeadlines(
	ctx context.Context, opts JobDeadlineRepairOptions,
) (JobDeadlineRepairResult, error) {
	result := JobDeadlineRepairResult{DryRun: opts.DryRun, Items: make([]JobDeadlineRepairItem, 0)}
	if s == nil {
		return result, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	reason, _ := redactOperatorReason(opts.Reason)
	if strings.TrimSpace(reason) == "" {
		reason = "repair missing job deadline"
	}

	run := func(runCtx context.Context) error {
		attemptResult := JobDeadlineRepairResult{
			DryRun: opts.DryRun,
			Items:  make([]JobDeadlineRepairItem, 0, limit),
		}
		txOptions := sqliteutil.WriteTxOptions
		if opts.DryRun {
			txOptions = &sql.TxOptions{ReadOnly: true}
		}
		tx, txErr := db.BeginTx(runCtx, txOptions)
		if txErr != nil {
			return fmt.Errorf("memorystore: begin job deadline repair: %w", txErr)
		}
		defer func() { _ = tx.Rollback() }()
		if !opts.DryRun {
			if _, ok := maintenanceLeaseFromContext(runCtx); !ok {
				return ErrMaintenanceLeaseLost
			}
			if txErr = validateMaintenanceLeaseTx(runCtx, tx, s.cfg.ProjectKey); txErr != nil {
				return txErr
			}
		}
		now := time.Now().UTC()
		nowText := now.Format(time.RFC3339Nano)
		rows, queryErr := tx.QueryContext(runCtx, `
SELECT id, kind, status, attempt, max_attempts, COALESCE(created_at, '')
FROM memory_jobs
WHERE project_key = ?
  AND COALESCE(deadline_at, '') = ''
  AND status = 'pending'
  AND (COALESCE(available_at, '') = '' OR available_at <= ?)
  AND (COALESCE(retry_at, '') = '' OR retry_at <= ?)
  AND (COALESCE(deferred_until, '') = '' OR deferred_until <= ?)
ORDER BY created_at ASC, id ASC
LIMIT ?
`, s.cfg.ProjectKey, nowText, nowText, nowText, limit)
		if queryErr != nil {
			return fmt.Errorf("memorystore: list jobs missing deadlines: %w", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var id, kind, status, createdText string
			var attempt, maxAttempts int
			if scanErr := rows.Scan(&id, &kind, &status, &attempt, &maxAttempts, &createdText); scanErr != nil {
				return fmt.Errorf("memorystore: scan job missing deadline: %w", scanErr)
			}
			attemptResult.Scanned++
			item := JobDeadlineRepairItem{
				ID: id, Kind: kind, Status: JobStatus(status), Attempt: attempt,
				MaxAttempts: maxAttempts,
			}
			createdAt := parseTime(createdText)
			if createdAt.IsZero() {
				item.Skipped = true
				item.Reason = "invalid_created_at"
				attemptResult.Skipped++
				attemptResult.Items = append(attemptResult.Items, item)
				continue
			}
			item.CreatedAt = createdAt.UTC()
			item.DeadlineAt = createdAt.UTC().Add(DefaultMemoryJobTimeout)
			item.Reason = "deadline_from_created_at"
			if fenceErr := checkJobPurgeFenceByIDTx(runCtx, tx, id, s.cfg.ProjectKey); fenceErr != nil {
				if !errors.Is(fenceErr, ErrSessionPurgeGenerationMismatch) {
					return fenceErr
				}
				item.Skipped = true
				item.Reason = "purge_fence"
				attemptResult.Skipped++
				attemptResult.Items = append(attemptResult.Items, item)
				continue
			}
			attemptResult.Eligible++
			if opts.DryRun {
				attemptResult.Items = append(attemptResult.Items, item)
				continue
			}
			deadlineText := item.DeadlineAt.Format(time.RFC3339Nano)
			update, updateErr := tx.ExecContext(runCtx, `
UPDATE memory_jobs
SET deadline_at = ?, updated_at = ?
WHERE project_key = ? AND id = ?
  AND COALESCE(deadline_at, '') = ''
  AND status = ?
			`, deadlineText, nowText, s.cfg.ProjectKey, id, status)
			if updateErr != nil {
				return fmt.Errorf("memorystore: repair job deadline: %w", updateErr)
			}
			affected, affectedErr := update.RowsAffected()
			if affectedErr != nil {
				return affectedErr
			}
			if affected != 1 {
				item.Skipped = true
				item.Reason = "state_changed"
				attemptResult.Skipped++
				attemptResult.Items = append(attemptResult.Items, item)
				continue
			}
			item.Applied = true
			attemptResult.Updated++
			attemptResult.Items = append(attemptResult.Items, item)
			if _, auditErr := tx.ExecContext(runCtx, `
INSERT INTO memory_job_operator_audit(
    id, project_key, job_id, action, reason, previous_status, new_status, created_at
) VALUES (?, ?, ?, 'repair_deadline', ?, ?, ?, ?)
			`, uuid.NewString(), s.cfg.ProjectKey, id, reason, status, status, nowText); auditErr != nil {
				return fmt.Errorf("memorystore: audit job deadline repair: %w", auditErr)
			}
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return fmt.Errorf("memorystore: read jobs missing deadlines: %w", rowsErr)
		}
		if opts.DryRun {
			result = attemptResult
			return nil
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("memorystore: commit job deadline repair: %w", commitErr)
		}
		result = attemptResult
		return nil
	}

	if opts.DryRun {
		err = run(ctx)
	} else {
		err = sqliteutil.RetryWriteTx(ctx, nil, run)
	}
	return result, err
}

func (s *Store) PlanMissingJobDeadlines(ctx context.Context, limit int) (JobDeadlineRepairResult, error) {
	return s.RepairMissingJobDeadlines(ctx, JobDeadlineRepairOptions{Limit: limit, DryRun: true})
}
