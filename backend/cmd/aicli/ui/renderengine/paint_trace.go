package renderengine

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// PaintTrace is the observability probe for the render path. It is purely
// diagnostic: it never influences layout, diffing, or terminal output, so it
// cannot compensate for a rendering defect the way legacy reserve fields did.
//
// It reconciles, per screen row and per frame, the rows that needed paint
// against the rows that were actually emitted by the double-buffer diff:
//
//	needsPaint (content changed, or forceRepaint) ⊆ painted (emitted rows)
//
// Two anomaly classes are quantified from that reconciliation:
//
//   - White repaints (repeated rendering): a row was emitted even though its
//     content was identical to the previous frame. Every forceRepaint /
//     full-screen Invalidate shows up here as a whole-screen white-repaint
//     burst, which is exactly the streaming duplicate-render symptom that was
//     previously only diagnosed by guessing.
//   - Missing coverage: a row changed but was not emitted by the diff. A
//     healthy diff engine never produces this; when it appears, the row was
//     skipped by the diff (a real rendering bug to investigate).
//
// The probe is enabled via /debug on (SetEnabled(true)); disabling keeps the
// accumulated counters so an operator can reproduce a symptom first and
// inspect the report afterwards with /debug display.
type PaintTrace struct {
	mu      sync.Mutex
	enabled bool
	frames  uint64
	height  int
	rows    []RowPaintStat
	// lastFrame is the reconciliation summary of the most recent recorded
	// frame, consumed by the surface to visualize paint activity live on the
	// terminal (flash markers for white-repainted rows on the message stream).
	// It is read-only for callers.
	lastFrame    FrameSummary
	// sticky maps a 1-based row that was white-repainted to the frame number
	// of its most recent white repaint. The surface keeps the flash marker on
	// those rows for a short window after the repaint so the annotation is
	// actually visible instead of depending on the immediately following
	// frame. Purely observational, like the rest of the probe.
	sticky       map[int]uint64
	totalWhite   uint64
	totalMissing uint64
}

// RowPaintStat holds the per-row reconciliation counters for one 1-based
// screen row.
type RowPaintStat struct {
	Row            int    // 1-based screen row
	Emits          uint64 // frames in which this row was emitted
	WhiteEmits     uint64 // emitted while content was identical to front buffer
	MissingPaints  uint64 // content changed but row was not emitted
	Changes        uint64 // frames in which content differed from front buffer
	LastEmitFrame  uint64 // frame number of the most recent emit
	LastChangeFrame uint64 // frame number of the most recent content change
}

// paintRowEvent is one row's reconciliation verdict for a single frame.
type paintRowEvent struct {
	row     int // 1-based
	changed bool
	painted bool
}

// FrameSummary is the per-frame reconciliation result of the most recent
// recorded frame. The surface renders it as on-screen debug information while
// /debug on is active: rows that were white-repainted (emitted with content
// identical to the previous frame) get a reverse-video flash in the composed
// frame. The summary is observational only; consuming it never influences
// layout or diffing.
type FrameSummary struct {
	// Frame is the number of the most recent recorded frame (0 before the
	// first frame or after re-enable).
	Frame uint64
	// PaintedRows is the number of rows emitted by the most recent frame.
	PaintedRows int
	// White holds the 1-based rows the most recent frame emitted while their
	// content was identical to the previous frame (duplicate rendering).
	White []int
	// Missing holds the 1-based rows whose content changed but that the most
	// recent frame did not emit (diff coverage loss).
	Missing []int
	// TotalWhite and TotalMissing accumulate across all recorded frames.
	TotalWhite   uint64
	TotalMissing uint64
}

// NewPaintTrace creates a disabled probe with zeroed counters.
func NewPaintTrace() *PaintTrace {
	return &PaintTrace{}
}

// SetEnabled starts (true) or stops (false) recording. Stopping keeps the
// accumulated counters intact; Reset clears them.
func (t *PaintTrace) SetEnabled(enabled bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.enabled = enabled
	if enabled {
		// A fresh recording window has no "previous frame": clear the last
		// summary so a stale flash cannot fire from a frame recorded before
		// the toggle, and drop the sticky marker set for the same reason.
		// Cumulative counters are kept for /debug display.
		t.lastFrame = FrameSummary{}
		t.sticky = nil
	}
	t.mu.Unlock()
}

// Enabled reports whether recording is active.
func (t *PaintTrace) Enabled() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enabled
}

