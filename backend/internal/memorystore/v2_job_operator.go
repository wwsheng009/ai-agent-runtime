package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

const maxJobOperatorSummaryGroups = 32
const maxJobOperatorSummaryGroupKeyRunes = 128

// SummarizeJobs returns bounded project-scoped aggregate counts without
// exposing job keys, session IDs, or provider diagnostics.
func (s *Store) SummarizeJobs(ctx context.Context) (JobOperatorSummary, error) {
	summary := JobOperatorSummary{
		ByKind:     JobCountGroups{Counts: map[string]int{}},
		ByStatus:   JobCountGroups{Counts: map[string]int{}},
		ByPriority: JobCountGroups{Counts: map[string]int{}},
	}
	db, err := s.ensureV2()
	if err != nil {
		return summary, err
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	var oldestPending string
	var runningJobs int
	err = db.QueryRowContext(ctx, `
SELECT COUNT(1),
       COALESCE(SUM(CASE WHEN status = 'pending' AND
         COALESCE(available_at, '') <= ? AND COALESCE(retry_at, '') <= ? AND
         COALESCE(deferred_until, '') <= ? THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'pending' AND
         (COALESCE(available_at, '') > ? OR COALESCE(retry_at, '') > ? OR
          COALESCE(deferred_until, '') > ?) THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'running' AND
         COALESCE(lease_until, '') > ? THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN COALESCE(dead_letter_at, '') = '' AND
         ((status = 'failed' AND attempt < max_attempts) OR
          (status = 'pending' AND attempt < max_attempts AND
           (COALESCE(retry_at, '') <> '' OR COALESCE(last_error, '') <> '')))
         THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'dead_letter' OR
         COALESCE(dead_letter_at, '') <> '' THEN 1 ELSE 0 END), 0),
       COALESCE(MIN(CASE WHEN status = 'pending' THEN NULLIF(created_at, '') END), '')
FROM memory_jobs
WHERE tenant_id = ? AND project_key = ?
	`, nowText, nowText, nowText, nowText, nowText, nowText, nowText, s.tenantID(), s.cfg.ProjectKey).Scan(
		&summary.TotalJobs, &summary.PendingDueJobs, &summary.PendingDeferredJobs,
		&summary.LeasedJobs, &runningJobs, &summary.RetryableJobs, &summary.DeadLetterJobs,
		&oldestPending,
	)
	if err != nil {
		return summary, fmt.Errorf("memorystore: summarize memory jobs: %w", err)
	}
	summary.BlockedJobs = summary.PendingDeferredJobs + runningJobs
	if createdAt := parseTime(oldestPending); !createdAt.IsZero() {
		if age := now.Sub(createdAt); age > 0 {
			summary.OldestPendingAgeSeconds = int64(age.Seconds())
		}
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_jobs
WHERE tenant_id = ? AND project_key = ?
  AND COALESCE(deadline_at, '') = ''
  AND status NOT IN ('succeeded', 'cancelled', 'dead_letter')
`, s.tenantID(), s.cfg.ProjectKey).Scan(&summary.MissingDeadlineJobs); err != nil {
		return summary, fmt.Errorf("memorystore: count jobs missing deadlines: %w", err)
	}
	if summary.ByKind, err = summarizeJobGroups(ctx, db, s.tenantID(), s.cfg.ProjectKey, "kind", summary.TotalJobs); err != nil {
		return summary, err
	}
	if summary.ByStatus, err = summarizeJobGroups(ctx, db, s.tenantID(), s.cfg.ProjectKey, "status", summary.TotalJobs); err != nil {
		return summary, err
	}
	if summary.ByPriority, err = summarizeJobGroups(ctx, db, s.tenantID(), s.cfg.ProjectKey, "priority", summary.TotalJobs); err != nil {
		return summary, err
	}
	return summary, nil
}

func summarizeJobGroups(ctx context.Context, db *sql.DB, tenantID, projectKey, dimension string, total int) (JobCountGroups, error) {
	group := JobCountGroups{Counts: map[string]int{}}
	column := ""
	switch dimension {
	case "kind":
		column = "kind"
	case "status":
		column = "status"
	case "priority":
		column = "CAST(priority AS TEXT)"
	default:
		return group, fmt.Errorf("memorystore: unsupported job summary dimension")
	}
	rows, err := db.QueryContext(ctx, `SELECT `+column+`, COUNT(1) FROM memory_jobs
WHERE tenant_id = ? AND project_key = ? GROUP BY `+column+` ORDER BY COUNT(1) DESC, `+column+` ASC LIMIT ?`,
		tenantID, projectKey, maxJobOperatorSummaryGroups)
	if err != nil {
		return group, fmt.Errorf("memorystore: group memory jobs by %s: %w", dimension, err)
	}
	defer rows.Close()
	returnedJobs := 0
	returnedGroups := 0
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return group, fmt.Errorf("memorystore: scan memory job summary group: %w", err)
		}
		if dimension == "priority" {
			if priority, err := strconv.Atoi(key); err == nil {
				key = strconv.Itoa(priority)
			}
		}
		key = boundJobSummaryGroupKey(key)
		group.Counts[key] += count
		returnedJobs += count
		returnedGroups++
	}
	if err := rows.Err(); err != nil {
		return group, fmt.Errorf("memorystore: read memory job summary groups: %w", err)
	}
	var distinctGroups int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT `+column+`) FROM memory_jobs WHERE tenant_id = ? AND project_key = ?`,
		tenantID, projectKey,
	).Scan(&distinctGroups); err != nil {
		return group, fmt.Errorf("memorystore: count memory job summary groups: %w", err)
	}
	if omitted := distinctGroups - returnedGroups; omitted > 0 {
		group.OmittedGroups = omitted
	}
	if omitted := total - returnedJobs; omitted > 0 {
		group.OmittedJobs = omitted
	}
	return group, nil
}

func boundJobSummaryGroupKey(key string) string {
	key = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, key)
	key = strings.Join(strings.Fields(key), " ")
	key, _ = memoryredact.Redact(key)
	if utf8.RuneCountInString(key) <= maxJobOperatorSummaryGroupKeyRunes {
		return key
	}
	runes := []rune(key)
	return string(runes[:maxJobOperatorSummaryGroupKeyRunes-1]) + "…"
}

