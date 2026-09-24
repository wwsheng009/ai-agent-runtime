package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newChatWebExportTestSession 构造与 chat_export_command_test.go 同形的会话：
// 一条用户消息 + 一条带工具调用的助手消息 + 工具结果 + 收尾助手消息。
// web 导出端点复用同一套写出实现，测试沿用这份最小会话以便逐项对照 CLI 产物。
func newChatWebExportTestSession() *ChatSession {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-web-export"
	messages := []runtimetypes.Message{
		{Role: "user", Content: "please run status", Metadata: runtimetypes.NewMetadata()},
		{
			Role:    "assistant",
			Content: "I will check.",
			ToolCalls: []runtimetypes.ToolCall{{
				ID:   "call-1",
				Name: "execute_shell_command",
				Args: map[string]interface{}{"command": "git status --short"},
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		{Role: "tool", ToolCallID: "call-1", Content: " M file.go", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "Done.", Metadata: runtimetypes.NewMetadata()},
	}
	runtimeSession.ReplaceHistory(messages)
	return &ChatSession{
		RuntimeSession: runtimeSession,
		Messages:       messages,
		SessionUserID:  "tester",
		NoInteractive:  true,
	}
}

func withChatWebExportSession(t *testing.T, session *ChatSession) {
	t.Helper()
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() { chatDebugDisplaySessionProvider = old })
}

func chatWebExportRequest(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	HandleChatWebAPIExport(rec, req)
	return rec
}

// TestChatWebAPIExportFullDownload 验证默认（full）导出的下载契约：
// 附件响应 + 与 CLI 同规则的默认文件名 + 与 /export --full 同构的 JSON 载荷。
func TestChatWebAPIExportFullDownload(t *testing.T) {
	withChatWebExportSession(t, newChatWebExportTestSession())
	rec := chatWebExportRequest(t, http.MethodGet, ChatWebAPIExportPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") {
		t.Fatalf("content-disposition = %q, want attachment", disposition)
	}
	if !strings.Contains(disposition, "session-web-export_") || !strings.Contains(disposition, "_full.json") {
		t.Fatalf("content-disposition = %q, want CLI-style {session}_{ts}_full.json", disposition)
	}
	if got := rec.Header().Get("X-AICLI-Export-Format"); got != string(chatExportFormatFull) {
		t.Fatalf("export format header = %q, want full", got)
	}
	if got := rec.Header().Get("X-AICLI-Export-Messages"); got != "4" {
		t.Fatalf("export messages header = %q, want 4", got)
	}
	if got := rec.Header().Get("X-AICLI-Export-Session"); got != "session-web-export" {
		t.Fatalf("export session header = %q, want session-web-export", got)
	}
	var envelope chatSessionExportEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode export body: %v\n%s", err, rec.Body.String())
	}
	if envelope.Stats.MessageCount != 4 || envelope.Stats.ToolCallCount != 1 || envelope.Stats.ToolResultCount != 1 {
		t.Fatalf("stats = %+v, want 4 messages / 1 tool call / 1 tool result", envelope.Stats)
	}
	if envelope.Session == nil || len(envelope.Session.History) < 4 {
		t.Fatalf("session payload = %+v, want full history", envelope.Session)
	}
}

// TestChatWebAPIExportMarkdownFormats 验证 markdown 家族的格式词与 Content-Type：
// body 不含工具细节，tools/trace 逐级包含工具调用与结果（与 CLI 同源）。
func TestChatWebAPIExportMarkdownFormats(t *testing.T) {
	cases := []struct {
		query        string
		format       chatExportFormat
		wantToolName bool
		wantToolBody bool
	}{
		{"format=body", chatExportFormatBody, false, false},
		{"format=tools", chatExportFormatMarkdownTools, true, false},
		{"format=trace", chatExportFormatMarkdownTrace, true, true},
	}
	for _, tc := range cases {
		withChatWebExportSession(t, newChatWebExportTestSession())
		rec := chatWebExportRequest(t, http.MethodGet, ChatWebAPIExportPath+"?"+tc.query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body=%s", tc.query, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") {
			t.Fatalf("%s: content-type = %q, want text/markdown", tc.query, got)
		}
		if got := rec.Header().Get("X-AICLI-Export-Format"); got != string(tc.format) {
			t.Fatalf("%s: format header = %q, want %q", tc.query, got, tc.format)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "please run status") || !strings.Contains(body, "Done.") {
			t.Fatalf("%s: markdown body missing conversation text:\n%s", tc.query, body)
		}
		if strings.Contains(body, "execute_shell_command") != tc.wantToolName {
			t.Fatalf("%s: tool call presence = %v, want %v:\n%s", tc.query, !tc.wantToolName, tc.wantToolName, body)
		}
		if strings.Contains(body, "M file.go") != tc.wantToolBody {
			t.Fatalf("%s: tool result presence = %v, want %v:\n%s", tc.query, !tc.wantToolBody, tc.wantToolBody, body)
		}
	}
}

// TestChatWebAPIExportSessionIDParam 验证 ?session_id= 与 /export <session-id> 同义。
func TestChatWebAPIExportSessionIDParam(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(t.Context(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	if err := manager.AddMessage(t.Context(), runtimeSession.ID, *runtimetypes.NewUserMessage("hello from store")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	// 当前会话与目标会话不同：显式 session_id 必须导出目标会话而非当前会话。
	current := newChatWebExportTestSession()
	current.SessionManager = manager
	withChatWebExportSession(t, current)

	rec := chatWebExportRequest(t, http.MethodGet, ChatWebAPIExportPath+"?format=body&session_id="+runtimeSession.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello from store") {
		t.Fatalf("explicit session_id export missing stored message:\n%s", rec.Body.String())
	}
	if got := rec.Header().Get("X-AICLI-Export-Session"); got != runtimeSession.ID {
		t.Fatalf("export session header = %q, want %q", got, runtimeSession.ID)
	}
}

// TestChatWebAPIExportErrors 验证稳定错误码与只读方法约束。
func TestChatWebAPIExportErrors(t *testing.T) {
	t.Run("no session", func(t *testing.T) {
		withChatWebExportSession(t, nil)
		rec := chatWebExportRequest(t, http.MethodGet, ChatWebAPIExportPath)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "chat_session_not_ready") {
			t.Fatalf("body = %s, want chat_session_not_ready", rec.Body.String())
		}
	})
	t.Run("unknown format", func(t *testing.T) {
		withChatWebExportSession(t, newChatWebExportTestSession())
		rec := chatWebExportRequest(t, http.MethodGet, ChatWebAPIExportPath+"?format=bogus")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid_format") {
			t.Fatalf("body = %s, want invalid_format", rec.Body.String())
		}
	})
	t.Run("method not allowed", func(t *testing.T) {
		withChatWebExportSession(t, newChatWebExportTestSession())
		rec := chatWebExportRequest(t, http.MethodPost, ChatWebAPIExportPath)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("allow = %q, want GET, HEAD", got)
		}
	})
}

// TestChatWebAPIExportLeavesNoTempFile 锁定"响应后不留临时文件"：导出走同机临时
// 文件中转，任何分支（成功/失败）都必须清理，否则长期运行会累积大文件。
func TestChatWebAPIExportLeavesNoTempFile(t *testing.T) {
	withChatWebExportSession(t, newChatWebExportTestSession())
	if rec := chatWebExportRequest(t, http.MethodGet, ChatWebAPIExportPath+"?format=trace"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	leftovers, err := filepath.Glob(filepath.Join(os.TempDir(), "aicli-web-export-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestChatWebExportContentDisposition 验证非 ASCII 会话 ID 的下载文件名：
// 同时给出 ASCII 回退名与 RFC 5987 的 filename*，浏览器才不会拿到乱码名。
func TestChatWebExportContentDisposition(t *testing.T) {
	disposition := chatWebExportContentDisposition("会话_20260101_000000_full.json")
	if !strings.Contains(disposition, `filename="___20260101_000000_full.json"`) {
		t.Fatalf("disposition = %q, want ASCII fallback name", disposition)
	}
	if !strings.Contains(disposition, "filename*=UTF-8''%E4%BC%9A%E8%AF%9D_20260101_000000_full.json") {
		t.Fatalf("disposition = %q, want RFC 5987 filename*", disposition)
	}
}
