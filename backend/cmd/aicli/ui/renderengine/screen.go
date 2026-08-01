package renderengine

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"

// RowOwner identifies the component that owns a physical screen row.
type RowOwner uint8

const (
	// RowOwnerGap represents intentional headroom, margins, or blank reserve.
	RowOwnerGap RowOwner = iota
	RowOwnerTranscript
	RowOwnerBand
	RowOwnerPrompt
	RowOwnerPopup
	RowOwnerStatus
)

// String returns the diagnostic owner name used by the display debug table.
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

// PlanCells extracts the cell grid from an ownership-aware plan.
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

// ComposePlan lays out committed history and the bottom reserve into a full
// screen plan. The bottom reserve owns the last rows; the most recent history
// fills the remaining output rows from the bottom; all other rows are gaps.
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
	for i := 0; i < bottomRows; i++ {
		plan[outputRows+i] = PlanRow{
			Owner: bottom[i].Owner,
			Cells: normalizeRow(bottom[i].Cells, width),
		}
	}

	if outputRows == 0 || len(history) == 0 {
		return plan
	}
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
	return plan
}

// Compose is the cell-only compatibility form of ComposePlan.
func Compose(width, height int, history, bottom [][]vt.Cell) [][]vt.Cell {
	return PlanCells(ComposePlan(width, height, transcriptPlan(history), gapPlan(bottom)))
}

func transcriptPlan(cells [][]vt.Cell) []PlanRow {
	if len(cells) == 0 {
		return nil
	}
	plan := make([]PlanRow, len(cells))
	for i, row := range cells {
		plan[i] = PlanRow{Owner: RowOwnerTranscript, Cells: row}
	}
	return plan
}

func gapPlan(cells [][]vt.Cell) []PlanRow {
	if len(cells) == 0 {
		return nil
	}
	plan := make([]PlanRow, len(cells))
	for i, row := range cells {
		plan[i] = PlanRow{Owner: RowOwnerGap, Cells: row}
	}
	return plan
}
