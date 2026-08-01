package renderengine

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"

// Composer is the renderengine facade for deterministic owned-screen layout.
// It is stateless because geometry and source-backed rows are supplied per
// frame; SceneState remains the next migration boundary.
type Composer struct{}

// NewComposer creates a stateless Composer. Geometry and row contents are
// supplied per call, so one instance can be shared by an Engine.
func NewComposer() *Composer {
	return &Composer{}
}

// ComposePlan delegates to the ownership-aware full-screen layout solver.
func (c *Composer) ComposePlan(width, height int, history, bottom []PlanRow) []PlanRow {
	return ComposePlan(width, height, history, bottom)
}

// Compose is the cell-only compatibility form used by snapshot tests.
func (c *Composer) Compose(width, height int, history, bottom [][]vt.Cell) [][]vt.Cell {
	return Compose(width, height, history, bottom)
}
