package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// /web/api/cache/* — 缓存分析端点（cache.analytics.v1）
// 复用 cacheanalytics.Handler 契约；此处验证 aicli 侧装配（会话解析、
// 本地 Service 构建、HistoryLookup 消息追溯）。
// ---------------------------------------------------------------------------

// cacheTestClockNsec 递增时间戳：bus.Publish 自动填充真实 time.Now()，
// Windows 时钟精度下同刻事件会破坏排序，测试事件显式传增 Timestamp。
var cacheTestClockNsec int64

func nextCacheTestTimestamp() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, int(atomic.AddInt64(&cacheTestClockNsec, 1000)), time.UTC)
}

// TestChatWebSSECacheRequestFinishedMapping 验证 cache_request_finished
// 的 SSE 映射（§6.3）：事件名映射、data 字段提取与 schema 注册。
func TestChatWebSSECacheRequestFinishedMapping(t *testing.T) {
	if name, ok := chatWebSSEEventName(cacheanalytics.EventCacheRequestFinished); !ok || name != "cache_request_finished" {
		t.Fatalf("chatWebSSEEventName = %q, %v", name, ok)
	}

	ev := runtimeevents.Event{
		Type:      cacheanalytics.EventCacheRequestFinished,
		SessionID: "s1",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"llm_request_id":       "req-1",
			"turn_id":              "turn-1",
			"step":                 2,
			"provider":             "openai",
			"model":                "gpt-4o",
			"status":               "success",
			"cache_status":         "hit",
			"cache_hit_ratio":      0.8,
			"duration_ms":          int64(1230),
			"assistant_message_id": "msg-a1",
			"correlation_source":   "event",
			"usage":                map[string]interface{}{"prompt_tokens": float64(1000)},
		},
	}
	data := chatWebSSEDataForEvent(ev)
	if data["llm_request_id"] != "req-1" || data["cache_status"] != "hit" || data["assistant_message_id"] != "msg-a1" {
		t.Fatalf("data fields missing: %v", data)
	}
	if _, ok := data["usage"].(map[string]interface{}); !ok {
		t.Fatalf("usage field missing: %v", data["usage"])
	}

	var found bool
	for _, spec := range chatWebSSESchema() {
		if spec.Event == "cache_request_finished" {
			found = true
			if spec.SourceEvent != cacheanalytics.EventCacheRequestFinished {
				t.Fatalf("schema source_event = %q", spec.SourceEvent)
			}
			if len(spec.Fields) == 0 {
				t.Fatal("schema fields empty")
			}
		}
	}
	if !found {
		t.Fatal("schema missing cache_request_finished")
	}
}

// newCacheTestSession 构造带 EventBus + InMemoryStorage 的测试会话。
// 返回 session、bus（发布 llm.request.* 事件）、storage（写历史消息）。
func newCacheTestSession(t *testing.T) (*ChatSession, *runtimeevents.Bus, *runtimechat.InMemoryStorage) {
	t.Helper()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	if err := storage.Save(ctx, runtimeSession); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	bus := runtimeevents.NewBus()
	session := newWebTestSession()
	session.RuntimeSession = runtimeSession
	session.LocalRuntimeHost = &localChatRuntimeHost{
		EventBus:     bus,
		SessionStore: storage,
	}
	withWebTestSession(t, session)
	// 生产环境在会话启动时即挂载 collector（chat_cache_local.go）；
	// 测试必须先构建 service 再发布事件，否则事件发布时无订阅者。
	if ensureLocalCacheService(session.LocalRuntimeHost) == nil {
		t.Fatal("expected local cache service to attach")
	}
	return session, bus, storage
}

// publishCacheStarted 发布 llm.request.started（点号格式，与 internal/agent/loop.go 一致）。
func publishCacheStarted(t *testing.T, bus *runtimeevents.Bus, sessionID, llmRequestID string, extra map[string]interface{}) {
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
	bus.Publish(runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: sessionID,
		TraceID:   "trace-" + llmRequestID,
		Payload:   payload,
		Timestamp: nextCacheTestTimestamp(),
	})
}

