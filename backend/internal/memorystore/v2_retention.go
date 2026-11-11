package memorystore

import (
	"context"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// DiagnosticsPruneResult reports bounded cleanup of diagnostic-only rows.
type DiagnosticsPruneResult struct {
	RecallTraces int64 `json:"recall_traces"`
	Exposures    int64 `json:"exposures"`
}

// PruneDiagnostics deletes expired recall traces and injected_only exposure
// rows for the current project. Cited/read usage is intentionally retained as
// part of usage accounting. A zero retention value disables that table's
// cleanup.
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
WHERE use_kind = 'injected_only' AND used_at < ?
  AND memory_id IN (SELECT id FROM memory_entries WHERE project_key = ?)
`, cutoff, s.cfg.ProjectKey)
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
