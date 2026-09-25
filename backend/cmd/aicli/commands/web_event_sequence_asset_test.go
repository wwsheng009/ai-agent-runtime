package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestChatWebPage_ServesEventSequenceModule 确认 SSE 帧序号守卫模块随 go:embed
// 发布、且装配链完整：sse.js 导入 event-sequence.js 并把它接进事件分发。
//
// 新增 js 文件最容易漏的回归点是 embed 收录与装配断链——两者都不会在编译期报错，
// 但会让页面在浏览器里白屏（模块 404）或让序号守卫静默失效（重复帧再次渲染）。
func TestChatWebPage_ServesEventSequenceModule(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/web/js/event-sequence.js", nil)
	recorder := httptest.NewRecorder()
	HandleChatWebPage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/event-sequence.js status = %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("event-sequence.js content-type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{"createEventSequenceGuard", "duplicate", "gap", "reset"} {
		if !strings.Contains(body, want) {
			t.Fatalf("event-sequence.js 缺少 %q（判定分支必须齐全）", want)
		}
	}

	sseReq := httptest.NewRequest(http.MethodGet, "/web/js/sse.js", nil)
	sseRecorder := httptest.NewRecorder()
	HandleChatWebPage(sseRecorder, sseReq)
	if sseRecorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/sse.js status = %d", sseRecorder.Code)
	}
	sse := sseRecorder.Body.String()
	for _, want := range []string{`from "./event-sequence.js"`, "createEventSequenceGuard", "seqGuard", "resyncConversationAfterGap"} {
		if !strings.Contains(sse, want) {
			t.Fatalf("sse.js 缺少 %q（事件管理装配断链：重复帧不会被丢弃 / 丢帧不会对账）", want)
		}
	}
}
