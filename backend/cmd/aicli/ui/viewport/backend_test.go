package viewport

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// gridFromFeed builds a [][]vt.Cell snapshot by replaying stream through the
// ui/vt screen model, so tests can stage realistic frames (wrapping, wide runes,
// SGR) without hand-authoring cells.
func gridFromFeed(width, height int, stream string) [][]vt.Cell {
	s := vt.NewScreen(width, height)
	s.Feed(stream)
	g := make([][]vt.Cell, height)
	for r := 1; r <= height; r++ {
		row := make([]vt.Cell, width)
		for c := 1; c <= width; c++ {
			row[c-1] = s.CellAt(r, c)
		}
		g[r-1] = row
	}
	return g
}

var cupPattern = regexp.MustCompile("\x1b\\[(\\d+);(\\d+)H")

// cupRows returns the set of 1-based rows addressed by CUP in a diff.
func cupRows(diff string) map[int]bool {
	rows := map[int]bool{}
	for _, m := range cupPattern.FindAllStringSubmatch(diff, -1) {
		r := 0
		for _, ch := range m[1] {
			r = r*10 + int(ch-'0')
		}
		rows[r] = true
	}
	return rows
}

// TestBackend_NoChangeEmitsNothing pins that a blank frame and a repeated frame
// both diff to an empty string (idempotent Flush).
func TestBackend_NoChangeEmitsNothing(t *testing.T) {
	b := New(20, 4)
	if got := b.Flush(); got != "" {
		t.Fatalf("blank->blank Flush should be empty, got %q", got)
	}
	frame := gridFromFeed(20, 4, "hello\nworld")
	b.StageFrame(frame)
	if got := b.Flush(); got == "" {
		t.Fatal("staging content should produce a non-empty diff")
	}
	b.StageFrame(frame)
	if got := b.Flush(); got != "" {
		t.Fatalf("restaging identical content should be empty, got %q", got)
	}
}

func TestBackend_InvalidateRepaintsBlankCells(t *testing.T) {
	b := New(4, 2)
	b.Invalidate()
	diff := b.Flush()
	if diff == "" {
		t.Fatal("invalidated blank frame must repaint")
	}
	screen := vt.NewScreen(4, 2)
	screen.Feed("stale\x1b[2;1Hold")
	screen.Feed(diff)
	if !screen.Blank(1) || !screen.Blank(2) {
		t.Fatalf("invalidated repaint did not clear stale cells\n%s", screen.Dump())
	}
	if got := b.Flush(); got != "" {
		t.Fatalf("invalidate should be consumed by one flush, got %q", got)
	}
}

// TestBackend_DiffBlankingUsesELNotSpaces guards the band/popup shrink path:
// clearing occupied cells must leave Text="" (via EL), not residual ' ' glyphs
// that would diverge from Compose's true blank cells.
func TestBackend_DiffBlankingUsesELNotSpaces(t *testing.T) {
	b := New(8, 2)
	b.StageFrame(gridFromFeed(8, 2, "active-1\nactive-2"))
	if diff := b.Flush(); diff == "" {
		t.Fatal("expected initial paint")
	}
	b.StageFrame(blankGrid(8, 2))
	diff := b.Flush()
	if diff == "" {
		t.Fatal("expected blanking diff")
	}
	if !strings.Contains(diff, "\x1b[K") {
		t.Fatalf("blanking diff must use EL, got %q", diff)
	}
	screen := vt.NewScreen(8, 2)
	screen.Feed("active-1\x1b[2;1Hactive-2")
	screen.Feed(diff)
	for row := 1; row <= 2; row++ {
		for col := 1; col <= 8; col++ {
			cell := screen.CellAt(row, col)
			if cell.Text != "" || cell.Cont || len(cell.SGR) > 0 {
				t.Fatalf("row %d col %d not true-blank after shrink: %+v\n%s", row, col, cell, screen.Dump())
			}
		}
	}
}

