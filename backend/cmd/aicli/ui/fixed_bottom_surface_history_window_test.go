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

// TestFixedBottomSurface_StreamingAppendDoesNotRepaintHistory pins that the
// log-style append path (history exceeding the visible region) emits only the
// handoff scroll plus the bottom-pane delta on each write, never a full-screen
// repaint of the retained history rows. The previous behavior invalidated the
// viewport backend inside insertHistoryLinesLocked on every handoff, which
// forced the next Flush to re-emit every history row on every streaming write.
func TestFixedBottomSurface_StreamingAppendDoesNotRepaintHistory(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	// Exceed the visible output region so further writes take the direct-scroll
	// append path (log-style natural scrolling) instead of full-frame recompose.
	captureUIStdout(t, func() {
		for i := 0; i < 30; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	output := captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "line-30\n")
	})
	if strings.Contains(output, "\x1b[1;1H") {
		t.Fatalf("streaming append repainted history row 1 (full-screen redraw): %q", output)
	}
	if !strings.Contains(output, "line-30") {
		t.Fatalf("new history row missing from append output: %q", output)
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

// TestFixedBottomSurface_ActiveBandGrowthDoesNotCreateHistoryHandoff pins the
// migration invariant: geometry changes only alter the viewport. They must not
// append history into native scrollback; Phase 4 finalization will enqueue a
// typed HistoryCommit instead.
func TestFixedBottomSurface_ActiveBandGrowthDoesNotCreateHistoryHandoff(t *testing.T) {
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
	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("geometry growth advanced history handoff to %d, want 0", got)
	}
	// Full-frame repaint may legitimately restage retained transcript rows.
	// A native scrollback handoff has the distinct HandoffPlan prefix below;
	// geometry must not emit that effect protocol.
	if strings.Contains(output, "\x1b[s\x1b[1;") {
		t.Fatalf("geometry growth emitted native scrollback handoff bytes: %q", output)
	}
	if got := surface.HistoryWindowForTest(); len(got) != visibleBefore {
		t.Fatalf("geometry growth trimmed retained history to %d, want %d", len(got), visibleBefore)
	}

	captureUIStdout(t, func() {
		surface.ClearActiveBand()
	})
	frame := frameDump(surface.ComposedFrameForTest())
	if !strings.Contains(frame, "reserve-0") {
		t.Fatalf("dual-retained history did not return after ActiveBand shrink:\n%s", frame)
	}
}

func TestFixedBottomSurface_SoftPartialWritesCoalesceBeforeRewrite(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	captureUIStdout(t, func() {
		if _, err, ok := surface.WriteSoftTrackedOutput(io.Discard, "foo"); !ok || err != nil {
			t.Fatalf("first soft write: ok=%t err=%v", ok, err)
		}
		if _, err, ok := surface.WriteSoftTrackedOutput(io.Discard, "bar\n"); !ok || err != nil {
			t.Fatalf("second soft write: ok=%t err=%v", ok, err)
		}
	})
	if got := surface.HistoryWindowForTest(); len(got) != 1 || got[0] != "foobar" {
		t.Fatalf("history=%q want [foobar]", got)
	}
	if got := surface.SoftOutputTailLines(); len(got) != 1 || got[0] != "foobar" {
		t.Fatalf("soft tail=%q want [foobar]", got)
	}

	captureUIStdout(t, func() {
		if !surface.RewriteSoftOutputTail(io.Discard, []string{"replacement"}) {
			t.Fatal("coalesced soft suffix should remain rewriteable")
		}
	})
	if got := surface.HistoryWindowForTest(); len(got) != 1 || got[0] != "replacement" {
		t.Fatalf("rewritten history=%q want [replacement]", got)
	}
}

func TestFixedBottomSurface_RejectsBogusSoftSuffixWithoutDestroyingHistory(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	captureUIStdout(t, func() {
		if _, err, ok := surface.WriteSoftTrackedOutput(io.Discard, "committed\n"); !ok || err != nil {
			t.Fatalf("soft write: ok=%t err=%v", ok, err)
		}
	})
	before := strings.Join(surface.HistoryWindowForTest(), "\n")

	surface.AdoptSoftOutputTail([]string{"bogus"})
	if surface.SoftOutputTailValid() {
		t.Fatal("non-suffix adoption must not create rewrite ownership")
	}
	if surface.RewriteSoftOutputTail(io.Discard, []string{"replacement"}) {
		t.Fatal("rewrite must fail after non-suffix adoption")
	}
	if got := strings.Join(surface.HistoryWindowForTest(), "\n"); got != before {
		t.Fatalf("failed soft validation mutated history: got %q want %q", got, before)
	}
	if frame := frameDump(surface.ComposedFrameForTest()); !strings.Contains(frame, "committed") {
		t.Fatalf("committed history disappeared from composed frame:\n%s", frame)
	}
}

