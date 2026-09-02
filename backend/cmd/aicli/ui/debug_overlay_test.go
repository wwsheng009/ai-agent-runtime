package ui

import (
	"strconv"
	"strings"
	"testing"
)

func TestDebugOverlayWrapBody(t *testing.T) {
	lines := wrapDebugOverlayBody([]string{
		"alpha beta gamma",
		"",
		"delta",
	}, 8)
	// Content width is width-3 (2 indent + 1 scrollbar column) = 5, so
	// "alpha beta gamma" hard-wraps as ["alpha", " beta", " gamm", "a"].
	if len(lines) != 6 {
		t.Fatalf("wrapped lines=%d want 6: %#v", len(lines), lines)
	}
	if lines[0] != "alpha" || lines[3] != "a" || lines[4] != "" || lines[5] != "delta" {
		t.Fatalf("unexpected wrap: %#v", lines)
	}
}

// TestDebugOverlayInfoGlyphWidth guards the root cause of the jumbled debug
// overlay: the ℹ️ glyph (U+2139+U+FE0F) renders as 2 terminal columns, so the
// width used for padding/wrapping must count it as 2. Under-counting it by one
// pushed every padded body row one column past the right edge, so the terminal
// soft-wrapped the scrollbar mid-row and merged it with the next row's text.
func TestDebugOverlayInfoGlyphWidth(t *testing.T) {
	if got := DisplayWidth("ℹ️"); got != 2 {
		t.Fatalf("DisplayWidth(\"ℹ️\")=%d want 2 (emoji presentation is 2 columns)", got)
	}
	if got := DisplayWidth("  ℹ️ Stream:    on"); got != 18 {
		t.Fatalf("DisplayWidth(row)=%d want 18", got)
	}
}

// TestDebugOverlayWideGlyphAlignment verifies every rendered body row has
// DisplayWidth exactly == terminal width (scrollbar pinned to last column),
// even with wide glyphs like ℹ️ and full-width separators.
func TestDebugOverlayWideGlyphAlignment(t *testing.T) {
	sep := strings.Repeat("═", 110)
	body := strings.Join([]string{
		sep,
		"ℹ️ Provider:  ( codex_ee )",
		"ℹ️ Model:     gpt-5.6-codex",
		"ℹ️ Stream:    on",
		"ℹ️ Fast:      off",
		"ℹ️ Reasoning: enabled",
		sep,
		"Runtime Core:      session_actor contract=v1",
		"Reasoning Effort:  max",
		"cell-0 [assistant] partial streamed body text",
		"cell-1 [tool] tool output line",
		"cell-2 [user] follow-up message",
		"",
	}, "\n")

	// height=8 → viewportRows=6 < totalRows, so the scrollbar is active and
	// every body row is padded to width-1 then gets the scrollbar glyph.
	width, height := 110, 8
	frame := renderDebugOverlayFrame("调试信息", strings.Split(body, "\n"), 0, width, height)
	rows := strings.Split(frame, "\r\n")
	for i, row := range rows {
		row = strings.TrimPrefix(row, "\x1b[2K")
		if i == 0 || i == len(rows)-1 {
			continue
		}
		if got := DisplayWidth(row); got != width {
			t.Errorf("row %d: DisplayWidth=%d want %d: %q", i, got, width, row)
		}
		runes := []rune(row)
		last := runes[len(runes)-1]
		if last != '█' && last != '░' {
			t.Errorf("row %d: last char must be a scrollbar glyph, got %q: %q", i, string(last), row)
		}
	}
}

// TestDebugOverlaySeparatorNoRemnant verifies a full-width separator does not
// wrap into a 3-column sliver row.
func TestDebugOverlaySeparatorNoRemnant(t *testing.T) {
	sep := strings.Repeat("═", 110)
	wrapped := wrapDebugOverlayBody([]string{sep}, 110)
	if len(wrapped) != 1 {
		t.Fatalf("separator wrapped into %d rows, want 1 (no sliver remnant): %q", len(wrapped), wrapped)
	}
	if got := DisplayWidth(wrapped[0]); got != 107 {
		t.Fatalf("separator width=%d want content width 107", got)
	}
}

