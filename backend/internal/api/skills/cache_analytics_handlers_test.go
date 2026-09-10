package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	skill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"

	"github.com/stretchr/testify/require"
)

// ============================================================================
// runtime server 缓存分析端点测试（cache.analytics.v1，方案 §11 Phase 2）。
// 覆盖：capabilities / overview / requests / request detail / message trace /
// 未知 session（cache_session_not_found）/ 未知端点。
// ============================================================================

// resetCacheAnalyticsSingleton 重置进程内单例，保证每个测试拿到独立
// collector（绑定各自 handler 的 EventBus）。
func resetCacheAnalyticsSingleton() {
	cacheAnalyticsMu.Lock()
	defer cacheAnalyticsMu.Unlock()
	if cacheAnalyticsClose != nil {
		cacheAnalyticsClose()
	}
	cacheAnalyticsSource = nil
	cacheAnalyticsClose = nil
}

// newCacheRuntimeHandler 构造带 SessionManager + 路由的测试 handler。
// RegisterRoutes 即挂载 collector（生产语义：server 启动即采集）。
func newCacheRuntimeHandler(t *testing.T) (*Handler, *mux.Router, *runtimechat.SessionManager, *runtimechat.Session) {
	t.Helper()
	resetCacheAnalyticsSingleton()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	storage := runtimechat.NewInMemoryStorage()
	sessionManager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	ctx := context.Background()
	session, err := sessionManager.Create(ctx, "cache-user")
	require.NoError(t, err)
	require.NoError(t, storage.Save(ctx, session))
	return handler, router, sessionManager, session
}

// publishRuntimeCacheStarted 发布 llm.request.started（点号格式）。
func publishRuntimeCacheStarted(t *testing.T, h *Handler, sessionID, llmRequestID string, extra map[string]interface{}) {
	t.Helper()
	payload := map[string]interface{}{
		"llm_request_id":     llmRequestID,
		"trace_id":           "trace-" + llmRequestID,
		"logical_turn_id":    "turn-" + llmRequestID,
		"step":               1,
		"provider":           "openai",
		"model":              "gpt-4o",
		"prompt_cache_epoch": 3,
		"prompt_cache_key":   "ck-1",
		"prompt_fingerprint": "fp-" + llmRequestID,
	}
	for key, value := range extra {
		payload[key] = value
	}
	h.getRuntimeEventBus().Publish(events.Event{
		Type:      "llm.request.started",
		SessionID: sessionID,
		TraceID:   "trace-" + llmRequestID,
		Payload:   payload,
		Timestamp: nextCacheRuntimeTestTimestamp(),
	})
}

// publishRuntimeCacheFinished 发布 llm.request.finished。
func publishRuntimeCacheFinished(t *testing.T, h *Handler, sessionID, llmRequestID string, extra map[string]interface{}) {
	t.Helper()
	payload := map[string]interface{}{
		"llm_request_id":            llmRequestID,
		"trace_id":                  "trace-" + llmRequestID,
		"logical_turn_id":           "turn-" + llmRequestID,
		"step":                      1,
		"provider":                  "openai",
		"model":                     "gpt-4o",
		"success":                   true,
		"usage_prompt_tokens":       200,
		"usage_cache_read_reported": true,
	}
	for key, value := range extra {
		payload[key] = value
	}
	h.getRuntimeEventBus().Publish(events.Event{
		Type:      "llm.request.finished",
		SessionID: sessionID,
		TraceID:   "trace-" + llmRequestID,
		Payload:   payload,
		Timestamp: nextCacheRuntimeTestTimestamp(),
	})
}

var cacheRuntimeTestClock int64

func nextCacheRuntimeTestTimestamp() time.Time {
	cacheRuntimeTestClock++
	return time.Unix(1700000000, 0).Add(time.Duration(cacheRuntimeTestClock) * time.Millisecond)
}

func cacheRuntimeGetJSON(t *testing.T, router *mux.Router, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	return rec.Code, body
}

func cacheRuntimeErrorCode(t *testing.T, body map[string]interface{}) string {
	t.Helper()
	errObj, ok := body["error"].(map[string]interface{})
	require.True(t, ok, "expected error envelope, got: %v", body)
	code, _ := errObj["code"].(string)
	return code
}

func TestHandleSessionCache_Capabilities(t *testing.T) {
	_, router, _, _ := newCacheRuntimeHandler(t)
	code, body := cacheRuntimeGetJSON(t, router, "/api/runtime/sessions/sess-x/cache/capabilities")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "cache.analytics.v1", body["schema_version"])
	require.Equal(t, "live", body["data_source"])
}

