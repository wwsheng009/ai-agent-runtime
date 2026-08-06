package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func dispatchChatCommand(session *ChatSession, command string, noInteractive bool) bool {
	// Structured commands are resolved before legacy prompt clearing/direct
	// output. Once matched, they own the command completely and never fall
	// through to handleCommand or retry through raw stdout.
	if session == nil || !session.JSONOutput {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if handled {
			if err != nil {
				result = commandErrorResult(err)
			}
			renderErr := renderChatCommandResult(session, result, noInteractive)
			if result.ReplayHistory && session != nil {
				// /load: replay the loaded transcript after the confirmation
				// cell. The replay renderer owns its cells (one per message)
				// and falls back to plain output when no surface is present.
				printVisibleChatHistory(session, "已加载历史会话")
			}
			if result.OpenTranscript && session != nil {
				// /history in the unified TUI is a view operation over the already
				// canonical Scene transcript. The pager borrows TerminalSession's
				// alternate-screen lease; it never replays duplicate terminal rows.
				openChatTranscriptPager(session)
			}
			if renderErr == nil && result.OpenResumePicker != nil && session != nil {
				// /resume is a typed alternate-screen picker effect. Its final
				// selection is committed only after ScreenLease release, preserving
				// one physical terminal owner throughout the modal lifecycle.
				openChatResumePicker(session, *result.OpenResumePicker)
			}
			if renderErr == nil && result.OpenBacktrackPicker != nil && session != nil {
				// The backtrack picker mutates canonical transcript state only after
				// ScreenLease release. Its replacement transaction therefore never
				// overlaps the alternate-screen frame or primary repaint recovery.
				openChatBacktrackPicker(session, *result.OpenBacktrackPicker)
			}
			if renderErr == nil && result.OpenModelPicker != nil && session != nil {
				// /model picker: provider→model→reasoning stages share one alternate
				// screen; the apply step runs only after ScreenLease release, keeping
				// one physical terminal owner throughout the modal lifecycle.
				openChatModelPicker(session, *result.OpenModelPicker)
			}
			if renderErr == nil && result.OpenThemePicker != nil && session != nil {
				// /theme select is a typed live-preview picker effect; the confirmed
				// theme is applied only after ScreenLease release and primary
				// presenter recovery.
				openChatThemePicker(session, *result.OpenThemePicker)
			}
			if renderErr == nil && result.OpenSkillPicker != nil && session != nil {
				// /skills select is a typed picker effect; the confirmed skill
				// becomes a composer draft only after ScreenLease release and
				// primary presenter recovery.
				openChatSkillPicker(session, *result.OpenSkillPicker)
			}
			if renderErr == nil && result.OpenExportPicker != nil && session != nil {
				// /export bare is a typed picker effect; the export file write
				// runs only after ScreenLease release and primary presenter
				// recovery.
				openChatExportPicker(session, *result.OpenExportPicker)
			}
			if renderErr == nil && result.ApplyBacktrack != nil && session != nil {
				// Direct backtrack apply has no alternate screen, but it still owns
				// the same destructive transaction: actor mutation, canonical Scene
				// replacement, one result cell, then submit/draft mutation.
				applyUnifiedBacktrackRequest(session, result.ApplyBacktrack.Request)
			}
			if result.SendObjective != "" && session != nil {
				// /goal <objective>: stream the objective request through the
				// normal send pipeline after the confirmation cell commits.
				// The send error goes through the surface-aware output helper
				// so it stays visible without bypassing the owned viewport.
				if err := sendGoalObjectiveRequest(session, result.SendObjective); err != nil {
					printfDirectInteractiveOutput(session, "错误: %v\n", err)
				}
			}
			if renderErr == nil && result.RestoreComposerDraft != "" && session != nil {
				// /retry is a command-cell result followed by a Composer mutation.
				// Keep that effect after the command commit so retained transcript
				// order and the actor-owned prompt state never interleave.
				if err := restoreChatRetryDraft(session, result.RestoreComposerDraft); err != nil {
					_ = renderChatCommandResult(session, commandErrorResult(err), noInteractive)
				}
			}
			return result.Action == CommandQuit
		}
	}
	// TerminalSession ownership is a one-way renderer cutover. Do not route an
	// unstructured command into handleCommand here: several retained handlers
	// still contain direct stdout writes and legacy modal/surface lifecycles.
	// The gate emits a typed Scene-backed result (or exits) and fail-closes the
	// command until it has an explicit CommandResult/UI-action migration.
	if unifiedDirectInteractiveOutput(session) {
		return dispatchUnmigratedUnifiedChatCommand(session, command)
	}
	if !noInteractive {
		beginDirectInteractiveOutput(session)
	}
	return handleCommand(session, command, noInteractive)
}

