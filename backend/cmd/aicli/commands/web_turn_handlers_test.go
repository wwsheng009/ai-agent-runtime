package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// newTurnRecorderTestSession 构造带真实 EventBus 的测试会话，并清理注册表。
func newTurnRecorderTestSession(t *testing.T) (*ChatSession, *runtimeevents.Bus) {
	t.Helper()
	bus := runtimeevents.NewBus()
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session_t"},
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
		InputQueue:       newChatInputQueue(nil),
	}
	withWebTestSession(t, session)
	withStubbedInvokeProbe(t, func(*ChatSession) (string, string, bool, map[string]interface{}, map[string]interface{}) {
		return "session_t", "", false, nil, nil
	})
	t.Cleanup(func() {
		chatWebTurnRecorders.mu.Lock()
		delete(chatWebTurnRecorders.m, bus)
		chatWebTurnRecorders.mu.Unlock()
	})
	return session, bus
}

func TestChatWebTurnRecorderLifecycle(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_1"},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: "session_t",
		Payload: map[string]interface{}{
			"turn_id": "turn_1", "success": true, "steps": 3, "duration": int64(1200),
		},
	})

	// 单条查询：终态 + 耗时字段。
	rec := httptest.NewRecorder()
	HandleChatWebAPITurn(rec, httptest.NewRequest(http.MethodGet, "/web/api/turn?id=turn_1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp chatWebTurnQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Found || resp.Turn == nil {
		t.Fatalf("found = %v turn = %+v", resp.Found, resp.Turn)
	}
	if resp.Turn.Status != "completed" || resp.Turn.Steps != 3 || resp.Turn.SessionID != "session_t" {
		t.Fatalf("turn = %+v", resp.Turn)
	}
	if resp.Turn.DurationMs < 0 || resp.Turn.FinishedAt == "" {
		t.Fatalf("turn timing not recorded: %+v", resp.Turn)
	}
	if resp.Current == nil {
		t.Fatal("current probe missing")
	}

	// 列表查询：最近记录按最新在前返回。
	rec = httptest.NewRecorder()
	HandleChatWebAPITurn(rec, httptest.NewRequest(http.MethodGet, "/web/api/turn", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Recent) != 1 || resp.Recent[0].TurnID != "turn_1" {
		t.Fatalf("recent = %+v", resp.Recent)
	}

	// 未记录 turn：found=false + 解释性 reason。
	rec = httptest.NewRecorder()
	HandleChatWebAPITurn(rec, httptest.NewRequest(http.MethodGet, "/web/api/turn?id=missing", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Found || resp.Reason == "" {
		t.Fatalf("missing turn response = %+v", resp)
	}
}

func TestChatWebTurnRecorderInterruptClosesRunningTurn(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_9"},
	})
	// session_interrupted 不带 turn_id：应关闭该会话最近一条 running 记录。
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionInterrupted, SessionID: "session_t",
	})

	recorder := recorderForSession(session)
	if recorder == nil {
		t.Fatal("recorder not installed")
	}
	record := recorder.lookup("turn_9")
	if record == nil {
		t.Fatal("turn_9 not recorded")
	}
	if record.Status != "interrupted" || record.FinishedAt == "" {
		t.Fatalf("record = %+v", record)
	}
}

func TestChatWebTurnRecorderFailureStatus(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_err"},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: "session_t",
		Payload: map[string]interface{}{
			"turn_id": "turn_err", "success": false, "error": "boom",
		},
	})

	record := recorderForSession(session).lookup("turn_err")
	if record == nil || record.Status != "failed" || record.Error != "boom" {
		t.Fatalf("record = %+v", record)
	}
}

