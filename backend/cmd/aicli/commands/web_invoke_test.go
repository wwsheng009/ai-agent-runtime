package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 回归（实测，独立进程 + /web/api/invoke）：第二次 invoke 时模型回复与上一轮
// 完全相同（两次都是"收到"），旧判定 `assistant != baselineAssistant` 只比文本，
// 于是把合法回复判成"没有新消息"，响应里 assistant 为空——而 TUI 与会话里回复都在。
// 判据改为"文本不同 或 assistant 消息条数增长"。
func TestChatWebInvokeFinalizeKeepsRepeatedAssistantReply(t *testing.T) {
	stubChatWebInvokeProbe(t)
	session := &ChatSession{Messages: []runtimetypes.Message{
		{Role: "user", Content: "只回复两个字：收到"},
		{Role: "assistant", Content: "收到"},
	}}
	baseline := chatWebInvokeAssistantContent(session)
	watch := newChatWebInvokeWatch()
	watch.baselineAssistantCount = chatWebInvokeAssistantMessageCount(session)

	// 第二轮：同文本回复，会话里多出一条 assistant 消息。
	session.Messages = append(session.Messages,
		runtimetypes.Message{Role: "user", Content: "只回复两个字：收到"},
		runtimetypes.Message{Role: "assistant", Content: "收到"},
	)

	resp := chatWebInvokeFinalize(&chatWebInvokeResponse{}, session, watch, baseline, "completed", "")
	if resp.Assistant == nil || resp.Assistant.Content != "收到" {
		t.Fatalf("与上一轮同文本的新回复不能被丢弃: %+v", resp.Assistant)
	}
}

// 反向纪律：turn 没有产生新回复（条数不变、文本等于基线）时不得回显基线，
// 否则调用方会把"上一轮的回复"读成"本轮回复"。
func TestChatWebInvokeFinalizeDoesNotEchoBaselineAssistant(t *testing.T) {
	stubChatWebInvokeProbe(t)
	session := &ChatSession{Messages: []runtimetypes.Message{
		{Role: "assistant", Content: "收到"},
	}}
	baseline := chatWebInvokeAssistantContent(session)
	watch := newChatWebInvokeWatch()
	watch.baselineAssistantCount = chatWebInvokeAssistantMessageCount(session)

	resp := chatWebInvokeFinalize(&chatWebInvokeResponse{}, session, watch, baseline, "timeout", "")
	if resp.Assistant != nil {
		t.Fatalf("没有新回复时不应回显基线: %+v", resp.Assistant)
	}
}

func stubChatWebInvokeProbe(t *testing.T) {
	t.Helper()
	prev := chatWebInvokeProbeFn
	chatWebInvokeProbeFn = func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session-invoke-test", "turn-invoke-test", false, nil, nil
	}
	t.Cleanup(func() { chatWebInvokeProbeFn = prev })
}

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

// TestChatWebInvokeWatch_TurnIDBackfilledOnFinalize 锁定终态 turn 身份回填：
// actor 在 turn 收尾后清空 state.CurrentTurnID，invoke 响应必须改用观察器从
// session_start/session_end 事件记录的 turn 身份，调用方才能直接凭响应里的
// turn_id 走 /web/api/turn 后验（而不必扫描 recent 列表交叉核对）。
func TestChatWebInvokeWatch_TurnIDBackfilledOnFinalize(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session_a"},
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
	}
	watch := newChatWebInvokeWatch()
	unsubscribe := subscribeChatWebInvokeWatch(session, watch)
	defer unsubscribe()

	// 其他会话的生命周期事件不得污染 turn 身份。
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_b",
		Payload: map[string]interface{}{"turn_id": "turn_foreign"},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_a",
		Payload: map[string]interface{}{"turn_id": "turn_e2e_1"},
	})
	if got := watch.snapshot().TurnID; got != "turn_e2e_1" {
		t.Fatalf("watch turn id = %q, want turn_e2e_1", got)
	}

	// 终态探测拿不到 turn（模拟 turn 结束后 CurrentTurnID 已清空）。
	withStubbedInvokeProbe(t, func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session_a", "", false, nil, nil
	})
	resp := chatWebInvokeFinalize(&chatWebInvokeResponse{}, session, watch, "", "completed", "")
	if resp.TurnID != "turn_e2e_1" {
		t.Fatalf("resp turn id = %q, want turn_e2e_1 (backfilled from watch)", resp.TurnID)
	}

	// state 探测仍能给出 turn（运行中）时以 state 为准，不被回填覆盖。
	withStubbedInvokeProbe(t, func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session_a", "turn_live_2", true, nil, nil
	})
	respLive := chatWebInvokeFinalize(&chatWebInvokeResponse{}, session, watch, "", "timeout", "")
	if respLive.TurnID != "turn_live_2" {
		t.Fatalf("resp turn id = %q, want turn_live_2 (state probe wins)", respLive.TurnID)
	}
}