// handleCommand 处理命令
func handleCommand(session *ChatSession, command string, noInteractive bool) bool {
	// 首先检查 /shell 和 /cmd 命令（带参数）
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	if commandMatches(cmdLower, "/shell") || commandMatches(cmdLower, "/cmd") {
		argument := extractCommandArgument(command)
		if strings.TrimSpace(argument) == "" {
			printChatCommandOutput(session, "错误: 需要指定 shell 命令\n用法: /shell [--output-bytes-cap <bytes> | --disable-output-cap] <命令>\n      /cmd   [--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
			return false
		}
		result, err := executeShellCommandDetailed(session, argument)
		if err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		// 将命令输出作为消息发送给 AI
		aiInput := buildShellCommandAIInput(result)
		response, err := sendMessage(session, aiInput)
		if err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		finishSuccessfulChatSend(session, response, noInteractive)
		return false
	}
	if commandMatches(cmdLower, "/function") || commandMatches(cmdLower, "/describe") {
		name, jsonOutput := extractCommandArgumentOptions(command)
		if name == "" {
			printChatCommandOutput(session, "错误: 需要指定 function 名称\n用法: /function <name> [--json] 或 /describe <name> [--json]")
			return false
		}
		printChatCommandOutput(session, formatFunctionDescriptor(session, name, jsonOutput))
		return false
	}
	if commandMatches(cmdLower, "/functions") || commandMatches(cmdLower, "/catalog") {
		prompt, jsonOutput := extractCommandArgumentOptions(command)
		if prompt == "" && jsonOutput {
			printChatCommandOutput(session, formatFunctionCatalogSummary(session, true))
			return false
		}
		if prompt == "" {
			printChatCommandOutput(session, "错误: 需要提供 prompt 预览最终暴露集合\n用法: /functions <prompt> [--json] 或 /catalog <prompt> [--json]")
			return false
		}
		printChatCommandOutput(session, formatFunctionExposurePreview(session, prompt, jsonOutput))
		return false
	}
	if commandMatches(cmdLower, "/call") || commandMatches(cmdLower, "/tool") {
		return handleDirectFunctionCommand(session, command)
	}
	if commandMatches(cmdLower, "/skills") {
		return handleSkillsMenuCommand(session, command)
	}
	if commandMatches(cmdLower, "/skill") {
		return handleDirectSkillCommand(session, command)
	}
	if commandMatches(cmdLower, "/sessions") {
		filter := session.SessionFilter
		filter.Query = strings.TrimSpace(extractCommandArgument(command))
		if err := printCurrentChatSessionSummaries(session, filter); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
		}
		return false
	}
	if commandMatches(cmdLower, "/load") {
		sessionID := extractCommandArgument(command)
		if strings.TrimSpace(sessionID) == "" {
			printChatCommandOutput(session, "错误: 需要指定会话 ID\n用法: /load <session-id>")
			return false
		}
		if err := loadRuntimeConversation(session, sessionID); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		// Route through the surface so ownedViewport retains the line in
		// transcript history. Raw fmt.Println after BeginOutput is invisible to
		// the double-buffer and leaves density holes on the next WriteOutput.
		printfDirectInteractiveOutput(session, "会话已加载\n")
		printCurrentRuntimeSession(session)
		if hasVisibleChatHistory(session) {
			// No raw blank separator: printVisibleChatHistory settles surface
			// layout debt and its header owns the spacing (same rule as
			// printResumeSuccess). A raw fmt.Println here bypasses the surface
			// and stacks a second blank row on top of the header gap.
			printVisibleChatHistory(session, "已加载历史会话")
		}
		return false
	}
	if commandMatches(cmdLower, "/goal") {
		return handleGoalCommand(session, command)
	}
	if commandMatches(cmdLower, "/title") || commandMatches(cmdLower, "/rename") {
		title := extractCommandArgument(command)
		if strings.TrimSpace(title) == "" {
			printChatCommandOutput(session, "错误: 需要指定会话标题\n用法: /title <title> 或 /rename <title>")
			return false
		}
		if err := updateChatSessionTitle(session, title); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		printfDirectInteractiveOutput(session, "会话标题已更新\n")
		return false
	}
	if commandMatches(cmdLower, "/image") {
		return handleImageGenerationCommand(session, command)
	}
	if commandMatches(cmdLower, "/attach") {
		return handleImageAttachmentCommand(session, command)
	}
	if commandMatches(cmdLower, "/queue") {
		return handleQueueCommand(session, command)
	}
	if commandMatches(cmdLower, "/retry") {
		return handleRetryCommand(session, command)
	}
	if commandMatches(cmdLower, "/compact") {
		return handleCompactCommand(session, command)
	}
	if commandMatches(cmdLower, "/backtrack") {
		return handleBacktrackCommand(session, command)
	}
	// /rewind with a numeric first arg is user-turn backtrack; non-numeric keeps
	// checkpoint-id restore reserved for a future dedicated path.
	if commandMatches(cmdLower, "/rewind") {
		arg := strings.TrimSpace(extractCommandArgument(command))
		if first := firstToken(arg); first != "" {
			if _, err := strconv.Atoi(first); err == nil {
				return handleBacktrackCommand(session, "/backtrack "+arg)
			}
		}
		if arg == "" || strings.EqualFold(firstToken(arg), "list") || strings.EqualFold(firstToken(arg), "ls") {
			return handleBacktrackCommand(session, "/backtrack "+arg)
		}
		printChatCommandOutput(session, "提示: /rewind <checkpoint_id> 尚未接线；数字参数请用 /backtrack <user_turn_index>\n用法: /backtrack [list|<index> --apply|--both|--edit|--submit]")
		return false
	}
	if commandMatches(cmdLower, "/model") {
		return handleModelCommand(session, command, noInteractive)
	}
	if commandMatches(cmdLower, "/login") {
		return handleLoginCommand(session, command, noInteractive)
	}
	if commandMatches(cmdLower, "/stream") {
		return applyStreamCommand(session, command)
	}
	if commandMatches(cmdLower, "/fast") {
		return applyFastCommand(session, command)
	}
	if commandMatches(cmdLower, "/theme") {
		return handleThemeCommand(session, command, noInteractive)
	}
	if commandMatches(cmdLower, "/reasoning") {
		return applyReasoningCommand(session, command)
	}
	if commandMatches(cmdLower, "/reasoning_effort") || commandMatches(cmdLower, "/reasoning-effort") {
		return handleReasoningEffortCommand(session, command, noInteractive)
	}
	if commandMatches(cmdLower, "/debug") {
		return handleDebugCommand(session, command)
	}
	if commandMatches(cmdLower, "/export") {
		return handleExportCommand(session, command)
	}
	if commandMatches(cmdLower, "/resume") {
		return handleResumeCommand(session, command)
	}
	if commandMatches(cmdLower, "/permission-mode") || commandMatches(cmdLower, "/mode") {
		return handlePermissionModeCommand(session, command)
	}
	if commandMatches(cmdLower, "/trust") {
		return handleTrustCommand(session, command)
	}
	if commandMatches(cmdLower, "/plan") {
		return handlePlanCommand(session, command)
	}
	if commandMatches(cmdLower, "/memory") {
		return handleMemoryCommand(session, command)
	}
	if commandMatches(cmdLower, "/approval-reuse") {
		return handleApprovalReuseCommand(session, command)
	}
	if commandMatches(cmdLower, "/agents") {
		handleChatAgentsCommand(session, command)
		return false
	}
	if commandMatches(cmdLower, "/timeline") {
		printChatTimeline(session, command)
		return false
	}
	if commandMatches(cmdLower, "/collab") {
		printChatCollab(session, command)
		return false
	}

	// 处理其他命令
	cmd := cmdLower
	switch cmd {
	case "/exit", "/quit", "/q":
		// The owned primary surface must own the farewell row as well. Writing
		// directly to stdout here lets a pending surface frame repaint over it,
		// which makes the final command result nondeterministic in a live TTY.
		printDirectInteractiveOutput(session, "再见！\n")
		return true

	case "/clear", "/cls":
		if !confirmClearConversationHistory(session) {
			return false
		}
		if err := replaceRuntimeMessages(session, nil); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		clearChatTurnRecovery(session)
		session.MsgCount = 0
		session.TurnRequestCount = 0
		session.turnPrimed = false
		resetChatConversationTokenUsage(session)
		ensureChatSystemPromptMessage(session)
		if err := syncRuntimeSessionFromChat(session); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		if session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
		printChatCommandOutput(session, "当前会话历史已清空")

	case "/new":
		if err := createNewRuntimeConversation(session, ""); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
			return false
		}
		printChatCommandOutput(session, "已创建新会话")
		printCurrentRuntimeSession(session)

	case "/s":
		return applyStreamShortcut(session, true)

	case "/normal", "/n":
		return applyStreamShortcut(session, false)

	case "/history", "/h":
		if printVisibleChatHistory(session, "对话历史") == 0 {
			printChatCommandOutput(session, "当前会话暂无历史消息")
		}

	case "/functions", "/catalog":
		printChatCommandOutput(session, formatFunctionCatalogSummary(session, false))

	case "/session":
		if session.RuntimeSession == nil {
			printChatCommandOutput(session, "当前没有持久化会话")
			return false
		}
		printCurrentRuntimeSession(session)

	case "/status":
		return handleStatusCommand(session, command)

	case "/debug":
		return handleDebugCommand(session, command)

	case "/export":
		return handleExportCommand(session, command)

	case "/agents":
		handleChatAgentsCommand(session, command)

	case "/timeline":
		printChatTimeline(session, command)

	case "/collab":
		printChatCollab(session, command)

	case "/compact":
		return handleCompactCommand(session, command)

	case "/sessions":
		if err := printCurrentChatSessionSummaries(session, session.SessionFilter); err != nil {
			printfChatCommandOutput(session, "错误: %v", err)
		}

	case "/yolo":
		if session == nil {
			printChatCommandOutput(session, "错误: 当前没有活动会话")
			return false
		}
		if !confirmBypassPermissionModeChange(session, "/yolo") {
			return false
		}
		session.PermissionMode = "bypass_permissions"
		session.RequestedPermissionMode = string(session.PermissionMode)
		session.EffectivePermissionMode = string(session.PermissionMode)
		if session.ActiveTeam != nil {
			session.ActiveTeam.PermissionMode = session.PermissionMode
		}
		warnIfChatSessionSyncFails(session, "toggle permission mode", syncRuntimeSessionFromChat(session))
		printChatCommandOutput(session, "提示: 已切换到 permission-mode=bypass_permissions（等价于 --yolo）")

	case "/image":
		handleImageGenerationCommand(session, cmd)

	case "/attach":
		handleImageAttachmentCommand(session, cmd)

	case "/help", "/?":
		printChatSlashHelp(session)

	default:
		printfChatCommandOutput(session, "错误: 未知命令: %s\n输入 /help 查看可用命令", cmd)
	}

	return false
}

