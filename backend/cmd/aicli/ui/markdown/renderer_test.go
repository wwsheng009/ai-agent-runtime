package markdown

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func testOpts(width int) Options {
	profile := style.ColorProfile{ColorProfile: render.NoColorProfile()}
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, profile)
	return DefaultOptions(width, theme)
}

func TestRenderHeadingListLinkCode(t *testing.T) {
	src := "# 标题\n\n一段 **粗体** 和 `code` 与 [链接](https://example.com)。\n\n- 项目一\n- 项目二\n\n```go\nfunc Hello() {}\n```\n"
	doc := Render(src, testOpts(80))
	plain := render.PlainBackend{}.Render(doc)
	if !strings.Contains(plain, "标题") {
		t.Fatalf("missing heading: %q", plain)
	}
	if !strings.Contains(plain, "• 项目") {
		t.Fatalf("missing list: %q", plain)
	}
	if !strings.Contains(plain, "链接") {
		t.Fatalf("missing link label: %q", plain)
	}
	if !strings.Contains(plain, "https://example.com") {
		t.Fatalf("expected visible URL fallback: %q", plain)
	}
	if !strings.Contains(plain, "Hello") {
		t.Fatalf("missing code: %q", plain)
	}
	if strings.Contains(plain, "```") {
		t.Fatalf("fence markers should not appear: %q", plain)
	}
}

func TestRenderNestedListCJK(t *testing.T) {
	src := "1. 第一层\n   - 中文子项很长很长需要换行处理路径 /usr/local/very/long/path/to/file.go\n   - 另一项\n2. 第二层\n"
	for _, width := range []int{40, 80, 120} {
		doc := Render(src, testOpts(width))
		plainLines := (render.PlainBackend{}).RenderLines(doc)
		for _, line := range plainLines {
			if render.Width(line) > width {
				t.Fatalf("width %d overflow on %q (w=%d)", width, line, render.Width(line))
			}
		}
	}
}

func TestRenderTableNoOverflow(t *testing.T) {
	src := "| Name | Path |\n| --- | --- |\n| a | /very/long/path/to/something/important/file.go |\n| 中文 | C:\\\\Users\\\\Someone\\\\Project\\\\module |\n"
	for _, width := range []int{40, 80, 120} {
		doc := Render(src, testOpts(width))
		plainLines := (render.PlainBackend{}).RenderLines(doc)
		if len(plainLines) == 0 {
			t.Fatalf("empty table at width %d", width)
		}
		for _, line := range plainLines {
			if render.Width(line) > width {
				t.Fatalf("table overflow width=%d line=%q w=%d", width, line, render.Width(line))
			}
		}
		joined := strings.Join(plainLines, "\n")
		if !strings.Contains(joined, "Name") && !strings.Contains(joined, "a") {
			t.Fatalf("lost table content at width %d: %q", width, joined)
		}
	}
}

func TestRenderTableRecordsOnNarrow(t *testing.T) {
	src := "| ColA | ColB | ColC |\n| --- | --- | --- |\n| 1 | 2 | 3 |\n"
	doc := Render(src, testOpts(40))
	plain := render.PlainBackend{}.Render(doc)
	// Narrow should prefer records (Key: Value).
	if !strings.Contains(plain, "ColA") {
		t.Fatalf("missing header key: %q", plain)
	}
}

func TestRenderQuoteAndRule(t *testing.T) {
	src := "> quoted text\n\n---\n\npara"
	doc := Render(src, testOpts(60))
	plain := render.PlainBackend{}.Render(doc)
	if !strings.Contains(plain, "│") {
		t.Fatalf("missing quote gutter: %q", plain)
	}
	if !strings.Contains(plain, "─") {
		t.Fatalf("missing rule: %q", plain)
	}
}

func TestRenderANSIHasTokenColors(t *testing.T) {
	profile := style.ColorProfile{
		ColorProfile: render.ColorProfile{Enabled: true, Depth: render.ColorTrueColor},
		Background:   style.BackgroundDark,
	}
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
		SyntaxName:  "monokai",
	}, profile)
	opts := DefaultOptions(80, theme)
	out := RenderANSI("```go\nfunc Hi() {}\n```", opts)
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI in highlighted code, got %q", out)
	}
	if strings.Contains(out, "38;2") || strings.Contains(out, "38;5") || strings.Contains(out, "\x1b[1;") || strings.Contains(out, "\x1b[3") {
		// any color channel is fine
	} else if !strings.Contains(out, "Hi") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestMarkdownFenceUnwrap(t *testing.T) {
	src := "```md\n# Hello\n- one\n```"
	plain := RenderPlain(src, testOpts(80))
	if strings.Contains(plain, "```") {
		t.Fatalf("should unwrap md fence: %q", plain)
	}
	if !strings.Contains(plain, "Hello") || !strings.Contains(plain, "• one") {
		t.Fatalf("inner markdown missing: %q", plain)
	}
}

func TestRenderCanHideIntentionalHighlightFallbackLabel(t *testing.T) {
	highlighter := syntax.NewChromaHighlighter()
	highlighter.Limits = syntax.Limits{MaxBytes: 8, MaxLines: 2}
	opts := testOpts(80)
	opts.Highlighter = highlighter
	opts.HideHighlightFallback = true
	plain := RenderPlain("```go\nfunc main() {}\n```", opts)
	if !strings.Contains(plain, "func main") {
		t.Fatalf("fallback lost code content: %q", plain)
	}
	if strings.Contains(plain, "limit_exceeded") {
		t.Fatalf("expected intentional active fallback label to stay hidden: %q", plain)
	}
}

func TestRenderSeparatesBlocksWithBlankLine(t *testing.T) {
	src := "第一段。\n\n第二段。\n\n- 项目一\n- 项目二\n\n```go\nfunc Hello() {}\n```\n\n收尾段落。\n"
	plain := RenderPlain(src, testOpts(60))
	wants := []string{
		"第一段。\n\n第二段。",
		"第二段。\n\n• 项目一",
		"• 项目一\n• 项目二",
		"• 项目二\n\nfunc Hello() {}",
		"func Hello() {}\n\n收尾段落。",
	}
	for _, want := range wants {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q in\n%s", want, plain)
		}
	}
	if strings.HasPrefix(plain, "\n") || strings.HasSuffix(plain, "\n") {
		t.Fatalf("document should not start or end with a blank line: %q", plain)
	}
}

func TestRenderSpacingNoneKeepsLegacyDenseLayout(t *testing.T) {
	src := "第一段。\n\n第二段。\n"
	opts := testOpts(60)
	opts.Spacing = SpacingNone
	plain := RenderPlain(src, opts)
	if plain != "第一段。\n第二段。" {
		t.Fatalf("dense render = %q", plain)
	}
}

func TestRenderHeadingKeepsBlankLineBeforeBody(t *testing.T) {
	plain := RenderPlain("# 标题\n\n正文内容。\n", testOpts(60))
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 || lines[1] != "" {
		t.Fatalf("expected heading, blank, body; got %q", lines)
	}
}

func TestRenderNestedMarkdownFenceKeepsSingleBlankLine(t *testing.T) {
	plain := RenderPlain("```md\n# 内标题\n\n内正文\n```", testOpts(60))
	if strings.Contains(plain, "\n\n\n") {
		t.Fatalf("nested markdown produced duplicated blank lines: %q", plain)
	}
	if !strings.Contains(plain, "内标题\n\n内正文") {
		t.Fatalf("nested markdown lost block spacing: %q", plain)
	}
}
