package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
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
	var leaseConflict *runtimechat.LeaseConflictError
	if errors.As(turnErr, &leaseConflict) {
		message = "恢复建议: 当前会话仍被其他执行器占用；请切回对应终端完成或退出该会话，或等待租约释放后再输入 /retry。/retry 只恢复草稿，不会强制抢占仍存活的会话。"
	}
	if session.Interaction != nil {
		session.Interaction.RenderAsyncLine(message)
		return
	}
	// Fall back without Interaction: still route through surface WriteOutput
	// so ClearPrompt shrink debt is not left for the next content write.
	printDirectInteractiveOutput(session, ui.NewStatus(ui.StatusInfo, message).Build()+"\n")
}

func handleRetryCommand(session *ChatSession, command string) bool {
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
