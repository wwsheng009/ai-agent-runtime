package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// TestUnifiedPromptRendersImmediatelyWithoutLLMResponse 验证 unified 模式下
// 用户输入 prompt 后、LLM 响应尚未到达时，prompt 必须立即渲染到终端。
func TestUnifiedPromptRendersImmediatelyWithoutLLMResponse(t *testing.T) {
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

	// 提交用户输入，不模拟任何 LLM 响应。
	coord.RenderSubmittedUserInput("你好，请立即显示我")

	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	got := terminal.String()
	if !strings.Contains(got, "> 你好，请立即显示我") {
		t.Fatalf("prompt 未立即渲染或缺少 '> ' 前缀（LLM 响应未到时）:\n%q", got)
	}
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) == 0 {
		t.Fatalf("scene snapshot 缺少 user cell")
	}
	if !strings.Contains(snapshot.Cells[0].Source, "你好，请立即显示我") {
		t.Fatalf("scene 第一个 cell 不是该 user 输入: %+v", snapshot.Cells[0])
	}
}
