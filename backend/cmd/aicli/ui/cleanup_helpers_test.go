package ui

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func newSuffixProjector(source string, start, width int, theme style.ThemeContext, reasoning bool, highlighter syntax.Highlighter) *suffixProjector {
	if highlighter == nil {
		highlighter = newActiveBandHighlighter()
	}
	return &suffixProjector{source: source, start: start, width: width, theme: theme, reasoning: reasoning, highlighter: highlighter}
}

func noteTranscriptResidencyBodyStoreGet(missed bool) {
	residencyBodyStoreGets.Add(1)
	if missed {
		residencyBodyStoreGetMisses.Add(1)
	}
}

func sessionInfoChildPrefix(theme *Theme) string {
	return strings.Repeat(" ", render.Width(theme.SystemIcon+" "))
}

func terminalScrollDownSequence(rows int) string {
	if rows < 1 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dT", rows)
}
