package commands

import (
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// rowOf returns the single screen row holding marker, failing when the marker is
// missing or duplicated (both would make a gap assertion meaningless).
func rowOf(t *testing.T, screen *screenVT, marker string) int {
	t.Helper()
	rows := screen.RowsContaining(marker)
	if len(rows) != 1 {
		t.Fatalf("expected %q exactly once on screen, got rows %v\n%s", marker, rows, screen.dump())
	}
	return rows[0]
}

// /load used to raw-print after ClearPrompt, which ownedViewport never retained.
// Production now routes the status line through WriteOutput so the subsequent
// history replay keeps seed → status → header dense (no multi-row blank hole).
func TestDispatchChatCommand_RawOutputBeforeHistoryKeepsTranscriptDense(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 80, 24
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	session.Surface = surface
	coord.SetSurface(surface)
	coord.promptAdvanceFn = func() bool { return false }
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	})

	screen := newScreenVT(width, height)
	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "上一轮助手回复内容\n"); !ok || err != nil {
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
	}))

	// /load: dispatch clears the prompt, the handler writes its status line
	// through WriteOutput (printDirectInteractiveOutput), then replays history
	// through the surface so both land in the owned transcript densily.
	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		beginDirectInteractiveOutput(session)
		printfDirectInteractiveOutput(session, "会话已加载\n")
		if count := printVisibleChatHistory(session, "已加载历史会话"); count != 2 {
			t.Fatalf("expected 2 replayed messages, got %d", count)
		}
	}))

	dump := screen.dump()
	seedRow := rowOf(t, screen, "上一轮助手回复内容")
	statusRow := rowOf(t, screen, "会话已加载")
	headerRow := rowOf(t, screen, "已加载历史会话")
	if statusRow-seedRow-1 > 1 {
		t.Fatalf("raw command output left %d blank rows below the transcript (rows %d→%d)\n%s",
			statusRow-seedRow-1, seedRow, statusRow, dump)
	}
	if headerRow-statusRow-1 > 1 {
		t.Fatalf("history header left %d blank rows below the command output (rows %d→%d)\n%s",
			headerRow-statusRow-1, statusRow, headerRow, dump)
	}
	// No multi-row hole anywhere above the bottom pane.
	if run, at := maxBlankRunAboveBottom(screen, headerRow); run > 1 {
		t.Fatalf("blank run of %d rows at row %d above the history header\n%s", run, at, dump)
	}
	for _, marker := range []string{"查看 docs", "目录里有 README。"} {
		if !strings.Contains(dump, marker) {
			t.Fatalf("expected replayed history to contain %q\n%s", marker, dump)
		}
	}
}
