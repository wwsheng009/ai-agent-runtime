package commands

import (
	"context"
	"fmt"

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
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// openChatDebugOverlay renders /debug display on a dedicated alternate screen
// instead of the main message stream. The debug document is captured once
// before the screen is entered, shown through the lease-bound overlay viewer,
// and never committed as a Scene command cell: dismissal restores the primary
// presenter from its retained state, exactly like /resume list and /history.
func openChatDebugOverlay(session *ChatSession) {
	if !canOpenChatDebugOverlay(session) {
		return
	}
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "调试信息",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开调试信息界面失败: %w", err)), false)
		return
	}
	// The surface posts LeaseAcquired as part of the acquire transaction. Wait
	// for that lease barrier so the first overlay frame is never raced by a
	// pending primary flush; the overlay itself owns no actor semantic state.
	if !session.Interaction.waitUIActorIdleBounded("open debug overlay") {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("调试信息界面渲染未就绪")), false)
		return
	}

	runErr := ui.RunDebugOverlayWithLease(context.Background(), resumeFullScreenTerminal(session), ui.DebugOverlayOptions{
		Title: "调试信息",
		Body:  chatDebugOverlayBody(session),
	}, lease)
	releaseErr := lease.Release(context.Background())
	if runErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("调试信息界面异常: %w", runErr)), false)
		return
	}
	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭调试信息界面失败: %w", releaseErr)), false)
		return
	}
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
