package renderengine

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TextHash4 hashes a row's plain text (trailing blanks trimmed) with FNV-1a
// 32, returning the full 32-bit value. Identical text always produces the
// same value, so duplicate rendering of the same content stays recognizable
// across rows and frames. The hash is content-addressed: a row that scrolls
// to another screen position keeps its hash.
func TextHash4(text string) uint32 {
	trimmed := strings.TrimRight(text, " ")
	h := fnv.New32a()
	_, _ = h.Write([]byte(trimmed))
	return h.Sum32()
}

// RowTextHash hashes one physical cell row with the same plain-text
// semantics as the terminal screen: continuation columns of wide runes are
// skipped, blank cells become spaces, trailing blanks are trimmed, and the
// remaining text is hashed with TextHash4. ScreenModel.Flush hashes the
// staged row and the surface hashes the composed plan row with this same
// function, so the debug tag's content fingerprint and the probe's
// per-content white counters always agree.
func RowTextHash(cells []vt.Cell) uint32 {
	var b strings.Builder
	for _, c := range cells {
		if c.Cont {
			continue
		}
		if c.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Text)
		}
	}
	return TextHash4(b.String())
}

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
	// resyncPending marks the next recorded frame as an explicit resync
	// (Reset or SetEnabled(true) opened a fresh recording window): every
	// painted row is classified as a content change so no white counter or
	// star is attributed to the re-synchronized frame.
	resyncPending bool
	frames        uint64
	height        int
	rows          []RowPaintStat
	// byHash accumulates white repaints per content hash (see RowTextHash)
	// so the on-screen w counter can follow the content across scrolling
	// instead of inheriting a screen position's history. WhiteEmits stays
	// position-based for the /debug display table; the row tag uses the
	// content-addressed counter.
	byHash map[uint32]uint64
	// lastFrame is the reconciliation summary of the most recent recorded
	// frame, the probe's immediate-summary outlet (per-row counters for the
	// /debug display table come from Stats). It is read-only for callers.
	lastFrame    FrameSummary
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
	row     int    // 1-based
	hash    uint32 // plain-text content hash of the staged row (RowTextHash)
	changed bool
	painted bool
}

// FrameSummary is the per-frame reconciliation result of the most recent
// recorded frame: which rows were painted, which were white-repainted
// (emitted with content identical to the previous frame), and which changed
// but were not painted. The summary is observational only; consuming it never
// influences layout or diffing.
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
		// summary so no stale reconciliation can be reported after the
		// toggle. Cumulative counters are kept for /debug display.
		t.lastFrame = FrameSummary{}
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
	t.byHash = nil
	t.lastFrame = FrameSummary{}
	t.totalWhite = 0
	t.totalMissing = 0
	// The next frame re-syncs the front tags (they were carried over from
	// before the reset) and must not count as duplicate rendering.
	t.resyncPending = true
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
	if t.resyncPending {
		// A fresh recording window re-syncs the committed front tags: the
		// frame re-emits rows whose tag state was reset, so every painted row
		// is a content change, not a white repaint. This keeps the resync
		// frame from polluting the white counters or the star marker. Rows
		// that were not painted stay unclassified: marking them changed
		// would turn a healthy silent history into a missing-coverage burst.
		for i := range events {
			if events[i].painted {
				events[i].changed = true
			}
		}
		t.resyncPending = false
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
			if t.byHash == nil {
				t.byHash = make(map[uint32]uint64)
			}
			t.byHash[event.hash]++
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

// WhiteEmits reports the cumulative white-repaint count for a 1-based screen
// row (0 for rows that never recorded an event). The counter is
// position-based: it follows the screen row, so after a scroll it reflects
// the history of the position, not of the content. The /debug display table
// uses it; the on-screen row tag uses WhiteEmitsByHash instead so the w
// counter survives scrolling without misleading.
func (t *PaintTrace) WhiteEmits(row int) uint64 {
	if t == nil || row < 1 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if row > len(t.rows) {
		return 0
	}
	return t.rows[row-1].WhiteEmits
}

// WhiteEmitsByHash reports the cumulative white-repaint count for one content
// hash (see RowTextHash). Unlike WhiteEmits, which counts per screen row and
// therefore follows a screen position when rows scroll, this counter follows
// the content: the same row text keeps the same count wherever it appears,
// so a row scrolled to a new position does not inherit another row's
// history, and a row whose content changed starts at its own count. Rows
// without any recorded white repaint report 0.
func (t *PaintTrace) WhiteEmitsByHash(hash uint32) uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byHash[hash]
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
