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

// TestFixedBottomSurface_BottomRowsSnapshotMatchesLegacyVT is migrated to the
// L3-2 state-only surface: the legacy byte writer is retired, so the bottom
// reserve snapshot is compared against the authoritative composed frame
// instead of a vt.Screen replay. Every prompt/status/band/popup transition
// must keep BottomRowsSnapshot equal to the composed frame's bottom rows.
func TestFixedBottomSurface_BottomRowsSnapshotMatchesLegacyVT(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	const width, height = 32, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	apply := func(name string, paint func()) {
		t.Helper()
		captureUIStdout(t, paint)
		assertBottomRowsSnapshotMatchesComposedFrame(t, name, surface)
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

// TestFixedBottomSurface_BottomRowsSnapshotPreservesStyledCells is migrated to
// the L3-2 state-only surface: the snapshot must still preserve SGR styles and
// wide-cell continuation markers after the legacy byte writer retirement, and
// must stay equal to the composed frame's bottom rows.
func TestFixedBottomSurface_BottomRowsSnapshotPreservesStyledCells(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("AICLI_COLOR_DEPTH", "truecolor")
	t.Setenv("FORCE_COLOR", "1")

	const width, height = 36, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	surface.terminal.driver.caps = TerminalCapabilities{Interactive: true, ANSI: true}
	captureUIStdout(t, func() {
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

	assertBottomRowsSnapshotMatchesComposedFrame(t, "styled active/status", surface)
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

func TestFixedBottomSurface_ActiveBandHasOneCollapsibleTopGap(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	const width, height = 32, 12
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		if _, err, ok := surface.WriteOutput(os.Stdout, "history-tail\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
		if !surface.SetActiveBand([]string{"• Running grep"}) {
			t.Fatal("expected ActiveBand update")
		}
	})

	lines := strings.Split(frameDump(surface.ComposedFrameForTest()), "\n")
	historyRow, activeRow := -1, -1
	for row, line := range lines {
		switch strings.TrimSpace(line) {
		case "history-tail":
			historyRow = row
		case "• Running grep":
			activeRow = row
		}
	}
	if historyRow < 0 || activeRow < 0 {
		t.Fatalf("failed to anchor history/ActiveBand rows:\n%s", strings.Join(lines, "\n"))
	}
	if activeRow != historyRow+2 || strings.TrimSpace(lines[historyRow+1]) != "" {
		t.Fatalf("expected exactly one blank before ActiveBand: history=%d active=%d\n%s",
			historyRow, activeRow, strings.Join(lines, "\n"))
	}
	if got := surface.ActiveBandLines(); !reflect.DeepEqual(got, []string{"• Running grep"}) {
		t.Fatalf("semantic gap must not pollute ActiveBandLines: %q", got)
	}

	short := newOwnedTestFixedBottomSurfaceWithSize(width, activeBandTopGapMinHeight-1)
	short.activeBandLines = []string{"• Running grep"}
	state := short.bottomPaneStateLocked()
	if got := state.activeBandTopGapRowCount(); got != 0 {
		t.Fatalf("short terminal must collapse ActiveBand gap, got %d", got)
	}
}

// TestFixedBottomSurface_ComposedFrameShadowMatchesLegacyBeforeShrink is
// migrated to the L3-2 state-only surface: the legacy vt.Screen shadow is
// retired with the byte writer. The equivalent characterization is that the
// composed frame is always terminal-height, its bottom reserve equals
// BottomRowsSnapshot, and its history section is the newest retained history
// rows - so band growth may only hide the oldest retained rows and the band
// shrink restores them without dropping or duplicating rows.
func TestFixedBottomSurface_ComposedFrameShadowMatchesLegacyBeforeShrink(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 32, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	feed := func(name string, paint func()) {
		t.Helper()
		captureUIStdout(t, paint)
		assertComposedFrameMatchesRetainedState(t, name, surface)
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
	feed("dynamic status", func() {
		dynamic := style.StatusLineModel{State: style.RunThinking, StateText: "◦ Working"}
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, &dynamic)
	})
	feed("active band growth", func() {
		surface.SetActiveBand([]string{"assistant", "中文 active", "tool progress"})
	})
	feed("active band shrink", func() {
		surface.SetActiveBand([]string{"tool done"})
	})
}

// TestFixedBottomSurface_ActiveBandShrinkRestoresOwnedHistoryRows pins the
// L3-2 state-only restoration contract: history that fits the visible output
// region is never handed off, band growth may hide its oldest rows from the
// composed frame, and clearing the band restores the exact pre-growth frame.
// The regression checks exact row identity rather than treating every blank as
// compensation noise.
func TestFixedBottomSurface_ActiveBandShrinkRestoresOwnedHistoryRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 32, 12

	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	captureUIStdout(t, func() {
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
	captureUIStdout(t, func() {
		text := strings.Join(history, "\n") + "\n"
		if _, err, ok := surface.WriteOutput(os.Stdout, text); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	wantOutput := append(append([]string(nil), history...), "")
	assertOutputRows := func(label string) {
		t.Helper()
		lines := strings.Split(frameDump(surface.ComposedFrameForTest()), "\n")
		if len(lines) < outputBottom {
			t.Fatalf("%s: composed frame rows=%d want at least %d", label, len(lines), outputBottom)
		}
		if got := lines[:outputBottom]; !reflect.DeepEqual(got, wantOutput) {
			t.Fatalf("%s: owned output rows=%q want=%q\n%s", label, got, wantOutput, strings.Join(lines, "\n"))
		}
	}
	assertOutputRows("precondition")

	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("history that fits the visible output region must not hand off, frontier=%d", got)
	}
	beforeGrow := frameDump(surface.ComposedFrameForTest())

	captureUIStdout(t, func() {
		surface.SetActiveBand([]string{"active-1", "active-2", "active-3"})
	})
	captureUIStdout(t, func() {
		surface.ClearActiveBand()
	})

	if got := frameDump(surface.ComposedFrameForTest()); got != beforeGrow {
		t.Fatalf("owned history was not restored after band shrink\ngot:  %q\nwant: %q", got, beforeGrow)
	}
	assertOutputRows("after band shrink")
}

// TestFixedBottomSurface_ComposedFrameShadowCharacterizesPopupClose is
// migrated to the L3-2 state-only surface: the legacy vt.Screen shadow is
// retired with the byte writer. The characterization now pins that popup
// growth does not disturb the retained history window (handed-off rows never
// re-enter the composed frame) and that popup close plus output settle
// restores the exact pre-popup frame.
func TestFixedBottomSurface_ComposedFrameShadowCharacterizesPopupClose(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 32, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		text := strings.Repeat("popup history\n", 40)
		if _, err, ok := surface.WriteOutput(os.Stdout, text); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	assertComposedFrameMatchesRetainedState(t, "seed prompt and history", surface)
	beforePopup := frameDump(surface.ComposedFrameForTest())

	captureUIStdout(t, func() {
		surface.ShowPopup([]string{"commands", "> /help", "  /clear"})
	})
	assertComposedFrameMatchesRetainedState(t, "popup growth", surface)
	if frame := frameDump(surface.ComposedFrameForTest()); !strings.Contains(frame, "> /help") {
		t.Fatalf("popup growth must render popup content:\n%s", frame)
	}

	captureUIStdout(t, func() {
		surface.ClearPopup()
	})
	captureUIStdout(t, func() {
		surface.SettleOutputDebt()
	})
	assertComposedFrameMatchesRetainedState(t, "popup settle", surface)
	if got := frameDump(surface.ComposedFrameForTest()); got != beforePopup {
		t.Fatalf("owned-history popup settle differs from pre-popup frame\ngot:\n%s\nwant:\n%s", got, beforePopup)
	}
	if frame := frameDump(surface.ComposedFrameForTest()); strings.Contains(frame, "> /help") {
		t.Fatalf("popup content leaked after close:\n%s", frame)
	}
}

// assertBottomRowsSnapshotMatchesComposedFrame pins the L3-2 state-only
// contract: the bottom reserve snapshot must equal the bottom rows of the
// authoritative composed frame after every state transition.
func assertBottomRowsSnapshotMatchesComposedFrame(t *testing.T, name string, surface *FixedBottomSurface) {
	t.Helper()
	got := surface.BottomRowsSnapshot()
	if len(got) == 0 {
		t.Fatalf("%s: empty bottom snapshot", name)
	}
	frame := surface.ComposedFrameForTest()
	if len(frame) < len(got) {
		t.Fatalf("%s: composed frame rows=%d shorter than bottom snapshot rows=%d", name, len(frame), len(got))
	}
	want := frame[len(frame)-len(got):]
	if reflect.DeepEqual(got, want) {
		return
	}
	for row := range got {
		for col := range got[row] {
			if !reflect.DeepEqual(got[row][col], want[row][col]) {
				t.Fatalf(
					"%s: bottom snapshot cell mismatch at frame row %d col %d: got=%s want=%s",
					name,
					len(frame)-len(got)+row+1,
					col+1,
					formatSnapshotCell(got[row][col]),
					formatSnapshotCell(want[row][col]),
				)
			}
		}
	}
	t.Fatalf("%s: bottom snapshot row dimensions differ: got=%d want=%d", name, len(got), len(want))
}

// assertComposedFrameMatchesRetainedState pins the L3-2 state-only frame
// contract: the composed frame is terminal-height, its bottom reserve equals
// BottomRowsSnapshot, and its history section is the newest retained history
// rows (optionally followed by the cursor-parking blank row). Rows handed off
// to scrollback never re-enter the frame, and reserve growth may only hide
// rows at the oldest edge.
func assertComposedFrameMatchesRetainedState(t *testing.T, name string, surface *FixedBottomSurface) {
	t.Helper()
	frame := surface.ComposedFrameForTest()
	if got, want := len(frame), surface.terminal.Height(); got != want {
		t.Fatalf("%s: composed frame rows=%d want %d", name, got, want)
	}
	bottom := surface.BottomRowsSnapshot()
	if len(bottom) == 0 || len(bottom) > len(frame) {
		t.Fatalf("%s: bottom snapshot rows=%d frame rows=%d", name, len(bottom), len(frame))
	}
	tail := frame[len(frame)-len(bottom):]
	if !reflect.DeepEqual(tail, bottom) {
		t.Fatalf("%s: bottom reserve diverges from composed frame:\n%s", name, frameDump(frame))
	}
	head := frame[:len(frame)-len(bottom)]
	history := surface.HistoryRowsSnapshot()
	if !frameHistorySectionMatches(head, history) {
		t.Fatalf(
			"%s: composed history rows are not the newest retained rows (frame history=%d retained=%d)\n%s",
			name,
			len(head),
			len(history),
			frameDump(frame),
		)
	}
}

// frameHistorySectionMatches reports whether the frame's history section is
// blank padding plus the newest retained history rows, with an optional
// trailing cursor-parking blank row.
func frameHistorySectionMatches(head, history [][]vt.Cell) bool {
	if len(history) == 0 {
		return allFrameRowsBlank(head)
	}
	if matchHistorySuffix(head, history) {
		return true
	}
	if len(head) > 0 && isBlankFrameRow(head[len(head)-1]) {
		return matchHistorySuffix(head[:len(head)-1], history)
	}
	return false
}

func matchHistorySuffix(head, history [][]vt.Cell) bool {
	rows := len(history)
	if rows > len(head) {
		rows = len(head)
	}
	if !reflect.DeepEqual(head[len(head)-rows:], history[len(history)-rows:]) {
		return false
	}
	return allFrameRowsBlank(head[:len(head)-rows])
}

func allFrameRowsBlank(rows [][]vt.Cell) bool {
	for _, row := range rows {
		if !isBlankFrameRow(row) {
			return false
		}
	}
	return true
}

func isBlankFrameRow(row []vt.Cell) bool {
	for _, cell := range row {
		if cell.Text != "" || cell.Cont || len(cell.SGR) > 0 {
			return false
		}
	}
	return true
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

func formatSnapshotCell(cell vt.Cell) string {
	return fmt.Sprintf("{Text:%q Cont:%t SGR:%q}", cell.Text, cell.Cont, cell.SGR)
}