// publishCacheFinished 发布 llm.request.finished。
func publishCacheFinished(t *testing.T, bus *runtimeevents.Bus, sessionID, llmRequestID string, extra map[string]interface{}) {
	t.Helper()
	payload := map[string]interface{}{
		"llm_request_id":     llmRequestID,
		"trace_id":           "trace-" + llmRequestID,
		"logical_turn_id":    "turn-" + llmRequestID,
		"step":               1,
		"provider":           "openai",
		"model":              "gpt-4o",
		"success":            true,
		"usage_prompt_tokens": 200,
		"usage_cache_read_reported": true,
	}
	for key, value := range extra {
		payload[key] = value
	}
	bus.Publish(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: sessionID,
		TraceID:   "trace-" + llmRequestID,
		Payload:   payload,
		Timestamp: nextCacheTestTimestamp(),
	})
}

func cacheGetJSON(t *testing.T, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPICache(rec, req)
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %s: %v; body: %s", path, err, rec.Body.String())
	}
	return rec.Code, body
}

func cacheErrorCode(t *testing.T, body map[string]interface{}) string {
	t.Helper()
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error envelope, got: %v", body)
	}
	code, _ := errObj["code"].(string)
	return code
}

func TestHandleChatWebAPICache_NoSessionDisabled(t *testing.T) {
	// 无活动会话：503 + 稳定错误码 cache_analytics_disabled。
	withWebTestSession(t, nil)
	code, body := cacheGetJSON(t, ChatWebAPICachePath+"/capabilities")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if got := cacheErrorCode(t, body); got != "cache_analytics_disabled" {
		t.Fatalf("error code = %q, want cache_analytics_disabled", got)
	}
}

func TestHandleChatWebAPICache_Capabilities(t *testing.T) {
	_, _, _ = newCacheTestSession(t)
	code, body := cacheGetJSON(t, ChatWebAPICachePath+"/capabilities")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", code, body)
	}
	if body["schema_version"] != "cache.analytics.v1" {
		t.Fatalf("schema_version = %v, want cache.analytics.v1", body["schema_version"])
	}
	if body["data_source"] != "live" {
		t.Fatalf("data_source = %v, want live", body["data_source"])
	}
}

func TestHandleChatWebAPICache_RequestsAndOverview(t *testing.T) {
	session, bus, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID

	// 请求 1：缓存命中（read=100/prompt=200 → hit_ratio 0.5）。
	publishCacheStarted(t, bus, sessionID, "req-1", nil)
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens":    100,
		"usage_cache_creation_tokens": 0,
	})
	// 请求 2：缓存写入（creation=150/prompt=200 → write_ratio 0.75）。
	publishCacheStarted(t, bus, sessionID, "req-2", nil)
	publishCacheFinished(t, bus, sessionID, "req-2", map[string]interface{}{
		"usage_cache_read_tokens":     0,
		"usage_cache_creation_tokens": 150,
	})

	code, body := cacheGetJSON(t, ChatWebAPICachePath+"/requests?limit=50")
	if code != http.StatusOK {
		t.Fatalf("requests status = %d, want 200", code)
	}
	requests, ok := body["requests"].([]interface{})
	if !ok || len(requests) != 2 {
		t.Fatalf("requests = %v, want 2 entries", body["requests"])
	}
	// 排序：新→旧，req-2 在前。
	first := requests[0].(map[string]interface{})
	if first["llm_request_id"] != "req-2" {
		t.Fatalf("first request = %v, want req-2 (newest first)", first["llm_request_id"])
	}
	if first["cache_status"] != "write" {
		t.Fatalf("req-2 cache_status = %v, want write", first["cache_status"])
	}
	second := requests[1].(map[string]interface{})
	if second["cache_status"] != "hit" {
		t.Fatalf("req-1 cache_status = %v, want hit", second["cache_status"])
	}

	code, body = cacheGetJSON(t, ChatWebAPICachePath+"/overview")
	if code != http.StatusOK {
		t.Fatalf("overview status = %d, want 200", code)
	}
	if body["requests_total"] != float64(2) {
		t.Fatalf("requests_total = %v, want 2", body["requests_total"])
	}
	dist, ok := body["cache_status_distribution"].(map[string]interface{})
	if !ok {
		t.Fatalf("cache_status_distribution missing: %v", body)
	}
	if dist["hit"] != float64(1) || dist["write"] != float64(1) {
		t.Fatalf("distribution = %v, want hit=1 write=1", dist)
	}
}

