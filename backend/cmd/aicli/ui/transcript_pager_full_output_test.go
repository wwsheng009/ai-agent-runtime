package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTranscriptPagerRowsKeepFullToolResultText locks the recovery path behind
// the collapsed transcript's display-only fold: the fold Ctrl+T points at
// (the most recent one) opens expanded in the alternate-screen pager, so the
// text the fold hid is never lost. If this ever regresses, the marker's
// "(display only)" wording becomes a lie and the user is left with no way to
// read the rest of the newest tool result.
func TestTranscriptPagerRowsKeepFullToolResultText(t *testing.T) {
	const rows = 80
	lines := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		lines = append(lines, fmt.Sprintf("row-%02d", i))
	}
	model := TranscriptPagerModel{
		Cells: []scene.TranscriptCell{committedPagerToolCell(1, strings.Join(lines, "\n"))},
	}

	rendered := make([]string, 0, rows)
	for _, row := range model.Rows(80) {
		rendered = append(rendered, row.Text)
	}
	text := strings.Join(rendered, "\n")

	for _, want := range []string{"row-00", "row-40", "row-79"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pager dropped %q from a long tool result:\n%s", want, text)
		}
	}
	if strings.Contains(text, "display only") {
		t.Fatalf("pager rendered a fold marker instead of the full text:\n%s", text)
	}
}