// updateChatSessionTitle applies a successful /title or /rename mutation. The
// structured command adapter and the direct compatibility handler share this
// side-effect boundary; only the caller selects its output projection.
func updateChatSessionTitle(session *ChatSession, title string) error {
	if session == nil || session.RuntimeSession == nil {
		return fmt.Errorf("当前没有可更新的会话")
	}
	session.RuntimeSession.UpdateTitle(title)
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return err
	}
	refreshChatTitleMetadata(session)
	return nil
}

func confirmClearConversationHistory(session *ChatSession) bool {
	if session == nil {
		return false
	}
	messageCount := countChatStatusMessages(session.Messages)
	if messageCount == 0 && session.MsgCount > 0 {
		messageCount = session.MsgCount
	}
	if messageCount == 0 {
		return true
	}

	lines := []string{
		fmt.Sprintf("[会话] 即将清空当前会话中的 %d 条聊天消息。", messageCount),
		"[会话] 此操作会重置上下文与 token 统计，不能通过 undo 恢复。",
		"[会话] Goal、会话配置、团队绑定和本地文件不会被删除。",
	}
	if session.NoInteractive {
		printChatCommandOutput(session, strings.Join(append(lines, "错误: 非交互模式不能确认清空会话历史"), "\n"))
		return false
	}

	restoreInputMode := pushChatComposerInputMode(session, chatInputModeConfirmation)
	defer restoreInputMode()
	prompt := "[会话] 请输入 clear 确认清空（其他输入取消）： "
	readPrompt, cleanupPrompt, transientPrompt := showChatRuntimePriorityPrompt(session, lines, prompt)
	text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), readPrompt)
	cleanupPrompt()
	if err != nil {
		printChatCommandOutput(session, "已取消，会话历史未清空")
		return false
	}
	text = strings.TrimSpace(normalizeQueuedInputLine(text))
	if transientPrompt {
		renderChatRuntimePriorityPromptTranscript(session, lines, prompt, text)
	}
	if text != "clear" {
		printChatCommandOutput(session, "已取消，会话历史未清空")
		return false
	}
	return true
}

func handlePermissionModeCommand(session *ChatSession, command string) bool {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	value := extractCommandArgument(command)
	if strings.TrimSpace(value) == "" {
		printfChatCommandOutput(session, "当前 permission-mode: %s", session.PermissionMode)
		return false
	}
	mode, err := parseChatPermissionMode(value, false)
	if err != nil {
		printfChatCommandOutput(session, "错误: %v", err)
		return false
	}
	if mode == "bypass_permissions" && !confirmBypassPermissionModeChange(session, "/permission-mode") {
		return false
	}
	session.PermissionMode = mode
	session.RequestedPermissionMode = string(mode)
	session.EffectivePermissionMode = string(mode)
	if session.ActiveTeam != nil {
		session.ActiveTeam.PermissionMode = mode
	}
	warnIfChatSessionSyncFails(session, "toggle permission mode", syncRuntimeSessionFromChat(session))
	printfChatCommandOutput(session, "提示: 已切换到 permission-mode=%s", mode)
	return false
}

func confirmBypassPermissionModeChange(session *ChatSession, source string) bool {
	if session == nil {
		return false
	}
	currentMode := chatRuntimePermissionModeLabel(session)
	if currentMode == "bypass_permissions" {
		return true
	}

	lines := []string{
		"[权限] 即将切换到 bypass_permissions",
		"[权限] 影响：当前会话后续的 Agent 工具调用将不再逐次请求审批。",
		"[权限] 边界：profile/tool policy 的明确拒绝与 sandbox 约束仍然生效。",
	}
	if session.ActiveTeam != nil {
		teamID := strings.TrimSpace(session.ActiveTeam.TeamID)
		if teamID == "" {
			teamID = "<unknown>"
		}
		lines = append(lines, "[权限] ActiveTeam：team="+teamID+" 将同步切换到 bypass_permissions。")
	} else {
		lines = append(lines, "[权限] ActiveTeam：当前未绑定团队；本次仅更新当前会话。")
	}
	if source = strings.TrimSpace(source); source != "" {
		lines = append(lines, "[权限] 来源："+source)
	}
	if session.NoInteractive {
		printChatCommandOutput(session, strings.Join(append(lines, "错误: 非交互模式不能在会话内确认 bypass_permissions；请改用启动参数显式配置"), "\n"))
		return false
	}

	restoreInputMode := pushChatComposerInputMode(session, chatInputModeConfirmation)
	defer restoreInputMode()
	prompt := "[权限] 请输入 bypass_permissions 确认切换（其他输入取消）： "
	readPrompt, cleanupPrompt, transientPrompt := showChatRuntimePriorityPrompt(session, lines, prompt)
	text, err := func() (string, error) {
		endAction := beginChatTitleAction(session, "Permission Mode Confirmation Required")
		defer endAction()
		return chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), readPrompt)
	}()
	cleanupPrompt()
	if err != nil {
		printfChatCommandOutput(session, "已取消，permission-mode 保持为 %s", currentMode)
		return false
	}
	text = strings.TrimSpace(normalizeQueuedInputLine(text))
	if transientPrompt {
		renderChatRuntimePriorityPromptTranscript(session, lines, prompt, text)
	}
	if text != "bypass_permissions" {
		printfChatCommandOutput(session, "已取消，permission-mode 保持为 %s", currentMode)
		return false
	}
	return true
}

