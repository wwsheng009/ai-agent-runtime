package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestQueryListTimeFilterAndPaging 验证时间过滤按会话开始时间生效、
// 分页 total 为命中总数（LIMIT/OFFSET 由 SQL 完成）。
func TestQueryListTimeFilterAndPaging(t *testing.T) {
	day1 := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	lookup := testLookup{meta: map[string]SessionMeta{
		"sess-a": {ProjectPath: "/work/p1", WorkingDirectory: "/work/p1"},
		"sess-b": {ProjectPath: "/work/p2", WorkingDirectory: "/work/p2"},
		"sess-c": {ProjectPath: "/work/p2", WorkingDirectory: "/work/p2"},
	}}
	service, bus := newTestService(t, lookup, day2.Add(time.Minute))

	publishRequestStarted(bus, "sess-a", "req-a", "trace-a", "turn-a", 1, day1, "acme", "m1")
	publishRequestFinished(bus, "sess-a", "req-a", usagePayload(100, 20, 120, 0))
	publishRequestStarted(bus, "sess-b", "req-b", "trace-b", "turn-b", 1, day2, "beta", "m2")
	publishRequestFinished(bus, "sess-b", "req-b", usagePayload(10, 2, 12, 0))

	all, err := service.ListSessions(Query{})
	require.NoError(t, err)
	require.Equal(t, 2, all.Total)
	require.Equal(t, 2, all.Scanned, "scanned 语义 = 命中总数")
	require.False(t, all.Partial)
	require.Empty(t, all.PartialReasons)

	fromDay2, err := service.ListSessions(Query{From: day2})
	require.NoError(t, err)
	require.Equal(t, 1, fromDay2.Total)
	require.Len(t, fromDay2.Sessions, 1)
	require.Equal(t, "sess-b", fromDay2.Sessions[0].SessionID)

	toDay2, err := service.ListSessions(Query{To: day2})
	require.NoError(t, err)
	require.Equal(t, 1, toDay2.Total)
	require.Equal(t, "sess-a", toDay2.Sessions[0].SessionID)

	page1, err := service.ListSessions(Query{Limit: 1, Offset: 0})
	require.NoError(t, err)
	require.Equal(t, 2, page1.Total)
	require.Equal(t, 2, page1.Scanned)
	require.Equal(t, 1, page1.Count)
	require.Equal(t, 1, page1.Limit)
	require.Len(t, page1.Sessions, 1)
	require.Equal(t, "sess-b", page1.Sessions[0].SessionID, "默认按会话开始时间倒序")

	page2, err := service.ListSessions(Query{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Equal(t, 2, page2.Total)
	require.Equal(t, 1, page2.Count)
	require.Equal(t, "sess-a", page2.Sessions[0].SessionID)

	outOfRange, err := service.ListSessions(Query{Limit: 1, Offset: 5})
	require.NoError(t, err)
	require.Equal(t, 2, outOfRange.Total)
	require.Equal(t, 0, outOfRange.Count)
	require.Empty(t, outOfRange.Sessions)
}

// TestQuerySummarizeGroupByAndDimensions 验证 GROUP BY 聚合与 DISTINCT 维度
// 都在 SQL 侧完成，且维度值去重。
func TestQuerySummarizeGroupByAndDimensions(t *testing.T) {
	day1 := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	lookup := testLookup{meta: map[string]SessionMeta{
		"sess-a": {ProjectPath: "/work/p1", WorkingDirectory: "/work/p1"},
		"sess-b": {ProjectPath: "/work/p2", WorkingDirectory: "/work/p2"},
		"sess-c": {ProjectPath: "/work/p2", WorkingDirectory: "/work/p2"},
	}}
	service, bus := newTestService(t, lookup, day2.Add(time.Minute))

	type seed struct {
		sessionID string
		provider  string
		model     string
		project   string
		tokens    int
		at        time.Time
	}
	seeds := []seed{
		{"sess-a", "acme", "m1", "/work/p1", 120, day1},
		{"sess-b", "beta", "m2", "/work/p2", 30, day1},
		{"sess-c", "acme", "m1", "/work/p2", 50, day2},
	}
	for i, item := range seeds {
		requestID := "req-" + string(rune('a'+i))
		publishRequestStarted(bus, item.sessionID, requestID, "trace-"+item.sessionID, "turn-"+item.sessionID, 1, item.at, item.provider, item.model)
		publishRequestFinished(bus, item.sessionID, requestID, usagePayload(item.tokens, 0, item.tokens, 0))
		publishSessionEnd(bus, item.sessionID)
	}

	summary, err := service.Summarize(Query{GroupBy: "provider"})
	require.NoError(t, err)
	require.Equal(t, "provider", summary.GroupBy)
	require.Equal(t, 3, summary.Matched)
	require.Equal(t, 3, summary.Scanned)
	require.False(t, summary.Partial)
	require.Len(t, summary.Groups, 2)

	byKey := map[string]GroupBucket{}
	for _, group := range summary.Groups {
		byKey[group.Key] = group
	}
	require.Contains(t, byKey, "acme")
	require.Equal(t, 2, byKey["acme"].Sessions)
	require.Equal(t, 170, byKey["acme"].TotalTokens)
	require.Equal(t, 2, byKey["acme"].TotalRequests)
	require.Contains(t, byKey, "beta")
	require.Equal(t, 1, byKey["beta"].Sessions)
	require.Equal(t, 30, byKey["beta"].TotalTokens)

	byModel, err := service.Summarize(Query{GroupBy: "model"})
	require.NoError(t, err)
	require.Len(t, byModel.Groups, 2)

	byDay, err := service.Summarize(Query{GroupBy: "day"})
	require.NoError(t, err)
	require.Len(t, byDay.Groups, 2)

	filtered, err := service.Summarize(Query{Provider: "acme"})
	require.NoError(t, err)
	require.Equal(t, 2, filtered.Matched)
	require.Equal(t, 2, filtered.Scanned)

	byProject, err := service.Summarize(Query{Project: "/work/p2"})
	require.NoError(t, err)
	require.Equal(t, 2, byProject.Matched)

	dimensions, err := service.Dimensions(Query{})
	require.NoError(t, err)
	require.Equal(t, []string{"acme", "beta"}, dimensions.Providers)
	require.Equal(t, []string{"m1", "m2"}, dimensions.Models)
	require.Equal(t, []string{"/work/p1", "/work/p2"}, dimensions.Projects)
	require.Equal(t, []string{"completed"}, dimensions.Statuses)
}

// TestQuerySessionUsageDerivesTurns 验证 turns 由 usage_requests 分组派生
// （无独立 turns 表），且明细统计一致。
func TestQuerySessionUsageDerivesTurns(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)

	publishRequestStarted(bus, "sess-turns", "req-1", "trace-1", "turn-1", 1, now.Add(-3*time.Second), "acme", "m1")
	publishRequestFinished(bus, "sess-turns", "req-1", usagePayload(100, 10, 110, 0))
	publishRequestStarted(bus, "sess-turns", "req-2", "trace-1", "turn-1", 2, now.Add(-2*time.Second), "acme", "m1")
	publishRequestFinished(bus, "sess-turns", "req-2", usagePayload(50, 5, 55, 0))
	publishRequestStarted(bus, "sess-turns", "req-3", "trace-2", "turn-2", 1, now.Add(-1*time.Second), "acme", "m1")
	publishRequestFinished(bus, "sess-turns", "req-3", usagePayload(20, 5, 25, 0))

	detail, err := service.SessionUsage("sess-turns")
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, detail.SchemaVersion)
	require.Equal(t, 3, detail.Session.TotalRequests)
	require.Equal(t, 3, detail.StepCount)
	require.Equal(t, 2, detail.Session.TurnCount, "同一 trace 的多次请求归为一个 turn")
	require.False(t, detail.Partial)

	require.Len(t, detail.Turns, 2)
	turnsByTrace := map[string]TurnUsage{}
	totalTokens := 0
	ordinals := map[int]bool{}
	for _, turn := range detail.Turns {
		turnsByTrace[turn.TraceID] = turn
		totalTokens += turn.Usage.TotalTokens
		ordinals[turn.Ordinal] = true
	}
	require.Equal(t, 190, totalTokens)
	require.True(t, ordinals[1] && ordinals[2], "ordinal 应为 1..n")
	require.Equal(t, 2, turnsByTrace["trace-1"].LLMRequests)
	require.Equal(t, 1, turnsByTrace["trace-2"].LLMRequests)
	require.Equal(t, 165, turnsByTrace["trace-1"].Usage.TotalTokens)
	require.Equal(t, 25, turnsByTrace["trace-2"].Usage.TotalTokens)

	_, err = service.SessionUsage("sess-missing")
	require.Error(t, err)
	require.True(t, IsNotFound(err))
}

