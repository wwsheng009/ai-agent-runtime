package formatter

import (
	"os"
	"regexp"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
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
	// Highlighter overrides the syntax highlighter for assistant-body renders
	// (nil = syntax.Default). ActiveBand injects a throttled instance here so
	// the band path stays on the single Formatter render path.
	Highlighter syntax.Highlighter
	// AssistantBody applies the shared assistant-body contract (Hyperlinks off,
	// TableMode auto) used by scrollback replay and the live band.
	AssistantBody bool
	// HideHighlightFallback hides large-block skip labels; ActiveBand sets this
	// so the live viewport stays clean.
	HideHighlightFallback bool
	// TrustMarkdown skips IsMarkdown detection; ActiveBand sets this because
	// its own markdown flag already decided the path.
	TrustMarkdown bool
	// Cache is the shared RenderCache (阶段 D). nil falls back to the
	// process-wide SharedRenderCache; pass an explicit cache to bypass.
	Cache *renderengine.RenderCache
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

	// 检查粗体（**）：只认"成对的、中间有内容的 ** 定界符"。裸星号串
	// （****、***** 分隔线）和未闭合的 ** 不算 markdown——纯文本 reasoning
	// 里的星号串会因此被误判进 markdown 渲染路径，被 goldmark 当作未闭合
	// 强调解析，造成 "****" 符号与换行被吞掉/泄漏。
	if hasClosedBoldPair(text) {
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

// hasClosedBoldPair 报告 text 中是否存在成对的粗体定界符：** 后跟非 * 的
// 内容，并在之后再次出现闭合的 **。** 后紧跟 * 属于更长的星号串（如 ****），
// 不是粗体起止符；未闭合的 ** 也不计数。
func hasClosedBoldPair(text string) bool {
	for i := 0; ; {
		open := strings.Index(text[i:], "**")
		if open < 0 {
			return false
		}
		open += i
		rest := text[open+2:]
		if rest == "" || strings.HasPrefix(rest, "*") {
			// 裸星号串/未闭合：从该 ** 之后继续找下一对。
			i = open + 2
			continue
		}
		if close := strings.Index(rest, "**"); close >= 0 {
			return true
		}
		i = open + 2
	}
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

	// Same shared RenderCache as FormatDocument/ActiveBand; "plain" mode keeps
	// this options group (AssistantBodyOptions, Hyperlinks forced off) distinct
	// from the structured "assistant" group.
	doc, _ := renderengine.SharedRenderCache().Render("plain", text, opts)
	if !f.useColor {
		return render.PlainBackend{}.Render(doc)
	}
	return style.RenderDocument(doc, theme)
}

// FormatDocument returns the structured render model without ANSI encoding.
func (f *MarkdownFormatter) FormatDocument(text string) render.Document {
	doc, _ := f.FormatDocumentCached(text)
	return doc
}

// FormatDocumentCached returns the structured render model, serving hits from
// the shared RenderCache when possible. The bool reports cache hit: false
// means the document was (re)built this call.
func (f *MarkdownFormatter) FormatDocumentCached(text string) (render.Document, bool) {
	if text == "" || (!f.TrustMarkdown && !f.IsMarkdown(text)) {
		if text == "" {
			return render.Document{}, false
		}
		return render.SingleLineDoc(render.TextSpan(text)), false
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
	if f.Highlighter != nil {
		opts.Highlighter = f.Highlighter
	}
	mode := "assistant"
	if f.AssistantBody {
		opts.ApplyAssistantBodyContract()
		mode = "band"
	}
	if f.HideHighlightFallback {
		opts.HideHighlightFallback = true
	}
	cache := f.Cache
	if cache == nil {
		cache = renderengine.SharedRenderCache()
	}
	return cache.Render(mode, text, opts)
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
