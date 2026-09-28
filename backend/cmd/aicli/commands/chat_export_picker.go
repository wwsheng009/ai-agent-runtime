package commands

import (
	"context"
	"errors"
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

// chatExportPickerLeaseHooks binds the shared lease lifecycle
// (chat_picker_common.go) to this picker's UI-actor action identity.
var chatExportPickerLeaseHooks = chatPickerLeaseHooks{
	Open:  func(leaseID uint64) ui.UIAction { return ui.OpenExportPicker{LeaseID: leaseID} },
	Close: func(leaseID uint64) ui.UIAction { return ui.CloseExportPicker{LeaseID: leaseID} },
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

	// 批次 2：租约与 close 序列由统一框架承担；两阶段（会话 → 格式）在同一
	// 租约内推进（与 backtrack 同型），文件写入仍在租约释放之后。
	var (
		pickedSession *runtimechat.Session
		format        = chatExportFormatFull
		stage         = 1
		cancelled     bool
	)
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "export.picker",
		Title: "导出会话",
		Hooks: chatExportPickerLeaseHooks,
		Run: func(s *ChatSession, lease ui.ScreenLease) error {
			// Stage 1: pick the target session.
			current := currentRuntimeSessionForResumeList(s)
			sessionItems, sessionPicks := buildExportSessionFullScreenItems(candidates, current, time.Now())
			sessionResult, sessionErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(s), ui.FullScreenListOptions{
				Title:        "选择要导出的会话",
				Subtitle:     formatResumePickerSubtitle(len(candidates), current != nil),
				EmptyMessage: "没有可导出的会话",
				ConfirmLabel: "使用选中会话",
				Items:        sessionItems,
			}, lease)
			if sessionErr != nil {
				return sessionErr
			}
			if sessionResult.Cancelled || sessionResult.Index < 0 || sessionResult.Index >= len(sessionPicks) || sessionPicks[sessionResult.Index] == nil {
				cancelled = true
				return nil
			}
			pickedSession = sessionPicks[sessionResult.Index]

			// Stage 2: pick the export format (same lease, mirroring backtrack mode).
			// 格式列表与 legacy 编号菜单共用 chatExportFormatOptions，顺序即索引。
			stage = 2
			formatOptions := chatExportFormatOptions()
			formatItems := buildExportFormatFullScreenItems(formatOptions)
			formatResult, formatErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(s), ui.FullScreenListOptions{
				Title:        "选择导出格式",
				Subtitle:     "Enter 确认 · Esc 取消",
				EmptyMessage: "没有可用的格式",
				ConfirmLabel: "使用选中格式",
				Items:        formatItems,
			}, lease)
			if formatErr != nil {
				return formatErr
			}
			if formatResult.Cancelled || formatResult.Index < 0 {
				cancelled = true
				return nil
			}
			if formatResult.Index < len(formatOptions) {
				format = formatOptions[formatResult.Index].Format
			}
			return nil
		},
	})
	if res.Degraded {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开导出选择器失败: %w", errChatPickerScreenUnavailable)), false)
		return
	}
	if res.Err != nil {
		switch {
		case res.Phase == chatPickerPhaseClose:
			if errors.Is(res.Err, errChatPickerActorNotIdle) {
				_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("导出选择器关闭未就绪")), false)
			} else {
				_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭导出选择器失败: %w", res.Err)), false)
			}
		case stage == 1:
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择导出会话失败: %w", res.Err)), false)
		default:
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择导出格式失败: %w", res.Err)), false)
		}
		return
	}
	if cancelled || pickedSession == nil {
		_ = renderChatCommandResult(session, commandTextResult("已取消导出"), false)
		return
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

// buildExportFormatFullScreenItems projects the shared format options into
// picker rows, index-aligned with options (chatExportFormatOptions order).
//
// 列表行右侧的 detail 列宽被 ui 限制为 min(32, width/3)，因此行内只放一行
// 简述；完整说明放 Preview：预览区按终端宽度折行渲染，选中任意格式都能读到
// 未被 "…" 截断的语义（含产物形式与 32KB 截断等约束）。
func buildExportFormatFullScreenItems(options []chatExportFormatOption) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(options))
	for _, option := range options {
		items = append(items, ui.FullScreenListItem{
			Title:      string(option.Format),
			Detail:     option.PickerDetail,
			Preview:    option.PickerPreview,
			SearchText: option.SearchText,
		})
	}
	return items
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
		return commandTextResult("错误: " + err.Error() + "\n用法: " + chatExportUsage), true
	}
	if !opts.ExplicitTarget && canOpenChatExportPicker(session) {
		return CommandResult{
			Action: CommandContinue,
			Screen: chatScreenEffectSpec("export.picker", "导出会话", func(s *ChatSession) {
				openChatExportPicker(s, ExportPickerRequest{})
			}),
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
	if chatExportFormatReportsToolStats(result.Format) {
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
