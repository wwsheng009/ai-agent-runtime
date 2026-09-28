package commands

import (
	"fmt"
	"strings"
)

// executeBusySlashCommand 是 P1 的忙时命令入口（策略判定 + inline 执行）。
// P2-3 起 capture 循环改走 runtimeCommandHost.SubmitBusy；本函数保留给
// P1 策略路径与既有测试使用。
func executeBusySlashCommand(session *ChatSession, line string) bool {
	if session == nil || session.Interaction == nil {
		return false
	}
	if chatSlashCommandBusyPolicyFor(line) != chatBusyPolicyImmediate {
		return false
	}
	return runBusyInlineCommand(session, line)
}

// runBusyInlineCommand 是 inline 模式的执行原语（P2-3 起由 runtimeCommandHost
// 复用）：不含策略判定，只做执行前的门禁与忙时安全断言。返回 true 表示命令已被
// 本通道占有并完成渲染；false 表示未占有（调用方必须回退入队）。
//
// 门禁（全部 fail-closed）：
//  1. 统一渲染面（INV-2/M2）：legacy stdout 直写会话一律降级；
//  2. L0 仲裁（INV-7）：仲裁器未注册或 modal/priority prompt 活跃 → 降级；
//  3. Phase A TryLock：主循环正在解析/渲染时降级，capture 永不阻塞。
//
// 忙时安全断言（只读契约）：命令结果若携带 send/picker/overlay/screen/quit 等
// 效应，视为分类错误 → 不渲染、降级排队并给出诊断。
func runBusyInlineCommand(session *ChatSession, line string) bool {
	if session == nil || session.Interaction == nil {
		return false
	}
	if !unifiedDirectInteractiveOutput(session) {
		return false
	}
	if !chatBusyCommandArbitrationAllows(session) {
		return false
	}
	if !session.commandMu.TryLock() {
		session.Interaction.RenderLocalSupplement("[input] 命令通道正忙，该命令已排队，将在当前回合结束后执行。")
		return false
	}

	// Phase A：解析（+ 会话/配置变更）与渲染都在 commandMu 内完成。
	result, handled, err := tryExecuteStructuredChatCommand(session, line)
	if !handled {
		session.commandMu.Unlock()
		return false
	}
	if err != nil {
		result = commandErrorResult(err)
	}
	if unsafe := chatBusyCommandUnsafeEffect(result); unsafe != "" {
		session.commandMu.Unlock()
		session.Interaction.RenderLocalSupplement(fmt.Sprintf(
			"[input] 忙时命令 %q 带有未预期的 %s 效应，已排队到当前回合结束后执行。",
			strings.TrimSpace(line), unsafe))
		return false
	}
	renderErr := renderChatCommandResult(session, result, false)
	session.commandMu.Unlock()
	if renderErr != nil {
		// 渲染失败：命令既未入队也未完成 → 交回队列保证可见性。
		return false
	}
	// immediate 契约：无 Phase B 效应（已由上面的安全断言保证），
	// 因此这里不再进入 dispatchChatCommand 的效应块。
	recordChatPromptHistory(session, line)
	return true
}

// chatBusyCommandArbitrationAllows 是 INV-7 的 L0 门：仲裁器未注册（ok=false）
// 或存在 modal 层时一律不放行（fail-closed）。busy capture 启动时会注册
// chatInputOwnerBusyCapture 影子层级，因此正常 capture 路径下 ok=true 且
// ModalDepth==0；priority prompt（审批/提问）会抬高 ModalDepth。
func chatBusyCommandArbitrationAllows(session *ChatSession) bool {
	snapshot, ok := chatInputArbitrationSnapshotOf(session)
	if !ok {
		return false
	}
	return snapshot.ModalDepth == 0
}

// chatBusyCommandUnsafeEffect 报告违反忙时只读契约的效应名；空串表示安全。
// 覆盖 CommandResult 的全部 Phase B 效应字段（chat_command_result.go:117-235）。
func chatBusyCommandUnsafeEffect(result CommandResult) string {
	switch {
	case result.Action == CommandQuit:
		return "quit"
	case result.ReplayHistory:
		return "replay-history"
	case result.OpenTranscript:
		return "transcript-pager"
	case result.OpenDebugOverlay:
		return "debug-overlay"
	case result.OpenWebEndpointsScreen:
		return "web-endpoints-screen"
	case result.OpenUsageScreen != nil:
		return "usage-screen"
	case result.OpenAccountScreen != nil:
		return "account-screen"
	case result.OpenAccountsScreen != nil:
		return "accounts-screen"
	case result.OpenResumePicker != nil:
		return "resume-picker"
	case result.OpenBacktrackPicker != nil:
		return "backtrack-picker"
	case result.OpenModelPicker != nil:
		return "model-picker"
	case result.OpenThemePicker != nil:
		return "theme-picker"
	case result.OpenSkillPicker != nil:
		return "skill-picker"
	case result.OpenExportPicker != nil:
		return "export-picker"
	case result.OpenMCPPicker != nil:
		return "mcp-picker"
	case result.ApplyBacktrack != nil:
		return "backtrack-apply"
	case result.SendObjective != "":
		return "send-objective"
	case result.SendMessageAfterCommit != "":
		return "send-message"
	case result.SendSkillTurn != nil:
		return "send-skill-turn"
	case result.RestoreComposerDraft != "":
		return "composer-draft"
	}
	return ""
}