func TestDebugOverlayScrollbarCells(t *testing.T) {
	if cells := debugOverlayScrollbarCells(3, 8, 0); cells != nil {
		t.Fatalf("content fitting viewport must not draw a scrollbar, got %#v", cells)
	}
	if cells := debugOverlayScrollbarCells(8, 8, 0); cells != nil {
		t.Fatalf("content exactly fitting viewport must not draw a scrollbar, got %#v", cells)
	}

	// 12 rows in an 8-row viewport: thumb size = 64/12 = 5, so it can slide
	// across starts 0..3.
	cells := debugOverlayScrollbarCells(12, 8, 0)
	if len(cells) != 8 {
		t.Fatalf("scrollbar cells=%d want 8", len(cells))
	}
	for index, cell := range cells {
		want := "░"
		if index < 5 {
			want = "█"
		}
		if cell != want {
			t.Fatalf("offset=0 cell[%d]=%q want %q", index, cell, want)
		}
	}

	bottom := debugOverlayScrollbarCells(12, 8, 4) // max offset = 12-8
	for index, cell := range bottom {
		want := "░"
		if index >= 3 {
			want = "█"
		}
		if cell != want {
			t.Fatalf("offset=max cell[%d]=%q want %q", index, cell, want)
		}
	}

	middle := debugOverlayScrollbarCells(12, 8, 2)
	if middle[0] != "░" || middle[7] != "░" || middle[3] != "█" {
		t.Fatalf("offset=middle scrollbar unexpected: %#v", middle)
	}
}

func TestDebugOverlayApplyKey(t *testing.T) {
	total := 50
	for _, tc := range []struct {
		name     string
		key      editorKey
		offset   int
		wantMove bool
		wantPos  int
	}{
		{name: "down", key: editorKey{kind: editorKeyDown}, offset: 0, wantMove: true, wantPos: 1},
		{name: "up-clamped", key: editorKey{kind: editorKeyUp}, offset: 0, wantMove: false, wantPos: 0},
		{name: "j", key: editorKey{kind: editorKeyRune, r: 'j'}, offset: 2, wantMove: true, wantPos: 3},
		{name: "k", key: editorKey{kind: editorKeyRune, r: 'k'}, offset: 2, wantMove: true, wantPos: 1},
		{name: "home", key: editorKey{kind: editorKeyHome}, offset: 9, wantMove: true, wantPos: 0},
	} {
		next := applyDebugOverlayKey(tc.offset, total, 24, tc.key)
		if (next != tc.offset) != tc.wantMove {
			t.Fatalf("%s: moved=%v want %v", tc.name, next != tc.offset, tc.wantMove)
		}
		if next != tc.wantPos {
			t.Fatalf("%s: offset=%d want %d", tc.name, next, tc.wantPos)
		}
	}

	// end (G) clamps to the last visible page.
	end := applyDebugOverlayKey(0, total, 24, editorKey{kind: editorKeyRune, r: 'G'})
	wantEnd := total - debugOverlayViewportRows(24)
	if end != wantEnd {
		t.Fatalf("G offset=%d want %d", end, wantEnd)
	}
}

func TestDebugOverlayKeyCloses(t *testing.T) {
	for _, key := range []editorKey{
		{kind: editorKeyCancelPopup},
		{kind: editorKeyInterrupt},
		{kind: editorKeyEOF},
		{kind: editorKeyTranspose},
		{kind: editorKeyEnter},
		{kind: editorKeyRune, r: 'q'},
		{kind: editorKeyRune, r: 'Q'},
	} {
		if !debugOverlayKeyCloses(key) {
			t.Fatalf("key %+v should close the overlay", key)
		}
	}
	if debugOverlayKeyCloses(editorKey{kind: editorKeyDown}) {
		t.Fatal("scroll key must not close the overlay")
	}
	if debugOverlayKeyCloses(editorKey{kind: editorKeyRune, r: 'j'}) {
		t.Fatal("j must not close the overlay")
	}
}

