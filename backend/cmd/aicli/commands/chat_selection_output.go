package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func printChatSelectionSection(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}

	printChatSelectionBlankLine()
	separator := ui.NewSeparator().SetTitle(fmt.Sprintf(" %s ", title)).Build()
	fmt.Fprintln(os.Stderr, separator)
	printChatSelectionBlankLine()
}

func printChatSelectionBlankLine() {
	fmt.Fprintln(os.Stderr)
}

func printChatSelectionLine(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func printChatSelectionPrompt(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
}

func printChatSelectionWarning(format string, args ...interface{}) {
	ui.PrintWarningTo(os.Stderr, format, args...)
}
