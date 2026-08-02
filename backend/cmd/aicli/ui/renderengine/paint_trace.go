package renderengine

import (
	"fmt"
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
		}
		if event.painted && !event.changed {
			stat.WhiteEmits++
		}
		if event.changed && !event.painted {
			stat.MissingPaints++
		}
		if event.changed {
			stat.Changes++
			stat.LastChangeFrame = t.frames
		}
	}
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
