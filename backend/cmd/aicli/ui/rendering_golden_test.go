package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uidiff "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestRenderingBufferGoldens(t *testing.T) {
	profile := style.ColorProfile{
		ColorProfile: render.TrueColorProfile(),
		Background:   style.BackgroundDark,
	}
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  "monokai",
		Mode:        style.ThemeModeDark,
	}, profile)

	tests := []struct {
		name  string
		build func(width int) render.Document
	}{
		{name: "markdown", build: func(width int) render.Document {
			source := "# 渲染状态\n\n- **完成**: 使用 `aicli`\n- [文档](https://example.com/docs)\n\n| 字段 | 值 |\n| --- | --- |\n| 模式 | structured render |\n\n```go\nfunc main() { fmt.Println(\"hi\") }\n```"
			return markdown.Render(source, markdown.DefaultOptions(width, theme))
		}},
		{name: "diff", build: func(width int) render.Document {
			fd := uiDiffGoldenFixture()
			return uidiff.Document(fd, uidiff.DefaultRenderOptions(width, theme))
		}},
		{name: "theme-preview", build: func(width int) render.Document {
			return ThemePreviewDocument(ThemePreviewOptions{
				Width:       width,
				Palette:     ThemePresetFocus,
				Mode:        ThemeModeDark,
				SyntaxTheme: "monokai",
				Profile:     &profile,
				Compact:     true,
			})
		}},
		{name: "typed-status", build: func(width int) render.Document {
			return style.StatusLineDocument(statusGoldenFixture(), width)
		}},
		{name: "session-info", build: func(width int) render.Document {
			return SessionInfoDocument(SessionInfo{
				ProviderName:     "codex_ee",
				Protocol:         "codex",
				ModelName:        "gpt-5.6-codex",
				EndpointURL:      "https://ai.example.com/v1/responses",
				KeyCount:         2,
				Timeout:          "5m0s",
				IsStream:         true,
				SupportsFast:     true,
				IsFast:           true,
				ReasoningEnabled: true,
			}, width)
		}},
		{name: "info-table", build: func(width int) render.Document {
			return TableDocument(
				[]string{"名称", "状态", "说明"},
				[][]string{
					{"Markdown", "完成", "Goldmark AST + Chroma token spans"},
					{"终端颜色", "进行中", "TrueColor / ANSI-256 / ANSI-16 / NoColor"},
				},
				width,
			)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var golden strings.Builder
			for _, width := range []int{40, 80, 120} {
				golden.WriteString(renderingBufferSnapshot(test.build(width), theme, width))
			}
			assertRenderingGolden(t, test.name, golden.String())
		})
	}
}

func uiDiffGoldenFixture() uidiff.FileDiff {
	return uidiff.FileDiff{
		OldPath: "internal/legacy_renderer.go",
		NewPath: "internal/structured_renderer.go",
		Hunks: []uidiff.Hunk{{
			Header: "@@ -18,3 +18,3 @@ func renderMessage()",
			Lines: []uidiff.DiffLine{
				{Kind: uidiff.LineDelete, OldLineNo: 18, Text: `fmt.Print(color.RedString("失败"))`},
				{Kind: uidiff.LineAdd, NewLineNo: 18, Text: `surface.Write(errorDocument("失败"))`},
				{Kind: uidiff.LineContext, OldLineNo: 19, NewLineNo: 19, Text: "return nil"},
			},
		}},
	}
}

func statusGoldenFixture() style.StatusLineModel {
	return style.StatusLineModel{
		State: style.RunStreaming,
		Segments: []style.StatusSegment{
			{Kind: style.StatusSegModel, Text: "gpt-5.6-codex", Priority: 1, Role: style.RoleAccent},
			{Kind: style.StatusSegPath, Text: "E:/项目/structured-render", Priority: 4, Role: style.RoleTextMuted},
			{Kind: style.StatusSegUsage, Text: "42k/128k", Priority: 2, Role: style.RoleProgress},
			{Kind: style.StatusSegMode, Text: "plan", Priority: 3, Role: style.RoleApproval},
			{Kind: style.StatusSegMeta, Text: "3 tools", Priority: 5, Role: style.RoleTextSecondary},
		},
	}
}

func renderingBufferSnapshot(doc render.Document, theme style.ThemeContext, width int) string {
	doc = style.NewResolver(theme).ResolveDocument(doc)
	buffer := render.BufferBackend{Width: width}
	lines := buffer.Layout(doc)
	var out strings.Builder
	fmt.Fprintf(&out, "[width=%d lines=%d]\n", width, len(lines))
	for index, line := range lines {
		plain := render.PlainBackend{}.Render(render.LinesDoc(line))
		fmt.Fprintf(&out, "%02d plain=%q", index, plain)
		if lineStyle := renderingGoldenStyle(line.Style); lineStyle != "" {
			fmt.Fprintf(&out, " line={%s}", lineStyle)
		}
		out.WriteByte('\n')
		for _, span := range line.Spans {
			fmt.Fprintf(&out, "   span=%q", span.Text)
			if spanStyle := renderingGoldenStyle(span.Style); spanStyle != "" {
				fmt.Fprintf(&out, " {%s}", spanStyle)
			}
			if span.Link != "" {
				fmt.Fprintf(&out, " link=%q", span.Link)
			}
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func renderingGoldenStyle(value render.Style) string {
	parts := make([]string, 0, 8)
	if value.Role != "" {
		parts = append(parts, "role="+value.Role)
	}
	if value.Foreground.IsSet() {
		parts = append(parts, "fg="+renderingGoldenColor(value.Foreground))
	}
	if value.Background.IsSet() {
		parts = append(parts, "bg="+renderingGoldenColor(value.Background))
	}
	if value.Bold {
		parts = append(parts, "bold")
	}
	if value.Dim {
		parts = append(parts, "dim")
	}
	if value.Italic {
		parts = append(parts, "italic")
	}
	if value.Underline {
		parts = append(parts, "underline")
	}
	if value.Reverse {
		parts = append(parts, "reverse")
	}
	return strings.Join(parts, ",")
}

func renderingGoldenColor(value render.Color) string {
	switch value.Kind {
	case render.ColorANSI:
		return fmt.Sprintf("ansi:%d", value.Index)
	case render.ColorIndexed:
		return fmt.Sprintf("idx:%d", value.Index)
	case render.ColorRGB:
		return fmt.Sprintf("#%02x%02x%02x", value.R, value.G, value.B)
	default:
		return "default"
	}
}

func assertRenderingGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "rendering", name+".golden")
	if os.Getenv("AICLI_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", path, err)
		}
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	want := strings.ReplaceAll(string(wantBytes), "\r\n", "\n")
	if got != want {
		t.Fatalf("rendering golden %s changed\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
