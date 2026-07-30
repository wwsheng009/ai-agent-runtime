package ui

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

func TestFixedBottomSurface_HistoryRowsSnapshotMaterializesWrapBlankStyleAndWideCells(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newTestFixedBottomSurfaceWithSize(4, 12)
	surface.historyWindow = []string{
		"ab中d",
		"",
		"\x1b[31mred\x1b[0m",
	}

	rows := surface.HistoryRowsSnapshot()
	if got, want := len(rows), 4; got != want {
		t.Fatalf("physical rows=%d want %d", got, want)
	}
	screen := vt.NewScreen(4, len(rows))
	for row, cells := range rows {
		for col, cell := range cells {
			if cell.Text == "" || cell.Cont {
				continue
			}
			screen.Feed(terminalMoveToSequence(row+1, col+1))
			if len(cell.SGR) > 0 {
				screen.Feed("\x1b[" + strings.Join(cell.SGR, ";") + "m")
			}
			screen.Feed(cell.Text)
			screen.Feed("\x1b[0m")
		}
	}
	if got, want := screen.Lines(1, 4), []string{"ab中", "d", "", "red"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("materialized lines=%q want %q", got, want)
	}
	if !rows[0][3].Cont {
		t.Fatalf("wide-rune continuation missing: %+v", rows[0])
	}
	if got := rows[3][0].SGR; len(got) != 1 || got[0] != "31" {
		t.Fatalf("history style lost: %q", got)
	}
}

func TestFixedBottomSurface_BottomRowsSnapshotMatchesLegacyVT(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	const width, height = 32, 24
	surface := newTestFixedBottomSurfaceWithSize(width, height)
	screen := vt.NewScreen(width, height)
	apply := func(name string, paint func()) {
		t.Helper()
		output := captureUIStdout(t, paint)
		screen.Feed(output)
		assertBottomRowsSnapshotMatchesScreen(t, name, surface, screen)
	}

	apply("prompt", func() {
		surface.ShowPrompt("> ")
		surface.SetPromptInputState("> ", "你好 viewport", 2, 0, 15)
	})
	apply("active band and statuses", func() {
		surface.SetPromptNoticeLine("notice")
		surface.SetPromptEditorStatusLine("editor status")
		dynamic := style.StatusLineModel{State: style.RunThinking, StateText: "◦ Working"}
		surface.SetStatusModels(
			style.StatusLineModel{State: style.RunReady, StateText: "Ready footer"},
			&dynamic,
		)
		surface.SetActiveBand([]string{"assistant", "中文 active", "tool progress"})
	})
	apply("same-height active diff", func() {
		surface.SetActiveBand([]string{"assistant", "中文 changed", "tool done"})
	})
	apply("active shrink", func() {
		surface.SetActiveBand([]string{"tool done"})
	})
	apply("active clear", func() {
		surface.ClearActiveBand()
	})
	apply("popup above prompt", func() {
		surface.ShowPopup([]string{"commands", "> /help", "  /clear"})
	})
	apply("popup clear", func() {
		surface.ClearPopup()
	})
	apply("popup composer", func() {
		surface.ShowPopupInputForOwner(
			[]string{"approval", "allow once", "deny"},
			"请选择 [1-2]: ",
			"approval",
		)
	})
}

