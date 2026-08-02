package renderengine

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// testRow builds a width-wide row where the first len(text) cells carry the
// rune sequence of text (simple ASCII) and the rest are blank.
func testRow(width int, text string) []vt.Cell {
	row := make([]vt.Cell, width)
	for i := 0; i < len(text) && i < width; i++ {
		row[i] = vt.Cell{Text: string(text[i])}
	}
	return row
}

func testGrid(width, height int, lines ...string) [][]vt.Cell {
	grid := make([][]vt.Cell, height)
	for r := 0; r < height; r++ {
		text := ""
		if r < len(lines) {
			text = lines[r]
		}
		grid[r] = testRow(width, text)
	}
	return grid
}

func sumWhiteEmits(stats []RowPaintStat) uint64 {
	var total uint64
	for _, stat := range stats {
		total += stat.WhiteEmits
	}
	return total
}

func sumMissingPaints(stats []RowPaintStat) uint64 {
	var total uint64
	for _, stat := range stats {
		total += stat.MissingPaints
	}
	return total
}

// TestPaintTraceDisabledByDefault pins that the probe starts disabled and
// records nothing until SetEnabled(true).
func TestPaintTraceDisabledByDefault(t *testing.T) {
	trace := NewPaintTrace()
	if trace.Enabled() {
		t.Fatal("new probe must be disabled")
	}
	trace.recordFrame([]paintRowEvent{{row: 1, changed: true, painted: true}}, 24)
	if trace.Frames() != 0 {
		t.Fatalf("disabled probe recorded frames: %d", trace.Frames())
	}
	if len(trace.Stats()) != 0 {
		t.Fatalf("disabled probe recorded stats: %#v", trace.Stats())
	}
}

// TestPaintTraceCountsDiffEmitsAndChanges pins the healthy diff path: a row
// whose content changes is emitted once (Emits/Changes +1, no white repaint),
// and an identical restage produces no new events at all.
func TestPaintTraceCountsDiffEmitsAndChanges(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	model := NewScreenModel(10, 4)
	model.AttachTrace(trace)

	model.StageFrame(testGrid(10, 4, "aaaa", "bbbb", "cccc", "dddd"))
	output := model.Flush()
	if output == "" {
		t.Fatal("expected first flush to emit changed rows")
	}
	stats := trace.Stats()
	if len(stats) != 4 {
		t.Fatalf("expected 4 changed rows, got %#v", stats)
	}
	for _, stat := range stats {
		if stat.Emits != 1 || stat.Changes != 1 || stat.WhiteEmits != 0 || stat.MissingPaints != 0 {
			t.Fatalf("row %d: emits=%d changes=%d white=%d missing=%d", stat.Row, stat.Emits, stat.Changes, stat.WhiteEmits, stat.MissingPaints)
		}
	}
	if trace.Frames() != 1 {
		t.Fatalf("frames=%d want 1", trace.Frames())
	}

	// Identical restage: no bytes, no events.
	model.StageFrame(testGrid(10, 4, "aaaa", "bbbb", "cccc", "dddd"))
	if output := model.Flush(); output != "" {
		t.Fatalf("identical restage must emit nothing, got %q", output)
	}
	stats = trace.Stats()
	for _, stat := range stats {
		if stat.Emits != 1 || stat.Changes != 1 {
			t.Fatalf("row %d must not re-emit on identical restage: %#v", stat.Row, stat)
		}
	}
	if trace.Frames() != 2 {
		t.Fatalf("frames=%d want 2 (empty frame still advances)", trace.Frames())
	}
}

