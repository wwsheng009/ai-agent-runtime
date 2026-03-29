package formatter

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/fatih/color"
)

// MarkdownFormatter Markdown 格式化器
type MarkdownFormatter struct {
	useColor bool
}

var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// NewMarkdownFormatter 创建新的 Markdown 格式化器
func NewMarkdownFormatter(useColor bool) *MarkdownFormatter {
	return &MarkdownFormatter{useColor: useColor}
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
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if listPrefixRE.MatchString(line) {
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

// Format 格式化 Markdown 文本到终端输出
func (f *MarkdownFormatter) Format(text string) string {
	if text == "" {
		return ""
	}

	if !f.IsMarkdown(text) {
		return text
	}

	// 按行处理
	lines := normalizeBrokenMarkdownTables(strings.Split(text, "\n"))
	var result strings.Builder

	i := 0
	for i < len(lines) {
		line := lines[i]

		// 处理代码块（```lang ... ```）
		if isFenceStart(line) {
			i = f.handleCodeBlock(lines, i, &result)
			continue
		}

		// 处理表格（| | |）
		if f.isTableRow(line) {
			i = f.handleTable(lines, i, &result)
			continue
		}

		// 处理空行
		if strings.TrimSpace(line) == "" {
			result.WriteString("\n")
			i++
			continue
		}

		// 处理单行 Markdown 元素
		formattedLine := f.formatLine(line)
		result.WriteString(formattedLine + "\n")
		i++
	}

	return strings.TrimRight(result.String(), "\n")
}

func normalizeBrokenMarkdownTables(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}

	normalized := make([]string, 0, len(lines))
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			inTable = false
			normalized = append(normalized, line)
			continue
		}

		if inTable && isBrokenTableContinuation(trimmed) && len(normalized) > 0 {
			normalized[len(normalized)-1] = strings.TrimRight(normalized[len(normalized)-1], " \t") + " " + trimmed
			continue
		}

		normalized = append(normalized, line)
		if len(normalized) >= 2 && isMarkdownTableHeader(normalized[len(normalized)-2], normalized[len(normalized)-1]) {
			inTable = true
			continue
		}
		if inTable && !isTableRow(line) && !isTableSeparatorLine(line) {
			inTable = false
		}
	}
	return normalized
}

func isBrokenTableContinuation(line string) bool {
	return !strings.HasPrefix(line, "|") && strings.Contains(line, "|") && strings.HasSuffix(line, "|")
}

func isMarkdownTableHeader(headerLine, separatorLine string) bool {
	return isTableRow(headerLine) && isTableSeparatorLine(separatorLine)
}

func isTableSeparatorLine(line string) bool {
	trimmed := strings.TrimSpace(strings.Trim(line, "|"))
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, "|")
	for _, part := range parts {
		if strings.Trim(part, " :-") == "" {
			continue
		}
		if strings.Trim(part, " :-\n") != "" {
			return false
		}
	}
	return true
}

func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

// handleCodeBlock 处理代码块
func (f *MarkdownFormatter) handleCodeBlock(lines []string, i int, result *strings.Builder) int {
	_, language := parseFenceStart(lines[i])
	i++

	var codeLines []string
	for i < len(lines) {
		if isFenceEnd(lines[i]) {
			i++
			break
		}
		codeLines = append(codeLines, lines[i])
		i++
	}

	if isMarkdownFence(language) {
		formatted := f.Format(strings.Join(codeLines, "\n"))
		if formatted != "" {
			result.WriteString(formatted)
			result.WriteString("\n")
		}
		return i
	}

	formatted := f.formatCodeBlock(codeLines, language)
	result.WriteString(formatted)
	return i
}

func isMarkdownFence(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "markdown", "md":
		return true
	default:
		return false
	}
}

// isTableRow 判断是否为表格行
func (f *MarkdownFormatter) isTableRow(line string) bool {
	return isTableRow(line)
}

