package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// withWebTestSession 注册一个临时 ChatSession provider，测试结束后恢复原值。
func withWebTestSession(t *testing.T, session *ChatSession) {
	t.Helper()
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() {
		chatDebugDisplaySessionProvider = old
	})
}

// syncResponseRecorder 包装 httptest.ResponseRecorder 以支持并发读/写。
type syncResponseRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSyncResponseRecorder() *syncResponseRecorder {
	return &syncResponseRecorder{rec: httptest.NewRecorder()}
}

func (s *syncResponseRecorder) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 返回内部 map 引用：handler 仅在流式写入前修改 header，
	// 测试在 handler 退出（<-done）后读取，由 done 通道建立 happens-before。
	return s.rec.Header()
}

func (s *syncResponseRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(p)
}

func (s *syncResponseRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.WriteHeader(code)
}

func (s *syncResponseRecorder) Code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Code
}

func (s *syncResponseRecorder) BodyString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Body.String()
}

func (s *syncResponseRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.Flush()
}

// newWebTestSession 构造一个带 InputQueue 的假会话。
func newWebTestSession() *ChatSession {
	return &ChatSession{
		InputQueue: newChatInputQueue(nil),
	}
}

// ---------------------------------------------------------------------------
// GET /web/ — 页面
// ---------------------------------------------------------------------------

func TestHandleChatWebPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "style.css") {
		t.Fatal("page body missing style.css reference")
	}
	if !strings.Contains(body, "app.js") {
		t.Fatal("page body missing app.js reference")
	}
	if !strings.Contains(body, "aicli micro web client") {
		t.Fatal("page body missing title")
	}
}

func TestHandleChatWebPage_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, ChatWebPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/screen — 屏幕快照
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIScreen_TextDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIScreen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("screen text body empty")
	}
}

func TestHandleChatWebAPIScreen_JSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?format=json", nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIScreen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("screen JSON invalid: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/status — 状态快照
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIStatus_JSONDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIStatusPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("status JSON invalid: %v", err)
	}
}

func TestHandleChatWebAPIStatus_Text(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIStatusPath+"?format=text", nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/events/schema — 事件 schema
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIEventsSchema(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISchemaPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIEventsSchema(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var specs []webSSEEventSpec
	if err := json.Unmarshal(rec.Body.Bytes(), &specs); err != nil {
		t.Fatalf("schema JSON invalid: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("schema empty")
	}
	events := make(map[string]bool)
	for _, s := range specs {
		events[s.Event] = true
	}
	for _, want := range []string{"connected", "turn_start", "turn_end", "assistant_delta", "approval_requested", "question_asked", "heartbeat"} {
		if !events[want] {
			t.Errorf("schema missing event %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/input — 输入注入
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIInput_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no active chat session") {
		t.Fatalf("body = %q, want no-active-session reason", rec.Body.String())
	}
}

func TestHandleChatWebAPIInput_PromptQueued(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"prompt":"hello web"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "queued" {
		t.Fatalf("status = %q, want queued", resp["status"])
	}
	// 输入应真实进入队列
	if count, _ := queuedInteractiveInputState(session); count < 1 {
		t.Fatalf("queued input count = %d, want >= 1", count)
	}
}

func TestHandleChatWebAPIInput_EmptyPrompt(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"prompt":"   "}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPIInput_TextPlainFallback(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader("plain text prompt"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "queued" {
		t.Fatalf("status = %q, want queued", resp["status"])
	}
}

func TestHandleChatWebAPIInput_ApprovalNoActor(t *testing.T) {
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-web-approval"}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"approval","request_id":"req_1","allow":true}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "not_found" {
		t.Fatalf("status = %q, want not_found", resp["status"])
	}
}

func TestHandleChatWebAPIInput_ApprovalMissingRequestID(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"approval","allow":true}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPIInput_QuestionNoActor(t *testing.T) {
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-web-question"}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"question_answer","question_id":"q_1","answer":"yes"}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "not_found" {
		t.Fatalf("status = %q, want not_found", resp["status"])
	}
}

func TestHandleChatWebAPIInput_Interrupt(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "interrupted" {
		t.Fatalf("status = %q, want interrupted", resp["status"])
	}
	// 中断标记应已设置，且未向输入队列注入任何消息。
	if !session.IsInterrupted() {
		t.Fatalf("session interrupted flag not set")
	}
	if count, _ := queuedInteractiveInputState(session); count != 0 {
		t.Fatalf("queued input count = %d, want 0 (interrupt must not enqueue)", count)
	}
	// 清理：避免中断清理协程影响后续测试。
	session.ResetInterrupt()
}

func TestHandleChatWebAPIInput_InterruptNoSession(t *testing.T) {
	withWebTestSession(t, nil)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no active chat session") {
		t.Fatalf("body = %q, want no-active-session reason", rec.Body.String())
	}
}

func TestHandleChatWebAPIInput_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIInputPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/events — SSE
// ---------------------------------------------------------------------------

// TestHandleChatWebAPIEvents_Connected 验证首事件为 connected，
// 且连接取消后 handler 正常退出（无泄漏）。
func TestHandleChatWebAPIEvents_Connected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIEventsPath, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleChatWebAPIEvents(rec, req)
	}()

	// 等待 handler 写入 connected 后取消连接。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.BodyString(), "event: connected") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after context cancel")
	}

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.BodyString()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("SSE body missing connected event: %q", body)
	}
	if !strings.Contains(body, `"schema_version":"skill_runtime.sse.v1"`) {
		t.Fatalf("SSE body missing schema_version envelope: %q", body)
	}
	if !strings.Contains(body, "session_active") {
		t.Fatalf("SSE connected data missing session_active: %q", body)
	}
}