func handleImageAttachmentCommand(session *ChatSession, command string) bool {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		// /attach with no argument: list current attachments
		if len(session.ImagePaths) == 0 {
			printChatCommandOutput(session, "当前无待发送图片附件")
		} else {
			lines := []string{fmt.Sprintf("待发送图片附件 (%d):", len(session.ImagePaths))}
			for i, path := range session.ImagePaths {
				lines = append(lines, fmt.Sprintf("  [%d] %s", i+1, path))
			}
			printChatCommandOutput(session, strings.Join(lines, "\n"))
		}
		return false
	}
	if strings.EqualFold(arg, "clear") {
		count := len(session.ImagePaths)
		session.ImagePaths = nil
		refreshChatComposerContext(session)
		printfChatCommandOutput(session, "已清空 %d 个待发送图片附件", count)
		return false
	}
	if strings.HasPrefix(strings.ToLower(arg), "remove ") {
		if len(session.ImagePaths) == 0 {
			printChatCommandOutput(session, "错误: 当前没有可移除的图片附件")
			return false
		}
		indexText := strings.TrimSpace(arg[len("remove "):])
		index, err := strconv.Atoi(indexText)
		if err != nil || index < 1 || index > len(session.ImagePaths) {
			printfChatCommandOutput(session, "错误: 附件序号无效，可选范围为 1-%d", len(session.ImagePaths))
			return false
		}
		removed := session.ImagePaths[index-1]
		session.ImagePaths = append(session.ImagePaths[:index-1], session.ImagePaths[index:]...)
		refreshChatComposerContext(session)
		printfChatCommandOutput(session, "已移除图片附件: %s (当前剩余 %d 个)", removed, len(session.ImagePaths))
		return false
	}
	// /attach <path>: add image path
	path := strings.Trim(strings.TrimSpace(arg), `"'`)
	if warnings := llm.ValidateLocalInputImagePaths([]string{path}); len(warnings) > 0 {
		printfChatCommandOutput(session, "错误: 无法添加图片附件 %q；请确认文件存在、可读且为支持的非 SVG 图片", path)
		return false
	}
	for _, existing := range session.ImagePaths {
		if strings.EqualFold(strings.TrimSpace(existing), path) {
			printfChatCommandOutput(session, "提示: 图片附件已存在: %s", path)
			return false
		}
	}
	session.ImagePaths = append(session.ImagePaths, path)
	refreshChatComposerContext(session)
	printfChatCommandOutput(session, "已添加图片附件: %s (当前共 %d 个)", path, len(session.ImagePaths))
	return false
}

func handleQueueCommand(session *ChatSession, command string) bool {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	switch arg {
	case "", "status":
		count, draining := queuedInteractiveInputState(session)
		state := fmt.Sprintf("%d pending", count)
		if draining {
			state += " (draining)"
		}
		printfChatCommandOutput(session, "当前 queued input: %s", state)
		return false
	case "clear":
		discarded := discardPendingInteractiveInput(session)
		refreshChatComposerContext(session)
		printfChatCommandOutput(session, "已清空 queued input: %d", discarded)
		return false
	default:
		printChatCommandOutput(session, "错误: /queue 仅支持空参数或 clear\n用法: /queue 或 /queue clear")
		return false
	}
}

func handleCompactCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredCompactCommand(session, command), false)
		return false
	}
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	mode, err := normalizeChatCompactMode(extractCommandArgument(command))
	if err != nil {
		printfChatCommandOutput(session, "错误: %v", err)
		return false
	}

	report, err := runManualChatCompact(session, mode)
	if report != nil {
		applyChatCompactContextUsage(session, report.Result, report.Status, true)
		// Compact rewrites the sticky title in place; refresh the terminal
		// window/tab title so "thread" reflects compact #N immediately.
		if report.Result != nil {
			refreshChatTitleMetadata(session)
		}
		printChatCommandOutput(session, formatChatCompactReport(report))
	}
	if err != nil {
		printfChatCommandOutput(session, "错误: %v", err)
	}
	return false
}

func handleApprovalReuseCommand(session *ChatSession, command string) bool {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	value := extractCommandArgument(command)
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "status" || value == "list" {
		printApprovalReuseStatus(session)
		return false
	}
	if value == "clear" || value == "revoke" {
		cleared := 0
		if session.RuntimeEventBridge != nil {
			cleared = session.RuntimeEventBridge.clearApprovalGrants()
		}
		printfChatCommandOutput(session, "已撤销 approval-reuse 授权: %d", cleared)
		return false
	}
	mode, err := parseChatApprovalReuseMode(value)
	if err != nil {
		printfChatCommandOutput(session, "错误: %v", err)
		return false
	}
	session.ApprovalReuseMode = mode
	cleared := 0
	if session.RuntimeEventBridge != nil {
		cleared = session.RuntimeEventBridge.clearApprovalGrants()
	}
	warnIfChatSessionSyncFails(session, "toggle approval reuse", syncRuntimeSessionFromChat(session))
	printfChatCommandOutput(session, "提示: 已切换到 approval-reuse=%s；已撤销旧作用域授权 %d 条", formatChatApprovalReuseMode(mode), cleared)
	return false
}

func printApprovalReuseStatus(session *ChatSession) {
	if session == nil {
		return
	}
	lines := []string{fmt.Sprintf("当前 approval-reuse: %s", formatChatApprovalReuseMode(session.ApprovalReuseMode))}
	grantLines := []string(nil)
	if session.RuntimeEventBridge != nil {
		grantLines = session.RuntimeEventBridge.approvalGrantStatusLines(time.Now())
	}
	if len(grantLines) == 0 {
		lines = append(lines, "当前没有生效的审批复用授权")
		printChatCommandOutput(session, strings.Join(lines, "\n"))
		return
	}
	lines = append(lines, fmt.Sprintf("生效中的审批复用授权: %d", len(grantLines)))
	lines = append(lines, grantLines...)
	lines = append(lines, "使用 /approval-reuse clear 可立即撤销全部授权")
	printChatCommandOutput(session, strings.Join(lines, "\n"))
}

func commandMatches(commandLower, name string) bool {
	commandLower = strings.TrimSpace(strings.ToLower(commandLower))
	name = strings.TrimSpace(strings.ToLower(name))
	if commandLower == name {
		return true
	}
	if !strings.HasPrefix(commandLower, name) || len(commandLower) <= len(name) {
		return false
	}
	switch commandLower[len(name)] {
	case ' ', '\t':
		return true
	default:
		return false
	}
}

func extractCommandArgument(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if idx := strings.IndexAny(command, " \t"); idx >= 0 {
		return strings.TrimSpace(command[idx+1:])
	}
	return ""
}

func extractCommandArgumentOptions(command string) (string, bool) {
	argument := extractCommandArgument(command)
	return stripJSONOption(argument)
}

func stripJSONOption(argument string) (string, bool) {
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return "", false
	}
	if argument == "--json" {
		return "", true
	}
	if strings.HasPrefix(argument, "--json ") {
		return strings.TrimSpace(argument[len("--json "):]), true
	}
	if strings.HasSuffix(argument, " --json") {
		return strings.TrimSpace(argument[:len(argument)-len(" --json")]), true
	}
	return argument, false
}

type aicliFunctionDescriptorReport struct {
	FunctionName string                 `json:"function_name"`
	Descriptor   *capability.Descriptor `json:"descriptor,omitempty"`
}

type aicliFunctionCatalogReport struct {
	Stats   aicliFunctionCatalogStats       `json:"stats"`
	Builtin []aicliFunctionDescriptorReport `json:"builtin,omitempty"`
	Skills  []aicliFunctionDescriptorReport `json:"skills,omitempty"`
}

