package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// diffLabelTestTheme builds a deterministic dark theme for layout tests.
func diffLabelTestTheme(t *testing.T) style.ThemeContext {
	t.Helper()
	profile := style.ColorProfile{
		ColorProfile: render.TrueColorProfile(),
		Background:   style.BackgroundDark,
	}
	return style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  "monokai",
		Mode:        style.ThemeModeDark,
	}, profile)
}

// TestStructuredTranscriptDiffSupplementLabel pins the layout half of the
// read-only git diff fix: a PresentationDiffSupplement cell whose source is a
// raw unified diff must render its file header with the encoder-annotated
// verb (Presentation.DiffLabel) instead of the diff renderer's default
// "Edited". Supplement text keeps its own embedded label.
func TestStructuredTranscriptDiffSupplementLabel(t *testing.T) {
	rawDiff := strings.Join([]string{
		"--- a/internal/gateway.go",
		"+++ b/internal/gateway.go",
		"@@ -1,2 +1,2 @@",
		" context.Background(),",
		"-value := 1",
		"+value := 2",
	}, "\n")
	theme := diffLabelTestTheme(t)

	cases := []struct {
		name      string
		diffLabel string
		source    string
		want      string
		notWant   string
	}{
		{
			name:      "raw diff honors DiffLabel Diff",
			diffLabel: "Diff",
			source:    rawDiff,
			want:      "• Diff internal/gateway.go",
			notWant:   "• Edited internal/gateway.go",
		},
		{
			name:      "raw diff honors DiffLabel Edited",
			diffLabel: "Edited",
			source:    rawDiff,
			want:      "• Edited internal/gateway.go",
			notWant:   "• Diff internal/gateway.go",
		},
		{
			name:      "empty DiffLabel keeps renderer default",
			diffLabel: "",
			source:    rawDiff,
			want:      "• Edited internal/gateway.go",
			notWant:   "",
		},
		{
			name:      "supplement text keeps embedded label",
			diffLabel: "",
			source:    "• Diff app.go (+1 -1)\n      1 -     old\n      1 +     new",
			want:      "• Diff app.go",
			notWant:   "• Edited app.go",
		},
	}
	for _, tc := range cases {
		cell := scene.TranscriptCell{
			ID:   scene.CellID(1),
			Kind: scene.KindSupplement,
			Presentation: scene.TranscriptPresentation{
				Kind:      scene.PresentationDiffSupplement,
				DiffLabel: tc.diffLabel,
			},
			Source: tc.source,
		}
		rows := structuredTranscriptScreenRows(cell, 120, theme)
		var got strings.Builder
		for _, row := range rows {
			got.WriteString(row.Text)
			got.WriteString("\n")
		}
		if !strings.Contains(got.String(), tc.want) {
			t.Fatalf("case %q: rows missing %q:\n%s", tc.name, tc.want, got.String())
		}
		if tc.notWant != "" && strings.Contains(got.String(), tc.notWant) {
			t.Fatalf("case %q: rows must not contain %q:\n%s", tc.name, tc.notWant, got.String())
		}
	}
}