func TestDebugOverlayRenderFrame(t *testing.T) {
	body := strings.Join([]string{
		"line one",
		"line two",
		"line three",
	}, "\n")
	frame := renderDebugOverlayFrame("调试信息", strings.Split(body, "\n"), 0, 40, 10)
	if !strings.Contains(frame, "Debug") || !strings.Contains(frame, "调试信息") {
		t.Fatalf("frame missing header:\n%q", frame)
	}
	if !strings.Contains(frame, "line two") {
		t.Fatalf("frame missing body line:\n%q", frame)
	}
	if !strings.Contains(frame, "q 或 Esc 关闭") {
		t.Fatalf("frame missing dismiss hint:\n%q", frame)
	}
	if !strings.HasPrefix(frame, "\x1b[H") {
		t.Fatalf("frame must reset cursor to home first:\n%q", frame)
	}

	// Body rows must preserve alignment spaces (fitFullScreenText would
	// collapse them); header/footer labels are normalized single-line text.
	aligned := renderDebugOverlayFrame("调试信息", []string{"Provider:  openai", "  indented"}, 0, 40, 10)
	if !strings.Contains(aligned, "Provider:  openai") {
		t.Fatalf("body row lost alignment spaces:\n%q", aligned)
	}
	if !strings.Contains(aligned, "  indented") {
		t.Fatalf("body row lost indentation:\n%q", aligned)
	}

	// Scrolled frame must not show the first line. Use a body tall enough
	// that offset 1 is a valid scroll position (viewport is height-2 rows).
	longBody := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		longBody = append(longBody, "row "+strconv.Itoa(i))
	}
	scrolled := renderDebugOverlayFrame("调试信息", longBody, 1, 40, 10)
	if strings.Contains(scrolled, "row 1") {
		t.Fatalf("scrolled frame still shows row 1:\n%q", scrolled)
	}
	if !strings.Contains(scrolled, "row 2") {
		t.Fatalf("scrolled frame missing row 2:\n%q", scrolled)
	}

	// A tall body draws a right-edge scrollbar whose thumb tracks the offset.
	top := renderDebugOverlayFrame("调试信息", longBody, 0, 40, 10)
	if !strings.Contains(top, "█") || !strings.Contains(top, "░") {
		t.Fatalf("scrollable frame missing scrollbar glyphs:\n%q", top)
	}
	bodyRows := strings.Split(top, "\r\n")
	// The scrollbar must sit in the terminal's rightmost column on every body
	// row: each row's visible width is exactly the terminal width and its last
	// character is a scrollbar glyph, regardless of the text length.
	for rowIndex := 1; rowIndex <= 8; rowIndex++ {
		row := strings.TrimPrefix(bodyRows[rowIndex], "\x1b[2K")
		if got := DisplayWidth(row); got != 40 {
			t.Fatalf("body row %d display width=%d want 40 (scrollbar must be pinned to the right edge): %q", rowIndex, got, row)
		}
		last := []rune(row)[len([]rune(row))-1]
		if last != '█' && last != '░' {
			t.Fatalf("body row %d must end with a scrollbar glyph, got %q: %q", rowIndex, string(last), row)
		}
	}
	firstBody := bodyRows[1]
	if !strings.HasSuffix(firstBody, "█") {
		t.Fatalf("offset=0 thumb must sit at the top of the track, first body row=%q", firstBody)
	}
	bottom := renderDebugOverlayFrame("调试信息", longBody, 4, 40, 10)
	lastBody := strings.Split(bottom, "\r\n")[8]
	if !strings.HasSuffix(lastBody, "█") {
		t.Fatalf("offset=max thumb must sit at the bottom of the track, last body row=%q", lastBody)
	}
}
