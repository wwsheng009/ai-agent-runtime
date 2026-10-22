package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// chat_debug_window_param_test.go —— T8b-4 契约（设计 §14.10）：
//  ① 参数解析：?window=N / ?full=1 / 缺省（residency 开关决定缺省窗口）；
//  ② 缺省全量路径不新增任何字段（零值窗口 = 历史形状，默认逐字节不变）；
//  ③ 窗口读取只物化尾部 cell，内容 = 全量尾部（首个窗口 cell 的 leading gap
//     按 §14.7 归零）；
//  ④ residency 开启时缺省窗口生效，显式 window/full 覆盖缺省。

func t8bDebugWindowRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// t8bDebugBridgeFixture 构造「仅 bridge Scene 有内容」的会话（无 uiActor /
// 无 Interaction / 无 surface），是 screen/status 的 fallback 通道。
func t8bDebugBridgeFixture(t *testing.T, cells int) *chatRuntimeEventBridge {
	t.Helper()
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "t8b-debug-window"}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	bridge.renderMu.Lock()
	for index := 1; index <= cells; index++ {
		turnID := fmt.Sprintf("turn-%03d", index)
		streamID := fmt.Sprintf("stream-%03d", index)
		for _, event := range []runtimeevents.Event{
			{Type: runtimechat.EventSessionStart, SessionID: session.RuntimeSession.ID, Payload: map[string]interface{}{"turn_id": turnID}},
			{Type: runtimechat.EventLLMRequestStarted, SessionID: session.RuntimeSession.ID, Payload: map[string]interface{}{
				"turn_id": turnID, "stream_id": streamID, "step": 1,
			}},
			{Type: runtimechat.EventAssistantMessage, SessionID: session.RuntimeSession.ID, Payload: map[string]interface{}{
				"turn_id": turnID, "stream_id": streamID, "content": fmt.Sprintf("T8B-CELL-%03d", index),
			}},
			{Type: runtimechat.EventSessionEnd, SessionID: session.RuntimeSession.ID, Payload: map[string]interface{}{"turn_id": turnID, "success": true}},
		} {
			bridge.applyChangeSet(bridge.renderEncoder.Encode(event))
		}
	}
	bridge.renderMu.Unlock()

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() { chatDebugDisplaySessionProvider = old })
	return bridge
}

func TestChatDebugTranscriptWindowParamParsing(t *testing.T) {
	cases := []struct {
		name          string
		target        string
		residency     bool
		wantCells     int
		wantDefaulted bool
		wantRequested bool
	}{
		{name: "缺省 + residency 关闭 = 全量", target: "/debug/chat/status", residency: false},
		{name: "缺省 + residency 开启 = 缺省窗口", target: "/debug/chat/status", residency: true, wantCells: chatDebugDefaultWindowCells, wantDefaulted: true},
		{name: "显式 window", target: "/debug/chat/status?window=128", residency: false, wantCells: 128, wantRequested: true},
		{name: "显式 window 覆盖缺省", target: "/debug/chat/status?window=7", residency: true, wantCells: 7, wantRequested: true},
		{name: "window 超上限钳制", target: "/debug/chat/status?window=99999", residency: false, wantCells: chatDebugWindowMaxCells, wantRequested: true},
		{name: "window 非法 = 未指定", target: "/debug/chat/status?window=abc", residency: false},
		{name: "window=0 = 全量", target: "/debug/chat/status?window=0", residency: true},
		{name: "window=all = 全量", target: "/debug/chat/status?window=all", residency: true},
		{name: "full=1 = 全量", target: "/debug/chat/status?full=1", residency: true},
		{name: "full=true = 全量", target: "/debug/chat/status?full=true", residency: true},
		{name: "full=0 = 未指定", target: "/debug/chat/status?full=0", residency: true, wantCells: chatDebugDefaultWindowCells, wantDefaulted: true},
		{name: "full 优先于 window", target: "/debug/chat/status?full=1&window=9", residency: false},
	}
	for _, testCase := range cases {
		got := chatDebugTranscriptWindowForRequest(t8bDebugWindowRequest(t, testCase.target), testCase.residency)
		if got.Cells != testCase.wantCells || got.Defaulted != testCase.wantDefaulted || got.Requested != testCase.wantRequested {
			t.Fatalf("%s：got=%+v want={Cells:%d Defaulted:%v Requested:%v}",
				testCase.name, got, testCase.wantCells, testCase.wantDefaulted, testCase.wantRequested)
		}
		if got.windowed() != (testCase.wantCells > 0) {
			t.Fatalf("%s：windowed()=%v", testCase.name, got.windowed())
		}
		// 只有「确实裁剪」时才产生注解：未裁剪（缺省全量）注解必须缺席，
		// 响应形状与历史一致。
		if info := got.info(testCase.wantCells + 1); (info != nil) != got.windowed() {
			t.Fatalf("%s：info()=%+v 与 windowed()=%v 不一致", testCase.name, info, got.windowed())
		}
	}
}