// TestHandleChatWebAPIEvents_ForwardsBusEvents 验证 EventBus 事件被映射转发。
func TestHandleChatWebAPIEvents_ForwardsBusEvents(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-sse-forward"}
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: bus}
	withWebTestSession(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIEventsPath, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleChatWebAPIEvents(rec, req)
	}()

	// 等 resubscribe 循环订阅上 EventBus。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bus.Publish(runtimeevents.Event{
			Type:      runtimechat.EventLLMRequestStarted,
			SessionID: "session-sse-forward",
			Payload: map[string]interface{}{
				"turn_id": "turn-1", "request_id": "req-1", "model": "test-model",
			},
		})
		if strings.Contains(rec.BodyString(), "event: turn_start") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after context cancel")
	}

	body := rec.BodyString()
	if !strings.Contains(body, "event: turn_start") {
		t.Fatalf("SSE body missing turn_start: %q", body)
	}
	if !strings.Contains(body, `"model":"test-model"`) {
		t.Fatalf("SSE turn_start missing model field: %q", body)
	}
}

// ---------------------------------------------------------------------------
// 事件映射单元测试
// ---------------------------------------------------------------------------

func TestChatWebSSEEventName(t *testing.T) {
	cases := []struct {
		busEvent string
		want     string
		mapped   bool
	}{
		{runtimechat.EventLLMRequestStarted, "turn_start", true},
		{"llm.request.started", "turn_start", true},
		{runtimechat.EventAssistantReasoningDelta, "reasoning_delta", true},
		{runtimechat.EventAssistantReasoning, "reasoning_delta", true},
		{runtimechat.EventLLMRequestFinished, "turn_end", true},
		{"llm.request.finished", "turn_end", true},
		{runtimechat.EventApprovalRequested, "approval_requested", true},
		{runtimechat.EventQuestionAsked, "question_asked", true},
		{"unknown_event_type", "unknown_event_type", false},
	}
	for _, c := range cases {
		got, mapped := chatWebSSEEventName(c.busEvent)
		if got != c.want || mapped != c.mapped {
			t.Errorf("chatWebSSEEventName(%q) = (%q,%v), want (%q,%v)",
				c.busEvent, got, mapped, c.want, c.mapped)
		}
	}
}