// TestPaintTraceCountsWhiteRepaintOnForceRepaint pins that a forceRepaint
// (Invalidate / reconcile / resize) with unchanged content is classified as a
// white repaint for every emitted row: this is the quantified signature of the
// full-screen duplicate-render symptom.
func TestPaintTraceCountsWhiteRepaintOnForceRepaint(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	model := NewScreenModel(10, 3)
	model.AttachTrace(trace)

	model.StageFrame(testGrid(10, 3, "aaa", "bbb", "ccc"))
	model.Flush()

	model.Invalidate()
	model.Flush()

	stats := trace.Stats()
	if len(stats) != 3 {
		t.Fatalf("expected 3 rows, got %#v", stats)
	}
	for _, stat := range stats {
		if stat.WhiteEmits != 1 {
			t.Fatalf("row %d must record one white repaint on forceRepaint: %#v", stat.Row, stat)
		}
		if stat.Emits != 2 || stat.Changes != 1 {
			t.Fatalf("row %d: emits=%d changes=%d want 2/1", stat.Row, stat.Emits, stat.Changes)
		}
	}
	if sumWhiteEmits(stats) != 3 {
		t.Fatalf("total white repaints=%d want 3", sumWhiteEmits(stats))
	}
}

// TestPaintTraceRecordsMissingPaint verifies the missing-coverage counter via
// a directly constructed event (a healthy diff never produces one; the
// counter exists to surface diff regressions when they happen).
func TestPaintTraceRecordsMissingPaint(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	trace.recordFrame([]paintRowEvent{
		{row: 1, changed: true, painted: false},
		{row: 2, changed: true, painted: true},
		{row: 3, changed: false, painted: false},
	}, 24)
	stats := trace.Stats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 rows with events, got %#v", stats)
	}
	for _, stat := range stats {
		switch stat.Row {
		case 1:
			if stat.MissingPaints != 1 || stat.Emits != 0 || stat.Changes != 1 {
				t.Fatalf("row 1 must record missing paint: %#v", stat)
			}
		case 2:
			if stat.MissingPaints != 0 || stat.Emits != 1 || stat.Changes != 1 {
				t.Fatalf("row 2 must record a clean emit: %#v", stat)
			}
		}
	}
}

// TestPaintTraceReconciliationInvariant drives a deterministic sequence of
// stage/commit/resize/invalidate operations and asserts the core accounting
// invariant after every frame: a changed row is never left unpainted. A
// violation of this invariant is a missing-coverage rendering bug.
func TestPaintTraceReconciliationInvariant(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	model := NewScreenModel(10, 5)
	model.AttachTrace(trace)

	assertInvariant := func(step string) {
		t.Helper()
		if missing := sumMissingPaints(trace.Stats()); missing != 0 {
			t.Fatalf("step %s: reconciliation invariant violated, missing paints=%d stats=%#v", step, missing, trace.Stats())
		}
	}

	// Frame 1: full frame from blank.
	model.StageFrame(testGrid(10, 5, "one", "two", "three", "four", "five"))
	model.Flush()
	assertInvariant("full frame")

	// Frame 2: single-row edit.
	model.StageRow(3, testRow(10, "THREE"))
	model.Flush()
	assertInvariant("single row edit")

	// Frame 3: no-op restage.
	model.StageFrame(testGrid(10, 5, "one", "two", "THREE", "four", "five"))
	model.Flush()
	assertInvariant("no-op restage")

	// Frame 4: commit a range silently, then edit a row inside it (the
	// direct-scroll append pattern).
	model.StageRow(2, testRow(10, "two!"))
	model.CommitRange(1, 2)
	model.Flush()
	assertInvariant("commit range then edit")

	// Frame 5: resize forces a full repaint of the new geometry.
	model.Resize(10, 6)
	model.StageFrame(testGrid(10, 6, "one", "two!", "THREE", "four", "five", "six"))
	model.Flush()
	assertInvariant("resize full repaint")

	// Frame 6: force repaint of unchanged content (white repaints are legal,
	// missing coverage is not).
	model.Invalidate()
	model.Flush()
	assertInvariant("force repaint")

	// The last frame must have painted every row it touched.
	stats := trace.Stats()
	for _, stat := range stats {
		if stat.MissingPaints != 0 {
			t.Fatalf("row %d missing paints=%d: %#v", stat.Row, stat.MissingPaints, stat)
		}
		if stat.Emits == 0 {
			t.Fatalf("row %d has events but no emits: %#v", stat.Row, stat)
		}
	}
}

