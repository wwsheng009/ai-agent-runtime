package ui

import (
	"bytes"
	"fmt"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func fixedStatusTestContext(theme *Theme) style.ThemeContext {
	return ThemeContextForTheme(theme, style.ColorProfile{ColorProfile: render.ColorProfile{
		Enabled: true,
		Depth:   render.ColorANSI16,
	}})
}

func assertANSI16Status(t *testing.T, got, plain string) {
	t.Helper()
	if visible := render.ANSIToPlain(got); visible != plain {
		t.Fatalf("status changed plain text: got %q want %q", visible, plain)
	}
	if !strings.ContainsRune(got, '\x1b') {
		t.Fatalf("status has no ANSI styling: %q", got)
	}
	if strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[38;5;") {
		t.Fatalf("ANSI-16 status contains higher-depth color: %q", got)
	}
}

func TestFormatFixedStatusModelColorsStateOnly(t *testing.T) {
	theme := createTheme(ThemeDark)
	got := formatFixedStatusModelWithContext(style.StatusLineModel{
		State:     style.RunReady,
		Separator: " | ",
		Segments:  []style.StatusSegment{{Text: "model mimo"}},
	}, 80, fixedStatusTestContext(theme))
	assertANSI16Status(t, got, "Ready | model mimo")
}

func TestFormatFixedStatusModelColorsCodexSeparator(t *testing.T) {
	theme := createTheme(ThemeDark)
	plain := "思考 · gpt-5.4-code high · Context 14% used"
	got := formatFixedStatusModelWithContext(style.StatusLineModel{
		State:     style.RunThinking,
		StateText: "思考",
		Segments: []style.StatusSegment{
			{Text: "gpt-5.4-code high", Role: style.RoleAccent},
			{Text: "Context 14% used", Role: style.RoleProgress},
		},
	}, 80, fixedStatusTestContext(theme))
	assertANSI16Status(t, got, plain)
}

func TestFormatFixedStatusModelColorsModelFirstLine(t *testing.T) {
	theme := createTheme(ThemeDark)
	plain := "gpt-5.6-sol xhigh · Context 90% used · Fast off"
	got := formatFixedStatusModelWithContext(style.StatusLineModel{
		HideState: true,
		Segments: []style.StatusSegment{
			{Text: "gpt-5.6-sol xhigh", Role: style.RoleAccent},
			{Text: "Context 90% used", Role: style.RoleProgress},
			{Text: "Fast off", Role: style.RoleTextMuted},
		},
	}, 80, fixedStatusTestContext(theme))
	assertANSI16Status(t, got, plain)
}

func TestFormatFixedStatusModelPreservesSegmentRoles(t *testing.T) {
	theme := createTheme(ThemeDark)
	model := style.StatusLineModel{
		HideState: true,
		Segments: []style.StatusSegment{
			{Text: "gpt-5.6-sol", Role: style.RoleAccent},
			{Text: "Context 14% used", Role: style.RoleProgress},
		},
	}
	got := formatFixedStatusModelWithContext(model, 80, fixedStatusTestContext(theme))
	assertANSI16Status(t, got, "gpt-5.6-sol · Context 14% used")
}

func TestFormatFixedStatusModelStateUsesDifferentColors(t *testing.T) {
	ctx := fixedStatusTestContext(createTheme(ThemeDark))
	seen := map[string]string{}
	for _, state := range []style.RunState{style.RunReady, style.RunStreaming, style.RunThinking, style.RunWaiting, style.RunError} {
		got := formatFixedStatusModelWithContext(style.StatusLineModel{State: state}, 80, ctx)
		assertANSI16Status(t, got, string(state))
		if prior, ok := seen[got]; ok {
			t.Fatalf("states %s and %s resolved to identical style %q", prior, state, got)
		}
		seen[got] = string(state)
	}
}

func TestFixedBottomSurface_ShowPopupClampsToViewportHeight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	lines := make([]string, 0, 40)
	for i := 1; i <= 40; i++ {
		lines = append(lines, strings.Repeat("x", i))
	}

	output := captureUIStdout(t, func() {
		surface.ShowPopup(lines)
	})

	if got := surface.popupRenderedRows; got != 21 {
		t.Fatalf("expected popup to clamp to 21 visible rows, got %d", got)
	}
	if got := surface.bottomRowsLocked(); got != 23 {
		t.Fatalf("expected bottom rows to reserve one output row, got %d", got)
	}
	if got := surface.popupRenderedGapRows; got != 1 {
		t.Fatalf("expected popup to reserve one input gap row, got %d", got)
	}
	if surface.popupLines == nil || len(surface.popupLines) != 40 {
		t.Fatalf("expected popupLines to retain full payload, got %#v", surface.popupLines)
	}
	if !strings.Contains(output, "选择模型") && !strings.Contains(output, "x") {
		t.Fatalf("expected popup render to emit visible popup content, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPopupReservesInputRowBelowPopup(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.ShowPopup([]string{
			"命令补全: /",
			"> /help",
		})
	})

	if surface.popupRenderedRows != 2 {
		t.Fatalf("expected two popup rows, got %d", surface.popupRenderedRows)
	}
	if surface.popupRenderedGapRows != 1 {
		t.Fatalf("expected one reserved input gap row, got %d", surface.popupRenderedGapRows)
	}
	if surface.bottomRowsLocked() != 4 {
		t.Fatalf("expected popup rows + input gap + status, got %d", surface.bottomRowsLocked())
	}
	if got := surface.popupStartRowLocked(surface.popupRenderedRows, surface.popupRenderedGapRows); got != 21 {
		t.Fatalf("expected popup to start at row 21 so row 23 remains for input, got %d", got)
	}
	if strings.Contains(output, "提示: ↑↓") {
		t.Fatalf("expected slash usage hint line to be omitted, got %q", output)
	}
	if !strings.Contains(output, "\x1b[21;1H") {
		t.Fatalf("expected last popup line to render on row 21, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPopupBelowPromptExpandsDownward(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if !surface.SetPromptInputState("> ", "/he", 1, 0, 5) {
			t.Fatal("expected enabled surface to track prompt input")
		}
	})

	output := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /",
			"> /help",
		}, "slash_completion")
	})

	if surface.bottomRowsLocked() != 6 {
		t.Fatalf("expected composer margins + prompt + popup rows + status, got %d", surface.bottomRowsLocked())
	}
	if got := surface.outputBottomRowLocked(); got != 18 {
		t.Fatalf("expected output region to shift above prompt and popup, got row %d", got)
	}
	if got := surface.promptBottomRowLocked(); got != 20 {
		t.Fatalf("expected prompt row above downward popup, got row %d", got)
	}
	if surface.popupRenderedStartRow != 22 {
		t.Fatalf("expected popup to start below prompt at row 22, got %d", surface.popupRenderedStartRow)
	}
	if !strings.Contains(output, "\x1b[1;20r\x1b[1;1H\x1b[20;1H\n\n\x1b[1;18r") {
		t.Fatalf("expected output region to scroll up before reserving slash popup rows, got %q", output)
	}
	if !strings.Contains(output, "\x1b[1;18r") {
		t.Fatalf("expected scroll region to shift above prompt and popup, got %q", output)
	}
	if !strings.Contains(output, "\x1b[20;1H\x1b[K") || !strings.Contains(output, "\x1b[20;1H> /he") {
		t.Fatalf("expected prompt input row to move above slash popup, got %q", output)
	}
	if count := strings.Count(output, "\x1b[20;1H> /he"); count != 1 {
		t.Fatalf("expected slash popup expansion to render prompt input once, got %d in %q", count, output)
	}
	if !strings.Contains(output, "\x1b[22;1H\x1b[K命令补全") {
		t.Fatalf("expected slash popup to render below prompt, got %q", output)
	}
	if strings.Contains(output, "\x1b[20;1H\x1b[K命令补全") {
		t.Fatalf("expected slash popup not to render on prompt row, got %q", output)
	}
	if strings.Contains(output, cursorSaveSequence) || strings.Contains(output, cursorRestoreSequence) {
		t.Fatalf("expected downward popup render to move to prompt cursor instead of saved cursor restore, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[20;6H"+cursorShowSequence) {
		t.Fatalf("expected downward popup render to leave cursor on lifted prompt row, got %q", output)
	}

	updateOutput := captureUIStdout(t, func() {
		if !surface.SetPromptInputState("> ", "/help", 1, 0, 7) {
			t.Fatal("expected enabled surface to update lifted prompt input")
		}
	})
	if !strings.Contains(updateOutput, "\x1b[20;1H> /help") {
		t.Fatalf("expected prompt updates to keep rendering above downward popup, got %q", updateOutput)
	}
	if strings.Contains(updateOutput, cursorSaveSequence) || strings.Contains(updateOutput, cursorRestoreSequence) {
		t.Fatalf("expected prompt updates during downward popup to restore tracked prompt cursor directly, got %q", updateOutput)
	}
	if !strings.HasSuffix(updateOutput, "\x1b[20;8H"+cursorShowSequence) {
		t.Fatalf("expected prompt update to leave cursor on lifted prompt row, got %q", updateOutput)
	}

	secondOutput := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /h",
			"> /help     显示命令帮助",
		}, "slash_completion")
	})
	if strings.Contains(secondOutput, "\n\n") || strings.Contains(secondOutput, "\x1b[1;20r\x1b[1;1H\x1b[20;1H") {
		t.Fatalf("expected same slash popup update not to scroll output again, got %q", secondOutput)
	}
	if !strings.Contains(secondOutput, "\x1b[20;1H> /help") {
		t.Fatalf("expected same slash popup update to keep prompt row fixed, got %q", secondOutput)
	}
	if !strings.HasSuffix(secondOutput, "\x1b[20;8H"+cursorShowSequence) {
		t.Fatalf("expected same slash popup update to leave cursor on lifted prompt row, got %q", secondOutput)
	}

	prefix, ok := surface.PromptCursorPrefix(0, 7)
	if !ok || !strings.HasSuffix(prefix, "\x1b[20;8H") {
		t.Fatalf("expected prompt cursor prefix to target row above popup, ok=%t prefix=%q", ok, prefix)
	}

	clearOutput := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("slash_completion")
	})
	if !strings.Contains(clearOutput, "\x1b[22;1H\x1b[K") || !strings.Contains(clearOutput, "\x1b[23;1H\x1b[K") {
		t.Fatalf("expected downward popup rows to clear, got %q", clearOutput)
	}
	if !strings.Contains(clearOutput, "\x1b[20;1H\x1b[K") {
		t.Fatalf("expected old lifted prompt row to clear when popup closes, got %q", clearOutput)
	}
	if !strings.Contains(clearOutput, "\x1b[22;1H\x1b[K") || !strings.Contains(clearOutput, "\x1b[22;1H> /help") {
		t.Fatalf("expected prompt input row to return below after popup clears, got %q", clearOutput)
	}
	if strings.Contains(clearOutput, cursorSaveSequence) || strings.Contains(clearOutput, cursorRestoreSequence) {
		t.Fatalf("expected downward popup clear to move to relocated prompt cursor instead of saved cursor restore, got %q", clearOutput)
	}
	if !strings.HasSuffix(clearOutput, "\x1b[22;8H"+cursorShowSequence) {
		t.Fatalf("expected downward popup clear to leave cursor on restored prompt row, got %q", clearOutput)
	}

	reopenOutput := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /",
			"> /help",
		}, "slash_completion")
	})
	if strings.Contains(reopenOutput, "\n\n") || strings.Contains(reopenOutput, "\x1b[1;20r\x1b[1;1H\x1b[20;1H") {
		t.Fatalf("expected reopening slash popup without new output not to scroll output again, got %q", reopenOutput)
	}
	if !strings.Contains(reopenOutput, "\x1b[20;1H> /help") {
		t.Fatalf("expected reopened slash popup to reuse lifted prompt row, got %q", reopenOutput)
	}
}

func TestFixedBottomSurface_BeginOutputDoesNotRepeatPopupScrollCompensation(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"one", "two"}, "command_popup")
		surface.ClearPopupForOwnerPreserveCursor("command_popup")
	})

	output := captureUIStdout(t, func() {
		// Slash-command dispatch positions the cursor before it knows whether
		// the command will write output or open another transient popup.
		surface.BeginOutput()
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"one", "two"}, "command_popup")
	})

	if strings.Contains(output, "\n\n") || strings.Contains(output, "\x1b[22;1H\n\n") {
		t.Fatalf("expected cursor positioning alone not to repeat popup scroll compensation, got %q", output)
	}
}