func TestHandleSessionCache_RequestsAndOverview(t *testing.T) {
	handler, router, _, session := newCacheRuntimeHandler(t)
	sessionID := session.ID

	publishRuntimeCacheStarted(t, handler, sessionID, "req-1", nil)
	publishRuntimeCacheFinished(t, handler, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens": 100,
	})
	publishRuntimeCacheStarted(t, handler, sessionID, "req-2", nil)
	publishRuntimeCacheFinished(t, handler, sessionID, "req-2", map[string]interface{}{
		"usage_cache_creation_tokens": 150,
	})

	base := "/api/runtime/sessions/" + sessionID + "/cache"
	code, body := cacheRuntimeGetJSON(t, router, base+"/requests?limit=50")
	require.Equal(t, http.StatusOK, code)
	requests, ok := body["requests"].([]interface{})
	require.True(t, ok)
	require.Len(t, requests, 2)
	first := requests[0].(map[string]interface{})
	require.Equal(t, "req-2", first["llm_request_id"], "newest first")
	require.Equal(t, "write", first["cache_status"])
	second := requests[1].(map[string]interface{})
	require.Equal(t, "hit", second["cache_status"])

	code, body = cacheRuntimeGetJSON(t, router, base+"/overview")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 2, body["requests_total"])
	dist := body["cache_status_distribution"].(map[string]interface{})
	require.EqualValues(t, 1, dist["hit"])
	require.EqualValues(t, 1, dist["write"])
}

func TestHandleSessionCache_RequestDetail(t *testing.T) {
	handler, router, _, session := newCacheRuntimeHandler(t)
	sessionID := session.ID
	publishRuntimeCacheStarted(t, handler, sessionID, "req-1", nil)
	publishRuntimeCacheFinished(t, handler, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens": 100,
	})

	base := "/api/runtime/sessions/" + sessionID + "/cache"
	code, body := cacheRuntimeGetJSON(t, router, base+"/requests/req-1")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "req-1", body["llm_request_id"])
	require.Equal(t, "trace-req-1", body["trace_id"])
	require.Equal(t, "turn-req-1", body["turn_id"])
	usage := body["usage"].(map[string]interface{})
	require.EqualValues(t, 200, usage["prompt_tokens"])

	code, body = cacheRuntimeGetJSON(t, router, base+"/requests/req-unknown")
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "cache_not_found", cacheRuntimeErrorCode(t, body))
}

func TestHandleSessionCache_MessageTrace(t *testing.T) {
	handler, router, sessionManager, session := newCacheRuntimeHandler(t)
	sessionID := session.ID

	// 历史：user(msg-u1) → assistant(msg-a1)，同一 turn。
	ctx := context.Background()
	userMsg := *runtimetypes.NewUserMessage("hello")
	userMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-u1", "turn_id": "turn-t1"}
	assistantMsg := runtimetypes.Message{Role: "assistant", Content: "hi"}
	assistantMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-a1", "turn_id": "turn-t1"}
	session.AddMessage(userMsg)
	session.AddMessage(assistantMsg)
	require.NoError(t, sessionManager.GetStorage().Save(ctx, session))

	publishRuntimeCacheStarted(t, handler, sessionID, "req-1", map[string]interface{}{"logical_turn_id": "turn-t1"})
	publishRuntimeCacheFinished(t, handler, sessionID, "req-1", map[string]interface{}{
		"logical_turn_id":         "turn-t1",
		"usage_cache_read_tokens": 100,
	})

	base := "/api/runtime/sessions/" + sessionID + "/cache"
	code, body := cacheRuntimeGetJSON(t, router, base+"/messages/msg-a1/trace")
	require.Equal(t, http.StatusOK, code)
	produced, ok := body["produced_by"].(map[string]interface{})
	require.True(t, ok, "produced_by missing: %v", body)
	require.Equal(t, "req-1", produced["llm_request_id"])
	require.Equal(t, "turn-t1", body["turn_id"])

	// 未知 session → 404 cache_session_not_found。
	code, body = cacheRuntimeGetJSON(t, router, "/api/runtime/sessions/sess-nope/cache/messages/msg-a1/trace")
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "cache_session_not_found", cacheRuntimeErrorCode(t, body))
}

func TestHandleSessionCache_UnknownEndpoint(t *testing.T) {
	_, router, _, session := newCacheRuntimeHandler(t)
	code, body := cacheRuntimeGetJSON(t, router, "/api/runtime/sessions/"+session.ID+"/cache/bogus")
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "cache_not_found", cacheRuntimeErrorCode(t, body))
}
