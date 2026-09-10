package cacheanalytics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

type stubHistory struct {
	exists     bool
	contexts   map[string]MessageContext
	assistant  map[string]string
	user       map[string]string
}

func (h *stubHistory) MessageContext(sessionID, messageID string) (MessageContext, bool) {
	ctx, ok := h.contexts[messageID]
	return ctx, ok
}

func (h *stubHistory) AssistantMessageIDByTurn(sessionID, turnID string) (string, bool) {
	id, ok := h.assistant[turnID]
	return id, ok
}

func (h *stubHistory) UserMessageIDByTurn(sessionID, turnID string) (string, bool) {
	id, ok := h.user[turnID]
	return id, ok
}

func (h *stubHistory) SessionExists(sessionID string) bool { return h.exists }

func newTestSource(t *testing.T) (*runtimeevents.Bus, *Service, *stubHistory) {
	t.Helper()
	bus := newTestBus()
	history := &stubHistory{
		exists:    true,
		contexts:  make(map[string]MessageContext),
		assistant: make(map[string]string),
		user:      make(map[string]string),
	}
	service := Attach(bus, Options{MaxRequestsPerSession: 100}, history)
	t.Cleanup(service.Close)
	return bus, service, history
}

func TestMessageTraceViaHistoryLookup(t *testing.T) {
	bus, service, history := newTestSource(t)
	src := service.Source()

	// turn-1: user 消息触发，两个请求（step 1/2），assistant 消息由终步产出。
	history.contexts["msg-user-1"] = MessageContext{MessageID: "msg-user-1", Role: "user", TurnID: "turn-1", NextMessageID: "msg-asst-1"}
	history.contexts["msg-asst-1"] = MessageContext{MessageID: "msg-asst-1", Role: "assistant", TurnID: "turn-1", PrevMessageID: "msg-user-1", NextMessageID: "msg-user-2"}
	history.user["turn-1"] = "msg-user-1"
	history.assistant["turn-1"] = "msg-asst-1"

	publishStarted(bus, "s1", "req-step1", map[string]interface{}{"logical_turn_id": "turn-1", "step": 1})
	publishFinished(bus, "s1", "req-step1", true, map[string]interface{}{
		"usage_prompt_tokens":       100,
		"usage_cache_read_tokens":   80,
		"usage_cache_read_reported": true,
	})
	publishStarted(bus, "s1", "req-step2", map[string]interface{}{"logical_turn_id": "turn-1", "step": 2})
	publishFinished(bus, "s1", "req-step2", true, map[string]interface{}{
		"usage_prompt_tokens":       200,
		"usage_cache_read_tokens":   160,
		"usage_cache_read_reported": true,
	})
	// 后续 turn 的请求消费了 turn-1 的 assistant 输出。
	publishStarted(bus, "s1", "req-turn2", map[string]interface{}{"logical_turn_id": "turn-2", "step": 1})
	publishFinished(bus, "s1", "req-turn2", true, nil)

	trace, err := src.MessageTrace("s1", "msg-asst-1")
	if err != nil {
		t.Fatalf("message trace: %v", err)
	}
	if !trace.HistoryAvailable || trace.CorrelationSource != CorrelationSourceHistory {
		t.Fatalf("expected history correlation: %+v", trace)
	}
	if trace.MessageRole != "assistant" || trace.TurnID != "turn-1" {
		t.Fatalf("role/turn mismatch: %s/%s", trace.MessageRole, trace.TurnID)
	}
	if trace.ProducedBy == nil || trace.ProducedBy.LLMRequestID != "req-step2" {
		t.Fatalf("produced_by should be last successful step: %+v", trace.ProducedBy)
	}
	if trace.Neighbors.PrevMessageID != "msg-user-1" || trace.Neighbors.NextMessageID != "msg-user-2" {
		t.Fatalf("neighbors mismatch: %+v", trace.Neighbors)
	}
	foundTurn2 := false
	for _, consumer := range trace.ConsumedBy {
		if consumer.LLMRequestID == "req-turn2" {
			foundTurn2 = true
		}
		if consumer.LLMRequestID == "req-step2" {
			t.Fatal("producer must not appear in consumed_by")
		}
	}
	if !foundTurn2 {
		t.Fatalf("turn-2 request should consume assistant output: %+v", trace.ConsumedBy)
	}
	// 回填生效：assistant_message_id 写入产出记录。
	record, err := src.Request("s1", "req-step2")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if record.AssistantMessageID != "msg-asst-1" {
		t.Fatalf("assistant message id not backfilled: %s", record.AssistantMessageID)
	}

	// user 消息追溯：produced_by 为空，consumed_by 为本 turn 请求。
	userTrace, err := src.MessageTrace("s1", "msg-user-1")
	if err != nil {
		t.Fatalf("user trace: %v", err)
	}
	if userTrace.ProducedBy != nil {
		t.Fatalf("user message has no producer: %+v", userTrace.ProducedBy)
	}
	if len(userTrace.ConsumedBy) < 2 {
		t.Fatalf("user message should be consumed by turn requests: %+v", userTrace.ConsumedBy)
	}
	first := userTrace.ConsumedBy[0].LLMRequestID
	if first != "req-step1" && first != "req-step2" {
		t.Fatalf("first consumer should be same-turn request, got %s", first)
	}
}