func TestFixedBottomSurface_ActualOutputRecomputesPopupScrollCompensation(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"one", "two"}, "command_popup")
		surface.ClearPopupForOwnerPreserveCursor("command_popup")
		if _, err, handled := surface.WriteOutput(os.Stdout, "new output\n"); !handled || err != nil {
			t.Fatalf("expected actual output to be written, handled=%t err=%v", handled, err)
		}
	})

	output := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"one", "two"}, "command_popup")
	})

	// The trailing output newline already owns one blank row, so a two-row
	// popup needs one fresh scroll row rather than replaying the old two rows.
	if !regexp.MustCompile(`\x1b\[[0-9]+;1H\n`).MatchString(output) {
		t.Fatalf("expected actual output to require fresh one-row popup compensation, got %q", output)
	}
	if strings.Contains(output, "\x1b[22;1H\n\n") {
		t.Fatalf("expected trailing output blank to absorb one popup row, got %q", output)
	}
}

func TestFixedBottomSurface_TrackPromptInputStateDoesNotRedraw(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.TrackPromptInputState("> ", "/help", 1, 0, 7) {
			t.Fatal("expected enabled surface to track prompt input")
		}
	})
	if output != "" {
		t.Fatalf("expected tracking active input to avoid terminal redraw, got %q", output)
	}

	prefix, ok := surface.PromptCursorPrefix(0, 3)
	if !ok || !strings.HasSuffix(prefix, "\x1b[22;4H") {
		t.Fatalf("expected prefix to target requested redraw start cursor, ok=%t prefix=%q", ok, prefix)
	}

	popupOutput := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /h",
			"> /help     显示命令帮助",
		}, "slash_completion")
	})
	if !strings.Contains(popupOutput, "\x1b[20;1H> /help") {
		t.Fatalf("expected later popup render to use tracked prompt input, got %q", popupOutput)
	}
	if !strings.HasSuffix(popupOutput, "\x1b[20;8H"+cursorShowSequence) {
		t.Fatalf("expected later popup render to restore tracked cursor, got %q", popupOutput)
	}
}

func TestFixedBottomSurface_TrackPromptInputStateRedrawsWhenRowsChange(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.TrackPromptInputState("> ", "first\nsecond", 2, 1, 6) {
			t.Fatal("expected enabled surface to track prompt input")
		}
	})

	if !strings.Contains(output, "\x1b[21;1H> first\r\nsecond") {
		t.Fatalf("expected row growth tracking to redraw multiline prompt input, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[22;7H"+cursorShowSequence) {
		t.Fatalf("expected row growth tracking to restore prompt cursor, got %q", output)
	}
}

func TestFixedBottomSurface_BoundsMultilinePromptAndFollowsCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	input := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight"
	output := captureUIStdout(t, func() {
		if !surface.TrackPromptInputState("> ", input, 8, 7, len("eight")) {
			t.Fatal("expected enabled surface to track multiline input")
		}
	})

	if surface.promptReservedRows != ChatComposerMaxVisibleRows || surface.promptViewportStart != 2 {
		t.Fatalf("expected bounded viewport, rows=%d start=%d", surface.promptReservedRows, surface.promptViewportStart)
	}
	if strings.Contains(output, "> one") || !strings.Contains(output, "three\r\nfour") {
		t.Fatalf("expected only the cursor-adjacent viewport to render, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[22;6H"+cursorShowSequence) {
		t.Fatalf("expected cursor on the final visible row, got %q", output)
	}
}

func TestFixedBottomSurface_PromptInputMaxVisibleRowsReservesSmallTerminalContext(t *testing.T) {
	surface := newTestFixedBottomSurface()
	surface.terminal.height = 10
	surface.promptNoticeLine = "queue\nattachments\npreview"

	if got := surface.PromptInputMaxVisibleRows(); got != 4 {
		t.Fatalf("expected four editor rows after context reservation, got %d", got)
	}

	surface.terminal.height = 5
	surface.promptNoticeLine = "queue"
	if got := surface.PromptInputMaxVisibleRows(); got != 1 {
		t.Fatalf("expected one editor row in a minimal terminal, got %d", got)
	}
}

func TestFixedBottomSurface_SetPromptStateUsesDynamicVisibleRowBudget(t *testing.T) {
	surface := newTestFixedBottomSurface()
	surface.terminal.height = 10
	surface.setPromptStateLocked("> ", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight", 8, 7, len("eight"))
	if surface.promptReservedRows != 6 || surface.promptViewportStart != 2 {
		t.Fatalf("expected initial six-row viewport, rows=%d start=%d", surface.promptReservedRows, surface.promptViewportStart)
	}

	surface.promptNoticeLine = "queue\nattachments\npreview"
	surface.reflowPromptViewportLocked()

	if surface.promptReservedRows != 4 || surface.promptViewportStart != 4 {
		t.Fatalf(
			"expected dynamic four-row viewport following the cursor, rows=%d start=%d",
			surface.promptReservedRows,
			surface.promptViewportStart,
		)
	}

	surface.terminal.height = 5
	surface.promptNoticeLine = "queue"
	surface.reflowPromptViewportLocked()
	if surface.promptReservedRows != 1 || surface.promptViewportStart != 7 {
		t.Fatalf(
			"expected minimal viewport to retain the cursor row, rows=%d start=%d",
			surface.promptReservedRows,
			surface.promptViewportStart,
		)
	}
}

func TestFixedBottomSurface_RendersCursorAdjacentRowsWithinSmallTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	surface.terminal.height = 10
	surface.promptNoticeLine = "queue\nattachments\npreview"
	surface.promptEditorStatusLine = "multiline 8/8"
	surface.setPromptStateLocked("> ", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight", 8, 7, len("eight"))

	output := captureUIStdout(t, func() {
		surface.renderPromptRowsLocked(true)
		surface.restoreStoredPromptCursorLocked()
	})

	if strings.Contains(output, "> one") || !strings.Contains(output, "five\r\nsix") || !strings.Contains(output, "eight") {
		t.Fatalf("expected cursor-adjacent rows in the bounded viewport, got %q", output)
	}
	if strings.Contains(output, "\x1b[10;") {
		t.Fatalf("expected prompt rendering to leave the status row untouched, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[9;6H") {
		t.Fatalf("expected cursor on the final prompt row, got %q", output)
	}
}

func TestFixedBottomSurface_EditorStatusDoesNotReplaceRuntimeNotice(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.SetPromptNoticeLine("已排队 1 条消息")
	})
	output := captureUIStdout(t, func() {
		surface.SetPromptEditorStatusLine("多行 2/3 · Enter 发送")
	})
	if !strings.Contains(output, "已排队 1 条消息") || !strings.Contains(output, "多行 2/3") {
		t.Fatalf("expected runtime notice and editor status to coexist, got %q", output)
	}
}

func TestFixedBottomSurface_SetPromptInputStateRestoresPromptCursorWithoutPopup(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.SetPromptInputState("> ", "", 1, 0, 2) {
			t.Fatal("expected enabled surface to update prompt input")
		}
	})

	if strings.Contains(output, cursorSaveSequence) || strings.Contains(output, cursorRestoreSequence) {
		t.Fatalf("expected prompt input update to restore prompt cursor directly, got %q", output)
	}
	if !strings.Contains(output, "\x1b[22;1H> ") {
		t.Fatalf("expected prompt marker to remain rendered, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[22;3H"+cursorShowSequence) {
		t.Fatalf("expected cursor to return after prompt marker, got %q", output)
	}
}

func TestFixedBottomSurface_WritePromptEditorTextUsesAtomicCursorSequence(t *testing.T) {
	surface := newTestFixedBottomSurface()
	surface.promptLine = "> "
	surface.promptReservedRows = 1
	surface.promptCursorCol = 2
	surface.lastWidth = 80
	surface.lastHeight = 24
	surface.lastBottomRows = 2

	var output bytes.Buffer
	if !surface.WritePromptEditorText(&output, 0, 2, "draft") {
		t.Fatal("expected enabled surface to handle the editor write")
	}
	got := output.String()
	if !strings.HasPrefix(got, cursorHideSequence) || !strings.Contains(got, "\x1b[22;3Hdraft") || !strings.HasSuffix(got, cursorShowSequence) {
		t.Fatalf("expected one cursor-positioned atomic sequence, got %q", got)
	}
}

func TestFixedBottomSurface_SetPromptNoticeLineRendersAbovePrompt(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	notice := "• Message to be submitted after next tool call\n  - queued prompt"
	output := captureUIStdout(t, func() {
		if !surface.SetPromptNoticeLine(notice) {
			t.Fatal("expected enabled surface to render prompt notice")
		}
	})

	if surface.bottomRowsLocked() != 6 {
		t.Fatalf("expected notice + composer margins + prompt + status rows, got %d", surface.bottomRowsLocked())
	}
	if got := surface.outputBottomRowLocked(); got != 18 {
		t.Fatalf("expected output region to leave room for prompt notice, got row %d", got)
	}
	if !strings.Contains(output, "\x1b[19;1H") || !strings.Contains(output, "Message to be submitted") {
		t.Fatalf("expected prompt notice to render above prompt, got %q", output)
	}
	if !strings.Contains(output, "\x1b[20;1H") || !strings.Contains(output, "  - queued prompt") {
		t.Fatalf("expected queued message list to render below notice title, got %q", output)
	}
	if !strings.Contains(output, "\x1b[22;1H> ") {
		t.Fatalf("expected prompt marker to remain below notice, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[22;3H"+cursorShowSequence) {
		t.Fatalf("expected cursor to return to prompt after notice render, got %q", output)
	}

	clearOutput := captureUIStdout(t, func() {
		if !surface.SetPromptNoticeLine("") {
			t.Fatal("expected enabled surface to clear prompt notice")
		}
	})
	if surface.bottomRowsLocked() != 4 {
		t.Fatalf("expected notice row to be released, got %d bottom rows", surface.bottomRowsLocked())
	}
	if !strings.Contains(clearOutput, "\x1b[19;1H\x1b[K") || !strings.Contains(clearOutput, "\x1b[20;1H\x1b[K") {
		t.Fatalf("expected previous notice row to clear, got %q", clearOutput)
	}
	if !strings.Contains(clearOutput, "\x1b[22;1H> ") {
		t.Fatalf("expected prompt marker to remain rendered after clearing notice, got %q", clearOutput)
	}
}

func TestFixedBottomSurface_SetActiveBandRendersWithoutScrollbackCommit(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.SetActiveBand([]string{"• assistant", "Hello stable paragraph."}) {
			t.Fatal("expected SetActiveBand to succeed")
		}
	})
	// status(1) + composer margins(2) + prompt(1) + active(2) = 6
	if surface.bottomRowsLocked() != 6 {
		t.Fatalf("bottomRows=%d want 6; band=%v", surface.bottomRowsLocked(), surface.ActiveBandLines())
	}
	if !strings.Contains(output, "Hello stable paragraph.") {
		t.Fatalf("expected active band content in surface paint, got %q", output)
	}
	if got := surface.ActiveBandLines(); len(got) != 2 {
		t.Fatalf("ActiveBandLines=%v", got)
	}

	// Cap to the adaptive row budget — keep newest tail.
	budget := surface.ActiveBandRowBudget()
	if budget < ActiveBandMinRows {
		t.Fatalf("row budget %d below minimum", budget)
	}
	long := make([]string, 0, budget+3)
	for i := 0; i < budget+3; i++ {
		long = append(long, fmt.Sprintf("line-%d", i))
	}
	captureUIStdout(t, func() {
		_ = surface.SetActiveBand(long)
	})
	got := surface.ActiveBandLines()
	if len(got) != budget {
		t.Fatalf("expected cap %d, got %d %v", budget, len(got), got)
	}
	if got[0] != "line-3" || got[len(got)-1] != fmt.Sprintf("line-%d", budget+2) {
		t.Fatalf("expected newest tail, got %v", got)
	}

	captureUIStdout(t, func() {
		if !surface.ClearActiveBand() {
			t.Fatal("clear failed")
		}
	})
	if surface.bottomRowsLocked() != 4 {
		t.Fatalf("expected band released, bottomRows=%d", surface.bottomRowsLocked())
	}
	if surface.ActiveBandLines() != nil {
		t.Fatalf("expected nil band after clear")
	}
}

func TestFixedBottomSurface_DynamicStatusDoesNotOverlapActiveBand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newTestFixedBottomSurface()
	dynamic := style.StatusLineModel{State: style.RunStreaming, StateText: "Generating response"}

	output := captureUIStdout(t, func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, &dynamic)
	})
	if got, want := surface.promptRenderedStartRow, 23; got != want {
		t.Fatalf("dynamic-only stack start=%d want %d", got, want)
	}

	output += captureUIStdout(t, func() {
		if !surface.SetActiveBand([]string{"assistant", "mutable tail"}) {
			t.Fatal("expected ActiveBand update")
		}
	})

	if got, want := surface.promptRenderedStartRow, 21; got != want {
		t.Fatalf("bottom stack start=%d want %d", got, want)
	}
	if !strings.Contains(output, "\x1b[21;1Hassistant") || !strings.Contains(output, "\x1b[22;1Hmutable tail") {
		t.Fatalf("active rows were not rendered above dynamic status: %q", output)
	}
	if !strings.Contains(output, "\x1b[23;1H") || !strings.Contains(output, "Generating response") {
		t.Fatalf("dynamic status was not rendered on its own row: %q", output)
	}
}

