// Package tooloutline centralizes the tree-outline formatting shared by the two
// tool-result transcript producers:
//
//   - the legacy compact renderer (commands/chat_tool_rendering.go) marks the
//     content lines under a chat-core tool block's display head;
//   - the unified event encoder (ui/render/encoding/encoder.go) tree-indents a
//     standalone output blob produced by runtime tool events.
//
// Both feed the same transcript contract and must agree byte-for-byte on the
// marker geometry ("  │  " branches plus one closing "  └  " line). Keeping the
// marker formatters and the structure-decision helpers in one leaf package
// prevents the drift that previously dropped tree markers on one path (the
// encoder skipped tree indentation for markdown-lookalike plain output).
package tooloutline

import "strings"

// Tree markers used by tool result blocks. The leading two spaces align the
// outline under the block head; the trailing two separate marker and content.
const (
	branchPrefix = "  │  "
	lastPrefix   = "  └  "
)

// TreeIndentLines marks a tool-result block whose first line is the top-level
// display head. The head stays unmarked; content lines from index 1 get branch
// markers and the final line the closing marker. One leading two-space indent
// is stripped from content lines first, per the legacy compact path convention
// (the "  └  27 lines" shape). Single-line blocks are returned unchanged.
func TreeIndentLines(lines []string) []string {
	if len(lines) <= 1 {
		return lines
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[0])
	for i := 1; i < len(lines); i++ {
		out = append(out, linePrefix(i, len(lines))+strings.TrimPrefix(lines[i], "  "))
	}
	return out
}

// TreeIndentText marks a standalone output blob that has no separate head line:
// every line gets a marker, the final line the closing "└". The blob lines are
// content under the caller's tool head, so a single line is still marked (it is
// that head's closing "└"); the old len==1 exception left one-line outputs
// flush-left and broke byte-parity with TreeIndentLines' head+content geometry.
// Empty output is returned unchanged.
func TreeIndentText(output string) string {
	if output == "" {
		return output
	}
	lines := strings.Split(output, "\n")
	var b strings.Builder
	b.Grow(len(output) + 6*len(lines))
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(linePrefix(i, len(lines)))
		b.WriteString(line)
	}
	return b.String()
}

// DeclaredMarkdown reports whether a render_output_format explicitly declares a
// rendered markdown supplement (editing tools). Callers deciding presentation
// must key off this declared format rather than content sniffing.
func DeclaredMarkdown(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "markdown")
}

// LooksLikeDiffText reports whether text already carries its own unified-diff
// structure and therefore must not be tree-indented. It matches the canonical
// textual forms accepted by ui/diff.RenderText plus the compact "• Edited" /
// "• Diff" supplement heads.
func LooksLikeDiffText(text string) bool {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "• Edited ") || strings.HasPrefix(line, "• Diff ") {
			return true
		}
	}
	if strings.HasPrefix(trimmed, "diff --git ") || strings.Contains(trimmed, "\ndiff --git ") {
		return true
	}
	return strings.Contains("\n"+trimmed, "\n--- ") &&
		strings.Contains("\n"+trimmed, "\n+++ ") &&
		strings.Contains("\n"+trimmed, "\n@@ ")
}

// linePrefix returns the marker prefix for line i of an n-line outline block.
func linePrefix(i, n int) string {
	if i == n-1 {
		return lastPrefix
	}
	return branchPrefix
}
