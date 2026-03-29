package ui

import (
	"fmt"
	"strings"
)

// OutputConfig 输出配置
type OutputConfig struct {
	Indent     string  // 缩进前缀
	MaxWidth   int     // 最大宽度
	WordWrap   bool    // 是否自动换行
	Colorize   bool    // 是否启用颜色
	LineNumber bool    // 是否显示行号
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

// FormatOutput 格式化输出文本
func FormatOutput(text string, config *OutputConfig, theme *Theme) string {
	if config == nil {
		config = NewOutputConfig()
	}
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}

	// 分行处理
	lines := strings.Split(text, "\n")

	var result strings.Builder

	for i, line := range lines {
		var formattedLine string

		// 添加行号（如果启用）
		if config.LineNumber {
			lineNum := fmt.Sprintf("%3d ", i+1)
			formattedLine += theme.Dimmed(lineNum)
		}

		// 添加缩进
		formattedLine += config.Indent

		// 自动换行（如果启用）
		if config.WordWrap && config.MaxWidth > 0 {
			wrapped := wrapLine(line, config.MaxWidth-len(config.Indent))
			for j, wl := range wrapped {
				if j > 0 {
					result.WriteString("\n" + wl)
					// 计算续行缩进
					continuationIndent := strings.Repeat(" ", len(config.Indent))
					if config.LineNumber {
						continuationIndent += "    "
					}
					result.WriteString(continuationIndent)
				} else {
					result.WriteString(wl)
				}
			}
		} else {
			result.WriteString(line)
		}

		// 换行
		if i < len(lines)-1 || line == "" {
			result.WriteString("\n")
		}
	}

	return result.String()
}

// wrapLine 自动换行
func wrapLine(line string, maxWidth int) []string {
	if maxWidth <= 0 || len(line) <= maxWidth {
		return []string{line}
	}

	var lines []string
	current := ""

	for _, r := range line {
		if len(current) < maxWidth {
			current += string(r)
		} else {
			lines = append(lines, current)
			current = string(r)
		}
	}

	if current != "" {
		lines = append(lines, current)
	}

	return lines
}

// FormatCodeBlock 格式化代码块
func FormatCodeBlock(code, language string, theme *Theme) string {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}

	if code == "" {
		return ""
	}

	// 语言标签
	var langTag string
	if language != "" {
		langTag = fmt.Sprintf("%s ", language)
	}

	// 分隔符
	separator := fmt.Sprintf("```%s", language)

	// 内容行
	lines := strings.Split(code, "\n")
	var content strings.Builder
	for _, line := range lines {
		content.WriteString(fmt.Sprintf("%s%s\n", strings.Repeat(" ", 2), line))
	}

	// 使用主题颜色构建代码块
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("%s%s%s\n",
		theme.SeparatorColor.Sprint(separator),
		langTag,
		content.String()))
	builder.WriteString(theme.SeparatorColor.Sprint(fmt.Sprintf("%s", separator)))

	return builder.String()
}

// FormatJSON 格式化 JSON 输出（简化版，不实际解析 JSON）
func FormatJSON(jsonStr string) string {
	if strings.TrimSpace(jsonStr) == "" {
		return ""
	}

	// 简单处理：如果 JSON 很长，可以添加缩进
	if strings.Count(jsonStr, "\n") <= 5 {
		return jsonStr
	}

	// 添加基础缩进
	var builder strings.Builder
	indent := 0
	inString := false

	for _, r := range jsonStr {
		switch r {
		case '{', '[':
			if inString {
				builder.WriteRune(r)
			} else {
				builder.WriteRune(r)
				builder.WriteString("\n")
				indent++
				builder.WriteString(strings.Repeat("  ", indent))
			}
		case '}', ']':
			if inString {
				builder.WriteRune(r)
			} else {
				builder.WriteString("\n")
				indent--
				builder.WriteString(strings.Repeat("  ", indent))
				builder.WriteRune(r)
			}
		case ',':
			if inString {
				builder.WriteRune(r)
			} else {
				builder.WriteRune(r)
				builder.WriteString("\n")
				builder.WriteString(strings.Repeat("  ", indent))
			}
		case '"':
			inString = !inString
			builder.WriteRune(r)
		default:
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

// FormatMarkdown 格式化 Markdown 文本到终端（简化版）
func FormatMarkdown(text string) string {
	if text == "" {
		return ""
	}

	// 这里可以添加 Markdown 基础格式化
	// 比如：加粗、列表、代码块等
	// 为了保持简单，暂时直接返回原文本
	return text
}

// Truncate 截断文本
func Truncate(text string, maxLen int, suffix string) string {
	if len(text) <= maxLen {
		return text
	}

	actualLen := maxLen - len(suffix)
	if actualLen < 0 {
		actualLen = 0
	}

	return text[:actualLen] + suffix
}

// HighlightKeywords 高亮关键词
func HighlightKeywords(text string, keywords []string, theme *Theme) string {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}

	if len(keywords) == 0 {
		return text
	}

	result := text
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		// 使用简单的字符串替换（不处理大小写）
		result = strings.ReplaceAll(result, keyword,
			theme.CommandColor.Sprintf("[%s]", keyword))
	}

	return result
}

// PrintFormattedOutput 打印格式化输出
func PrintFormattedOutput(text string, config *OutputConfig) {
	theme := GetTheme(ThemeAuto)
	fmt.Println(FormatOutput(text, config, theme))
}

// PrintCodeBlock 打印代码块
func PrintCodeBlock(code, language string) {
	theme := GetTheme(ThemeAuto)
	fmt.Println(FormatCodeBlock(code, language, theme))
}
