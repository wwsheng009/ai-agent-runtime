package viewport

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// Compose preserves the historical cell-only viewport API.
func Compose(width, height int, history, bottom [][]vt.Cell) [][]vt.Cell {
	return renderengine.Compose(width, height, history, bottom)
}