func TestFixedBottomSurface_RemovingDynamicStatusReclaimsOutputRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newTestFixedBottomSurface()
	dynamic := style.StatusLineModel{State: style.RunStreaming, StateText: "Generating response"}
	captureUIStdout(t, func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, &dynamic)
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed line\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})

	output := captureUIStdout(t, func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, nil)
	})
	if !strings.Contains(output, terminalScrollDownSequence(1)) {
		t.Fatalf("dynamic status release did not reclaim output row: %q", output)
	}
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("dynamic status left pending compensation=%d", surface.pendingScrollDownRows)
	}
}

func TestFixedBottomSurface_SetActiveBandStyledPreservesRolesAndStripsControls(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("AICLI_COLOR_DEPTH", "truecolor")
	t.Setenv("FORCE_COLOR", "1")

	surface := newTestFixedBottomSurface()
	surface.terminal.driver.caps = TerminalCapabilities{Interactive: true, ANSI: true}
	if profile := surface.terminal.driver.ColorProfile(); !profile.Enabled || profile.Depth != render.ColorTrueColor {
		t.Fatalf("expected forced truecolor test profile, got %+v", profile)
	}
	output := captureUIStdout(t, func() {
		if !surface.SetActiveBandStyled([]render.Line{
			{Spans: []render.Span{{Text: "\x1b[2Jassistant", Style: render.Style{Role: string(style.RoleAccent)}}}},
			{Spans: []render.Span{{Text: "body\x1b]52;c;payload\x07", Style: render.Style{Role: string(style.RoleTextPrimary)}}}},
			{Spans: []render.Span{{Text: "keyword", Style: render.Style{Role: "Code.Keyword", Foreground: render.RGB(255, 0, 0)}}}},
		}) {
			t.Fatal("expected styled active band update to succeed")
		}
	})

	if strings.Contains(output, "\x1b[2J") || strings.Contains(output, "\x1b]52;") {
		t.Fatalf("dangerous active-band controls leaked to terminal: %q", output)
	}
	if !regexp.MustCompile(`\x1b\[[0-9;]*m`).MatchString(output) || !strings.Contains(output, "assistant") {
		t.Fatalf("expected semantic accent SGR styling, got %q", output)
	}
	if !strings.Contains(output, "\x1b[38;2;255;0;0mkeyword") {
		t.Fatalf("expected explicit Chroma-style token color, got %q", output)
	}
	if got := surface.ActiveBandLines(); len(got) != 3 || got[0] != "assistant" || got[1] != "body" || got[2] != "keyword" {
		t.Fatalf("unexpected sanitized plain projection: %#v", got)
	}
}

func TestFixedBottomSurface_SetActiveBandStyledNoColorEmitsNoSGR(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	output := captureUIStdout(t, func() {
		_ = surface.SetActiveBandStyled([]render.Line{{Spans: []render.Span{
			{Text: "assistant", Style: render.Style{Role: string(style.RoleAccent), Bold: true}},
		}}})
	})
	if regexp.MustCompile(`\x1b\[[0-9;]*m`).MatchString(output) {
		t.Fatalf("NO_COLOR styled active band emitted SGR: %q", output)
	}
	if !strings.Contains(output, "assistant") {
		t.Fatalf("expected visible active content without color, got %q", output)
	}
}

func TestFixedBottomSurface_RefreshActiveBandRepaintsUnchangedStyledFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		_ = surface.SetActiveBandStyled([]render.Line{{Spans: []render.Span{
			{Text: "assistant", Style: render.Style{Role: string(style.RoleAccent)}},
		}}})
	})
	output := captureUIStdout(t, func() {
		if !surface.RefreshActiveBand() {
			t.Fatal("expected active band refresh to succeed")
		}
	})
	if !strings.Contains(output, "assistant") {
		t.Fatalf("expected unchanged styled frame to repaint, got %q", output)
	}
}

func TestFixedBottomSurface_SetActiveBandRepaintsOnlyChangedRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		_ = surface.SetActiveBand([]string{"stable-header", "old-tail", "stable-footer"})
	})
	output := captureUIStdout(t, func() {
		if !surface.SetActiveBand([]string{"stable-header", "new-tail", "stable-footer"}) {
			t.Fatal("expected differential active band update to succeed")
		}
	})
	if !strings.Contains(output, "new-tail") {
		t.Fatalf("changed active row was not repainted: %q", output)
	}
	if strings.Contains(output, "stable-header") || strings.Contains(output, "stable-footer") || strings.Contains(output, "old-tail") {
		t.Fatalf("unchanged or stale active rows were unnecessarily repainted: %q", output)
	}
	if got := strings.Count(output, "\x1b[K"); got != 1 {
		t.Fatalf("differential update cleared %d rows, want exactly one: %q", got, output)
	}
}

func TestFixedBottomSurface_ActiveBandWorksWithoutPrompt(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		_ = surface.SetActiveBand([]string{"streaming…", "partial body"})
	})
	// status + 2 active rows
	if surface.bottomRowsLocked() != 3 {
		t.Fatalf("bottomRows=%d want 3", surface.bottomRowsLocked())
	}
	if got := surface.outputBottomRowLocked(); got != 21 {
		t.Fatalf("output bottom row=%d want 21", got)
	}
}

func TestFixedBottomSurface_ShowPopupDoesNotUseCursorSaveRestore(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.ShowPopup([]string{
			"命令补全",
			"> /model",
			"提示: Tab/Enter 接受",
		})
		surface.ClearPopup()
	})

	if strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected popup render not to save cursor state, got %q", output)
	}
	if strings.Contains(output, cursorRestoreSequence) {
		t.Fatalf("expected popup render not to restore cursor state, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPopupPreserveCursorRestoresPromptCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursor([]string{
			"命令补全: /co",
			"> /collab",
			"提示: Tab/Enter 接受",
		})
	})

	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected preserve popup render to save cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected preserve popup render to restore cursor at the end, got %q", output)
	}
	if surface.popupRenderedRows != 3 {
		t.Fatalf("expected popup rows to render, got %d", surface.popupRenderedRows)
	}
	if surface.popupRenderedGapRows != 1 {
		t.Fatalf("expected input gap row to remain reserved, got %d", surface.popupRenderedGapRows)
	}
}

func TestFixedBottomSurface_ClearPopupPreserveCursorRestoresPromptCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopup([]string{
			"命令补全: /co",
			"> /collab",
			"提示: Tab/Enter 接受",
		})
	})

	output := captureUIStdout(t, func() {
		surface.ClearPopupPreserveCursor()
	})

	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected preserve popup clear to save cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected preserve popup clear to restore cursor at the end, got %q", output)
	}
	if surface.popupRenderedRows != 0 {
		t.Fatalf("expected popup rows to clear, got %d", surface.popupRenderedRows)
	}
	if surface.popupLines != nil {
		t.Fatalf("expected popup lines to clear, got %#v", surface.popupLines)
	}
}

func TestFixedBottomSurface_ClearPopupInputPreserveCursorRestoresPromptCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupInput([]string{
			"Select model",
			"  [1] gpt-4.1",
		}, "choice: ")
	})

	output := captureUIStdout(t, func() {
		surface.ClearPopupPreserveCursor()
	})

	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected preserve popup input clear to save cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected preserve popup input clear to restore cursor at the end, got %q", output)
	}
	if surface.popupRenderedRows != 0 {
		t.Fatalf("expected popup rows to clear, got %d", surface.popupRenderedRows)
	}
	if surface.composerLine != "" {
		t.Fatalf("expected popup input composer line to clear, got %q", surface.composerLine)
	}
}

func TestFixedBottomSurface_ClearPopupForOwnerPreserveCursorKeepsOtherPopup(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopup([]string{
			"Agent Control Panel:",
			"  selected=<none>",
		})
	})
	renderedRows := surface.popupRenderedRows

	output := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("slash_completion")
	})

	if output != "" {
		t.Fatalf("expected owner-mismatched clear to be a no-op, got %q", output)
	}
	if surface.popupLines == nil || surface.popupRenderedRows != renderedRows {
		t.Fatalf("expected non-owned popup to remain rendered, rows=%d lines=%#v", surface.popupRenderedRows, surface.popupLines)
	}

	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{
			"命令补全: /ag",
			"> /agents",
		}, "slash_completion")
	})
	output = captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("slash_completion")
	})

	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected matching owner clear to preserve cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected matching owner clear to restore cursor at the end, got %q", output)
	}
	if surface.popupLines != nil || surface.popupRenderedRows != 0 {
		t.Fatalf("expected owned popup to clear, rows=%d lines=%#v", surface.popupRenderedRows, surface.popupLines)
	}
}

func TestFixedBottomSurface_OwnerPopupRestoresPreviousPanel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{
			"Agent Control Panel:",
			"  selected=/root/worker",
		}, "agent_panel")
		surface.ShowPopupPreserveCursorForOwner([]string{
			"命令补全: /ag",
			"> /agents",
		}, "slash_completion")
	})

	if surface.popupOwner != "slash_completion" {
		t.Fatalf("expected slash popup to be active, got owner=%q", surface.popupOwner)
	}

	output := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("slash_completion")
	})

	if surface.popupOwner != "agent_panel" {
		t.Fatalf("expected agent panel to be restored, got owner=%q lines=%#v", surface.popupOwner, surface.popupLines)
	}
	if !strings.Contains(strings.Join(surface.popupLines, "\n"), "Agent Control Panel:") {
		t.Fatalf("expected restored panel lines, got %#v", surface.popupLines)
	}
	if !strings.Contains(output, "Agent Control Panel:") {
		t.Fatalf("expected restored panel to render, got %q", output)
	}
}

func TestFixedBottomSurface_OwnedPopupInputRestoresBackgroundPanel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{
			"Agent Control Panel:",
			"  selected=/root/worker",
		}, "agent_panel")
		surface.ShowPopupInputForOwner([]string{
			"选择模型",
			"  [1] gpt-5",
		}, "请输入选项: ", "modal:selection")
	})

	if surface.popupOwner != "modal:selection" || surface.composerLine != "请输入选项: " {
		t.Fatalf("expected owned modal input to be active, owner=%q composer=%q", surface.popupOwner, surface.composerLine)
	}

	output := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("modal:selection")
	})
	if surface.popupOwner != "agent_panel" || surface.composerLine != "" {
		t.Fatalf("expected background panel restore, owner=%q composer=%q", surface.popupOwner, surface.composerLine)
	}
	if !strings.Contains(output, "Agent Control Panel:") {
		t.Fatalf("expected restored background panel to render, got %q", output)
	}
}

func TestFixedBottomSurface_DelayedOwnedModalCleanupKeepsNewPopup(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{"Agent Control Panel:"}, "agent_panel")
		surface.ShowPopupInputForOwner([]string{"选择模型"}, "model> ", "modal:selection")
		surface.ShowPopupInputForOwner([]string{"需要审批"}, "approval> ", "modal:priority:approval")
	})

	output := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("modal:selection")
	})
	if output != "" {
		t.Fatalf("expected delayed cleanup of suspended modal to avoid redraw, got %q", output)
	}
	if surface.popupOwner != "modal:priority:approval" || surface.composerLine != "approval> " {
		t.Fatalf("expected newer priority popup to remain active, owner=%q composer=%q", surface.popupOwner, surface.composerLine)
	}

	captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("modal:priority:approval")
	})
	if surface.popupOwner != "agent_panel" {
		t.Fatalf("expected cleanup to restore background directly, owner=%q stack=%#v", surface.popupOwner, surface.popupStack)
	}
}

func TestFixedBottomSurface_PopupHandleCleanupKeepsNewSameOwnerInstance(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	var first, second PopupHandle
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{"Agent Control Panel:"}, "agent_panel")
		first = surface.BeginPopupInputForOwner([]string{"选择模型 A"}, "model-a> ", "modal:selection")
		second = surface.BeginPopupInputForOwner([]string{"选择模型 B"}, "model-b> ", "modal:selection")
	})

	if !first.Valid() || !second.Valid() || first.instance == second.instance {
		t.Fatalf("expected distinct valid popup handles, first=%#v second=%#v", first, second)
	}
	captureUIStdout(t, func() {
		surface.ClearPopupHandlePreserveCursor(first)
	})
	if surface.popupInstance != second.instance || surface.composerLine != "model-b> " {
		t.Fatalf("expected second instance to remain active, instance=%d composer=%q", surface.popupInstance, surface.composerLine)
	}

	captureUIStdout(t, func() {
		surface.ClearPopupHandlePreserveCursor(second)
	})
	if surface.popupOwner != "agent_panel" {
		t.Fatalf("expected background panel to be restored, owner=%q stack=%#v", surface.popupOwner, surface.popupStack)
	}
}

