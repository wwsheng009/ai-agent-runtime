package markdown

import (
	"strings"
)

// LooksLikeMarkdown reports whether source should use the structured Markdown
// presentation pipeline. It intentionally recognizes the same common body
// syntax accepted by the chat formatter, while leaving ordinary multi-line
// prose on the source-preserving plain transcript path.
//
// 该函数位于流式活跃 cell 的每帧路径上（投影 + stable 边界推导各一次），
// 源随 token 增长；此前的 regexp 版本在长源上每帧付出全源匹配 + 全源 Split
// 分配（pprof：单帧约 23% CPU 在 regexp 回溯）。这里改为无 regexp 的线性
// 扫描，语义与旧实现逐例等价（等价性由 detect_test.go 对照旧正则实现钉住）。
func LooksLikeMarkdown(source string) bool {
	if source == "" {
		return false
	}
	if strings.Contains(source, "```") || strings.Contains(source, "**") ||
		strings.Count(source, "`") >= 2 {
		return true
	}
	// 逐行扫描，不分配 Split 结果。行内检查见 lineLooksLikeMarkdownBody。
	for start := 0; ; {
		end := strings.IndexByte(source[start:], '\n')
		line := source[start:]
		if end >= 0 {
			line = source[start : start+end]
		}
		if lineLooksLikeMarkdownBody(line) {
			return true
		}
		if end < 0 {
			return false
		}
		start += end + 1
	}
}

// lineLooksLikeMarkdownBody 等价于旧实现的行内检查集合：
//
//	markdownLinkPattern.MatchString(line) ||
//	markdownListPrefix.MatchString(line) || markdownOrderedPrefix.MatchString(line) ||
//	HasPrefix(TrimLeft(line," \t"), "# "|"## "|"### "|"> ") ||
//	(HasPrefix(TrimSpace(line),"|") && HasSuffix(TrimSpace(line),"|"))
func lineLooksLikeMarkdownBody(line string) bool {
	if lineHasMarkdownLink(line) || lineLooksLikeListPrefix(line) || lineLooksLikeOrderedPrefix(line) {
		return true
	}
	// 字节门卫：标题/引用前缀必含 '#' 或 '>'，表格行必含 '|'。先做 O(1) 判定，
	// 避免对绝大多数普通行支付 TrimLeft/TrimSpace 的逐 rune 扫描（pprof：两者
	// 合计约占该函数成本的三分之一以上）。
	if strings.IndexByte(line, '#') >= 0 || strings.IndexByte(line, '>') >= 0 {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") ||
			strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "> ") {
			return true
		}
	}
	if strings.IndexByte(line, '|') < 0 {
		return false
	}
	spaceTrimmed := strings.TrimSpace(line)
	return strings.HasPrefix(spaceTrimmed, "|") && strings.HasSuffix(spaceTrimmed, "|")
}

// lineHasMarkdownLink 等价于 \[.*?\]\(.*?\)（'.' 不跨行）：
// 首个 '[' 之后必须存在 '](' 且其后再有 ')'。若首个 '](' 之后没有 ')'，
// 更晚的 '](' 只会搜索同一后缀的更短片段，同样不可能命中。
func lineHasMarkdownLink(line string) bool {
	open := strings.IndexByte(line, '[')
	if open < 0 {
		return false
	}
	rest := line[open+1:]
	closeParen := strings.Index(rest, "](")
	if closeParen < 0 {
		return false
	}
	return strings.IndexByte(rest[closeParen+2:], ')') >= 0
}

// lineLooksLikeListPrefix 等价于 ^\s*[-*]\s+（Go 正则 \s = [\t\n\f\r ]，
// 行内不含 \n）。
func lineLooksLikeListPrefix(line string) bool {
	index := 0
	for index < len(line) && isRegexpSpace(line[index]) {
		index++
	}
	if index >= len(line) || (line[index] != '-' && line[index] != '*') {
		return false
	}
	index++
	return index < len(line) && isRegexpSpace(line[index])
}

// lineLooksLikeOrderedPrefix 等价于 ^\s*\d+\.\s+。
func lineLooksLikeOrderedPrefix(line string) bool {
	index := 0
	for index < len(line) && isRegexpSpace(line[index]) {
		index++
	}
	digits := index
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index == digits || index >= len(line) || line[index] != '.' {
		return false
	}
	index++
	return index < len(line) && isRegexpSpace(line[index])
}

func isRegexpSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\f', '\r':
		return true
	}
	return false
}