func (s *Store) ListJobDiagnostics(ctx context.Context, f JobDiagnosticFilter) ([]JobDiagnostic, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	where := []string{"tenant_id = ?", "project_key = ?"}
	args := []interface{}{s.tenantID(), s.cfg.ProjectKey}
	for _, x := range []struct{ column, value string }{{"kind", f.Kind}, {"status", f.Status}, {"session_id", f.SessionID}} {
		if strings.TrimSpace(x.value) != "" {
			where = append(where, x.column+" = ?")
			args = append(args, strings.TrimSpace(x.value))
		}
	}
	if f.OlderThan > 0 {
		where = append(where, "created_at <= ?")
		args = append(args, time.Now().UTC().Add(-f.OlderThan).Format(time.RFC3339Nano))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `SELECT kind,id,job_key,COALESCE(session_id,''),status,priority,attempt,
 COALESCE(created_at,''),COALESCE(available_at,''),COALESCE(lease_until,''),COALESCE(deadline_at,''),
 COALESCE(dead_letter_at,''),COALESCE(dead_letter_reason,''),COALESCE(last_error,'')
 FROM memory_jobs WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]JobDiagnostic, 0)
	for rows.Next() {
		var d JobDiagnostic
		if err := rows.Scan(&d.Kind, &d.ID, &d.JobKey, &d.SessionID, &d.Status, &d.Priority, &d.Attempt,
			&d.CreatedAt, &d.AvailableAt, &d.LeaseUntil, &d.TimeoutAt, &d.DeadLetterAt, &d.DeadLetterReason, &d.LastError); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RequeueJob(ctx context.Context, id, reason string) error {
	return s.operatorJobChange(ctx, id, "retry", reason)
}
func (s *Store) DeadLetterJob(ctx context.Context, id, reason string) error {
	return s.operatorJobChange(ctx, id, "dead_letter", reason)
}
func (s *Store) operatorJobChange(ctx context.Context, id, action, reason string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	reason, _ = redactOperatorReason(reason)
	return sqliteutil.RetryWriteTx(ctx, nil, func(ctx context.Context) error {
		tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
			return err
		}
		var status, lease, retryAt, lastError string
		var attempt int
		err = tx.QueryRowContext(ctx, `
SELECT status,COALESCE(lease_until,''),attempt,COALESCE(retry_at,''),COALESCE(last_error,'')
FROM memory_jobs
WHERE id=? AND tenant_id=? AND project_key=?
`, id, s.tenantID(), s.cfg.ProjectKey).Scan(&status, &lease, &attempt, &retryAt, &lastError)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		}
		if err != nil {
			return err
		}
		if err = checkJobPurgeFenceByIDTx(ctx, tx, s.tenantID(), id, s.cfg.ProjectKey); err != nil {
			return err
		}
		now := time.Now().UTC()
		if status == string(JobStatusRunning) && lease != "" && parseTime(lease).After(now) {
			return ErrJobConflict
		}
		var next string
		if action == "retry" {
			// Pending jobs may be manually released only when they represent a
			// scheduled retry (retry_at set) or a failed attempt (last_error set).
			if status != string(JobStatusFailed) && status != string(JobStatusDeadLetter) &&
				(status != string(JobStatusPending) || (retryAt == "" && lastError == "")) {
				return ErrJobConflict
			}
			next = string(JobStatusPending)
			deadline := now.Add(DefaultMemoryJobTimeout).Format(time.RFC3339Nano)
			var res sql.Result
			res, err = tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status='pending',worker_id=NULL,lease_until=NULL,retry_at=NULL,finished_at=NULL,
    dead_letter_at=NULL,dead_letter_reason=NULL,last_error='',available_at=?,deadline_at=?,
    updated_at=?
WHERE id=? AND tenant_id=? AND project_key=? AND status=? AND attempt=?
  AND COALESCE(lease_until,'')=?
`, now.Format(time.RFC3339Nano), deadline, now.Format(time.RFC3339Nano),
				id, s.tenantID(), s.cfg.ProjectKey, status, attempt, lease)
			if err == nil {
				var affected int64
				affected, err = res.RowsAffected()
				if err == nil && affected != 1 {
					return ErrJobConflict
				}
			}
		} else {
			if status == string(JobStatusSucceeded) || status == string(JobStatusCancelled) || status == string(JobStatusDeadLetter) {
				return ErrJobConflict
			}
			next = string(JobStatusDeadLetter)
			var res sql.Result
			res, err = tx.ExecContext(ctx, `
UPDATE memory_jobs
SET status='dead_letter',worker_id=NULL,lease_until=NULL,retry_at=NULL,finished_at=?,
    dead_letter_at=?,dead_letter_reason=?,last_error=?,updated_at=?
WHERE id=? AND tenant_id=? AND project_key=? AND status=? AND attempt=?
  AND COALESCE(lease_until,'')=?
`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), reason, reason,
				now.Format(time.RFC3339Nano), id, s.tenantID(), s.cfg.ProjectKey, status, attempt, lease)
			if err == nil {
				var affected int64
				affected, err = res.RowsAffected()
				if err == nil && affected != 1 {
					return ErrJobConflict
				}
			}
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO memory_job_operator_audit(id,project_key,job_id,action,reason,previous_status,new_status,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			uuid.NewString(), s.cfg.ProjectKey, id, action, reason, status, next, now.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrJobConflict
		}
		return tx.Commit()
	})
}

func redactOperatorReason(reason string) (string, bool) {
	reason = strings.TrimSpace(reason)
	reason, _ = memoryredact.Redact(reason)
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return reason, false
}
