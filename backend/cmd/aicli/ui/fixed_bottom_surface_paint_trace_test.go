package ui

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
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

	// The first reconcile resyncs the committed front tags: the tag value
	// changes, so a tagged row's full row differs (a genuine content change,
	// not duplicate rendering) and must not count as white. Untagged rows
	// (prompt/status) have nothing to resync; their forced re-emission is a
	// real white repaint and is allowed to be recorded.
	captureUIStdout(t, func() {
		surface.Reconcile()
	})
	rowNo, _ := firstTaggedRow(surface.ComposedFrameForTest())
	if rowNo == 0 {
		t.Fatal("no message row carries a debug tag")
	}
	if white := engine.Trace().WhiteEmits(rowNo); white != 0 {
		t.Fatalf("tag resync reconcile must not count as white repaints on tagged row %d: %#v", rowNo, engine.Trace().Stats())
	}

	// The second full-screen reconcile re-emits every retained row with
	// unchanged content: that duplicate rendering must be visible as white.
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

// debugTagPattern matches a message-row debug tag "[hhhh #NN wN]" (with an
// optional trailing "*" marking rows white-repainted by the most recent
// frame) and captures the content fingerprint, the 1-based screen row
// number, and the cumulative white-repaint count for that content.
var debugTagPattern = regexp.MustCompile(`^\[([0-9a-f]{4}) #(\d+) w(\d+)\*?\]`)

// firstTaggedRow returns the 1-based screen row of the first row carrying a
// debug tag, plus its white-repaint count (-1 when no row is tagged).
func firstTaggedRow(frame [][]vt.Cell) (int, int) {
	for i, row := range frame {
		if m := debugTagPattern.FindStringSubmatch(rowText(row)); m != nil {
			n, _ := strconv.Atoi(m[3])
			return i + 1, n
		}
	}
	return 0, -1
}

// TestPaintDebugRowTagStableAndDistinct pins the content fingerprint: the
// same text always hashes to the same tag (so duplicate rendering of a row is
// instantly recognizable), different text hashes differently, and full-width
// padding blanks do not affect the fingerprint.
func TestPaintDebugRowTagStableAndDistinct(t *testing.T) {
	if hash4Hex("line-0") != hash4Hex("line-0") {
		t.Fatal("fingerprint must be deterministic for identical text")
	}
	if hash4Hex("line-0") != hash4Hex("line-0        ") {
		t.Fatal("trailing padding blanks must not change the fingerprint")
	}
	if hash4Hex("line-0") == hash4Hex("line-1") {
		t.Fatal("distinct text must hash differently")
	}

	// Screen level: two identical output lines carry the same fingerprint in
	// their tags, a different line carries a different one.
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "same-text\n")
		surface.WriteOutput(io.Discard, "same-text\n")
		surface.WriteOutput(io.Discard, "other-text\n")
	})

	frame := surface.ComposedFrameForTest()
	var sameA, sameB, other string
	for _, row := range frame {
		m := debugTagPattern.FindStringSubmatch(rowText(row))
		if m == nil {
			continue
		}
		text := rowText(row)
		switch {
		case strings.Contains(text, "same-text"):
			if sameA == "" {
				sameA = m[1]
			} else {
				sameB = m[1]
			}
		case strings.Contains(text, "other-text"):
			other = m[1]
		}
	}
	if sameA == "" || sameB == "" || other == "" {
		t.Fatalf("expected three tagged rows, got sameA=%q sameB=%q other=%q", sameA, sameB, other)
	}
	if sameA != sameB {
		t.Fatalf("identical content must share a fingerprint: %s vs %s", sameA, sameB)
	}
	if sameA == other {
		t.Fatalf("distinct content must hash differently: %s", sameA)
	}
}

// TestPaintDebugRowTagWhiteCounterGrows pins the duplicate-render indicator
// on the message stream itself: the w counter in a row's tag increments
// visibly each time that row is white-repainted, and the probe agrees.
func TestPaintDebugRowTagWhiteCounterGrows(t *testing.T) {
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

	rowNo, before := firstTaggedRow(surface.ComposedFrameForTest())
	if rowNo == 0 {
		t.Fatal("no message row carries a debug tag")
	}
	if before != 0 {
		t.Fatalf("baseline white count = %d, want 0", before)
	}

	// First reconcile: resyncs the committed front tags (tag reappears =
	// content change, not duplicate rendering), so the white counter must
	// stay at the fresh baseline.
	captureUIStdout(t, func() {
		surface.Reconcile()
	})
	frame := surface.ComposedFrameForTest()
	m := debugTagPattern.FindStringSubmatch(rowText(frame[rowNo-1]))
	if m == nil {
		t.Fatalf("row %d lost its debug tag after the resync reconcile", rowNo)
	}
	if resync, _ := strconv.Atoi(m[3]); resync != 0 {
		t.Fatalf("resync reconcile must not count as white: w=%d", resync)
	}

	// Second reconcile: duplicate-render burst, a full-screen reconcile
	// re-emits the retained history with unchanged content. The white
	// counter on the row must grow visibly and the probe must agree.
	captureUIStdout(t, func() {
		surface.Reconcile()
	})
	frame = surface.ComposedFrameForTest()
	m = debugTagPattern.FindStringSubmatch(rowText(frame[rowNo-1]))
	if m == nil {
		t.Fatalf("row %d lost its debug tag after the reconcile", rowNo)
	}
	after, _ := strconv.Atoi(m[3])
	if after <= before {
		t.Fatalf("white counter did not grow on the tagged row: before=%d after=%d", before, after)
	}
	if white := engine.Trace().WhiteEmits(rowNo); white == 0 {
		t.Fatalf("probe recorded no white repaint for row %d", rowNo)
	}
}