func TestHandleChatWebAPICache_RequestDetail(t *testing.T) {
	session, bus, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	publishCacheStarted(t, bus, sessionID, "req-1", nil)
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens": 100,
	})

	code, body := cacheGetJSON(t, ChatWebAPICachePath+"/requests/req-1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", code, body)
	}
	if body["llm_request_id"] != "req-1" {
		t.Fatalf("llm_request_id = %v, want req-1", body["llm_request_id"])
	}
	if body["trace_id"] != "trace-req-1" {
		t.Fatalf("trace_id = %v, want trace-req-1", body["trace_id"])
	}
	if body["turn_id"] != "turn-req-1" {
		t.Fatalf("turn_id = %v, want turn-req-1", body["turn_id"])
	}
	if body["status"] != "success" {
		t.Fatalf("status = %v, want success", body["status"])
	}
	usage, ok := body["usage"].(map[string]interface{})
	if !ok || usage["prompt_tokens"] != float64(200) {
		t.Fatalf("usage = %v, want prompt_tokens=200", body["usage"])
	}

	// 未知请求 → 404 cache_not_found。
	code, body = cacheGetJSON(t, ChatWebAPICachePath+"/requests/req-unknown")
	if code != http.StatusNotFound {
		t.Fatalf("unknown request status = %d, want 404", code)
	}
	if got := cacheErrorCode(t, body); got != "cache_not_found" {
		t.Fatalf("error code = %q, want cache_not_found", got)
	}
}

