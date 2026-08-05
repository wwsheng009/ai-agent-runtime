package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

var (
	ErrInvalidActiveCellRanges = errors.New("invalid active-cell streaming ranges")
	ErrStaleActiveCellUpdate   = errors.New("stale active-cell update")
)

// StreamingRangesKnown reports whether an ActiveCellState carries the complete
// source-range ledger. During the migration all-zero ranges are allowed for
// Scene-derived snapshots, because those snapshots intentionally do not guess
// the coordinator's effect progress. An update cannot silently erase a known
// queued-but-unacked range; source replacement must explicitly reset it.
func (s ActiveCellState) StreamingRangesKnown() bool {
	return s.Stable != (SourceRange{}) || s.Enqueued != (SourceRange{}) || s.Acked != (SourceRange{})
}

// ValidateStreamingRanges enforces the only legal ordering for one active
// source owner. Ranges are byte offsets in the same immutable Source string;
// each range is a prefix boundary, not a terminal row or rune count.
func (s ActiveCellState) ValidateStreamingRanges() error {
	if s.CellID == 0 || s.Phase == ActiveCellInactive {
		return fmt.Errorf("%w: inactive identity", ErrInvalidActiveCellRanges)
	}
	if !s.StreamingRangesKnown() {
		return nil
	}
	length := len(s.Source)
	for name, r := range map[string]SourceRange{
		"stable":   s.Stable,
		"enqueued": s.Enqueued,
		"acked":    s.Acked,
	} {
		if !r.Valid() || r.Start != 0 || r.End > length {
			return fmt.Errorf("%w: %s=%+v source_len=%d", ErrInvalidActiveCellRanges, name, r, length)
		}
	}
	if s.Acked.End > s.Enqueued.End || s.Enqueued.End > s.Stable.End || s.Stable.End > length {
		return fmt.Errorf("%w: acked=%+v enqueued=%+v stable=%+v source_len=%d", ErrInvalidActiveCellRanges, s.Acked, s.Enqueued, s.Stable, length)
	}
	for _, r := range []SourceRange{s.Stable, s.Enqueued, s.Acked} {
		if !activeCellSourceBoundary(s.Source, r.End) {
			return fmt.Errorf("%w: range ends inside UTF-8 rune: %+v", ErrInvalidActiveCellRanges, r)
		}
	}
	return nil
}

func sourceRangePrefix(source, prefix string) bool {
	return prefix == "" || strings.HasPrefix(source, prefix)
}

// deriveActiveStableEnd rebuilds the largest source prefix whose presentation
// no longer depends on an incomplete trailing Markdown construct. Scene owns
// the raw mutable source but does not carry renderer progress, so the reducer
// derives this boundary instead of leaving production Active.Stable at zero.
func deriveActiveStableEnd(source string) int {
	if source == "" {
		return 0
	}
	if !markdown.LooksLikeMarkdown(source) {
		return len(source)
	}
	var collector markdown.StreamCollector
	_ = collector.SetContent(source)
	return len(collector.Stable())
}

func normalizeActiveStableRange(active ActiveCellState, minimum int) ActiveCellState {
	stableEnd := deriveActiveStableEnd(active.Source)
	if active.Stable.Start == 0 && active.Stable.End > stableEnd {
		// An adapter with a stronger streaming parser may explicitly release a
		// prefix. Never move a producer-owned stable boundary backwards.
		stableEnd = active.Stable.End
	}
	if minimum > stableEnd {
		stableEnd = minimum
	}
	active.Stable = SourceRange{Start: 0, End: stableEnd}
	return active
}