func TestHandleChatWebAPITurnGuards(t *testing.T) {
	// 无会话 → 503。
	withWebTestSession(t, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPITurn(rec, httptest.NewRequest(http.MethodGet, "/web/api/turn", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-session status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no active chat session") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// 非 GET → 405。
	withWebTestSession(t, newWebTestSession())
	rec = httptest.NewRecorder()
	HandleChatWebAPITurn(rec, httptest.NewRequest(http.MethodPost, "/web/api/turn", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

// TestChatWebTurnRecorder_AssistantPreview 锁定 assistant 预览：
// 按 rune 截断（≤200 + 省略号）并记录完整字符数。
func TestChatWebTurnRecorder_AssistantPreview(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_preview"},
	})

	long := strings.Repeat("测", 260)
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage, SessionID: "session_t",
		Payload: map[string]interface{}{"content": long},
	})

	record := recorderForSession(session).lookup("turn_preview")
	if record == nil {
		t.Fatal("turn_preview not recorded")
	}
	if record.AssistantChars != 260 {
		t.Fatalf("assistant_chars = %d, want 260", record.AssistantChars)
	}
	if runes := []rune(record.AssistantPreview); len(runes) != chatWebTurnAssistantPreviewRunes+1 {
		t.Fatalf("preview runes = %d, want %d（含省略号）", len(runes), chatWebTurnAssistantPreviewRunes+1)
	}
	if !strings.HasSuffix(record.AssistantPreview, "…") {
		t.Fatalf("preview 应以省略号结尾: %q", record.AssistantPreview)
	}

	// 短消息：原样保留、无省略号。
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage, SessionID: "session_t",
		Payload: map[string]interface{}{"content": "短回复"},
	})
	record = recorderForSession(session).lookup("turn_preview")
	if record.AssistantPreview != "短回复" || record.AssistantChars != 3 {
		t.Fatalf("short assistant preview = %q chars=%d", record.AssistantPreview, record.AssistantChars)
	}
}

// TestChatWebTurnRecorder_UsageScope 锁定 usage 口径：
// 有增量时报 turn 增量（usage_scope=turn）；增量确实不可得的场景才回退为
// 会话累计快照（usage_scope=session，仅作参考），不再静默报 0。
func TestChatWebTurnRecorder_UsageScope(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	// 场景 1：正常增量。
	session.InputTokenCount, session.OutputTokenCount, session.TokenCount = 100, 20, 120
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_inc"},
	})
	session.InputTokenCount += 1000
	session.OutputTokenCount += 250
	session.TokenCount += 1250
	session.ContextTokenCount = 4096
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage, SessionID: "session_t",
		Payload: map[string]interface{}{"content": "增量口径测试回复"},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_inc", "success": true},
	})

	record := recorderForSession(session).lookup("turn_inc")
	if record == nil || record.Usage == nil {
		t.Fatalf("turn_inc usage missing: %+v", record)
	}
	if record.Usage.InputTokens != 1000 || record.Usage.OutputTokens != 250 || record.Usage.TotalTokens != 1250 {
		t.Fatalf("turn 增量 = %+v, want 1000/250/1250", record.Usage)
	}
	if record.Usage.ContextTokens != 4096 {
		t.Fatalf("context_tokens = %d, want 4096", record.Usage.ContextTokens)
	}
	if record.UsageScope != chatWebTurnUsageScopeTurn {
		t.Fatalf("usage_scope = %q, want %q", record.UsageScope, chatWebTurnUsageScopeTurn)
	}

	// 场景 2：计数未再变化（增量为 0）且累计非 0 → 回退 session 快照。
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_zero"},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_zero", "success": true},
	})
	record = recorderForSession(session).lookup("turn_zero")
	if record == nil || record.Usage == nil {
		t.Fatalf("turn_zero usage missing: %+v", record)
	}
	if record.Usage.InputTokens != 1100 || record.Usage.OutputTokens != 270 || record.Usage.TotalTokens != 1370 {
		t.Fatalf("回退快照 = %+v, want 1100/270/1370（会话累计，仅参考）", record.Usage)
	}
	if record.UsageScope != chatWebTurnUsageScopeSession {
		t.Fatalf("usage_scope = %q, want %q", record.UsageScope, chatWebTurnUsageScopeSession)
	}

	// 线级契约：JSON 字段名必须稳定（脚本按名字读），且两条记录的 preview 一并在响应里。
	body := httptest.NewRecorder()
	HandleChatWebAPITurn(body, httptest.NewRequest(http.MethodGet, "/web/api/turn?id=turn_inc", nil))
	raw := body.Body.String()
	for _, want := range []string{`"usage_scope":"turn"`, `"assistant_preview":`, `"assistant_chars":`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("turn 响应缺少 %s: %s", want, raw)
		}
	}
}