func TestFixedBottomSurface_PopupHandleNewestCleanupRestoresPreviousInstance(t *testing.T) {
	surface := newTestFixedBottomSurface()
	var first, second PopupHandle
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{"background"}, "agent_panel")
		first = surface.BeginPopupInputForOwner([]string{"first"}, "first> ", "modal:selection")
		second = surface.BeginPopupInputForOwner([]string{"second"}, "second> ", "modal:selection")
		surface.ClearPopupHandlePreserveCursor(second)
	})
	if surface.popupInstance != first.instance || surface.composerLine != "first> " {
		t.Fatalf("expected first instance restore, instance=%d composer=%q", surface.popupInstance, surface.composerLine)
	}
	captureUIStdout(t, func() {
		surface.ClearPopupHandlePreserveCursor(second)
		surface.ClearPopupHandlePreserveCursor(first)
		surface.ClearPopupHandlePreserveCursor(first)
	})
	if surface.popupOwner != "agent_panel" {
		t.Fatalf("expected idempotent cleanup to preserve background, owner=%q", surface.popupOwner)
	}
}

func TestFixedBottomSurface_LegacySameOwnerUpdateInvalidatesOldHandle(t *testing.T) {
	surface := newTestFixedBottomSurface()
	var handle PopupHandle
	captureUIStdout(t, func() {
		handle = surface.BeginPopupInputForOwner([]string{"handle"}, "handle> ", "modal:selection")
		surface.ShowPopupInputForOwner([]string{"legacy-new"}, "legacy> ", "modal:selection")
		surface.ClearPopupHandlePreserveCursor(handle)
	})
	if surface.popupInstance != 0 || surface.composerLine != "legacy> " || strings.Join(surface.popupLines, "") != "legacy-new" {
		t.Fatalf("expected stale handle cleanup to keep legacy content, instance=%d composer=%q lines=%#v", surface.popupInstance, surface.composerLine, surface.popupLines)
	}
}

func TestFixedBottomSurface_PopupHandleViewportSurvivesUpdateAndStackRestore(t *testing.T) {
	surface := newTestFixedBottomSurface()
	viewport := PopupViewportSpec{
		HeaderLines: []string{"header"},
		BodyLines:   []string{"reason", "risk=high", "command"},
		FooterLines: []string{"warning"},
		Anchor:      1,
	}
	var first, second PopupHandle
	captureUIStdout(t, func() {
		first = surface.BeginPopupInputForOwnerWithViewport(
			[]string{"header", "reason", "risk=high", "command", "warning"},
			"approve> ",
			"modal:priority:approval",
			viewport,
		)
		second = surface.BeginPopupInputForOwner([]string{"second"}, "second> ", "modal:priority:approval")
		if surface.UpdatePopupInputForHandle(first, []string{"header", "reason updated", "risk=high", "command", "warning"}, "approve> ", true) {
			t.Fatal("expected hidden handle update not to become active")
		}
		surface.ClearPopupHandlePreserveCursor(second)
	})
	if surface.popupInstance != first.instance || surface.popupViewport == nil {
		t.Fatalf("expected viewport instance restore, instance=%d viewport=%#v", surface.popupInstance, surface.popupViewport)
	}
	visible := surface.bottomPaneStateLocked().VisiblePopupLines(6)
	if got := strings.Join(visible, "\n"); got != "header\nrisk=high\nwarning" {
		t.Fatalf("expected restored semantic viewport, got %q", got)
	}
	viewport.HeaderLines[0] = "mutated caller"
	if surface.popupViewport.HeaderLines[0] != "header" {
		t.Fatalf("expected viewport input to be copied, got %#v", surface.popupViewport)
	}
}

func TestFixedBottomSurface_LowerPriorityOwnerUpdateDoesNotStealActivePopup(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{
			"命令补全: /ag",
			"> /agents",
		}, "slash_completion")
	})

	output := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{
			"Agent Control Panel:",
			"  selected=/root/updated",
		}, "agent_panel")
	})

	if output != "" {
		t.Fatalf("expected lower priority panel update not to render over slash popup, got %q", output)
	}
	if surface.popupOwner != "slash_completion" {
		t.Fatalf("expected slash popup to remain active, got owner=%q", surface.popupOwner)
	}

	captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("slash_completion")
	})
	if surface.popupOwner != "agent_panel" || !strings.Contains(strings.Join(surface.popupLines, "\n"), "/root/updated") {
		t.Fatalf("expected updated panel to restore, owner=%q lines=%#v", surface.popupOwner, surface.popupLines)
	}
}

func TestFixedBottomSurface_ClearPopupKeepsStatusModel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	surface.statusModel = &style.StatusLineModel{
		State:     style.RunReady,
		Separator: " | ",
		Segments:  []style.StatusSegment{{Text: "model gpt-4.1"}},
	}

	output := captureUIStdout(t, func() {
		surface.ShowPopup([]string{
			"选择模型",
			"  当前模型: gpt-4.1",
			"  [1] gpt-4.1",
			"  [2] gpt-4.1-mini",
			"  提示: 输入编号、模型名，回车保持当前",
		})
		surface.ClearPopup()
	})

	if got := style.StatusLineDocument(*surface.statusModel, 0).PlainText(); got != "Ready | model gpt-4.1" {
		t.Fatalf("expected status model to remain unchanged, got %q", got)
	}
	if surface.popupRenderedRows != 0 {
		t.Fatalf("expected popup rows to be cleared, got %d", surface.popupRenderedRows)
	}
	if surface.popupLines != nil {
		t.Fatalf("expected popup lines to be cleared, got %#v", surface.popupLines)
	}
	if surface.bottomRowsLocked() != 1 {
		t.Fatalf("expected bottom rows to collapse back to status-only mode, got %d", surface.bottomRowsLocked())
	}
	if !strings.Contains(output, "Ready | model gpt-4.1") {
		t.Fatalf("expected status line to be re-rendered, got %q", output)
	}
}

func TestFixedBottomSurface_SetStatusModelPreservesCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.SetStatusModel(style.StatusLineModel{
			State:     style.RunReady,
			Separator: " | ",
			Segments:  []style.StatusSegment{{Text: "Agent Panel"}},
		})
	})

	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected status render to save cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected status render to restore cursor at the end, got %q", output)
	}
}

func TestFixedBottomSurface_SetStatusModelPreservesCursorAndSanitizesText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	output := captureUIStdout(t, func() {
		surface.SetStatusModel(style.StatusLineModel{
			State:     style.RunThinking,
			StateText: "思考\x1b[2J",
			Segments: []style.StatusSegment{
				{Text: "gpt-5.6-sol\nspoof", Role: style.RoleAccent},
			},
		})
	})

	if surface.statusModel == nil {
		t.Fatal("expected typed status model to be stored")
	}
	plain := style.StatusLineDocument(*surface.statusModel, 0).PlainText()
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\n") {
		t.Fatalf("expected status model text to be single-line and sanitized, got %q", plain)
	}
	if !strings.Contains(output, "思考 · gpt-5.6-sol spoof") {
		t.Fatalf("expected sanitized typed status content, got %q", output)
	}
	if !strings.Contains(output, cursorSaveSequence) || !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected typed status update to preserve cursor, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPopupInputFocusesPromptRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.ShowPopupInput([]string{
			"Select model",
			"  [1] gpt-4.1",
		}, "choice: ")
	})

	if surface.popupRenderedRows != 3 {
		t.Fatalf("expected popup and input prompt to render 3 rows, got %d", surface.popupRenderedRows)
	}
	if surface.composerLine != "choice: " {
		t.Fatalf("expected composer line to be stored separately, got %q", surface.composerLine)
	}
	if !strings.Contains(output, "  [1] gpt-4.1") {
		t.Fatalf("expected popup rendering to preserve leading spaces, got %q", output)
	}
	if !strings.Contains(output, "choice:") {
		t.Fatalf("expected popup input prompt to render, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[23;8H") {
		t.Fatalf("expected final cursor position after popup prompt, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPopupInputPreserveCursorKeepsPromptRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupInput([]string{
			"Agent Panel:",
			"  [1] /root/one",
		}, "Agent Panel> ")
	})

	output := captureUIStdout(t, func() {
		surface.ShowPopupInputPreserveCursor([]string{
			"Agent Panel:",
			"  [1] /root/one",
			"  [2] /root/two",
		}, "Agent Panel> ")
	})

	if surface.composerLine != "Agent Panel> " {
		t.Fatalf("expected preserve render to keep composer prompt, got %q", surface.composerLine)
	}
	if surface.popupRenderedRows != 4 {
		t.Fatalf("expected popup plus prompt row to render, got %d", surface.popupRenderedRows)
	}
	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected preserve input render to save cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected preserve input render to restore cursor at the end, got %q", output)
	}
	if !strings.Contains(output, "Agent Panel>") {
		t.Fatalf("expected prompt row to remain rendered, got %q", output)
	}
}

func TestFixedBottomSurface_SetComposerPreviewRendersStandaloneComposerRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	surface.statusModel = &style.StatusLineModel{
		State:     style.RunReady,
		Separator: " | ",
		Segments:  []style.StatusSegment{{Text: "composer"}},
	}

	output := captureUIStdout(t, func() {
		surface.SetComposerPreview("draft: /model")
	})

	if surface.composerLine != "draft: /model" {
		t.Fatalf("expected composer line to be stored, got %q", surface.composerLine)
	}
	if surface.popupRenderedRows != 1 {
		t.Fatalf("expected standalone composer row to render one row, got %d", surface.popupRenderedRows)
	}
	if surface.bottomRowsLocked() != 2 {
		t.Fatalf("expected bottom rows to reserve composer plus status, got %d", surface.bottomRowsLocked())
	}
	if !strings.Contains(output, "draft: /model") {
		t.Fatalf("expected composer preview to render, got %q", output)
	}

	captureUIStdout(t, func() {
		surface.ClearComposerPreview()
	})
	if surface.composerLine != "" {
		t.Fatalf("expected composer line to clear, got %q", surface.composerLine)
	}
	if surface.popupRenderedRows != 0 {
		t.Fatalf("expected composer row to clear, got %d", surface.popupRenderedRows)
	}
}

func TestFixedBottomSurface_SetComposerPreviewSuppressesPromptState(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if !surface.SetPromptInputState("> ", "/help", 1, 0, 7) {
			t.Fatal("expected enabled surface to track prompt input")
		}
	})

	output := captureUIStdout(t, func() {
		surface.SetComposerPreview("Provider 名称: ")
	})

	if surface.composerLine != "Provider 名称: " {
		t.Fatalf("expected composer preview to be active, got %q", surface.composerLine)
	}
	if surface.promptLine != "" || surface.promptInput != "" || surface.promptReservedRows != 0 {
		t.Fatalf("expected prompt state to be suppressed while composer is active, line=%q input=%q rows=%d", surface.promptLine, surface.promptInput, surface.promptReservedRows)
	}
	if surface.promptCursorRow != 0 || surface.promptCursorCol != 0 {
		t.Fatalf("expected prompt cursor to reset while composer is active, row=%d col=%d", surface.promptCursorRow, surface.promptCursorCol)
	}
	if strings.Contains(output, "\x1b[23;1H> /help") {
		t.Fatalf("expected previous prompt input not to render under composer preview, got %q", output)
	}
	if !strings.Contains(output, "Provider 名称:") {
		t.Fatalf("expected composer preview to render, got %q", output)
	}
}