func formatFunctionCatalogSummary(session *ChatSession, jsonOutput bool) string {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return formatCommandError("Function Catalog: 未初始化", jsonOutput)
	}

	report := buildFunctionCatalogReport(catalog)
	if jsonOutput {
		return marshalIndentedJSON(report)
	}

	stats := report.Stats
	descriptors := append([]aicliFunctionDescriptorReport(nil), report.Builtin...)
	descriptors = append(descriptors, report.Skills...)
	lines := []string{
		fmt.Sprintf("Function Catalog: total=%d builtin=%d skills=%d", stats.TotalFunctions, stats.BuiltinTools, stats.SkillFunctions),
	}
	if len(descriptors) == 0 {
		lines = append(lines, "Functions: <none>")
		return strings.Join(lines, "\n")
	}

	builtinLines := make([]string, 0)
	skillLines := make([]string, 0)
	for _, item := range descriptors {
		descriptor := item.Descriptor
		if descriptor == nil {
			continue
		}
		line := fmt.Sprintf("  - %s [%s]", item.FunctionName, descriptor.Kind)
		if source, _ := descriptor.Metadata["source"].(string); source != "" {
			line += " source=" + source
		}
		if skillPath, _ := descriptor.Metadata["skill_path"].(string); skillPath != "" {
			line += " path=" + skillPath
		}
		if descriptor.Category != "" {
			line += " category=" + descriptor.Category
		}
		if descriptor.Description != "" {
			line += " :: " + descriptor.Description
		}
		if descriptor.Kind == "skill" || strings.HasPrefix(descriptor.Name, skillFunctionPrefix) {
			skillLines = append(skillLines, line)
		} else {
			builtinLines = append(builtinLines, line)
		}
	}
	sort.Strings(builtinLines)
	sort.Strings(skillLines)

	lines = append(lines, "Builtin Tools:")
	if len(builtinLines) == 0 {
		lines = append(lines, "  <none>")
	} else {
		lines = append(lines, builtinLines...)
	}

	lines = append(lines, "Skill Functions:")
	if len(skillLines) == 0 {
		lines = append(lines, "  <none>")
	} else {
		lines = append(lines, skillLines...)
	}

	return strings.Join(lines, "\n")
}

func buildFunctionCatalogReport(catalog *aicliFunctionCatalog) *aicliFunctionCatalogReport {
	if catalog == nil {
		return nil
	}

	report := &aicliFunctionCatalogReport{
		Stats: catalog.Stats(),
	}
	for _, descriptor := range catalog.Descriptors() {
		if descriptor == nil {
			continue
		}
		item := aicliFunctionDescriptorReport{
			FunctionName: descriptorDisplayName(descriptor),
			Descriptor:   descriptor,
		}
		if descriptor.Kind == "skill" || strings.HasPrefix(item.FunctionName, skillFunctionPrefix) {
			report.Skills = append(report.Skills, item)
			continue
		}
		report.Builtin = append(report.Builtin, item)
	}
	return report
}

func formatFunctionExposurePreview(session *ChatSession, prompt string, jsonOutput bool) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return formatCommandError("错误: prompt 不能为空", jsonOutput)
	}

	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return formatCommandError("Function Catalog: 未初始化", jsonOutput)
	}

	report := buildFunctionExposureReportForPrompt(session, prompt)
	if report == nil {
		return formatCommandError("Function Exposure Preview: 无可用 functions", jsonOutput)
	}
	if jsonOutput {
		return marshalIndentedJSON(report)
	}

	lines := []string{
		"Function Exposure Preview:",
		"Prompt: " + report.Prompt,
		fmt.Sprintf("Mode: %s", report.Mode),
		fmt.Sprintf("Include Builtin: %t", report.IncludeBuiltin),
		"Builtin Exposed: " + formatPreviewList(report.BuiltinFunctions),
		"Skill Exposed: " + formatPreviewList(report.SkillFunctions),
		"Final Functions: " + formatPreviewList(report.FinalFunctionNames),
	}
	if len(report.ExplicitMentions) > 0 {
		lines = append(lines, "Explicit Mentions: "+strings.Join(report.ExplicitMentions, ", "))
	}
	if len(report.PreviouslyCalled) > 0 {
		lines = append(lines, "Previously Called: "+strings.Join(report.PreviouslyCalled, ", "))
	}
	lines = append(lines, "Routed Skills: "+formatPreviewList(report.RoutedSkills))
	if len(report.Candidates) > 0 {
		candidateParts := make([]string, 0, len(report.Candidates))
		for _, candidate := range report.Candidates {
			if candidate.FunctionName == "" {
				continue
			}
			part := fmt.Sprintf("%s(score=%.3f matched_by=%s)", candidate.FunctionName, candidate.Score, candidate.MatchedBy)
			candidateParts = append(candidateParts, part)
		}
		lines = append(lines, "Candidates: "+formatPreviewList(candidateParts))
	}

	return strings.Join(lines, "\n")
}

func formatFunctionDescriptor(session *ChatSession, name string, jsonOutput bool) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return formatCommandError("错误: function 名称不能为空", jsonOutput)
	}

	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return formatCommandError("Function Catalog: 未初始化", jsonOutput)
	}

	resolvedName, _, err := resolveDirectCallableFunctionName(session, name, false)
	if err != nil {
		return formatCommandError("错误: "+err.Error(), jsonOutput)
	}

	descriptor := catalog.Descriptor(resolvedName)
	if descriptor == nil {
		return formatCommandError(fmt.Sprintf("错误: 未找到 function: %s", name), jsonOutput)
	}
	if jsonOutput {
		return marshalIndentedJSON(aicliFunctionDescriptorReport{
			FunctionName: descriptorDisplayName(descriptor),
			Descriptor:   descriptor,
		})
	}

	lines := []string{
		fmt.Sprintf("Function: %s", descriptorDisplayName(descriptor)),
		fmt.Sprintf("Kind: %s", descriptor.Kind),
	}
	if callableName, _ := descriptor.Metadata["function_name"].(string); callableName != "" && callableName != descriptor.Name {
		lines = append(lines, "Capability: "+descriptor.Name)
	}
	if descriptor.Description != "" {
		lines = append(lines, "Description: "+descriptor.Description)
	}
	if descriptor.Category != "" {
		lines = append(lines, "Category: "+descriptor.Category)
	}
	if len(descriptor.Capabilities) > 0 {
		lines = append(lines, "Capabilities: "+strings.Join(descriptor.Capabilities, ", "))
	}
	if len(descriptor.Labels) > 0 {
		lines = append(lines, "Labels: "+strings.Join(descriptor.Labels, ", "))
	}
	if len(descriptor.Triggers) > 0 {
		triggerParts := make([]string, 0, len(descriptor.Triggers))
		for _, trigger := range descriptor.Triggers {
			part := trigger.Type
			if len(trigger.Values) > 0 {
				part += ":" + strings.Join(trigger.Values, "|")
			}
			triggerParts = append(triggerParts, part)
		}
		lines = append(lines, "Triggers: "+strings.Join(triggerParts, ", "))
	}
	if descriptor.Source != nil {
		sourceParts := make([]string, 0, 3)
		if descriptor.Source.Layer != "" {
			sourceParts = append(sourceParts, "layer="+descriptor.Source.Layer)
		}
		if descriptor.Source.Dir != "" {
			sourceParts = append(sourceParts, "dir="+descriptor.Source.Dir)
		}
		if descriptor.Source.Path != "" {
			sourceParts = append(sourceParts, "path="+descriptor.Source.Path)
		}
		if len(sourceParts) > 0 {
			lines = append(lines, "Source: "+strings.Join(sourceParts, " "))
		}
	}
	if len(descriptor.Metadata) > 0 {
		metaKeys := make([]string, 0, len(descriptor.Metadata))
		for key := range descriptor.Metadata {
			metaKeys = append(metaKeys, key)
		}
		sort.Strings(metaKeys)
		metaParts := make([]string, 0, len(metaKeys))
		for _, key := range metaKeys {
			metaParts = append(metaParts, fmt.Sprintf("%s=%v", key, descriptor.Metadata[key]))
		}
		lines = append(lines, "Metadata: "+strings.Join(metaParts, ", "))
	}

	return strings.Join(lines, "\n")
}

