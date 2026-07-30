package commands

import (
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// replayHistoryTranscriptSkeleton replays a fixed multi-turn history through a
// real FixedBottomSurface and returns the reconstructed transcript region as a
// CONTENT/BLANK skeleton. When armWaiting is true it first arms the ready-prompt
// state (prompt reserved + waitingActive), reproducing a `/history` or `/resume`
// issued while the composer is waiting for input.
func replayHistoryTranscriptSkeleton(t *testing.T, armWaiting bool) []string {
	t.Helper()
	const width, height = 80, 24

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	coord.promptAdvanceFn = func() bool { return false }
	session.Interaction = coord
	session.Surface = surface

	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("第一个问题"),
		*runtimetypes.NewAssistantMessage("第一个回答"),
		*runtimetypes.NewUserMessage("第二个问题"),
		*runtimetypes.NewAssistantMessage("第二个回答"),
	})

	screen := newScreenVT(width, height)
	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.SetSurface(surface)
		// Establish a ready prompt exactly like the interactive loop does.
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected initial ShowPrompt")
		}
		if armWaiting {
			// The state a `/history` command runs in: prompt reserved and the
			// turn armed for input. beginDirectInteractiveOutput clears the
			// prompt but leaves waitingActive set.
			coord.mu.Lock()
			coord.promptVisible = true
			coord.promptRenderedOnSurface = true
			coord.waitingActive = true
			coord.mu.Unlock()
		}
		// Real dispatch path: clear prompt (defers shrink), settle, replay.
		beginDirectInteractiveOutput(session)
		if count := printVisibleChatHistory(session, "对话历史"); count != 4 {
			t.Fatalf("expected 4 replayed messages, got %d\n%s", count, screen.dump())
		}
	}))

	markers := []string{"第一个问题", "第一个回答", "第二个问题", "第二个回答"}
	first := screen.RowsContaining("对话历史")
	if len(first) != 1 {
		t.Fatalf("expected history header once, got %v\n%s", first, screen.dump())
	}
	top := first[0]
	last := top
	for _, m := range markers {
		rows := screen.RowsContaining(m)
		if len(rows) != 1 {
			t.Fatalf("expected %q exactly once, got %v\n%s", m, rows, screen.dump())
		}
		if rows[0] > last {
			last = rows[0]
		}
	}

	skeleton := make([]string, 0, last-top+1)
	for row := top; row <= last; row++ {
		if strings.TrimSpace(screen.line(row)) == "" {
			skeleton = append(skeleton, "BLANK")
			continue
		}
		skeleton = append(skeleton, "CONTENT:"+strings.TrimSpace(screen.line(row)))
	}
	return skeleton
}

// TestPrintVisibleChatHistory_ReplayIgnoresPromptCompensation pins the render/
// data plane isolation for history replay: replaying already-final transcript
// must not depend on whether the composer was waiting for input. When it did,
// RenderSubmittedUserInput re-showed the prompt between replayed messages, grew
// the bottom reserve, and let the surface bill scroll compensation into the
// replayed history as extra blank rows.
func TestPrintVisibleChatHistory_ReplayIgnoresPromptCompensation(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	baseline := replayHistoryTranscriptSkeleton(t, false)
	waiting := replayHistoryTranscriptSkeleton(t, true)

	// No multi-row blank hole may appear inside the replayed transcript in
	// either state (compensation would show up here).
	for _, line := range []struct {
		name     string
		skeleton []string
	}{{"baseline", baseline}, {"waiting", waiting}} {
		for i := 1; i < len(line.skeleton); i++ {
			if line.skeleton[i] == "BLANK" && line.skeleton[i-1] == "BLANK" {
				t.Fatalf("%s: consecutive blank rows at %d inside replayed history:\n%s",
					line.name, i, strings.Join(line.skeleton, "\n"))
			}
		}
	}

	// Strong oracle: the replayed transcript is identical regardless of the
	// waiting state, i.e. no layout compensation leaked into the content plane.
	if strings.Join(baseline, "\n") != strings.Join(waiting, "\n") {
		t.Fatalf("history replay differs by waiting state (compensation leaked):\nbaseline:\n%s\n\nwaiting:\n%s",
			strings.Join(baseline, "\n"), strings.Join(waiting, "\n"))
	}
}
