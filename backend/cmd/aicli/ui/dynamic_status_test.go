package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

func TestFixedBottomSurface_DynamicStatusRendersAbovePrompt(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt to render")
		}
		surface.SetStatusModels(
			style.StatusLineModel{
				State:     style.RunReady,
				StateText: "Plan OFF",
			},
			&style.StatusLineModel{
				State:     style.RunThinking,
				StateText: "◦ Analyzing (2m 20s • esc to interrupt)",
			},
		)
	})

	if got := surface.bottomRowsLocked(); got != 5 {
		t.Fatalf("expected dynamic + composer margins + prompt + footer rows, got %d", got)
	}
	assertTextPaintedAtRow := func(text string, row int) {
		t.Helper()
		textIndex := strings.LastIndex(output, text)
		if textIndex < 0 {
			t.Fatalf("expected %q in output %q", text, output)
		}
		move := terminalMoveToSequence(row, 1)
		if strings.LastIndex(output[:textIndex], move) < 0 {
			t.Fatalf("expected %q to be painted at row %d, got %q", text, row, output)
		}
	}
	assertTextPaintedAtRow("◦ Analyzing", 20)
	assertTextPaintedAtRow("> ", 22)
	assertTextPaintedAtRow("Plan OFF", 24)
}

func TestBottomPaneStateDynamicStatusReservesOneRow(t *testing.T) {
	state := BottomPaneState{
		DynamicStatusModel:     &style.StatusLineModel{StateText: "◦ Working"},
		PromptReservedRows:     1,
		PromptTopMarginRows:    chatComposerTopMarginRows,
		PromptBottomMarginRows: chatComposerBottomMarginRows,
	}
	if got := state.dynamicStatusVisibleRowCount(); got != 1 {
		t.Fatalf("dynamic status visible rows=%d, want 1", got)
	}
	if got := state.promptAreaVisibleRowCount(); got != 4 {
		t.Fatalf("prompt area rows=%d, want dynamic + margins + prompt = 4", got)
	}
}

func TestFixedBottomSurface_ComposerMarginsCollapseOnShortTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newTestFixedBottomSurfaceWithSize(80, 10)

	output := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt to render")
		}
	})

	if got := surface.bottomRowsLocked(); got != 2 {
		t.Fatalf("short terminal should reserve only prompt + footer, got %d rows", got)
	}
	screen := vt.NewScreen(80, 10)
	screen.Feed(output)
	if got := screen.Line(9); !strings.HasPrefix(got, ">") {
		t.Fatalf("short terminal should keep the prompt adjacent to the footer, row 9=%q\n%s", got, screen.Dump())
	}
}
