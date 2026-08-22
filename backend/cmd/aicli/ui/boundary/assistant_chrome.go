// Package boundary 提供 transcript 边界策略以及过渡期展示兼容助手。
package boundary

import "strings"

// AssistantStreamMarkerText is the removed legacy assistant marker. It remains
// exported for compatibility and regression assertions only; production
// assistant rendering must not prepend it. Tool events own their explicit
// "• Running/Completed" prefixes independently.
const AssistantStreamMarkerText = "• "

// AssistantStreamMarker returns the removed legacy marker.
// Deprecated: ordinary assistant rendering must use no marker.
func AssistantStreamMarker() string {
	return AssistantStreamMarkerText
}

// AssistantContentIndent retains the old two-column gutter for reasoning and
// migration helpers. Ordinary assistant rendering must pass an empty indent.
func AssistantContentIndent() string {
	return "  "
}

// FormatAssistantBlockChrome is retained as a source-compatible migration
// helper. Assistant chrome was removed, so semantic content is returned exactly
// as supplied (including leading/internal/trailing newlines).
func FormatAssistantBlockChrome(content string) string {
	return content
}

// StripAssistantBlockChrome is the inverse compatibility helper. There is no
// display prefix to strip, so offsets stay in semantic source coordinates.
func StripAssistantBlockChrome(content string) (string, int) {
	return content, 0
}

// IndentAssistantContent retains the explicit two-column indent used by local
// notices and reasoning. Ordinary assistant body paths no longer call it.
func IndentAssistantContent(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = AssistantContentIndent() + line
	}
	return strings.Join(lines, "\n")
}
