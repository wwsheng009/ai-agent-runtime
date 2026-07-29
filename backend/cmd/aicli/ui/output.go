package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// OutputConfig 输出配置
type OutputConfig struct {
	Indent     string // 缩进前缀
	MaxWidth   int    // 最大宽度
	WordWrap   bool   // 是否自动换行
	Colorize   bool   // 是否启用颜色
	LineNumber bool   // 是否显示行号
}

// NewOutputConfig 创建新的输出配置
func NewOutputConfig() *OutputConfig {
	return &OutputConfig{
		Indent:     "",
		MaxWidth:   0, // 0 表示不限制
		WordWrap:   false,
		Colorize:   true,
		LineNumber: false,
	}
}

// FormatOutput 格式化输出文本。
//
// Migration note: prefer building a render.Document and encoding via
// render.ANSIBackend / render.PlainBackend. This helper remains for
// legacy call sites and will eventually become a thin adapter.
func FormatOutput(text string, config *OutputConfig, theme *Theme) string {
	if config == nil {
		config = NewOutputConfig()
	}
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}

	doc := FormatOutputDocument(text, config)
	if !config.Colorize {
		return render.PlainBackend{}.Render(doc)
	}
	return renderDocumentWithProfile(doc, theme)
}

// FormatOutputDocument 构建带行号、缩进和终端单元格换行的结构化输出。
func FormatOutputDocument(text string, config *OutputConfig) render.Document {
	if config == nil {
		config = NewOutputConfig()
	}
	safe := strings.ReplaceAll(SanitizeTerminalText(text), "\r", "")
	sourceLines := strings.Split(safe, "\n")
	out := make([]render.Line, 0, len(sourceLines))
	indent := strings.ReplaceAll(strings.ReplaceAll(SanitizeTerminalText(config.Indent), "\r", ""), "\n", " ")
	for i, source := range sourceLines {
		prefix := make([]render.Span, 0, 2)
		prefixWidth := render.Width(indent)
		if config.LineNumber {
			lineNum := fmt.Sprintf("%3d ", i+1)
			prefix = append(prefix, semanticSpan(lineNum, style.RoleTextMuted, false))
			prefixWidth += render.Width(lineNum)
		}
		if indent != "" {
			prefix = append(prefix, semanticSpan(indent, style.RoleTextPrimary, false))
		}
		content := render.Line{Spans: []render.Span{semanticSpan(source, style.RoleTextPrimary, false)}}
		wrapped := []render.Line{content}
		if config.WordWrap && config.MaxWidth > 0 {
			budget := config.MaxWidth - prefixWidth
			if budget < 1 {
				budget = 1
			}
			wrapped = render.Wrap(content, budget, render.WrapOptions{BreakWord: true})
		}
		for j, line := range wrapped {
			if j == 0 {
				line.Spans = append(append([]render.Span{}, prefix...), line.Spans...)
			} else if prefixWidth > 0 {
				line.Spans = append([]render.Span{semanticSpan(strings.Repeat(" ", prefixWidth), style.RoleTextMuted, false)}, line.Spans...)
			}
			out = append(out, line)
		}
	}
	return render.Document{Blocks: []render.Block{{Kind: render.BlockParagraph, Lines: out}}}
}

// FormatCodeBlock 格式化代码块。
//
// Deprecated: migration-period helper. Prefer markdown/syntax renderers that
// emit render.Document. Do not use in new components.
func FormatCodeBlock(code, language string, theme *Theme) string {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	return renderDocumentWithProfile(CodeBlockDocument(code, language), theme)
}

// CodeBlockDocument 使用 Chroma 生成 token spans，不再把 fence 或语言名
// 混入可复制的代码正文。
func CodeBlockDocument(code, language string) render.Document {
	if code == "" {
		return render.Document{}
	}
	safe := strings.ReplaceAll(SanitizeTerminalText(code), "\r", "")
	lines, _ := syntax.Highlight(syntax.HighlightRequest{
		Code:     safe,
		Language: strings.TrimSpace(SanitizeTerminalText(language)),
		Theme:    CurrentResolvedSyntaxThemeName(),
	})
	return render.Document{Blocks: []render.Block{{Kind: render.BlockCode, Lines: lines}}}
}

