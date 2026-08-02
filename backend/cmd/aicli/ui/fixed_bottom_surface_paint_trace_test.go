package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestStreamingAppendNoWhiteRepaintWithTrace is the reconciliation-form
// regression for the streaming duplicate-render bug: with the paint probe
// enabled, a steady-state log-style append must produce zero white repaints
// (no history row re-emitted with unchanged content) and zero missing
// coverage. Before commit a26b39d the unconditional Invalidate in
// insertHistoryLinesLocked forced the next Flush to re-emit every retained
// history row, which this probe now quantifies as a WhiteEmits burst on rows
// 1..N.
func TestStreamingAppendNoWhiteRepaintWithTrace(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	// Overflow into the direct-scroll append path (log-style natural scroll).
	captureUIStdout(t, func() {
		for i := 0; i < 30; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})

	// Start the reproduction window after the initial fill so only the
	// steady-state append is measured.
	engine.Trace().Reset()
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "line-30\n")
	})

	// The trace must be recording (the append flushed at least one frame).
	if engine.Trace().Frames() == 0 {
		t.Fatal("expected the append to advance the trace frame counter")
	}
	// Reconciliation: no white repaints and no missing coverage. An empty
	// stat table is the healthy steady state (history rows were committed
	// silently by CommitRange, nothing changed, nothing was repainted).
	var white, missing uint64
	for _, stat := range engine.Trace().Stats() {
		white += stat.WhiteEmits
		missing += stat.MissingPaints
	}
	if white != 0 {
		t.Fatalf("steady-state append produced %d white repaints (duplicate rendering): %#v", white, engine.Trace().Stats())
	}
	if missing != 0 {
		t.Fatalf("reconciliation invariant violated: %d rows changed but were not painted: %#v", missing, engine.Trace().Stats())
	}
}

// TestStreamingAppendBottomDeltaIsNotWhiteRepaint pins the same invariant in
// the representative streaming shape: the bottom pane (prompt + active band)
// is present and repainted per frame, while the retained history stays
// untouched. Bottom rows must be recorded as real content changes (never as
// white repaints), and history rows must not appear in the trace at all.
func TestStreamingAppendBottomDeltaIsNotWhiteRepaint(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)
	surface.ShowPrompt("> ")
	surface.SetActiveBand([]string{"band-1", "band-2"})

	captureUIStdout(t, func() {
		for i := 0; i < 30; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	engine.Trace().Reset()
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "line-30\n")
	})

	stats := engine.Trace().Stats()
	if len(stats) == 0 {
		t.Fatal("expected bottom-pane delta events for the append")
	}
	var white, missing uint64
	for _, stat := range stats {
		white += stat.WhiteEmits
		missing += stat.MissingPaints
	}
	if white != 0 {
		t.Fatalf("bottom delta contains %d white repaints: %#v", white, stats)
	}
	if missing != 0 {
		t.Fatalf("reconciliation invariant violated: %#v", stats)
	}
	// Every recorded row must be a genuine content change.
	for _, stat := range stats {
		if stat.Changes == 0 {
			t.Fatalf("row %d recorded an emit without a content change: %#v", stat.Row, stat)
		}
	}
}

// TestPaintTraceWhiteRepaintVisibleOnForceRepaint proves the probe actually
// observes full-screen duplicate rendering: a forced reconcile (the old
// per-write Invalidate behavior) shows up as white repaints on the retained
// history rows instead of being invisible.
func TestPaintTraceWhiteRepaintVisibleOnForceRepaint(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 30; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	engine.Trace().Reset()

	// Simulate the pre-fix behavior: full-screen invalidation on every write.
	captureUIStdout(t, func() {
		surface.Reconcile()
	})

	stats := engine.Trace().Stats()
	var white uint64
	for _, stat := range stats {
		white += stat.WhiteEmits
	}
	if white == 0 {
		t.Fatalf("forced reconcile must be visible as white repaints, got none: %#v", stats)
	}
	report := surface.PaintTraceDebugString()
	if !strings.Contains(report, "white") || !strings.Contains(report, "miss") {
		t.Fatalf("report must expose white/miss columns:\n%s", report)
	}
}