func TestFixedBottomSurface_ClearComposerPreviewLeavesPopupStateIntact(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwner([]string{
			"选择 Provider",
			"  [1] openai",
		}, "provider_select")
		surface.SetComposerPreview("providers> ")
	})

	output := captureUIStdout(t, func() {
		surface.ClearComposerPreview()
	})

	if surface.composerLine != "" {
		t.Fatalf("expected composer preview to clear, got %q", surface.composerLine)
	}
	if surface.popupOwner != "provider_select" || !strings.Contains(strings.Join(surface.popupLines, "\n"), "选择 Provider") {
		t.Fatalf("expected popup state to remain after composer clear, owner=%q lines=%#v", surface.popupOwner, surface.popupLines)
	}
	if surface.promptCursorRow != 0 || surface.promptCursorCol != 0 {
		t.Fatalf("expected prompt cursor to remain reset after composer clear, row=%d col=%d", surface.promptCursorRow, surface.promptCursorCol)
	}
	if !strings.Contains(output, "选择 Provider") {
		t.Fatalf("expected popup to re-render after composer clear, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPendingPastePreviewRendersPreview(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.ShowPendingPastePreview(3, "line-1\nline-2\nline-3")
	})

	if !strings.Contains(output, "粘贴草稿预览") {
		t.Fatalf("expected pending paste preview title, got %q", output)
	}
	if !strings.Contains(output, "行数: 3") {
		t.Fatalf("expected pending paste preview line count, got %q", output)
	}
	if !strings.Contains(output, "line-2") {
		t.Fatalf("expected pending paste preview content, got %q", output)
	}
	if surface.popupRenderedRows == 0 {
		t.Fatal("expected pending paste preview to render popup rows")
	}
	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected pending paste preview to preserve cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected pending paste preview to restore cursor at the end, got %q", output)
	}
}

func TestFixedBottomSurface_ClearPromptRowsUsesAbsoluteRows(t *testing.T) {
	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		if !surface.ClearPromptRows(3) {
			t.Fatal("expected enabled surface to clear prompt rows")
		}
	})

	for _, expected := range []string{
		"\x1b[21;1H\x1b[K",
		"\x1b[22;1H\x1b[K",
		"\x1b[23;1H\x1b[K",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected absolute prompt-row clear %q, got %q", expected, output)
		}
	}
	if strings.Contains(output, "\x1b[2A") || strings.Contains(output, "\x1b[1B") {
		t.Fatalf("expected prompt clear not to use relative vertical movement, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[23;1H") {
		t.Fatalf("expected cursor to end at output bottom row, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPromptReservesRowAboveStatus(t *testing.T) {
	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		surface.BeginOutput()
	})

	if !strings.Contains(output, "\x1b[1;20r") {
		t.Fatalf("expected prompt reserve to move scroll bottom above prompt row, got %q", output)
	}
	if !strings.Contains(output, "\x1b[22;1H> ") {
		t.Fatalf("expected prompt to render on row above status, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[20;1H") {
		t.Fatalf("expected BeginOutput to target row above prompt, got %q", output)
	}
}

func TestFixedBottomSurface_ClearPromptRowsReleasesPromptReserve(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected enabled surface to clear prompt")
		}
	})

	if surface.promptReservedRows != 0 {
		t.Fatal("expected prompt reserve to be released")
	}
	if !strings.Contains(output, "\x1b[22;1H\x1b[K") {
		t.Fatalf("expected prompt row to be cleared, got %q", output)
	}
	if !strings.Contains(output, "\x1b[1;23r") {
		t.Fatalf("expected scroll region to return to status-only layout, got %q", output)
	}
}

func TestFixedBottomSurface_ResetPromptClearsInputAndKeepsPromptVisible(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if !surface.SetPromptRows(3) {
			t.Fatal("expected enabled surface to reserve wrapped prompt rows")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.ResetPrompt("> ", 3) {
			t.Fatal("expected enabled surface to reset prompt")
		}
	})

	if surface.promptReservedRows != 1 {
		t.Fatalf("expected prompt reserve to collapse back to one row, got %d", surface.promptReservedRows)
	}
	for _, expected := range []string{
		"\x1b[19;1H\x1b[K",
		"\x1b[20;1H\x1b[K",
		"\x1b[21;1H\x1b[K",
		"\x1b[22;1H\x1b[K",
		"\x1b[23;1H\x1b[K",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected reset to clear old prompt row %q, got %q", expected, output)
		}
	}
	if !strings.Contains(output, "\x1b[22;1H> ") {
		t.Fatalf("expected reset to redraw visible prompt, got %q", output)
	}
	if !strings.Contains(output, "\x1b[1;20r") {
		t.Fatalf("expected reset to keep one prompt row reserved, got %q", output)
	}
}

func TestFixedBottomSurface_SetPromptRowsReservesWrappedInput(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.SetPromptRows(3) {
			t.Fatal("expected enabled surface to update prompt rows")
		}
		surface.BeginOutput()
	})

	if !strings.Contains(output, "\x1b[1;18r") {
		t.Fatalf("expected three prompt rows to move scroll bottom above input block, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[18;1H") {
		t.Fatalf("expected output cursor above reserved prompt rows, got %q", output)
	}
}

func TestFixedBottomSurface_WriteOutputUsesOutputRegionWithPromptReserved(t *testing.T) {
	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "reasoning\n"); !ok || err != nil {
			t.Fatalf("expected surface output write to be handled, ok=%t err=%v", ok, err)
		}
	})

	if !strings.Contains(output, "\x1b[20;1Hreasoning\r\n") {
		t.Fatalf("expected output to be written above prompt row, got %q", output)
	}
	if strings.Contains(output, "\x1b[22;1Hreasoning") {
		t.Fatalf("expected output not to be written on prompt row, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[22;3H") {
		t.Fatalf("expected cursor to return after visible prompt, got %q", output)
	}
}

func TestFixedBottomSurface_WriteOutputNormalizesNewlinesForRawUnixTerminals(t *testing.T) {
	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "first\nsecond\r\nthird\rfourth"); !ok || err != nil {
			t.Fatalf("expected surface output write to be handled, ok=%t err=%v", ok, err)
		}
	})

	if !strings.Contains(output, "first\r\nsecond\r\nthird\r\nfourth") {
		t.Fatalf("expected surface output newlines to be normalized to CRLF, got %q", output)
	}
	if strings.Contains(output, "first\nsecond") || strings.Contains(output, "second\r\nthird\rfourth") {
		t.Fatalf("expected no bare LF/CR in rendered surface output, got %q", output)
	}
}

func TestFixedBottomSurface_WriteOutputRestoresTrackedPromptCursor(t *testing.T) {
	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if !surface.SetPromptCursor(0, 7) {
			t.Fatal("expected prompt cursor to be tracked")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "tool output\n"); !ok || err != nil {
			t.Fatalf("expected surface output write to be handled, ok=%t err=%v", ok, err)
		}
	})

	if !strings.HasSuffix(output, "\x1b[22;8H") {
		t.Fatalf("expected cursor to return to tracked prompt cursor, got %q", output)
	}
}

func TestFixedBottomSurface_ClampsOversizedBottomReserveToKeepOutputRow(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.SetPromptRows(80) {
			t.Fatal("expected enabled surface to update prompt rows")
		}
		surface.BeginOutput()
	})

	if !strings.Contains(output, "\x1b[1;1r") {
		t.Fatalf("expected scroll region to clamp to first row when bottom reserve is oversized, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[1;1H") {
		t.Fatalf("expected output cursor to stay on the preserved output row, got %q", output)
	}
}

func TestFixedBottomSurface_ClearPromptRowsClearsOnlyPopupInputGap(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.ShowPopup([]string{
			"命令补全: /",
			"> /help",
		})
	})

	output := captureUIStdout(t, func() {
		if !surface.ClearPromptRows(3) {
			t.Fatal("expected enabled surface to clear prompt rows")
		}
	})

	if !strings.Contains(output, "\x1b[23;1H\x1b[K") {
		t.Fatalf("expected prompt gap row to be cleared, got %q", output)
	}
	if strings.Contains(output, "\x1b[21;1H\x1b[K") || strings.Contains(output, "\x1b[22;1H\x1b[K") {
		t.Fatalf("expected popup rows to remain owned by popup renderer, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[20;1H") {
		t.Fatalf("expected cursor to return to popup-adjusted output bottom row, got %q", output)
	}
}

func TestBottomPaneSelectionViewportKeepsSlashSelectionVisible(t *testing.T) {
	state := BottomPaneState{
		PopupOwner: "slash_completion",
		PopupLines: []string{
			"  /agents  查看 agents",
			"  /clear   清空会话",
			"> /model   切换模型",
			"  /resume  恢复会话",
			"方向键选择，Enter 确认",
		},
	}

	visible := state.VisiblePopupLines(4)
	if len(visible) != 1 || !strings.Contains(visible[0], "> /model") {
		t.Fatalf("expected the selected slash command to remain visible, got %#v", visible)
	}
}

func TestBottomPaneSelectionViewportKeepsCurrentOptionVisible(t *testing.T) {
	state := BottomPaneState{
		PopupOwner: "modal:selection",
		PopupLines: []string{
			"选择模型",
			"当前模型: gpt-5",
			"  [1] gpt-4.1",
			"  [2] gpt-4.1-mini",
			"  [3] gpt-5  (当前)",
			"  [4] o3",
			"提示: 输入编号，回车保持当前",
		},
	}

	visible := state.VisiblePopupLines(6)
	rendered := strings.Join(visible, "\n")
	for _, want := range []string{"选择模型", "[3] gpt-5", "提示: 输入编号"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected viewport to contain %q, got %#v", want, visible)
		}
	}
}

func TestBottomPaneSelectionViewportKeepsValidationVisible(t *testing.T) {
	state := BottomPaneState{
		PopupOwner: "modal:selection",
		PopupLines: []string{
			"选择模型",
			"当前模型: gpt-5",
			"无效的选择，请重新输入",
			"  [1] gpt-4.1",
			"  [2] gpt-5  (当前)",
			"  [3] o3",
			"提示: 输入编号",
		},
	}

	visible := state.VisiblePopupLines(6)
	rendered := strings.Join(visible, "\n")
	for _, want := range []string{"选择模型", "无效的选择", "[2] gpt-5"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected validation viewport to contain %q, got %#v", want, visible)
		}
	}
}

func TestBottomPaneSemanticViewportKeepsPriorityPromptContextInSixRows(t *testing.T) {
	state := BottomPaneState{
		PopupLines: []string{
			"审批 | tool=shell",
			"reason=needs access",
			"risk=high",
			"command=git status",
			"invalid choice",
		},
		PopupViewport: &PopupViewportSpec{
			HeaderLines: []string{"审批 | tool=shell"},
			BodyLines:   []string{"reason=needs access", "risk=high", "command=git status"},
			FooterLines: []string{"invalid choice"},
			Anchor:      1,
		},
		ComposerLine: "请选择 [1] 允许 [2] 拒绝: ",
	}

	visible := state.VisiblePopupLines(6)
	if got, want := strings.Join(visible, "\n"), "审批 | tool=shell\nrisk=high\ninvalid choice"; got != want {
		t.Fatalf("expected semantic priority viewport %q, got %q", want, got)
	}
	if state.ComposerLine != "请选择 [1] 允许 [2] 拒绝: " {
		t.Fatalf("expected operation footer to remain in composer, got %q", state.ComposerLine)
	}
}

func newTestFixedBottomSurface() *FixedBottomSurface {
	return newTestFixedBottomSurfaceWithSize(80, 24)
}

// newTestFixedBottomSurfaceWithSize pins a synthetic geometry so layout tests
// can cover terminal heights the test host cannot report.
func newTestFixedBottomSurfaceWithSize(width, height int) *FixedBottomSurface {
	term := &Terminal{
		theme:  GetTheme(ThemeAuto),
		driver: &TerminalDriver{},
	}
	term.SetSizeForTest(width, height)
	surface := NewFixedBottomSurface(term)
	surface.enabled = true
	return surface
}

func TestActiveBandRowsAdaptsToTerminalHeight(t *testing.T) {
	cases := []struct {
		height int
		want   int
	}{
		{height: 0, want: ActiveBandMinRows},
		{height: 10, want: ActiveBandMinRows},
		{height: 20, want: ActiveBandMinRows},
		{height: 24, want: 8},
		{height: 30, want: 10},
		{height: 40, want: 13},
		{height: 60, want: ActiveBandMaxRows},
		{height: 200, want: ActiveBandMaxRows},
	}
	for _, tc := range cases {
		if got := ActiveBandRows(tc.height); got != tc.want {
			t.Fatalf("ActiveBandRows(%d)=%d want %d", tc.height, got, tc.want)
		}
	}
}

func TestActiveBandRowsAlwaysLeavesRoomForOutputAndPrompt(t *testing.T) {
	for height := 1; height <= 120; height++ {
		rows := ActiveBandRows(height)
		if rows < ActiveBandMinRows || rows > ActiveBandMaxRows {
			t.Fatalf("height=%d rows=%d out of [%d,%d]", height, rows, ActiveBandMinRows, ActiveBandMaxRows)
		}
		if height > ActiveBandMinRows+activeBandReservedRows && height-rows < activeBandReservedRows {
			t.Fatalf("height=%d rows=%d leaves only %d rows for output/prompt", height, rows, height-rows)
		}
	}
}