// FormatJSON pretty-prints JSON using encoding/json.
// On parse failure the original text is returned unchanged (no brace rewriting).
func FormatJSON(jsonStr string) string {
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" {
		return ""
	}

	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return jsonStr
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(parsed); err != nil {
		return jsonStr
	}
	// Encoder always appends a trailing newline; trim for stable callers.
	return strings.TrimSuffix(buf.String(), "\n")
}

// FormatMarkdown formats Markdown for the terminal via Goldmark + Chroma.
//
// Prefer FormatMarkdownDocument when the caller can consume render.Document.
func FormatMarkdown(text string) string {
	if text == "" {
		return ""
	}
	width := GetTerminalWidth()
	return FormatMarkdownWidth(text, width)
}

// FormatMarkdownWidth formats Markdown constrained to width terminal cells.
func FormatMarkdownWidth(text string, width int) string {
	if text == "" {
		return ""
	}
	f := formatter.NewMarkdownFormatter(true)
	f.Width = width
	f.ThemeContextProvider = CurrentThemeContext
	return renderDocumentWithProfile(f.FormatDocument(text), GetTheme(ThemeAuto))
}

// FormatMarkdownDocument returns the structured markdown render model.
func FormatMarkdownDocument(text string, width int) render.Document {
	if text == "" {
		return render.Document{}
	}
	f := formatter.NewMarkdownFormatter(true)
	f.Width = width
	f.ThemeContextProvider = CurrentThemeContext
	return f.FormatDocument(text)
}

// TruncateVisible truncates by terminal cell width.
func TruncateVisible(text string, maxWidth int, marker string) string {
	return render.TruncateText(text, maxWidth, marker)
}

// HighlightKeywords 高亮关键词。
//
// Deprecated: migration-period helper. Prefer typed spans over string replace.
func HighlightKeywords(text string, keywords []string, theme *Theme) string {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}

	doc := KeywordDocument(text, keywords)
	return renderDocumentWithProfile(doc, theme)
}

// KeywordDocument performs deterministic, non-overlapping keyword matching.
// At the same position the longest keyword wins; source order breaks ties.
func KeywordDocument(text string, keywords []string) render.Document {
	safe := strings.ReplaceAll(SanitizeTerminalText(text), "\r", "")
	clean := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = SanitizeTerminalText(keyword)
		if keyword != "" && !strings.Contains(keyword, "\n") {
			clean = append(clean, keyword)
		}
	}
	if len(clean) == 0 {
		return RoleTextDocument(safe, style.RoleTextPrimary)
	}
	parts := strings.Split(safe, "\n")
	lines := make([]render.Line, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, render.Line{Spans: keywordLineSpans(part, clean)})
	}
	return render.LinesDoc(lines...)
}

func keywordLineSpans(safe string, keywords []string) []render.Span {
	var spans []render.Span
	for pos := 0; pos < len(safe); {
		bestAt, bestKeyword := -1, ""
		for _, keyword := range keywords {
			idx := strings.Index(safe[pos:], keyword)
			if idx < 0 {
				continue
			}
			at := pos + idx
			if bestAt < 0 || at < bestAt || (at == bestAt && len(keyword) > len(bestKeyword)) {
				bestAt, bestKeyword = at, keyword
			}
		}
		if bestAt < 0 {
			spans = append(spans, semanticSpan(safe[pos:], style.RoleTextPrimary, false))
			break
		}
		if bestAt > pos {
			spans = append(spans, semanticSpan(safe[pos:bestAt], style.RoleTextPrimary, false))
		}
		spans = append(spans, semanticSpan("["+bestKeyword+"]", style.RoleCommand, true))
		pos = bestAt + len(bestKeyword)
	}
	if len(spans) == 0 {
		spans = append(spans, semanticSpan("", style.RoleTextPrimary, false))
	}
	return spans
}

// PrintFormattedOutput 打印格式化输出
func PrintFormattedOutput(text string, config *OutputConfig) {
	theme := GetTheme(ThemeAuto)
	_, _ = WriteTerminalLine(os.Stdout, FormatOutput(text, config, theme))
}

// PrintCodeBlock 打印代码块
func PrintCodeBlock(code, language string) {
	theme := GetTheme(ThemeAuto)
	_, _ = WriteTerminalLine(os.Stdout, FormatCodeBlock(code, language, theme))
}
