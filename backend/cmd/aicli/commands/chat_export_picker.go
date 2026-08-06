package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// canOpenChatExportPicker mirrors the other lease-bound picker gates. Export
// writes files, so it may only begin while the unified primary presenter is
// idle, owns its viewport, and no competing popup or alternate screen owns
// input.
func canOpenChatExportPicker(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	if session.RuntimeEventBridge != nil && session.RuntimeEventBridge.isRunActive() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// openChatExportPicker executes the typed alternate-screen export selector.
// The lease ends before exportChatSession runs, so file writes happen only
// after lease release and primary presenter recovery.
func openChatExportPicker(session *ChatSession, _ ExportPickerRequest) {
	if !canOpenChatExportPicker(session) {
		return
	}

	// Candidate sessions: the current session first (disabled row), then the
	// resumable history list. No candidates → nothing to export.
	currentID := currentRuntimeSessionID(session)
	var candidates []*runtimechat.Session
	if session.SessionManager != nil {
		var err error
		candidates, err = listResumeCandidateChatSessions(session.SessionManager, session.SessionUserID, session.SessionFilter, currentID)
		if err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(err), false)
			return
		}
	}
	if session.RuntimeSession == nil && len(candidates) == 0 {
		_ = renderChatCommandResult(session, commandTextResult("当前没有可导出的会话"), false)
		return
	}
	if len(candidates) == 0 && session.RuntimeSession != nil {
		// No resumable history but a live current session: mirror the legacy
		// exportInteractiveSelect fast path and export the current session in
		// full instead of opening a picker with only a disabled row.
		opts := chatExportOptions{
			Target:         "current",
			Format:         chatExportFormatFull,
			ExplicitTarget: true,
			ExplicitFormat: true,
		}
		result, err := exportChatSession(session, opts)
		if err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(err), false)
			return
		}
		_ = renderChatCommandResult(session, buildChatExportResultDocument(result), false)
		return
	}

	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "导出会话",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开导出选择器失败: %w", err)), false)
		return
	}
	if !session.Interaction.postUIAction(ui.OpenExportPicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("导出选择器状态未提交")), false)
		return
	}
	// Lifecycle barrier only: the first list frame sees the matching actor
	// state. Key navigation stays local to the fullscreen list.
	session.Interaction.waitUIActorIdle()

	// Stage 1: pick the target session.
	current := currentRuntimeSessionForResumeList(session)
	sessionItems, sessionPicks := buildExportSessionFullScreenItems(candidates, current, time.Now())
	sessionResult, sessionErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
		Title:        "选择要导出的会话",
		Subtitle:     formatResumePickerSubtitle(len(candidates), current != nil),
		EmptyMessage: "没有可导出的会话",
		ConfirmLabel: "使用选中会话",
		Items:        sessionItems,
	}, lease)
	if sessionErr != nil {
		closeExportPickerLease(session, lease)
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择导出会话失败: %w", sessionErr)), false)
		return
	}
	if sessionResult.Cancelled || sessionResult.Index < 0 || sessionResult.Index >= len(sessionPicks) || sessionPicks[sessionResult.Index] == nil {
		closeExportPickerLease(session, lease)
		_ = renderChatCommandResult(session, commandTextResult("已取消导出"), false)
		return
	}
	pickedSession := sessionPicks[sessionResult.Index]

	// Stage 2: pick the export format (same lease, mirroring backtrack mode).
	formatResult, formatErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
		Title:        "选择导出格式",
		Subtitle:     "Enter 确认 · Esc 取消",
		EmptyMessage: "没有可用的格式",
		ConfirmLabel: "使用选中格式",
		Items: []ui.FullScreenListItem{
			{Title: "full", Detail: "完整 JSON（含消息、工具调用与结果）", SearchText: "full json 完整"},
			{Title: "body", Detail: "纯文本正文（不含工具链）", SearchText: "body text markdown 正文"},
		},
	}, lease)
	_ = session.Interaction.postUIAction(ui.CloseExportPicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	// LeaseReleased is the primary recovery barrier. Do not write files until
	// the actor has observed it.
	session.Interaction.waitUIActorIdle()

	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭导出选择器失败: %w", releaseErr)), false)
		return
	}
	if formatErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择导出格式失败: %w", formatErr)), false)
		return
	}
	if formatResult.Cancelled || formatResult.Index < 0 {
		_ = renderChatCommandResult(session, commandTextResult("已取消导出"), false)
		return
	}
	format := chatExportFormatFull
	if formatResult.Index == 1 {
		format = chatExportFormatBody
	}

	opts := chatExportOptions{
		Target:         strings.TrimSpace(pickedSession.ID),
		Format:         format,
		ExplicitTarget: true,
		ExplicitFormat: true,
	}
	result, err := exportChatSession(session, opts)
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}
	_ = renderChatCommandResult(session, buildChatExportResultDocument(result), false)
}

