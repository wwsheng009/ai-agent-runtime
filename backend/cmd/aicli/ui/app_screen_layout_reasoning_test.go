package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func reasoningTestTheme() style.ThemeContext {
	return style.BuildThemeContext(style.ThemeSelection{Mode: style.ThemeModeDark}, style.ColorProfile{
		ColorProfile: render.ColorProfile{Enabled: true, Depth: render.ColorANSI256},
		Background:   style.BackgroundDark,
	})
}

func reasoningTestCell(source string, presentation scene.PresentationKind) scene.TranscriptCell {
	return scene.TranscriptCell{
		ID:           scene.CellID(7),
		Kind:         scene.KindReasoning,
		Source:       source,
		Phase:        scene.CellCommitted,
		Presentation: scene.TranscriptPresentation{Kind: presentation},
	}
}

// Reasoning is always a structured projection because its dividers are
// presentation chrome. Ordinary supplements retain body-based Markdown
// detection and do not acquire reasoning chrome.
func TestCellUsesStructuredPresentationForReasoning(t *testing.T) {
	markdownBody := "# Heading\n\n- **one**\n- `two`"

	cases := []struct {
		name string
		cell scene.TranscriptCell
		want bool
	}{
		{
			name: "marked markdown presentation",
			cell: reasoningTestCell(markdownBody, scene.PresentationAssistantMarkdown),
			want: true,
		},
		{
			name: "plain presentation with markdown-looking body",
			cell: reasoningTestCell(markdownBody, scene.PresentationPlain),
			want: true,
		},
		{
			name: "plain reasoning still derives chrome",
			cell: reasoningTestCell("plain thinking", scene.PresentationPlain),
			want: true,
		},
		{
			name: "ordinary plain supplement stays plain",
			cell: scene.TranscriptCell{Kind: scene.KindSupplement, Source: "plain supplement"},
			want: false,
		},
	}
	for _, tc := range cases {
		if got := cellUsesStructuredPresentation(tc.cell); got != tc.want {
			t.Fatalf("%s: cellUsesStructuredPresentation = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A committed reasoning cell derives exactly one opening and closing divider;
// the semantic provider body remains literal even when it resembles Markdown.
func TestStructuredReasoningCellRendersSourceFaithfulBodyWithDerivedDividers(t *testing.T) {
	source := "# Heading\n\n- **one**\n- `two`"
	cell := reasoningTestCell(source, scene.PresentationAssistantMarkdown)

	rows := structuredTranscriptScreenRows(cell, 60, reasoningTestTheme())
	if len(rows) < 4 {
		t.Fatalf("structured rows = %d, want >= 4 (head + heading + list + tail)", len(rows))
	}

	// 首行：分隔线，reasoning 角色。
	first := rows[0]
	if !strings.Contains(first.Text, "reasoning") || strings.Contains(first.Text, "end reasoning") {
		t.Fatalf("rows[0].Text = %q, want head divider", first.Text)
	}
	if len(first.RenderLine.Spans) == 0 || first.RenderLine.Spans[0].Style.Role != string(style.RoleReasoning) {
		t.Fatalf("rows[0].RenderLine = %+v, want reasoning role", first.RenderLine)
	}

	// 末行：结束分隔线，reasoning 角色（divider 在窄宽度下会被 wrap 成多段，
	// 这里按语义行查找而非固定取最后一行）。
	var tailRow *AppScreenRow
	for i := range rows {
		if strings.Contains(rows[i].Text, "end reasoning") {
			tailRow = &rows[i]
			break
		}
	}
	if tailRow == nil {
		t.Fatalf("no end divider row found in %q", strings.Join(rowsText(rows), "|"))
	}
	if len(tailRow.RenderLine.Spans) == 0 || tailRow.RenderLine.Spans[0].Style.Role != string(style.RoleReasoning) {
		t.Fatalf("tailRow.RenderLine = %+v, want reasoning role", tailRow.RenderLine)
	}

	// 正文：provider bytes remain literal; reasoning does not pass through the
	// assistant Markdown parser.
	plain := strings.Join(rowsText(rows), "\n")
	for _, want := range []string{"# Heading", "- **one**", "- `two`"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structured reasoning missing %q: %q", want, plain)
		}
	}
	if strings.Count(plain, " end reasoning ") != 1 {
		t.Fatalf("end divider count = %d, want 1: %q", strings.Count(plain, " end reasoning "), plain)
	}
	if strings.Count(strings.ReplaceAll(plain, " end reasoning ", ""), " reasoning ") != 1 {
		t.Fatalf("opening divider count is not exactly 1: %q", plain)
	}
}

// TestStructuredSupplementWithoutDividersRendersWholeSource 验证无分隔线的
// supplement（如 priority prompt）整体按正文渲染，不丢内容。
func TestStructuredSupplementWithoutDividersRendersWholeSource(t *testing.T) {
	source := "**prompt** body\n- item"
	cell := scene.TranscriptCell{
		ID: scene.CellID(8), Kind: scene.KindSupplement, Source: source,
		Phase:        scene.CellCommitted,
		Presentation: scene.TranscriptPresentation{Kind: scene.PresentationAssistantMarkdown},
	}
	rows := structuredTranscriptScreenRows(cell, 60, reasoningTestTheme())
	plain := strings.Join(rowsText(rows), "\n")
	for _, want := range []string{"prompt", "body", "item"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("undivided supplement missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "**prompt**") {
		t.Fatalf("undivided supplement retained raw markdown: %q", plain)
	}
}

func TestReasoningDerivedChromeRespectsLifecycleAndProviderWhitespace(t *testing.T) {
	body := "\n\nrepeat\nrepeat\n"
	cell := reasoningTestCell(body, scene.PresentationPlain)
	cell.Phase = scene.CellMutable

	mutable := strings.Join(rowsText(structuredTranscriptScreenRows(cell, 80, reasoningTestTheme())), "\n")
	if strings.Count(mutable, " reasoning ") != 1 || strings.Contains(mutable, " end reasoning ") {
		t.Fatalf("mutable reasoning chrome = %q, want one opening and no closing divider", mutable)
	}
	if !strings.Contains(mutable, "\n\n\nrepeat\nrepeat\n") {
		t.Fatalf("mutable reasoning did not preserve provider blank/repeated lines: %q", mutable)
	}

	cell.Phase = scene.CellCommitted
	committed := strings.Join(rowsText(structuredTranscriptScreenRows(cell, 80, reasoningTestTheme())), "\n")
	if strings.Count(committed, " end reasoning ") != 1 {
		t.Fatalf("committed reasoning end divider count != 1: %q", committed)
	}
	if strings.Count(committed, "repeat") != 2 {
		t.Fatalf("legitimate repeated body text changed: %q", committed)
	}
}

func TestCommittedReasoningTrailingLFUsesClosingDividerCursorRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{name: "single trailing LF", body: "A\n", want: []string{"A"}},
		{name: "double trailing LF", body: "A\n\n", want: []string{"A", ""}},
		{name: "leading LF", body: "\nA", want: []string{"", "A"}},
		{name: "single LF body", body: "\n", want: []string{""}},
		{name: "double LF body", body: "\n\n", want: []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := rowsText(structuredTranscriptScreenRows(
				reasoningTestCell(tc.body, scene.PresentationPlain),
				100,
				reasoningTestTheme(),
			))
			if len(rows) < 2 {
				t.Fatalf("reasoning rows = %q, want opening/body/closing", rows)
			}
			got := rows[1 : len(rows)-1]
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("body rows = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWhitespaceOnlyReasoningIsNotTreatedAsOrderingPlaceholder(t *testing.T) {
	for _, source := range []string{" ", "\n", "\n\n"} {
		active, ok := activeCellFromTranscriptCell(&scene.TranscriptCell{
			ID: 9, Kind: scene.KindReasoning, Phase: scene.CellMutable, Source: source,
		})
		if !ok || active.Source != source {
			t.Fatalf("whitespace reasoning %q projected as (%+v,%v), want active exact source", source, active, ok)
		}
	}
	if _, ok := activeCellFromTranscriptCell(&scene.TranscriptCell{
		ID: 10, Kind: scene.KindReasoning, Phase: scene.CellMutable,
	}); ok {
		t.Fatal("empty ordering placeholder projected as active reasoning")
	}
}

func rowsText(rows []AppScreenRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out
}
