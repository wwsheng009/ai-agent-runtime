package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// staticSessionMeta 是测试用会话元数据来源（best-effort 补齐 title/project）。
type staticSessionMeta struct {
	meta map[string]usageanalytics.SessionMeta
}

func (s staticSessionMeta) SessionMeta(sessionID string) (usageanalytics.SessionMeta, bool) {
	meta, ok := s.meta[sessionID]
	return meta, ok
}

// TestAnalyticsHandlersReadFromUsageDB 验证 /api/runtime/analytics/* 全部读
// usage_analytics.sqlite（由 runtime EventBus 实时写入），不再扫描 chat-logs。
func TestAnalyticsHandlersReadFromUsageDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	sessionID := "20260727_080000.000_analytics1"
	project := filepath.ToSlash(filepath.Join(t.TempDir(), "workspace"))
	started := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)

	handler := newRuntimeLogTestHandler()
	handler.SetUsageAnalyticsDBPath(dbPath)
	t.Cleanup(detachUsageAnalyticsService)

	bus := handler.getRuntimeEventBus()
	service := attachUsageAnalyticsSingleton(dbPath, bus, staticSessionMeta{meta: map[string]usageanalytics.SessionMeta{
		sessionID: {Title: "Analyze runtime usage", ProjectPath: project, WorkingDirectory: project},
	}}, nil)
	require.NotNil(t, service)

	// 事件实时写入（等价于 runtime 主链上的 llm.request.* / session_end）。
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventLLMRequestStarted,
		TraceID:   "trace-analytics",
		SessionID: sessionID,
		Timestamp: started,
		Payload: map[string]interface{}{
			"llm_request_id": "req-analytics",
			"trace_id":       "trace-analytics",
			"turn_id":        "turn-analytics",
			"step":           1,
			"provider":       "openai",
			"model":          "gpt-5",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventLLMRequestFinished,
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"llm_request_id":          "req-analytics",
			"success":                 true,
			"usage_prompt_tokens":     20,
			"usage_completion_tokens": 10,
			"usage_total_tokens":      30,
			"context_prompt_tokens":   20000,
			"context_window_tokens":   128000,
			"prompt_budget":           108800,
		},
	})
	bus.Publish(runtimeevents.Event{Type: usageanalytics.EventSessionEnd, SessionID: sessionID})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "127.0.0.1:4321"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	listRec := get("/api/runtime/analytics/sessions?limit=10")
	require.Equal(t, http.StatusOK, listRec.Code)
	var listPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listPayload))
	require.Equal(t, "runtime.analytics.v1", listPayload["schema_version"])
	require.EqualValues(t, 1, listPayload["total"])
	require.EqualValues(t, 1, listPayload["scanned"], "scanned = 命中总数（不再扫盘）")
	require.NotNil(t, listPayload["coverage"])
	sessions := listPayload["sessions"].([]interface{})
	require.Len(t, sessions, 1)
	require.Equal(t, project, sessions[0].(map[string]interface{})["project"])
	require.Equal(t, "Analyze runtime usage", sessions[0].(map[string]interface{})["title"])

	summaryRec := get("/api/runtime/analytics/summary?group_by=provider")
	require.Equal(t, http.StatusOK, summaryRec.Code)
	var summaryPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(summaryRec.Body.Bytes(), &summaryPayload))
	require.Equal(t, "provider", summaryPayload["group_by"])
	require.EqualValues(t, 1, summaryPayload["matched"])
	require.EqualValues(t, 1, summaryPayload["scanned"])

	projectRec := get("/api/runtime/analytics/summary?group_by=project&project=" + url.QueryEscape(project))
	require.Equal(t, http.StatusOK, projectRec.Code)
	var projectPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(projectRec.Body.Bytes(), &projectPayload))
	require.Equal(t, "project", projectPayload["group_by"])
	require.EqualValues(t, 1, projectPayload["matched"])

	dimensionsRec := get("/api/runtime/analytics/dimensions")
	require.Equal(t, http.StatusOK, dimensionsRec.Code)
	var dimensionsPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(dimensionsRec.Body.Bytes(), &dimensionsPayload))
	require.Contains(t, dimensionsPayload["projects"], project)
	require.Contains(t, dimensionsPayload["providers"], "openai")
	require.Contains(t, dimensionsPayload["models"], "gpt-5")

	detailRec := get("/api/runtime/analytics/sessions/" + sessionID + "/usage")
	require.Equal(t, http.StatusOK, detailRec.Code)
	var detailPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(detailRec.Body.Bytes(), &detailPayload))
	require.EqualValues(t, 1, detailPayload["step_count"])
	require.Len(t, detailPayload["turns"], 1)

	// 上下文事实（出站 token / 窗口 / 预算）必须出现在 HTTP 明细里——工作台
	// composer 的"上下文用量"面板直接读这三个字段，缺一个就退回"未知"。
	steps := detailPayload["steps"].([]interface{})
	require.Len(t, steps, 1)
	firstStep := steps[0].(map[string]interface{})
	require.EqualValues(t, 20000, firstStep["context_prompt_tokens"])
	require.EqualValues(t, 128000, firstStep["context_window_tokens"])
	require.EqualValues(t, 108800, firstStep["prompt_budget"])

	turnsRec := get("/api/runtime/analytics/sessions/" + sessionID + "/turns")
	require.Equal(t, http.StatusOK, turnsRec.Code)
	var turnsPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(turnsRec.Body.Bytes(), &turnsPayload))
	require.EqualValues(t, 1, turnsPayload["count"])

	missingRec := get("/api/runtime/analytics/sessions/20260727_080000.000_missing/usage")
	require.Equal(t, http.StatusNotFound, missingRec.Code)

	// /overview 现为一次性引导载荷（方案 Phase 2）：sessions/summary/dimensions
	// 一次取齐，顶层 matched 兼容旧断言。
	overviewRec := get("/api/runtime/analytics/overview")
	require.Equal(t, http.StatusOK, overviewRec.Code)
	var overviewPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(overviewRec.Body.Bytes(), &overviewPayload))
	require.EqualValues(t, 1, overviewPayload["matched"])
	overviewSessions, ok := overviewPayload["sessions"].(map[string]interface{})
	require.True(t, ok, "overview 必须包含 sessions 对象")
	require.EqualValues(t, 1, overviewSessions["total"])
	overviewSummary, ok := overviewPayload["summary"].(map[string]interface{})
	require.True(t, ok, "overview 必须包含 summary 对象")
	require.EqualValues(t, 1, overviewSummary["matched"])
	overviewDimensions, ok := overviewPayload["dimensions"].(map[string]interface{})
	require.True(t, ok, "overview 必须包含 dimensions 对象")
	require.Contains(t, overviewDimensions["projects"], project)
	require.Equal(t, "runtime.analytics.v1", overviewPayload["schema_version"])
}