// handleTable 处理表格
func (f *MarkdownFormatter) handleTable(lines []string, i int, result *strings.Builder) int {
	// 收集表格行
	var tableLines []string
	for i < len(lines) && f.isTableRow(lines[i]) {
		// 跳过分隔行（|---|---|）
		if !f.isTableSeparator(lines[i]) {
			tableLines = append(tableLines, lines[i])
		}
		i++
	}

	if len(tableLines) < 2 {
		// 不是有效的表格，正常处理
		for _, line := range tableLines {
			result.WriteString(line + "\n")
		}
		return i
	}

	// 解析表格
	formatted := f.formatTable(tableLines)
	result.WriteString(formatted)
	return i
}

// isTableSeparator 判断是否为表格分隔行
func (f *MarkdownFormatter) isTableSeparator(line string) bool {
	return isTableSeparatorLine(line)
}

// formatTable 格式化表格
func (f *MarkdownFormatter) formatTable(lines []string) string {
	// 解析表头和内容
	headers := f.parseTableRow(lines[0])
	var rows [][]string
	for _, line := range lines[1:] {
		rows = append(rows, f.parseTableRow(line))
	}

	if len(headers) == 0 {
		return ""
	}

	// 计算每列宽度
	colWidths := make([]int, len(headers))
	for j, header := range headers {
		colWidths[j] = displayWidth(header)
	}
	for _, row := range rows {
		for j, cell := range row {
			if j < len(colWidths) {
				width := displayWidth(cell)
				if width > colWidths[j] {
					colWidths[j] = width
				}
			}
		}
	}

	var result strings.Builder

	// 输出表头
	result.WriteString(f.formatTableRow(headers, colWidths, true))

	// 输出分隔线
	result.WriteString(f.formatTableSeparator(colWidths))

	// 输出内容行
	for _, row := range rows {
		result.WriteString(f.formatTableRow(row, colWidths, false))
	}

	return result.String()
}

// parseTableRow 解析表格行
func (f *MarkdownFormatter) parseTableRow(line string) []string {
	// 移除首尾的 |
	trimmed := strings.Trim(line, "|")
	if trimmed == "" {
		return nil
	}

	// 分割并清理单元格
	parts := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cell := strings.TrimSpace(part)
		// 处理单元格内的 inline markdown
		cell = f.formatInline(cell)
		cells = append(cells, cell)
	}

	return cells
}

// formatTableRow 格式化表格行
func (f *MarkdownFormatter) formatTableRow(cells []string, widths []int, isHeader bool) string {
	if len(widths) == 0 {
		return ""
	}

	var buf bytes.Buffer

	for j := 0; j < len(widths); j++ {
		cell := ""
		if j < len(cells) {
			cell = cells[j]
		}
		if j > 0 {
			buf.WriteString(" │ ")
		}

		maxWidth := widths[j]
		padded := padRightVisible(cell, maxWidth)
		if f.useColor {
			if isHeader {
				buf.WriteString(color.New(color.FgCyan, color.Bold).Sprint(padded))
			} else {
				buf.WriteString(padded)
			}
		} else {
			buf.WriteString(padded)
		}
	}

	buf.WriteString("\n")
	return buf.String()
}

// formatTableSeparator 格式化表格分隔线
func (f *MarkdownFormatter) formatTableSeparator(widths []int) string {
	if len(widths) == 0 {
		return ""
	}

	var buf bytes.Buffer
	for j, w := range widths {
		if j > 0 {
			buf.WriteString("─┼─")
		}
		dashes := "─"
		for i := 1; i < w; i++ {
			dashes += "─"
		}
		if f.useColor {
			buf.WriteString(color.New(color.FgHiBlack).Sprint(dashes))
		} else {
			buf.WriteString(dashes)
		}
	}

	buf.WriteString("\n")
	return buf.String()
}