func formatCommandError(message string, jsonOutput bool) string {
	if !jsonOutput {
		return message
	}
	return marshalIndentedJSON(map[string]string{
		"error": message,
	})
}

func marshalIndentedJSON(value interface{}) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(data)
}

func descriptorDisplayName(descriptor *capability.Descriptor) string {
	if descriptor == nil {
		return ""
	}
	if descriptor.Metadata != nil {
		if callableName, _ := descriptor.Metadata["function_name"].(string); callableName != "" {
			return callableName
		}
	}
	return descriptor.Name
}

func formatPreviewList(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

// isDangerousCommand 检查命令是否危险
func isDangerousCommand(cmd string) bool {
	// 常见危险命令模式
	dangerousPatterns := []string{
		"rm -rf", "rm -r", "rm -f",
		"del /f", "del /s", "del /q",
		"format ",
		"shutdown ", "halt ", "reboot ",
		":(){ :|:& };:", // fork bomb
		"cat /dev/zero", "dd if=/dev/zero",
		"mkfs.",   // 文件系统格式化
		"> /dev/", // 直接写入设备
	}

	cmdLower := strings.ToLower(cmd)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(cmdLower, pattern) {
			return true
		}
	}
	return false
}

// prefixPowershellUTF8ForInteractiveCommand keeps Windows shell output UTF-8 encoded.
func prefixPowershellUTF8ForInteractiveCommand(cmd *exec.Cmd) {
	if len(cmd.Args) < 3 {
		return
	}
	lastIdx := len(cmd.Args) - 1
	cmd.Args[lastIdx] = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + cmd.Args[lastIdx]
}

type shellCommandResult struct {
	ExecutedCommand       string
	Output                string
	Capture               runtimeexecutor.CombinedOutputCapture
	Config                ShellCommandConfig
	RawOutputArtifactPath string
	Shell                 runtimeexecutor.Shell
}

func defaultShellCommandConfig() ShellCommandConfig {
	return ShellCommandConfig{
		Timeout:        DefaultShellTimeout,
		MaxLines:       DefaultShellMaxLines,
		MaxOutputSize:  DefaultShellMaxOutputSize,
		OutputBytesCap: runtimeexecutor.DefaultRetainedOutputBytes,
	}
}

func shellCommandCaptureLimit(cfg ShellCommandConfig) int {
	if cfg.DisableOutputCap {
		return runtimeexecutor.DisableRetainedOutputLimit
	}
	if cfg.OutputBytesCap > 0 {
		return cfg.OutputBytesCap
	}
	if cfg.MaxOutputSize > 0 {
		return cfg.MaxOutputSize
	}
	return runtimeexecutor.DefaultRetainedOutputBytes
}

func parseShellCommandInvocation(raw string) (string, ShellCommandConfig, error) {
	cfg := defaultShellCommandConfig()
	explicitOutputCap := false
	command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "!"))
	for {
		command = strings.TrimSpace(command)
		switch {
		case strings.HasPrefix(command, "--disable-output-cap"):
			if len(command) > len("--disable-output-cap") && !isShellCommandOptionSeparator(command[len("--disable-output-cap")]) {
				return "", cfg, fmt.Errorf("无法解析 shell 选项: %s", command)
			}
			cfg.DisableOutputCap = true
			command = strings.TrimSpace(command[len("--disable-output-cap"):])
		case strings.HasPrefix(command, "--output-bytes-cap="):
			token, remaining := splitLeadingShellCommandToken(command)
			value := strings.TrimSpace(token[len("--output-bytes-cap="):])
			if value == "" {
				return "", cfg, fmt.Errorf("output-bytes-cap 需要正整数值")
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return "", cfg, fmt.Errorf("output-bytes-cap 需要正整数值，收到 %q", value)
			}
			cfg.OutputBytesCap = parsed
			explicitOutputCap = true
			command = remaining
		case strings.HasPrefix(command, "--output-bytes-cap"):
			if len(command) > len("--output-bytes-cap") && !isShellCommandOptionSeparator(command[len("--output-bytes-cap")]) {
				return "", cfg, fmt.Errorf("无法解析 shell 选项: %s", command)
			}
			valueToken, remaining, err := consumeRequiredShellCommandValue(command[len("--output-bytes-cap"):])
			if err != nil {
				return "", cfg, err
			}
			parsed, parseErr := strconv.Atoi(valueToken)
			if parseErr != nil || parsed <= 0 {
				return "", cfg, fmt.Errorf("output-bytes-cap 需要正整数值，收到 %q", valueToken)
			}
			cfg.OutputBytesCap = parsed
			explicitOutputCap = true
			command = remaining
		default:
			if cfg.DisableOutputCap && explicitOutputCap {
				return "", cfg, fmt.Errorf("output-bytes-cap 不能与 disable-output-cap 同时设置")
			}
			if strings.TrimSpace(command) == "" {
				return "", cfg, fmt.Errorf("需要在 shell 选项后提供要执行的命令")
			}
			return strings.TrimSpace(command), cfg, nil
		}
	}
}

func isShellCommandOptionSeparator(ch byte) bool {
	return ch == ' ' || ch == '\t'
}

func splitLeadingShellCommandToken(command string) (string, string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", ""
	}
	if idx := strings.IndexAny(command, " \t"); idx >= 0 {
		return command[:idx], strings.TrimSpace(command[idx+1:])
	}
	return command, ""
}

func consumeRequiredShellCommandValue(rest string) (string, string, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", fmt.Errorf("output-bytes-cap 需要正整数值")
	}
	value, remaining := splitLeadingShellCommandToken(rest)
	if value == "" {
		return "", "", fmt.Errorf("output-bytes-cap 需要正整数值")
	}
	return value, remaining, nil
}

func buildShellCommandAIInput(result shellCommandResult) string {
	lines := []string{
		fmt.Sprintf("我执行了命令: %s", result.ExecutedCommand),
	}
	if metadata := result.Shell.Metadata(); len(metadata) > 0 {
		lines = append(lines, fmt.Sprintf("实际执行 Shell: %s", result.Shell.String()))
	}
	lines = append(lines,
		fmt.Sprintf("命令输出捕获状态: %s", shellCommandCaptureStatusLine(result)),
	)
	if result.Capture.Truncated && strings.TrimSpace(result.RawOutputArtifactPath) != "" {
		lines = append(lines, fmt.Sprintf("完整原始输出已旁路保存: %s", result.RawOutputArtifactPath))
	}
	lines = append(lines, "输出如下：", result.Output)
	return strings.Join(lines, "\n")
}

