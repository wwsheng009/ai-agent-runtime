package commands

import (
	"fmt"
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

// dispatchChatCommand hands the terminal to raw fmt.Print* output: it clears the
// surface prompt (which defers bottom-reserve shrink compensation) and then the
// command handler writes through plain stdout, which the surface never sees.
//
// If that deferred shrink is still outstanding when the raw bytes land, the
// later flush (here: the settle inside history replay) scrolls the transcript
// and leaves a multi-row blank hole between the previous transcript, the command
// output, and the replayed history — the abnormal blank lines seen on /load.
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

	// /load: dispatch clears the prompt, the handler raw-prints its status line,
	// then replays history through the surface.
	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		beginDirectInteractiveOutput(session)
		fmt.Println("会话已加载")
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
