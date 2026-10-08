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

// TestStreamingAppendRecordsNoPaintEventsStateOnly pins the L3-2 state-only
// contract for the streaming append: with the paint probe enabled, the append
// updates retained state without touching the physical painter, so the probe
// records no frames and no reconciliation events. The append stays observable
// through the composed frame (the new oracle for the retired byte path).
func TestStreamingAppendRecordsNoPaintEventsStateOnly(t *testing.T) {
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
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "line-30\n")
	})

	if frames := engine.Trace().Frames(); frames != 0 {
		t.Fatalf("state-only append must not record physical frames, got %d", frames)
	}
	if stats := engine.Trace().Stats(); len(stats) != 0 {
		t.Fatalf("state-only append must not record paint events: %#v", stats)
	}
	frame := frameDump(surface.ComposedFrameForTest())
	if !strings.Contains(frame, "line-30") {
		t.Fatal("new history row missing from composed frame")
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

// TestPaintDebugRowTagHashFollowsContentAcrossScroll pins the
// content-addressed tag identity in state-only mode: when rows move to new
// screen positions (new output pushing retained rows), the tag's content hash
// follows the content while the row number follows the position. The w
// counter stays 0 because no physical paint occurs (L3-2), and no "*" marker
// may appear.
func TestPaintDebugRowTagHashFollowsContentAcrossScroll(t *testing.T) {
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

	// Reconciles record no paint events state-only; run a burst to prove the
	// tags survive it unchanged.
	captureUIStdout(t, func() { surface.Reconcile() })
	captureUIStdout(t, func() { surface.Reconcile() })
	captureUIStdout(t, func() { surface.Reconcile() })
	captureUIStdout(t, func() { surface.Reconcile() })

	findTag := func(needle string) (rowNo, white int, hash string) {
		t.Helper()
		for _, row := range surface.ComposedFrameForTest() {
			text := rowText(row)
			m := debugTagPattern.FindStringSubmatch(text)
			if m == nil || !strings.Contains(text, needle) {
				continue
			}
			rowNo, _ = strconv.Atoi(m[2])
			white, _ = strconv.Atoi(m[3])
			return rowNo, white, m[1]
		}
		return 0, -1, ""
	}

	rowBefore, wBefore, hashBefore := findTag("scroll-me")
	if rowBefore == 0 || wBefore != 0 || hashBefore == "" {
		t.Fatalf("before scroll: row=%d w=%d hash=%q, want a tagged row with w=0", rowBefore, wBefore, hashBefore)
	}

	// Append a new line: the retained rows move to new screen positions.
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "new-line\n")
	})

	rowAfter, wAfter, hashAfter := findTag("scroll-me")
	if rowAfter == 0 {
		t.Fatal("scroll-me row lost its tag after scrolling")
	}
	if rowAfter == rowBefore {
		t.Fatalf("test setup: scroll-me did not move (row %d), append did not reflow", rowAfter)
	}
	if hashAfter != hashBefore {
		t.Fatalf("content hash must follow the content across scroll: before=%s after=%s", hashBefore, hashAfter)
	}
	if wAfter != 0 {
		t.Fatalf("state-only mode must keep w=0 (no physical paint), got %d", wAfter)
	}
	for _, row := range surface.ComposedFrameForTest() {
		if strings.Contains(rowText(row), "*]") {
			t.Fatalf("state-only frames must not carry the white '*' marker: %q", rowText(row))
		}
	}

	// A fresh content at the vacated position must carry its own identity.
	if _, _, hashNew := findTag("new-line"); hashNew == hashBefore {
		t.Fatalf("distinct content must hash differently, both %s", hashNew)
	}
}
