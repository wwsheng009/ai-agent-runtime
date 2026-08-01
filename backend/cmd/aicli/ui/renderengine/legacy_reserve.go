package renderengine

import (
	"fmt"
	"strings"
)

// LegacyReserveState is the compatibility state used by the immediate-mode
// reserve fallback. Owned ScreenModel frames do not need it; keeping the pure
// transition here lets the fallback converge on one state machine while the
// surface still exposes its historical diagnostic fields to tests/callers.
type LegacyReserveState struct {
	ScrollCompensatedRows int
	PendingScrollDownRows int
	OutputScrollDebtRows  int
	CursorOnBlankRow      bool
}

// LegacyReserveTransition describes the geometry-side effect that still needs
// to be rendered by the caller. A non-zero old/new pair requests a scroll-up
// of the output region before the new reserve is applied.
type LegacyReserveTransition struct {
	ScrollUpOldBottomRows int
	ScrollUpNewBottomRows int
}

// ApplyGeometry applies one legacy reserve geometry transition. Width and
// height changes reset deferred compensation; same-size reserve growth/shrink
// preserves the historical cancellation and trailing-blank semantics.
func (s *LegacyReserveState) ApplyGeometry(width, height, bottomRows, lastWidth, lastHeight, lastBottomRows int) LegacyReserveTransition {
	if s == nil {
		return LegacyReserveTransition{}
	}
	if width == lastWidth && height == lastHeight && bottomRows == lastBottomRows {
		return LegacyReserveTransition{}
	}
	sameSize := width == lastWidth && height == lastHeight
	compensatedRows := s.ScrollCompensatedRows
	var transition LegacyReserveTransition
	switch {
	case sameSize && compensatedRows > 0 && bottomRows > compensatedRows:
		growth := bottomRows - compensatedRows
		if s.PendingScrollDownRows > 0 {
			canceled := s.PendingScrollDownRows
			if canceled > growth {
				canceled = growth
			}
			s.PendingScrollDownRows -= canceled
			growth -= canceled
		}
		scrollGrowth := growth
		if scrollGrowth > 0 && s.CursorOnBlankRow {
			scrollGrowth--
			s.CursorOnBlankRow = false
			s.OutputScrollDebtRows++
		}
		if scrollGrowth > 0 {
			transition.ScrollUpOldBottomRows = bottomRows - scrollGrowth
			transition.ScrollUpNewBottomRows = bottomRows
		}
		s.ScrollCompensatedRows = bottomRows
	case sameSize && compensatedRows > 0 && bottomRows < compensatedRows:
		s.PendingScrollDownRows += compensatedRows - bottomRows
		s.ScrollCompensatedRows = bottomRows
	case !sameSize || compensatedRows <= 0:
		s.PendingScrollDownRows = 0
		s.ScrollCompensatedRows = bottomRows
		s.CursorOnBlankRow = false
		s.OutputScrollDebtRows = 0
	}
	return transition
}

// MarkOutputWritten records the reserve height occupied by a successful
// immediate-mode output write.
func (s *LegacyReserveState) MarkOutputWritten(bottomRows int) {
	if s != nil {
		s.ScrollCompensatedRows = bottomRows
	}
}

// LegacyReserveScrollUpANSI builds the immediate-mode reserve-growth sequence.
// It intentionally preserves the historical DECSTBM ordering while keeping
// sequence construction independent from FixedBottomSurface.
func LegacyReserveScrollUpANSI(height, oldBottomRows, newBottomRows int) string {
	if height <= 1 || newBottomRows <= oldBottomRows {
		return ""
	}
	oldBottomRows = effectiveReserveRows(height, oldBottomRows)
	newBottomRows = effectiveReserveRows(height, newBottomRows)
	delta := newBottomRows - oldBottomRows
	if delta <= 0 {
		return ""
	}
	oldOutputBottom := outputBottomForReserve(height, oldBottomRows)
	if delta > oldOutputBottom {
		delta = oldOutputBottom
	}
	return reserveScrollRegion(1, oldOutputBottom) + reserveMoveTo(oldOutputBottom, 1) + strings.Repeat("\n", delta)
}

// LegacyReserveScrollDownANSI builds the deferred reserve-shrink sequence.
func LegacyReserveScrollDownANSI(height, bottomRows, rows int) string {
	if height <= 1 || rows < 1 {
		return ""
	}
	outputBottom := outputBottomForReserve(height, bottomRows)
	if rows > outputBottom {
		rows = outputBottom
	}
	return reserveScrollRegion(1, outputBottom) + reserveMoveTo(1, 1) + fmt.Sprintf("\x1b[%dT", rows)
}

// LegacyReserveDebtANSI pays rows absorbed from a trailing blank before the
// next legacy output write reaches the output bottom.
func LegacyReserveDebtANSI(height, bottomRows, rows int) string {
	if height <= 1 || rows < 1 {
		return ""
	}
	bottom := outputBottomForReserve(height, bottomRows)
	if rows > bottom {
		rows = bottom
	}
	return reserveMoveTo(bottom, 1) + strings.Repeat("\n", rows)
}

func effectiveReserveRows(height, rows int) int {
	if height <= 1 {
		return 1
	}
	if rows < 1 {
		rows = 1
	}
	maxRows := height - 1
	if rows > maxRows {
		return maxRows
	}
	return rows
}

func outputBottomForReserve(height, bottomRows int) int {
	bottom := height - effectiveReserveRows(height, bottomRows)
	if bottom < 1 {
		return 1
	}
	return bottom
}

func reserveMoveTo(row, col int) string {
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}

func reserveScrollRegion(top, bottom int) string {
	if top < 1 {
		top = 1
	}
	if bottom < top {
		bottom = top
	}
	return fmt.Sprintf("\x1b[%d;%dr", top, bottom) + reserveMoveTo(top, 1)
}
