package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestBottomReserveShrinkCompensationDrawsBlanksAtTop characterizes a KNOWN,
// still-open defect in the immediate-mode bottom-reserve compensation: with the
// output region full of history, growing the reserve scrolls the top history
// up/off (irreversible into scrollback) and shrinking it then scrolls the region
// down (SD), painting blank rows at the SCREEN TOP over where history used to be.
//
// This cannot be fixed by tweaking the compensation in place: the shrink SD is
// load-bearing for the "no blank gap" / "ActiveBand is layout neutral" / live-vs
// -replay parity invariants (removing it regresses those suites). The correct
// fix is the owned viewport, which retains history and re-renders it — proven in
// viewport/TestCompose_GrowShrinkKeepsHistoryAnchored. This test stays RED-in
// spirit (asserts the current buggy output) until the owned viewport lands, at
// which point it is inverted to "top must not be blanked".
func TestBottomReserveShrinkCompensationDrawsBlanksAtTop(t *testing.T) {
	const width, height = 20, 6

	screen := vt.NewScreen(width, height)
	// Fill the output region (rows 1..5) with L1..L5.
	screen.Feed("L1\r\nL2\r\nL3\r\nL4\r\nL5")
	if got := strings.TrimSpace(screen.Line(1)); got != "L1" {
		t.Fatalf("precondition: row1=%q want L1\n%s", got, screen.Dump())
	}

	// Band grows 1 -> 3: the top history scrolls up out of the region.
	var grow strings.Builder
	appendOutputScrollUpForBottomReserveGrowthSequence(&grow, height, 1, 3)
	screen.Feed(grow.String())

	// Band shrinks 3 -> 1: deferred scroll-down compensation of 2 rows.
	var shrink strings.Builder
	appendOutputScrollDownForBottomReserveShrinkSequence(&shrink, height, 1, 2)
	screen.Feed(shrink.String())

	t.Logf("after grow+shrink:\n%s", screen.Dump())

	// Current (buggy) behavior: two blank compensation rows at the top, L1/L2 gone.
	if strings.TrimSpace(screen.Line(1)) != "" || strings.TrimSpace(screen.Line(2)) != "" {
		t.Fatalf("expected the repro to show blank rows painted at the top; screen:\n%s", screen.Dump())
	}
	if strings.Contains(screen.Dump(), "L1") || strings.Contains(screen.Dump(), "L2") {
		t.Fatalf("expected L1/L2 to have scrolled off; screen:\n%s", screen.Dump())
	}
}
