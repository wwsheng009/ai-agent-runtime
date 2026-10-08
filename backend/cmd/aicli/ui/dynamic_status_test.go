package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestFixedBottomSurface_DynamicStatusRendersAbovePrompt(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)

	captureUIStdout(t, func() {
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
	frameLines := strings.Split(frameDump(surface.ComposedFrameForTest()), "\n")
	assertFrameTextAtRow := func(text string, row int) {
		t.Helper()
		if row < 1 || row > len(frameLines) {
			t.Fatalf("row %d out of frame range 1..%d", row, len(frameLines))
		}
		if !strings.Contains(frameLines[row-1], text) {
			t.Fatalf("expected %q at frame row %d, got %q", text, row, frameLines[row-1])
		}
	}
	assertFrameTextAtRow("◦ Analyzing", 20)
	assertFrameTextAtRow("> ", 22)
	assertFrameTextAtRow("Plan OFF", 24)
}

func TestFixedBottomSurface_OwnedComposerWriteKeepsDynamicStatusWhole(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 80, 24
	surface := newTestFixedBottomSurfaceWithSize(width, height)
	var editorOutput strings.Builder

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt to render")
		}
		surface.SetStatusModels(
			style.StatusLineModel{State: style.RunReady, StateText: "Plan OFF"},
			&style.StatusLineModel{
				State:     style.RunThinking,
				StateText: "◦ Analyzing (1m 41s • esc to interrupt)",
			},
		)
		if !surface.WritePromptEditorText(&editorOutput, 0, 0, "hello") {
			t.Fatal("expected prompt editor write to be surface-owned")
		}
	})

	frame := frameDump(surface.ComposedFrameForTest())
	if !strings.Contains(frame, "◦ Analyzing (1m 41s • esc to interrupt)") {
		t.Fatalf("dynamic status was fragmented after composer write:\n%s", frame)
	}
	if !strings.Contains(frame, ">") {
		t.Fatalf("prompt was lost after composer write:\n%s", frame)
	}
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
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 10)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt to render")
		}
	})

	if got := surface.bottomRowsLocked(); got != 2 {
		t.Fatalf("short terminal should reserve only prompt + footer, got %d rows", got)
	}
	frameLines := strings.Split(frameDump(surface.ComposedFrameForTest()), "\n")
	if len(frameLines) != 10 {
		t.Fatalf("short terminal composed frame rows=%d, want 10", len(frameLines))
	}
	if got := frameLines[8]; !strings.HasPrefix(got, ">") {
		t.Fatalf("short terminal should keep the prompt adjacent to the footer, frame row 9=%q", got)
	}
}
