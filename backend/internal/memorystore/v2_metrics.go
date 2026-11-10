package memorystore

import (
	"context"
	"time"
)

type MemoryMetrics struct {
	Total              int            `json:"total"`
	ByStatus           map[string]int `json:"by_status"`
	ByFreshness        map[string]int `json:"by_freshness"`
	ByKind             map[string]int `json:"by_kind"`
	PinnedCount        int            `json:"pinned_count"`
	SummaryRevision    int64          `json:"summary_revision"`
	SummaryStatus      string         `json:"summary_status,omitempty"`
	SummaryFreshness   string         `json:"summary_freshness,omitempty"`
	Jobs               map[string]int `json:"jobs"`
	Usage              map[string]int `json:"usage"`
	LastExposureAt     time.Time      `json:"last_exposure_at,omitempty"`
	RecallTraceCount   int            `json:"recall_trace_count"`
	RecallTokenTotal   int            `json:"recall_token_total"`
	SummaryRebuildJobs map[string]int `json:"summary_rebuild_jobs"`
}

func (s *Store) Metrics(ctx context.Context) (MemoryMetrics, error) {
	status, err := s.Status(ctx)
	if err != nil && !status.Fallback {
		return MemoryMetrics{}, err
	}
	metrics := MemoryMetrics{
		Total: status.Total, ByStatus: status.ByStatus, ByFreshness: status.ByFreshness,
		PinnedCount: status.PinnedCount, SummaryRevision: status.SummaryRevision,
		SummaryStatus: status.SummaryStatus, SummaryFreshness: status.SummaryFreshness,
		Jobs: status.Jobs, ByKind: map[string]int{}, Usage: map[string]int{},
		SummaryRebuildJobs: map[string]int{},
	}
	if !s.v2Configured() {
		return metrics, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return metrics, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT kind, COUNT(1) FROM memory_entries
WHERE project_key = ? AND status = 'active'
GROUP BY kind ORDER BY kind
`, s.cfg.ProjectKey)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			_ = rows.Close()
			return metrics, err
		}
		metrics.ByKind[kind] = count
	}
	_ = rows.Close()
	rows, err = db.QueryContext(ctx, `
SELECT u.use_kind, COUNT(1), COALESCE(MAX(u.used_at), '')
FROM memory_usage u
JOIN memory_entries e ON e.id = u.memory_id
WHERE e.project_key = ?
GROUP BY u.use_kind
`, s.cfg.ProjectKey)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var kind string
		var count int
		var last string
		if err := rows.Scan(&kind, &count, &last); err != nil {
			_ = rows.Close()
			return metrics, err
		}
		metrics.Usage[kind] = count
		if parsed := parseTime(last); parsed.After(metrics.LastExposureAt) {
			metrics.LastExposureAt = parsed
		}
	}
	_ = rows.Close()
	_ = db.QueryRowContext(ctx, `
SELECT COUNT(1), COALESCE(SUM(token_estimate), 0) FROM memory_recall_traces WHERE project_key = ?
`, s.cfg.ProjectKey).Scan(&metrics.RecallTraceCount, &metrics.RecallTokenTotal)
	rows, err = db.QueryContext(ctx, `
SELECT status, COUNT(1) FROM memory_jobs
WHERE project_key = ? AND kind = 'summary_rebuild'
GROUP BY status
`, s.cfg.ProjectKey)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var statusValue string
		var count int
		if err := rows.Scan(&statusValue, &count); err != nil {
			_ = rows.Close()
			return metrics, err
		}
		metrics.SummaryRebuildJobs[statusValue] = count
	}
	_ = rows.Close()
	return metrics, rows.Err()
}
