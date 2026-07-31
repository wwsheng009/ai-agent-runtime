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

// TestFixedBottomSurface_HistoryWindowBounds pins that the window is bounded
// (hard cap historyWindowMaxLines, soft bound visible+headroom) and keeps the
// most recent lines. Excess is handed off to native scrollback via
// insertHistoryLines before the hard cap trims.
func TestFixedBottomSurface_HistoryWindowBounds(t *testing.T) {
	surface := newTestFixedBottomSurface()
	total := historyWindowMaxLines + 50
	captureUIStdout(t, func() {
		for i := 0; i < total; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	got := surface.HistoryWindowForTest()
	if len(got) > historyWindowMaxLines {
		t.Fatalf("exceeded hard cap %d, got %d", historyWindowMaxLines, len(got))
	}
	if len(got) == 0 {
		t.Fatalf("history window empty after %d writes", total)
	}
	// Soft bound keeps visible + headroom; the last retained line must be the
	// most recent write.
	if got[len(got)-1] != fmt.Sprintf("line-%d", total-1) {
		t.Fatalf("last retained history line = %q", got[len(got)-1])
	}
	// First retained should be the oldest still in the window (total - len).
	wantFirst := total - len(got)
	if got[0] != fmt.Sprintf("line-%d", wantFirst) {
		t.Fatalf("first retained history line = %q want line-%d", got[0], wantFirst)
	}
}

// TestFixedBottomSurface_OverflowHistoryHandsOffToScrollback pins that when
// history exceeds the visible output region, the oldest lines are inserted into
// native scrollback (via insertHistoryLines) once. The window dual-retains up to
// visible+headroom for shrink restore. This fixes "off-screen history missing
// on scroll" — handoff must not wait for the headroom soft bound.
func TestFixedBottomSurface_OverflowHistoryHandsOffToScrollback(t *testing.T) {
	// Use a small terminal so visible+headroom is modest and we exceed it.
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	output := captureUIStdout(t, func() {
		for i := 0; i < 100; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	// Correct handoff uses the Codex-aligned path (region 1..outputBottom + \r\n + line),
	// NOT CSI T (Scroll Down) which never enters native scrollback.
	if !strings.Contains(output, "\x1b[1;") || !strings.Contains(output, "r") {
		snippet := output
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		t.Fatalf("expected DECSTBM handoff sequence, got %q", snippet)
	}
	if !strings.Contains(output, "\r\n") {
		t.Fatalf("expected Codex-style \\r\\n before history lines in handoff")
	}
	// Fail only on genuine CSI nT scroll-down (not letter T in plain text).
	if strings.Contains(output, "\x1b[1T") || strings.Contains(output, "\x1b[2T") ||
		strings.Contains(output, terminalScrollDownSequence(1)) {
		t.Fatalf("handoff must not use CSI T scroll-down (does not enter scrollback)")
	}
	if !strings.Contains(output, terminalResetScrollRegionSequence(20)) {
		t.Fatalf("expected scroll-region reset after handoff")
	}
	// Soft bound keeps visible + headroom; hard cap is a safety net.
	history := surface.HistoryWindowForTest()
	if len(history) > historyWindowMaxLines {
		t.Fatalf("window exceeded hard cap %d, got %d", historyWindowMaxLines, len(history))
	}
	if len(history) == 0 {
		t.Fatalf("history window empty after overflow writes")
	}
	// Last retained must be the most recent write.
	if history[len(history)-1] != "line-99" {
		t.Fatalf("last retained = %q want line-99", history[len(history)-1])
	}
	// Soft bound: window should not grow past visible+headroom once overflowed.
	visible := surface.visibleOutputRowsForTest()
	keep := visible + historyWindowHeadroom
	if keep > historyWindowMaxLines {
		keep = historyWindowMaxLines
	}
	if len(history) > keep {
		t.Fatalf("window len=%d exceeds soft keep=%d (visible=%d + headroom)", len(history), keep, visible)
	}
}

// TestFixedBottomSurface_OffScreenHistoryHandsOffBeforeHeadroom pins the live
// bug: lines that leave the visible output region must enter native scrollback
// immediately, even when total history is still within visible+headroom.
// Previously handoff waited until visible+40, so typical sessions never put
// anything into host scrollback and "scroll up" showed nothing.
func TestFixedBottomSurface_OffScreenHistoryHandsOffBeforeHeadroom(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	surface.ShowPrompt("> ")
	visible := surface.visibleOutputRowsForTest()
	if visible < 4 {
		t.Fatalf("visible output rows too small: %d", visible)
	}
	// Stay under visible+headroom so the old soft-bound path would never hand off,
	// but exceed visible so the eager path must.
	total := visible + 8
	if total >= visible+historyWindowHeadroom {
		total = visible + historyWindowHeadroom - 1
	}
	output := captureUIStdout(t, func() {
		for i := 0; i < total; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("offscreen-%d\n", i))
		}
	})
	if !strings.Contains(output, "\x1b[1;") || !strings.Contains(output, "\r\n") {
		t.Fatalf("expected Codex-aligned DECSTBM handoff once history exceeded visible=%d (total=%d); no handoff sequence", visible, total)
	}
	// Oldest off-screen line must appear in the insert stream.
	if !strings.Contains(output, "offscreen-0") {
		t.Fatalf("expected oldest off-screen line in scrollback handoff output; total=%d visible=%d", total, visible)
	}
	history := surface.HistoryWindowForTest()
	if len(history) != total {
		// Dual-retain: still within keepForRestore, so window keeps all lines.
		t.Fatalf("expected dual-retain window len=%d, got %d", total, len(history))
	}
	if got := surface.HistoryHandedOffForTest(); got != total-visible {
		t.Fatalf("historyHandedOff=%d want %d (total-visible)", got, total-visible)
	}
}

// TestFixedBottomSurface_ActiveBandGrowthHandsNewlyHiddenHistoryToScrollback
// pins the reserve-growth variant of the same contract. A transient ActiveBand
// and its semantic top gap reduce the visible output region; rows displaced by
// that geometry change must enter host scrollback immediately instead of
// disappearing until the band is cleared.
func TestFixedBottomSurface_ActiveBandGrowthHandsNewlyHiddenHistoryToScrollback(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	})
	visibleBefore := surface.visibleOutputRowsForTest()
	if visibleBefore < 4 {
		t.Fatalf("visible output rows too small: %d", visibleBefore)
	}
	captureUIStdout(t, func() {
		for i := 0; i < visibleBefore; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("reserve-%d\n", i))
		}
	})
	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("precondition: historyHandedOff=%d want 0", got)
	}

	output := captureUIStdout(t, func() {
		surface.SetActiveBand([]string{"• Running grep"})
	})
	visibleAfter := surface.visibleOutputRowsForTest()
	displaced := visibleBefore - visibleAfter
	if displaced != 2 {
		t.Fatalf("ActiveBand row + semantic gap displaced %d rows, want 2", displaced)
	}
	if got := surface.HistoryHandedOffForTest(); got != displaced {
		t.Fatalf("historyHandedOff=%d want displaced=%d", got, displaced)
	}
	for i := 0; i < displaced; i++ {
		if want := fmt.Sprintf("reserve-%d", i); !strings.Contains(output, want) {
			t.Fatalf("newly hidden history %q was not handed to scrollback", want)
		}
	}

	captureUIStdout(t, func() {
		surface.ClearActiveBand()
	})
	frame := frameDump(surface.ComposedFrameForTest())
	if !strings.Contains(frame, "reserve-0") {
		t.Fatalf("dual-retained history did not return after ActiveBand shrink:\n%s", frame)
	}
}