// TestPaintDebugRowTagOnMessageRows pins the on-screen contract of the
// diagnostics: with /debug on every message-stream row (transcript + active
// band) carries a dim "[hhhh #NN wN]" tag whose row number matches the
// physical screen position (so the screen maps 1:1 onto the /debug display
// table), while the status row stays untouched - no debug text leaks into
// the status line.
func TestPaintDebugRowTagOnMessageRows(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)
	surface.SetActiveBand([]string{"band-row"})

	captureUIStdout(t, func() {
		for i := 0; i < 10; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})

	frame := surface.ComposedFrameForTest()
	if len(frame) != 24 {
		t.Fatalf("composed frame has %d rows, want 24", len(frame))
	}

	tagged := 0
	bandTagged := false
	for i, row := range frame {
		text := rowText(row)
		m := debugTagPattern.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		tagged++
		rowNo, _ := strconv.Atoi(m[2])
		if rowNo != i+1 {
			t.Fatalf("row %d tag says #%d - the tag must match the screen position", i+1, rowNo)
		}
		if strings.Contains(text, "band-row") {
			bandTagged = true
		}
	}
	if tagged == 0 {
		t.Fatal("no message row carries a debug tag")
	}
	if !bandTagged {
		t.Fatal("active band row must carry a debug tag too")
	}
	status := rowText(frame[len(frame)-1])
	if debugTagPattern.MatchString(status) {
		t.Fatalf("status row must not carry a debug tag: %q", status)
	}
	if strings.Contains(status, "paint") || strings.Contains(status, "w=") {
		t.Fatalf("status row leaked debug counters: %q", status)
	}
	if !strings.Contains(status, "Ready") {
		t.Fatalf("status row lost its normal content: %q", status)
	}

	// Disabling the trace removes the tags completely: the message stream
	// returns to its normal form.
	surface.SetPaintTraceEnabled(false)
	frame = surface.ComposedFrameForTest()
	for _, row := range frame {
		if debugTagPattern.MatchString(rowText(row)) {
			t.Fatalf("debug tag survives disable: %q", rowText(row))
		}
	}
}

// TestPaintDebugRowTagWhiteFollowsContentAcrossScroll pins the
// content-addressed w counter against the misreading this marker series set
// out to fix: when rows move to new screen positions (new output pushing
// retained rows down), the w counter must follow the content, not inherit
// the position's history. A row white-repainted twice keeps w2 wherever it
// appears; a fresh content that lands at that position starts at w0.
func TestPaintDebugRowTagWhiteFollowsContentAcrossScroll(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "scroll-me\n")
		surface.WriteOutput(io.Discard, "fill\n")
	})
	engine.Trace().Reset()

	// Two white-repaint bursts on unchanged content (reconciles 2 and 4;
	// reconciles 1 and 3 are tag-resync frames and must not count): the
	// scroll-me content reaches w2.
	captureUIStdout(t, func() { surface.Reconcile() }) // tag resync, no white
	captureUIStdout(t, func() { surface.Reconcile() }) // white burst -> w1
	captureUIStdout(t, func() { surface.Reconcile() }) // tag resync
	captureUIStdout(t, func() { surface.Reconcile() }) // white burst -> w2

	findTag := func(needle string) (rowNo, white int) {
		t.Helper()
		for _, row := range surface.ComposedFrameForTest() {
			text := rowText(row)
			m := debugTagPattern.FindStringSubmatch(text)
			if m == nil || !strings.Contains(text, needle) {
				continue
			}
			rowNo, _ = strconv.Atoi(m[2])
			white, _ = strconv.Atoi(m[3])
			return rowNo, white
		}
		return 0, -1
	}

	rowBefore, wBefore := findTag("scroll-me")
	if rowBefore == 0 || wBefore != 2 {
		t.Fatalf("before scroll: row=%d w=%d, want a tagged row with w=2", rowBefore, wBefore)
	}

	// Append a new line: the retained rows move to new screen positions.
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "new-line\n")
	})

	rowAfter, wAfter := findTag("scroll-me")
	if rowAfter == 0 {
		t.Fatal("scroll-me row lost its tag after scrolling")
	}
	if rowAfter == rowBefore {
		t.Fatalf("test setup: scroll-me did not move (row %d), append did not reflow", rowAfter)
	}
	if wAfter != wBefore {
		t.Fatalf("w must follow the content across scroll: before=%d after=%d", wBefore, wAfter)
	}

	// A fresh content at the vacated position must not inherit the
	// position's white history.
	if _, wNew := findTag("new-line"); wNew != 0 {
		t.Fatalf("fresh content must start at w=0, got w=%d", wNew)
	}
}

// TestPaintDebugRowTagStarMarksLastFrameWhite pins the "*" marker: after a
// white-repaint burst the duplicated rows carry "[... wN*]", and once a
// frame records no white repaint on the row the marker disappears again -
// "duplicated right now" stays separate from the lifetime count.
func TestPaintDebugRowTagStarMarksLastFrameWhite(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 5; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	engine.Trace().Reset()

	// Resync frame: no white, no star.
	captureUIStdout(t, func() { surface.Reconcile() })
	for _, row := range surface.ComposedFrameForTest() {
		if strings.Contains(rowText(row), "w0*]") {
			t.Fatalf("resync frame must not mark white rows: %q", rowText(row))
		}
	}

	// Duplicate-render burst: white rows carry the star.
	captureUIStdout(t, func() { surface.Reconcile() })
	starred := 0
	for _, row := range surface.ComposedFrameForTest() {
		if strings.Contains(rowText(row), "w1*]") {
			starred++
		}
	}
	if starred == 0 {
		t.Fatal("white-repainted rows must carry the '*' marker after the burst")
	}
}