func TestFixedBottomSurface_BottomRowsSnapshotPreservesStyledCells(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("AICLI_COLOR_DEPTH", "truecolor")
	t.Setenv("FORCE_COLOR", "1")

	const width, height = 36, 24
	surface := newTestFixedBottomSurfaceWithSize(width, height)
	surface.terminal.driver.caps = TerminalCapabilities{Interactive: true, ANSI: true}
	screen := vt.NewScreen(width, height)
	output := captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.SetStatusModels(style.StatusLineModel{
			HideState: true,
			Segments: []style.StatusSegment{
				{Text: "model", Role: style.RoleAccent},
				{Text: "context", Role: style.RoleProgress},
			},
		}, nil)
		surface.SetActiveBandStyled([]render.Line{
			{Spans: []render.Span{{Text: "assistant", Style: render.Style{Role: string(style.RoleAccent)}}}},
			{Spans: []render.Span{{Text: "中", Style: render.Style{Foreground: render.RGB(255, 0, 0)}}}},
		})
	})
	screen.Feed(output)

	assertBottomRowsSnapshotMatchesScreen(t, "styled active/status", surface, screen)
	snapshot := surface.BottomRowsSnapshot()
	foundStyle, foundWide := false, false
	for _, row := range snapshot {
		for _, cell := range row {
			foundStyle = foundStyle || len(cell.SGR) > 0
			foundWide = foundWide || cell.Cont
		}
	}
	if !foundStyle {
		t.Fatal("snapshot lost all SGR cell styles")
	}
	if !foundWide {
		t.Fatal("snapshot lost wide-cell continuation markers")
	}
}