// TestParseAnalyticsQueryTreatsDateEndAsInclusiveDay 验证查询参数解析
// （日期 to 按整天包含），DB-only 之后语义不变。
func TestParseAnalyticsQueryTreatsDateEndAsInclusiveDay(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?from=2026-07-26&to=2026-07-27&project=C%3A%5Cwork", nil)
	query, err := parseAnalyticsQuery(req)
	require.NoError(t, err)
	require.Equal(t, "2026-07-26", query.From.Format("2006-01-02"))
	require.Equal(t, "2026-07-28", query.To.Format("2006-01-02"))
	require.Equal(t, `C:\work`, query.Project)
}

// ============================================================================
// 批次 3.1：工具 / 子代理 / 失败模式增量端点。
// 统一请求助手：与既有用例同风格（本机 RemoteAddr + mux 路由直连）。
// ============================================================================

func getAnalyticsResponse(router *mux.Router, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeAnalyticsPayload(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload
}

func analyticsRows(t *testing.T, payload map[string]interface{}, key string) []interface{} {
	t.Helper()
	require.NotNil(t, payload[key], "%s 必须是数组（空库为空数组而非 null）", key)
	rows, ok := payload[key].([]interface{})
	require.True(t, ok, "%s 必须是数组", key)
	return rows
}

// publishAnalyticsToolCall 写入一次工具调用（requested 骨架 + completed 终值）。
func publishAnalyticsToolCall(
	bus *runtimeevents.Bus,
	sessionID, toolCallID, toolName, outcome, errorCode string,
	startedAt time.Time,
	durationMS int64,
) {
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventToolRequested,
		SessionID: sessionID,
		TraceID:   "trace-" + toolCallID,
		Timestamp: startedAt,
		Payload: map[string]interface{}{
			"tool_call_id": toolCallID,
			"turn_id":      "turn-tools",
			"step":         1,
			"logical_tool": toolName,
			"source":       "builtin",
			"kind":         "read",
		},
	})
	payload := map[string]interface{}{
		"tool_call_id": toolCallID,
		"turn_id":      "turn-tools",
		"step":         1,
		"logical_tool": toolName,
		"outcome":      outcome,
		"ok":           outcome == "success",
		"duration_ms":  durationMS,
	}
	if errorCode != "" {
		payload["error_code"] = errorCode
	}
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventToolCompleted,
		SessionID: sessionID,
		TraceID:   "trace-" + toolCallID,
		Timestamp: startedAt.Add(time.Duration(durationMS) * time.Millisecond),
		Payload:   payload,
	})
}

