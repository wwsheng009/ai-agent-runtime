package ui

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"

// AppLayout is the pure Phase 2 layout result. It is intentionally terminal
// agnostic: no ANSI bytes, front buffer, terminal cursor, or effect progress
// appear here. Presenter/TerminalSession will later turn it into a frame plan.
type AppLayout struct {
	Revision         uint64
	LayoutGeneration uint64
	Geometry         GeometryState
	Lease            LeaseState
	Transcript       []scene.LayoutRow
	Active           ActiveCellState
	Bottom           BottomPaneLayout
}

// BottomPaneLayout records deterministic row allocation and cursor intent for
// the bottom overlay. State is copied so layout consumers cannot mutate the
// AppState snapshot from which this result was derived.
type BottomPaneLayout struct {
	State                BottomPaneState
	StatusRows           int
	DynamicStatusRows    int
	PromptNoticeRows     int
	ActiveBandRows       int
	PromptRows           int
	PopupRows            int
	VisiblePopupLines    []string
	PromptTotalRows      int
	PromptViewportStart  int
	VisiblePromptLines   []string
	RowPlan              BottomPaneRowPlan
	CursorFocus          BottomFocus
	LegacyBandProjection bool
}

// LayoutAppState derives semantic transcript rows and bottom-pane allocation
// from one immutable AppState snapshot. It must remain free of terminal I/O,
// live surface reads, effects, timers, and mutable global state.
//
// ActiveBand is explicitly marked LegacyBandProjection while the Phase 2
// adapter still receives it from old facade actions. Once streaming source is
// fully migrated, this input is replaced by a display projection of Active.
func LayoutAppState(state AppState) AppLayout {
	state = state.Clone()
	height := state.Geometry.Height
	bottom := DeriveBottomPaneState(state.Bottom, state.Geometry)

	layout := AppLayout{
		Revision:         state.Revision,
		LayoutGeneration: state.LayoutGeneration,
		Geometry:         state.Geometry,
		Lease:            state.Lease,
		Transcript:       scene.LayoutTranscript(state.Transcript.Snapshot().Cells, state.LayoutGeneration),
		Active:           state.Active.Clone(),
		Bottom: BottomPaneLayout{
			State:                bottom,
			StatusRows:           bottom.statusVisibleRowCount(),
			DynamicStatusRows:    bottom.dynamicStatusVisibleRowCount(),
			PromptNoticeRows:     bottom.promptNoticeVisibleRowCount(),
			ActiveBandRows:       bottom.activeBandLayoutRowCount(),
			PromptRows:           bottom.promptVisibleRowCount(),
			PopupRows:            bottom.popupVisibleRowCount(height),
			VisiblePopupLines:    bottom.VisiblePopupLines(height),
			PromptTotalRows:      bottom.PromptTotalRows,
			PromptViewportStart:  bottom.PromptViewportStart,
			VisiblePromptLines:   VisiblePromptInputLines(bottom, state.Geometry),
			RowPlan:              LayoutBottomPaneRows(bottom, state.Geometry),
			CursorFocus:          bottom.Focus,
			LegacyBandProjection: len(bottom.ActiveBandLines) > 0 || len(bottom.ActiveBandStyled) > 0,
		},
	}
	return layout
}
