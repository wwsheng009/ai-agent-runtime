package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// executeStructuredClearCommand migrates /clear and /cls to the unified
// rendering command channel. The confirmation reuses the TTY-safe priority-line
// confirm primitives shared with the approval flow; the cancellation message
// becomes the atomic command cell instead of a transient print, and the
// mutation side effects mirror the legacy handler exactly.
func executeStructuredClearCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	confirmed, message := evalClearConversationConfirmation(session)
	if !confirmed {
		if message == "" {
			message = "已取消，会话历史未清空"
		}
		return commandTextResult(message)
	}
	if err := replaceRuntimeMessages(session, nil); err != nil {
		return commandErrorResult(err)
	}
	clearChatTurnRecovery(session)
	session.MsgCount = 0
	session.TurnRequestCount = 0
	session.turnPrimed = false
	resetChatConversationTokenUsage(session)
	ensureChatSystemPromptMessage(session)
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return commandErrorResult(err)
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return commandTextResult("当前会话历史已清空")
}

// executeStructuredYoloCommand migrates /yolo to the unified rendering
// command channel: one confirmation cell (priority-line confirm), then the
// bypass_permissions mutation with an atomic result cell.
func executeStructuredYoloCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	confirmed, message := evalBypassPermissionModeConfirmation(session, "/yolo")
	if !confirmed {
		if message == "" {
			message = "已取消"
		}
		return commandTextResult(message)
	}
	setChatPermissionMode(session, runtimepolicy.ModeBypassPermissions)
	result := "提示: 已切换到 permission-mode=bypass_permissions（等价于 --yolo）"
	if err := syncRuntimeSessionFromChat(session); err != nil {
		result += fmt.Sprintf("\n警告: 切换 permission mode 后同步会话失败: %v", err)
	}
	return commandTextResult(result)
}

// executeStructuredImageCommand migrates /image to the unified rendering
// command channel. Parsing and generation reuse the legacy pipeline; the
// projected output (text or JSON envelope) becomes the atomic command cell.
func executeStructuredImageCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	req, outputOptions, err := parseChatImageGenerationCommand(session, command)
	if err != nil {
		return commandTextResult(formatCommandError("错误: "+err.Error(), chatImageGenerationWantsJSON(session, command)))
	}
	result, details, err := runImageGenerateCommand(req)
	if err != nil {
		if isJSONOutputFormat(outputOptions.Format) {
			return commandTextResult(marshalCompactJSONLine(commandErrorPayload{
				OK:      false,
				Command: "image",
				Error:   err.Error(),
				Details: details,
			}))
		}
		return commandTextResult("错误: " + err.Error())
	}
	var b strings.Builder
	if strings.TrimSpace(result.Output) != "" {
		b.WriteString(result.Output)
		if !strings.HasSuffix(result.Output, "\n") {
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(result.OutputDir) != "" {
		b.WriteString("Output dir: " + result.OutputDir + "\n")
	}
	return commandTextResult(b.String())
}

// selectStructuredReasoningEffort runs the /reasoning_effort select interaction
// through the TTY-safe priority-line primitives used by the approval flow,
// instead of the legacy popup/terminal loop. Options render in the composer
// prompt; the confirmed value returns to the structured handler which applies
// it and commits one atomic status cell. Enter keeps the current value, 0
// clears it, and any other input cancels.
func selectStructuredReasoningEffort(session *ChatSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	catalog := reasoningEffortCatalogForModel(session.Provider, effectiveRuntimeModel(session))
	options := catalog.options
	current := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	lines := []string{"选择 reasoning_effort 值:"}
	if len(options) > 0 {
		for index, option := range options {
			marker := "  "
			if option == current {
				marker = " *"
			}
			lines = append(lines, fmt.Sprintf("  [%d]%s %s", index+1, marker, option))
		}
	} else {
		lines = append(lines, "  (未声明，可输入任意值)")
	}
	lines = append(lines, "  提示: 输入编号或值确认；0 清空；其他输入取消")

	restoreInputMode := pushChatComposerInputMode(session, chatInputModeConfirmation)
	defer restoreInputMode()
	prompt := "请输入选项 (回车保持当前): "
	readPrompt, cleanupPrompt, transientPrompt := showChatRuntimePriorityPrompt(session, lines, prompt)
	text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), readPrompt)
	cleanupPrompt()
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(normalizeQueuedInputLine(text))
	if transientPrompt {
		renderChatRuntimePriorityPromptTranscript(session, lines, prompt, text)
	}
	if text == "" {
		return current, nil
	}
	if strings.EqualFold(text, "0") || strings.EqualFold(text, "clear") || strings.EqualFold(text, "unset") {
		return "", nil
	}
	if normalized := runtimetypes.NormalizeReasoningEffort(text); normalized != "" {
		return normalized, nil
	}
	if index, err := strconv.Atoi(text); err == nil && index >= 1 && index <= len(options) {
		return options[index-1], nil
	}
	return "", errChatInteractivePromptCancelled
}
