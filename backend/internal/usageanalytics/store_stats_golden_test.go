package usageanalytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStatsGoldenMatchesLegacyAggregation 锁定 §11.1 golden 测试：同一 fixture
// 下，「旧 sessionSelect 聚合结果」与「新预聚合列」在列表/汇总/维度三路逐字段一致。
// 通过逃生开关在同一 store 上切换两条读路径，原始数据完全相同。
func TestStatsGoldenMatchesLegacyAggregation(t *testing.T) {
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	lookup := testLookup{meta: map[string]SessionMeta{
		"sess-a": {Title: "A", ProjectPath: "/work/p1", WorkingDirectory: "/work/p1"},
		"sess-b": {Title: "B", ProjectPath: "/work/p2", WorkingDirectory: "/work/p2"},
		"sess-c": {Title: "C", ProjectPath: "/work/p1", WorkingDirectory: "/work/p1"},
	}}
	service, bus := newTestService(t, lookup, now)
	store := service.Store()
	require.True(t, store.StatsReady())

	day1 := now.Add(-25 * time.Hour)
	day2 := now.Add(-5 * time.Hour)

	// sess-a：两个 turn，一成功一失败（失败请求无 usage）。
	publishRequestStarted(bus, "sess-a", "req-a1", "trace-a1", "turn-a1", 1, day1, "acme", "m1")
	publishRequestFinished(bus, "sess-a", "req-a1", usagePayload(100, 10, 110, 30))
	publishRequestStarted(bus, "sess-a", "req-a2", "trace-a2", "turn-a2", 1, day1.Add(time.Minute), "acme", "m1")
	publishRequestFinished(bus, "sess-a", "req-a2", map[string]interface{}{"success": false})
	// sess-b：同一 turn 两个成功请求（跨 provider/model）。
	publishRequestStarted(bus, "sess-b", "req-b1", "trace-b1", "turn-b1", 1, day2, "beta", "m2")
	publishRequestFinished(bus, "sess-b", "req-b1", usagePayload(200, 20, 220, 0))
	publishRequestStarted(bus, "sess-b", "req-b2", "trace-b1", "turn-b1", 2, day2.Add(time.Minute), "beta", "m2")
	publishRequestFinished(bus, "sess-b", "req-b2", usagePayload(10, 1, 11, 0))
	// sess-c：成功但 usage 缺失（usage_available=0）。
	publishRequestStarted(bus, "sess-c", "req-c1", "trace-c1", "turn-c1", 1, day1.Add(2*time.Hour), "acme", "m1")
	publishRequestFinished(bus, "sess-c", "req-c1", map[string]interface{}{"total_tokens_hint": 5})

	compareList := func(legacy, stats ListResult) {
		t.Helper()
		legacy.GeneratedAt, stats.GeneratedAt = time.Time{}, time.Time{}
		require.Equal(t, legacy, stats)
	}
	compareSummary := func(legacy, stats SummaryResult) {
		t.Helper()
		legacy.GeneratedAt, stats.GeneratedAt = time.Time{}, time.Time{}
		require.Equal(t, legacy, stats)
	}

	cases := []struct {
		name string
		q    Query
	}{
		{"all", Query{}},
		{"time-window", Query{From: day1.Add(-time.Hour), To: day2.Add(time.Hour)}},
		{"provider-filter", Query{Provider: "beta"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvDisableStats, "1")
			legacyList, err := store.ListSessions(tc.q)
			require.NoError(t, err)
			legacySummary, err := store.Summarize(Query{GroupBy: "day", From: tc.q.From, To: tc.q.To, Provider: tc.q.Provider})
			require.NoError(t, err)

			t.Setenv(EnvDisableStats, "")
			statsList, err := store.ListSessions(tc.q)
			require.NoError(t, err)
			statsSummary, err := store.Summarize(Query{GroupBy: "day", From: tc.q.From, To: tc.q.To, Provider: tc.q.Provider})
			require.NoError(t, err)

			compareList(legacyList, statsList)
			compareSummary(legacySummary, statsSummary)
		})
	}

	// 维度：5 个 distinct 结果的顺序与取值必须一致。
	t.Setenv(EnvDisableStats, "1")
	legacyDimensions, err := store.Dimensions(Query{})
	require.NoError(t, err)
	t.Setenv(EnvDisableStats, "")
	statsDimensions, err := store.Dimensions(Query{})
	require.NoError(t, err)
	legacyDimensions.GeneratedAt, statsDimensions.GeneratedAt = time.Time{}, time.Time{}
	require.Equal(t, legacyDimensions, statsDimensions)
	require.Equal(t, []string{"acme", "beta"}, statsDimensions.Providers)
	require.Equal(t, []string{"/work/p1", "/work/p2"}, statsDimensions.Directories)
}

// TestStatsGoldenGroupByDimensions 锁定 provider/model/directory/project/status
// 五种 GROUP BY 的逐字段一致性。
func TestStatsGoldenGroupByDimensions(t *testing.T) {
	now := time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)
	lookup := testLookup{meta: map[string]SessionMeta{
		"sess-x": {ProjectPath: "/proj/x", WorkingDirectory: "/work/x", Status: SessionStatusCompleted},
	}}
	service, bus := newTestService(t, lookup, now)
	store := service.Store()

	started := now.Add(-2 * time.Hour)
	publishRequestStarted(bus, "sess-x", "req-x1", "trace-x1", "turn-x1", 1, started, "acme", "m1")
	publishRequestFinished(bus, "sess-x", "req-x1", usagePayload(60, 6, 66, 0))
	publishSessionEnd(bus, "sess-x")

	for _, groupBy := range []string{"day", "provider", "model", "directory", "project", "status"} {
		t.Run(groupBy, func(t *testing.T) {
			t.Setenv(EnvDisableStats, "1")
			legacy, err := store.Summarize(Query{GroupBy: groupBy})
			require.NoError(t, err)
			t.Setenv(EnvDisableStats, "")
			stats, err := store.Summarize(Query{GroupBy: groupBy})
			require.NoError(t, err)
			legacy.GeneratedAt, stats.GeneratedAt = time.Time{}, time.Time{}
			require.Equal(t, legacy, stats)
		})
	}
}
