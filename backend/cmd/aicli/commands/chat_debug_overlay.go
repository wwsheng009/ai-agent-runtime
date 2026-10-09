package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// canOpenChatDebugOverlay keeps /debug display strictly inside the unified
// alternate-screen contract. The overlay borrows the same ScreenLease the
// resume picker and transcript pager use; when any prerequisite is absent the
// command is left behind the fail-closed gate instead of falling back to the
// main message stream.
func canOpenChatDebugOverlay(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !chatSurfaceScreenGate(session) {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// chatDebugOverlayBody is the plain-text projection of the debug display
// document. The document stays the single source of truth; the overlay viewer
// wraps it to the terminal width.
func chatDebugOverlayBody(session *ChatSession) string {
	if session == nil {
		return "错误: 当前没有活动会话"
	}
	return ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
}
