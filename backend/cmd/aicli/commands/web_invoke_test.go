package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ---------------------------------------------------------------------------
// 纯判定函数
// ---------------------------------------------------------------------------

func TestChatWebInvokeDecide(t *testing.T) {
	base := chatWebInvokeSample{
		Consumed:         true,
		BusyAfterConsume: true,
		Starts:           1,
		Finishes:         1,
		SinceActivity:    2 * time.Second,
		SinceConsumed:    5 * time.Second,
		Quiet:            chatWebInvokeQuietWindow,
		NoLLMGrace:       chatWebInvokeNoLLMGrace,
	}
	tests := []struct {
		name       string
		mutate     func(*chatWebInvokeSample)
		wantStatus string
		wantDone   bool
	}{
		{"completed", func(*chatWebInvokeSample) {}, "completed", true},
		{"busy still running", func(s *chatWebInvokeSample) { s.Busy = true }, "", false},
		{"pending input", func(s *chatWebInvokeSample) { s.Pending = 1 }, "", false},
		{"quiet window not reached", func(s *chatWebInvokeSample) { s.SinceActivity = 100 * time.Millisecond }, "", false},
		{"finish not observed", func(s *chatWebInvokeSample) { s.Finishes = 0 }, "", false},
		{"settled no-llm path", func(s *chatWebInvokeSample) {
			s.BusyAfterConsume = false
			s.Starts = 0
			s.Finishes = 0
		}, "settled", true},
		{"settled grace not reached", func(s *chatWebInvokeSample) {
			s.BusyAfterConsume = false
			s.Starts = 0
			s.Finishes = 0
			s.SinceConsumed = 500 * time.Millisecond
		}, "", false},
		{"interrupted", func(s *chatWebInvokeSample) { s.Interrupted = true }, "interrupted", true},
		{"interrupted but still busy", func(s *chatWebInvokeSample) {
			s.Interrupted = true
			s.Busy = true
		}, "", false},
		{"requires approval", func(s *chatWebInvokeSample) {
			s.PendingApproval = true
			s.AttentionStable = true
			s.Busy = true
		}, "requires_approval", true},
		{"approval unstable", func(s *chatWebInvokeSample) {
			s.PendingApproval = true
			s.Busy = true
		}, "", false},
		{"requires answer", func(s *chatWebInvokeSample) {
			s.PendingQuestion = true
			s.AttentionStable = true
			s.Busy = true
		}, "requires_answer", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sample := base
			tt.mutate(&sample)
			status, done := chatWebInvokeDecide(sample)
			if status != tt.wantStatus || done != tt.wantDone {
				t.Fatalf("chatWebInvokeDecide = (%q, %v), want (%q, %v)", status, done, tt.wantStatus, tt.wantDone)
			}
		})
	}
}

func TestChatWebInvokeTimeoutClamp(t *testing.T) {
	if got := chatWebInvokeTimeout(0); got != chatWebInvokeDefaultTimeoutMs*time.Millisecond {
		t.Fatalf("default timeout = %v", got)
	}
	if got := chatWebInvokeTimeout(10); got != chatWebInvokeMinTimeoutMs*time.Millisecond {
		t.Fatalf("min clamp = %v", got)
	}
	if got := chatWebInvokeTimeout(chatWebInvokeMaxTimeoutMs * 10); got != chatWebInvokeMaxTimeoutMs*time.Millisecond {
		t.Fatalf("max clamp = %v", got)
	}
	if got := chatWebInvokeTimeout(2500); got != 2500*time.Millisecond {
		t.Fatalf("passthrough = %v", got)
	}
}

