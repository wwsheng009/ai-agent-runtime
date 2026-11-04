package usageanalytics

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPruneDetailBeforeCoversAllRawTablesAndRebuildsStats locks the automatic
// retention contract: the largest table is usage_tool_calls, not requests.
func TestPruneDetailBeforeCoversAllRawTablesAndRebuildsStats(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour).UnixNano()
	current := now.Add(-24 * time.Hour).UnixNano()
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := store.db.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	exec(`INSERT INTO usage_sessions (session_id, status) VALUES ('s-prune-all', 'completed')`)
	for _, id := range []string{"r-old-1", "r-old-2"} {
		exec(`INSERT INTO usage_requests
			(llm_request_id, session_id, status, success, started_at_unix_nano, total_tokens)
			VALUES (?, 's-prune-all', 'success', 1, ?, 10)`, id, old)
	}
	exec(`INSERT INTO usage_requests
		(llm_request_id, session_id, status, success, started_at_unix_nano, total_tokens)
		VALUES ('r-new', 's-prune-all', 'success', 1, ?, 20)`, current)

	exec(`INSERT INTO usage_tool_calls (tool_call_id, session_id, tool_name, started_at_unix_nano)
		VALUES ('tool-old', 's-prune-all', 'view', ?), ('tool-new', 's-prune-all', 'view', ?)`, old, current)
	exec(`INSERT INTO usage_turns (session_id, turn_id, started_at_unix_nano)
		VALUES ('s-prune-all', 'turn-old', ?), ('s-prune-all', 'turn-new', ?)`, old, current)
	exec(`INSERT INTO usage_subagents (subagent_id, parent_session_id, batch_id, started_at_unix_nano)
		VALUES ('sub-old', 's-prune-all', 'b', ?), ('sub-new', 's-prune-all', 'b', ?)`, old, current)
	exec(`INSERT INTO usage_routes (route_event_id, session_id, recorded_at_unix_nano)
		VALUES ('route-old', 's-prune-all', ?), ('route-new', 's-prune-all', ?)`, old, current)
	exec(`INSERT INTO usage_lsp_requests (session_id, path_fingerprint, server, started_unix_nano)
		VALUES ('s-prune-all', 'fp-old', 'gopls', ?), ('s-prune-all', 'fp-new', 'gopls', ?)`, old, current)
	exec(`INSERT INTO usage_render_fence_drops (session_id, turn_id, dropped_class, last_at_unix_nano)
		VALUES ('s-prune-all', 'turn-old', 'x', ?), ('s-prune-all', 'turn-new', 'x', ?)`, old, current)

	stats, err := store.PruneDetailBefore(context.Background(), PruneOptions{
		Cutoff:    now.Add(-30 * 24 * time.Hour),
		BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if stats.Requests != 2 || stats.ToolCalls != 1 || stats.Turns != 1 ||
		stats.Subagents != 1 || stats.Routes != 1 || stats.LSPRequests != 1 ||
		stats.RenderFenceDrops != 1 || !stats.StatsRebuilt {
		t.Fatalf("prune stats = %+v", stats)
	}

	for table, want := range map[string]int{
		"usage_requests":           1,
		"usage_tool_calls":         1,
		"usage_turns":              1,
		"usage_subagents":          1,
		"usage_routes":             1,
		"usage_lsp_requests":       1,
		"usage_render_fence_drops": 1,
	} {
		var got int
		if err := store.readDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
	var requests int
	var tokens int64
	if err := store.readDB().QueryRow(
		`SELECT c_total_requests, c_total_tokens FROM usage_sessions WHERE session_id = 's-prune-all'`,
	).Scan(&requests, &tokens); err != nil {
		t.Fatalf("read rebuilt stats: %v", err)
	}
	if requests != 1 || tokens != 20 {
		t.Fatalf("rebuilt stats = requests %d tokens %d, want 1/20", requests, tokens)
	}
}

func TestPruneBatchesUseTimeIndexesWithoutSort(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	tables := []struct {
		name     string
		index    string
		timeExpr string
	}{
		{"usage_requests", "idx_usage_requests_started", "started_at_unix_nano"},
		{"usage_tool_calls", "idx_usage_tool_calls_started", "started_at_unix_nano"},
		{"usage_turns", "idx_usage_turns_started", "started_at_unix_nano"},
		{"usage_subagents", "idx_usage_subagents_started", "started_at_unix_nano"},
		{"usage_routes", "idx_usage_routes_time", "recorded_at_unix_nano"},
		{"usage_lsp_requests", "idx_usage_lsp_requests_time", "started_unix_nano"},
		{"usage_render_fence_drops", "idx_usage_render_fence_drops_time", "last_at_unix_nano"},
	}
	for _, table := range tables {
		t.Run(table.name, func(t *testing.T) {
			plan := explainPlanText(t, store,
				pruneRowsBeforeSQL(table.name, table.index, table.timeExpr),
				int64(1), 2000)
			if !strings.Contains(plan, table.index) {
				t.Fatalf("prune plan does not use %s:\n%s", table.index, plan)
			}
			if strings.Contains(plan, "TEMP B-TREE") {
				t.Fatalf("prune plan sorts rows:\n%s", plan)
			}
		})
	}
}
