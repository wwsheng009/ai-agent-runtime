package usageanalytics

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// explainPlanText 读取 EXPLAIN QUERY PLAN 的 detail 列并拼接。
func explainPlanText(t *testing.T, store *Store, sqlText string, args ...interface{}) string {
	t.Helper()
	rows, ok, err := store.query("EXPLAIN QUERY PLAN " + sqlText, args...)
	require.NoError(t, err)
	require.True(t, ok)
	defer rows.Close()
	details := make([]string, 0, 8)
	for rows.Next() {
		var (
			id     int
			parent int
			notUsd int
			detail string
		)
		require.NoError(t, rows.Scan(&id, &parent, &notUsd, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(details, "\n")
}

// TestStatsReadPathPlanDoesNotScanUsageRequests 锁定 §12 验收标准 1：
// 首屏会话级读路径（列表/汇总/维度）的查询计划不得出现 usage_requests 扫描。
func TestStatsReadPathPlanDoesNotScanUsageRequests(t *testing.T) {
	now := time.Date(2026, 9, 17, 19, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()
	require.True(t, store.StatsReady(), "v3 库必须启用预聚合读路径")

	startedAt := now.Add(-time.Second)
	publishRequestStarted(bus, "sess-plan", "req-plan", "trace-plan", "turn-plan", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-plan", "req-plan", usagePayload(10, 1, 11, 0))

	cases := []struct {
		name string
		sql  string
	}{
		{"list-count", "SELECT COUNT(*) FROM (" + sessionStatsSelect + "1=1)"},
		{"list-page", sessionStatsSelect + "1=1 ORDER BY session_start DESC, s.session_id ASC LIMIT 50 OFFSET 0"},
		{"totals", "SELECT COUNT(*) FROM (" + sessionStatsSelect + "1=1)"},
		{"group-by", "SELECT provider, COUNT(*) FROM (" + sessionStatsSelect + "1=1) GROUP BY provider"},
		{"dimensions", "SELECT DISTINCT provider AS value FROM (" + sessionStatsSelect +
			"1=1) WHERE value IS NOT NULL AND TRIM(value) <> '' ORDER BY value ASC LIMIT 1000"},
	}
	for _, tc := range cases {
		plan := explainPlanText(t, store, tc.sql)
		require.NotContains(t, plan, "usage_requests",
			"%s 的查询计划仍在扫描 usage_requests:\n%s", tc.name, plan)
	}

	// 逃生开关回退的旧路径必须仍然扫描 usage_requests（对照组，证明差异真实存在）。
	t.Setenv(EnvDisableStats, "1")
	legacyPlan := explainPlanText(t, store, "SELECT COUNT(*) FROM ("+sessionSelect+"1=1)")
	require.Contains(t, legacyPlan, "usage_requests", "旧路径本应聚合原始表:\n%s", legacyPlan)
}

// TestRootScopeQueriesUseRootIndex 锁定 schema v9 性能约束：父会话明细/缓存按
// root 展开时命中 idx_usage_requests_root_started（非 partial），不退化为全表扫描。
func TestRootScopeQueriesUseRootIndex(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	startedAt := now.Add(-time.Second)
	publishRequestStarted(bus, "sess-root-plan", "req-plan", "trace-plan", "turn-plan", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-root-plan", "req-plan", usagePayload(10, 1, 11, 0))
	publishChildRequest(t, bus, "subagent_plan_child", "req-plan-child", "sess-root-plan", "sess-root-plan", "task-plan", now, usagePayload(5, 1, 6, 0))

	cases := []struct {
		name string
		sql  string
		args []interface{}
	}{
		{"session-steps", `SELECT llm_request_id, record_json FROM usage_requests WHERE root_session_id = ? ORDER BY started_at_unix_nano ASC, step ASC, llm_request_id ASC LIMIT 20001`, []interface{}{"sess-root-plan"}},
		{"cache-requests", `SELECT record_json FROM usage_requests WHERE root_session_id = ? ORDER BY started_at_unix_nano DESC, llm_request_id DESC LIMIT 50`, []interface{}{"sess-root-plan"}},
		{"cache-scope-or", `SELECT COUNT(*) FROM usage_requests WHERE (session_id = ? OR root_session_id = ?)`, []interface{}{"sess-root-plan", "sess-root-plan"}},
		{"child-count", `SELECT COUNT(DISTINCT session_id), COUNT(*) FROM usage_requests WHERE root_session_id = ? AND session_id <> ?`, []interface{}{"sess-root-plan", "sess-root-plan"}},
	}
	for _, tc := range cases {
		plan := explainPlanText(t, store, tc.sql, tc.args...)
		require.Contains(t, plan, "idx_usage_requests_root_started",
			"%s 的查询计划未命中 root 索引:\n%s", tc.name, plan)
	}
}
