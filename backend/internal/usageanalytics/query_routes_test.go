package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestStoreRouteStatsAndEvents 锁定路由观测查询：全量 totals、维度桶、过滤与
// 明细分页（空库另测）。
func TestStoreRouteStatsAndEvents(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	// 空库：零值 totals + 空桶，不报错。
	empty, err := store.RouteStats(RouteQuery{})
	if err != nil {
		t.Fatalf("空库 RouteStats: %v", err)
	}
	if empty.Totals.Total != 0 || len(empty.ByScope) != 0 || len(empty.ByDifficultySource) != 0 || len(empty.Warnings) != 0 {
		t.Fatalf("空库应为零值: %#v", empty)
	}
	emptyEvents, err := store.RouteEvents(RouteQuery{})
	if err != nil {
		t.Fatalf("空库 RouteEvents: %v", err)
	}
	if emptyEvents.Count != 0 || len(emptyEvents.Events) != 0 {
		t.Fatalf("空库明细应为空: %#v", emptyEvents)
	}

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	started := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	publish := func(eventType string, payload map[string]interface{}, at time.Time) {
		bus.Publish(runtimeevents.Event{
			Type:      eventType,
			SessionID: "session-routes",
			Payload:   payload,
			Timestamp: at,
		})
	}

	publish(runtimeevents.EventSubagentRouteResolved, map[string]interface{}{
		"subagent_id":            "sub-1",
		"role":                   "writer",
		"goal":                   "改一个文件",
		"task_type":              "migrate",
		"task_subject":           "把配置迁到新 schema",
		"parent_session_id":      "session-routes",
		"child_session_id":       "child-1",
		"attempt":                1,
		"max_attempts":           1,
		"trace_id":               "trace-1",
		"difficulty":             "hard",
		"difficulty_source":      "explicit",
		"route_provider":         "remote",
		"route_model":            "strong-model",
		"route_reasoning_effort": "high",
		"route_source":           "difficulty_level",
		"route_warnings":         []interface{}{"provider_fallback_parent"},
		"fallback_used":          true,
		"fallback_reason":        "health_gate",
	}, started)

	publish(runtimeevents.EventMainAgentRouteApplied, map[string]interface{}{
		"trace_id":          "trace-1",
		"step":              3,
		"reason":            "prediction",
		"source":            "predicted",
		"difficulty":        "hard",
		"difficulty_source": "explicit",
		"provider":          "remote",
		"model":             "strong-model",
		"task_type":         "implement",
		"task_subject":      "接入 by_task_type 聚合",
		"route_changed":     true,
		"candidates": []interface{}{
			map[string]interface{}{"provider": "remote"},
			map[string]interface{}{"provider": "local"},
		},
	}, started.Add(time.Second))

	publish(runtimeevents.EventMainAgentRouteCleared, map[string]interface{}{
		"trace_id":          "trace-1",
		"steps_total":       5,
		"final_difficulty":  "hard",
		"restored_provider": "local",
		"restored_model":    "baseline-model",
		"restored_effort":   "medium",
	}, started.Add(2*time.Second))

	publish(runtimeevents.EventMainAgentRoutePredictionInvalid, map[string]interface{}{
		"trace_id":   "trace-1",
		"step":       4,
		"difficulty": "impossible",
		"reason":     "invalid_value",
	}, started.Add(3*time.Second))

	stats, err := store.RouteStats(RouteQuery{})
	if err != nil {
		t.Fatalf("RouteStats: %v", err)
	}
	totals := stats.Totals
	if totals.Total != 4 || totals.MainAgent != 3 || totals.Subagent != 1 {
		t.Fatalf("totals 范围计数不符: %#v", totals)
	}
	if totals.Applied != 2 || totals.Cleared != 1 || totals.Warnings != 1 {
		t.Fatalf("totals 类型计数不符: %#v", totals)
	}
	if totals.RouteChanged != 1 || totals.FallbackUsed != 1 || totals.CandidateTotal != 2 {
		t.Fatalf("totals 改道/回退计数不符: %#v", totals)
	}
	if totals.DistinctSessions != 1 || totals.DistinctModels != 2 {
		t.Fatalf("totals 去重计数不符: %#v", totals)
	}
	if stats.Sampled || stats.SampleSize != 4 {
		t.Fatalf("全量扫描不应标记抽样: sampled=%v size=%d", stats.Sampled, stats.SampleSize)
	}

	assertBucket(t, stats.ByScope, RouteScopeMainAgent, 3)
	assertBucket(t, stats.ByScope, RouteScopeSubagent, 1)
	assertBucket(t, stats.ByKind, RouteKindApplied, 2)
	assertBucket(t, stats.ByKind, RouteKindCleared, 1)
	assertBucket(t, stats.ByKind, RouteKindWarning, 1)
	assertBucket(t, stats.BySource, "difficulty_level", 1)
	assertBucket(t, stats.BySource, "predicted", 1)
	assertBucket(t, stats.ByProvider, "remote", 2)
	assertBucket(t, stats.ByProvider, "local", 1)
	assertBucket(t, stats.ByDifficulty, "hard", 3)
	assertBucket(t, stats.ByDifficulty, "impossible", 1)
	// 难度来源只统计「事件确实携带该字段」的行：cleared / warning 两行未携带，
	// 不得进桶（否则「未记录」会被伪装成某个具体取值）。
	assertBucket(t, stats.ByDifficultySource, "explicit", 2)
	if len(stats.ByDifficultySource) != 1 {
		t.Fatalf("难度来源桶应只有 explicit 一个: %#v", stats.ByDifficultySource)
	}
	assertBucket(t, stats.ByRole, "writer", 1)
	// task_type 是新分类轴：缺省行（cleared / warning）不进桶，桶计数与 ByRole 对照。
	assertBucket(t, stats.ByTaskType, "migrate", 1)
	assertBucket(t, stats.ByTaskType, "implement", 1)
	if len(stats.ByTaskType) != 2 {
		t.Fatalf("task_type 桶应只有 migrate / implement 两个: %#v", stats.ByTaskType)
	}
	assertBucket(t, stats.Warnings, "provider_fallback_parent", 1)

	// 过滤：scope=subagent 只剩 1 行。
	scoped, err := store.RouteStats(RouteQuery{Scope: RouteScopeSubagent})
	if err != nil {
		t.Fatalf("RouteStats(scope): %v", err)
	}
	if scoped.Totals.Total != 1 || scoped.Totals.Subagent != 1 {
		t.Fatalf("scope 过滤不符: %#v", scoped.Totals)
	}
	warnOnly, err := store.RouteStats(RouteQuery{Kind: RouteKindWarning})
	if err != nil {
		t.Fatalf("RouteStats(kind): %v", err)
	}
	if warnOnly.Totals.Total != 1 || warnOnly.Totals.Warnings != 1 {
		t.Fatalf("kind 过滤不符: %#v", warnOnly.Totals)
	}
	byModel, err := store.RouteStats(RouteQuery{Model: "baseline-model"})
	if err != nil {
		t.Fatalf("RouteStats(model): %v", err)
	}
	if byModel.Totals.Total != 1 {
		t.Fatalf("model 过滤不符: %#v", byModel.Totals)
	}
	// 时间窗过滤：只取前 2 秒内的 2 行。
	windowed, err := store.RouteStats(RouteQuery{From: started, To: started.Add(time.Second)})
	if err != nil {
		t.Fatalf("RouteStats(window): %v", err)
	}
	if windowed.Totals.Total != 2 {
		t.Fatalf("时间窗过滤不符: %#v", windowed.Totals)
	}

	// 明细：时间倒序 + 分页 + 总数。
	events, err := store.RouteEvents(RouteQuery{Limit: 2})
	if err != nil {
		t.Fatalf("RouteEvents: %v", err)
	}
	if events.Count != 4 || len(events.Events) != 2 || events.Limit != 2 || events.Offset != 0 {
		t.Fatalf("明细分页不符: %#v", events)
	}
	if events.Events[0].Kind != RouteKindWarning || events.Events[0].Step != 4 {
		t.Fatalf("明细应按时间倒序: %#v", events.Events[0])
	}
	first := events.Events[0]
	if first.Scope != RouteScopeMainAgent || first.Reason != "prediction_invalid" || first.Difficulty != "impossible" {
		t.Fatalf("明细行语义不符: %#v", first)
	}
	if first.Goal != "" {
		t.Fatalf("主 Agent 行不应携带 goal: %#v", first)
	}
	if first.TaskType != "" || first.TaskSubject != "" {
		t.Fatalf("未携带 task_type / task_subject 的护栏行应为空: %#v", first)
	}
	second, err := store.RouteEvents(RouteQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("RouteEvents(offset): %v", err)
	}
	if second.Count != 4 || len(second.Events) != 2 || second.Offset != 2 {
		t.Fatalf("明细 offset 不符: %#v", second)
	}
	subagentEvents, err := store.RouteEvents(RouteQuery{Scope: RouteScopeSubagent})
	if err != nil {
		t.Fatalf("RouteEvents(scope): %v", err)
	}
	if len(subagentEvents.Events) != 1 {
		t.Fatalf("子代理明细应为 1 行: %#v", subagentEvents.Events)
	}
	row := subagentEvents.Events[0]
	if row.AgentID != "sub-1" || row.Role != "writer" || row.ChildSessionID != "child-1" {
		t.Fatalf("子代理明细归属不符: %#v", row)
	}
	if row.Difficulty != "hard" || row.DifficultySource != "explicit" {
		t.Fatalf("子代理明细难度来源不符: %#v", row)
	}
	if row.Goal != "改一个文件" {
		t.Fatalf("子代理明细 goal 不符: %#v", row)
	}
	if row.TaskType != "migrate" || row.TaskSubject != "把配置迁到新 schema" {
		t.Fatalf("子代理明细 task_type / task_subject 不符: %#v", row)
	}
	if row.RouteChanged != nil || row.FallbackUsed == nil || !*row.FallbackUsed {
		t.Fatalf("三态字段不符: %#v", row)
	}
	if len(row.Warnings) != 1 || row.Warnings[0] != "provider_fallback_parent" {
		t.Fatalf("明细告警不符: %#v", row)
	}
}

