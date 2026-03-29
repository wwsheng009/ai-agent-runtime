package commands

import (
	"bufio"
	"os"
	"strings"
)

var discardPendingConsoleInput = platformDiscardPendingConsoleInput
var pendingConsoleInputCount = platformPendingConsoleInputCount
var shouldDiscardPendingInput = canDiscardPendingInteractiveInput

func discardPendingInteractiveInput(session *ChatSession) int {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return 0
	}
	if session.InputQueue != nil {
		return discardQueuedInteractiveLines(session)
	}
	if !shouldDiscardPendingInput() {
		return 0
	}
	discarded, _ := discardPendingConsoleInput()
	session.InputReader = newChatInputReader()
	return discarded
}

func canDiscardPendingInteractiveInput() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

func resetChatInputReader(reader **bufio.Reader) {
	if reader == nil {
		return
	}
	*reader = newChatInputReader()
}

func pendingInteractiveInputCount(session *ChatSession) int {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return 0
	}
	if session.InputQueue != nil {
		return lenQueuedInteractiveInput(session)
	}
	if !shouldDiscardPendingInput() {
		return 0
	}
	count, _ := pendingConsoleInputCount()
	return count
}

func discardPendingInteractiveInputForPriorityPrompt(session *ChatSession, promptKind string) string {
	discarded := discardPendingInteractiveInput(session)
	if discarded <= 0 {
		return ""
	}
	publishLocalChatDiagnosticEvent(session, chatEventInputQueueDiscarded, map[string]interface{}{
		"discarded_count": discarded,
		"prompt_kind":     strings.TrimSpace(promptKind),
	})
	promptKind = strings.TrimSpace(promptKind)
	if promptKind == "" {
		promptKind = "交互提示"
	}
	return "[input] 检测到之前排队的输入内容；为避免误用，已在" + promptKind + "前丢弃这些输入。"
}
