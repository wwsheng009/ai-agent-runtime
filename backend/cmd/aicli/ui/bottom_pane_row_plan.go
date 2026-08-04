package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// BottomPaneRow is one plain-text ownership row in the reserved bottom area.
// It is a pure Layout artifact, not a terminal cell buffer or an ANSI command.
// Row is one-based to match the terminal coordinate system used by the legacy
// adapter and the future Presenter transaction.
type BottomPaneRow struct {
	Row   int
	Owner renderengine.RowOwner
	Text  string
}

// BottomPaneRowPlan describes the bottom reserve portion of an AppLayout. It
// intentionally excludes transcript/history rows: those require the future
// semantic active/history composition and tokenized handoff work.
type BottomPaneRowPlan struct {
	Rows            []BottomPaneRow
	StatusRow       int
	OutputBottomRow int
}

// LayoutBottomPaneRows composes the bottom pane into plain text and ownership
// rows from an immutable snapshot. It has no terminal I/O, no surface read,
// no ANSI rendering, and no effect progression. This is the Phase 2 parity
// target for overlays before the full history/active Frame Compose migration.
func LayoutBottomPaneRows(bottom BottomPaneState, geometry GeometryState) BottomPaneRowPlan {
	if geometry.Height < 1 {
		return BottomPaneRowPlan{}
	}

	bottom = DeriveBottomPaneState(bottom, geometry)
	policy := BottomPanePolicyForGeometry(bottom, geometry)
	height := geometry.Height
	statusRow := height
	popupLines := bottom.VisiblePopupLines(height)
	popupRows := len(popupLines) + bottom.composerVisibleRowCount()
	bottomRows := bottomPaneReservedRowCount(bottom, len(popupLines))
	if height <= 1 {
		bottomRows = 1
	} else if bottomRows > height-1 {
		bottomRows = height - 1
	}
	if bottomRows < 1 {
		bottomRows = 1
	}
	firstRow := height - bottomRows + 1
	outputBottom := height - bottomRows
	if outputBottom < 1 {
		outputBottom = 1
	}

	rows := make(map[int]BottomPaneRow, bottomRows)
	for row := firstRow; row <= statusRow; row++ {
		rows[row] = BottomPaneRow{Row: row, Owner: renderengine.RowOwnerGap}
	}
	setRow := func(row int, owner renderengine.RowOwner, text string) {
		if row < firstRow || row > statusRow {
			return
		}
		// Terminal snapshots contain empty cells after the last visible glyph.
		// Preserve that projection in the plain plan instead of treating a
		// trailing input space as a painted cell that legacy output does not own.
		rows[row] = BottomPaneRow{Row: row, Owner: owner, Text: strings.TrimRight(text, " ")}
	}

	// Popup paints after the prompt in the legacy text path. Owner allocation is
	// still resolved by the explicit layer rules below; normal layouts do not
	// overlap their reserved ranges.
	promptBottom := bottomPanePromptBottomRow(bottom, height, outputBottom, len(popupLines))
	popupStart := bottomPanePopupStartRow(bottom, height, promptBottom, popupRows)
	for index, line := range popupLines {
		setRow(popupStart+index, renderengine.RowOwnerPopup, truncateFixedPopupLine(line, policy.Width))
	}
	if composer := bottom.composerLineText(); composer != "" {
		setRow(popupStart+len(popupLines), renderengine.RowOwnerPopup, truncateFixedPopupLine(composer, policy.Width))
	}

	if bottom.composerVisibleRowCount() == 0 {
		layoutBottomPanePromptRows(setRow, bottom, policy, height, outputBottom, promptBottom)
	}
	setRow(statusRow, renderengine.RowOwnerStatus, bottomPaneStatusPlainText(bottom.StatusModel, policy.Width))

	plan := BottomPaneRowPlan{
		Rows:            make([]BottomPaneRow, 0, bottomRows),
		StatusRow:       statusRow,
		OutputBottomRow: outputBottom,
	}
	for row := firstRow; row <= statusRow; row++ {
		plan.Rows = append(plan.Rows, rows[row])
	}
	return plan
}

func bottomPaneReservedRowCount(bottom BottomPaneState, visiblePopupLines int) int {
	rows := 1 + visiblePopupLines
	if bottom.popupExpandsBelowPrompt() {
		rows += bottom.promptAreaVisibleRowCount()
	} else {
		rows += bottom.composerVisibleRowCount() + bottom.popupBottomGapRowCount()
	}
	return rows
}