func TestChatWebSSEDataForEvent(t *testing.T) {
	// 1) 旧版 "text" 键（兼容 fallback）
	data := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"turn_id": "t1", "stream_id": "st1", "sequence": 3, "text": "hello",
		},
	})
	if data["text"] != "hello" || data["turn_id"] != "t1" {
		t.Fatalf("unexpected data: %#v", data)
	}
	if data["session_id"] != "s1" {
		t.Fatalf("session_id missing: %#v", data)
	}

	// 2) 新版 "delta" 键 → "text"
	data2 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"turn_id": "t1", "stream_id": "st1", "sequence": 1, "delta": "Hel",
		},
	})
	if data2["text"] != "Hel" {
		t.Fatalf("delta→text failed: %#v", data2)
	}
	if _, ok := data2["delta"]; ok {
		t.Fatalf("delta should not be in output: %#v", data2)
	}

	// 3) "content" 键 fallback → "text"
	data3 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		Payload:   map[string]interface{}{"content": "lo", "turn_id": "t1"},
	})
	if data3["text"] != "lo" {
		t.Fatalf("content→text fallback failed: %#v", data3)
	}

	// 4) reasoning_delta 嵌套 reasoning → "content"
	data4 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoningDelta,
		Payload:   map[string]interface{}{"reasoning": map[string]interface{}{"summary": "thinking text"}},
	})
	if data4["content"] != "thinking text" {
		t.Fatalf("reasoning→content failed: %#v", data4)
	}

	// 5) reasoning_delta reasoning 为 string → "content"
	data5 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoningDelta,
		Payload:   map[string]interface{}{"reasoning": "raw thinking"},
	})
	if data5["content"] != "raw thinking" {
		t.Fatalf("reasoning string→content failed: %#v", data5)
	}
}

func TestChatWebConnectedPayload_NoSession(t *testing.T) {
	payload := chatWebConnectedPayload(nil)
	if payload["session_active"] != false {
		t.Fatalf("session_active = %v, want false", payload["session_active"])
	}
	if payload["server_version"] == "" {
		t.Fatal("server_version empty")
	}
}

// ---------------------------------------------------------------------------
// 并发安全冒烟测试：多个 Web 输入同时注入不 panic。
// ---------------------------------------------------------------------------

func TestChatWebInputConcurrent(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
				strings.NewReader(`{"prompt":"concurrent `+string(rune('a'+n))+`"}`))
			rec := httptest.NewRecorder()
			HandleChatWebAPIInput(rec, req)
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// GET /web/api/sessions — 会话列表
// ---------------------------------------------------------------------------

func TestHandleChatWebAPISessions_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
		Current  string                   `json:"current_session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Sessions) != 0 {
		t.Fatalf("sessions len = %d, want 0", len(body.Sessions))
	}
}

func TestHandleChatWebAPISessions_NoSessionManager(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
		Current  string                   `json:"current_session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Sessions) != 0 {
		t.Fatalf("sessions len = %d, want 0", len(body.Sessions))
	}
}

func TestHandleChatWebAPISessions_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/resume — 恢复会话
// ---------------------------------------------------------------------------