// formatLine 格式化单行 Markdown
func (f *MarkdownFormatter) formatLine(line string) string {
	// 处理标题（#）
	if strings.HasPrefix(line, "### ") {
		return f.formatHeading(line[4:], 3)
	} else if strings.HasPrefix(line, "## ") {
		return f.formatHeading(line[3:], 2)
	} else if strings.HasPrefix(line, "# ") {
		return f.formatHeading(line[2:], 1)
	}

	// 处理引用（>）
	if strings.HasPrefix(line, "> ") {
		return f.formatQuote(line[2:])
	}

	// 处理有序列表（1. 2. 3.）
	if matched, _ := regexp.MatchString(`^\s*\d+\.\s+`, line); matched {
		return f.formatOrderedList(line)
	}

	// 处理无序列表（- 或 *）
	listRE := regexp.MustCompile(`^(\s*)([-*])\s+(.*)`)
	matches := listRE.FindStringSubmatch(line)
	if len(matches) >= 4 {
		indent := matches[1]
		return indent + f.formatListItem(matches[3])
	}

	// 处理普通行中的 Markdown 元素
	return f.formatInline(line)
}

// formatHeading 格式化标题
func (f *MarkdownFormatter) formatHeading(text string, level int) string {
	if !f.useColor {
		prefix := ""
		for i := 0; i < level; i++ {
			prefix += "#"
		}
		return fmt.Sprintf("%s %s", prefix, f.formatInline(text))
	}

	headingText := text
	switch level {
	case 1:
		return color.New(color.FgCyan, color.Bold).Sprint("▶ ") +
			color.New(color.FgCyan, color.Bold).Sprintf("%s", headingText)
	case 2:
		return color.New(color.FgCyan, color.Bold).Sprint("▷ ") +
			color.New(color.FgCyan, color.Bold).Sprintf("%s", headingText)
	case 3:
		return color.New(color.FgBlue, color.Bold).Sprint("◉ ") +
			color.New(color.FgBlue, color.Bold).Sprintf("%s", headingText)
	default:
		return color.New(color.FgBlue, color.Bold).Sprintf("%s", headingText)
	}
}

// formatQuote 格式化引用
func (f *MarkdownFormatter) formatQuote(text string) string {
	formatted := f.formatInline(text)
	if !f.useColor {
		return fmt.Sprintf("│ %s", formatted)
	}
	return color.New(color.FgYellow).Sprint("│") + " " + formatted
}

// formatListItem 格式化无序列表项
func (f *MarkdownFormatter) formatListItem(text string) string {
	// 处理列表项内部的 Markdown
	formatted := f.formatInline(text)
	if !f.useColor {
		return fmt.Sprintf("• %s", formatted)
	}
	return color.New(color.FgGreen).Sprintf("•") + " " + formatted
}

// formatOrderedList 格式化有序列表项
func (f *MarkdownFormatter) formatOrderedList(line string) string {
	re := regexp.MustCompile(`^(\s*)(\d+)\.\s+(.*)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 4 {
		indent := matches[1]
		num := matches[2]
		text := f.formatInline(matches[3])

		if !f.useColor {
			return fmt.Sprintf("%s%s. %s", indent, num, text)
		}
		return fmt.Sprintf("%s%s%s. %s", indent, color.New(color.FgCyan).Sprintf("%s", num), color.New(color.FgCyan).Sprintf("."), text)
	}
	return line
}

// formatCodeBlock 格式化代码块
func (f *MarkdownFormatter) formatCodeBlock(codeLines []string, language string) string {
	if len(codeLines) == 0 {
		return ""
	}

	var result strings.Builder

	// 输出代码内容（不显示边框）
	codeColor := color.New(color.FgHiWhite)

	for _, line := range codeLines {
		if f.useColor {
			result.WriteString(codeColor.Sprint(line))
		} else {
			result.WriteString(line)
		}
		result.WriteString("\n")
	}

	return result.String()
}

func isFenceStart(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```")
}

func isFenceEnd(line string) bool {
	return strings.TrimSpace(line) == "```"
}

func parseFenceStart(line string) (bool, string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return false, ""
	}
	lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
	return true, lang
}

