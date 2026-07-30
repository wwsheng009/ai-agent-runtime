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
func Compose(width, height int, history, bottom [][]vt.Cell) [][]vt.Cell {
	width, height = clampSize(width, height)
	frame := blankGrid(width, height)

	bottomRows := len(bottom)
	if bottomRows > height {
		bottomRows = height
	}
	outputRows := height - bottomRows

	// Bottom reserve fills the last bottomRows rows, top-to-bottom.
	for i := 0; i < bottomRows; i++ {
		frame[outputRows+i] = normalizeRow(bottom[i], width)
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
			frame[rowStart+i] = normalizeRow(visible[i], width)
		}
	}
	return frame
}
