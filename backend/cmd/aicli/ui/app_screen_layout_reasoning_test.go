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
		Kind:         scene.KindSupplement,
		Source:       source,
		Presentation: scene.TranscriptPresentation{Kind: presentation},
	}
}

// TestCellUsesStructuredPresentationForSupplementMarkdown 验证 supplement
// （reasoning 等）cell 的结构化渲染判定：markdown presentation 或正文兜底
// 命中时走结构化，纯文本保持 plain。
func TestCellUsesStructuredPresentationForSupplementMarkdown(t *testing.T) {
	markdownBody := "# Heading\n\n- **one**\n- `two`"
	divider := strings.Repeat("─", 30) + " reasoning " + strings.Repeat("─", 30)

	cases := []struct {
		name string
		cell scene.TranscriptCell
		want bool
	}{
		{
			name: "marked markdown presentation",
			cell: reasoningTestCell(divider+"\n"+markdownBody, scene.PresentationAssistantMarkdown),
			want: true,
		},
		{
			name: "plain presentation with markdown body fallback",
			cell: reasoningTestCell(divider+"\n"+markdownBody, scene.PresentationPlain),
			want: true,
		},
		{
			name: "plain presentation with plain body",
			cell: reasoningTestCell(divider+"\nplain thinking", scene.PresentationPlain),
			want: false,
		},
	}
	for _, tc := range cases {
		if got := cellUsesStructuredPresentation(tc.cell); got != tc.want {
			t.Fatalf("%s: cellUsesStructuredPresentation = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStructuredSupplementCellRendersMarkdownBodyWithReasoningDividers 验证
// reasoning cell 的结构化渲染：首/尾分隔线保留 reasoning 角色，正文走
// markdown（标题/列表/行内代码有样式，无原始语法残留）。
func TestStructuredSupplementCellRendersMarkdownBodyWithReasoningDividers(t *testing.T) {
	head := strings.Repeat("─", 30) + " reasoning " + strings.Repeat("─", 30)
	tail := strings.Repeat("─", 30) + " end reasoning " + strings.Repeat("─", 30)
	source := head + "\n# Heading\n\n- **one**\n- `two`\n" + tail
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

	// 正文：markdown 语义渲染，无原始语法残留。
	plain := strings.Join(rowsText(rows), "\n")
	for _, want := range []string{"Heading", "one", "two"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structured supplement missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "# Heading") || strings.Contains(plain, "**one**") {
		t.Fatalf("structured supplement retained raw markdown: %q", plain)
	}
}

// TestStructuredSupplementWithoutDividersRendersWholeSource 验证无分隔线的
// supplement（如 priority prompt）整体按正文渲染，不丢内容。
func TestStructuredSupplementWithoutDividersRendersWholeSource(t *testing.T) {
	source := "**prompt** body\n- item"
	cell := reasoningTestCell(source, scene.PresentationAssistantMarkdown)
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

func rowsText(rows []AppScreenRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out
}
