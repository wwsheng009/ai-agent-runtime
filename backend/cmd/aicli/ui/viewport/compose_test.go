package viewport

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

func lineCells(width int, text string) []vt.Cell {
	return gridFromFeed(width, 1, text)[0]
}

// TestCompose_GrowShrinkKeepsHistoryAnchored is the P5.2 proof-of-fix: it mirrors
// the immediate-mode defect scenario (ui/TestBottomReserveShrinkCompensationDrawsBlanksAtTop)
// through the owned viewport. Growing the bottom reserve hides the oldest history
// rows; shrinking it restores them with no blank rows painted at the top.
func TestCompose_GrowShrinkKeepsHistoryAnchored(t *testing.T) {
	const w, h = 20, 6
	history := [][]vt.Cell{
		lineCells(w, "L1"), lineCells(w, "L2"), lineCells(w, "L3"),
		lineCells(w, "L4"), lineCells(w, "L5"),
	}
	status := [][]vt.Cell{lineCells(w, "STATUS")}
	band3 := [][]vt.Cell{lineCells(w, "B1"), lineCells(w, "B2"), lineCells(w, "STATUS")}

	b := New(w, h)
	screen := vt.NewScreen(w, h)

	// Frame A: reserve = 1 row. Output region shows L1..L5.
	b.StageFrame(Compose(w, h, history, status))
	screen.Feed(b.Flush())
	if got := strings.TrimSpace(screen.Line(1)); got != "L1" {
		t.Fatalf("frame A: row1=%q want L1\n%s", got, screen.Dump())
	}

	// Frame B: reserve grows to 3 rows. Only the last 3 history rows fit.
	b.StageFrame(Compose(w, h, history, band3))
	screen.Feed(b.Flush())
	if strings.Contains(screen.Dump(), "L1") || strings.Contains(screen.Dump(), "L2") {
		t.Fatalf("frame B: oldest rows must be hidden while the band is tall\n%s", screen.Dump())
	}

	// Frame C: reserve shrinks back to 1 row. L1..L5 must be restored, with no
	// blank compensation at the top — the exact defect the old path produced.
	b.StageFrame(Compose(w, h, history, status))
	screen.Feed(b.Flush())
	for i, want := range []string{"L1", "L2", "L3", "L4", "L5", "STATUS"} {
		if got := strings.TrimSpace(screen.Line(i + 1)); got != want {
			t.Fatalf("frame C row %d = %q want %q (history not restored / top blanked)\n%s",
				i+1, got, want, screen.Dump())
		}
	}
}

// TestCompose_WindowSelectsRecentHistory pins the visible-window selection: when
// history exceeds the output region, the most recent rows show top-aligned and
// adjacent to the reserve.
func TestCompose_WindowSelectsRecentHistory(t *testing.T) {
	const w, h = 10, 4
	history := [][]vt.Cell{
		lineCells(w, "a"), lineCells(w, "b"), lineCells(w, "c"),
		lineCells(w, "d"), lineCells(w, "e"),
	}
	status := [][]vt.Cell{lineCells(w, "s")}

	b := New(w, h)
	b.StageFrame(Compose(w, h, history, status))
	screen := vt.NewScreen(w, h)
	screen.Feed(b.Flush())

	for i, want := range []string{"c", "d", "e", "s"} {
		if got := strings.TrimSpace(screen.Line(i + 1)); got != want {
			t.Fatalf("row %d=%q want %q\n%s", i+1, got, want, screen.Dump())
		}
	}
}

// TestCompose_ShortHistoryLeavesHeadroomAboveReserve pins that history shorter
// than the output region is top-aligned, leaving blank headroom just above the
// reserve (never blank rows above the content).
func TestCompose_ShortHistoryLeavesHeadroomAboveReserve(t *testing.T) {
	const w, h = 10, 5
	history := [][]vt.Cell{lineCells(w, "one"), lineCells(w, "two")}
	status := [][]vt.Cell{lineCells(w, "s")}

	b := New(w, h)
	b.StageFrame(Compose(w, h, history, status))
	screen := vt.NewScreen(w, h)
	screen.Feed(b.Flush())

	for i, want := range []string{"one", "two", "", "", "s"} {
		if got := strings.TrimSpace(screen.Line(i + 1)); got != want {
			t.Fatalf("row %d=%q want %q\n%s", i+1, got, want, screen.Dump())
		}
	}
}
