package commands

// 复现验证：统一渲染器（TerminalSession 主 presenter）下执行 /debug display
// 必须打开独立的备用屏幕界面（类似 /resume list 与 /history），而不是把调试
// 文档提交为主信息流的 Scene cell。
//
// 用户实测：aicli chat 统一渲染器会话中执行 /debug display，界面不更新且
// 没有任何调试信息输出。本测试用真实 unified presenter 链路（bridge +
// ReplaceTranscriptAction + TerminalSession）验证调试文档不再进入主信息流，
// 命令结果只携带 OpenDebugOverlay 请求。

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// TestDebugDisplayUnifiedPresenterOpensOverlayInsteadOfSceneCell 验证统一渲染
// 器下 /debug display 不再提交为 Scene cell：主信息流（transcript 与主
// presenter 输出）不包含调试文档，命令结果只请求打开独立备用屏。
func TestDebugDisplayUnifiedPresenterOpensOverlayInsteadOfSceneCell(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(100, 40)
	coordinator.SetSurface(surface)

	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	output.Reset()

	// 结构化命令结果必须只携带 overlay 请求，不得携带可提交为 Scene cell 的
	// 文档。
	result, handled, err := tryExecuteStructuredChatCommand(session, "/debug display")
	if err != nil || !handled {
		t.Fatalf("/debug display structured match=(%t, %v), want handled", handled, err)
	}
	if !result.OpenDebugOverlay {
		t.Fatalf("/debug display did not request the alternate-screen overlay: %+v", result)
	}
	if got := ui.RenderDocumentPlain(result.Document()); strings.TrimSpace(got) != "" {
		t.Fatalf("/debug display must not carry a Scene-cell document, got: %q", got)
	}

	if quit := dispatchChatCommand(session, "/debug display", false); quit {
		t.Fatal("/debug display unexpectedly requested chat exit")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Contains(transcript.String(), "会话文件与目录:") {
		t.Errorf("main transcript must NOT contain /debug display content (it belongs on the alternate screen):\n%s", transcript.String())
	}
	if len(state.Transcript.Cells) != 0 {
		t.Errorf("main transcript must stay empty after /debug display, got %d cells:\n%s", len(state.Transcript.Cells), transcript.String())
	}

	plain := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	for _, marker := range []string{"会话文件与目录:", "Session Store:", "运行时调试:", "Mailbox Pending:"} {
		if !strings.Contains(plain, marker) {
			continue // 该节在本次会话条件下不输出
		}
		if strings.Contains(output.String(), marker) {
			t.Errorf("primary TerminalSession output must NOT contain %q (debug content belongs on the alternate screen)", marker)
		}
	}
}

// TestDebugDisplayOverlayGatedWithoutFullScreenTTY 验证备用屏打开器在测试环境
// （无真实 TTY / 无 Layout）下安全失败，绝不回退到主信息流。
func TestDebugDisplayOverlayGatedWithoutFullScreenTTY(t *testing.T) {
	session := &ChatSession{}
	if canOpenChatDebugOverlay(session) {
		t.Fatal("canOpenChatDebugOverlay must be false without a unified surface")
	}
}
