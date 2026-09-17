package skills

import (
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

	// /overview 与 /summary 同源（同一 DB 查询）。
	overviewRec := get("/api/runtime/analytics/overview")
	require.Equal(t, http.StatusOK, overviewRec.Code)
	var overviewPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(overviewRec.Body.Bytes(), &overviewPayload))
	require.EqualValues(t, 1, overviewPayload["matched"])
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