// TestBackend_RoundTripThroughVTScreen is the core P5.1 shadow validation:
// applying (blank->front) then (front->back) must equal (blank->back) on the
// ui/vt screen model, for both text and per-cell SGR.
func TestBackend_RoundTripThroughVTScreen(t *testing.T) {
	cases := []struct {
		name        string
		w, h        int
		front, back string
	}{
		{"blank_to_text", 20, 4, "", "hello\nworld"},
		{"text_change", 20, 4, "hello\nworld", "hallo\nworld"},
		{"append_row", 20, 4, "one\ntwo", "one\ntwo\nthree"},
		{"clear_middle_row", 20, 4, "one\ntwo\nthree", "one\n\nthree"},
		{"cjk_swap", 12, 3, "abc\n中文", "中文\nabc"},
		{"sgr_change", 20, 3, "\x1b[31mred\x1b[0m text", "\x1b[32mgrn\x1b[0m text"},
		{"scroll_like", 20, 5, "l1\nl2\nl3\nl4", "l2\nl3\nl4\nl5"},
		{"widen_narrow", 16, 3, "中文字", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			front := gridFromFeed(tc.w, tc.h, tc.front)
			back := gridFromFeed(tc.w, tc.h, tc.back)

			b := New(tc.w, tc.h)
			b.StageFrame(front)
			frontDiff := b.Flush()
			b.StageFrame(back)
			diff := b.Flush()

			replayed := vt.NewScreen(tc.w, tc.h)
			replayed.Feed(frontDiff)
			replayed.Feed(diff)

			tb := New(tc.w, tc.h)
			tb.StageFrame(back)
			target := vt.NewScreen(tc.w, tc.h)
			target.Feed(tb.Flush())

			if replayed.Dump() != target.Dump() {
				t.Fatalf("front->back diff diverged from blank->back\nreplayed:\n%s\ntarget:\n%s", replayed.Dump(), target.Dump())
			}
			// Content fidelity against the original stream.
			orig := vt.NewScreen(tc.w, tc.h)
			orig.Feed(tc.back)
			if replayed.Dump() != orig.Dump() {
				t.Fatalf("replayed frame != original back stream\nreplayed:\n%s\norig:\n%s", replayed.Dump(), orig.Dump())
			}
			for r := 1; r <= tc.h; r++ {
				if got, want := replayed.RowSGRCodes(r), target.RowSGRCodes(r); !reflect.DeepEqual(got, want) {
					t.Fatalf("row %d SGR mismatch: got %v want %v", r, got, want)
				}
			}
		})
	}
}

// TestBackend_SingleRowChangeIsLocal pins diff locality: changing one row must
// address only that row (no full-frame repaint).
func TestBackend_SingleRowChangeIsLocal(t *testing.T) {
	const w, h = 20, 5
	front := gridFromFeed(w, h, "a\nb\nc\nd\ne")
	back := gridFromFeed(w, h, "a\nb\nX\nd\ne")

	b := New(w, h)
	b.StageFrame(front)
	_ = b.Flush()
	b.StageFrame(back)
	diff := b.Flush()

	rows := cupRows(diff)
	if !rows[3] || len(rows) != 1 {
		t.Fatalf("expected diff to address only row 3, got rows %v (diff %q)", rows, diff)
	}
}

// TestBackend_ResizeRepaints pins that Resize clears the front buffer so the
// next Flush repaints the whole frame.
func TestBackend_ResizeRepaints(t *testing.T) {
	b := New(10, 4)
	b.StageFrame(gridFromFeed(10, 4, "hi"))
	_ = b.Flush()

	b.Resize(12, 5)
	if w, h := b.Size(); w != 12 || h != 5 {
		t.Fatalf("Size after resize = %dx%d, want 12x5", w, h)
	}
	b.StageFrame(gridFromFeed(12, 5, "hi"))
	if got := b.Flush(); got == "" {
		t.Fatal("after resize the next Flush must repaint (non-empty), got empty")
	}
}
