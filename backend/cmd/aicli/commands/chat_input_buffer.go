package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var discardPendingConsoleInput = platformDiscardPendingConsoleInput
var pendingConsoleInputCount = platformPendingConsoleInputCount
var pendingConsoleLineInput = platformPendingConsoleLineInput
var pendingConsoleTextInput = platformPendingConsoleTextInput
var inputPasteSettleDelay = platformInputPasteSettleDelay
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

// notifyChatInputDraftState 只保留状态钩子，不向终端输出任何提示。
// 多行粘贴进入 draft 后，用户应保持在当前输入流程中，直到按 Enter 确认提交。
func notifyChatInputDraftState(session *ChatSession, active bool, lines int) {
	if session == nil || session.Interaction == nil {
		return
	}
	if active {
		if lines < 1 {
			lines = 1
		}
		session.Interaction.RefreshStatus(fmt.Sprintf("Paste draft %d lines", lines))
		return
	}
	session.Interaction.RefreshStatus("")
}
