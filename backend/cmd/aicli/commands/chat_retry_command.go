package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	llm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

type chatTurnRecovery struct {
	Prompt      string
	SessionID   string
	Interrupted bool
}

func rememberChatTurnRecovery(session *ChatSession, prompt string, interrupted bool) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	session.turnRecoveryMu.Lock()
	session.turnRecovery = &chatTurnRecovery{
		Prompt:      prompt,
		SessionID:   currentRuntimeSessionID(session),
		Interrupted: interrupted,
	}
	session.turnRecoveryMu.Unlock()
}

func clearChatTurnRecovery(session *ChatSession) {
	if session == nil {
		return
	}
	session.turnRecoveryMu.Lock()
	session.turnRecovery = nil
	session.turnRecoveryMu.Unlock()
}

func chatTurnRecoverySnapshot(session *ChatSession) *chatTurnRecovery {
	if session == nil {
		return nil
	}
	session.turnRecoveryMu.Lock()
	defer session.turnRecoveryMu.Unlock()
	if session.turnRecovery == nil {
		return nil
	}
	copy := *session.turnRecovery
	return &copy
}

func renderChatTurnRecoveryHint(session *ChatSession) {
	renderChatTurnRecoveryHintForError(session, nil)
}

func renderChatTurnRecoveryHintForError(session *ChatSession, turnErr error) {
	recovery := chatTurnRecoverySnapshot(session)
	if recovery == nil {
		return
	}
	message := "恢复建议: 输入 /retry 可将上一条失败消息恢复到输入区；为避免重复工具副作用，该命令不会自动执行。"
	if recovery.Interrupted {
		message = "恢复建议: 本轮可能已部分执行工具。输入 /retry 可恢复原消息，检查后再发送；该命令不会自动执行。"
	}
	// turn 级自动重跑只在「本轮从未真正执行过工具」时发生（chat_turn_tool_executions.go），
	// 因此恢复提示必须先看工具执行计数：已执行过工具就必须提醒用户检查副作用，
	// 不能再用"工具未执行、无副作用"的措辞。
	var malformed *llmadapter.MalformedToolCallError
	switch {
	case chatTurnToolExecutions(session) > 0:
		message = "恢复建议: 本轮已执行过工具，为避免重复副作用系统未做自动重跑。请检查执行结果后重新发送，或输入 /retry 将上一条消息恢复到输入区（该命令不会自动执行）。"
	case errors.As(turnErr, &malformed):
		// 工具参数非法（invalid_tool_arguments）是可重试的退化采样：本轮没有任何
		// 可执行的工具调用，工具从未执行（无副作用）。通用提示里的"避免重复工具
		// 副作用"对这类错误是误导，改为说明系统已自动重采样、按 schema 重发并做过
		// 有界的 turn 级自动重跑（见 maybeAutoRetryDegenerateTurn）。
		message = "恢复建议: 本轮模型返回的工具参数非法，工具未执行（无副作用）。系统已自动重采样、按 schema 重发并自动重跑了少量次数仍失败；可直接重新发送，或输入 /retry 将上一条消息恢复到输入区（该命令不会自动执行）。"
	case llm.IsEmptyReplyError(turnErr):
		message = "恢复建议: 本轮模型回复为空（未渲染内容、未执行工具，无副作用）。系统已自动重跑少量次数仍失败；可直接重新发送，或输入 /retry 将上一条消息恢复到输入区（该命令不会自动执行）。"
	}
	var leaseConflict *runtimechat.LeaseConflictError
	if errors.As(turnErr, &leaseConflict) {
		message = "恢复建议: 当前会话仍被其他执行器占用；请切回对应终端完成或退出该会话，或等待租约释放后再输入 /retry。/retry 只恢复草稿，不会强制抢占仍存活的会话。"
	}
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement(message)
		return
	}
	// Fall back without Interaction: still route through surface WriteOutput
	// so ClearPrompt shrink debt is not left for the next content write.
	printDirectInteractiveOutput(session, ui.NewStatus(ui.StatusInfo, message).Build()+"\n")
}

func handleRetryCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		result := executeStructuredRetryCommand(session, command)
		if err := renderChatCommandResult(session, result, false); err == nil && result.RestoreComposerDraft != "" {
			if restoreErr := restoreChatRetryDraft(session, result.RestoreComposerDraft); restoreErr != nil {
				_ = renderChatCommandResult(session, commandErrorResult(restoreErr), false)
			}
		}
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	if strings.TrimSpace(extractCommandArgument(command)) != "" {
		fmt.Println("错误: /retry 不接受参数")
		fmt.Println("用法: /retry")
		return false
	}
	if session.NoInteractive || session.JSONOutput {
		fmt.Println("错误: /retry 仅用于交互式 Composer；它只恢复草稿，不会自动发送")
		return false
	}
	recovery := chatTurnRecoverySnapshot(session)
	if recovery == nil {
		fmt.Println("当前没有可恢复的失败或中断消息")
		return false
	}
	if recovery.SessionID != currentRuntimeSessionID(session) {
		clearChatTurnRecovery(session)
		fmt.Println("当前会话已切换，不能恢复其他会话中的失败消息")
		return false
	}
	if err := restoreChatRetryDraft(session, recovery.Prompt); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if recovery.Interrupted {
		fmt.Println("已恢复上一条中断消息。工具可能已部分执行，请检查草稿后再按 Enter 发送；当前未执行任何操作。")
	} else {
		fmt.Println("已恢复上一条失败消息到输入区，请检查后按 Enter 发送；当前未执行任何操作。")
	}
	return false
}

// executeStructuredRetryCommand returns the recovery report plus a typed
// post-commit Composer draft effect. It never sends the prompt and it never
// owns a terminal writer; recovery remains deliberately conservative around
// interrupted turns that may already have invoked tools.
func executeStructuredRetryCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	if strings.TrimSpace(extractCommandArgument(command)) != "" {
		return commandTextResult("错误: /retry 不接受参数\n用法: /retry")
	}
	if session.NoInteractive || session.JSONOutput {
		return commandTextResult("错误: /retry 仅用于交互式 Composer；它只恢复草稿，不会自动发送")
	}
	recovery := chatTurnRecoverySnapshot(session)
	if recovery == nil {
		return commandTextResult("当前没有可恢复的失败或中断消息")
	}
	if recovery.SessionID != currentRuntimeSessionID(session) {
		clearChatTurnRecovery(session)
		return commandTextResult("当前会话已切换，不能恢复其他会话中的失败消息")
	}
	prompt := strings.TrimSpace(recovery.Prompt)
	if prompt == "" {
		return commandErrorResult(fmt.Errorf("失败消息内容为空，无法恢复"))
	}
	if session.Interaction == nil {
		return commandErrorResult(fmt.Errorf("当前终端不支持安全恢复可编辑草稿"))
	}
	if existing := strings.TrimSpace(session.Interaction.PromptInputSnapshot().Text); existing != "" {
		return commandErrorResult(fmt.Errorf("输入区已有草稿，未覆盖现有内容"))
	}
	message := "已恢复上一条失败消息到输入区，请检查后按 Enter 发送；当前未执行任何操作。"
	if recovery.Interrupted {
		message = "已恢复上一条中断消息。工具可能已部分执行，请检查草稿后再按 Enter 发送；当前未执行任何操作。"
	}
	result := commandTextResult(message)
	result.RestoreComposerDraft = prompt
	return result
}

func restoreChatRetryDraft(session *ChatSession, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("失败消息内容为空，无法恢复")
	}
	if session.Interaction != nil {
		if existing := strings.TrimSpace(session.Interaction.PromptInputSnapshot().Text); existing != "" {
			return fmt.Errorf("输入区已有草稿，未覆盖现有内容")
		}
		session.Interaction.SetPromptInput(prompt)
		return nil
	}
	if session.InputQueue != nil {
		if session.InputQueue.hasDraft() || session.InputQueue.hasReadySubmission() || session.InputQueue.pendingCount() > 0 {
			return fmt.Errorf("输入队列中已有待处理内容，未覆盖现有草稿或队列")
		}
		session.InputQueue.stageDraft(prompt)
		return nil
	}
	return fmt.Errorf("当前终端不支持安全恢复可编辑草稿")
}