// TestFixedBottomSurface_ActiveBandGrowthDoesNotHandOffSoftHistory pins the
// Phase 1 contract (D2): ActiveBand growth must not trigger a text handoff.
// Displaced rows stay in the model so band shrink restores them by repaint;
// the soft rewrite window therefore never crosses the committed boundary and
// keeps its ownership.
func TestFixedBottomSurface_ActiveBandGrowthDoesNotHandOffSoftHistory(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	})
	visible := surface.visibleOutputRowsForTest()
	captureUIStdout(t, func() {
		for i := 0; i < visible; i++ {
			if _, err, ok := surface.WriteSoftTrackedOutput(io.Discard, fmt.Sprintf("soft-%d\n", i)); !ok || err != nil {
				t.Fatalf("soft write %d: ok=%t err=%v", i, ok, err)
			}
		}
	})
	if !surface.SoftOutputTailValid() {
		t.Fatal("precondition: visible soft history should still be rewriteable")
	}

	captureUIStdout(t, func() {
		surface.SetActiveBand([]string{"• Running grep"})
	})
	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("ActiveBand growth handed history to scrollback: frontier=%d want 0 (D2: displaced rows stay in the model)", got)
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("no committed boundary was crossed, soft rewrite ownership must stay valid")
	}
}

// TestFixedBottomSurface_WrappedHistoryHandsOffViaPhysicalExpansion pins the
// refactor contract: wrapped logical lines must reach native scrollback as
// expanded physical rows (one terminal row per emitted line) instead of
// stalling the whole handoff boundary. Previously a wrapped line anywhere in
// the retained window froze handoff at 0, leaving history neither on screen
// nor in scrollback and growing the window without bound.
func TestFixedBottomSurface_WrappedHistoryHandsOffViaPhysicalExpansion(t *testing.T) {
	const width, height = 12, 20
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	})
	visible := surface.visibleOutputRowsForTest()
	total := visible + 8
	longLine := strings.Repeat("x", width+5)

	output := captureUIStdout(t, func() {
		for i := 0; i < total; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("%s-%02d\n", longLine, i))
		}
	})

	history := surface.HistoryWindowForTest()
	if len(surface.HistoryRowsSnapshot()) <= len(history) {
		t.Fatalf("precondition: wrapped history has physical=%d logical=%d", len(surface.HistoryRowsSnapshot()), len(history))
	}
	// Wrapped segment must advance the logical boundary just like a plain one.
	if got := surface.HistoryHandedOffForTest(); got != total-visible {
		t.Fatalf("historyHandedOff=%d want %d (total-visible)", got, total-visible)
	}
	// Dual-retain: window still holds every logical line (kept for restore).
	if len(history) != total {
		t.Fatalf("wrapped history was trimmed before handoff completed: got %d lines want %d", len(history), total)
	}
	// Handoff bytes must be the Codex-aligned DECSTBM path.
	if !strings.Contains(output, "\x1b[1;") || !strings.Contains(output, "\r\n") {
		t.Fatalf("expected DECSTBM physical-row handoff sequence; total=%d visible=%d", total, visible)
	}
	// The oldest wrapped logical line's first physical row must appear in the
	// handoff stream (expansion preserves content, not just the boundary).
	if !strings.Contains(output, strings.Repeat("x", width)) {
		t.Fatalf("oldest wrapped line's physical rows missing from handoff output")
	}
	for i, line := range history {
		want := fmt.Sprintf("%s-%02d", longLine, i)
		if line != want {
			t.Fatalf("history[%d]=%q want %q", i, line, want)
		}
	}
}