// ---------------------------------------------------------------------------
// handler 级：wait_only / 幂等 / 会话校验 / 流式（P1b + P2）
// ---------------------------------------------------------------------------

// withStubbedInvokeProbe 临时替换运行状态探测器。
func withStubbedInvokeProbe(t *testing.T, fn func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{})) {
	t.Helper()
	prev := chatWebInvokeProbeFn
	chatWebInvokeProbeFn = fn
	t.Cleanup(func() { chatWebInvokeProbeFn = prev })
}

func idleInvokeProbe() func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
	return func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "", "", false, nil, nil
	}
}

func TestHandleChatWebAPIInvokeSessionMismatch(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "session_a"},
		InputQueue:     newChatInputQueue(nil),
	}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, "/web/api/invoke",
		strings.NewReader(`{"prompt":"hi","session_id":"session_b"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session_id mismatch") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if session.InputQueue.queuedSubmissionCount() != 0 {
		t.Fatal("mismatched session must not receive the prompt")
	}
}

// TestHandleChatWebAPIInvokeWaitOnlyBusyTimeout 锁定忙碌会话的 wait_only 语义：
// 不短路，等到 deadline 后按 timeout 返回（空闲短路见
// TestHandleChatWebAPIInvoke_WaitOnlyIdleShortCircuit）。
func TestHandleChatWebAPIInvokeWaitOnlyBusyTimeout(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	withStubbedInvokeProbe(t, func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session_test", "turn_busy", true, nil, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/web/api/invoke",
		strings.NewReader(`{"wait_only":true,"timeout_ms":1000}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp chatWebInvokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if resp.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", resp.Status)
	}
	if resp.Queued {
		t.Fatal("wait_only must not report queued")
	}
	if session.InputQueue.queuedSubmissionCount() != 0 {
		t.Fatal("wait_only must not inject any prompt")
	}
}