// TestPaintTraceSetEnabledKeepsCounters pins the /debug workflow contract:
// disabling stops recording but retains the accumulated report so an operator
// can reproduce first and inspect afterwards.
func TestPaintTraceSetEnabledKeepsCounters(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	model := NewScreenModel(10, 2)
	model.AttachTrace(trace)

	model.StageFrame(testGrid(10, 2, "aa", "bb"))
	model.Flush()

	trace.SetEnabled(false)
	model.StageFrame(testGrid(10, 2, "cc", "dd"))
	model.Flush()

	stats := trace.Stats()
	if len(stats) != 2 {
		t.Fatalf("expected retained stats after disable, got %#v", stats)
	}
	for _, stat := range stats {
		if stat.Emits != 1 || stat.Changes != 1 {
			t.Fatalf("row %d must keep pre-disable counters only: %#v", stat.Row, stat)
		}
	}
	if trace.Frames() != 1 {
		t.Fatalf("frames=%d want 1 (disabled frame must not advance)", trace.Frames())
	}
}

// TestPaintTraceReset pins that Reset clears counters while keeping the
// enabled state (a fresh reproduction window keeps recording).
func TestPaintTraceReset(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	trace.recordFrame([]paintRowEvent{{row: 1, changed: true, painted: true}}, 24)
	trace.Reset()
	if trace.Frames() != 0 || len(trace.Stats()) != 0 {
		t.Fatalf("reset must clear counters: frames=%d stats=%#v", trace.Frames(), trace.Stats())
	}
	if !trace.Enabled() {
		t.Fatal("reset must keep enabled state")
	}
}

