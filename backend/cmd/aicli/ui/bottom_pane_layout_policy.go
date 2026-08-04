package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// BottomPaneGeometryPolicy is the geometry-derived portion of the bottom-pane
// layout contract. It contains no terminal cache or renderer state, so Layout
// can derive the same allocation from an AppState snapshot after a resize.
//
// The legacy FixedBottomSurface still owns physical painting during Phase 2.
// This policy intentionally shares its sizing rules without reading its mutex.
type BottomPaneGeometryPolicy struct {
	Width                  int
	Height                 int
	ActiveBandMaxRows      int
	ActiveBandTopGapRows   int
	PromptTopMarginRows    int
	PromptBottomMarginRows int
	PromptMaxVisibleRows   int
}

// BottomPanePolicyForGeometry calculates all row-budget constants from the
// immutable geometry and bottom semantic inputs. Width has a conservative
// fallback because text wrapping requires a column count; a zero height stays
// unknown and preserves the legacy minimum-editor policy until a measured
// Resize action arrives.
func BottomPanePolicyForGeometry(bottom BottomPaneState, geometry GeometryState) BottomPaneGeometryPolicy {
	width := geometry.Width
	if width < 1 {
		width = 80
	}

	policy := BottomPaneGeometryPolicy{
		Width:                width,
		Height:               geometry.Height,
		ActiveBandMaxRows:    ActiveBandRows(geometry.Height),
		ActiveBandTopGapRows: activeBandTopGap(geometry.Height),
	}
	policy.PromptTopMarginRows, policy.PromptBottomMarginRows = chatComposerVerticalMargins(geometry.Height)
	policy.PromptMaxVisibleRows = promptInputMaxVisibleRowsForGeometry(bottom, policy)
	return policy
}

// DeriveBottomPaneState applies the pure geometry policy to a detached
// BottomPaneState. The returned value is a display snapshot: ActiveBand rows,
// prompt viewport, and margins are projections, never additional AppState
// authority. Callers must retain the original BottomPaneState as their
// semantic source.
func DeriveBottomPaneState(bottom BottomPaneState, geometry GeometryState) BottomPaneState {
	bottom = bottom.Clone()
	policy := BottomPanePolicyForGeometry(bottom, geometry)

	bottom.ActiveBandMaxRows = policy.ActiveBandMaxRows
	bottom.ActiveBandTopGapRows = policy.ActiveBandTopGapRows
	bottom.PromptTopMarginRows = policy.PromptTopMarginRows
	bottom.PromptBottomMarginRows = policy.PromptBottomMarginRows

	if len(bottom.ActiveBandStyled) > 0 {
		bottom.ActiveBandStyled = normalizeActiveBandStyledLines(bottom.ActiveBandStyled, policy.Width, policy.ActiveBandMaxRows)
		bottom.ActiveBandLines = render.PlainBackend{}.RenderLines(render.LinesDoc(bottom.ActiveBandStyled...))
	} else {
		bottom.ActiveBandLines = normalizeActiveBandLines(bottom.ActiveBandLines, policy.Width, policy.ActiveBandMaxRows)
	}

	derivePromptViewportForGeometry(&bottom, policy)
	return bottom
}

func promptInputMaxVisibleRowsForGeometry(bottom BottomPaneState, policy BottomPaneGeometryPolicy) int {
	if policy.Height <= 0 {
		return ChatComposerMaxVisibleRows
	}

	dynamicRows := 0
	if bottom.DynamicStatusModel != nil {
		dynamicRows = 1
	}
	activeRows := len(bottom.ActiveBandLines)
	if len(bottom.ActiveBandStyled) > activeRows {
		activeRows = len(bottom.ActiveBandStyled)
	}
	if activeRows > policy.ActiveBandMaxRows {
		activeRows = policy.ActiveBandMaxRows
	}
	if bottom.composerVisibleRowCount() > 0 {
		activeRows = 0
	}
	rows := policy.Height - 1 - 1 - 1 -
		policy.PromptTopMarginRows - policy.PromptBottomMarginRows -
		dynamicRows - len(bottom.promptNoticeLines()) - activeRows
	if rows < 1 {
		return 1
	}
	if rows > ChatComposerMaxVisibleRows {
		return ChatComposerMaxVisibleRows
	}
	return rows
}

func derivePromptViewportForGeometry(bottom *BottomPaneState, policy BottomPaneGeometryPolicy) {
	if bottom == nil {
		return
	}
	if !bottom.PromptVisible && bottom.PromptReservedRows < 1 {
		bottom.PromptTotalRows = 0
		bottom.PromptViewportStart = 0
		bottom.PromptCursorAbsoluteRow = 0
		bottom.PromptCursorRow = 0
		return
	}

	input := []rune(bottom.PromptInput)
	startCol := terminalVisibleWidth(bottom.PromptLine)
	totalRows := interactiveInputDisplayRows(input, startCol, policy.Width)
	if bottom.PromptRowsOverride > 0 {
		totalRows = bottom.PromptRowsOverride
	}
	if totalRows < 1 {
		totalRows = 1
	}

	cursorRow := bottom.PromptCursorAbsoluteRow
	cursorCol := bottom.PromptCursorCol
	if bottom.PromptCursorKnown {
		position := interactiveInputVisualPosition(input, bottom.PromptCursor, startCol, policy.Width)
		cursorRow = position.row
		cursorCol = position.col
	}
	if cursorRow < 0 {
		cursorRow = 0
	}
	if cursorRow >= totalRows {
		cursorRow = totalRows - 1
	}
	if cursorCol < 0 {
		cursorCol = 0
	}

	visibleRows := totalRows
	if visibleRows > policy.PromptMaxVisibleRows {
		visibleRows = policy.PromptMaxVisibleRows
	}
	if visibleRows < 1 {
		visibleRows = 1
	}
	start := boundedInteractiveInputViewportStart(totalRows, cursorRow, visibleRows, bottom.PromptViewportStart)

	bottom.PromptTotalRows = totalRows
	bottom.PromptReservedRows = visibleRows
	bottom.PromptViewportStart = start
	bottom.PromptCursorAbsoluteRow = cursorRow
	bottom.PromptCursorRow = cursorRow - start
	bottom.PromptCursorCol = cursorCol
}

// VisiblePromptInputLines returns the plain text rows that a future Compose
// stage will place into the allocated prompt viewport. It deliberately keeps
// styling and cursor movement out of Layout.
func VisiblePromptInputLines(bottom BottomPaneState, geometry GeometryState) []string {
	bottom = DeriveBottomPaneState(bottom, geometry)
	return visiblePromptInputLinesFromDerived(bottom, BottomPanePolicyForGeometry(bottom, geometry))
}

func visiblePromptInputLinesFromDerived(bottom BottomPaneState, policy BottomPaneGeometryPolicy) []string {
	if bottom.composerVisibleRowCount() > 0 || bottom.promptVisibleRowCount() < 1 {
		return nil
	}
	text := renderInteractiveInputViewport(
		bottom.PromptLine,
		[]rune(bottom.PromptInput),
		policy.Width,
		bottom.PromptViewportStart,
		bottom.promptVisibleRowCount(),
	)
	if text == "" {
		return nil
	}
	return strings.Split(text, "\r\n")
}