// TestReadOnlyOpenOfMissingDatabaseReturnsEmpty 验证只读打开不存在的库
// 返回空结果而不是错误。
func TestReadOnlyOpenOfMissingDatabaseReturnsEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "runtime", DefaultDBFileName)
	service, err := Attach(nil, Options{Config: Config{Path: missing, ReadOnly: true}})
	require.NoError(t, err)
	require.NotNil(t, service)
	t.Cleanup(service.Close)

	require.True(t, service.Store().Empty())
	list, err := service.ListSessions(Query{})
	require.NoError(t, err)
	require.Equal(t, 0, list.Total)
	require.Equal(t, 0, list.Scanned)
	require.Empty(t, list.Sessions)
	require.False(t, list.Partial)

	summary, err := service.Summarize(Query{GroupBy: "provider"})
	require.NoError(t, err)
	require.Equal(t, 0, summary.Matched)
	require.Empty(t, summary.Groups)

	dimensions, err := service.Dimensions(Query{})
	require.NoError(t, err)
	require.Empty(t, dimensions.Providers)
	require.Empty(t, dimensions.Models)
	require.Empty(t, dimensions.Directories)
	require.Empty(t, dimensions.Projects)
	require.Empty(t, dimensions.Statuses)

	_, err = service.SessionUsage("sess-anything")
	require.Error(t, err)
	require.True(t, IsNotFound(err))
}
