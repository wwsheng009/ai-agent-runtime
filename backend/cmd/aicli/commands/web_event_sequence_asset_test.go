package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
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

// TestChatWebSSEAsset_RegistersConsumedEventListeners 守住 sse.js 的监听注册与
// 服务端事件全集同集（chatWebSSEMappings + 合成事件）：EventSource 只把命名事件
// 派发给显式注册的监听器，漏注册 = 该帧被浏览器静默丢弃、switch 分支永远不可达
// （assistant_message 终稿曾因此整帧丢失：TUI 完整、web 缺最后一段）。
//
// 判别口径：只检查监听数组块（["connected" … ].forEach）内的字符串——switch 的
// `case "x":` 与 refreshKeys 的 `"x": 1` 都不在块内，不会误判通过。
func TestChatWebSSEAsset_RegistersConsumedEventListeners(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/web/js/sse.js", nil)
	recorder := httptest.NewRecorder()
	HandleChatWebPage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/sse.js status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	start := strings.Index(body, `["connected"`)
	if start < 0 {
		t.Fatal(`sse.js 未找到监听注册数组（["connected" … ]）`)
	}
	rel := strings.Index(body[start:], `].forEach`)
	if rel < 0 {
		t.Fatal("sse.js 监听注册数组未闭合（缺少 ].forEach）")
	}
	block := body[start : start+rel]

	// 服务端事件全集：映射表 + schema 端点里的合成事件（无 BusEvent）。
	want := map[string]bool{
		"connected": true, "heartbeat": true, "screen_refresh": true,
		"session_switched": true, "error": true,
	}
	for _, m := range chatWebSSEMappings {
		want[m.SSEEvent] = true
	}
	for name := range want {
		if !strings.Contains(block, `"`+name+`"`) {
			t.Errorf("sse.js 监听数组缺少 %q：该 SSE 帧会被浏览器静默丢弃（switch 分支不可达）", name)
		}
	}
}

// TestChatWebSSEAsset_RegistersMeshFrameListeners 守住 sse.js 的 mesh.* 监听数组
// 与服务端帧类型同集：mesh.Frame* 常量（internal/mesh/fanin.go）加上 web_handlers
// 合成的 mesh.unavailable 降级帧。漏注册 = 该网格帧被浏览器静默丢弃、sessions.js
// 的处理分支永远不可达（与主事件流的 assistant_message 同类缺陷）。
func TestChatWebSSEAsset_RegistersMeshFrameListeners(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/web/js/sse.js", nil)
	recorder := httptest.NewRecorder()
	HandleChatWebPage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/sse.js status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	start := strings.Index(body, `["mesh.ready"`)
	if start < 0 {
		t.Fatal(`sse.js 未找到网格监听注册数组（["mesh.ready" … ]）`)
	}
	rel := strings.Index(body[start:], `].forEach`)
	if rel < 0 {
		t.Fatal("sse.js 网格监听注册数组未闭合（缺少 ].forEach）")
	}
	block := body[start : start+rel]
	for _, name := range []string{
		mesh.FrameReady,
		mesh.FramePeerJoined,
		mesh.FramePeerLeft,
		mesh.FramePeerUpdated,
		mesh.FrameSessionChanged,
		mesh.FrameCallInvoked,
		mesh.FrameCallCompleted,
		mesh.FramePeerEvent,
		mesh.FrameLagged,
		"mesh.unavailable", // web_handlers.go 的降级帧（无 Frame* 常量）
	} {
		if !strings.Contains(block, `"`+name+`"`) {
			t.Errorf("sse.js mesh 监听数组缺少 %q：该网格帧会被浏览器静默丢弃", name)
		}
	}
}