// Reset clears all counters and the frame counter. Enabled state is kept.
func (t *PaintTrace) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.frames = 0
	t.height = 0
	t.rows = nil
	t.lastFrame = FrameSummary{}
	t.sticky = nil
	t.totalWhite = 0
	t.totalMissing = 0
	t.mu.Unlock()
}

// Frames reports the number of recorded frames.
func (t *PaintTrace) Frames() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.frames
}

// recordFrame appends one frame's per-row events. It is called by
// ScreenModel.Flush only when the probe is attached and enabled; the call is
// a no-op otherwise. Callers must pass height as the current model height so
// the report can bound rows that are out of range after a resize.
func (t *PaintTrace) recordFrame(events []paintRowEvent, height int) {
	if t == nil || len(events) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled {
		return
	}
	t.frames++
	summary := FrameSummary{Frame: t.frames}
	if height > t.height {
		t.height = height
	}
	if cap(t.rows) < height {
		rows := make([]RowPaintStat, height)
		copy(rows, t.rows)
		t.rows = rows
	}
	for _, event := range events {
		if event.row < 1 || event.row > height {
			continue
		}
		stat := &t.rows[event.row-1]
		stat.Row = event.row
		if event.painted {
			stat.Emits++
			stat.LastEmitFrame = t.frames
			summary.PaintedRows++
		}
		if event.painted && !event.changed {
			stat.WhiteEmits++
			t.totalWhite++
			summary.White = append(summary.White, event.row)
		}
		if event.changed && !event.painted {
			stat.MissingPaints++
			t.totalMissing++
			summary.Missing = append(summary.Missing, event.row)
		}
		if event.changed {
			stat.Changes++
			stat.LastChangeFrame = t.frames
		}
	}
	summary.TotalWhite = t.totalWhite
	summary.TotalMissing = t.totalMissing
	if t.sticky == nil {
		t.sticky = make(map[int]uint64)
	}
	for _, row := range summary.White {
		t.sticky[row] = t.frames
	}
	t.lastFrame = summary
}

// LastFrame returns the reconciliation summary of the most recent recorded
// frame. The returned slices are owned by the caller. Before any frame is
// recorded (or right after SetEnabled(true)) the summary is zero-valued with
// Frame == 0.
func (t *PaintTrace) LastFrame() FrameSummary {
	if t == nil {
		return FrameSummary{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	summary := t.lastFrame
	summary.White = append([]int(nil), t.lastFrame.White...)
	summary.Missing = append([]int(nil), t.lastFrame.Missing...)
	return summary
}

// StickyRows returns the 1-based rows white-repainted within the last window
// frames (window 0 = only the most recent recorded frame). The surface uses
// this to keep the flash marker visible for a few frames after a white
// repaint, so duplicate rendering on the message stream is actually visible
// on the terminal even when the immediately following frame does not get
// composed (e.g. the white repaint was the last activity). The slice is
// sorted ascending and owned by the caller.
func (t *PaintTrace) StickyRows(window uint64) []int {
	if t == nil || t.frames == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var rows []int
	for row, last := range t.sticky {
		if t.frames-last <= window {
			rows = append(rows, row)
		}
	}
	sort.Ints(rows)
	return rows
}

// Stats returns a snapshot of the per-row counters for rows that recorded at
// least one event. The slice is owned by the caller.
func (t *PaintTrace) Stats() []RowPaintStat {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var result []RowPaintStat
	for _, stat := range t.rows {
		if stat.Emits > 0 || stat.WhiteEmits > 0 || stat.MissingPaints > 0 || stat.Changes > 0 {
			result = append(result, stat)
		}
	}
	return result
}

// DebugString renders the reconciliation report. owners is an optional
// per-row owner table (index 0 = screen row 1, e.g. the surface's most recent
// row-ownership plan); nil entries render as "-". Rows without any recorded
// event are omitted.
func (t *PaintTrace) DebugString(owners []RowOwner) string {
	if t == nil {
		return ""
	}
	stats := t.Stats()
	if len(stats) == 0 {
		return "Paint Trace: no events recorded (enable with /debug on)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Paint Trace: frames=%d\n", t.Frames())
	b.WriteString("  row  emits  white  miss  changes  lastEmit  lastChange  owner\n")
	for _, stat := range stats {
		owner := "-"
		if stat.Row >= 1 && stat.Row <= len(owners) {
			owner = owners[stat.Row-1].String()
		}
		fmt.Fprintf(&b, "%5d %6d %6d %5d %8d %9d %11d  %s\n",
			stat.Row, stat.Emits, stat.WhiteEmits, stat.MissingPaints,
			stat.Changes, stat.LastEmitFrame, stat.LastChangeFrame, owner)
	}
	return b.String()
}