func TestFixedBottomSurface_ComposedFrameShadowMatchesLegacyBeforeShrink(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 32, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	screen := vt.NewScreen(width, height)
	feed := func(name string, paint func()) int {
		t.Helper()
		screen.Feed(captureUIStdout(t, paint))
		expected := surface.ComposedFrameForTest()
		actual := screen.CellRows(1, height)
		differences := frameCellDifferences(expected, actual)
		if differences > 0 {
			t.Logf("%s: shadow frame differs in %d cells", name, differences)
		}
		assertBottomRowsSnapshotMatchesScreen(t, name, surface, screen)
		return differences
	}

	feed("prompt", func() {
		surface.ShowPrompt("> ")
	})
	feed("history", func() {
		text := strings.Repeat("history line\n", 40)
		if _, err, ok := surface.WriteOutput(os.Stdout, text); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if differences := feed("dynamic status", func() {
		dynamic := style.StatusLineModel{State: style.RunThinking, StateText: "◦ Working"}
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, &dynamic)
	}); differences != 0 {
		t.Fatalf("dynamic status should be frame-equivalent, differences=%d", differences)
	}
	if differences := feed("active band growth", func() {
		surface.SetActiveBand([]string{"assistant", "中文 active", "tool progress"})
	}); differences != 0 {
		t.Fatalf("active-band growth should be frame-equivalent, differences=%d", differences)
	}

	if differences := feed("active band shrink", func() {
		surface.SetActiveBand([]string{"tool done"})
	}); differences != 0 {
		t.Fatalf("owned-history reserve shrink differs from composed frame: differences=%d", differences)
	}
}

func TestFixedBottomSurface_ActiveBandShrinkRestoresOwnedHistoryRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 32, 12

	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	screen := vt.NewScreen(width, height)
	feed := func(paint func()) {
		t.Helper()
		screen.Feed(captureUIStdout(t, paint))
	}

	feed(func() {
		surface.ShowPrompt("> ")
	})
	outputBottom := surface.outputBottomRowLocked()
	history := make([]string, outputBottom-1)
	for i := range history {
		history[i] = fmt.Sprintf("H%02d", i+1)
	}
	// Preserve one intentional Markdown-style blank row. The regression checks
	// exact row identity rather than treating every blank as compensation noise.
	history[3] = ""
	feed(func() {
		text := strings.Join(history, "\n") + "\n"
		if _, err, ok := surface.WriteOutput(os.Stdout, text); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	wantOutput := append(append([]string(nil), history...), "")
	if got := screen.Lines(1, outputBottom); !reflect.DeepEqual(got, wantOutput) {
		t.Fatalf("precondition: output rows=%q want=%q\n%s", got, wantOutput, screen.Dump())
	}

	feed(func() {
		surface.SetActiveBand([]string{"active-1", "active-2", "active-3"})
	})
	feed(func() {
		surface.ClearActiveBand()
	})

	if got := screen.Lines(1, outputBottom); !reflect.DeepEqual(got, wantOutput) {
		t.Fatalf("owned history was not restored after band shrink\ngot:  %q\nwant: %q\n%s", got, wantOutput, screen.Dump())
	}
	if differences := frameCellDifferences(surface.ComposedFrameForTest(), screen.CellRows(1, height)); differences != 0 {
		t.Fatalf("production frame differs from owned composition after shrink: differences=%d\n%s", differences, screen.Dump())
	}
}

func TestFixedBottomSurface_ComposedFrameShadowCharacterizesPopupClose(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 32, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	screen := vt.NewScreen(width, height)
	feed := func(name string, paint func()) int {
		t.Helper()
		screen.Feed(captureUIStdout(t, paint))
		assertBottomRowsSnapshotMatchesScreen(t, name, surface, screen)
		return frameCellDifferences(surface.ComposedFrameForTest(), screen.CellRows(1, height))
	}

	feed("seed prompt and history", func() {
		surface.ShowPrompt("> ")
		text := strings.Repeat("popup history\n", 40)
		if _, err, ok := surface.WriteOutput(os.Stdout, text); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if differences := feed("popup growth", func() {
		surface.ShowPopup([]string{"commands", "> /help", "  /clear"})
	}); differences != 0 {
		t.Fatalf("popup growth should be frame-equivalent, differences=%d", differences)
	}
	feed("popup close", func() {
		surface.ClearPopup()
	})
	if differences := feed("popup settle", func() {
		surface.SettleOutputDebt()
	}); differences != 0 {
		t.Fatalf("owned-history popup settle differs from composed frame: differences=%d", differences)
	}
}

func frameCellDifferences(a, b [][]vt.Cell) int {
	rows := len(a)
	if len(b) > rows {
		rows = len(b)
	}
	differences := 0
	for row := 0; row < rows; row++ {
		cols := 0
		if row < len(a) {
			cols = len(a[row])
		}
		if row < len(b) && len(b[row]) > cols {
			cols = len(b[row])
		}
		for col := 0; col < cols; col++ {
			var left, right vt.Cell
			if row < len(a) && col < len(a[row]) {
				left = a[row][col]
			}
			if row < len(b) && col < len(b[row]) {
				right = b[row][col]
			}
			if !reflect.DeepEqual(left, right) {
				differences++
			}
		}
	}
	return differences
}

func frameDump(rows [][]vt.Cell) string {
	var lines []string
	for _, row := range rows {
		var line strings.Builder
		for _, cell := range row {
			if !cell.Cont {
				line.WriteString(cell.Text)
			}
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

func assertBottomRowsSnapshotMatchesScreen(
	t *testing.T,
	name string,
	surface *FixedBottomSurface,
	screen *vt.Screen,
) {
	t.Helper()
	got := surface.BottomRowsSnapshot()
	if len(got) == 0 {
		t.Fatalf("%s: empty bottom snapshot", name)
	}
	start := screen.Height() - len(got) + 1
	want := screen.CellRows(start, screen.Height())
	if reflect.DeepEqual(got, want) {
		return
	}
	for row := range got {
		for col := range got[row] {
			if !reflect.DeepEqual(got[row][col], want[row][col]) {
				t.Fatalf(
					"%s: cell mismatch at absolute (%d,%d): got=%s want=%s\nlegacy:\n%s",
					name,
					start+row,
					col+1,
					formatSnapshotCell(got[row][col]),
					formatSnapshotCell(want[row][col]),
					screen.Dump(),
				)
			}
		}
	}
	t.Fatalf("%s: row dimensions differ: got=%d want=%d", name, len(got), len(want))
}

func formatSnapshotCell(cell vt.Cell) string {
	return fmt.Sprintf("{Text:%q Cont:%t SGR:%q}", cell.Text, cell.Cont, cell.SGR)
}
