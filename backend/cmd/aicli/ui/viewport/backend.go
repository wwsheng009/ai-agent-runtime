// Package viewport provides compatibility aliases for the historical P5
// viewport API. The owned screen model now lives in ui/renderengine.
package viewport

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// Backend is the historical name for renderengine.ScreenModel.
type Backend = renderengine.ScreenModel

// New creates an owned screen model through the historical viewport API.
func New(width, height int) *Backend {
	return renderengine.NewScreenModel(width, height)
}

// blankGrid remains package-private test support for viewport compatibility
// tests; the production implementation is owned by renderengine.
func blankGrid(width, height int) [][]vt.Cell {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	grid := make([][]vt.Cell, height)
	for row := range grid {
		grid[row] = make([]vt.Cell, width)
	}
	return grid
}