// ---------------------------------------------------------------------------
// Handler 错误路径
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIInvoke_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIInvokePath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIInvoke_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInvokePath,
		strings.NewReader(`{"prompt":"hello"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "no active chat session") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIInvoke_EmptyPrompt(t *testing.T) {
	withWebTestSession(t, newWebTestSession())
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInvokePath, strings.NewReader("   "))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "empty prompt") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIInvoke_ConflictWhenInvokeInProgress(t *testing.T) {
	webInvokeMu.Lock()
	defer webInvokeMu.Unlock()

	withWebTestSession(t, newWebTestSession())
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInvokePath,
		strings.NewReader(`{"prompt":"hello"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "another /web/api/invoke") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIInvoke_RejectedByCommandGate(t *testing.T) {
	session := newWebTestSession()
	session.InputQueue.setCommandGate(func(string) bool { return false })
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInvokePath,
		strings.NewReader(`{"prompt":"/exit","timeout_ms":1000}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp chatWebInvokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// Handler 等待路径：无主循环消费时到达超时，仍返回渲染快照与状态字段
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIInvoke_TimeoutReturnsRender(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	started := time.Now()
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInvokePath,
		strings.NewReader(`{"prompt":"hello","timeout_ms":1000}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)
	if elapsed := time.Since(started); elapsed < chatWebInvokeMinTimeoutMs*time.Millisecond/2 {
		t.Fatalf("invoke returned too early: %v", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp chatWebInvokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", resp.Status)
	}
	if !resp.Queued {
		t.Fatalf("expected queued=true, got %+v", resp)
	}
	if resp.Screen == nil {
		t.Fatalf("expected screen snapshot in response, got %+v", resp)
	}
	if resp.LLMObserved {
		t.Fatalf("expected llm_observed=false, got %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/screen?view=tui
// ---------------------------------------------------------------------------

// TestChatWebInvokeWait_CompletedAfterTurnEvents 用真实 EventBus + 可替换的
// 状态探测驱动等待循环，覆盖"输入被消费 → turn 忙碌 → 事件完成 → 会话空闲
// → completed"的主路径（不依赖真实 LLM）。
func TestChatWebInvokeWait_CompletedAfterTurnEvents(t *testing.T) {
	queue := newChatInputQueue(nil)
	if result := queue.routeInputText("hello"); !result.queued() {
		t.Fatal("route input failed")
	}
	session := &ChatSession{
		InputQueue:       queue,
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: runtimeevents.NewBus()},
	}
	withWebTestSession(t, session)

	var busy atomic.Bool
	busy.Store(true)
	prevProbe := chatWebInvokeProbeFn
	chatWebInvokeProbeFn = func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session_test", "turn_1", busy.Load(), nil, nil
	}
	t.Cleanup(func() { chatWebInvokeProbeFn = prevProbe })

	// 模拟主循环消费注入的 prompt：队列计数回到 0。
	go func() {
		time.Sleep(50 * time.Millisecond)
		select {
		case <-queue.lines:
		default:
		}
	}()

	// 模拟 turn：开始 → assistant 完整消息 → 结束；随后会话空闲。
	go func() {
		time.Sleep(150 * time.Millisecond)
		session.LocalRuntimeHost.EventBus.Publish(runtimeevents.Event{
			Type: runtimechat.EventLLMRequestStarted, SessionID: "session_test",
		})
		time.Sleep(150 * time.Millisecond)
		session.LocalRuntimeHost.EventBus.Publish(runtimeevents.Event{
			Type:    runtimechat.EventAssistantMessage,
			Payload: map[string]interface{}{"content": "最终回复"},
		})
		session.LocalRuntimeHost.EventBus.Publish(runtimeevents.Event{
			Type: runtimechat.EventLLMRequestFinished, SessionID: "session_test",
		})
		time.Sleep(50 * time.Millisecond)
		busy.Store(false)
	}()

	watch := newChatWebInvokeWatch()
	unsubscribe := subscribeChatWebInvokeWatch(session, watch)
	defer unsubscribe()

	resp := chatWebInvokeWait(context.Background(), session, watch, "", time.Now().Add(5*time.Second))
	if resp.Status != "completed" {
		t.Fatalf("status = %q, want completed (resp=%+v)", resp.Status, resp)
	}
	if !resp.LLMObserved {
		t.Fatalf("expected llm_observed=true, got %+v", resp)
	}
	if resp.Assistant == nil || resp.Assistant.Content != "最终回复" {
		t.Fatalf("assistant = %+v, want 最终回复", resp.Assistant)
	}
	if resp.Screen == nil {
		t.Fatalf("expected screen snapshot, got %+v", resp)
	}
	if resp.SessionID != "session_test" {
		t.Fatalf("session_id = %q, want session_test", resp.SessionID)
	}
}

func TestHandleChatWebAPIScreen_TUIViewText(t *testing.T) {
	withWebTestSession(t, newWebTestSession())
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?view=tui", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Debug Screen: no active terminal surface") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIScreen_TUIViewJSON(t *testing.T) {
	withWebTestSession(t, newWebTestSession())
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?view=tui&format=json", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var snap chatDebugScreenSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if snap.Available {
		t.Fatalf("expected available=false for session without surface, got %+v", snap)
	}
}

// TestChatWebInvokeWait_SessionSwitchAborts 锁定等待期间会话切换的行为：
// 返回 error + reason，而不是把新会话的空闲状态误判为 completed。
func TestChatWebInvokeWait_SessionSwitchAborts(t *testing.T) {
	sessionA := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "session_a"},
		InputQueue:     newChatInputQueue(nil),
	}
	sessionB := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "session_b"},
		InputQueue:     newChatInputQueue(nil),
	}
	withWebTestSession(t, sessionA)

	var calls atomic.Int32
	prevProbe := chatWebInvokeProbeFn
	chatWebInvokeProbeFn = func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		// 从等待循环所在 goroutine 切换到会话 B（避免跨 goroutine 写全局）。
		if calls.Add(1) == 3 {
			chatDebugDisplaySessionProvider = func() *ChatSession { return sessionB }
		}
		return "session_a", "turn_1", false, nil, nil
	}
	t.Cleanup(func() { chatWebInvokeProbeFn = prevProbe })

	watch := newChatWebInvokeWatch()
	resp := chatWebInvokeWait(context.Background(), sessionA, watch, "", time.Now().Add(5*time.Second))
	if resp.Status != "error" {
		t.Fatalf("status = %q, want error (resp=%+v)", resp.Status, resp)
	}
	if !strings.Contains(resp.Reason, "session switched") {
		t.Fatalf("reason = %q, want session switched", resp.Reason)
	}
}

// TestSubscribeChatWebInvokeWatch_FiltersOtherSessions 锁定观察器只统计绑定
// 会话的事件，避免其他会话的活动拖长 invoke 等待。
func TestSubscribeChatWebInvokeWatch_FiltersOtherSessions(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session_a"},
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
	}
	watch := newChatWebInvokeWatch()
	unsubscribe := subscribeChatWebInvokeWatch(session, watch)
	defer unsubscribe()

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, SessionID: "session_b"})
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, SessionID: "session_a"})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage, SessionID: "session_a",
		Payload: map[string]interface{}{"content": "hi"},
	})

	ws := watch.snapshot()
	if ws.Starts != 1 {
		t.Fatalf("starts = %d, want 1 (other session event must be ignored)", ws.Starts)
	}
	if ws.Assistant != "hi" {
		t.Fatalf("assistant = %q, want hi", ws.Assistant)
	}
}