func TestChatDebugWindowDefaultFollowsResidency(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, dir)

	plain := t8bDebugWindowRequest(t, "/debug/chat/status")
	if got := ChatDebugDisplayOptionsForRequest(plain, ChatDebugDisplayBoundedOptions()); got.Window.Cells != chatDebugDefaultWindowCells || !got.Window.Defaulted {
		t.Fatalf("residency 开启时缺省应套用窗口：%+v", got.Window)
	}
	if got := ChatDebugDisplayOptionsForRequest(t8bDebugWindowRequest(t, "/debug/chat/status?full=1"), ChatDebugDisplayBoundedOptions()); got.Window.windowed() {
		t.Fatalf("显式 full 应关闭窗口：%+v", got.Window)
	}
	if got := ChatDebugDisplayOptionsForRequest(t8bDebugWindowRequest(t, "/debug/chat/status?window=64"), ChatDebugDisplayBoundedOptions()); got.Window.Cells != 64 || !got.Window.Requested {
		t.Fatalf("显式 window 应生效：%+v", got.Window)
	}

	// residency 关闭后缺省回到全量（历史行为）。
	shutdownResidencyRuntimeForTest()
	if got := chatDebugTranscriptWindowForRequest(plain, transcriptResidencyRuntimeEnabled()); got.windowed() {
		t.Fatalf("residency 关闭时缺省必须全量：%+v", got)
	}
}

func TestChatDebugScreenWindowMatchesFullTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const total = 40
	t8bDebugBridgeFixture(t, total)

	full := BuildChatDebugScreenSnapshot()
	if !full.Available || len(full.Lines) == 0 {
		t.Fatalf("fixture 未产生 fallback 文本：available=%v reason=%q", full.Available, full.Reason)
	}
	if full.Window != nil {
		t.Fatalf("全量读取不得携带窗口标注：%+v", full.Window)
	}
	if !strings.Contains(full.Text, "T8B-CELL-001") || !strings.Contains(full.Text, fmt.Sprintf("T8B-CELL-%03d", total)) {
		t.Fatalf("全量文本未覆盖首尾 cell：\n%s", full.Text)
	}

	const keep = 12
	win := BuildChatDebugScreenSnapshotWithWindow(chatDebugTranscriptWindow{Cells: keep, Requested: true})
	if !win.Available {
		t.Fatalf("窗口读取应可用：reason=%q", win.Reason)
	}
	if win.Window == nil || win.Window.Cells != keep || win.Window.TotalCells != total || !win.Window.Requested {
		t.Fatalf("窗口标注不符：%+v", win.Window)
	}
	if strings.Contains(win.Text, "T8B-CELL-001") {
		t.Fatalf("窗口读取不得包含被裁掉的早期 cell：\n%s", win.Text)
	}
	if !strings.Contains(win.Text, fmt.Sprintf("T8B-CELL-%03d", total)) {
		t.Fatalf("窗口读取必须包含最新 cell：\n%s", win.Text)
	}
	// 与全量尾部逐行一致：首个窗口 cell 失去前驱 gap（§14.7 leading gap 归零），
	// 因此窗口文本恰好等于全量文本的最后 len(win.Lines) 行。
	fullTail := full.Lines[len(full.Lines)-len(win.Lines):]
	if strings.Join(win.Lines, "\n") != strings.Join(fullTail, "\n") {
		t.Fatalf("窗口文本与全量尾部不一致：\nwin =%q\nwant=%q", strings.Join(win.Lines, "\n"), strings.Join(fullTail, "\n"))
	}
	if len(win.Lines) >= len(full.Lines) {
		t.Fatalf("窗口未裁剪：win=%d full=%d", len(win.Lines), len(full.Lines))
	}
	// 零值窗口 = 历史全量（逐字节同形状）。
	if legacy := BuildChatDebugScreenSnapshotWithWindow(chatDebugTranscriptWindow{}); legacy.Window != nil ||
		strings.Join(legacy.Lines, "\n") != strings.Join(full.Lines, "\n") {
		t.Fatal("零值窗口必须与全量读取逐行一致")
	}
}

