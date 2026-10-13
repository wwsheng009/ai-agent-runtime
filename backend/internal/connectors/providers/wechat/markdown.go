package wechat

import (
	"regexp"
	"strings"
)

// 微信纯文本渲染不支持 markdown：发送前按上游 wechat_markdown_filter.go 的策略
// 做最小清洗——去掉标题/引用/分隔线/代码围栏标记，把链接与图片语法降级为
// 可读文本，去除强调标记，表格转为制表符分隔。
var (
	markdownImagePattern            = regexp.MustCompile(`!\[[^\]]*]\([^)\n]*\)`)
	markdownLinkPattern             = regexp.MustCompile(`\[(.*?)\]\([^)\n]*\)`)
	markdownInlineCodePattern       = regexp.MustCompile("`([^`\\n]+)`")
	markdownStrikePattern           = regexp.MustCompile(`~~([^~\n]+)~~`)
	markdownTripleStarPattern       = regexp.MustCompile(`\*\*\*([^*\n]+)\*\*\*`)
	markdownTripleUnderscorePattern = regexp.MustCompile(`___([^_\n]+)___`)
	markdownBoldPattern             = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	markdownUnderlineBoldPattern    = regexp.MustCompile(`__([^_\n]+)__`)
	markdownItalicPattern           = regexp.MustCompile(`\*([^*\n]+)\*`)
	markdownUnderlineItalicPattern  = regexp.MustCompile(`_([^_\n]+)_`)
	markdownHeadingPattern          = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)
	markdownQuotePattern            = regexp.MustCompile(`^\s*>\s?`)
	markdownRulePattern             = regexp.MustCompile(`^\s*([*\-_]\s*){3,}\s*$`)
)

// cleanMarkdown 清洗 markdown 文本；空串表示无可发送内容。
func cleanMarkdown(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if strings.TrimSpace(value) == "" {
		return ""
	}

	lines := strings.Split(value, "\n")
	filtered := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			// 去掉代码围栏标记本身，保留代码内容。
			inFence = !inFence
			continue
		}
		if inFence {
			filtered = append(filtered, strings.TrimRight(line, " \t"))
			continue
		}
		if markdownRulePattern.MatchString(line) {
			continue
		}

		line = markdownQuotePattern.ReplaceAllString(line, "")
		line = markdownHeadingPattern.ReplaceAllString(line, "")
		if row, ok := cleanMarkdownTableRow(line); ok {
			if row != "" {
				filtered = append(filtered, row)
			}
			continue
		}
		filtered = append(filtered, cleanMarkdownInline(line))
	}

	return collapseBlankLines(strings.Join(filtered, "\n"))
}

func cleanMarkdownTableRow(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") {
		return "", false
	}
	if strings.Contains(trimmed, "-") && strings.Trim(trimmed, "|:- ") == "" {
		return "", true // 表头分隔行
	}

	parts := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cells = append(cells, cleanMarkdownInline(part))
	}
	if len(cells) == 0 {
		return "", true
	}
	return strings.Join(cells, "\t"), true
}

func cleanMarkdownInline(line string) string {
	line = markdownImagePattern.ReplaceAllString(line, "")
	line = markdownLinkPattern.ReplaceAllString(line, "$1")

	replacers := []*regexp.Regexp{
		markdownInlineCodePattern,
		markdownStrikePattern,
		markdownTripleStarPattern,
		markdownTripleUnderscorePattern,
		markdownBoldPattern,
		markdownUnderlineBoldPattern,
		markdownItalicPattern,
		markdownUnderlineItalicPattern,
	}
	for _, pattern := range replacers {
		for {
			next := pattern.ReplaceAllString(line, "$1")
			if next == line {
				break
			}
			line = next
		}
	}

	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "__", "")
	line = strings.ReplaceAll(line, "~~", "")
	return strings.TrimRight(line, " \t")
}

// collapseBlankLines 折叠连续空行（最多保留一个空行），并去除首尾空白行。
func collapseBlankLines(value string) string {
	lines := strings.Split(value, "\n")
	collapsed := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blank || len(collapsed) == 0 {
				continue
			}
			blank = true
			collapsed = append(collapsed, "")
			continue
		}
		blank = false
		collapsed = append(collapsed, line)
	}
	for len(collapsed) > 0 && strings.TrimSpace(collapsed[len(collapsed)-1]) == "" {
		collapsed = collapsed[:len(collapsed)-1]
	}
	return strings.TrimSpace(strings.Join(collapsed, "\n"))
}
