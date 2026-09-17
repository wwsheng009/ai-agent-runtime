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
