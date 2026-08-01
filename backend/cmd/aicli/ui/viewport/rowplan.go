package viewport

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"

// RowOwner identifies which component owns a physical screen row. The owner
// table is recomputed by ComposePlan on every frame; no component may claim a
// row outside the layout solver. This is the stage C fix for the
// viewport/history/band coordination defects: every row is either owned by a
// component or explicitly a Gap, so "undeclared rows" cannot exist.
type RowOwner uint8

const (
	// RowOwnerGap is a blank row reserved by a component (margins, top gap,
	// popup gap) or unused output headroom. Gap is the zero value so an
	// unannotated row never masquerades as content.
	RowOwnerGap RowOwner = iota
	// RowOwnerTranscript is a committed history row visible in the output
	// region.
	RowOwnerTranscript
	// RowOwnerBand is an ActiveBand row.
	RowOwnerBand
	// RowOwnerPrompt is a prompt/notice/composer input row.
	RowOwnerPrompt
	// RowOwnerPopup is a popup content row.
	RowOwnerPopup
	// RowOwnerStatus is the fixed status row or the dynamic status animation
	// row.
	RowOwnerStatus
)

// String renders the owner name for diagnostics and the /debug display table.
func (o RowOwner) String() string {
	switch o {
	case RowOwnerTranscript:
		return "transcript"
	case RowOwnerBand:
		return "band"
	case RowOwnerPrompt:
		return "prompt"
	case RowOwnerPopup:
		return "popup"
	case RowOwnerStatus:
		return "status"
	default:
		return "gap"
	}
}

// PlanRow is one physical screen row plus its ownership annotation.
type PlanRow struct {
	Owner RowOwner
	Cells []vt.Cell
}

// PlanCells extracts the cell grids from a plan, dropping ownership.
func PlanCells(rows []PlanRow) [][]vt.Cell {
	if len(rows) == 0 {
		return nil
	}
	cells := make([][]vt.Cell, len(rows))
	for i, row := range rows {
		cells[i] = row.Cells
	}
	return cells
}

// ComposePlan lays out committed history and the bottom reserve into a
// full-screen (width x height) frame with an ownership annotation per row. It
// is Compose plus the owner table: the bottom reserve (status / prompt /
// popup / active band) occupies the last len(bottom) rows top-to-bottom, the
// output region above shows the most recent history rows bottom-aligned, and
// every other row is annotated RowOwnerGap.
//
// The layout rules are identical to Compose (same P5.2 grow/shrink semantics:
// nothing is scrolled, a grow/shrink cycle re-composes from the same owned
// history). Callers annotate history rows RowOwnerTranscript and bottom rows
// with their component owner; ComposePlan fills gaps and unused headroom with
// RowOwnerGap.
func ComposePlan(width, height int, history, bottom []PlanRow) []PlanRow {
	width, height = clampSize(width, height)
	plan := make([]PlanRow, height)
	for i := range plan {
		plan[i] = PlanRow{Owner: RowOwnerGap, Cells: make([]vt.Cell, width)}
	}

	bottomRows := len(bottom)
	if bottomRows > height {
		bottomRows = height
	}
	outputRows := height - bottomRows

	// Bottom reserve fills the last bottomRows rows, top-to-bottom.
	for i := 0; i < bottomRows; i++ {
		plan[outputRows+i] = PlanRow{
			Owner: bottom[i].Owner,
			Cells: normalizeRow(bottom[i].Cells, width),
		}
	}

	// Output region shows the most recent history rows, top-aligned.
	if outputRows > 0 && len(history) > 0 {
		start := 0
		if len(history) > outputRows {
			start = len(history) - outputRows
		}
		visible := history[start:]
		rowStart := outputRows - len(visible)
		if rowStart < 0 {
			rowStart = 0
		}
		for i := 0; i < len(visible) && rowStart+i < outputRows; i++ {
			plan[rowStart+i] = PlanRow{
				Owner: visible[i].Owner,
				Cells: normalizeRow(visible[i].Cells, width),
			}
		}
	}
	return plan
}

func toTranscriptPlan(cells [][]vt.Cell) []PlanRow {
	if len(cells) == 0 {
		return nil
	}
	plan := make([]PlanRow, len(cells))
	for i, row := range cells {
		plan[i] = PlanRow{Owner: RowOwnerTranscript, Cells: row}
	}
	return plan
}

func toGapPlan(cells [][]vt.Cell) []PlanRow {
	if len(cells) == 0 {
		return nil
	}
	plan := make([]PlanRow, len(cells))
	for i, row := range cells {
		plan[i] = PlanRow{Owner: RowOwnerGap, Cells: row}
	}
	return plan
}
