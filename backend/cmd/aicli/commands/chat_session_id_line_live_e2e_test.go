package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// L3 进程内 e2e：双行状态栏的 session ID 行。
//
// 回归场景：绑定持久化 runtime session 后，mount surface 必须把 session ID
// 发布到第二行状态栏（statusRow-1），并让整条状态栏占两行；未绑定 session
// 时保持单行。
//
// 断言走 surface 的 ComposedFrameForTest（与应用侧同一权威投影路径），
// 覆盖「coordinator → facade → UI actor → surface 状态 → 合成帧」全链路。
//
// popup/composer 覆盖层的 session 行隐藏已在 app_layout_test.go 的
// TestLayoutTwoRowStatus 中覆盖，此处不再重复。
func TestChatSessionIDLineE2E_PublishedOnSurfaceMount(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(interaction.Shutdown)
	interaction.SetWriter(&bytes.Buffer{})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)
	session.Interaction = interaction
	interaction.waitUIActorIdle()

	// L3-3：状态模型经 reducer 落在 AppState（非统一路径的 surface Apply
	// 投递已退役，合成帧不再接收该模型）；双行状态栏的行布局口径由 ui 层
	// app_layout_test.TestLayoutTwoRowStatus 覆盖。
	st := interaction.uiActor.AppState()
	if !strings.Contains(st.Bottom.SessionIDLine, "会话 lead-session") {
		t.Fatalf("session ID line missing in AppState after surface mount: %q", st.Bottom.SessionIDLine)
	}
	if !strings.Contains(st.Bottom.SessionIDLine, "--pprof off") || !strings.Contains(st.Bottom.SessionIDLine, "--debug off") {
		t.Fatalf("flag status missing on session ID line: %q", st.Bottom.SessionIDLine)
	}
}

// 未绑定 runtime session 时，第二行显示 --pprof / --debug 标志状态（不渲染 "会话 " 前缀）。
func TestChatSessionIDLineE2E_ShowsFlagStatusWithoutRuntimeSession(t *testing.T) {
	session := &ChatSession{Stream: true}
	interaction := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(interaction.Shutdown)
	interaction.SetWriter(&bytes.Buffer{})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)
	session.Interaction = interaction
	interaction.waitUIActorIdle()

	// L3-3：改读 AppState 的 session ID 行（surface 帧不再接收状态模型）。
	st := interaction.uiActor.AppState()
	if strings.Contains(st.Bottom.SessionIDLine, "会话 ") {
		t.Fatalf("session ID line rendered without a bound runtime session: %q", st.Bottom.SessionIDLine)
	}
	if !strings.Contains(st.Bottom.SessionIDLine, "--pprof off") {
		t.Fatalf("expected --pprof off in session ID line, got %q", st.Bottom.SessionIDLine)
	}
	if !strings.Contains(st.Bottom.SessionIDLine, "--debug off") {
		t.Fatalf("expected --debug off in session ID line, got %q", st.Bottom.SessionIDLine)
	}
}

// buildChatFlagStatusLine 反映 --pprof / --debug 的启用状态（on/off）。
func TestBuildChatFlagStatusLine(t *testing.T) {
	prevDebug := chatDebugFlagEnabled()
	prevProvider := chatDebugPprofProvider
	t.Cleanup(func() {
		setChatDebugFlag(prevDebug)
		chatDebugPprofProvider = prevProvider
	})

	// 两个标志都关闭 → off/off
	setChatDebugFlag(false)
	chatDebugPprofProvider = func() string { return "" }
	if got := buildChatFlagStatusLine(); got != "--pprof off  --debug off" {
		t.Fatalf("both off: got %q", got)
	}

	// --debug 开启
	setChatDebugFlag(true)
	if got := buildChatFlagStatusLine(); got != "--pprof off  --debug on" {
		t.Fatalf("debug on: got %q", got)
	}

	// --pprof 开启（provider 返回端点 URL）
	chatDebugPprofProvider = func() string { return "http://127.0.0.1:6060/debug/pprof/" }
	if got := buildChatFlagStatusLine(); got != "endpoints: http://127.0.0.1:6060/debug/endpoints  web: http://127.0.0.1:6060/web/" {
		t.Fatalf("both on: got %q", got)
	}

	// --pprof 开启、--debug 关闭
	setChatDebugFlag(false)
	if got := buildChatFlagStatusLine(); got != "--pprof on  --debug off" {
		t.Fatalf("pprof on only: got %q", got)
	}
}

// chatDebugPprofBaseURL / chatDebugPprofDisplayURL 从 pprof 端点 URL 派生基础
// 地址与 /debug/chat/status 展示端点 URL。
func TestChatDebugPprofDerivedURLs(t *testing.T) {
	prevProvider := chatDebugPprofProvider
	t.Cleanup(func() { chatDebugPprofProvider = prevProvider })

	// 未启用 → 全部空串
	chatDebugPprofProvider = func() string { return "" }
	if got := chatDebugPprofBaseURL(); got != "" {
		t.Fatalf("base url without pprof: got %q, want empty", got)
	}
	if got := chatDebugPprofDisplayURL(); got != "" {
		t.Fatalf("display url without pprof: got %q, want empty", got)
	}
	if got := chatDebugPprofScreenURL(); got != "" {
		t.Fatalf("screen url without pprof: got %q, want empty", got)
	}

	// 标准 pprof 端点 URL
	chatDebugPprofProvider = func() string { return "http://127.0.0.1:6060/debug/pprof/" }
	if got := chatDebugPprofBaseURL(); got != "http://127.0.0.1:6060" {
		t.Fatalf("base url: got %q, want %q", got, "http://127.0.0.1:6060")
	}
	if got := chatDebugPprofDisplayURL(); got != "http://127.0.0.1:6060/debug/chat/status" {
		t.Fatalf("display url: got %q, want %q", got, "http://127.0.0.1:6060/debug/chat/status")
	}
	if got := chatDebugPprofScreenURL(); got != "http://127.0.0.1:6060/debug/chat/screen" {
		t.Fatalf("screen url: got %q, want %q", got, "http://127.0.0.1:6060/debug/chat/screen")
	}
	if got := chatDebugPprofEndpointsURL(); got != "http://127.0.0.1:6060/debug/endpoints" {
		t.Fatalf("endpoints url: got %q, want %q", got, "http://127.0.0.1:6060/debug/endpoints")
	}
	if got := chatDebugPprofWebURL(); got != "http://127.0.0.1:6060/web/" {
		t.Fatalf("web url: got %q, want %q", got, "http://127.0.0.1:6060/web/")
	}
}
