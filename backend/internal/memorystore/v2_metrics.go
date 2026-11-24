package memorystore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const MetricRecoveredDigestSessions = "recovered_digest_sessions"

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
	TrustCounts        map[string]int `json:"trust_counts,omitempty"`
	FeedbackByKind     map[string]int `json:"feedback_by_kind,omitempty"`
	PatchesByStatus    map[string]int `json:"patches_by_status,omitempty"`
	DreamRunsByStatus  map[string]int `json:"dream_runs_by_status,omitempty"`
	EntityCount        int            `json:"entity_count"`
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
WHERE tenant_id = ? AND project_key = ? AND status = 'active'
GROUP BY kind ORDER BY kind
`, s.tenantID(), s.cfg.ProjectKey)
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
WHERE u.tenant_id = ? AND e.tenant_id = ? AND e.project_key = ?
GROUP BY u.use_kind
`, s.tenantID(), s.tenantID(), s.cfg.ProjectKey)
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
SELECT COUNT(1), COALESCE(SUM(token_estimate), 0) FROM memory_recall_traces
WHERE tenant_id = ? AND project_key = ?
`, s.tenantID(), s.cfg.ProjectKey).Scan(&metrics.RecallTraceCount, &metrics.RecallTokenTotal)
	rows, err = db.QueryContext(ctx, `
SELECT status, COUNT(1) FROM memory_jobs
WHERE tenant_id = ? AND project_key = ? AND kind = 'summary_rebuild'
GROUP BY status
`, s.tenantID(), s.cfg.ProjectKey)
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
	if extra, extraErr := s.OperationalMetrics(ctx); extraErr == nil {
		metrics.TrustCounts = extra.TrustCounts
		metrics.FeedbackByKind = extra.FeedbackByKind
		metrics.PatchesByStatus = extra.PatchesByStatus
		metrics.DreamRunsByStatus = extra.DreamRunsByStatus
		metrics.EntityCount = extra.EntityCount
	}
	return metrics, rows.Err()
}

// IncrementRuntimeMetric bumps a small project-scoped counter used by status
// diagnostics. Metrics are best-effort operational data, not memory content.
func (s *Store) IncrementRuntimeMetric(ctx context.Context, metric string, delta int64) error {
	metric = strings.TrimSpace(metric)
	if metric == "" {
		return fmt.Errorf("memorystore: runtime metric name is required")
	}
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_runtime_metrics (project_key, metric, value, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(project_key, metric) DO UPDATE SET
    value = memory_runtime_metrics.value + excluded.value,
    updated_at = excluded.updated_at
`, s.cfg.ProjectKey, metric, delta, nowText())
	return err
}

func (s *Store) RuntimeMetric(ctx context.Context, metric string) (int64, error) {
	metric = strings.TrimSpace(metric)
	if metric == "" {
		return 0, nil
	}
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	var value int64
	err = db.QueryRowContext(ctx, `
SELECT COALESCE((SELECT value FROM memory_runtime_metrics WHERE project_key = ? AND metric = ?), 0)
`, s.cfg.ProjectKey, metric).Scan(&value)
	return value, err
}
