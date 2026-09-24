package commands

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 诊断出口（mesh peer 断连等后台 warning）的语义：交互式会话期间只进动态栏
// ——单行、临时、整行覆盖活动行、到期自动清除。
//
// 两条被否决的路线：
//   - 写 stderr：字节落在 FixedBottomSurface 的底部保留区（状态栏 / 输入行）
//     上，把状态栏覆盖成半截文本；
//   - 写 transcript（语义补充 cell）：历史信息流只承载会话语义，peer 断连/
//     重连的降级告警会持续刷屏，淹没正文。
//
// 没有交互式会话（启动阶段 / 非交互 / JSON 模式）时返回 false，调用方保留
// stderr 兜底。

func TestNotifyChatDiagnosticWithoutSinkFallsBack(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	if NotifyChatDiagnostic("Warning: mesh: peer node-x stream lost") {
		t.Fatal("diagnostic accepted without a registered interactive session")
	}
	if NotifyChatDiagnostic("   ") {
		t.Fatal("blank diagnostic accepted")
	}
}

func TestRegisterChatDiagnosticSinkSkipsNonInteractiveSession(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	for name, session := range map[string]*ChatSession{
		"no-session":      nil,
		"non-interactive": {NoInteractive: true},
		"json-output":     {JSONOutput: true},
	} {
		registerChatDiagnosticSink(&chatInteractionCoordinator{session: session})
		if NotifyChatDiagnostic("Warning: mesh: peer node-x stream lost") {
			t.Fatalf("%s session accepted a TUI diagnostic", name)
		}
	}
}

// chatDynamicStatusRowText 读取动态栏（transient activity row）的渲染文本。
// 动态栏是后台诊断的唯一展示位置，因此测试必须断言这一行的实际内容。
func chatDynamicStatusRowText(coordinator *chatInteractionCoordinator, width int) string {
	coordinator.mu.Lock()
	model := coordinator.dynamicStatusModel
	coordinator.mu.Unlock()
	if model == nil {
		return ""
	}
	return style.StatusLineDocument(*model, width).PlainText()
}

func TestNotifyChatDiagnosticRoutesToDynamicStatusRow(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	var history bytes.Buffer
	coordinator.SetWriter(&history)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)
	session.Surface = surface

	const line = "Warning: mesh: peer node-12504 stream lost: unexpected EOF"
	var accepted bool
	_, stderr := captureStdoutStderr(t, func() {
		accepted = NotifyChatDiagnostic(line)
		coordinator.waitUIActorIdle()
	})
	if !accepted {
		t.Fatal("interactive session did not accept the diagnostic")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("diagnostic leaked to stderr: %q", stderr)
	}

	// 1) 动态栏必须显示告警：整行、单行（"Warning:" 前缀由 ⚠ 语义取代）。
	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "⚠ mesh: peer node-12504 stream lost: unexpected EOF") {
		t.Fatalf("diagnostic missing from the dynamic status row: %q", row)
	}
	if strings.ContainsAny(row, "\r\n") {
		t.Fatalf("dynamic status row must stay single-line: %q", row)
	}

	// 2) 界面合成帧把同一行放在动态栏上（用户可见的唯一位置）。
	if frame := composedSurfaceFrameText(surface); !strings.Contains(frame, "mesh: peer node-12504 stream lost") {
		t.Fatalf("surface frame lost the diagnostic row:\n%s", frame)
	}

	// 3) 历史信息流（scene/transcript）与持久历史必须保持干净。
	if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
		t.Fatalf("diagnostic leaked into the scene transcript: %+v", snapshot.Cells)
	}
	if got := history.String(); strings.Contains(got, "mesh") {
		t.Fatalf("diagnostic leaked into durable history: %q", got)
	}
}