func bottomPanePromptBottomRow(bottom BottomPaneState, height, outputBottom, visiblePopupLines int) int {
	statusRow := height
	if bottom.popupExpandsBelowPrompt() {
		rows := bottom.promptAreaVisibleRowCount()
		if rows < 1 {
			return outputBottom
		}
		row := outputBottom + rows - bottom.promptBottomMarginRowCount()
		return clampBottomPaneRow(row, statusRow)
	}
	if bottom.composerVisibleRowCount() > 0 {
		popupRows := visiblePopupLines + bottom.composerVisibleRowCount()
		row := bottomPanePopupStartRow(bottom, height, 0, popupRows) + visiblePopupLines
		return clampBottomPaneRow(row, statusRow)
	}
	if bottom.popupInputGapRowCount() > 0 || bottom.promptReservedRowCount() > 0 || bottom.dynamicStatusVisibleRowCount() > 0 || bottom.activeBandVisibleRowCount() > 0 {
		return clampBottomPaneRow(statusRow-1-bottom.promptBottomMarginRowCount(), statusRow)
	}
	return outputBottom
}

func bottomPanePopupStartRow(bottom BottomPaneState, height, promptBottom, rows int) int {
	statusRow := height
	if bottom.popupExpandsBelowPrompt() {
		row := promptBottom + bottom.promptBottomMarginRowCount() + 1
		return clampBottomPaneRow(row, statusRow)
	}
	row := statusRow - bottom.popupInputGapRowCount() - rows
	if row < 1 {
		return 1
	}
	return row
}

func clampBottomPaneRow(row, statusRow int) int {
	if row < 1 {
		return 1
	}
	if row >= statusRow {
		return statusRow - 1
	}
	return row
}

func layoutBottomPanePromptRows(setRow func(int, renderengine.RowOwner, string), bottom BottomPaneState, policy BottomPaneGeometryPolicy, height, outputBottom, promptBottom int) {
	promptRows := bottom.promptVisibleRowCount()
	noticeRows := bottom.promptNoticeVisibleRowCount()
	dynamicRows := bottom.dynamicStatusVisibleRowCount()
	activeRows := bottom.activeBandVisibleRowCount()
	topMarginRows := bottom.promptTopMarginRowCount()
	bottomMarginRows := bottom.promptBottomMarginRowCount()
	if promptRows < 1 && noticeRows < 1 && dynamicRows < 1 && activeRows < 1 {
		return
	}
	if promptRows > 0 {
		maxRows := promptBottom - outputBottom - dynamicRows - noticeRows - bottom.activeBandLayoutRowCount() - topMarginRows
		if maxRows < 1 {
			maxRows = 1
		}
		if promptRows > maxRows {
			promptRows = maxRows
		}
	}

	promptStart := promptBottom + bottomMarginRows + 1
	if promptRows > 0 {
		promptStart = promptBottom - promptRows + 1
	}
	topMarginStart := promptStart - topMarginRows
	dynamicStart := topMarginStart - dynamicRows
	noticeStart := dynamicStart - noticeRows
	activeStart := noticeStart - activeRows

	band := bottom.ActiveBandLines
	if len(band) > activeRows {
		band = band[len(band)-activeRows:]
	}
	for index, line := range band {
		setRow(activeStart+index, renderengine.RowOwnerBand, truncateFixedPopupLine(line, policy.Width))
	}

	for index, line := range bottom.promptNoticeLines() {
		if index >= noticeRows {
			break
		}
		setRow(noticeStart+index, renderengine.RowOwnerPrompt, truncateFixedPopupLine(line, policy.Width))
	}
	if dynamicRows > 0 && bottom.DynamicStatusModel != nil {
		setRow(dynamicStart, renderengine.RowOwnerStatus, bottomPaneStatusPlainText(bottom.DynamicStatusModel, policy.Width))
	}

	for index, line := range visiblePromptInputLinesFromDerived(bottom, policy) {
		if index >= promptRows {
			break
		}
		setRow(promptStart+index, renderengine.RowOwnerPrompt, line)
	}
}

func bottomPaneStatusPlainText(model *style.StatusLineModel, width int) string {
	value := style.StatusLineModel{State: style.RunReady}
	if model != nil {
		value = *model
	}
	return truncateFixedPopupLine(style.StatusLineDocument(value, width).PlainText(), width)
}