// TestFixedBottomSurface_WrappedHistoryTrimsAtHardCapAfterPhysicalHandoff
// pins that wrapped history no longer pins the window at its unbounded size:
// once physical-row handoff completes, the normal soft-trim bound applies even
// though the retained lines wrap.
func TestFixedBottomSurface_WrappedHistoryTrimsAtHardCapAfterPhysicalHandoff(t *testing.T) {
	const width, height = 12, 20
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	total := historyWindowMaxLines + 5
	longLine := strings.Repeat("界", width)

	captureUIStdout(t, func() {
		var text strings.Builder
		for i := 0; i < total; i++ {
			text.WriteString(fmt.Sprintf("%s-%03d\n", longLine, i))
		}
		if _, err, ok := surface.WriteOutput(io.Discard, text.String()); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})

	history := surface.HistoryWindowForTest()
	if got := surface.HistoryHandedOffForTest(); got == 0 {
		t.Fatal("wrapped history must hand off via physical expansion")
	}
	if len(history) > historyWindowMaxLines {
		t.Fatalf("wrapped history exceeded hard cap %d: got %d lines", historyWindowMaxLines, len(history))
	}
	if history[len(history)-1] != fmt.Sprintf("%s-%03d", longLine, total-1) {
		t.Fatalf("newest unhanded line changed: %q", history[len(history)-1])
	}
}

func TestFixedBottomSurface_FailedHistoryInsertDoesNotAdvanceBoundary(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 20)
	surface.mu.Lock()
	surface.historyWindow = []string{"oldest", "middle", "newest"}
	surface.terminal.sizeOverride = true
	surface.terminal.width = 0
	surface.terminal.height = 3
	surface.commitExcessHistoryToScrollbackLocked()
	got := surface.handoffFrontier.Value()
	surface.mu.Unlock()

	if got != 0 {
		t.Fatalf("invalid terminal geometry advanced handoff boundary to %d", got)
	}
	if history := surface.HistoryWindowForTest(); strings.Join(history, "|") != "oldest|middle|newest" {
		t.Fatalf("failed handoff changed retained history: %q", history)
	}
}

// TestFixedBottomSurface_WrappedHistorySurvivesActiveBandGrowth pins the
// Phase 1 contract (D2): wrapped rows displaced by ActiveBand growth stay in
// the retained model instead of being handed off, and the band-shrink
// repaint restores them. The window keeps every logical line and the frontier
// never advances on a pure geometry change.
func TestFixedBottomSurface_WrappedHistorySurvivesActiveBandGrowth(t *testing.T) {
	const width, height = 12, 20
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	})
	visibleBefore := surface.visibleOutputRowsForTest()
	longLine := strings.Repeat("y", width+5)
	captureUIStdout(t, func() {
		// Fill the visible region with wrapped lines (no overflow yet).
		for i := 0; i < visibleBefore; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("%s-%02d\n", longLine, i))
		}
	})
	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("precondition: historyHandedOff=%d want 0 (no overflow yet)", got)
	}

	captureUIStdout(t, func() {
		surface.SetActiveBand([]string{"• Running grep"})
	})
	visibleAfter := surface.visibleOutputRowsForTest()
	displaced := visibleBefore - visibleAfter
	if displaced < 1 {
		t.Fatalf("ActiveBand displaced %d rows, want >= 1", displaced)
	}
	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("band growth handed displaced rows to scrollback: frontier=%d want 0 (D2)", got)
	}
	// Model-retain: every logical line stays for shrink restore.
	if history := surface.HistoryWindowForTest(); len(history) != visibleBefore {
		t.Fatalf("band growth trimmed retained history: got %d lines want %d", len(history), visibleBefore)
	}

	captureUIStdout(t, func() {
		surface.ClearActiveBand()
	})
	// The frontier must never advance on a pure geometry change; the window
	// still holds every logical line for the repaint restore.
	if got := surface.HistoryHandedOffForTest(); got != 0 {
		t.Fatalf("band shrink advanced handoff frontier to %d, want 0", got)
	}
	if history := surface.HistoryWindowForTest(); len(history) != visibleBefore {
		t.Fatalf("band shrink trimmed retained history: got %d lines want %d", len(history), visibleBefore)
	}
	// The newest wrapped line must be re-materialized after shrink (its
	// continuation row carries the line marker).
	frame := frameDump(surface.ComposedFrameForTest())
	wantNewest := fmt.Sprintf("-%02d", visibleBefore-1)
	if !strings.Contains(frame, wantNewest) {
		t.Fatalf("band shrink did not restore newest wrapped line (marker %q); frame=%q", wantNewest, frame)
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
