package viewport

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"

// Compose lays out committed history and the bottom reserve into a full-screen
// (width x height) frame for a Backend.
//
// The bottom reserve (status / prompt / popup / active band) occupies the last
// len(bottom) rows, top-to-bottom. The output region above shows the most recent
// history rows, top-aligned: growing the reserve hides the OLDEST rows and
// shrinking it RESTORES them, because the caller owns the full history list.
//
// This is the P5.2 fix for the immediate-mode compensation defect: the old path
// scrolled history into scrollback on band growth (irreversible) and then
// painted blank rows at the screen top on shrink. Here nothing is scrolled — a
// grow/shrink cycle re-composes from the same owned history, so the top history
// rows are identical across frames and the Backend diff leaves them untouched.
//
// When history is shorter than the output region it is bottom-aligned. This
// matches transcript semantics: the newest committed row stays adjacent to the
// active band/prompt, while unused headroom remains above older content.
// Compose is ComposePlan without ownership: it returns the cell grid only.
// Kept for compatibility and content-level assertions; production frames go
// through ComposePlan so the owner table stays authoritative.
func Compose(width, height int, history, bottom [][]vt.Cell) [][]vt.Cell {
	return PlanCells(ComposePlan(width, height, toTranscriptPlan(history), toGapPlan(bottom)))
}
