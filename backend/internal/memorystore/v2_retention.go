package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// DiagnosticsPruneResult reports bounded cleanup of diagnostic-only rows.
type DiagnosticsPruneResult struct {
	RecallTraces int64 `json:"recall_traces"`
	Exposures    int64 `json:"exposures"`
}

// PruneDiagnostics deletes expired recall traces and injected_only exposure
// rows for the current project. Cited/read usage is intentionally retained for
// the longer lifecycle audit window. A zero retention value disables that
// table's cleanup.
func (s *Store) PruneDiagnostics(ctx context.Context, now time.Time) (DiagnosticsPruneResult, error) {
	var result DiagnosticsPruneResult
	if s == nil {
		return result, fmt.Errorf("memorystore: store is nil")
	}
	traceDays := s.cfg.RecallTraceRetentionDays
	exposureDays := s.cfg.ExposureRetentionDays
	if traceDays <= 0 && exposureDays <= 0 {
		return result, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return result, err
	}
	if traceDays > 0 {
		cutoff := now.UTC().Add(-time.Duration(traceDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
DELETE FROM memory_recall_traces
WHERE project_key = ? AND created_at < ?
`, s.cfg.ProjectKey, cutoff)
		if err != nil {
			return result, err
		}
		result.RecallTraces, _ = res.RowsAffected()
	}
	if exposureDays > 0 {
		cutoff := now.UTC().Add(-time.Duration(exposureDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
DELETE FROM memory_usage
WHERE project_key = ? AND use_kind = 'injected_only' AND used_at < ?
`, s.cfg.ProjectKey, cutoff)
		if err != nil {
			return result, err
		}
		result.Exposures, _ = res.RowsAffected()
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// SweepLifecycle applies the configured retention policy to records. It is
// bounded and idempotent:
//   - candidate rows older than CandidateDays become expired;
//   - standard/ephemeral active rows unused for their class window expire;
//   - the active+candidate set is capped at MaxRecords by expiring the
//     lowest-value non-pinned, non-permanent rows;
//   - tombstones older than DeletedDays are physically removed;
//   - audit events and usage older than AuditDays are pruned;
//   - only already-expired suppressions are removed (live suppressions guard
//     against deletion resurrection and are never silently dropped).
//
// Permanent, pinned, and manual records are never auto-expired by a time or
// count rule. Zero day/count values disable that individual dimension. The
// method is a no-op when Retention.Enabled is false.
func (s *Store) SweepLifecycle(ctx context.Context, now time.Time) (RetentionSweepResult, error) {
	var result RetentionSweepResult
	if s == nil {
		return result, fmt.Errorf("memorystore: store is nil")
	}
	if !s.v2Configured() {
		return result, nil
	}
	policy := s.cfg.Retention
	if !policy.Enabled {
		return result, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return result, err
	}

	if policy.Classes.Configured() {
		rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT kind FROM memory_entries
WHERE project_key = ? AND source_kind != 'manual'
`, s.cfg.ProjectKey)
		if err != nil {
			return result, err
		}
		var kinds []string
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				_ = rows.Close()
				return result, err
			}
			kinds = append(kinds, kind)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return result, err
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
		for _, kind := range kinds {
			kind = strings.ToLower(strings.TrimSpace(kind))
			if kind == "" {
				continue
			}
			class := policy.Classes.ClassForKind(kind)
			res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET retention_class = ?
WHERE project_key = ? AND kind = ? AND source_kind != 'manual' AND retention_class != ?
`, class, s.cfg.ProjectKey, kind, class)
			if err != nil {
				return result, err
			}
			affected, _ := res.RowsAffected()
			result.Reclassified += affected
		}
	}

	if policy.CandidateDays > 0 {
		cutoff := now.Add(-time.Duration(policy.CandidateDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		expired, err := s.expireRetentionRows(ctx, tx, now, `
SELECT id, COALESCE(source_session_id, ''), revision
FROM memory_entries
WHERE project_key = ? AND status = 'candidate' AND pinned = 0 AND source_kind != 'manual'
  AND updated_at < ?
ORDER BY updated_at ASC, id ASC
LIMIT 500
`, []interface{}{s.cfg.ProjectKey, cutoff}, "candidate_retention", "candidate_retention")
		if err != nil {
			return result, err
		}
		result.CandidatesExpired = expired
	}

	var activeConds []string
	var activeArgs []interface{}
	activeArgs = append(activeArgs, s.cfg.ProjectKey)
	if policy.ActiveUnusedDays > 0 {
		activeConds = append(activeConds,
			"(retention_class = 'standard' AND COALESCE(last_used_at, updated_at, created_at) < ?)")
		activeArgs = append(activeArgs,
			now.Add(-time.Duration(policy.ActiveUnusedDays)*24*time.Hour).Format(time.RFC3339Nano))
	}
	if policy.EphemeralDays > 0 {
		activeConds = append(activeConds,
			"(retention_class = 'ephemeral' AND COALESCE(last_used_at, updated_at, created_at) < ?)")
		activeArgs = append(activeArgs,
			now.Add(-time.Duration(policy.EphemeralDays)*24*time.Hour).Format(time.RFC3339Nano))
	}
	if len(activeConds) > 0 {
		query := `
SELECT id, COALESCE(source_session_id, ''), revision
FROM memory_entries
WHERE project_key = ? AND status = 'active' AND pinned = 0 AND source_kind != 'manual'
  AND (` + strings.Join(activeConds, " OR ") + `)
ORDER BY COALESCE(last_used_at, updated_at, created_at) ASC, id ASC
LIMIT 500
`
		expired, err := s.expireRetentionRows(ctx, tx, now, query, activeArgs,
			"active_unused", "active_unused")
		if err != nil {
			return result, err
		}
		result.ActiveExpired = expired
	}

	if policy.MaxRecords > 0 {
		var current int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_entries
WHERE project_key = ? AND status IN ('candidate', 'active')
`, s.cfg.ProjectKey).Scan(&current); err != nil {
			return result, err
		}
		if overflow := current - policy.MaxRecords; overflow > 0 {
			rows, queryErr := tx.QueryContext(ctx, `
SELECT id, COALESCE(source_session_id, ''), revision
FROM memory_entries
WHERE project_key = ? AND status IN ('candidate', 'active') AND pinned = 0
  AND source_kind != 'manual' AND retention_class != 'permanent'
ORDER BY CASE status WHEN 'candidate' THEN 0 ELSE 1 END,
         COALESCE(last_used_at, updated_at, created_at) ASC,
         use_count ASC, id ASC
LIMIT ?
`, s.cfg.ProjectKey, overflow)
			if queryErr != nil {
				return result, queryErr
			}
			expired, expireErr := s.expireRetentionRowsFromRows(ctx, tx, rows, now,
				"max_records", "max_records")
			if expireErr != nil {
				return result, expireErr
			}
			result.MaxRecordsExpired = expired
		}
	}

	if policy.DeletedDays > 0 {
		cutoff := now.Add(-time.Duration(policy.DeletedDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_summary_items
WHERE memory_id IN (
    SELECT id FROM memory_entries
    WHERE project_key = ? AND status = 'deleted' AND deleted_at IS NOT NULL
      AND deleted_at != '' AND deleted_at < ?
)
`, s.cfg.ProjectKey, cutoff); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_sources
WHERE memory_id IN (
    SELECT id FROM memory_entries
    WHERE project_key = ? AND status = 'deleted' AND deleted_at IS NOT NULL
      AND deleted_at != '' AND deleted_at < ?
)
`, s.cfg.ProjectKey, cutoff); err != nil {
			return result, err
		}
		res, err := tx.ExecContext(ctx, `
DELETE FROM memory_entries
WHERE project_key = ? AND status = 'deleted' AND deleted_at IS NOT NULL
  AND deleted_at != '' AND deleted_at < ?
`, s.cfg.ProjectKey, cutoff)
		if err != nil {
			return result, err
		}
		result.TombstonesDeleted, _ = res.RowsAffected()
	}

	if policy.AuditDays > 0 {
		cutoff := now.Add(-time.Duration(policy.AuditDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
DELETE FROM memory_events
WHERE project_key = ? AND created_at < ?
`, s.cfg.ProjectKey, cutoff)
		if err != nil {
			return result, err
		}
		result.EventsDeleted, _ = res.RowsAffected()
		res, err = tx.ExecContext(ctx, `
DELETE FROM memory_usage
WHERE project_key = ? AND used_at < ?
`, s.cfg.ProjectKey, cutoff)
		if err != nil {
			return result, err
		}
		result.UsageDeleted, _ = res.RowsAffected()
	}

	res, err := tx.ExecContext(ctx, `
DELETE FROM memory_suppressions
WHERE project_key = ? AND expires_at IS NOT NULL AND expires_at != '' AND expires_at <= ?
`, s.cfg.ProjectKey, now.Format(time.RFC3339Nano))
	if err != nil {
		return result, err
	}
	result.SuppressionsPurged, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) expireRetentionRows(ctx context.Context, tx *sql.Tx, now time.Time, query string,
	args []interface{}, reason, staleReason string) (int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return s.expireRetentionRowsFromRows(ctx, tx, rows, now, reason, staleReason)
}

func (s *Store) expireRetentionRowsFromRows(ctx context.Context, tx *sql.Tx, rows *sql.Rows,
	now time.Time, reason, staleReason string) (int64, error) {
	type target struct {
		id, session string
		revision    int64
	}
	var targets []target
	for rows.Next() {
		var item target
		if err := rows.Scan(&item.id, &item.session, &item.revision); err != nil {
			_ = rows.Close()
			return 0, err
		}
		targets = append(targets, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var expired int64
	for _, item := range targets {
		res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET status = 'expired', freshness_status = 'stale', stale_reason = ?,
    revision = revision + 1, updated_at = ?
WHERE id = ? AND project_key = ? AND revision = ? AND status IN ('candidate', 'active')
`, staleReason, now.Format(time.RFC3339Nano), item.id, s.cfg.ProjectKey, item.revision)
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			continue
		}
		expired++
		if err := insertEventTx(ctx, tx, s.cfg.ProjectKey, item.id, "expired", "system",
			item.session, item.revision+1, map[string]interface{}{"reason": reason}); err != nil {
			return 0, err
		}
		if err := s.markSummariesStaleForRecordTx(ctx, tx, item.id, "retention_"+reason); err != nil {
			return 0, err
		}
	}
	return expired, nil
}