// TestChatWebTurnRecorder_DuplicateStartKeepsBaseline 锁定重复 session_start
// （重试/自动续跑）不覆盖 token 增量基线：两次 start 前后的增量都计入本轮。
func TestChatWebTurnRecorder_DuplicateStartKeepsBaseline(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	startEvent := runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_dup"},
	}
	bus.Publish(startEvent)
	session.InputTokenCount += 400
	session.TokenCount += 400
	bus.Publish(startEvent) // 重复 start：不得把基线推到 400
	session.InputTokenCount += 600
	session.TokenCount += 600
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_dup", "success": true},
	})

	record := recorderForSession(session).lookup("turn_dup")
	if record == nil || record.Usage == nil {
		t.Fatalf("turn_dup usage missing: %+v", record)
	}
	if record.Usage.InputTokens != 1000 || record.Usage.TotalTokens != 1000 {
		t.Fatalf("delta = %+v, want 1000（含重复 start 之前的 400）", record.Usage)
	}
	if record.UsageScope != chatWebTurnUsageScopeTurn {
		t.Fatalf("usage_scope = %q, want %q", record.UsageScope, chatWebTurnUsageScopeTurn)
	}
}

// TestChatWebTurnRecorder_UsageFromEventPayload 锁定首选口径：终态事件载荷
// 自带 usage_*（actor 结算时写入 result.Usage）。即使会话计数器在事件之后
// 才落账（增量为 0），也必须以载荷为准报 usage_scope=turn，而不是回退累计
// 快照或报 0——这正是"0/0/0 与口径漂移"的根因场景。
func TestChatWebTurnRecorder_UsageFromEventPayload(t *testing.T) {
	session, bus := newTurnRecorderTestSession(t)
	ensureChatWebTurnRecorder(session)

	// 会话计数器先保持不动（模拟"事件先到、账后到"）。
	session.InputTokenCount, session.OutputTokenCount, session.TokenCount = 33015, 281, 33296
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: "session_t",
		Payload: map[string]interface{}{"turn_id": "turn_payload"},
	})
	bus.Publish(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: "session_t",
		Payload: map[string]interface{}{
			"turn_id":                 "turn_payload",
			"success":                 true,
			"steps":                   11,
			"usage_prompt_tokens":     369602,
			"usage_completion_tokens": 10968,
			"usage_total_tokens":      380570,
			"usage_cached_tokens":     1024,
			"usage_source":            "provider_reported",
		},
	})

	record := recorderForSession(session).lookup("turn_payload")
	if record == nil || record.Usage == nil {
		t.Fatalf("turn_payload usage missing: %+v", record)
	}
	if record.Usage.InputTokens != 369602 || record.Usage.OutputTokens != 10968 || record.Usage.TotalTokens != 380570 {
		t.Fatalf("载荷口径 usage = %+v, want 369602/10968/380570", record.Usage)
	}
	if record.UsageScope != chatWebTurnUsageScopeTurn {
		t.Fatalf("usage_scope = %q, want %q（载荷即本轮增量）", record.UsageScope, chatWebTurnUsageScopeTurn)
	}
	if record.UsageSource != "provider_reported" {
		t.Fatalf("usage_source = %q, want provider_reported", record.UsageSource)
	}

	// 线级契约：usage_source 必须出现在 JSON 里（脚本据此判断真报/估算）。
	body := httptest.NewRecorder()
	HandleChatWebAPITurn(body, httptest.NewRequest(http.MethodGet, "/web/api/turn?id=turn_payload", nil))
	raw := body.Body.String()
	// 注意：载荷字段名（usage_prompt_tokens）与响应字段名（input_tokens）不同，
	// 这里锁定的是 API 契约名。
	for _, want := range []string{`"usage_scope":"turn"`, `"usage_source":"provider_reported"`, `"input_tokens":369602`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("turn 响应缺少 %s: %s", want, raw)
		}
	}
}