func TestMessageTraceNotFound(t *testing.T) {
	_, service, _ := newTestSource(t)
	if _, err := service.Source().MessageTrace("s1", "msg-unknown"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSessionNotFoundWhenHistorySaysMissing(t *testing.T) {
	_, service, history := newTestSource(t)
	history.exists = false
	src := service.Source()
	if _, err := src.Overview("ghost"); err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
	history.exists = true
	if _, err := src.Overview("empty"); err != nil {
		t.Fatalf("existing session with no records should return empty overview: %v", err)
	}
}

func TestHTTPContract(t *testing.T) {
	bus, service, _ := newTestSource(t)
	src := service.Source()
	publishStarted(bus, "s1", "req-1", nil)
	publishFinished(bus, "s1", "req-1", true, map[string]interface{}{
		"usage_prompt_tokens":       1000,
		"usage_cache_read_tokens":   800,
		"usage_cache_read_reported": true,
	})

	mux := http.NewServeMux()
	Mount(mux, "/web/api/cache", src)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	do := func(path string) (int, map[string]interface{}) {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		var body map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return resp.StatusCode, body
	}

	// capabilities
	status, body := do("/web/api/cache/capabilities")
	if status != http.StatusOK || body["schema_version"] != SchemaVersion || body["data_source"] != DataSourceLive {
		t.Fatalf("capabilities mismatch: %d %+v", status, body)
	}

	// overview
	status, body = do("/web/api/cache/overview?session_id=s1")
	if status != http.StatusOK || body["requests_total"].(float64) != 1 {
		t.Fatalf("overview mismatch: %d %+v", status, body)
	}
	tokens := body["tokens"].(map[string]interface{})
	if tokens["cache_read_tokens"].(float64) != 800 {
		t.Fatalf("tokens mismatch: %+v", tokens)
	}

	// requests 过滤
	status, body = do("/web/api/cache/requests?session_id=s1&cache_status=hit")
	if status != http.StatusOK || body["total"].(float64) != 1 {
		t.Fatalf("requests filter mismatch: %d %+v", status, body)
	}
	status, body = do("/web/api/cache/requests?session_id=s1&cache_status=miss")
	if status != http.StatusOK || body["total"].(float64) != 0 {
		t.Fatalf("requests filter mismatch: %d %+v", status, body)
	}

	// 单请求详情
	status, body = do("/web/api/cache/requests/req-1?session_id=s1")
	if status != http.StatusOK || body["llm_request_id"] != "req-1" {
		t.Fatalf("request detail mismatch: %d %+v", status, body)
	}

	// 404：未知请求
	status, body = do("/web/api/cache/requests/req-none?session_id=s1")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
	errBody := body["error"].(map[string]interface{})
	if errBody["code"] != ErrNotFound.Error() {
		t.Fatalf("error code mismatch: %+v", errBody)
	}

	// 404：未知会话（history stub exists=true 时返回空 overview 而非 404）
	status, _ = do("/web/api/cache/overview?session_id=unknown-but-valid")
	if status != http.StatusOK {
		t.Fatalf("unknown session with valid id should be empty overview: %d", status)
	}

	// 400：非法 limit
	status, body = do("/web/api/cache/requests?session_id=s1&limit=abc")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if body["error"].(map[string]interface{})["code"] != ErrInvalidRequest.Error() {
		t.Fatalf("error code mismatch: %+v", body)
	}

	// 404：未知端点
	status, _ = do("/web/api/cache/unknown")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown endpoint, got %d", status)
	}

	// 405：POST
	resp, err := http.Post(server.URL+"/web/api/cache/overview", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestFromToTimeFilter(t *testing.T) {
	bus, service, _ := newTestSource(t)
	src := service.Source()
	publishStarted(bus, "s1", "req-old", nil)
	publishFinished(bus, "s1", "req-old", true, nil)
	// 手动构造晚于 req-old 的记录时间窗口。
	response, err := src.Requests("s1", RequestQuery{
		From: ptrTime(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if response.Total != 0 {
		t.Fatalf("future window should be empty, got %d", response.Total)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
