package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestFixedBottomSurface_HistoryWindowCapturesCommittedLines pins the P5.2b/P5.3
// foundation: committed scrollback writes are captured as logical lines.
func TestFixedBottomSurface_HistoryWindowCapturesCommittedLines(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "L1\nL2\nL3\n")
	})
	got := surface.HistoryWindowForTest()
	if len(got) != 3 || got[0] != "L1" || got[1] != "L2" || got[2] != "L3" {
		t.Fatalf("history window = %#v", got)
	}
}

// TestFixedBottomSurface_HistoryWindowCoalescesPartialLines pins that streaming
// fragments (no trailing newline) continue the same logical line rather than
// creating spurious breaks.
func TestFixedBottomSurface_HistoryWindowCoalescesPartialLines(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "foo")
		surface.WriteOutput(io.Discard, "bar\n")
		surface.WriteOutput(io.Discard, "baz\n")
	})
	got := surface.HistoryWindowForTest()
	if want := []string{"foobar", "baz"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history window = %#v want %#v", got, want)
	}
}

// TestFixedBottomSurface_HistoryWindowBounds pins that the window is bounded and
// keeps the most recent lines.
func TestFixedBottomSurface_HistoryWindowBounds(t *testing.T) {
	surface := newTestFixedBottomSurface()
	captureUIStdout(t, func() {
		for i := 0; i < historyWindowMaxLines+50; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	got := surface.HistoryWindowForTest()
	if len(got) != historyWindowMaxLines {
		t.Fatalf("expected bounded to %d, got %d", historyWindowMaxLines, len(got))
	}
	if got[len(got)-1] != fmt.Sprintf("line-%d", historyWindowMaxLines+50-1) {
		t.Fatalf("last retained history line = %q", got[len(got)-1])
	}
	if got[0] != fmt.Sprintf("line-%d", 50) {
		t.Fatalf("first retained history line = %q", got[0])
	}
}