// AdvanceActiveSource applies one source revision to the same mutable cell.
// It is the pure transition that a future Scene/stream bridge can use before
// emitting UpdateActiveCellAction. A prefix extension preserves effect ranges;
// a replacement clears them because old byte offsets no longer identify the
// same semantic content.
func AdvanceActiveSource(active ActiveCellState, revision uint64, source string, stableEnd int) (ActiveCellState, error) {
	if active.CellID == 0 || active.Phase == ActiveCellInactive || revision <= active.Revision {
		return active, ErrStaleActiveCellUpdate
	}
	if stableEnd < 0 || stableEnd > len(source) || !activeCellSourceBoundary(source, stableEnd) {
		return active, ErrInvalidActiveCellRanges
	}
	next := active.Clone()
	next.Revision = revision
	next.Source = source
	if !sourceRangePrefix(source, active.Source) {
		next.Stable = SourceRange{}
		next.Enqueued = SourceRange{}
		next.Acked = SourceRange{}
	}
	if next.StreamingRangesKnown() && (next.Enqueued.End > stableEnd || next.Acked.End > stableEnd) {
		return active, ErrInvalidActiveCellRanges
	}
	next.Stable = SourceRange{Start: 0, End: stableEnd}
	return next, next.ValidateStreamingRanges()
}

// MarkActiveEnqueued advances the one owner-held queued source boundary. It
// cannot cross stable content or move backwards.
func MarkActiveEnqueued(active ActiveCellState, end int) (ActiveCellState, error) {
	if err := active.ValidateStreamingRanges(); err != nil {
		return active, err
	}
	if end < active.Enqueued.End || end > active.Stable.End || !activeCellSourceBoundary(active.Source, end) {
		return active, ErrInvalidActiveCellRanges
	}
	active.Enqueued = SourceRange{Start: 0, End: end}
	return active, nil
}

// MarkActiveAcked advances only after the physical handoff has been confirmed.
// Ack cannot pass enqueued content, so a short write or deferred transaction
// leaves the source visible in the active projection.
func MarkActiveAcked(active ActiveCellState, end int) (ActiveCellState, error) {
	if err := active.ValidateStreamingRanges(); err != nil {
		return active, err
	}
	if end < active.Acked.End || end > active.Enqueued.End || !activeCellSourceBoundary(active.Source, end) {
		return active, ErrInvalidActiveCellRanges
	}
	active.Acked = SourceRange{Start: 0, End: end}
	return active, nil
}

func reduceActiveCellUpdate(state *UIControllerState, action UpdateActiveCellAction) error {
	if state == nil {
		return ErrStaleActiveCellUpdate
	}
	next := action.Active.Clone()
	if next.CellID == 0 || next.Phase == ActiveCellInactive {
		return ErrInvalidActiveCellRanges
	}
	current := state.Active
	if current.CellID == 0 {
		if action.ExpectedCellID != 0 || action.ExpectedRevision != 0 {
			return ErrStaleActiveCellUpdate
		}
		if state.SemanticActiveCellProjection {
			next = normalizeActiveStableRange(next, next.Enqueued.End)
		}
		if err := next.ValidateStreamingRanges(); err != nil {
			return err
		}
		state.Active = next
		return nil
	}
	if current.CellID != action.ExpectedCellID || current.Revision != action.ExpectedRevision ||
		next.CellID != current.CellID || next.Revision <= current.Revision {
		return ErrStaleActiveCellUpdate
	}
	if !sourceRangePrefix(next.Source, current.Source) {
		// Pending bytes can be safely rebased before a write, but acknowledged
		// bytes are already native scrollback. Preserve that physical frontier
		// only when the correction leaves its exact source prefix unchanged.
		ackedEnd := current.Acked.End
		if ackedEnd > len(next.Source) ||
			(ackedEnd > 0 && next.Source[:ackedEnd] != current.Source[:ackedEnd]) {
			state.HistoryEffects.ProjectionUnknown = true
			ackedEnd = 0
		}
		next.Acked = SourceRange{Start: 0, End: ackedEnd}
		next.Enqueued = next.Acked
		next.Stable = SourceRange{}
	} else if !next.StreamingRangesKnown() && current.StreamingRangesKnown() {
		// A producer may omit ranges only for a Scene-derived snapshot. An
		// active update cannot erase a known queued-but-unacked range.
		return ErrInvalidActiveCellRanges
	}
	if state.SemanticActiveCellProjection {
		next = normalizeActiveStableRange(next, next.Enqueued.End)
	}
	if err := next.ValidateStreamingRanges(); err != nil {
		return err
	}
	state.Active = next
	return nil
}

func updateActiveCellActionKey(id scene.CellID) string {
	return "active-cell/" + fmt.Sprint(uint64(id))
}