func TestActiveBandRowBudgetFollowsSurfaceTerminal(t *testing.T) {
	surface := newTestFixedBottomSurface()
	if got, want := surface.ActiveBandRowBudget(), ActiveBandRows(24); got != want {
		t.Fatalf("budget=%d want %d", got, want)
	}
	width, rows := surface.ActiveBandViewportSize()
	if width != 80 || rows != ActiveBandRows(24) {
		t.Fatalf("viewport size = %d x %d", width, rows)
	}
	surface.terminal.height = 48
	if got, want := surface.ActiveBandRowBudget(), ActiveBandRows(48); got != want {
		t.Fatalf("resized budget=%d want %d", got, want)
	}
	var nilSurface *FixedBottomSurface
	if got := nilSurface.ActiveBandRowBudget(); got != ActiveBandMinRows {
		t.Fatalf("nil surface budget=%d", got)
	}
}

func TestBottomPaneStateClampsBandToStateBudget(t *testing.T) {
	lines := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("row-%d", i))
	}
	state := BottomPaneState{ActiveBandLines: lines, ActiveBandMaxRows: 7}
	if got := state.activeBandVisibleRowCount(); got != 7 {
		t.Fatalf("visible rows=%d want 7", got)
	}
	state.ActiveBandMaxRows = 0
	if got := state.activeBandVisibleRowCount(); got != 10 {
		t.Fatalf("fallback visible rows=%d want 10", got)
	}
	state.ComposerLine = "draft"
	if got := state.activeBandVisibleRowCount(); got != 0 {
		t.Fatalf("composer should hide band, got %d", got)
	}
}

func TestSetActiveBandUsesTallerBudgetOnTallTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurface()
	surface.terminal.height = 48
	lines := make([]string, 0, ActiveBandMaxRows+4)
	for i := 0; i < ActiveBandMaxRows+4; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	captureUIStdout(t, func() {
		_ = surface.SetActiveBand(lines)
	})
	got := surface.ActiveBandLines()
	if len(got) != ActiveBandMaxRows {
		t.Fatalf("expected %d rows on a 48-row terminal, got %d %v", ActiveBandMaxRows, len(got), got)
	}
	if got[len(got)-1] != fmt.Sprintf("line-%d", ActiveBandMaxRows+3) {
		t.Fatalf("expected newest tail, got %v", got)
	}
}

func TestFixedBottomSurface_ReleasedActiveBandScrollsOutputBackDown(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed line\n"); !ok || err != nil {
			t.Fatalf("expected output write to be handled, ok=%t err=%v", ok, err)
		}
		if !surface.SetActiveBand([]string{"• assistant", "streaming", "tail"}) {
			t.Fatal("expected active band to render")
		}
	})

	output := captureUIStdout(t, func() {
		if !surface.ClearActiveBand() {
			t.Fatal("expected active band to clear")
		}
	})

	if !strings.Contains(output, "\x1b[1;20r") {
		t.Fatalf("expected output region to grow back to row 20, got %q", output)
	}
	if want := terminalScrollDownSequence(3); !strings.Contains(output, want) {
		t.Fatalf("expected freed band rows to scroll output down, got %q", output)
	}
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("expected pending compensation to be flushed, got %d", surface.pendingScrollDownRows)
	}
}

func TestFixedBottomSurface_ActiveBandGrowthAndReleaseScrollSymmetrically(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
	})

	grow := captureUIStdout(t, func() {
		if !surface.SetActiveBand([]string{"• assistant", "streaming"}) {
			t.Fatal("expected active band to render")
		}
	})
	if !strings.Contains(grow, "\x1b[20;1H\n\n") {
		t.Fatalf("expected reserved band rows to scroll output up, got %q", grow)
	}

	release := captureUIStdout(t, func() {
		if !surface.ClearActiveBand() {
			t.Fatal("expected active band to clear")
		}
	})
	if want := terminalScrollDownSequence(2); !strings.Contains(release, want) {
		t.Fatalf("expected released band rows to scroll output back down, got %q", release)
	}
}

// TestFixedBottomSurface_TrailingOutputNewlineAbsorbedByBandGrowth pins the
// mid-stream hole fix: WriteOutput("...\n") parks the cursor on a blank output
// row. Growing the active band must consume that blank instead of scrolling it
// into a permanent gap above the band (most visible near ActiveBandMaxRows).
func TestFixedBottomSurface_TrailingOutputNewlineAbsorbedByBandGrowth(t *testing.T) {
	height := 48
	surface := newTestFixedBottomSurfaceWithSize(80, height)
	budget := ActiveBandRows(height)
	if budget != ActiveBandMaxRows {
		t.Fatalf("precondition: height %d should budget %d, got %d", height, ActiveBandMaxRows, budget)
	}

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "prior transcript line\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		if !surface.outputCursorOnBlankRow {
			t.Fatal("expected trailing newline to mark output cursor on a blank row")
		}
	})

	lines := make([]string, 0, budget)
	for i := 0; i < budget; i++ {
		lines = append(lines, fmt.Sprintf("band-%02d", i+1))
	}
	grow := captureUIStdout(t, func() {
		if !surface.SetActiveBand(lines) {
			t.Fatal("expected SetActiveBand")
		}
	})
	if surface.outputCursorOnBlankRow {
		t.Fatal("band growth should consume the trailing blank marker")
	}

	// Status-only layout → band(budget)+status grows by `budget` rows, but one
	// is absorbed from the trailing blank. Scroll therefore pushes budget-1
	// newlines at the row above the blank (height-2), not at the blank itself.
	scrollRow := height - 2
	wantScroll := "\x1b[" + fmt.Sprintf("%d", scrollRow) + ";1H" + strings.Repeat("\n", budget-1)
	if !strings.Contains(grow, wantScroll) {
		t.Fatalf("expected growth scroll of %d (budget-1) at row %d, got %q", budget-1, scrollRow, grow)
	}
	// Must not scroll the full budget from the pre-band output bottom.
	fullScroll := "\x1b[" + fmt.Sprintf("%d", height-1) + ";1H" + strings.Repeat("\n", budget)
	if strings.Contains(grow, fullScroll) {
		t.Fatalf("growth still scrolled full budget %d; trailing blank was not absorbed: %q", budget, grow)
	}
}

// ClearPrompt shrinks the bottom reserve and defers scroll-down. Multi-line
// command output (e.g. /status) must WriteOutput so that pending compensation
// flushes before text lands; otherwise restoring the prompt cancels the
// matching growth scroll and paints over the box bottom.
func TestFixedBottomSurface_WriteOutputAfterClearPromptFlushesPendingForPromptRestore(t *testing.T) {
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
	})
	if surface.pendingScrollDownRows != 3 {
		t.Fatalf("expected deferred shrink compensation after clear, got %d", surface.pendingScrollDownRows)
	}

	// BeginOutput alone must NOT flush — that is the bug path used by raw
	// fmt.Println after beginDirectInteractiveOutput.
	captureUIStdout(t, func() {
		surface.BeginOutput()
	})
	if surface.pendingScrollDownRows != 3 {
		t.Fatalf("BeginOutput must leave pending compensation intact, got %d", surface.pendingScrollDownRows)
	}

	written := captureUIStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "╭ status top\n│ Token usage\n│ Limits\n╰ status bottom\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("WriteOutput should flush pending shrink compensation, got %d", surface.pendingScrollDownRows)
	}
	if !strings.Contains(written, terminalScrollDownSequence(3)) {
		t.Fatalf("expected deferred scroll-down flush before status text, got %q", written)
	}
	for _, fragment := range []string{"Token usage", "Limits", "status bottom"} {
		if !strings.Contains(written, fragment) {
			t.Fatalf("expected status fragment %q in surface output, got %q", fragment, written)
		}
	}

	// Prompt restore must reclaim the composer and its margin rows without
	// canceling against stale
	// pendingScrollDown. WriteOutput left outputCursorOnBlankRow set, so the
	// first growth row is absorbed from that blank —
	// that still keeps the status bottom above the prompt. The bug path leaves
	// pending set and cancels growth entirely instead.
	restored := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt restore")
		}
	})
	if !strings.Contains(restored, "\x1b[1;20r") {
		t.Fatalf("expected prompt restore to reserve composer margins and a prompt row (scroll region 1..20), got %q", restored)
	}
	if !strings.Contains(restored, "\x1b[22;1H> ") {
		t.Fatalf("expected prompt to repaint on the reserved row, got %q", restored)
	}
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("prompt restore should not reintroduce pending compensation, got %d", surface.pendingScrollDownRows)
	}
	if surface.lastBottomRows != 4 {
		t.Fatalf("expected margins+prompt+status bottom reserve of 4, got %d", surface.lastBottomRows)
	}
}

// SettleOutputDebt is the history/resume path: flush ClearPrompt shrink debt
// BEFORE any transcript content WriteOutput, so layout compensation is not
// billed to already-final messages. The blank-row flag tracks real geometry
// after the flushes (false here: no prior trailing blank or absorb debt).
func TestFixedBottomSurface_SettleOutputDebtFlushesPendingWithoutContent(t *testing.T) {
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		surface.BeginOutput()
	})
	if surface.pendingScrollDownRows != 3 {
		t.Fatalf("expected deferred shrink compensation after clear, got %d", surface.pendingScrollDownRows)
	}

	settled := captureUIStdout(t, func() {
		surface.SettleOutputDebt()
	})
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("SettleOutputDebt should flush pending, got %d", surface.pendingScrollDownRows)
	}
	if surface.outputCursorOnBlankRow {
		t.Fatal("debt-less settle with no prior blank should leave blank-row flag false")
	}
	if !strings.Contains(settled, terminalScrollDownSequence(3)) {
		t.Fatalf("expected settle to emit deferred scroll-down, got %q", settled)
	}
	// No transcript payload mixed into the settle sequence.
	if strings.Contains(settled, "history") || strings.Contains(settled, "assistant") {
		t.Fatalf("settle must not write transcript content, got %q", settled)
	}

	// Subsequent content write must not re-emit the already-flushed scroll-down.
	content := captureUIStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "history line one\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if strings.Contains(content, terminalScrollDownSequence(3)) {
		t.Fatalf("content write must not re-flush settled debt, got %q", content)
	}
	if !strings.Contains(content, "history line one") {
		t.Fatalf("expected history content, got %q", content)
	}
}

// When settle pays an absorb debt, the region bottom becomes blank again. Soft
// rewrites and later band growth must see that blank — not a forced-false flag
// left over from the pre-debt state.
func TestFixedBottomSurface_SettleOutputDebtRestoresBlankAfterAbsorbDebt(t *testing.T) {
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed tail row\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		if !surface.SetActiveBand([]string{"• assistant", "streaming"}) {
			t.Fatal("expected active band")
		}
	})
	if surface.outputScrollDebtRows != 1 {
		t.Fatalf("expected one absorbed row before settle, got %d", surface.outputScrollDebtRows)
	}
	if surface.outputCursorOnBlankRow {
		t.Fatal("band growth should have consumed the trailing blank marker")
	}

	settled := captureUIStdout(t, func() {
		surface.SettleOutputDebt()
	})
	if surface.outputScrollDebtRows != 0 {
		t.Fatalf("settle should pay absorb debt, got %d", surface.outputScrollDebtRows)
	}
	if !surface.outputCursorOnBlankRow {
		t.Fatal("paying absorb debt must leave the region bottom blank")
	}
	bottom := outputBottomRowForHeight(height, surface.effectiveBottomRowsLocked(height))
	repay := terminalMoveToSequence(bottom, 1) + "\n"
	if !strings.Contains(settled, repay) {
		t.Fatalf("expected settle to scroll the absorbed row at %d, got %q", bottom, settled)
	}
}

// After a content write parks the cursor on a blank row, ClearPrompt defers a
// shrink. Settle flushes that shrink and must leave the blank flag true so soft
// rewrites still compute prevStart correctly. (ClearActiveBand flushes pending
// eagerly, so this uses ClearPrompt — the history/resume path.)
func TestFixedBottomSurface_SettleOutputDebtPreservesTrailingBlank(t *testing.T) {
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed tail row\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		// Clear while the trailing blank is still parked. A ShowPrompt after the
		// write would re-absorb that blank into reserve growth and leave the flag
		// false, which is a different path (absorb debt) already covered above.
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected clear to defer shrink while blank is parked")
		}
	})
	if !surface.outputCursorOnBlankRow {
		t.Fatal("expected blank-row flag true before debt-less settle: ClearPrompt must not clear a parked blank")
	}
	if surface.pendingScrollDownRows < 1 {
		t.Fatalf("expected deferred shrink after ClearPrompt, got %d", surface.pendingScrollDownRows)
	}

	captureUIStdout(t, func() {
		surface.SettleOutputDebt()
	})
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("settle should flush pending shrink, got %d", surface.pendingScrollDownRows)
	}
	if !surface.outputCursorOnBlankRow {
		t.Fatal("debt-less settle must preserve the trailing blank flag")
	}
}