// TestPaintTraceDebugString pins the report shape: header, per-row lines with
// owner annotation, and the empty-state message.
func TestPaintTraceDebugString(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	model := NewScreenModel(10, 3)
	model.AttachTrace(trace)

	if got := trace.DebugString(nil); !strings.Contains(got, "no events recorded") {
		t.Fatalf("empty report must explain the no-events state, got %q", got)
	}

	model.StageFrame(testGrid(10, 3, "aaa", "bbb", "ccc"))
	model.Flush()
	model.Invalidate()
	model.Flush()

	owners := []RowOwner{RowOwnerTranscript, RowOwnerPrompt, RowOwnerGap}
	report := trace.DebugString(owners)
	for _, want := range []string{"Paint Trace: frames=", "row", "emits", "white", "miss", "changes", "transcript", "prompt"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	lines := strings.Split(strings.TrimSuffix(report, "\n"), "\n")
	if len(lines) != 5 { // header + column line + 3 rows
		t.Fatalf("expected 5 report lines, got %d:\n%s", len(lines), report)
	}
}

// TestPaintTraceLastFrameSummary pins the on-screen visualization feed: the
// most recent frame's white-repainted and missing rows, plus the cumulative
// counters. The surface flashes exactly the White rows on the next composed
// frame and renders the totals in the status-row HUD, so the summary must be
// deterministic per frame and reset on re-enable (no stale flash from before
// a /debug on toggle).
func TestPaintTraceLastFrameSummary(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	trace.recordFrame([]paintRowEvent{
		{row: 1, changed: true, painted: true},  // legit change
		{row: 2, changed: false, painted: true}, // white repaint
		{row: 3, changed: true, painted: false}, // missing coverage
	}, 3)

	summary := trace.LastFrame()
	if summary.Frame != 1 {
		t.Fatalf("frame = %d, want 1", summary.Frame)
	}
	if summary.PaintedRows != 2 {
		t.Fatalf("painted rows = %d, want 2", summary.PaintedRows)
	}
	if summary.TotalWhite != 1 || summary.TotalMissing != 1 {
		t.Fatalf("totals = white %d miss %d, want 1/1", summary.TotalWhite, summary.TotalMissing)
	}
	if len(summary.White) != 1 || summary.White[0] != 2 {
		t.Fatalf("white rows = %v, want [2]", summary.White)
	}
	if len(summary.Missing) != 1 || summary.Missing[0] != 3 {
		t.Fatalf("missing rows = %v, want [3]", summary.Missing)
	}

	// A second frame accumulates totals but carries only its own rows.
	trace.recordFrame([]paintRowEvent{
		{row: 4, changed: false, painted: true}, // white
		{row: 5, changed: true, painted: true},  // legit change
	}, 5)
	summary = trace.LastFrame()
	if summary.Frame != 2 || summary.PaintedRows != 2 {
		t.Fatalf("frame 2 = %#v", summary)
	}
	if summary.TotalWhite != 2 || summary.TotalMissing != 1 {
		t.Fatalf("frame 2 totals = white %d miss %d, want 2/1", summary.TotalWhite, summary.TotalMissing)
	}
	if len(summary.White) != 1 || summary.White[0] != 4 {
		t.Fatalf("frame 2 white rows = %v, want [4]", summary.White)
	}
	if len(summary.Missing) != 0 {
		t.Fatalf("frame 2 missing rows = %v, want none", summary.Missing)
	}

	// Disabled frames must not advance or update the summary.
	trace.SetEnabled(false)
	trace.recordFrame([]paintRowEvent{{row: 1, changed: false, painted: true}}, 5)
	if summary := trace.LastFrame(); summary.Frame != 2 {
		t.Fatalf("disabled frame advanced the summary to %#v", summary)
	}

	// Re-enable opens a fresh window: no previous frame, so a stale flash
	// cannot fire. Cumulative counters are kept for /debug display.
	trace.SetEnabled(true)
	summary = trace.LastFrame()
	if summary.Frame != 0 || len(summary.White) != 0 || len(summary.Missing) != 0 {
		t.Fatalf("re-enable must clear the last frame summary, got %#v", summary)
	}
	if frames := trace.Frames(); frames != 2 {
		t.Fatalf("frames = %d, want 2 (counters kept across re-enable)", frames)
	}
}

// TestPaintTraceWhiteEmits pins the per-row duplicate-render counter that the
// surface renders in the message-row debug tag: it counts white repaints per
// 1-based screen row, reports 0 for rows without events, survives disable
// (cumulative counters are kept), and is cleared by Reset.
func TestPaintTraceWhiteEmits(t *testing.T) {
	trace := NewPaintTrace()
	trace.SetEnabled(true)
	trace.recordFrame([]paintRowEvent{
		{row: 1, changed: false, painted: true}, // white repaint
		{row: 2, changed: true, painted: true},  // legit change
	}, 5)

	if got := trace.WhiteEmits(1); got != 1 {
		t.Fatalf("white emits for row 1 = %d, want 1", got)
	}
	if got := trace.WhiteEmits(2); got != 0 {
		t.Fatalf("legit change must not count as white: row 2 = %d", got)
	}
	if got := trace.WhiteEmits(9); got != 0 {
		t.Fatalf("row without events = %d, want 0", got)
	}
	if got := trace.WhiteEmits(0); got != 0 {
		t.Fatalf("row 0 = %d, want 0", got)
	}

	// A second white repaint on the same row accumulates.
	trace.recordFrame([]paintRowEvent{{row: 1, changed: false, painted: true}}, 5)
	if got := trace.WhiteEmits(1); got != 2 {
		t.Fatalf("white emits for row 1 after second repaint = %d, want 2", got)
	}

	// Disabling keeps cumulative counters; Reset clears them.
	trace.SetEnabled(false)
	if got := trace.WhiteEmits(1); got != 2 {
		t.Fatalf("disable must keep counters, row 1 = %d", got)
	}
	trace.Reset()
	if got := trace.WhiteEmits(1); got != 0 {
		t.Fatalf("reset must clear counters, row 1 = %d", got)
	}
}
