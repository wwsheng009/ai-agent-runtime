package renderengine

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