func shellCommandCaptureStatusLine(result shellCommandResult) string {
	retainedBytes := result.Capture.RetainedBytes
	if retainedBytes == 0 && result.Output != "" {
		retainedBytes = len(result.Output)
	}
	totalBytes := result.Capture.TotalBytes
	if totalBytes == 0 && result.Output != "" {
		totalBytes = len(result.Output)
	}
	limitValue := "disabled"
	if !result.Capture.CaptureLimitDisabled {
		limitBytes := result.Capture.CaptureLimitBytes
		if limitBytes <= 0 {
			limitBytes = shellCommandCaptureLimit(result.Config)
		}
		limitValue = fmt.Sprintf("%dB", limitBytes)
	}
	parts := []string{
		fmt.Sprintf("complete=%t", !result.Capture.Truncated),
		fmt.Sprintf("limit=%s", limitValue),
		fmt.Sprintf("retained=%dB", retainedBytes),
		fmt.Sprintf("total=%dB", totalBytes),
	}
	if result.Capture.OmittedBytes > 0 {
		parts = append(parts, fmt.Sprintf("omitted=%dB", result.Capture.OmittedBytes))
	}
	return strings.Join(parts, "; ")
}

func logLocalShellCommandDebug(session *ChatSession, result shellCommandResult, err error) {
	if session == nil {
		return
	}
	writeSessionDebugInfo(session, shellCommandDebugLine(result, err), false)
}

func shellCommandDebugLine(result shellCommandResult, err error) string {
	status := shellCommandCaptureStatusLine(result)
	shellSuffix := ""
	if metadata := result.Shell.Metadata(); len(metadata) > 0 {
		shellType, _ := metadata["shell_type"].(string)
		shellPath, _ := metadata["shell_path"].(string)
		shellSuffix = fmt.Sprintf(" shell_type=%q shell_path=%q", shellType, shellPath)
	}
	artifactSuffix := ""
	if strings.TrimSpace(result.RawOutputArtifactPath) != "" {
		artifactSuffix = fmt.Sprintf(" artifact=%q", result.RawOutputArtifactPath)
	}
	if err != nil {
		return fmt.Sprintf("[shell-debug] local command=%q%s success=false capture={%s}%s error=%q", result.ExecutedCommand, shellSuffix, status, artifactSuffix, err.Error())
	}
	return fmt.Sprintf("[shell-debug] local command=%q%s success=true capture={%s}%s", result.ExecutedCommand, shellSuffix, status, artifactSuffix)
}

func executeShellCommand(session *ChatSession, cmdStr string) (string, error) {
	result, err := executeShellCommandDetailed(session, cmdStr)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

func shouldRenderLocalShellDiff(command string, capture runtimeexecutor.CombinedOutputCapture, commandErr error) bool {
	return commandErr == nil &&
		!capture.Truncated &&
		runtimeexecutor.IsGitDiffCommand(command) &&
		runtimeexecutor.LooksLikeUnifiedDiffOutput(capture.Output)
}

// executeShellCommand 执行 shell 命令
func executeShellCommandDetailed(session *ChatSession, cmdStr string) (shellCommandResult, error) {
	executedCommand, cfg, err := parseShellCommandInvocation(cmdStr)
	if err != nil {
		return shellCommandResult{ExecutedCommand: executedCommand, Config: cfg}, err
	}

	result := shellCommandResult{
		ExecutedCommand: executedCommand,
		Config:          cfg,
	}
	if unifiedDirectInteractiveOutput(session) {
		return result, fmt.Errorf("interactive shell command is not migrated to the unified renderer")
	}
	bufferGitDiffOutput := runtimeexecutor.IsGitDiffCommand(executedCommand)
	gitDiffRenderBufferLimit := runtimeexecutor.DefaultRetainedOutputBytes
	if captureLimit := shellCommandCaptureLimit(cfg); captureLimit > 0 && captureLimit < gitDiffRenderBufferLimit {
		gitDiffRenderBufferLimit = captureLimit
	}
	var bufferedGitDiff bytes.Buffer

	// 检查危险命令
	if isDangerousCommand(executedCommand) {
		printfDirectInteractiveOutput(session, "\n警告: 检测到可能危险的命令: %s\n", executedCommand)
		if !showRuntimeComposerPrompt(session, "确认执行? (yes/no): ") {
			beginDirectInteractiveOutput(session)
			fmt.Print("确认执行? (yes/no): ")
		}
		confirm, err := func() (string, error) {
			endAction := beginChatTitleAction(session, "Command Approval Required")
			defer endAction()
			return chatInteractiveReadTransientLine(session, context.Background())
		}()
		if err != nil || strings.ToLower(strings.TrimSpace(normalizeQueuedInputLine(confirm))) != "yes" {
			return result, fmt.Errorf("命令已取消")
		}
	}

	endRunning := beginChatTitleTool(session, "local-shell")
	defer endRunning()

	artifactWriter, artifactErr := openLocalShellArtifactWriter(session, executedCommand)
	if artifactErr != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[shell-debug] local shell artifact create failed command=%q error=%q", executedCommand, artifactErr.Error()), false)
	} else if artifactWriter != nil {
		result.RawOutputArtifactPath = artifactWriter.Path()
		artifactFile := artifactWriter
		defer func() {
			if closeErr := artifactFile.Close(); closeErr != nil {
				writeSessionDebugInfo(session, fmt.Sprintf("[shell-debug] local shell artifact close failed path=%q error=%q", artifactFile.Path(), closeErr.Error()), false)
			}
		}()
	}

	// Use the same detected user shell as tool-based command execution.
	shell := runtimeexecutor.DefaultUserShell()
	result.Shell = shell
	shellCmd := shell.DeriveExecArgs(executedCommand, false)

	fmt.Printf("\n执行命令: %s\n", executedCommand)
	fmt.Println("--- 输出 ---")

	// 创建带超时和中断的 context
	ctx, cancel := context.WithTimeout(session.cancelCtx, cfg.Timeout)
	defer cancel()

	// 执行命令
	cmd := exec.CommandContext(ctx, shellCmd[0], shellCmd[1:]...)
	if shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh {
		prefixPowershellUTF8ForInteractiveCommand(cmd)
	}
	runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)

	// 启动 Goroutine 实时输出命令结果（支持长时间运行的命令）
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("创建标准输出管道失败: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return result, fmt.Errorf("创建标准错误管道失败: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("启动命令失败: %w", err)
	}

	// 实时读取输出
	outputChan := make(chan []byte, 100)
	doneChan := make(chan struct{})
	captureAccumulator := runtimeexecutor.NewOutputCaptureAccumulator(shellCommandCaptureLimit(cfg))

	var wg sync.WaitGroup
	readPipe := func(reader io.Reader) {
		defer wg.Done()
		buf := make([]byte, 1024)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				outputChan <- chunk
			}
			if err != nil {
				break
			}
		}
	}

	wg.Add(2)
	go readPipe(stdoutPipe)
	go readPipe(stderrPipe)
	go func() {
		wg.Wait()
		close(outputChan)
		close(doneChan)
	}()

	// 实时打印输出，同时按配置保留传递给 AI 的输出窗口
	for {
		select {
		case chunk, ok := <-outputChan:
			if !ok {
				// 输出通道关闭，等待 goroutine 完成
				<-doneChan
				if !bufferGitDiffOutput {
					fmt.Println()
				}
				goto commandDone
			}
			_, _ = captureAccumulator.Write(chunk)
			if bufferGitDiffOutput {
				_, _ = bufferedGitDiff.Write(chunk)
				if bufferedGitDiff.Len() > gitDiffRenderBufferLimit {
					// Rendering waits for a complete diff, but an explicitly
					// disabled/large capture limit must not create an unbounded
					// in-memory display buffer. Fall back to the original stream.
					fmt.Print(bufferedGitDiff.String())
					bufferedGitDiff.Reset()
					bufferGitDiffOutput = false
				}
			} else {
				fmt.Print(string(chunk))
			}
			if artifactWriter != nil {
				if err := artifactWriter.WriteChunk(chunk); err != nil {
					writeSessionDebugInfo(session, fmt.Sprintf("[shell-debug] local shell artifact write failed path=%q error=%q", artifactWriter.Path(), err.Error()), false)
					_ = artifactWriter.Abort()
					artifactWriter = nil
					result.RawOutputArtifactPath = ""
					recordLocalShellArtifactPath(session, "")
				}
			}

		case <-ctx.Done():
			// 检查是否是用户中断
			if session.IsInterrupted() {
				cmd.Process.Kill()
				<-doneChan
				if bufferGitDiffOutput {
					fmt.Print(bufferedGitDiff.String())
				}
				capture := captureAccumulator.Result()
				result.Capture = capture
				result.Output = capture.Output
				logLocalShellCommandDebug(session, result, fmt.Errorf("用户中断"))
				fmt.Println("\n[已中断] 命令执行已停止")
				return result, fmt.Errorf("用户中断")
			}
			// 超时
			cmd.Process.Kill()
			<-doneChan
			if bufferGitDiffOutput {
				fmt.Print(bufferedGitDiff.String())
			}
			capture := captureAccumulator.Result()
			result.Capture = capture
			result.Output = capture.Output
			logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行超时（超过 %v）", cfg.Timeout))
			fmt.Println("\n[超时] 命令执行超时")
			return result, fmt.Errorf("命令执行超时（超过 %v）", cfg.Timeout)
		}
	}

