package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
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