// assertBucket 断言维度桶存在且计数正确。
func assertBucket(t *testing.T, buckets []RouteBucket, key string, want int) {
	t.Helper()
	for _, bucket := range buckets {
		if bucket.Key == key {
			if bucket.Count != want {
				t.Fatalf("桶 %s 计数期望 %d，实际 %d", key, want, bucket.Count)
			}
			return
		}
	}
	t.Fatalf("缺少桶 %s（实际 %#v）", key, buckets)
}

// TestServiceRouteStatsDegradedBuckets 锁定只读降级路径（无 store）的空桶契约：
// 每个维度桶都必须是空数组而非 nil——nil 会被序列化成 null，前端拿到的是
// 「字段缺失」而不是「没有数据」。新增维度桶时漏配这里会立刻失败。
func TestServiceRouteStatsDegradedBuckets(t *testing.T) {
	service := &Service{}
	stats, err := service.RouteStats(RouteQuery{})
	if err != nil {
		t.Fatalf("降级 RouteStats: %v", err)
	}
	if stats.Totals.Total != 0 {
		t.Fatalf("降级 totals 应为零值: %#v", stats.Totals)
	}
	buckets := map[string][]RouteBucket{
		"by_scope":             stats.ByScope,
		"by_kind":              stats.ByKind,
		"by_reason":            stats.ByReason,
		"by_source":            stats.BySource,
		"by_provider":          stats.ByProvider,
		"by_model":             stats.ByModel,
		"by_difficulty":        stats.ByDifficulty,
		"by_difficulty_source": stats.ByDifficultySource,
		"by_role":              stats.ByRole,
		"by_task_type":         stats.ByTaskType,
		"warnings":             stats.Warnings,
	}
	for name, bucket := range buckets {
		if bucket == nil {
			t.Fatalf("降级路径的桶 %s 必须是空数组而非 nil", name)
		}
		if len(bucket) != 0 {
			t.Fatalf("降级路径的桶 %s 应为空: %#v", name, bucket)
		}
	}
}