// formatInlineCode 格式化内联代码
func (f *MarkdownFormatter) formatInlineCode(text string) string {
	re := regexp.MustCompile(`` + "(`[^`]+`)" + ``)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		code := strings.Trim(match, "`")
		if !f.useColor {
			return fmt.Sprintf("`%s`", code)
		}
		return color.New(color.BgHiBlack, color.FgHiMagenta).Sprintf(" %s ", code)
	})
}

func (f *MarkdownFormatter) formatInline(text string) string {
	if text == "" {
		return text
	}
	re := regexp.MustCompile("`[^`]+`")
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return f.formatInlineNoCode(text)
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] > last {
			out.WriteString(f.formatInlineNoCode(text[last:m[0]]))
		}
		out.WriteString(f.formatInlineCode(text[m[0]:m[1]]))
		last = m[1]
	}
	if last < len(text) {
		out.WriteString(f.formatInlineNoCode(text[last:]))
	}
	return out.String()
}

func (f *MarkdownFormatter) formatInlineNoCode(text string) string {
	text = f.formatBold(text)
	text = f.formatItalic(text)
	text = f.formatLink(text)
	return text
}

// formatBold 格式化粗体文本
func (f *MarkdownFormatter) formatBold(text string) string {
	re := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		content := strings.Trim(match, "**")
		if !f.useColor {
			return fmt.Sprintf("%s", content)
		}
		return color.New(color.Bold).Sprintf("%s", content)
	})
}

// formatItalic 格式化斜体文本
func (f *MarkdownFormatter) formatItalic(text string) string {
	re := regexp.MustCompile(`\*([^*]+)\*`)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		// 跳过粗体（**）
		if strings.HasPrefix(match, "**") {
			return match
		}
		content := strings.Trim(match, "*")
		if !f.useColor {
			return fmt.Sprintf("%s", content)
		}
		return color.New(color.Faint).Sprintf("%s", content)
	})
}

// formatLink 格式化链接
func (f *MarkdownFormatter) formatLink(text string) string {
	linkRE := regexp.MustCompile(`\[([^\]]+)\]\(([^\)]+)\)`)
	return linkRE.ReplaceAllStringFunc(text, func(match string) string {
		reMatches := linkRE.FindStringSubmatch(match)
		if len(reMatches) >= 3 {
			linkText := reMatches[1]
			url := reMatches[2]
			if !f.useColor {
				return fmt.Sprintf("%s (%s)", linkText, url)
			}
			return color.New(color.FgCyan, color.Underline).Sprintf("%s", linkText) +
				color.New(color.FgHiBlack).Sprintf(" (%s)", url)
		}
		return match
	})
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

func displayWidth(text string) int {
	if text == "" {
		return 0
	}
	plain := stripANSICodes(text)
	width := 0
	for _, r := range plain {
		width += runeWidth(r)
	}
	return width
}

func stripANSICodes(text string) string {
	if text == "" {
		return text
	}
	return ansiEscapeRE.ReplaceAllString(text, "")
}

func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r < 32 || r == 127 {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

func isWideRune(r rune) bool {
	if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
		return true
	}
	if r >= 0x1100 && r <= 0x115F {
		return true
	}
	if r >= 0x2E80 && r <= 0xA4CF {
		return true
	}
	if r >= 0xAC00 && r <= 0xD7A3 {
		return true
	}
	if r >= 0xF900 && r <= 0xFAFF {
		return true
	}
	if r >= 0xFE10 && r <= 0xFE6F {
		return true
	}
	if r >= 0xFF00 && r <= 0xFF60 {
		return true
	}
	if r >= 0xFFE0 && r <= 0xFFE6 {
		return true
	}
	if r >= 0x1F300 && r <= 0x1FAFF {
		return true
	}
	return false
}

func padRightVisible(text string, width int) string {
	if width <= 0 {
		return text
	}
	visible := displayWidth(text)
	if visible >= width {
		return text
	}
	return text + strings.Repeat(" ", width-visible)
}