func TestHandleChatWebAPISessionsResume_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"test-id"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_InvalidJSON(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_EmptySessionID(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":""}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_NoSessionManager(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"test-id"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsResumePath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/sessions — 带真实存储的完整测试
// ---------------------------------------------------------------------------

// newWebTestSessionWithManager 构造一个带 InMemoryStorage SessionManager 的测试会话。
func newWebTestSessionWithManager(t *testing.T) *ChatSession {
	t.Helper()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	runtimeSession.Metadata.Title = "Test Session Title"
	_ = storage.Save(ctx, runtimeSession)

	session := newWebTestSession()
	session.SessionManager = manager
	session.SessionUserID = "test-user"
	session.RuntimeSession = runtimeSession
	return session
}

func TestHandleChatWebAPISessions_OrderingCurrentNotPinned(t *testing.T) {
	// 排序语义：
	//   - 默认（无 sort 参数）与 ?sort=created_at：按创建时间降序
	//   - ?sort=updated_at：按更新时间降序
	// 两种模式下当前会话都不置顶（带 current 标记但不排第一）。
	// 构造 CreatedAt 与 UpdatedAt 顺序不同的会话（oldest 最后再 Save 一次），
	// 使两种排序产生不同结果，验证排序参数真实生效。
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	mk := func(title string) *runtimechat.Session {
		t.Helper()
		s, err := manager.Create(ctx, "test-user")
		if err != nil {
			t.Fatalf("manager.Create: %v", err)
		}
		s.Metadata.Title = title
		s.AddMessage(*runtimetypes.NewUserMessage("conversation seed"))
		if err := storage.Save(ctx, s); err != nil {
			t.Fatalf("storage.Save: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // 保证下一个 Create/Save 的时间戳严格更新
		return s
	}

	oldestCreated := mk("oldest-created") // CreatedAt 最旧
	current := mk("current-mid")          // 创建于中间，设为当前会话
	newestCreated := mk("newest-created") // CreatedAt 最新
	time.Sleep(2 * time.Millisecond)
	// 再 Save 一次 oldestCreated：其 UpdatedAt 变成最新，与 CreatedAt 顺序相反。
	if err := storage.Save(ctx, oldestCreated); err != nil {
		t.Fatalf("storage.Save(oldestCreated): %v", err)
	}

	session := newWebTestSession()
	session.SessionManager = manager
	session.SessionUserID = "test-user"
	session.RuntimeSession = current
	withWebTestSession(t, session)

	fetchOrder := func(sortParam string) ([]string, string) {
		t.Helper()
		target := ChatWebAPISessionsPath
		if sortParam != "" {
			target += "?sort=" + sortParam
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		HandleChatWebAPISessions(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var body struct {
			Sessions []chatWebSessionListItem `json:"sessions"`
			Current  string                   `json:"current_session_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(body.Sessions) != 3 {
			t.Fatalf("sessions len = %d, want 3", len(body.Sessions))
		}
		if body.Current != current.ID {
			t.Fatalf("current_session_id = %q, want %q", body.Current, current.ID)
		}
		gotIDs := make([]string, 0, len(body.Sessions))
		var gotCurrent string
		for _, item := range body.Sessions {
			gotIDs = append(gotIDs, item.ID)
			if item.Current {
				gotCurrent = item.ID
			}
		}
		return gotIDs, gotCurrent
	}

	assertOrder := func(sortParam string, wantOrder []string) {
		t.Helper()
		gotIDs, gotCurrent := fetchOrder(sortParam)
		for i, want := range wantOrder {
			if gotIDs[i] != want {
				t.Fatalf("sort=%q order[%d] = %q, want %q (full: %v)", sortParam, i, gotIDs[i], want, gotIDs)
			}
		}
		if gotCurrent != current.ID {
			t.Fatalf("sort=%q current flag on %q, want %q", sortParam, gotCurrent, current.ID)
		}
	}

	// 创建时间降序：newest > current > oldest；当前会话（中间创建）不置顶。
	createdOrder := []string{newestCreated.ID, current.ID, oldestCreated.ID}
	assertOrder("", createdOrder)
	assertOrder("created_at", createdOrder)
	// 更新时间降序：oldestCreated（最后 Save）> newest > current。
	assertOrder("updated_at", []string{oldestCreated.ID, newestCreated.ID, current.ID})
}

func TestHandleChatWebAPISessions_WithManager(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
		Current  string                   `json:"current_session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(body.Sessions))
	}
	item := body.Sessions[0]
	if !item.Current {
		t.Fatalf("current = false, want true")
	}
	if item.Title != "Test Session Title" {
		t.Fatalf("title = %q, want %q", item.Title, "Test Session Title")
	}
	if item.ID == "" {
		t.Fatalf("id is empty")
	}
}

func TestHandleChatWebAPISessionsResume_AlreadyCurrent(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	currentID := currentRuntimeSessionID(session)
	if currentID == "" {
		t.Fatal("current session id is empty")
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"`+currentID+`"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Status != "already_current" {
		t.Fatalf("status = %q, want %q", body.Status, "already_current")
	}
}

func TestHandleChatWebAPISessionsResume_SessionNotFound(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"non-existent-id"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_SessionBelongsToOtherUser(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	// 同一 manager 中创建另一个用户的会话
	other, err := session.SessionManager.Create(context.Background(), "other-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"`+other.ID+`"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
}