// TestStoreRouteStatsBucketsAreExactBeyondSampleCap 锁定「分布桶不再抽样」：
// 早期实现按 recorded_at 倒序只扫前 2000 行再在 Go 侧聚合，超过上限时桶只覆盖
// 最近 2000 行，最旧的告警/难度来源会整类消失（与全量 totals 口径不一致）。
// 这里播 2100 行，断言最旧一行仍在桶里、且桶计数之和等于 totals。
func TestStoreRouteStatsBucketsAreExactBeyondSampleCap(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	// 单条递归 CTE 批量播种 2100 行 = 旧上限 2000 + 100（最旧 100 行是旧实现会漏掉的部分）。
	// n=1 是最旧行，携带告警与难度来源。
	if err := store.execWithLockRetry(`
INSERT INTO usage_routes (route_event_id, session_id, scope, kind, reason, difficulty, difficulty_source, recorded_at_unix_nano, warnings_json)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 2100)
SELECT 'bulk-' || n,
       'session-bulk',
       'subagent',
       'applied',
       'route_resolved',
       'hard',
       CASE WHEN n = 1 THEN 'inferred' ELSE 'explicit' END,
       n,
       CASE WHEN n = 1 THEN '["legacy_warning"]' ELSE '' END
FROM seq`); err != nil {
		t.Fatalf("播种 2100 行: %v", err)
	}

	stats, err := store.RouteStats(RouteQuery{})
	if err != nil {
		t.Fatalf("RouteStats: %v", err)
	}
	if stats.Totals.Total != 2100 {
		t.Fatalf("totals 应为 2100: %#v", stats.Totals)
	}
	if stats.Sampled || stats.SampleSize != 2100 {
		t.Fatalf("桶已是全量精确，不应标记抽样: sampled=%v size=%d", stats.Sampled, stats.SampleSize)
	}
	assertBucket(t, stats.ByScope, RouteScopeSubagent, 2100)
	// 最旧一行（旧实现落在 2000 行窗口之外）的维度必须仍被统计。
	assertBucket(t, stats.ByDifficultySource, "inferred", 1)
	assertBucket(t, stats.Warnings, "legacy_warning", 1)
	// 桶计数之和 == totals：口径一致的直接判据。
	bucketTotal := 0
	for _, bucket := range stats.ByScope {
		bucketTotal += bucket.Count
	}
	if bucketTotal != stats.Totals.Total {
		t.Fatalf("桶计数之和 %d != totals %d", bucketTotal, stats.Totals.Total)
	}
}