// Absorbing the trailing blank output row is display-only: it parks committed
// content on the row every writer targets. The debt must be scrolled — never
// overwritten — before the next byte reaches the output region.
func TestFixedBottomSurface_AbsorbedBlankRowIsScrolledNotOverwritten(t *testing.T) {
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed tail row\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if !surface.outputCursorOnBlankRow {
		t.Fatal("trailing newline should park the cursor on a blank row")
	}

	captureUIStdout(t, func() {
		if !surface.SetActiveBand([]string{"• assistant", "streaming"}) {
			t.Fatal("expected active band")
		}
	})
	if surface.outputScrollDebtRows != 1 {
		t.Fatalf("band growth should record one absorbed row, got %d", surface.outputScrollDebtRows)
	}

	next := captureUIStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "next committed row\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if surface.outputScrollDebtRows != 0 {
		t.Fatalf("write should settle the absorb debt, got %d", surface.outputScrollDebtRows)
	}
	bottom := outputBottomRowForHeight(height, surface.effectiveBottomRowsLocked(height))
	repay := terminalMoveToSequence(bottom, 1) + "\n"
	repayAt := strings.Index(next, repay)
	if repayAt < 0 {
		t.Fatalf("expected absorb debt scroll at row %d before content, got %q", bottom, next)
	}
	if contentAt := strings.Index(next, "next committed row"); contentAt >= 0 && contentAt < repayAt {
		t.Fatalf("content was written before the debt scroll, got %q", next)
	}
}

// A deferred shrink moves the transcript (and its trailing blank row) down
// inside the output region, so the blank row stays absorbable. Clearing the
// marker on shrink made the following reserve growth scroll that blank up
// again, leaving a second empty row between transcript and prompt.
func TestFixedBottomSurface_ShrinkKeepsTrailingBlankAbsorbable(t *testing.T) {
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed tail row\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		// Submitting input clears the prompt rows: a shrink with a deferred
		// scroll-down, while the transcript trailing blank stays in the region.
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		if !surface.SetActiveBand([]string{"• assistant", "streaming"}) {
			t.Fatal("expected active band")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "streamed commit row\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		// Turn end: the band is released and its deferred scroll-down is emitted
		// inside ClearActiveBand, so nothing is pending afterwards.
		if !surface.ClearActiveBand() {
			t.Fatal("expected band clear")
		}
	})
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("band release should flush its own compensation, got %d", surface.pendingScrollDownRows)
	}
	if !surface.outputCursorOnBlankRow {
		t.Fatal("shrink must keep the transcript trailing blank row absorbable")
	}

	// Restoring the prompt grows the reserve again. It must absorb that blank
	// row instead of scrolling it up into a second empty row above the prompt.
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt restore")
		}
	})
	if surface.outputScrollDebtRows != 1 {
		t.Fatalf("prompt restore should absorb the trailing blank once, got debt %d", surface.outputScrollDebtRows)
	}
}

func TestFixedBottomSurface_BeginOutputRawWriteLeavesPendingAndPromptRestoreCancelsGrowth(t *testing.T) {
	// Documents the clip failure mode: ClearPrompt + BeginOutput + raw write
	// leaves pendingScrollDown set, so ShowPrompt cancels growth and the prompt
	// row reclaims the last status lines without scrolling them up.
	height := 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		surface.BeginOutput()
		_, _ = fmt.Fprint(os.Stdout, "╭ top\n╰ bottom-should-be-clipped\n")
	})
	if surface.pendingScrollDownRows != 3 {
		t.Fatalf("raw write path should still have pending shrink compensation, got %d", surface.pendingScrollDownRows)
	}

	restored := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt restore")
		}
	})
	if strings.Contains(restored, "\x1b[23;1H\n") {
		t.Fatalf("bug-path prompt restore should cancel growth scroll, got %q", restored)
	}
	if surface.pendingScrollDownRows != 0 {
		// Growth canceled against pending; pending should be consumed.
		t.Fatalf("expected pending to be consumed by canceled growth, got %d", surface.pendingScrollDownRows)
	}
}

func TestFixedBottomSurface_ClosedPopupScrollCompensationFlushesOnNextOutput(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed line\n"); !ok || err != nil {
			t.Fatalf("expected output write to be handled, ok=%t err=%v", ok, err)
		}
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"one", "two"}, "command_popup")
	})

	closed := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("command_popup")
	})
	if strings.Contains(closed, terminalScrollDownSequence(2)) {
		t.Fatalf("expected popup close to defer scroll compensation, got %q", closed)
	}
	if surface.pendingScrollDownRows < 1 {
		t.Fatalf("expected pending scroll compensation after popup close, got %d", surface.pendingScrollDownRows)
	}

	next := captureUIStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "next line\n"); !ok || err != nil {
			t.Fatalf("expected output write to be handled, ok=%t err=%v", ok, err)
		}
	})
	if !strings.Contains(next, terminalScrollDownSequence(2)) {
		t.Fatalf("expected deferred compensation to flush before the next output, got %q", next)
	}
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("expected pending compensation to reset, got %d", surface.pendingScrollDownRows)
	}
}

func TestFixedBottomSurface_TerminalSizeChangeDropsPendingScrollCompensation(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected enabled surface to show prompt")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed line\n"); !ok || err != nil {
			t.Fatalf("expected output write to be handled, ok=%t err=%v", ok, err)
		}
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"one", "two"}, "command_popup")
		surface.ClearPopupForOwnerPreserveCursor("command_popup")
	})
	if surface.pendingScrollDownRows < 1 {
		t.Fatalf("expected pending compensation before resize, got %d", surface.pendingScrollDownRows)
	}

	// A layout applied for a different terminal size invalidates the deferred
	// compensation: absolute rows no longer describe the current viewport.
	surface.lastHeight = 40
	next := captureUIStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "after resize\n"); !ok || err != nil {
			t.Fatalf("expected output write to be handled, ok=%t err=%v", ok, err)
		}
	})
	if strings.Contains(next, terminalScrollDownSequence(2)) {
		t.Fatalf("expected resize to drop stale compensation, got %q", next)
	}
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("expected pending compensation to reset on resize, got %d", surface.pendingScrollDownRows)
	}
}

// The absorbed-row debt is absolute geometry too, so a resize must drop it for
// the same reason it drops a deferred shrink: paying it afterwards would scroll
// for a blank row the terminal already reflowed away.
func TestFixedBottomSurface_TerminalSizeChangeDropsAbsorbedRowDebt(t *testing.T) {
	const height = 24
	surface := newTestFixedBottomSurfaceWithSize(80, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, "committed line\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		if !surface.SetActiveBand([]string{"• assistant", "streaming"}) {
			t.Fatal("expected active band")
		}
	})
	if surface.outputScrollDebtRows != 1 {
		t.Fatalf("expected one absorbed row before resize, got %d", surface.outputScrollDebtRows)
	}
	// Guard against a vacuous assertion: a deferred shrink would block the
	// repayment on its own, so the debt must be the only outstanding item.
	if surface.pendingScrollDownRows != 0 {
		t.Fatalf("expected no deferred shrink before resize, got %d", surface.pendingScrollDownRows)
	}
	repay := terminalMoveToSequence(outputBottomRowForHeight(height, surface.effectiveBottomRowsLocked(height)), 1) + "\n"

	// A layout applied for a different terminal size invalidates absolute rows.
	surface.lastHeight = 40
	next := captureUIStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "after resize\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if strings.Contains(next, repay) {
		t.Fatalf("expected resize to drop the stale absorb debt, got %q", next)
	}
	if surface.outputScrollDebtRows != 0 {
		t.Fatalf("expected absorb debt to reset on resize, got %d", surface.outputScrollDebtRows)
	}
}

func TestFixedBottomSurface_SyncTerminalGeometryPreservesSoftTail(t *testing.T) {
	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	var wrote bytes.Buffer
	captureUIStdout(t, func() {
		if _, err, ok := surface.WriteSoftTrackedOutput(&wrote, "alpha\nbeta\n"); !ok || err != nil {
			t.Fatalf("soft write failed: ok=%t err=%v", ok, err)
		}
	})
	if !surface.SoftOutputTailValid() {
		t.Fatal("expected soft tail after soft-tracked write")
	}
	beforeCount := surface.SoftOutputTailLineCount()

	// Stale layout cache + newly pinned terminal size simulates a live resize
	// probe discovering geometry drift without an intermediate write.
	surface.lastWidth = 80
	surface.lastHeight = 24
	surface.terminal.SetSizeForTest(40, 20)

	var changed bool
	captureUIStdout(t, func() {
		changed = surface.SyncTerminalGeometry()
	})
	if !changed {
		t.Fatal("expected SyncTerminalGeometry to report size change")
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("soft tail must survive geometry sync so source reflow can rewrite it")
	}
	if got := surface.SoftOutputTailLineCount(); got != beforeCount {
		t.Fatalf("soft line count changed across sync: got %d want %d", got, beforeCount)
	}
	if surface.lastWidth != 40 || surface.lastHeight != 20 {
		t.Fatalf("layout cache not updated: last=%dx%d want 40x20", surface.lastWidth, surface.lastHeight)
	}
}

func TestFixedBottomSurface_SyncTerminalGeometryThrottledSkipsWithinInterval(t *testing.T) {
	surface := newTestFixedBottomSurfaceWithSize(80, 24)

	// Seed the probe clock with an unthrottled sync.
	captureUIStdout(t, func() {
		_ = surface.SyncTerminalGeometry()
	})
	firstProbe := surface.lastGeometryProbeAt
	if firstProbe.IsZero() {
		t.Fatal("expected lastGeometryProbeAt after unthrottled sync")
	}

	// Pin a new size while still inside the throttle window.
	surface.lastWidth = 80
	surface.lastHeight = 24
	surface.terminal.SetSizeForTest(40, 20)

	var changed, probed bool
	captureUIStdout(t, func() {
		changed, probed = surface.SyncTerminalGeometryThrottled(time.Hour)
	})
	if probed {
		t.Fatal("throttled sync must skip while minInterval has not elapsed")
	}
	if changed {
		t.Fatal("skipped probe must not report sizeChanged")
	}
	if surface.lastWidth != 80 || surface.lastHeight != 24 {
		t.Fatalf("layout cache must stay stale across skipped probe: got %dx%d", surface.lastWidth, surface.lastHeight)
	}
	if !surface.lastGeometryProbeAt.Equal(firstProbe) {
		t.Fatal("skipped probe must not advance lastGeometryProbeAt")
	}

	// Zero interval forces a probe (same contract as SyncTerminalGeometry).
	captureUIStdout(t, func() {
		changed, probed = surface.SyncTerminalGeometryThrottled(0)
	})
	if !probed {
		t.Fatal("zero-interval throttled sync must probe")
	}
	if !changed {
		t.Fatal("forced probe must observe the pinned size change")
	}
	if surface.lastWidth != 40 || surface.lastHeight != 20 {
		t.Fatalf("forced probe layout cache: got %dx%d want 40x20", surface.lastWidth, surface.lastHeight)
	}
}

// TestFixedBottomSurface_SyncTerminalGeometryProbesSizeOnce guards the paint-
// path budget: syncTerminalGeometry already called RefreshSize, so applyLayout
// must reuse that size instead of probing again under the same lock hold.
func TestFixedBottomSurface_SyncTerminalGeometryProbesSizeOnce(t *testing.T) {
	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	term := surface.terminal

	before := term.SizeProbeCountForTest()
	captureUIStdout(t, func() {
		_ = surface.SyncTerminalGeometry()
	})
	if got := term.SizeProbeCountForTest() - before; got != 1 {
		t.Fatalf("same-size SyncTerminalGeometry RefreshSize calls: got %d want 1", got)
	}

	surface.lastWidth = 80
	surface.lastHeight = 24
	term.SetSizeForTest(40, 20)
	before = term.SizeProbeCountForTest()
	var changed bool
	captureUIStdout(t, func() {
		changed = surface.SyncTerminalGeometry()
	})
	if !changed {
		t.Fatal("expected size change on second sync")
	}
	if got := term.SizeProbeCountForTest() - before; got != 1 {
		t.Fatalf("resize SyncTerminalGeometry RefreshSize calls: got %d want 1", got)
	}
	if surface.lastWidth != 40 || surface.lastHeight != 20 {
		t.Fatalf("layout cache: got %dx%d want 40x20", surface.lastWidth, surface.lastHeight)
	}
}

