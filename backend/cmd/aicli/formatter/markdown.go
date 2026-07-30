package formatter

import (
	"os"
	"regexp"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
	"golang.org/x/term"
)

// MarkdownFormatter Markdown 格式化器
type MarkdownFormatter struct {
	useColor bool
	// Width overrides terminal width for layout (0 = auto-detect / 80).
	Width int
	// SyntaxTheme selects a Chroma style name; empty uses auto/default.
	SyntaxTheme string
	// ThemeContextProvider lets UI owners inject the live palette and terminal
	// profile without creating a formatter -> ui package import cycle.
	ThemeContextProvider func() style.ThemeContext
}

// NewMarkdownFormatter 创建新的 Markdown 格式化器
func NewMarkdownFormatter(useColor bool) *MarkdownFormatter {
	return &MarkdownFormatter{useColor: useColor}
}

func (f *MarkdownFormatter) themeContext(syntaxTheme string) style.ThemeContext {
	if f != nil && f.ThemeContextProvider != nil {
		ctx := f.ThemeContextProvider()
		if strings.TrimSpace(syntaxTheme) != "" && !strings.EqualFold(syntaxTheme, "auto") {
			ctx.SyntaxName = syntaxTheme
		}
		if !f.useColor {
			ctx.Terminal.ColorProfile = render.NoColorProfile()
			ctx.UseHyperlink = false
		}
		return ctx
	}
	profile := style.ColorProfile{ColorProfile: render.NoColorProfile()}
	if f != nil && f.useColor {
		profile = style.ColorProfile{
			ColorProfile: render.ColorProfile{
				Enabled:    true,
				Depth:      render.ColorTrueColor,
				Hyperlinks: false,
				Forced:     true,
			},
			Background: style.BackgroundDark,
		}
	}
	return style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  syntaxTheme,
		Mode:        style.ThemeModeDark,
	}, profile)
}

// IsMarkdown 检测文本是否包含 Markdown 格式的元素
func (f *MarkdownFormatter) IsMarkdown(text string) bool {
	if text == "" {
		return false
	}

	// 检查代码块（```）
	if strings.Contains(text, "```") {
		return true
	}

	// 检查内联代码（`）
	if strings.Count(text, "`") >= 2 {
		return true
	}

	// 检查标题（#）
	if strings.Contains(text, "\n# ") || strings.HasPrefix(text, "# ") {
		return true
	}

	// 检查粗体（**）
	if strings.Contains(text, "**") {
		return true
	}

	// 检查列表项（\n- 或 \n* 前缀）
	lines := strings.Split(text, "\n")
	listPrefixRE := regexp.MustCompile(`^\s*[-*]\s+`)
	orderedListPrefixRE := regexp.MustCompile(`^\s*\d+\.\s+`)
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if listPrefixRE.MatchString(line) || orderedListPrefixRE.MatchString(line) {
			return true
		}
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			return true
		}
		if strings.HasPrefix(trimmed, "> ") {
			return true
		}
	}

	// 检查链接（[text](url) 或 <url>）
	linkRE := regexp.MustCompile(`\[.*?\]\(.*?\)`)
	if linkRE.MatchString(text) {
		return true
	}

	// 检查表格（允许前导空格）
	for _, line := range lines {
		if isTableRow(line) {
			return true
		}
	}

	return false
}

// Format 格式化 Markdown 文本到终端输出。
//
// Goldmark AST + Chroma produce structured spans that are encoded only at the
// ANSI/plain backend boundary.
func (f *MarkdownFormatter) Format(text string) string {
	if text == "" {
		return ""
	}

	if !f.IsMarkdown(text) {
		return text
	}

	width := f.Width
	if width <= 0 {
		width = detectFormatterWidth()
	}

	syntaxTheme := strings.TrimSpace(f.SyntaxTheme)
	if syntaxTheme == "" {
		syntaxTheme = syntax.GlobalDefaultTheme()
	}
	theme := f.themeContext(syntaxTheme)

	opts := markdown.AssistantBodyOptions(width, theme)
	opts.SyntaxTheme = syntaxTheme

	doc := markdown.Render(text, opts)
	if !f.useColor {
		return render.PlainBackend{}.Render(doc)
	}
	return style.RenderDocument(doc, theme)
}

// FormatDocument returns the structured render model without ANSI encoding.
func (f *MarkdownFormatter) FormatDocument(text string) render.Document {
	if text == "" || !f.IsMarkdown(text) {
		if text == "" {
			return render.Document{}
		}
		return render.SingleLineDoc(render.TextSpan(text))
	}
	width := f.Width
	if width <= 0 {
		width = detectFormatterWidth()
	}
	syntaxTheme := strings.TrimSpace(f.SyntaxTheme)
	if syntaxTheme == "" {
		syntaxTheme = syntax.GlobalDefaultTheme()
	}
	theme := f.themeContext(syntaxTheme)
	// FormatDocument keeps theme hyperlink policy so structured consumers
	// (OSC 8, tests) can still attach span.Link. Format() uses
	// AssistantBodyOptions which forces Hyperlinks=false for scrollback plain
	// parity with ActiveBand.
	opts := markdown.DefaultOptions(width, theme)
	opts.SyntaxTheme = syntaxTheme
	return markdown.Render(text, opts)
}

func detectFormatterWidth() int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return 80
}

func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

// FormatUserMessage 格式化用户消息（可能包含 Markdown）
func (f *MarkdownFormatter) FormatUserMessage(text string) string {
	if !f.IsMarkdown(text) {
		return text
	}
	return f.Format(text)
}

// GetPlain 提取纯文本（移除 Markdown 语法）
func (f *MarkdownFormatter) GetPlain(text string) string {
	result := text

	// 移除代码块（```）
	codeBlockRE := regexp.MustCompile("```[\\s\\S]*?```")
	result = codeBlockRE.ReplaceAllString(result, " [代码块] ")

	// 移除内联代码 - 修复捕获组问题
	result = regexp.MustCompile("`([^`]+)`").ReplaceAllString(result, "$1")

	// 移除粗体
	result = regexp.MustCompile(`\*\*([^*]+)\*\*`).ReplaceAllString(result, "$1")

	// 移除斜体
	result = regexp.MustCompile(`\*([^*]+)\*`).ReplaceAllString(result, "$1")

	// 移除标题符号
	result = regexp.MustCompile(`(?m)^#+\s+`).ReplaceAllString(result, "")

	// 移除列表符号
	result = regexp.MustCompile(`(?m)^\s*[-*]\s+`).ReplaceAllString(result, "• ")
	result = regexp.MustCompile(`(?m)^\s*\d+\.\s+`).ReplaceAllString(result, "$1. ")

	// 移除引用符号
	result = regexp.MustCompile(`(?m)^\s*>\s+`).ReplaceAllString(result, "")

	// 移除链接语法（保留文本）
	result = regexp.MustCompile(`\[([^\]]+)\]\([^\)]+\)`).ReplaceAllString(result, "$1")

	return result
}
