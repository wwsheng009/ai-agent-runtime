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
	if !hasPendingConsoleUserInput() {
		return 0
	}
	count, _ := pendingConsoleInputCount()
	if count > 0 {
		return count
	}
	return 1
}

// hasPendingConsoleUserInput 只把真正会影响聊天交互的键盘输入当成“有待处理输入”。
// Windows 控制台在启动阶段会混入 focus / resize 等噪声事件，如果直接按事件总数判断，
// 就会把首个 prompt 误压住，导致用户必须先按一次回车才能看到 ">"。
func hasPendingConsoleUserInput() bool {
	pending, err := pendingConsoleLineInput()
	if err == nil && pending {
		return true
	}
	pending, err = pendingConsoleTextInput()
	if err == nil && pending {
		return true
	}
	return false
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

// notifyChatInputDraftState 通过 surface 展示 pending paste preview；
// 没有 surface 时才回退到旧的状态栏提示。
func notifyChatInputDraftState(session *ChatSession, active bool, lines int, text string) {
	if session == nil {
		return
	}
	if active {
		if lines < 1 {
			lines = 1
		}
		if session.Surface != nil && session.Surface.Enabled() {
			session.Surface.ShowPendingPastePreview(lines, text)
			return
		}
		if session.Interaction != nil {
			session.Interaction.RefreshStatus(fmt.Sprintf("Paste draft %d lines", lines))
		}
		return
	}
	if session.Surface != nil && session.Surface.Enabled() {
		session.Surface.ClearPendingPastePreview()
		return
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}