commandDone: // 等待命令完成
	// 检查命令执行状态
	outputCapture := captureAccumulator.Result()
	outputStr := outputCapture.Output
	result.Capture = outputCapture
	result.Output = outputStr

	waitErr := cmd.Wait()
	renderDeferredDiff := bufferGitDiffOutput && shouldRenderLocalShellDiff(executedCommand, outputCapture, waitErr)
	var renderedDiff string
	if renderDeferredDiff {
		if supplement := renderDiffOutput(outputStr, "Diff"); supplement != "" {
			renderedDiff = ui.FormatAssistantSupplementBlock(supplement)
		}
	}
	if bufferGitDiffOutput && renderedDiff == "" {
		fmt.Print(bufferedGitDiff.String())
		fmt.Println()
	}

	if waitErr != nil {
		// 命令执行失败（返回非零状态码）
		// 针对常见错误给出友好提示
		cmdLower := strings.ToLower(cmdStr)
		cmdParts := strings.Fields(cmdStr)

		var friendlyHint string
		exitCode := -1
		if exitError, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}

		// 获取主命令名
		mainCmd := ""
		if len(cmdParts) > 0 {
			mainCmd = strings.ToLower(cmdParts[0])
		}

		switch {
		case cmdLower == "pwd" && runtime.GOOS == "windows":
			// 在 Windows 上执行 pwd（这是 Unix 命令）
			if runtimeexecutor.DefaultUserShell().Type == runtimeexecutor.ShellTypeCmd {
				friendlyHint = "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
			}

		case mainCmd == "ls" && runtime.GOOS == "windows":
			// 在 Windows 上执行 ls（这是 Unix 命令）
			friendlyHint = "提示: Windows 下请使用 `dir` 查看目录内容"

		case exitCode == 127:
			// Unix: exit code 127 通常表示命令未找到
			friendlyHint = "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"

		case runtime.GOOS == "windows" && exitCode == 1 && len(cmdParts) > 0:
			// Windows: 检查是否是常见命令未找到
			// Windows 内置命令: dir, cd, echo, type, del, copy, move, md, rd, cls, ver 等
			windowsCommands := map[string]bool{
				"dir": true, "cd": true, "echo": true, "type": true,
				"del": true, "copy": true, "move": true, "rename": true,
				"md": true, "mkdir": true, "rd": true, "rmdir": true,
				"cls": true, "ver": true, "vol": true, "date": true,
				"time": true, "set": true, "path": true, "chdir": true,
				"help": true, "exit": true, "pause": true, "ipconfig": true,
				"ping": true, "tracert": true, "netstat": true, "tasklist": true,
				"taskkill": true, "format": true, "shutdown": true,
			}

			if !windowsCommands[mainCmd] {
				// 不是 Windows 内置命令，可能是未安装的外部命令
				friendlyHint = "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"
			}

		case strings.Contains(strings.ToLower(outputStr), "permission") ||
			strings.Contains(strings.ToLower(outputStr), "access") && strings.Contains(strings.ToLower(outputStr), "denied"):
			friendlyHint = "提示: 权限不足，请检查是否有执行该命令的权限"

		case strings.Contains(outputStr, "no such file or directory") ||
			strings.Contains(outputStr, "cannot find the path"):
			friendlyHint = "提示: 文件或目录不存在"
		}

		if friendlyHint != "" {
			logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行失败: %w\n%s", waitErr, friendlyHint))
			return result, fmt.Errorf("命令执行失败: %w\n%s", waitErr, friendlyHint)
		}
		logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行失败: %w", waitErr))
		return result, fmt.Errorf("命令执行失败: %w", waitErr)
	}

	// 命令执行成功
	if outputStr == "" {
		logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行成功，但没有输出"))
		return result, fmt.Errorf("命令执行成功，但没有输出")
	}

	if outputCapture.Truncated {
		fmt.Printf("[提示] 命令输出较大，传递给 AI 的内容已按 capture limit 截断：total=%dB retained=%dB omitted=%dB limit=%dB\n",
			outputCapture.TotalBytes, outputCapture.RetainedBytes, outputCapture.OmittedBytes, outputCapture.CaptureLimitBytes)
		if strings.TrimSpace(result.RawOutputArtifactPath) != "" {
			fmt.Printf("[提示] 完整原始输出已保存到: %s\n", resolveAbsoluteChatPath(result.RawOutputArtifactPath))
		}
	}
	if renderedDiff != "" {
		fmt.Println(renderedDiff)
		fmt.Println()
	}

	fmt.Println("--- 完成 ---")
	fmt.Println()

	logLocalShellCommandDebug(session, result, nil)
	return result, nil
}
