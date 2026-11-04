package usageanalytics

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const defaultPruneBatchSize = 2000

// PruneOptions bounds one retention sweep. BatchSize keeps each write
// transaction short so live event ingestion is not blocked for a long scan.
type PruneOptions struct {
	Cutoff    time.Time
	BatchSize int
}

// PruneStats reports raw detail rows removed by one retention sweep.
type PruneStats struct {
	Requests         int64 `json:"requests"`
	ToolCalls        int64 `json:"tool_calls"`
	Turns            int64 `json:"turns"`
	Subagents        int64 `json:"subagents"`
	Routes           int64 `json:"routes"`
	LSPRequests      int64 `json:"lsp_requests"`
	RenderFenceDrops int64 `json:"render_fence_drops"`
	StatsRebuilt     bool  `json:"stats_rebuilt"`
}

func (s PruneStats) Total() int64 {
	return s.Requests + s.ToolCalls + s.Turns + s.Subagents + s.Routes +
		s.LSPRequests + s.RenderFenceDrops
}

// PruneBefore removes request detail before cutoff and rebuilds rollups.
// It is retained for the existing CLI contract and now also prunes the other
// high-volume raw tables (tool calls dominate the analytics database size).
func (s *Store) PruneBefore(cutoff time.Time) (int64, error) {
	stats, err := s.PruneDetailBefore(context.Background(), PruneOptions{Cutoff: cutoff})
	return stats.Requests, err
}

// PruneDetailBefore performs a bounded, repeatable retention sweep across raw
// analytics tables. usage_sessions is intentionally retained so historical
// sessions remain discoverable; its pre-aggregated request counters are rebuilt
// whenever request rows were removed.
func (s *Store) PruneDetailBefore(ctx context.Context, opts PruneOptions) (PruneStats, error) {
	var stats PruneStats
	if s == nil || s.db == nil || s.empty || s.readOnly {
		return stats, fmt.Errorf("prune usage analytics: store is not writable")
	}
	if opts.Cutoff.IsZero() {
		return stats, fmt.Errorf("prune usage analytics: cutoff is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultPruneBatchSize
	}
	cutoff := opts.Cutoff.UnixNano()

	tables := []struct {
		name     string
		index    string
		timeExpr string
		target   *int64
	}{
		{"usage_requests", "idx_usage_requests_started", "started_at_unix_nano", &stats.Requests},
		{"usage_tool_calls", "idx_usage_tool_calls_started", "started_at_unix_nano", &stats.ToolCalls},
		{"usage_turns", "idx_usage_turns_started", "started_at_unix_nano", &stats.Turns},
		{"usage_subagents", "idx_usage_subagents_started", "started_at_unix_nano", &stats.Subagents},
		{"usage_routes", "idx_usage_routes_time", "recorded_at_unix_nano", &stats.Routes},
		{"usage_lsp_requests", "idx_usage_lsp_requests_time", "started_unix_nano", &stats.LSPRequests},
		{"usage_render_fence_drops", "idx_usage_render_fence_drops_time", "last_at_unix_nano", &stats.RenderFenceDrops},
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		deleted, err := s.pruneRowsBefore(ctx, table.name, table.index, table.timeExpr, cutoff, batchSize)
		if err != nil {
			return stats, err
		}
		*table.target = deleted
	}

	if stats.Requests > 0 {
		if err := s.withWriteTx(func(tx *sql.Tx) error {
			return s.rebuildSessionStatsTx(tx, "")
		}); err != nil {
			return stats, fmt.Errorf("rebuild usage session stats after prune: %w", err)
		}
		stats.StatsRebuilt = true
		s.statsGen.Add(1)
	}
	return stats, nil
}

func (s *Store) pruneRowsBefore(
	ctx context.Context,
	table string,
	index string,
	timeExpr string,
	cutoff int64,
	batchSize int,
) (int64, error) {
	query := pruneRowsBeforeSQL(table, index, timeExpr)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var affected int64
		if err := s.withWriteTx(func(tx *sql.Tx) error {
			result, err := tx.Exec(query, cutoff, batchSize)
			if err != nil {
				return fmt.Errorf("prune %s: %w", table, err)
			}
			affected, err = result.RowsAffected()
			if err != nil {
				return fmt.Errorf("prune %s rows: %w", table, err)
			}
			return nil
		}); err != nil {
			return total, err
		}
		total += affected
		if affected < int64(batchSize) {
			return total, nil
		}
	}
}

func pruneRowsBeforeSQL(table, index, timeExpr string) string {
	return fmt.Sprintf(`DELETE FROM %s
WHERE rowid IN (
  SELECT rowid FROM %s INDEXED BY %s
  WHERE %s > 0 AND %s < ?
  ORDER BY %s
  LIMIT ?
)`, table, table, index, timeExpr, timeExpr, timeExpr)
}
