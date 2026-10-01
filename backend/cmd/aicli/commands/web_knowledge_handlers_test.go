package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// ---------------------------------------------------------------------------
// /web/api/knowledge[/status] —— 知识层状态端点（knowledge.status.v1）。
//
// 直调 handler（不经 mux），用 display provider 注入测试会话，覆盖：
// mode=off 的稳定最小载荷 / 真实 Layer 的完整载荷（含 watch 段）/
// 裸路径等价 / 405 / 404 / 无会话。契约与 runtime-server 的
// GET /api/runtime/knowledge/status 同源同形（同一 StatusReport）。
// ---------------------------------------------------------------------------

// withChatWebKnowledgeSession 通过 display provider 模拟"当前活动会话"。
func withChatWebKnowledgeSession(t *testing.T, session *ChatSession) {
	t.Helper()
	prev := chatDebugDisplaySessionProvider
	t.Cleanup(func() { chatDebugDisplaySessionProvider = prev })
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })
}

func chatWebKnowledgeDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

func TestChatWebKnowledgeStatusModeOff(t *testing.T) {
	// 无知识层的会话（Knowledge=nil）→ mode=off 的最小载荷，不是 404/500。
	withChatWebKnowledgeSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIKnowledgePath+"/status", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIKnowledge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := chatWebKnowledgeDecode(t, rec)
	if got := body["mode"]; got != string(knowledge.ModeOff) {
		t.Fatalf("mode = %v, want %q", got, knowledge.ModeOff)
	}
	if enabled, _ := body["enabled"].(bool); enabled {
		t.Fatalf("enabled = true, want false（Knowledge=nil）")
	}
	if _, hasErr := body["error"]; hasErr {
		t.Fatalf("mode=off 不得返回 error envelope: %v", body)
	}
	// watch 段必须存在且为显式的 inactive（状态面最怕静默）。
	watch, ok := body["watch"].(map[string]interface{})
	if !ok {
		t.Fatalf("watch 必须是对象: %T", body["watch"])
	}
	if active, _ := watch["active"].(bool); active {
		t.Fatalf("watch.active = true, want false（默认 off）")
	}
}

func TestChatWebKnowledgeStatusWithLayer(t *testing.T) {
	act := activateLocalChatKnowledgeForTest(t, knowledge.ModeOn)
	session := newWebTestSession()
	session.Knowledge = act
	withChatWebKnowledgeSession(t, session)

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIKnowledgePath+"/status", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIKnowledge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := chatWebKnowledgeDecode(t, rec)
	if got := body["mode"]; got != string(knowledge.ModeOn) {
		t.Fatalf("mode = %v, want %q", got, knowledge.ModeOn)
	}
	if enabled, _ := body["enabled"].(bool); !enabled {
		t.Fatalf("enabled = false, want true（mode=on）")
	}
	if got := body["role"]; got != string(knowledge.RoleOwner) {
		t.Fatalf("role = %v, want %q", got, knowledge.RoleOwner)
	}
	if _, ok := body["schema_version"].(float64); !ok {
		t.Fatalf("schema_version 应为数字（store schema 版本）: %T", body["schema_version"])
	}
	// SkipInitialIndex 的激活 → 未索引是正常初始态，必须有可解释的 degraded_reason。
	if reason, _ := body["degraded_reason"].(string); reason == "" {
		t.Fatalf("未索引时必须给出 degraded_reason: %v", body)
	}
	// 交付 1 第三类变更源的本进程视角必须在载荷里（watch 段完整口径）。
	watch, ok := body["watch"].(map[string]interface{})
	if !ok {
		t.Fatalf("watch 必须是对象: %T", body["watch"])
	}
	if active, _ := watch["active"].(bool); active {
		t.Fatalf("watch.active = true, want false（默认 off，未配置监听）")
	}
}

func TestChatWebKnowledgeBarePathEqualsStatus(t *testing.T) {
	withChatWebKnowledgeSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIKnowledgePath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIKnowledge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（裸路径等价于 /status）", rec.Code)
	}
	body := chatWebKnowledgeDecode(t, rec)
	if got := body["mode"]; got != string(knowledge.ModeOff) {
		t.Fatalf("mode = %v, want %q", got, knowledge.ModeOff)
	}
}

func TestChatWebKnowledgeNilSession(t *testing.T) {
	withChatWebKnowledgeSession(t, nil)

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIKnowledgePath+"/status", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIKnowledge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（无会话也应给出 off 载荷）", rec.Code)
	}
	if got := chatWebKnowledgeDecode(t, rec)["mode"]; got != string(knowledge.ModeOff) {
		t.Fatalf("mode = %v, want %q", got, knowledge.ModeOff)
	}
}

func TestChatWebKnowledgeMethodNotAllowed(t *testing.T) {
	withChatWebKnowledgeSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIKnowledgePath+"/status", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIKnowledge(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	body := chatWebKnowledgeDecode(t, rec)
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("405 必须带 error envelope: %v", body)
	}
	if code, _ := errObj["code"].(string); code != chatWebKnowledgeMethodCode {
		t.Fatalf("error.code = %v, want %q", errObj["code"], chatWebKnowledgeMethodCode)
	}
}

func TestChatWebKnowledgeUnknownSubPath(t *testing.T) {
	withChatWebKnowledgeSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIKnowledgePath+"/nope", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIKnowledge(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := chatWebKnowledgeDecode(t, rec)
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("404 必须带 error envelope: %v", body)
	}
	if code, _ := errObj["code"].(string); code != chatWebKnowledgeNotFoundCode {
		t.Fatalf("error.code = %v, want %q", errObj["code"], chatWebKnowledgeNotFoundCode)
	}
}