// TestFixedBottomSurface_OwnedPathPaintsVisibleHistory pins that committed
// WriteOutput lines appear in the owned frame's output region (not only the
// bottom band). This is the "history still not visible" regression.
func TestFixedBottomSurface_OwnedPathPaintsVisibleHistory(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.WriteOutput(io.Discard, "alpha-line\n")
		surface.WriteOutput(io.Discard, "beta-line\n")
		surface.WriteOutput(io.Discard, "gamma-line\n")
	})
	frame := frameDump(surface.ComposedFrameForTest())
	if frame == "" {
		t.Fatal("composed frame empty")
	}
	for _, want := range []string{"alpha-line", "beta-line", "gamma-line"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("owned frame missing visible history %q; frame=%q", want, frame)
		}
	}
}

// TestFixedBottomSurface_OwnedPathKeepsRecentHistoryAfterOverflow pins that
// after soft-bound handoff, the newest lines remain in the owned window and
// still paint on the composed frame.
func TestFixedBottomSurface_OwnedPathKeepsRecentHistoryAfterOverflow(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
		for i := 0; i < 100; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	history := surface.HistoryWindowForTest()
	if len(history) == 0 {
		t.Fatal("history window empty after overflow")
	}
	if history[len(history)-1] != "line-99" {
		t.Fatalf("last history = %q want line-99", history[len(history)-1])
	}
	frame := frameDump(surface.ComposedFrameForTest())
	// Newest retained lines must still be on the owned screen.
	for _, want := range history[len(history)-3:] {
		if !strings.Contains(frame, want) {
			t.Fatalf("composed frame missing recent history %q after overflow; history len=%d", want, len(history))
		}
	}
}