func TestHandleChatWebAPICache_MessageTrace(t *testing.T) {
	session, bus, storage := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID

	// 历史：user(msg-u1, turn-t1) → assistant(msg-a1, turn-t1)。
	// message_id/turn_id 存于 Metadata（session.go 持久化时分配的同一机制）。
	ctx := context.Background()
	userMsg := *runtimetypes.NewUserMessage("hello")
	userMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-u1", "turn_id": "turn-t1"}
	assistantMsg := runtimetypes.Message{Role: "assistant", Content: "hi"}
	assistantMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-a1", "turn_id": "turn-t1"}
	session.RuntimeSession.AddMessage(userMsg)
	session.RuntimeSession.AddMessage(assistantMsg)
	if err := storage.Save(ctx, session.RuntimeSession); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	// LLM 请求：trace-1 / turn-t1（trace/turn 主路径关联）。
	publishCacheStarted(t, bus, sessionID, "req-1", map[string]interface{}{"logical_turn_id": "turn-t1"})
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"logical_turn_id":         "turn-t1",
		"usage_cache_read_tokens": 100,
	})

	// assistant 消息 → produced_by 反查产出请求。
	// 事件流不带 message_id（生产环境 assistant_message 事件同样缺失），
	// 走 HistoryLookup 兜底 → correlation_source=history_inferred（§5.2）。
	code, body := cacheGetJSON(t, ChatWebAPICachePath+"/messages/msg-a1/trace")
	if code != http.StatusOK {
		t.Fatalf("assistant trace status = %d, want 200; body: %v", code, body)
	}
	produced, ok := body["produced_by"].(map[string]interface{})
	if !ok {
		t.Fatalf("produced_by missing: %v", body)
	}
	if produced["llm_request_id"] != "req-1" {
		t.Fatalf("produced_by.llm_request_id = %v, want req-1", produced["llm_request_id"])
	}
	if body["correlation_source"] != "history_inferred" {
		t.Fatalf("correlation_source = %v, want history_inferred (history fallback)", body["correlation_source"])
	}

	// 主路径：finished 载荷直接携带 message_id（§5.2 path 1）→ 不标记推断。
	assistantMsg2 := runtimetypes.Message{Role: "assistant", Content: "hi-2"}
	assistantMsg2.Metadata = runtimetypes.Metadata{"message_id": "msg-a2", "turn_id": "turn-t2"}
	session.RuntimeSession.AddMessage(assistantMsg2)
	if err := storage.Save(ctx, session.RuntimeSession); err != nil {
		t.Fatalf("storage.Save(msg-a2): %v", err)
	}
	publishCacheStarted(t, bus, sessionID, "req-2", map[string]interface{}{"logical_turn_id": "turn-t2"})
	publishCacheFinished(t, bus, sessionID, "req-2", map[string]interface{}{
		"logical_turn_id":         "turn-t2",
		"usage_cache_read_tokens": 100,
		"message_id":              "msg-a2",
	})
	code, body = cacheGetJSON(t, ChatWebAPICachePath+"/messages/msg-a2/trace")
	if code != http.StatusOK {
		t.Fatalf("path1 trace status = %d, want 200; body: %v", code, body)
	}
	if produced, ok = body["produced_by"].(map[string]interface{}); !ok || produced["llm_request_id"] != "req-2" {
		t.Fatalf("path1 produced_by = %v, want req-2", body["produced_by"])
	}
	// correlation_source 标记消息上下文（role/邻居）的解析来源；msg-a2 在
	// 历史中，故仍为 history_inferred——produced_by 已由 path 1 直接登记。

	// user 消息 → consumed_by 包含该请求（历史推断路径）。
	code, body = cacheGetJSON(t, ChatWebAPICachePath+"/messages/msg-u1/trace")
	if code != http.StatusOK {
		t.Fatalf("user trace status = %d, want 200", code)
	}
	consumers, ok := body["consumed_by"].([]interface{})
	if !ok || len(consumers) == 0 {
		t.Fatalf("consumed_by = %v, want non-empty", body["consumed_by"])
	}
	firstConsumer := consumers[0].(map[string]interface{})
	if firstConsumer["llm_request_id"] != "req-1" {
		t.Fatalf("consumed_by[0].llm_request_id = %v, want req-1", firstConsumer["llm_request_id"])
	}
	if body["correlation_source"] != "history_inferred" {
		t.Fatalf("user trace correlation_source = %v, want history_inferred", body["correlation_source"])
	}

	// 相邻消息（HistoryLookup 兜底路径）。
	neighbors, ok := body["neighbors"].(map[string]interface{})
	if !ok {
		t.Fatalf("neighbors missing: %v", body)
	}
	if neighbors["next_message_id"] != "msg-a1" {
		t.Fatalf("neighbors.next_message_id = %v, want msg-a1", neighbors["next_message_id"])
	}

	// 未知消息 → 404 cache_not_found。
	code, body = cacheGetJSON(t, ChatWebAPICachePath+"/messages/msg-unknown/trace")
	if code != http.StatusNotFound {
		t.Fatalf("unknown message status = %d, want 404", code)
	}
	if got := cacheErrorCode(t, body); got != "cache_not_found" {
		t.Fatalf("error code = %q, want cache_not_found", got)
	}
}

func TestHandleChatWebAPICache_UnknownSession(t *testing.T) {
	_, _, _ = newCacheTestSession(t)
	code, body := cacheGetJSON(t, ChatWebAPICachePath+"/requests?session_id=no-such-session")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if got := cacheErrorCode(t, body); got != "cache_session_not_found" {
		t.Fatalf("error code = %q, want cache_session_not_found", got)
	}
}