func TestFormatChatDiagnosticNoticeLineCollapsesMultilineWarning(t *testing.T) {
	// 真实的 mesh 告警是跨行的：Go 的 http 错误在 "connectex:" 处换行。
	raw := "Warning: mesh: peer node-14460 stream lost: read tcp 127.0.0.1:57235->127.0.0.1:49952:\n" +
		"\twsarecv: An existing connection was forcibly closed by the remote host."
	got := formatChatDiagnosticNoticeLine(raw, 48)
	if !strings.HasPrefix(got, "⚠ mesh: peer node-14460") {
		t.Fatalf("notice must drop the Warning prefix and keep the payload: %q", got)
	}
	if strings.Contains(got, "Warning:") {
		t.Fatalf("notice must not repeat the Warning prefix: %q", got)
	}
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("notice must collapse to a single line: %q", got)
	}
	if width := ui.DisplayWidth(got); width > 48 {
		t.Fatalf("notice width = %d, want <= 48: %q", width, got)
	}

	for _, blank := range []string{"", "   ", "\n\t ", "Warning:", "  Warning:  "} {
		if out := formatChatDiagnosticNoticeLine(blank, 80); out != "" {
			t.Fatalf("blank diagnostic %q produced %q, want empty", blank, out)
		}
	}
}

func TestDiagnosticNoticeExpiresAndRestoresActivityRow(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	coordinator := newTestChatInteractionCoordinator(t, &ChatSession{})

	// 一个正在运行的活动行：诊断提示必须临时覆盖它，到期后必须让位回去。
	coordinator.mu.Lock()
	coordinator.updateSurfaceStatusLocked(chatSurfaceStatus{kind: chatSurfaceStatusStreaming})
	coordinator.mu.Unlock()
	liveRow := chatDynamicStatusRowText(coordinator, 160)
	if liveRow == "" || strings.Contains(liveRow, "mesh") {
		t.Fatalf("expected a live activity row before the notice, got %q", liveRow)
	}

	if !coordinator.ShowDiagnosticNotice("Warning: mesh: peer node-14460 stream lost") {
		t.Fatal("interactive coordinator rejected the diagnostic notice")
	}
	coordinator.mu.Lock()
	noticeSeq := coordinator.diagnosticNoticeSeq
	coordinator.mu.Unlock()
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "mesh: peer node-14460") {
		t.Fatalf("diagnostic must take over the dynamic row, got %q", row)
	}

	// 到期：把 deadline 拨到过去并驱动一次 tick（等价 UI actor 的
	// FrameKeyDiagnosticNotice Timer 回调，chat_ui_actor.go 分支）。
	coordinator.mu.Lock()
	coordinator.diagnosticNoticeUntil = time.Now().Add(-time.Millisecond)
	coordinator.mu.Unlock()
	coordinator.refreshDiagnosticNoticeTick(noticeSeq)

	row := chatDynamicStatusRowText(coordinator, 160)
	if strings.Contains(row, "mesh") {
		t.Fatalf("expired diagnostic must leave the dynamic row: %q", row)
	}
	// 到期后必须让位回原来的活动行（同一构建路径，标签与时钟一并还原）。
	if row != liveRow {
		t.Fatalf("dynamic row must return to the live activity state: before=%q after=%q", liveRow, row)
	}
}

// 真实定时器路径：engine 到期 → ui.Timer{Key: FrameKeyDiagnosticNotice} →
// applyTimerAction → refreshDiagnosticNoticeTick，验证提示确实会自动消失，
// 而不是永久占据动态栏。
func TestDiagnosticNoticeTimerExpiryClearsTheRow(t *testing.T) {
	previousSink := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previousSink) })
	previousTTL := diagnosticNoticeTTL
	diagnosticNoticeTTL = 30 * time.Millisecond
	t.Cleanup(func() { diagnosticNoticeTTL = previousTTL })

	coordinator := newTestChatInteractionCoordinator(t, &ChatSession{})
	if !coordinator.ShowDiagnosticNotice("Warning: mesh: peer node-12504 stream lost") {
		t.Fatal("interactive coordinator rejected the diagnostic notice")
	}
	coordinator.waitUIActorIdle()
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "mesh: peer node-12504") {
		t.Fatalf("diagnostic must be visible before it expires, got %q", row)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		coordinator.waitUIActorIdle()
		if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "mesh") {
			return
		}
	}
	t.Fatalf("diagnostic notice never expired: %q", chatDynamicStatusRowText(coordinator, 160))
}
