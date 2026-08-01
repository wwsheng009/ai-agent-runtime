package viewport

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// RowOwner and PlanRow preserve the historical viewport API while the
// ownership model is implemented by RenderEngine.
type RowOwner = renderengine.RowOwner

const (
	RowOwnerGap        = renderengine.RowOwnerGap
	RowOwnerTranscript = renderengine.RowOwnerTranscript
	RowOwnerBand       = renderengine.RowOwnerBand
	RowOwnerPrompt     = renderengine.RowOwnerPrompt
	RowOwnerPopup      = renderengine.RowOwnerPopup
	RowOwnerStatus     = renderengine.RowOwnerStatus
)

type PlanRow = renderengine.PlanRow

func PlanCells(rows []PlanRow) [][]vt.Cell {
	return renderengine.PlanCells(rows)
}

func ComposePlan(width, height int, history, bottom []PlanRow) []PlanRow {
	return renderengine.ComposePlan(width, height, history, bottom)
}
