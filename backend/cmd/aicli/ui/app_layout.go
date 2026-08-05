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
	ActiveBand       ActiveBandProjection
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
// Before the primary-renderer cutover, a legacy ActiveBand facade input wins so
// AppState shadow frames retain existing visual parity. Unified production sets
// SemanticActiveCellProjection, which makes the mutable Scene/AppState cell the
// exclusive band source. In either mode Layout never overlays both bodies.
func LayoutAppState(state AppState) AppLayout {
	activeBand := ProjectActiveCellBand(state.Active, state.Geometry)
	legacyBand := !state.SemanticActiveCellProjection && hasLegacyActiveBand(state.Bottom)
	bottom := layoutBottomPane(state, activeBand, legacyBand)

	layout := AppLayout{
		Revision:         state.Revision,
		LayoutGeneration: state.LayoutGeneration,
		Geometry:         state.Geometry,
		Lease:            state.Lease,
		Transcript:       state.Transcript.LayoutRows(state.LayoutGeneration),
		Active:           state.Active,
		ActiveBand:       activeBand.Clone(),
		Bottom: BottomPaneLayout{
			State:                bottom.State,
			StatusRows:           bottom.StatusRows,
			DynamicStatusRows:    bottom.DynamicStatusRows,
			PromptNoticeRows:     bottom.PromptNoticeRows,
			ActiveBandRows:       bottom.ActiveBandRows,
			PromptRows:           bottom.PromptRows,
			PopupRows:            bottom.PopupRows,
			VisiblePopupLines:    bottom.VisiblePopupLines,
			PromptTotalRows:      bottom.PromptTotalRows,
			PromptViewportStart:  bottom.PromptViewportStart,
			VisiblePromptLines:   bottom.VisiblePromptLines,
			RowPlan:              bottom.RowPlan,
			CursorFocus:          bottom.CursorFocus,
			LegacyBandProjection: legacyBand,
		},
	}
	return layout
}

type appBottomPaneLayout struct {
	State               BottomPaneState
	StatusRows          int
	DynamicStatusRows   int
	PromptNoticeRows    int
	ActiveBandRows      int
	PromptRows          int
	PopupRows           int
	VisiblePopupLines   []string
	PromptTotalRows     int
	PromptViewportStart int
	VisiblePromptLines  []string
	RowPlan             BottomPaneRowPlan
	CursorFocus         BottomFocus
}

func hasLegacyActiveBand(bottom BottomPaneState) bool {
	return len(bottom.ActiveBandLines) > 0 || len(bottom.ActiveBandStyled) > 0
}

func layoutBottomPane(state AppState, activeBand ActiveBandProjection, legacyBand bool) appBottomPaneLayout {
	bottomSource := state.Bottom
	if state.SemanticActiveCellProjection || (!legacyBand && activeBand.Valid()) {
		bottomSource.ActiveBandLines = nil
		bottomSource.ActiveBandStyled = cloneRenderLines(activeBand.Lines)
	}
	bottom := DeriveBottomPaneState(bottomSource, state.Geometry)
	policy := BottomPanePolicyForGeometry(bottom, state.Geometry)
	return appBottomPaneLayout{
		State:               bottom,
		StatusRows:          bottom.statusVisibleRowCount(),
		DynamicStatusRows:   bottom.dynamicStatusVisibleRowCount(),
		PromptNoticeRows:    bottom.promptNoticeVisibleRowCount(),
		ActiveBandRows:      bottom.activeBandLayoutRowCount(),
		PromptRows:          bottom.promptVisibleRowCount(),
		PopupRows:           bottom.popupVisibleRowCount(state.Geometry.Height),
		VisiblePopupLines:   bottom.VisiblePopupLines(state.Geometry.Height),
		PromptTotalRows:     bottom.PromptTotalRows,
		PromptViewportStart: bottom.PromptViewportStart,
		VisiblePromptLines:  visiblePromptInputLinesFromDerived(bottom, policy),
		RowPlan:             LayoutBottomPaneRows(bottom, state.Geometry),
		CursorFocus:         bottom.Focus,
	}
}
