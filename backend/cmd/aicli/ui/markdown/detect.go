package markdown

import (
	"regexp"
	"strings"
)

var (
	markdownListPrefix    = regexp.MustCompile(`^\s*[-*]\s+`)
	markdownOrderedPrefix = regexp.MustCompile(`^\s*\d+\.\s+`)
	markdownLinkPattern   = regexp.MustCompile(`\[.*?\]\(.*?\)`)
)

// LooksLikeMarkdown reports whether source should use the structured Markdown
// presentation pipeline. It intentionally recognizes the same common body
// syntax accepted by the chat formatter, while leaving ordinary multi-line
// prose on the source-preserving plain transcript path.
func LooksLikeMarkdown(source string) bool {
	if source == "" {
		return false
	}
	if strings.Contains(source, "```") || strings.Contains(source, "**") ||
		strings.Count(source, "`") >= 2 || markdownLinkPattern.MatchString(source) {
		return true
	}
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if markdownListPrefix.MatchString(line) || markdownOrderedPrefix.MatchString(line) ||
			strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") ||
			strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "> ") ||
			(strings.HasPrefix(strings.TrimSpace(line), "|") && strings.HasSuffix(strings.TrimSpace(line), "|")) {
			return true
		}
	}
	return false
}