func TestChatDebugScreenRequestEntryWindow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const total = 30
	t8bDebugBridgeFixture(t, total)

	full := ChatDebugScreenTextForRequest(t8bDebugWindowRequest(t, "/debug/chat/screen?format=text"))
	if !strings.Contains(full, "T8B-CELL-001") || !strings.Contains(full, fmt.Sprintf("T8B-CELL-%03d", total)) {
		t.Fatalf("缺省（residency 关闭）= 全量：\n%s", full)
	}
	windowed := ChatDebugScreenTextForRequest(t8bDebugWindowRequest(t, "/debug/chat/screen?format=text&window=5"))
	if strings.Contains(windowed, "T8B-CELL-001") {
		t.Fatalf("?window=5 不得包含早期 cell：\n%s", windowed)
	}
	if !strings.Contains(windowed, fmt.Sprintf("T8B-CELL-%03d", total)) {
		t.Fatalf("?window=5 必须包含最新 cell：\n%s", windowed)
	}
	body, err := ChatDebugScreenJSONForRequest(t8bDebugWindowRequest(t, "/debug/chat/screen?window=5"))
	if err != nil {
		t.Fatalf("窗口 JSON 序列化失败：%v", err)
	}
	if !strings.Contains(string(body), "\"window\"") || !strings.Contains(string(body), "\"total_cells\": 30") {
		t.Fatalf("窗口 JSON 应携带 window 标注：\n%s", body)
	}
	fullBody, err := ChatDebugScreenJSONForRequest(t8bDebugWindowRequest(t, "/debug/chat/screen?full=1"))
	if err != nil {
		t.Fatalf("全量 JSON 序列化失败：%v", err)
	}
	if strings.Contains(string(fullBody), "\"window\"") {
		t.Fatalf("全量 JSON 不得携带 window 标注：\n%s", fullBody)
	}
}

func TestChatDebugStatusWindowMatchesFullTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const total = 40
	bridge := t8bDebugBridgeFixture(t, total)
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != total {
		t.Fatalf("fixture scene cells=%d，期望 %d", len(snapshot.Cells), total)
	}

	full := BuildChatDebugDisplaySnapshotWithOptions(ChatDebugDisplayOptions{})
	if full.Scene == nil || full.Scene.Window != nil {
		t.Fatalf("全量 status 不得携带窗口标注：%+v", full.Scene)
	}
	fullRows := scene.LayoutTranscript(snapshot.Cells, snapshot.Revision)
	if full.Scene.LayoutRows != len(fullRows) {
		t.Fatalf("全量布局行数=%d，期望 %d", full.Scene.LayoutRows, len(fullRows))
	}

	const keep = 10
	win := BuildChatDebugDisplaySnapshotWithOptions(ChatDebugDisplayOptions{
		Window: chatDebugTranscriptWindow{Cells: keep, Requested: true},
	})
	if win.Scene == nil || win.Scene.Window == nil {
		t.Fatalf("窗口 status 必须携带窗口标注：%+v", win.Scene)
	}
	if win.Scene.Window.Cells != keep || win.Scene.Window.TotalCells != total || !win.Scene.Window.Requested {
		t.Fatalf("窗口标注不符：%+v", win.Scene.Window)
	}
	windowSnap := tailWindowSnapshot(snapshot, keep)
	wantRows := scene.LayoutTranscript(windowSnap.Cells, windowSnap.Revision)
	if win.Scene.LayoutRows != len(wantRows) {
		t.Fatalf("窗口布局行数=%d，期望 %d", win.Scene.LayoutRows, len(wantRows))
	}
	if win.Scene.LayoutRows >= full.Scene.LayoutRows {
		t.Fatalf("窗口布局行数 %d 应小于全量 %d", win.Scene.LayoutRows, full.Scene.LayoutRows)
	}
	// 文本形式的窗口标注（?format=text）。
	text := BuildChatDebugDisplayTextWithOptions(ChatDebugDisplayOptions{
		Window: chatDebugTranscriptWindow{Cells: keep, Requested: true},
	})
	if !strings.Contains(text, "Layout Window:") || !strings.Contains(text, "last 10 / 40 cells (requested)") {
		t.Fatalf("文本未标注窗口：\n%s", text)
	}
}
