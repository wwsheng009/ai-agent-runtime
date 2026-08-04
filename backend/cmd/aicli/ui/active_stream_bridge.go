package ui

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// ActiveCellStateFromSourceSnapshot maps the legacy controller's semantic
// source view into the AppState active-cell shape. Identity and physical
// effect progress are explicit inputs: CommittedEnd is deliberately ignored
// because it is controller-local progress, not a confirmed terminal Ack.
//
// The function is pure and is intended for a future coordinator/Scene bridge.
// It does not read terminal state, rendered rows, or any controller mutex.
func ActiveCellStateFromSourceSnapshot(snapshot ActiveStreamSourceSnapshot, id scene.CellID, revision uint64, enqueuedEnd, ackedEnd int) (ActiveCellState, error) {
	if !snapshot.Active {
		return ActiveCellState{}, fmt.Errorf("%w: inactive source snapshot", ErrInvalidActiveCellRanges)
	}
	if id == 0 || revision == 0 {
		return ActiveCellState{}, fmt.Errorf("%w: missing active-cell identity", ErrInvalidActiveCellRanges)
	}

	kind, ok := activeSceneKind(snapshot.Kind)
	if !ok {
		return ActiveCellState{}, fmt.Errorf("%w: unsupported active kind %d", ErrInvalidActiveCellRanges, snapshot.Kind)
	}
	if enqueuedEnd < 0 || ackedEnd < 0 || ackedEnd > enqueuedEnd {
		return ActiveCellState{}, ErrInvalidActiveCellRanges
	}
	if (snapshot.Kind == cell.ActiveTool || snapshot.Kind == cell.ActiveStatus) &&
		(snapshot.Source != "" || snapshot.StableEnd != 0 || enqueuedEnd != 0 || ackedEnd != 0) {
		// A running tool/status cell is an overlay. It has no transcript source
		// range, even when its viewport display contains text. Reasoning is a
		// semantic supplement and therefore does carry source text.
		return ActiveCellState{}, fmt.Errorf("%w: overlay carries transcript range", ErrInvalidActiveCellRanges)
	}
	if snapshot.StableEnd < 0 || snapshot.StableEnd > len(snapshot.Source) ||
		enqueuedEnd > snapshot.StableEnd || ackedEnd > snapshot.StableEnd ||
		!activeCellSourceBoundary(snapshot.Source, snapshot.StableEnd) ||
		!activeCellSourceBoundary(snapshot.Source, enqueuedEnd) ||
		!activeCellSourceBoundary(snapshot.Source, ackedEnd) {
		return ActiveCellState{}, ErrInvalidActiveCellRanges
	}

	active := ActiveCellState{
		CellID:   id,
		Revision: revision,
		Kind:     kind,
		Phase:    ActiveCellMutable,
		Source:   snapshot.Source,
		Stable:   SourceRange{Start: 0, End: snapshot.StableEnd},
		Enqueued: SourceRange{Start: 0, End: enqueuedEnd},
		Acked:    SourceRange{Start: 0, End: ackedEnd},
	}
	if err := active.ValidateStreamingRanges(); err != nil {
		return ActiveCellState{}, err
	}
	return active, nil
}

// UpdateActiveCellActionFromSourceSnapshot builds a fenced coalescable update
// from the current AppState active cell. Existing effect ranges are carried
// forward from AppState; no controller-local committed cursor is consulted.
func UpdateActiveCellActionFromSourceSnapshot(current ActiveCellState, snapshot ActiveStreamSourceSnapshot) (UpdateActiveCellAction, error) {
	if current.CellID == 0 || current.Phase == ActiveCellInactive || current.Revision == 0 {
		return UpdateActiveCellAction{}, fmt.Errorf("%w: no mounted active cell", ErrInvalidActiveCellRanges)
	}
	enqueuedEnd, ackedEnd := current.Enqueued.End, current.Acked.End
	if !strings.HasPrefix(snapshot.Source, current.Source) {
		// The reducer recognizes the replacement and invalidates the prior
		// ledger. Do the same before mapping so old byte offsets cannot make an
		// otherwise valid correction fail local validation.
		enqueuedEnd, ackedEnd = 0, 0
	}
	active, err := ActiveCellStateFromSourceSnapshot(
		snapshot,
		current.CellID,
		current.Revision+1,
		enqueuedEnd,
		ackedEnd,
	)
	if err != nil {
		return UpdateActiveCellAction{}, err
	}
	return UpdateActiveCellAction{
		ExpectedCellID:   current.CellID,
		ExpectedRevision: current.Revision,
		Active:           active,
	}, nil
}

func activeSceneKind(kind cell.ActiveKind) (scene.CellKind, bool) {
	switch kind {
	case cell.ActiveAssistant:
		return scene.KindAssistant, true
	case cell.ActiveReasoning:
		return scene.KindSupplement, true
	case cell.ActiveTool:
		return scene.KindToolChain, true
	default:
		return 0, false
	}
}
