package commands

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// /load used to raw-print after ClearPrompt, which ownedViewport never retained.
// Production now routes the status line through WriteOutput so the subsequent
// history replay keeps seed → status → header dense (no multi-row blank hole).
// L3-2 后物理绘制退役：density 的权威观察面是合成帧行号，而不是回放字节。
func TestDispatchChatCommand_RawOutputBeforeHistoryKeepsTranscriptDense(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 80, 24
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)

	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	session.Surface = surface
	coord.SetSurface(surface)
	coord.promptAdvanceFn = func() bool { return false }
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	})

	coord.SetWriter(os.Stdout)
	if !surface.ShowPrompt("> ") {
		t.Fatal("expected prompt")
	}
	if !surface.ClearPromptRows(1) {
		t.Fatal("expected prompt clear")
	}
	if _, err, ok := surface.WriteOutput(io.Discard, "上一轮助手回复内容\n"); !ok || err != nil {
		t.Fatalf("seed WriteOutput: ok=%t err=%v", ok, err)
	}
	// Ready prompt: reserve grows again and absorbs the trailing blank.
	if !surface.ShowPrompt("> ") {
		t.Fatal("expected ready prompt")
	}
	coord.mu.Lock()
	coord.promptVisible = true
	coord.promptRenderedOnSurface = true
	coord.mu.Unlock()

	// /load: dispatch clears the prompt, the handler writes its status line
	// through WriteOutput (printDirectInteractiveOutput), then replays history
	// through the surface so both land in the owned transcript densily.
	beginDirectInteractiveOutput(session)
	printfDirectInteractiveOutput(session, "会话已加载\n")
	if count := printVisibleChatHistory(session, "已加载历史会话"); count != 2 {
		t.Fatalf("expected 2 replayed messages, got %d", count)
	}

	frameLines := composedFrameLines(surface)
	frame := strings.Join(frameLines, "\n")
	seedRow := frameRowOf(t, surface, "上一轮助手回复内容")
	statusRow := frameRowOf(t, surface, "会话已加载")
	headerRow := frameRowOf(t, surface, "已加载历史会话")
	if statusRow-seedRow-1 > 1 {
		t.Fatalf("raw command output left %d blank rows below the transcript (rows %d→%d)\n%s",
			statusRow-seedRow-1, seedRow, statusRow, frame)
	}
	if headerRow-statusRow-1 > 1 {
		t.Fatalf("history header left %d blank rows below the command output (rows %d→%d)\n%s",
			headerRow-statusRow-1, statusRow, headerRow, frame)
	}
	// No multi-row hole anywhere inside the seed → replayed transcript region.
	assertNoFrameBlankRun(t, "direct output before history", surface,
		"上一轮助手回复内容", "目录里有 README。")
	for _, marker := range []string{"查看 docs", "目录里有 README。"} {
		if !strings.Contains(frame, marker) {
			t.Fatalf("expected replayed history to contain %q\n%s", marker, frame)
		}
	}
}
