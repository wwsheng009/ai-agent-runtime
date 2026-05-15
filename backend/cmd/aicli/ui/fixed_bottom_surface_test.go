package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestTruncateFixedStatusLineFitsWidth(t *testing.T) {
	if got := truncateFixedStatusLine("Ready | model mimo", 80); got != "Ready | model mimo" {
		t.Fatalf("expected status line to remain unchanged, got %q", got)
	}
}

func TestTruncateFixedStatusLineAddsAsciiEllipsis(t *testing.T) {
	got := truncateFixedStatusLine("Ready | model mimo | provider anthropic", 16)
	if got != "Ready | model..." {
		t.Fatalf("unexpected truncated status line: %q", got)
	}
	if DisplayWidth(got) > 16 {
		t.Fatalf("expected truncated status line to fit width, got width=%d text=%q", DisplayWidth(got), got)
	}
}

func TestFixedBottomSurface_ShowPopupClampsToViewportHeight(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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

	if surface.bottomRowsLocked() != 4 {
		t.Fatalf("expected prompt + popup rows + status, got %d", surface.bottomRowsLocked())
	}
	if got := surface.outputBottomRowLocked(); got != 20 {
		t.Fatalf("expected output region to shift above prompt and popup, got row %d", got)
	}
	if got := surface.promptBottomRowLocked(); got != 21 {
		t.Fatalf("expected prompt row above downward popup, got row %d", got)
	}
	if surface.popupRenderedStartRow != 22 {
		t.Fatalf("expected popup to start below prompt at row 22, got %d", surface.popupRenderedStartRow)
	}
	if !strings.Contains(output, "\x1b[1;22r\x1b[1;1H\x1b[22;1H\n\n\x1b[1;20r") {
		t.Fatalf("expected output region to scroll up before reserving slash popup rows, got %q", output)
	}
	if !strings.Contains(output, "\x1b[1;20r") {
		t.Fatalf("expected scroll region to shift above prompt and popup, got %q", output)
	}
	if !strings.Contains(output, "\x1b[21;1H\x1b[K") || !strings.Contains(output, "\x1b[21;1H> /he") {
		t.Fatalf("expected prompt input row to move above slash popup, got %q", output)
	}
	if count := strings.Count(output, "\x1b[21;1H> /he"); count != 1 {
		t.Fatalf("expected slash popup expansion to render prompt input once, got %d in %q", count, output)
	}
	if !strings.Contains(output, "\x1b[22;1H\x1b[K命令补全") {
		t.Fatalf("expected slash popup to render below prompt, got %q", output)
	}
	if strings.Contains(output, "\x1b[21;1H\x1b[K命令补全") {
		t.Fatalf("expected slash popup not to render on prompt row, got %q", output)
	}
	if strings.Contains(output, cursorSaveSequence) || strings.Contains(output, cursorRestoreSequence) {
		t.Fatalf("expected downward popup render to move to prompt cursor instead of saved cursor restore, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[21;6H"+cursorShowSequence) {
		t.Fatalf("expected downward popup render to leave cursor on lifted prompt row, got %q", output)
	}

	updateOutput := captureUIStdout(t, func() {
		if !surface.SetPromptInputState("> ", "/help", 1, 0, 7) {
			t.Fatal("expected enabled surface to update lifted prompt input")
		}
	})
	if !strings.Contains(updateOutput, "\x1b[21;1H> /help") {
		t.Fatalf("expected prompt updates to keep rendering above downward popup, got %q", updateOutput)
	}
	if strings.Contains(updateOutput, cursorSaveSequence) || strings.Contains(updateOutput, cursorRestoreSequence) {
		t.Fatalf("expected prompt updates during downward popup to restore tracked prompt cursor directly, got %q", updateOutput)
	}
	if !strings.HasSuffix(updateOutput, "\x1b[21;8H"+cursorShowSequence) {
		t.Fatalf("expected prompt update to leave cursor on lifted prompt row, got %q", updateOutput)
	}

	secondOutput := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /h",
			"> /help     显示命令帮助",
		}, "slash_completion")
	})
	if strings.Contains(secondOutput, "\n\n") || strings.Contains(secondOutput, "\x1b[1;22r\x1b[1;1H\x1b[22;1H") {
		t.Fatalf("expected same slash popup update not to scroll output again, got %q", secondOutput)
	}
	if !strings.Contains(secondOutput, "\x1b[21;1H> /help") {
		t.Fatalf("expected same slash popup update to keep prompt row fixed, got %q", secondOutput)
	}
	if !strings.HasSuffix(secondOutput, "\x1b[21;8H"+cursorShowSequence) {
		t.Fatalf("expected same slash popup update to leave cursor on lifted prompt row, got %q", secondOutput)
	}

	prefix, ok := surface.PromptCursorPrefix(0, 7)
	if !ok || !strings.HasSuffix(prefix, "\x1b[21;8H") {
		t.Fatalf("expected prompt cursor prefix to target row above popup, ok=%t prefix=%q", ok, prefix)
	}

	clearOutput := captureUIStdout(t, func() {
		surface.ClearPopupForOwnerPreserveCursor("slash_completion")
	})
	if !strings.Contains(clearOutput, "\x1b[22;1H\x1b[K") || !strings.Contains(clearOutput, "\x1b[23;1H\x1b[K") {
		t.Fatalf("expected downward popup rows to clear, got %q", clearOutput)
	}
	if !strings.Contains(clearOutput, "\x1b[21;1H\x1b[K") {
		t.Fatalf("expected old lifted prompt row to clear when popup closes, got %q", clearOutput)
	}
	if !strings.Contains(clearOutput, "\x1b[23;1H\x1b[K") || !strings.Contains(clearOutput, "\x1b[23;1H> /help") {
		t.Fatalf("expected prompt input row to return below after popup clears, got %q", clearOutput)
	}
	if strings.Contains(clearOutput, cursorSaveSequence) || strings.Contains(clearOutput, cursorRestoreSequence) {
		t.Fatalf("expected downward popup clear to move to relocated prompt cursor instead of saved cursor restore, got %q", clearOutput)
	}
	if !strings.HasSuffix(clearOutput, "\x1b[23;8H"+cursorShowSequence) {
		t.Fatalf("expected downward popup clear to leave cursor on restored prompt row, got %q", clearOutput)
	}

	reopenOutput := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /",
			"> /help",
		}, "slash_completion")
	})
	if strings.Contains(reopenOutput, "\n\n") || strings.Contains(reopenOutput, "\x1b[1;22r\x1b[1;1H\x1b[22;1H") {
		t.Fatalf("expected reopening slash popup without new output not to scroll output again, got %q", reopenOutput)
	}
	if !strings.Contains(reopenOutput, "\x1b[21;1H> /help") {
		t.Fatalf("expected reopened slash popup to reuse lifted prompt row, got %q", reopenOutput)
	}
}