// TestPaintTraceToggleThroughSurface pins the /debug on|off wiring: the
// surface forwards the toggle to the engine-owned probe, and disabling keeps
// the accumulated counters.
func TestPaintTraceToggleThroughSurface(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)

	if engine.Trace().Enabled() {
		t.Fatal("trace must start disabled")
	}
	surface.SetPaintTraceEnabled(true)
	if !engine.Trace().Enabled() {
		t.Fatal("SetPaintTraceEnabled(true) must enable the engine probe")
	}
	surface.SetPaintTraceEnabled(false)
	if engine.Trace().Enabled() {
		t.Fatal("SetPaintTraceEnabled(false) must disable the engine probe")
	}
	// A surface without an engine must not panic or record.
	plain := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	plain.SetPaintTraceEnabled(true)
	if plain.PaintTraceDebugString() != "" {
		t.Fatal("surface without engine must report empty trace")
	}
}

// TestPaintTraceDebugStringOwnerAnnotation pins that the report carries the
// row-ownership plan (transcript rows) so operators can see which component
// owns the rows being repainted or skipped.
func TestPaintTraceDebugStringOwnerAnnotation(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 10; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	report := surface.PaintTraceDebugString()
	if !strings.Contains(report, "transcript") {
		t.Fatalf("report must annotate transcript rows:\n%s", report)
	}
}

// rowHasReverseVideo reports whether any non-continuation cell of the row
// carries the paint-flash reverse-video attribute ("7" as an exact SGR code).
func rowHasReverseVideo(cells []vt.Cell) bool {
	for _, cell := range cells {
		if cell.Cont {
			continue
		}
		for _, sgr := range cell.SGR {
			if sgr == paintFlashSGR {
				return true
			}
		}
	}
	return false
}

func rowText(cells []vt.Cell) string {
	var builder strings.Builder
	for _, cell := range cells {
		if cell.Cont {
			continue
		}
		builder.WriteString(cell.Text)
	}
	return builder.String()
}

// TestPaintFlashMarkersVisibleOnScreen pins the on-screen duplicate-render
// visualization: after a frame white-repainted the retained history (forced
// reconcile), the next composed frame carries a reverse-video flash on exactly
// those rows, and the emitted terminal bytes contain the reverse-video
// attribute. The flash is one frame: with a healthy renderer the markers
// disappear again, so the screen itself shows "which rows are being redrawn"
// instead of only a table in /debug display.
func TestPaintFlashMarkersVisibleOnScreen(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 10; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	engine.Trace().Reset()

	// Reproduce the duplicate-render shape: a full-screen reconcile
	// re-emits every row with unchanged content (white repaints).
	captureUIStdout(t, func() {
		surface.Reconcile()
	})

	summary := engine.Trace().LastFrame()
	if len(summary.White) == 0 {
		t.Fatalf("reconcile must white-repaint rows, summary: %#v", summary)
	}
	frame := surface.ComposedFrameForTest()
	if len(frame) != 24 {
		t.Fatalf("composed frame has %d rows, want 24", len(frame))
	}
	flashed := 0
	for _, row := range summary.White {
		if row < 1 || row > len(frame) {
			t.Fatalf("white row %d out of frame bounds", row)
		}
		if !rowHasReverseVideo(frame[row-1]) {
			t.Fatalf("white-repainted row %d is not flashed on screen", row)
		}
		flashed++
	}
	if flashed == 0 {
		t.Fatal("no row flashed")
	}

	// The next real flush must emit the reverse-video bytes to the terminal:
	// the flash is observable live, not only in the /debug display table.
	diff := captureUIStdout(t, func() {
		surface.Reconcile()
	})
	if !strings.Contains(diff, "\x1b[7m") {
		t.Fatalf("flushed diff must contain the reverse-video flash bytes")
	}

	// The flash is a real attribute change: the marker frame is recorded as
	// changed rows (never as white repaints), so the probe keeps honest
	// counts instead of double-counting the marker as a new white repaint.
	nextSummary := engine.Trace().LastFrame()
	if nextSummary.Frame == summary.Frame {
		t.Fatalf("second reconcile did not advance the recorded frame")
	}
	for _, row := range summary.White {
		if containsInt(nextSummary.White, row) {
			t.Fatalf("flash marker on row %d was misclassified as a white repaint", row)
		}
	}
}

