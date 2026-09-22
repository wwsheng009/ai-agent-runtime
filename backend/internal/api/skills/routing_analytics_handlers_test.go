package skills

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestAnalyticsHandlersRoutingObservability 覆盖 /api/runtime/analytics/routing
// 与 /routing/events：空库 → 200 + 空桶/空明细、totals 精确、scope/warnings_only
// 过滤、明细分页与时间参数非法 400。
func TestAnalyticsHandlersRoutingObservability(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	sessionID := "20260922_120000.000_routing1"
	handler := newRuntimeLogTestHandler()
	handler.SetUsageAnalyticsDBPath(dbPath)
	t.Cleanup(detachUsageAnalyticsService)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// 空库：200 + 空桶 + 零值 totals（不 500）。
	emptyStats := getAnalyticsResponse(router, "/api/runtime/analytics/routing")
	require.Equal(t, http.StatusOK, emptyStats.Code)
	require.Contains(t, emptyStats.Body.String(), `"by_scope":[]`)
	require.Contains(t, emptyStats.Body.String(), `"total":0`)
	emptyEvents := getAnalyticsResponse(router, "/api/runtime/analytics/routing/events")
	require.Equal(t, http.StatusOK, emptyEvents.Code)
	require.Contains(t, emptyEvents.Body.String(), `"events":[]`)

	// 播种：子代理开工路由 + 主 Agent 改道/还原/护栏（走真实 EventBus → 采集器）。
	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	started := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	bus.Publish(runtimeevents.Event{
		Type: runtimeevents.EventSubagentRouteResolved, SessionID: sessionID, Timestamp: started,
		Payload: map[string]interface{}{
			"subagent_id": "sub-route", "role": "writer", "goal": "改一个文件", "child_session_id": "child-route",
			"attempt": 1, "max_attempts": 1, "trace_id": "trace-route",
			"difficulty": "hard", "route_provider": "remote", "route_model": "strong-model",
			"route_reasoning_effort": "high", "route_source": "difficulty_level",
			"route_warnings": []interface{}{"provider_fallback_parent"},
			"fallback_used":  true,
		},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimeevents.EventMainAgentRouteApplied, SessionID: sessionID, Timestamp: started.Add(time.Second),
		Payload: map[string]interface{}{
			"trace_id": "trace-route", "step": 2, "reason": "prediction", "source": "predicted",
			"difficulty": "hard", "provider": "remote", "model": "strong-model",
			"reasoning_effort": "high", "route_changed": true,
		},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimeevents.EventMainAgentRouteCleared, SessionID: sessionID, Timestamp: started.Add(2 * time.Second),
		Payload: map[string]interface{}{
			"trace_id": "trace-route", "steps_total": 4, "final_difficulty": "hard",
			"restored_provider": "local", "restored_model": "baseline-model", "restored_effort": "medium",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimeevents.EventMainAgentRouteCostGuardTripped, SessionID: sessionID, Timestamp: started.Add(3 * time.Second),
		Payload: map[string]interface{}{
			"trace_id": "trace-route", "step": 3, "level": "expert", "mode": "downgrade", "trips": 1,
		},
	})

	statsRec := getAnalyticsResponse(router, "/api/runtime/analytics/routing")
	require.Equal(t, http.StatusOK, statsRec.Code)
	stats := decodeAnalyticsPayload(t, statsRec)
	totals := stats["totals"].(map[string]interface{})
	require.EqualValues(t, 4, totals["total"])
	require.EqualValues(t, 1, totals["subagent"])
	require.EqualValues(t, 3, totals["main_agent"])
	require.EqualValues(t, 2, totals["applied"])
	require.EqualValues(t, 1, totals["cleared"])
	require.EqualValues(t, 1, totals["warnings"])
	require.EqualValues(t, 1, totals["route_changed"])
	require.EqualValues(t, 1, totals["fallback_used"])
	require.EqualValues(t, 1, totals["distinct_sessions"])
	require.EqualValues(t, false, stats["sampled"])

	requireBucketCount(t, analyticsRows(t, stats, "by_scope"), "main_agent", 3)
	requireBucketCount(t, analyticsRows(t, stats, "by_scope"), "subagent", 1)
	requireBucketCount(t, analyticsRows(t, stats, "by_provider"), "remote", 2)
	requireBucketCount(t, analyticsRows(t, stats, "by_provider"), "local", 1)
	requireBucketCount(t, analyticsRows(t, stats, "warnings"), "provider_fallback_parent", 1)
	requireBucketCount(t, analyticsRows(t, stats, "by_reason"), "cost_guard_tripped", 1)

	// 过滤：scope=subagent / warnings_only=true。
	scoped := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/routing?scope=subagent"))
	require.EqualValues(t, 1, scoped["totals"].(map[string]interface{})["total"])
	warnOnly := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/routing?warnings_only=true"))
	require.EqualValues(t, 1, warnOnly["totals"].(map[string]interface{})["total"])
	bySession := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/routing?session=other-session"))
	require.EqualValues(t, 0, bySession["totals"].(map[string]interface{})["total"])

	// 明细：时间倒序 + limit 分页 + 总数。
	eventsPayload := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/routing/events?limit=2"))
	require.EqualValues(t, 4, eventsPayload["count"])
	require.EqualValues(t, 2, eventsPayload["limit"])
	events := analyticsRows(t, eventsPayload, "events")
	require.Len(t, events, 2)
	first := events[0].(map[string]interface{})
	require.Equal(t, "cost_guard_tripped", first["reason"])
	require.Equal(t, "warning", first["kind"])
	require.Equal(t, "expert", first["difficulty"])

	subagentEvents := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/routing/events?scope=sub"))
	rows := analyticsRows(t, subagentEvents, "events")
	require.Len(t, rows, 1)
	require.Equal(t, "sub-route", rows[0].(map[string]interface{})["agent_id"])
	require.Equal(t, "改一个文件", rows[0].(map[string]interface{})["goal"])
	require.Equal(t, true, rows[0].(map[string]interface{})["fallback_used"])

	// 非法时间参数：400（与既有 /analytics/* 一致）。
	require.Equal(t, http.StatusBadRequest, getAnalyticsResponse(router, "/api/runtime/analytics/routing?to=not-a-time").Code)
	require.Equal(t, http.StatusBadRequest, getAnalyticsResponse(router, "/api/runtime/analytics/routing/events?from=not-a-time").Code)
}

// requireBucketCount 断言维度桶存在且计数正确。
func requireBucketCount(t *testing.T, rows []interface{}, key string, want int) {
	t.Helper()
	for _, row := range rows {
		item := row.(map[string]interface{})
		if item["key"] == key {
			require.EqualValues(t, want, item["count"])
			return
		}
	}
	t.Fatalf("缺少桶 %s（实际 %#v）", key, rows)
}