func TestFixedBottomSurface_TrackPromptInputStateDoesNotRedraw(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	if !ok || !strings.HasSuffix(prefix, "\x1b[23;4H") {
		t.Fatalf("expected prefix to target requested redraw start cursor, ok=%t prefix=%q", ok, prefix)
	}

	popupOutput := captureUIStdout(t, func() {
		surface.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{
			"命令补全: /h",
			"> /help     显示命令帮助",
		}, "slash_completion")
	})
	if !strings.Contains(popupOutput, "\x1b[21;1H> /help") {
		t.Fatalf("expected later popup render to use tracked prompt input, got %q", popupOutput)
	}
	if !strings.HasSuffix(popupOutput, "\x1b[21;8H"+cursorShowSequence) {
		t.Fatalf("expected later popup render to restore tracked cursor, got %q", popupOutput)
	}
}

func TestFixedBottomSurface_ShowPopupDoesNotUseCursorSaveRestore(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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

func TestFixedBottomSurface_ClearPopupForOwnerPreserveCursorKeepsOtherPopup(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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

func TestFixedBottomSurface_LowerPriorityOwnerUpdateDoesNotStealActivePopup(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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

func TestFixedBottomSurface_ClearPopupKeepsStatusLine(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

	surface := newTestFixedBottomSurface()
	surface.statusLine = "Ready | model gpt-4.1"

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

	if surface.statusLine != "Ready | model gpt-4.1" {
		t.Fatalf("expected status line to remain unchanged, got %q", surface.statusLine)
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

func TestFixedBottomSurface_SetStatusLinePreservesCursor(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

	surface := newTestFixedBottomSurface()

	output := captureUIStdout(t, func() {
		surface.SetStatusLine("Ready | Agent Panel")
	})

	if !strings.Contains(output, cursorSaveSequence) {
		t.Fatalf("expected status render to save cursor, got %q", output)
	}
	if !strings.HasSuffix(output, cursorRestoreSequence) {
		t.Fatalf("expected status render to restore cursor at the end, got %q", output)
	}
}

func TestFixedBottomSurface_ShowPopupInputFocusesPromptRow(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

	surface := newTestFixedBottomSurface()
	surface.statusLine = "Ready | composer"

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

func TestFixedBottomSurface_ShowPendingPastePreviewRendersPreview(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

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

	if !strings.Contains(output, "\x1b[1;22r") {
		t.Fatalf("expected prompt reserve to move scroll bottom above prompt row, got %q", output)
	}
	if !strings.Contains(output, "\x1b[23;1H> ") {
		t.Fatalf("expected prompt to render on row above status, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[22;1H") {
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
	if !strings.Contains(output, "\x1b[23;1H\x1b[K") {
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
		"\x1b[21;1H\x1b[K",
		"\x1b[22;1H\x1b[K",
		"\x1b[23;1H\x1b[K",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected reset to clear old prompt row %q, got %q", expected, output)
		}
	}
	if !strings.Contains(output, "\x1b[23;1H> ") {
		t.Fatalf("expected reset to redraw visible prompt, got %q", output)
	}
	if !strings.Contains(output, "\x1b[1;22r") {
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

	if !strings.Contains(output, "\x1b[1;20r") {
		t.Fatalf("expected three prompt rows to move scroll bottom above input block, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[20;1H") {
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

	if !strings.Contains(output, "\x1b[22;1Hreasoning\n") {
		t.Fatalf("expected output to be written above prompt row, got %q", output)
	}
	if strings.Contains(output, "\x1b[23;1Hreasoning") {
		t.Fatalf("expected output not to be written on prompt row, got %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[23;3H") {
		t.Fatalf("expected cursor to return after visible prompt, got %q", output)
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

	if !strings.HasSuffix(output, "\x1b[23;8H") {
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

func newTestFixedBottomSurface() *FixedBottomSurface {
	term := &Terminal{
		width:  80,
		height: 24,
		theme:  GetTheme(ThemeAuto),
		driver: &TerminalDriver{},
	}
	surface := NewFixedBottomSurface(term)
	surface.enabled = true
	return surface
}