func toolStatsByName(t *testing.T, payload map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	index := map[string]map[string]interface{}{}
	for _, row := range analyticsRows(t, payload, "tools") {
		item, ok := row.(map[string]interface{})
		require.True(t, ok)
		name, _ := item["tool_name"].(string)
		index[name] = item
	}
	return index
}

// TestAnalyticsHandlersToolStats 覆盖 /api/runtime/analytics/tools：
// 空库 → 空数组、参数过滤、非法 limit 归一。
func TestAnalyticsHandlersToolStats(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	sessionID := "20260917_100000.000_tools1"
	handler := newRuntimeLogTestHandler()
	handler.SetUsageAnalyticsDBPath(dbPath)
	t.Cleanup(detachUsageAnalyticsService)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// 空库：200 + `"tools":[]`（不是 500，也不是 null）。
	emptyRec := getAnalyticsResponse(router, "/api/runtime/analytics/tools")
	require.Equal(t, http.StatusOK, emptyRec.Code)
	require.Contains(t, emptyRec.Body.String(), `"tools":[]`)
	emptyPayload := decodeAnalyticsPayload(t, emptyRec)
	require.Equal(t, usageanalytics.SchemaVersion, emptyPayload["schema_version"])
	require.Len(t, analyticsRows(t, emptyPayload, "tools"), 0)
	require.EqualValues(t, 0, emptyPayload["totals"].(map[string]interface{})["calls"])

	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	started := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	publishAnalyticsToolCall(bus, sessionID, "call-read", "read_file", "success", "", started, 1200)
	publishAnalyticsToolCall(bus, sessionID, "call-bash-1", "bash", "failed", "tool_timeout", started.Add(time.Second), 3000)
	publishAnalyticsToolCall(bus, sessionID, "call-bash-2", "bash", "success", "", started.Add(2*time.Second), 800)

	rec := getAnalyticsResponse(router, "/api/runtime/analytics/tools")
	require.Equal(t, http.StatusOK, rec.Code)
	payload := decodeAnalyticsPayload(t, rec)
	tools := toolStatsByName(t, payload)
	require.Len(t, tools, 2)
	require.EqualValues(t, 2, tools["bash"]["calls"])
	require.EqualValues(t, 1, tools["bash"]["failures"])
	require.InDelta(t, 0.5, tools["bash"]["failure_rate"].(float64), 1e-9)
	require.EqualValues(t, 1900, tools["bash"]["average_duration_ms"], "平均耗时 = (3000+800)/2")
	require.EqualValues(t, 800, tools["bash"]["min_duration_ms"], "最小耗时 = min(3000,800)")
	require.EqualValues(t, 3000, tools["bash"]["max_duration_ms"], "最大耗时 = max(3000,800)")
	require.EqualValues(t, 1, tools["read_file"]["calls"])
	require.EqualValues(t, 0, tools["read_file"]["failures"])
	require.EqualValues(t, 1200, tools["read_file"]["average_duration_ms"])
	require.EqualValues(t, 1200, tools["read_file"]["min_duration_ms"])
	require.EqualValues(t, 1200, tools["read_file"]["max_duration_ms"])
	totals := payload["totals"].(map[string]interface{})
	require.EqualValues(t, 3, totals["calls"])
	require.EqualValues(t, 1, totals["failures"])
	require.EqualValues(t, 800, totals["min_duration_ms"])
	require.EqualValues(t, 3000, totals["max_duration_ms"])
	require.EqualValues(t, 1666, totals["average_duration_ms"], "全局平均耗时 = (1200+3000+800)/3")

	// 参数过滤：tool / outcome / session / 时间窗。
	byTool := toolStatsByName(t, decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/tools?tool=read_file")))
	require.Len(t, byTool, 1)
	require.Equal(t, "read_file", byTool["read_file"]["tool_name"])

	byOutcome := toolStatsByName(t, decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/tools?outcome=failed")))
	require.Len(t, byOutcome, 1)
	require.EqualValues(t, 1, byOutcome["bash"]["calls"])
	require.EqualValues(t, 1, byOutcome["bash"]["failures"])

	bySession := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/tools?session=20260917_100000.000_other"))
	require.Len(t, analyticsRows(t, bySession, "tools"), 0)

	inWindow := decodeAnalyticsPayload(t, getAnalyticsResponse(router,
		"/api/runtime/analytics/tools?session="+sessionID+"&from=2026-09-17T09:59:00Z&to=2026-09-17T10:01:00Z"))
	require.Len(t, analyticsRows(t, inWindow, "tools"), 2)
	outOfWindow := decodeAnalyticsPayload(t, getAnalyticsResponse(router,
		"/api/runtime/analytics/tools?from=2026-09-17T11:00:00Z"))
	require.Len(t, analyticsRows(t, outOfWindow, "tools"), 0)

	// 非法 limit 归一：不 400、不 500，按查询层默认上限返回。
	for _, target := range []string{
		"/api/runtime/analytics/tools?limit=abc",
		"/api/runtime/analytics/tools?limit=-5",
		"/api/runtime/analytics/tools?limit=100000",
	} {
		normalized := getAnalyticsResponse(router, target)
		require.Equal(t, http.StatusOK, normalized.Code, target)
		require.Len(t, analyticsRows(t, decodeAnalyticsPayload(t, normalized), "tools"), 2, target)
	}
	limited := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/tools?limit=1"))
	require.Len(t, analyticsRows(t, limited, "tools"), 1)

	// 时间参数非法仍按既有风格 400。
	require.Equal(t, http.StatusBadRequest, getAnalyticsResponse(router, "/api/runtime/analytics/tools?from=not-a-time").Code)
}

// TestAnalyticsHandlersSubagentStats 覆盖 /api/runtime/analytics/subagents：
// 空库 → 空数组、failed_only / failure_category / session 过滤、非法 limit 归一。
func TestAnalyticsHandlersSubagentStats(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	sessionID := "20260917_110000.000_subagents1"
	handler := newRuntimeLogTestHandler()
	handler.SetUsageAnalyticsDBPath(dbPath)
	t.Cleanup(detachUsageAnalyticsService)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// 空库：200 + 空数组 + 零值统计。
	emptyRec := getAnalyticsResponse(router, "/api/runtime/analytics/subagents")
	require.Equal(t, http.StatusOK, emptyRec.Code)
	require.Contains(t, emptyRec.Body.String(), `"subagents":[]`)
	emptyPayload := decodeAnalyticsPayload(t, emptyRec)
	require.Len(t, analyticsRows(t, emptyPayload, "subagents"), 0)
	emptySummary := emptyPayload["summary"].(map[string]interface{})
	require.EqualValues(t, 0, emptySummary["total"])
	require.EqualValues(t, 0, emptySummary["failure_rate"])

	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	completedAt := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
	bus.Publish(runtimeevents.Event{
		Type: usageanalytics.EventSubagentCompleted, SessionID: sessionID, Timestamp: completedAt,
		Payload: map[string]interface{}{
			"subagent_id": "sub-ok", "child_session_id": "child-ok", "role": "explore",
			"success": true, "duration_ms": 1500, "source": "scheduler", "usage_total_tokens": 120,
		},
	})
	bus.Publish(runtimeevents.Event{
		Type: usageanalytics.EventSubagentCompleted, SessionID: sessionID, Timestamp: completedAt.Add(time.Second),
		Payload: map[string]interface{}{
			"subagent_id": "sub-timeout", "child_session_id": "child-timeout", "role": "explore",
			"success": false, "status": "failed", "failure_category": "timeout",
			"error_code": "subagent_timeout", "attempt": 2, "max_attempts": 2,
			"retry_reason": "timeout", "duration_ms": 30000, "source": "scheduler",
		},
	})
	// 双生产者冲突载荷（success 与 status 语义不一致）：以 success 为准并计冲突。
	bus.Publish(runtimeevents.Event{
		Type: usageanalytics.EventSubagentCompleted, SessionID: sessionID, Timestamp: completedAt.Add(2 * time.Second),
		Payload: map[string]interface{}{
			"subagent_id": "sub-conflict", "child_session_id": "child-conflict", "role": "explore",
			"success": true, "status": "failed", "source": "agent_controller",
		},
	})

	rec := getAnalyticsResponse(router, "/api/runtime/analytics/subagents")
	require.Equal(t, http.StatusOK, rec.Code)
	payload := decodeAnalyticsPayload(t, rec)
	rows := analyticsRows(t, payload, "subagents")
	require.Len(t, rows, 3)
	summary := payload["summary"].(map[string]interface{})
	require.EqualValues(t, 3, summary["total"])
	require.EqualValues(t, 2, summary["succeeded"])
	require.EqualValues(t, 1, summary["failed"])
	require.InDelta(t, 1.0/3.0, summary["failure_rate"].(float64), 1e-9)
	require.EqualValues(t, 1, summary["timeouts"])
	require.EqualValues(t, 1, summary["retried"])
	require.EqualValues(t, 1, summary["failure_categories"].(map[string]interface{})["timeout"])
	sources := summary["sources"].(map[string]interface{})
	require.EqualValues(t, 2, sources["scheduler"])
	require.EqualValues(t, 1, sources["agent_controller"])

	byID := map[string]map[string]interface{}{}
	for _, row := range rows {
		item := row.(map[string]interface{})
		byID[item["subagent_id"].(string)] = item
	}
	require.EqualValues(t, 2, byID["sub-timeout"]["attempt"])
	require.Equal(t, "timeout", byID["sub-timeout"]["failure_category"])
	require.EqualValues(t, 1, byID["sub-conflict"]["conflict_count"])
	require.Equal(t, true, byID["sub-conflict"]["success"])

	// 过滤：failed_only / failure_category / session。
	failedOnly := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/subagents?failed_only=true"))
	require.Len(t, analyticsRows(t, failedOnly, "subagents"), 1)
	require.Equal(t, "sub-timeout", analyticsRows(t, failedOnly, "subagents")[0].(map[string]interface{})["subagent_id"])

	byCategory := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/subagents?failure_category=timeout"))
	require.Len(t, analyticsRows(t, byCategory, "subagents"), 1)

	bySession := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/subagents?session=20260917_110000.000_other"))
	require.Len(t, analyticsRows(t, bySession, "subagents"), 0)

	// 非法 failed_only / limit 归一：不 400、不 500。
	normalized := getAnalyticsResponse(router, "/api/runtime/analytics/subagents?failed_only=maybe&limit=abc&limit=-2")
	require.Equal(t, http.StatusOK, normalized.Code)
	require.Len(t, analyticsRows(t, decodeAnalyticsPayload(t, normalized), "subagents"), 3)

	require.Equal(t, http.StatusBadRequest, getAnalyticsResponse(router, "/api/runtime/analytics/subagents?to=not-a-time").Code)
}

// TestAnalyticsHandlersErrorPatterns 覆盖 /api/runtime/analytics/errors：
// 空库 → 空数组、source 过滤、top 归一。
func TestAnalyticsHandlersErrorPatterns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	sessionID := "20260917_120000.000_errors1"
	handler := newRuntimeLogTestHandler()
	handler.SetUsageAnalyticsDBPath(dbPath)
	t.Cleanup(detachUsageAnalyticsService)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// 空库：200 + `"patterns":[]`。
	emptyRec := getAnalyticsResponse(router, "/api/runtime/analytics/errors")
	require.Equal(t, http.StatusOK, emptyRec.Code)
	require.Contains(t, emptyRec.Body.String(), `"patterns":[]`)
	require.Len(t, analyticsRows(t, decodeAnalyticsPayload(t, emptyRec), "patterns"), 0)

	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	started := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	publishAnalyticsToolCall(bus, sessionID, "call-fail-1", "bash", "failed", "tool_timeout", started, 3000)
	publishAnalyticsToolCall(bus, sessionID, "call-fail-2", "bash", "failed", "tool_timeout", started.Add(time.Second), 3000)
	bus.Publish(runtimeevents.Event{
		Type: usageanalytics.EventSubagentCompleted, SessionID: sessionID, Timestamp: started.Add(2 * time.Second),
		Payload: map[string]interface{}{
			"subagent_id": "sub-fail", "child_session_id": "child-fail", "role": "explore",
			"success": false, "failure_category": "timeout", "error_code": "subagent_timeout",
		},
	})

	rec := getAnalyticsResponse(router, "/api/runtime/analytics/errors")
	require.Equal(t, http.StatusOK, rec.Code)
	payload := decodeAnalyticsPayload(t, rec)
	patterns := analyticsRows(t, payload, "patterns")
	require.Len(t, patterns, 2)
	bySource := map[string]map[string]interface{}{}
	for _, row := range patterns {
		item := row.(map[string]interface{})
		bySource[item["source"].(string)] = item
	}
	require.EqualValues(t, 2, bySource["tools"]["count"])
	require.Equal(t, "tool_timeout", bySource["tools"]["error_code"])
	require.EqualValues(t, 1, bySource["subagents"]["count"])
	require.Equal(t, "timeout", bySource["subagents"]["failure_category"])

	// source 过滤。
	toolsOnly := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/errors?source=tools"))
	require.Len(t, analyticsRows(t, toolsOnly, "patterns"), 1)
	subagentsOnly := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/errors?source=subagents"))
	require.Len(t, analyticsRows(t, subagentsOnly, "patterns"), 1)
	bySession := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/errors?session=20260917_120000.000_other"))
	require.Len(t, analyticsRows(t, bySession, "patterns"), 0)

	// top 归一：合法值截断，非法值回退默认。
	topOne := decodeAnalyticsPayload(t, getAnalyticsResponse(router, "/api/runtime/analytics/errors?top=1"))
	topRows := analyticsRows(t, topOne, "patterns")
	require.Len(t, topRows, 1)
	require.EqualValues(t, 2, topRows[0].(map[string]interface{})["count"])
	for _, target := range []string{
		"/api/runtime/analytics/errors?top=abc",
		"/api/runtime/analytics/errors?top=-3",
	} {
		normalized := getAnalyticsResponse(router, target)
		require.Equal(t, http.StatusOK, normalized.Code, target)
		require.Len(t, analyticsRows(t, decodeAnalyticsPayload(t, normalized), "patterns"), 2, target)
	}

	require.Equal(t, http.StatusBadRequest, getAnalyticsResponse(router, "/api/runtime/analytics/errors?from=not-a-time").Code)
}

// TestAnalyticsHandlersUsageAnalyticsHealthBlock 覆盖批次 3.2：
// runtimeStatusSnapshot 的 usage_analytics 健康块（未挂载/挂载两种形态字段齐全）。
func TestAnalyticsHandlersUsageAnalyticsHealthBlock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	sessionID := "20260917_130000.000_health1"
	handler := newRuntimeLogTestHandler()
	handler.SetUsageAnalyticsDBPath(dbPath)
	t.Cleanup(detachUsageAnalyticsService)

	// 未挂载：字段仍存在且语义稳定（attached=false、db_path 非空、counts=0、last=null）。
	detachUsageAnalyticsService()
	block := usageAnalyticsHealthBlock(t, handler)
	require.Equal(t, false, block["attached"])
	require.Equal(t, dbPath, block["db_path"])
	require.EqualValues(t, 0, block["ingested_total"])
	require.EqualValues(t, 0, block["conflict_total"])
	require.Nil(t, block["last_ingest_at"])
	require.Equal(t, true, block["degraded"])
	require.NotNil(t, block["table_counts"])

	// 挂载 + 写入事件：attached=true，计数与最近采集时间可见。
	bus := handler.getRuntimeEventBus()
	service := attachUsageAnalyticsSingleton(handler.UsageAnalyticsDBPath(), bus, nil, nil)
	require.NotNil(t, service)
	started := time.Now().Add(-time.Minute)
	publishAnalyticsToolCall(bus, sessionID, "call-health", "read_file", "success", "", started, 250)
	bus.Publish(runtimeevents.Event{
		Type: usageanalytics.EventSubagentCompleted, SessionID: sessionID, Timestamp: time.Now(),
		Payload: map[string]interface{}{
			"subagent_id": "sub-conflict-health", "child_session_id": "child-health", "role": "explore",
			"success": true, "status": "failed",
		},
	})

	block = usageAnalyticsHealthBlock(t, handler)
	require.Equal(t, true, block["attached"])
	require.Equal(t, dbPath, block["db_path"])
	require.EqualValues(t, 2, block["ingested_total"])
	require.EqualValues(t, 1, block["conflict_total"])
	require.Equal(t, false, block["degraded"])
	require.NotNil(t, block["last_ingest_at"])
	tableCounts := block["table_counts"].(map[string]interface{})
	require.EqualValues(t, 1, tableCounts["tool_calls"])
	require.EqualValues(t, 1, tableCounts["subagents"])
}

func usageAnalyticsHealthBlock(t *testing.T, handler *Handler) map[string]interface{} {
	t.Helper()
	snapshot := handler.RuntimeStatusSnapshot(context.Background(), llm.HealthCheckModeNone)
	block, ok := snapshot["usage_analytics"].(map[string]interface{})
	require.True(t, ok, "runtimeStatusSnapshot 必须包含 usage_analytics 块")
	return block
}
