package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestCollectorIngestsRouteObservability 锁定路由切换观测采集（方案 §观测面）：
// 子代理开工决策（含 attempt 重试轨迹）、主 Agent 改道/还原/护栏信号 → usage_routes，
// 且重复投递幂等合并。
func TestCollectorIngestsRouteObservability(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	started := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	publish := func(eventType string, payload map[string]interface{}, at time.Time) {
		bus.Publish(runtimeevents.Event{
			Type:      eventType,
			SessionID: "session-routes",
			Payload:   payload,
			Timestamp: at,
		})
	}

	// --- 子代理开工路由（attempt=1）---
	subagentRoute := map[string]interface{}{
		"subagent_id":            "sub-1",
		"role":                   "writer",
		"goal":                   "改一个文件",
		"task_type":              "migrate",
		"task_subject":           "把配置迁到新 schema",
		"parent_session_id":      "session-routes",
		"child_session_id":       "child-1",
		"attempt":                1,
		"max_attempts":           2,
		"trace_id":               "trace-routes",
		"difficulty":             "hard",
		"difficulty_source":      "explicit",
		"route_provider":         "remote",
		"route_model":            "strong-model",
		"route_reasoning_effort": "high",
		"route_source":           "difficulty_level",
		"route_warnings":         []interface{}{"provider_fallback_parent"},
		"fallback_used":          true,
		"fallback_reason":        "health_gate",
	}
	publish(runtimeevents.EventSubagentRouteResolved, subagentRoute, started)

	// 幂等：同一行重复投递不新增。
	publish(runtimeevents.EventSubagentRouteResolved, subagentRoute, started.Add(time.Millisecond))
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_routes WHERE scope='subagent'`, 1)

	// 重试（attempt=2）保留独立轨迹。
	retryRoute := map[string]interface{}{
		"subagent_id":            "sub-1",
		"role":                   "writer",
		"parent_session_id":      "session-routes",
		"child_session_id":       "child-2",
		"attempt":                2,
		"max_attempts":           2,
		"trace_id":               "trace-routes",
		"difficulty":             "hard",
		"route_provider":         "local",
		"route_model":            "small-model",
		"route_reasoning_effort": "medium",
		"route_source":           "difficulty_level",
	}
	publish(runtimeevents.EventSubagentRouteResolved, retryRoute, started.Add(time.Second))
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_routes WHERE scope='subagent'`, 2)

	// 重复投递的合并语义：先落一行无 goal / task_type，再带值重投同一自然键 → 补写而非新增。
	mergeRoute := map[string]interface{}{
		"subagent_id":            "sub-2",
		"role":                   "verifier",
		"parent_session_id":      "session-routes",
		"child_session_id":       "child-3",
		"attempt":                1,
		"max_attempts":           1,
		"trace_id":               "trace-routes",
		"difficulty":             "easy",
		"route_provider":         "local",
		"route_model":            "small-model",
		"route_reasoning_effort": "medium",
		"route_source":           "difficulty_level",
	}
	publish(runtimeevents.EventSubagentRouteResolved, mergeRoute, started.Add(2*time.Second))
	mergeRoute["goal"] = "补写目标"
	mergeRoute["task_type"] = "verify"
	mergeRoute["task_subject"] = "复核改动"
	publish(runtimeevents.EventSubagentRouteResolved, mergeRoute, started.Add(2*time.Second))
	mergeRow := queryRow(t, store, `SELECT goal, task_type, task_subject FROM usage_routes WHERE child_session_id='child-3'`)
	if mergeRow[0] != "补写目标" || mergeRow[1] != "verify" || mergeRow[2] != "复核改动" {
		t.Fatalf("重复投递应补写 goal / task_type / task_subject: %v", mergeRow)
	}
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_routes WHERE child_session_id='child-3'`, 1)

	subagentRow := queryRow(t, store, `SELECT session_id, parent_session_id, child_session_id, kind, reason, source, difficulty, difficulty_source, provider, model, reasoning_effort, fallback_used, warning_count, attempt, goal, task_type, task_subject FROM usage_routes WHERE child_session_id='child-1'`)
	if subagentRow[0] != "session-routes" || subagentRow[1] != "session-routes" || subagentRow[2] != "child-1" {
		t.Fatalf("子代理路由行归属不符: %v", subagentRow)
	}
	if subagentRow[3] != RouteKindApplied || subagentRow[4] != routeReasonResolved || subagentRow[5] != "difficulty_level" {
		t.Fatalf("子代理路由行语义不符: %v", subagentRow)
	}
	if subagentRow[6] != "hard" || subagentRow[7] != "explicit" || subagentRow[8] != "remote" || subagentRow[9] != "strong-model" || subagentRow[10] != "high" {
		t.Fatalf("子代理路由行决策不符: %v", subagentRow)
	}
	if subagentRow[11] != int64(1) || subagentRow[12] != int64(1) || subagentRow[13] != int64(1) {
		t.Fatalf("子代理路由行计数不符: %v", subagentRow)
	}
	if subagentRow[14] != "改一个文件" {
		t.Fatalf("子代理路由行 goal 未落列: %v", subagentRow)
	}
	if subagentRow[15] != "migrate" || subagentRow[16] != "把配置迁到新 schema" {
		t.Fatalf("子代理路由行 task_type / task_subject 未落列: %v", subagentRow)
	}
	// 缺省兼容：未携带这些键的事件落空串（与今天逐字节一致）。
	retryGoalRow := queryRow(t, store, `SELECT goal, task_type, task_subject FROM usage_routes WHERE child_session_id='child-2'`)
	if retryGoalRow[0] != "" || retryGoalRow[1] != "" || retryGoalRow[2] != "" {
		t.Fatalf("未携带 goal / task_type / task_subject 的行应为空: %v", retryGoalRow)
	}

	// --- 主 Agent 改道（prediction，带候选链）---
	publish(runtimeevents.EventMainAgentRouteApplied, map[string]interface{}{
		"trace_id":         "trace-main",
		"step":             3,
		"reason":           "prediction",
		"source":           "predicted",
		"difficulty":       "hard",
		"provider":         "remote",
		"model":            "strong-model",
		"reasoning_effort": "high",
		"task_type":        "implement",
		"task_subject":     "接入 task_type 聚合",
		"route_changed":    true,
		"candidates": []interface{}{
			map[string]interface{}{"provider": "remote", "model": "strong-model", "selected": true},
			map[string]interface{}{"provider": "local", "model": "small-model", "selected": false},
		},
	}, started.Add(2*time.Second))

	// --- 主 Agent 还原基线 ---
	publish(runtimeevents.EventMainAgentRouteCleared, map[string]interface{}{
		"trace_id":            "trace-main",
		"steps_total":         5,
		"steps_with_override": 2,
		"final_difficulty":    "hard",
		"cost_guard_trips":    1,
		"restored_provider":   "local",
		"restored_model":      "baseline-model",
		"restored_effort":     "medium",
	}, started.Add(3*time.Second))

	// --- 主 Agent 路由机制异常/护栏 ---
	publish(runtimeevents.EventMainAgentRoutePredictionInvalid, map[string]interface{}{
		"trace_id":   "trace-main",
		"step":       2,
		"difficulty": "impossible",
		"reason":     "invalid_value",
	}, started.Add(4*time.Second))
	publish(runtimeevents.EventMainAgentRouteCostGuardTripped, map[string]interface{}{
		"trace_id": "trace-main",
		"step":     4,
		"level":    "expert",
		"mode":     "downgrade",
		"limit":    3,
		"trips":    1,
	}, started.Add(5*time.Second))

	mainRow := queryRow(t, store, `SELECT kind, reason, step, source, provider, model, route_changed, candidate_count, task_type, task_subject FROM usage_routes WHERE scope='main_agent' AND kind='applied'`)
	if mainRow[0] != RouteKindApplied || mainRow[1] != "prediction" || mainRow[2] != int64(3) {
		t.Fatalf("主 Agent 改道行语义不符: %v", mainRow)
	}
	if mainRow[3] != "predicted" || mainRow[4] != "remote" || mainRow[5] != "strong-model" {
		t.Fatalf("主 Agent 改道行决策不符: %v", mainRow)
	}
	if mainRow[6] != int64(1) || mainRow[7] != int64(2) {
		t.Fatalf("主 Agent 改道行计数不符: %v", mainRow)
	}
	if mainRow[8] != "implement" || mainRow[9] != "接入 task_type 聚合" {
		t.Fatalf("主 Agent 改道行 task_type / task_subject 未落列: %v", mainRow)
	}

	clearedRow := queryRow(t, store, `SELECT kind, reason, step, difficulty, provider, model, reasoning_effort, task_type, task_subject FROM usage_routes WHERE scope='main_agent' AND kind='cleared'`)
	if clearedRow[0] != RouteKindCleared || clearedRow[1] != RouteKindCleared || clearedRow[2] != int64(5) {
		t.Fatalf("主 Agent 还原行语义不符: %v", clearedRow)
	}
	if clearedRow[3] != "hard" || clearedRow[4] != "local" || clearedRow[5] != "baseline-model" || clearedRow[6] != "medium" {
		t.Fatalf("主 Agent 还原行目标不符: %v", clearedRow)
	}
	if clearedRow[7] != "" || clearedRow[8] != "" {
		t.Fatalf("未携带 task_type / task_subject 的还原行应为空: %v", clearedRow)
	}

	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_routes WHERE scope='main_agent' AND kind='warning'`, 2)
	invalidRow := queryRow(t, store, `SELECT reason, step, difficulty, task_type, task_subject FROM usage_routes WHERE scope='main_agent' AND reason='prediction_invalid'`)
	if invalidRow[0] != "prediction_invalid" || invalidRow[1] != int64(2) || invalidRow[2] != "impossible" {
		t.Fatalf("非法上报行不符: %v", invalidRow)
	}
	if invalidRow[3] != "" || invalidRow[4] != "" {
		t.Fatalf("未携带 task_type / task_subject 的护栏行应为空: %v", invalidRow)
	}
	guardRow := queryRow(t, store, `SELECT reason, difficulty, step FROM usage_routes WHERE scope='main_agent' AND reason='cost_guard_tripped'`)
	if guardRow[0] != "cost_guard_tripped" || guardRow[1] != "expert" || guardRow[2] != int64(4) {
		t.Fatalf("成本护栏行不符: %v", guardRow)
	}

	// 全表计数：子代理 3（child-1 / child-2 / child-3 合并行）+ 主 Agent 4（applied/cleared/warning×2）。
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_routes`, 7)
}
