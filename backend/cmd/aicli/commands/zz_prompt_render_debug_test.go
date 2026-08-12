package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestUnifiedPromptWakeChainDebug(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(100, 30)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	var terminal bytes.Buffer
	if !coord.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)
	terminal.Reset()

	// 先模拟 composer 有输入（prompt 提交前 AppState 显示输入内容）。
	coord.postUIAction(ui.ShowPromptAction{Line: "❯"})
	coord.postUIAction(ui.TrackPromptInputAction{Line: "❯", Input: "prompt-wake-debug"})
	coord.waitUIActorIdle()
	coord.RenderSubmittedUserInput("prompt-wake-debug")

	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	st := coord.uiActor.AppState()
	t.Logf("Bottom.PromptVisible=%v PromptInput=%q PromptLine=%q LastAction=%v",
		st.Bottom.PromptVisible, st.Bottom.PromptInput, st.Bottom.PromptLine, coord.uiActor.Stats().LastAction)
	if st.Bottom.PromptInput != "" {
		t.Fatalf("composer 输入未清空: %q", st.Bottom.PromptInput)
	}
	if !st.Bottom.PromptVisible {
		t.Fatalf("空提示符未恢复显示")
	}
	snap := bridge.sceneSnapshot()
	if snap == nil || len(snap.Cells) == 0 || !strings.Contains(snap.Cells[0].Source, "prompt-wake-debug") {
		t.Fatalf("scene 缺少 user cell")
	}
	got := terminal.String()
	if !strings.Contains(got, "prompt-wake-debug") {
		t.Fatalf("prompt not rendered:\n%q", got)
	}
}
