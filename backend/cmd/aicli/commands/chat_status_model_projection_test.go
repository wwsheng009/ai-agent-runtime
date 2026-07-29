package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// These helpers keep string-focused assertions readable while ensuring tests
// exercise the same StatusLineModel -> Document projection as production.
func buildChatSurfaceStatusLine(session *ChatSession, state string) string {
	return buildChatSurfaceStatusLineForWidth(session, state, ui.GetTerminalWidth())
}

func buildChatSurfaceStatusLineForWidth(session *ChatSession, state string, width int) string {
	return buildChatSurfaceStatusLineForWidthAndInputMode(session, state, width, chatInputModeForSurfaceState(state))
}

func buildChatSurfaceStatusLineForWidthAndInputMode(session *ChatSession, state string, width int, inputMode chatInputMode) string {
	model := buildChatSurfaceStatusModelForWidthAndInputMode(session, state, width, inputMode)
	return style.StatusLineDocument(model, width).PlainText()
}
