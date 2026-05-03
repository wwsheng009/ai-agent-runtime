package ui

import (
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
			"提示: ↑↓ 选择，Tab/Enter 接受，Esc 关闭",
		})
	})

	if surface.popupRenderedRows != 3 {
		t.Fatalf("expected three popup rows, got %d", surface.popupRenderedRows)
	}
	if surface.popupRenderedGapRows != 1 {
		t.Fatalf("expected one reserved input gap row, got %d", surface.popupRenderedGapRows)
	}
	if surface.bottomRowsLocked() != 5 {
		t.Fatalf("expected popup rows + input gap + status, got %d", surface.bottomRowsLocked())
	}
	if got := surface.popupStartRowLocked(surface.popupRenderedRows, surface.popupRenderedGapRows); got != 20 {
		t.Fatalf("expected popup to start at row 20 so row 23 remains for input, got %d", got)
	}
	if strings.Contains(output, "\x1b[23;1H提示") {
		t.Fatalf("expected hint line not to render on input row 23, got %q", output)
	}
	if !strings.Contains(output, "\x1b[22;1H") {
		t.Fatalf("expected last popup line to render on row 22, got %q", output)
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