func TestHandleChatWebAPIInvokeIdempotentReplay(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	key := chatWebInvokeIdemKey("", "req-1")
	storeChatWebInvokeIdem(key, &chatWebInvokeResponse{
		Status:    "completed",
		Assistant: &chatWebScreenMessage{Role: "assistant", Content: "hi"},
	})
	t.Cleanup(func() {
		chatWebInvokeIdem.mu.Lock()
		delete(chatWebInvokeIdem.m, key)
		chatWebInvokeIdem.mu.Unlock()
	})

	req := httptest.NewRequest(http.MethodPost, "/web/api/invoke",
		strings.NewReader(`{"prompt":"hi","client_request_id":"req-1"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp chatWebInvokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Duplicate {
		t.Fatalf("duplicate = false, want replay (resp=%+v)", resp)
	}
	if resp.Status != "completed" || resp.Assistant == nil || resp.Assistant.Content != "hi" {
		t.Fatalf("replayed response mismatch: %+v", resp)
	}
	if session.InputQueue.queuedSubmissionCount() != 0 {
		t.Fatal("replay must not inject the prompt again")
	}
}

func TestChatWebInvokeIdempotencyStoreRules(t *testing.T) {
	key := chatWebInvokeIdemKey("session_x", "k1")
	storeChatWebInvokeIdem(key, &chatWebInvokeResponse{Status: "settled"})
	if cached, ok := lookupChatWebInvokeIdem(key); !ok || !cached.Duplicate || cached.Status != "settled" {
		t.Fatalf("lookup = (%+v, %v), want settled duplicate", cached, ok)
	}

	// 过期条目不回放。
	chatWebInvokeIdem.mu.Lock()
	entry := chatWebInvokeIdem.m[key]
	entry.at = time.Now().Add(-2 * chatWebInvokeIdemTTL)
	chatWebInvokeIdem.m[key] = entry
	chatWebInvokeIdem.mu.Unlock()
	if _, ok := lookupChatWebInvokeIdem(key); ok {
		t.Fatal("expired entry must not replay")
	}

	// rejected 等无副作用结果不固化。
	rejectedKey := chatWebInvokeIdemKey("session_x", "k2")
	storeChatWebInvokeIdem(rejectedKey, &chatWebInvokeResponse{Status: "rejected"})
	if _, ok := lookupChatWebInvokeIdem(rejectedKey); ok {
		t.Fatal("rejected result must not be replayable")
	}
}

// TestHandleChatWebAPIInvokeStreaming 覆盖 SSE 结果帧：空闲短路（settled，
// 立即返回）与忙碌等待（timeout）两条路径都必须写出 result 帧。
func TestHandleChatWebAPIInvokeStreaming(t *testing.T) {
	cases := []struct {
		name       string
		probe      func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{})
		wantStatus string
	}{
		{name: "空闲短路", probe: idleInvokeProbe(), wantStatus: "settled"},
		{name: "忙碌等待到超时", probe: func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
			return "session_test", "turn_busy", true, nil, nil
		}, wantStatus: "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := newWebTestSession()
			withWebTestSession(t, session)
			withStubbedInvokeProbe(t, tc.probe)

			req := httptest.NewRequest(http.MethodPost, "/web/api/invoke",
				strings.NewReader(`{"wait_only":true,"timeout_ms":1000}`))
			req.Header.Set("Accept", "text/event-stream")
			rec := httptest.NewRecorder()
			HandleChatWebAPIInvoke(rec, req)

			out := rec.Body.String()
			if !strings.Contains(out, "event: start") {
				t.Fatalf("missing start frame: %s", out)
			}
			if !strings.Contains(out, "event: result") {
				t.Fatalf("missing result frame: %s", out)
			}
			if !strings.Contains(out, `"status":"`+tc.wantStatus+`"`) {
				t.Fatalf("result frame missing status %s: %s", tc.wantStatus, out)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
				t.Fatalf("content-type = %q", ct)
			}
		})
	}
}

func TestChatWebInvokeWatchStreamsDeltas(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "s1"},
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
	}
	watch := newChatWebInvokeWatch()
	var mu sync.Mutex
	var got []string
	watch.onStream = func(event string, data map[string]interface{}, sourceEvent string) {
		mu.Lock()
		got = append(got, event+"="+payloadStringValue(data["delta"])+payloadStringValue(data["name"]))
		mu.Unlock()
	}
	unsubscribe := subscribeChatWebInvokeWatch(session, watch)
	defer unsubscribe()

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "s1",
		Payload: map[string]interface{}{"delta": "he"}})
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "s2",
		Payload: map[string]interface{}{"delta": "other-session"}})
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventToolStarted, SessionID: "s1",
		Payload: map[string]interface{}{"name": "shell"}})

	mu.Lock()
	joined := strings.Join(got, ",")
	mu.Unlock()
	if !strings.Contains(joined, "assistant_delta=he") {
		t.Fatalf("delta not streamed: %q", joined)
	}
	if strings.Contains(joined, "other-session") {
		t.Fatalf("other session event leaked into stream: %q", joined)
	}
	if !strings.Contains(joined, "tool_started=shell") {
		t.Fatalf("tool event not streamed: %q", joined)
	}
}

func TestChatWebInvokeUsageFromSession(t *testing.T) {
	if chatWebInvokeUsageFrom(nil) != nil {
		t.Fatal("nil session must yield nil usage")
	}
	if chatWebInvokeUsageFrom(&ChatSession{}) != nil {
		t.Fatal("empty counters must yield nil usage")
	}
	session := &ChatSession{
		InputTokenCount:         10,
		OutputTokenCount:        2,
		TokenCount:              12,
		ContextTokenCount:       8,
		ContextWindowTokenCount: 1000,
	}
	usage := chatWebInvokeUsageFrom(session)
	if usage == nil {
		t.Fatal("usage = nil, want counters")
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 2 || usage.TotalTokens != 12 ||
		usage.ContextTokens != 8 || usage.ContextWindowTokens != 1000 {
		t.Fatalf("usage = %+v", usage)
	}
}

// TestHandleChatWebAPIInvoke_WaitOnlyIdleShortCircuit 锁定 wait_only 的空闲
// 短路：会话本来空闲时立即返回 settled（reason 说明无事可等），不空等 timeout_ms。
func TestHandleChatWebAPIInvoke_WaitOnlyIdleShortCircuit(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	withStubbedInvokeProbe(t, func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session_test", "", false, nil, nil
	})

	start := time.Now()
	rec := httptest.NewRecorder()
	HandleChatWebAPIInvoke(rec, httptest.NewRequest(http.MethodPost, ChatWebAPIInvokePath,
		strings.NewReader(`{"wait_only":true,"timeout_ms":60000}`)))
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp chatWebInvokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if resp.Status != "settled" {
		t.Fatalf("wait_only 空闲应返回 settled，实际 %q (reason=%s)", resp.Status, resp.Reason)
	}
	if !strings.Contains(resp.Reason, "idle") {
		t.Fatalf("reason 应说明会话已空闲: %q", resp.Reason)
	}
	if resp.Busy {
		t.Fatalf("idle 短路不应报告 busy: %+v", resp)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("空闲短路应立即返回，实际耗时 %s", elapsed)
	}
	if session.InputQueue != nil && session.InputQueue.queuedSubmissionCount() != 0 {
		t.Fatalf("wait_only 不得注入输入，队列剩余 %d", session.InputQueue.queuedSubmissionCount())
	}
}
