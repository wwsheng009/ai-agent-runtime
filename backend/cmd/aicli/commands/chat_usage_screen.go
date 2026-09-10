package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// /usage 独立屏幕（alternate screen）：与 /model picker、/debug display 共用
// 同一 ScreenLease 契约。统一 interactive TTY 中 /usage 的视图变体不提交
// Scene 命令 cell，而是打开一个只读查看器（ui.RunDebugOverlayWithLease），
// 展示缓存总览 + 会话缓存请求列表（§6.4 纯渲染函数；快照在进入备用屏前
// 捕获一次，查看器不持有 actor 语义状态）。Plain/JSON/非交互投影保持
// §6.4 文档 cell；备用屏不可用的统一会话降级为同一文档 cell。
// ============================================================================

// /usage 查看器的视图模式（UsageScreenRequest.Mode）。
const (
	usageScreenModeOverview = "overview"
	usageScreenModeRequests = "requests"
	usageScreenModeTrace    = "trace"

	// usageScreenTitle 是备用屏标题（FullscreenRequest 与 overlay 头部共用）。
	usageScreenTitle = "会话缓存用量"
)

// canOpenChatUsageScreen keeps /usage strictly inside the unified
// alternate-screen contract. The viewer borrows the same ScreenLease the
// /debug overlay, resume picker and transcript pager use; when any
// prerequisite is absent the command degrades to the §6.4 document cell
// instead of flashing a broken alternate screen.
func canOpenChatUsageScreen(session *ChatSession) bool {
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

// openChatUsageScreen renders /usage on a dedicated alternate screen instead
// of the main message stream. The overview and session cache request list are
// captured once before the screen is entered, shown through the lease-bound
// overlay viewer, and never committed as a Scene command cell: dismissal
// restores the primary presenter from its retained state, exactly like /debug
// display. When the alternate screen cannot be hosted the §6.4 document cell
// is committed instead (same degrade-to-document contract as /model), so a
// degraded TTY never silently swallows /usage.
func openChatUsageScreen(session *ChatSession, req UsageScreenRequest) {
	if !canOpenChatUsageScreen(session) {
		_ = renderChatCommandResult(session, usageFallbackDocumentResult(session, req), false)
		return
	}
	// Capture the snapshot before entering the alternate screen: if the cache
	// source cannot be resolved we never flash an empty viewer and instead
	// commit the stable degradation cell (same contract as the document path).
	body, ok := buildUsageScreenBody(session, req)
	if !ok {
		_ = renderChatCommandResult(session, commandTextResult(body), false)
		return
	}
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: usageScreenTitle,
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开缓存用量界面失败: %w", err)), false)
		return
	}
	// The surface posts LeaseAcquired as part of the acquire transaction. Wait
	// for that lease barrier so the first overlay frame is never raced by a
	// pending primary flush; the viewer itself owns no actor semantic state.
	if !session.Interaction.waitUIActorIdleBounded("open usage screen") {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("缓存用量界面渲染未就绪")), false)
		return
	}

	runErr := ui.RunDebugOverlayWithLease(context.Background(), resumeFullScreenTerminal(session), ui.DebugOverlayOptions{
		Title: usageScreenTitle,
		Body:  body,
	}, lease)
	releaseErr := lease.Release(context.Background())
	if runErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("缓存用量界面异常: %w", runErr)), false)
		return
	}
	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭缓存用量界面失败: %w", releaseErr)), false)
		return
	}
}

// usageFallbackDocumentResult rebuilds the §6.4 document cell for unified
// sessions that cannot host the alternate screen (degrade-to-document).
func usageFallbackDocumentResult(session *ChatSession, req UsageScreenRequest) CommandResult {
	src, sessionID, errLines, ok := usageCacheSourceOrLines(session)
	if !ok {
		return commandTextResult(strings.Join(errLines, "\n"))
	}
	return commandTextResult(strings.Join(usageDocumentLines(src, sessionID, req), "\n"))
}

// buildUsageScreenBody composes the plain-text overlay body from the §6.4
// pure render functions. The bare overview view embeds the session cache
// request list（会话缓存列表）so one screen answers both "命中率如何" and
// "最近请求明细". The body is ANSI-free; the overlay viewer wraps it to the
// terminal width.
func buildUsageScreenBody(session *ChatSession, req UsageScreenRequest) (string, bool) {
	src, sessionID, errLines, ok := usageCacheSourceOrLines(session)
	if !ok {
		return strings.Join(errLines, "\n"), false
	}
	return strings.Join(usageScreenBodyLines(src, sessionID, req), "\n"), true
}

// usageScreenBodyLines projects one screen body per view mode. The overview
// mode appends the request list below the stats; requests/trace modes stay
// focused on their own §6.4 section.
func usageScreenBodyLines(src cacheanalytics.Source, sessionID string, req UsageScreenRequest) []string {
	switch req.Mode {
	case usageScreenModeRequests:
		limit := req.Limit
		if limit <= 0 {
			limit = usageCacheRequestsDefaultLimit
		}
		return renderUsageCacheRequests(src, sessionID, limit)
	case usageScreenModeTrace:
		return renderUsageCacheTrace(src, sessionID, req.TraceID)
	default:
		lines := renderUsageCacheOverview(src, sessionID)
		lines = append(lines, "")
		lines = append(lines, renderUsageCacheRequests(src, sessionID, usageCacheRequestsDefaultLimit)...)
		return lines
	}
}