// TestFixedBottomSurface_ActiveBandFillsReservedRowsWithoutGap pins the band
// layout invariant: the band must occupy exactly the rows it reserves in
// bottomRowsLocked, directly above the notice/prompt/status stack. Anchoring it
// to the output bottom while the prompt is hidden used to paint the band inside
// the scroll region and leave its reserved rows blank above the status line.
func TestFixedBottomSurface_ActiveBandFillsReservedRowsWithoutGap(t *testing.T) {
	heights := []int{24, 40, 60}
	for _, height := range heights {
		for _, withPrompt := range []bool{true, false} {
			bandSizes := []int{1, 2, 3, 6, ActiveBandRows(height)}
			for _, bandRows := range bandSizes {
				name := fmt.Sprintf("height=%d/prompt=%t/rows=%d", height, withPrompt, bandRows)
				t.Run(name, func(t *testing.T) {
					surface := newTestFixedBottomSurfaceWithSize(80, height)
					lines := make([]string, 0, bandRows)
					for i := 0; i < bandRows; i++ {
						lines = append(lines, fmt.Sprintf("band-%d", i))
					}
					captureUIStdout(t, func() {
						if withPrompt {
							if !surface.ShowPrompt("> ") {
								t.Fatal("expected prompt to show")
							}
						}
						if !surface.SetActiveBand(lines) {
							t.Fatal("expected active band to render")
						}
					})

					surface.mu.Lock()
					defer surface.mu.Unlock()
					state := surface.bottomPaneStateLocked()
					activeRows := state.activeBandVisibleRowCount()
					promptRows := state.promptVisibleRowCount()
					noticeRows := state.promptNoticeVisibleRowCount()
					marginRows := state.promptVerticalMarginRowCount()
					outputBottom := surface.outputBottomRowLocked()
					statusRow := surface.statusRowLocked()
					if activeRows != bandRows {
						t.Fatalf("activeRows=%d want %d", activeRows, bandRows)
					}
					if statusRow != height {
						t.Fatalf("status row=%d want %d (pinned geometry lost)", statusRow, height)
					}
					if got, want := surface.promptRenderedStartRow, outputBottom+1; got != want {
						t.Fatalf("band start row=%d want %d (output bottom %d)", got, want, outputBottom)
					}
					wantRows := activeRows + noticeRows + marginRows + promptRows
					if surface.promptRenderedRows != wantRows {
						t.Fatalf("rendered rows=%d want %d", surface.promptRenderedRows, wantRows)
					}
					if got, want := surface.promptRenderedStartRow+surface.promptRenderedRows-1, statusRow-1; got != want {
						t.Fatalf("bottom pane ends at row %d, leaving a blank gap before status row %d", got, want+1)
					}
				})
			}
		}
	}
}

func TestFixedBottomSurface_WriteSoftTrackedOutputTracksSoftTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	var buf bytes.Buffer
	captureUIStdout(t, func() {
		n, err, handled := surface.WriteSoftTrackedOutput(&buf, "alpha\nbeta\n")
		if !handled || err != nil || n <= 0 {
			t.Fatalf("WriteSoftTrackedOutput failed: handled=%v n=%d err=%v", handled, n, err)
		}
	})

	if !surface.SoftOutputTailValid() {
		t.Fatal("expected soft tail after WriteSoftTrackedOutput")
	}
	if surface.SoftOutputTailTrimmed() {
		t.Fatal("two lines should not trim the soft window")
	}
	if got := surface.SoftOutputTailLineCount(); got != 2 {
		t.Fatalf("soft line count=%d want 2", got)
	}
	if got := surface.SoftOutputTailLines(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("soft lines=%#v", got)
	}
	if !strings.Contains(buf.String(), "alpha") || !strings.Contains(buf.String(), "beta") {
		t.Fatalf("writer missing output text: %q", buf.String())
	}
}

// Plain WriteOutput must never open or reopen a soft rewrite window: foreign
// tool/notice rows would otherwise be mistaken for assistant-owned reflow.
func TestFixedBottomSurface_WriteOutputInvalidatesSoftTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	var buf bytes.Buffer
	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteSoftTrackedOutput(&buf, "owned\n"); !handled || err != nil {
			t.Fatalf("seed WriteSoftTrackedOutput: handled=%v err=%v", handled, err)
		}
	})
	if !surface.SoftOutputTailValid() {
		t.Fatal("precondition: soft tail should exist")
	}

	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteOutput(&buf, "foreign tool line\n"); !handled || err != nil {
			t.Fatalf("WriteOutput: handled=%v err=%v", handled, err)
		}
	})
	if surface.SoftOutputTailValid() || surface.SoftOutputTailLineCount() != 0 {
		t.Fatalf("plain WriteOutput must drop soft ownership, valid=%v count=%d lines=%#v",
			surface.SoftOutputTailValid(), surface.SoftOutputTailLineCount(), surface.SoftOutputTailLines())
	}
}

// TestFixedBottomSurface_MultiLineWriteOutputAvoidsHoleInjection pins the
// "• Edited" diff corruption: when writeCompleteBlockLocked emits a
// multi-line block as one WriteOutput, there is only one applyLayoutLocked
// call and one growth/shrink cycle per complete block. Per-line writeLineLocked
// releases the surface lock between rows and lets ActiveBand/status updates
// insert permanent holes into already-scrolled content.
func TestFixedBottomSurface_MultiLineWriteOutputAvoidsHoleInjection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// Build a multi-line "• Edited" block that looks like a typical tool result.
	block := strings.Join([]string{
		"• Edited demo.go (+1 -1)",
		"  10   func main() {",
		"  11 - old()",
		"  11 + new()",
		"  12   }",
	}, "\n")

	surface := newTestFixedBottomSurfaceWithSize(80, 32)
	var buf bytes.Buffer

	// Simulate interleaved band growth: write three lines separately (old path)
	// vs one atomic multi-line block (new path) and capture scroll sequences.
	captureUIStdout(t, func() {
		for _, line := range strings.Split(block, "\n") {
			if _, err, handled := surface.WriteOutput(&buf, line+"\n"); !handled || err != nil {
				t.Fatalf("per-line WriteOutput failed: handled=%v err=%v", handled, err)
			}
		}
	})
	perLineScroll := buf.String()

	buf.Reset()
	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteOutput(&buf, block+"\n"); !handled || err != nil {
			t.Fatalf("atomic WriteOutput failed: handled=%v err=%v", handled, err)
		}
	})
	atomicScroll := buf.String()

	// The per-line path can interleave band growth and produce extra scroll-up
	// sequences (holes). The atomic path applies layout once and should be
	// cleaner for multi-line tool results.
	if strings.Count(perLineScroll, "\x1b") > strings.Count(atomicScroll, "\x1b") {
		t.Fatalf("per-line path produced more scroll sequences (%d) than atomic multi-line (%d); hole injection likely",
			strings.Count(perLineScroll, "\x1b"), strings.Count(atomicScroll, "\x1b"))
	}
	if !strings.Contains(atomicScroll, "demo.go") {
		t.Fatal("atomic multi-line write should preserve block content")
	}
}

func TestFixedBottomSurface_SoftTailTrimsAndBlocksReflowMapping(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurfaceWithSize(80, 40)
	var buf bytes.Buffer
	captureUIStdout(t, func() {
		for i := 0; i < SoftOutputTailMaxLines+5; i++ {
			line := fmt.Sprintf("line-%03d\n", i)
			if _, err, handled := surface.WriteSoftTrackedOutput(&buf, line); !handled || err != nil {
				t.Fatalf("WriteSoftTrackedOutput %d: handled=%v err=%v", i, handled, err)
			}
		}
	})

	if !surface.SoftOutputTailValid() {
		t.Fatal("trimmed soft tail should remain valid for local rewrite")
	}
	if !surface.SoftOutputTailTrimmed() {
		t.Fatal("expected soft window to mark trimmed after overflow")
	}
	if got := surface.SoftOutputTailLineCount(); got != SoftOutputTailMaxLines {
		t.Fatalf("soft line count=%d want %d", got, SoftOutputTailMaxLines)
	}
	lines := surface.SoftOutputTailLines()
	if lines[0] != fmt.Sprintf("line-%03d", 5) {
		t.Fatalf("expected oldest retained line line-005, got %q", lines[0])
	}
	if lines[len(lines)-1] != fmt.Sprintf("line-%03d", SoftOutputTailMaxLines+4) {
		t.Fatalf("expected newest retained line, got %q", lines[len(lines)-1])
	}
}

func TestFixedBottomSurface_RewriteSoftOutputTailReplacesRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	var buf bytes.Buffer
	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteSoftTrackedOutput(&buf, "old-one\nold-two\n"); !handled || err != nil {
			t.Fatalf("seed WriteSoftTrackedOutput: handled=%v err=%v", handled, err)
		}
	})
	if got := surface.SoftOutputTailLines(); len(got) != 2 {
		t.Fatalf("seed soft lines=%#v", got)
	}

	rewritten := captureUIStdout(t, func() {
		if !surface.RewriteSoftOutputTail(&buf, []string{"new-alpha", "new-beta", "new-gamma"}) {
			t.Fatal("RewriteSoftOutputTail should succeed for a valid soft window")
		}
	})
	if !surface.SoftOutputTailValid() {
		t.Fatal("soft tail should stay valid after rewrite")
	}
	if surface.SoftOutputTailTrimmed() {
		t.Fatal("rewrite should clear the trimmed flag")
	}
	if got := surface.SoftOutputTailLines(); len(got) != 3 || got[0] != "new-alpha" || got[2] != "new-gamma" {
		t.Fatalf("rewritten soft lines=%#v", got)
	}
	if !strings.Contains(buf.String(), "new-alpha") || !strings.Contains(buf.String(), "new-gamma") {
		t.Fatalf("rewrite missing new content in writer: %q", buf.String())
	}
	// Clearing old rows must use absolute cursor moves.
	if !strings.Contains(rewritten, "\x1b[") {
		t.Fatalf("expected ANSI cursor moves while rewriting soft rows, got %q", rewritten)
	}
}

func TestFixedBottomSurface_InvalidateAndDisableDropSoftTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	var buf bytes.Buffer
	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteSoftTrackedOutput(&buf, "keep-in-scrollback\n"); !handled || err != nil {
			t.Fatalf("WriteSoftTrackedOutput: handled=%v err=%v", handled, err)
		}
	})
	if !surface.SoftOutputTailValid() {
		t.Fatal("expected soft tail before invalidate")
	}

	surface.InvalidateSoftOutputTail()
	if surface.SoftOutputTailValid() || surface.SoftOutputTailLineCount() != 0 {
		t.Fatalf("invalidate should drop soft ownership, valid=%v count=%d", surface.SoftOutputTailValid(), surface.SoftOutputTailLineCount())
	}

	// Plain WriteOutput must not reopen the rewrite window after invalidate.
	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteOutput(&buf, "again\n"); !handled || err != nil {
			t.Fatalf("second WriteOutput: handled=%v err=%v", handled, err)
		}
	})
	if surface.SoftOutputTailValid() {
		t.Fatal("plain WriteOutput must not re-seed soft ownership")
	}

	// Only the soft-tracked path may re-open the window.
	captureUIStdout(t, func() {
		if _, err, handled := surface.WriteSoftTrackedOutput(&buf, "owned-again\n"); !handled || err != nil {
			t.Fatalf("WriteSoftTrackedOutput re-seed: handled=%v err=%v", handled, err)
		}
	})
	if !surface.SoftOutputTailValid() {
		t.Fatal("expected soft tail after soft-tracked re-seed")
	}
	captureUIStdout(t, func() {
		surface.Disable()
	})
	if surface.SoftOutputTailValid() {
		t.Fatal("Disable should drop the soft rewrite window")
	}
}

func TestFixedBottomSurface_AdoptSoftOutputTailRebasesWindow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := newTestFixedBottomSurfaceWithSize(80, 40)
	var buf bytes.Buffer
	captureUIStdout(t, func() {
		for i := 0; i < SoftOutputTailMaxLines+3; i++ {
			line := fmt.Sprintf("keep-%03d\n", i)
			if _, err, handled := surface.WriteSoftTrackedOutput(&buf, line); !handled || err != nil {
				t.Fatalf("WriteSoftTrackedOutput %d: handled=%v err=%v", i, handled, err)
			}
		}
	})
	if !surface.SoftOutputTailTrimmed() {
		t.Fatal("precondition: soft tail should be trimmed")
	}

	adopted := []string{"only-a", "only-b"}
	surface.AdoptSoftOutputTail(adopted)
	if surface.SoftOutputTailTrimmed() {
		t.Fatal("adopt should clear trimmed flag for the rebased window")
	}
	if got := surface.SoftOutputTailLines(); len(got) != 2 || got[0] != "only-a" || got[1] != "only-b" {
		t.Fatalf("adopted soft lines=%#v", got)
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("adopted soft tail should remain valid")
	}
}