func closeExportPickerLease(session *ChatSession, lease ui.ScreenLease) {
	_ = session.Interaction.postUIAction(ui.CloseExportPicker{LeaseID: lease.ID()})
	_ = lease.Release(context.Background())
	session.Interaction.waitUIActorIdle()
}

// buildExportSessionFullScreenItems builds export rows for the session picker.
// picks is index-aligned with items. Unlike the resume picker, the current
// session is selectable here: exporting the live session is a first-class
// /export target, not a disabled placeholder.
func buildExportSessionFullScreenItems(sessions []*runtimechat.Session, current *runtimechat.Session, now time.Time) ([]ui.FullScreenListItem, []*runtimechat.Session) {
	capacity := len(sessions)
	if current != nil {
		capacity++
	}
	items := make([]ui.FullScreenListItem, 0, capacity)
	selectable := make([]*runtimechat.Session, 0, capacity)
	if current != nil {
		items = append(items, ui.FullScreenListItem{
			Title:      formatCurrentResumeSessionTitle(runtimeResumeSessionTitle(current)),
			Detail:     "当前会话",
			SearchText: "current 当前 " + current.ID,
		})
		selectable = append(selectable, current)
	}
	for _, item := range sessions {
		if item == nil {
			continue
		}
		title := runtimeResumeSessionTitle(item)
		summary := ""
		if preview := item.BuildPreview(); preview != nil {
			summary = strings.TrimSpace(preview.Summary)
		}
		if summary == "" || strings.EqualFold(summary, title) {
			summary = runtimeSessionWorkspacePath(item)
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     summary,
			SearchText: item.ID + " " + title + " " + summary,
		})
		selectable = append(selectable, item)
	}
	return items, selectable
}

// executeStructuredExportCommand is the unified interactive entry point for
// /export. Explicit targets/formats apply directly through the unified command
// cell; bare /export opens the typed session/format picker. When the picker is
// unavailable, bare /export degrades to exporting the current session in full.
func executeStructuredExportCommand(session *ChatSession, command string) (CommandResult, bool) {
	opts, err := parseChatExportOptions(extractCommandArgument(command))
	if err != nil {
		return commandTextResult("错误: " + err.Error() + "\n用法: /export [current|latest|<session-id>] [--full|--body] [--output <path>|--dir <dir>]"), true
	}
	if !opts.ExplicitTarget && canOpenChatExportPicker(session) {
		return CommandResult{
			Action:         CommandContinue,
			OpenExportPicker: &ExportPickerRequest{},
		}, true
	}
	if !opts.ExplicitTarget {
		opts.Target = "current"
		opts.ExplicitTarget = true
	}
	result, err := exportChatSession(session, opts)
	if err != nil {
		return commandErrorResult(err), true
	}
	return buildChatExportResultDocument(result), true
}

// buildChatExportResultDocument is the terminal-neutral projection of the
// legacy printChatExportResult.
func buildChatExportResultDocument(result *chatExportResult) CommandResult {
	if result == nil {
		return commandTextResult("错误: 导出结果为空")
	}
	lines := []string{"会话已导出"}
	lines = append(lines, formatChatSessionMetaRow("Session:", chatDebugValueOrNone(result.SessionID)))
	lines = append(lines, formatChatSessionMetaRow("Format:", string(result.Format)))
	lines = append(lines, formatChatSessionMetaRow("Output File:", chatDebugValueOrNone(result.Path)))
	lines = append(lines, formatChatSessionMetaRow("Messages:", fmt.Sprintf("%d", result.Stats.MessageCount)))
	if result.Format == chatExportFormatFull {
		lines = append(lines, formatChatSessionMetaRow("Tool Calls:", fmt.Sprintf("%d", result.Stats.ToolCallCount)))
		lines = append(lines, formatChatSessionMetaRow("Tool Results:", fmt.Sprintf("%d", result.Stats.ToolResultCount)))
	}
	return commandTextResult(strings.Join(lines, "\n"))
}

// formatChatSessionMetaRow renders one label/value row as plain text for
// command cells, mirroring printChatSessionMetaRow without terminal styling.
func formatChatSessionMetaRow(label, value string) string {
	if strings.TrimSpace(label) == "" {
		return ""
	}
	label = strings.Join(strings.Fields(ui.SanitizeTerminalText(label)), " ")
	pad := chatSessionMetaLabelWidth - ui.DisplayWidth(label)
	if pad < 0 {
		pad = 0
	}
	return label + strings.Repeat(" ", pad) + " " + value
}