func containsInt(list []int, want int) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// TestPaintDebugAnnotationsLiveOnMessageRows pins the user-facing contract of
// the on-screen diagnostics: with /debug on the reverse-video markers live on
// the message stream / history rows, and the status row is byte-identical to
// normal operation. Debug counters must never leak into the status line.
func TestPaintDebugAnnotationsLiveOnMessageRows(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 10; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})

	statusBefore := rowText(surface.ComposedFrameForTest()[len(surface.ComposedFrameForTest())-1])

	// Duplicate-render burst: a full-screen reconcile re-emits the retained
	// history with unchanged content (white repaints).
	captureUIStdout(t, func() {
		surface.Reconcile()
	})

	frame := surface.ComposedFrameForTest()
	status := rowText(frame[len(frame)-1])
	if status != statusBefore {
		t.Fatalf("status row must not carry debug counters: before=%q after=%q", statusBefore, status)
	}
	if strings.Contains(status, "paint") || strings.Contains(status, "f=") {
		t.Fatalf("status row leaked paint counters: %q", status)
	}

	// The annotation is on the message rows instead.
	flashed := 0
	for _, row := range engine.Trace().LastFrame().White {
		if row < 1 || row > len(frame) {
			continue
		}
		if !rowHasReverseVideo(frame[row-1]) {
			t.Fatalf("white-repainted message row %d is not marked on screen", row)
		}
		flashed++
	}
	if flashed == 0 {
		t.Fatal("no message row carries the duplicate-render marker")
	}
}

// TestPaintFlashMarkerStickyWindow pins the marker retention: after a white
// repaint the annotation stays on the history rows for a few frames (so it is
// actually noticeable), and expires once the window passes - a row that is
// no longer being re-rendered stops flashing.
func TestPaintFlashMarkerStickyWindow(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)
	surface.SetActiveBand([]string{"band-0"})

	captureUIStdout(t, func() {
		for i := 0; i < 10; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	engine.Trace().Reset()

	// Frame 1: white-repaint burst over the retained history.
	captureUIStdout(t, func() {
		surface.Reconcile()
	})
	white := engine.Trace().LastFrame().White
	if len(white) == 0 {
		t.Fatal("reconcile must white-repaint rows")
	}
	frame := surface.ComposedFrameForTest()
	for _, row := range white {
		if row < 1 || row > len(frame) {
			continue
		}
		if !rowHasReverseVideo(frame[row-1]) {
			t.Fatalf("row %d not marked right after the white repaint", row)
		}
	}

	// Frames 2..3 only touch the bottom pane (real changes, no white
	// repaints): the marker must persist for the sticky window.
	surface.SetActiveBand([]string{"band-1"})
	surface.SetActiveBand([]string{"band-2"})
	frame = surface.ComposedFrameForTest()
	for _, row := range white {
		if row < 1 || row > len(frame) {
			continue
		}
		if !rowHasReverseVideo(frame[row-1]) {
			t.Fatalf("row %d lost its marker within the sticky window", row)
		}
	}

	// Enough frames later, without further white repaints, the marker
	// expires and the history rows return to normal.
	for i := 3; i < 12; i++ {
		surface.SetActiveBand([]string{fmt.Sprintf("band-%d", i)})
	}
	frame = surface.ComposedFrameForTest()
	for _, row := range white {
		if row < 1 || row > len(frame) {
			continue
		}
		if rowHasReverseVideo(frame[row-1]) {
			t.Fatalf("row %d still marked after the sticky window expired", row)
		}
	}
}
