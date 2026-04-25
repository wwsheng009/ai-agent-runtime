package commands

import (
	"context"
	"fmt"
	"strings"
)

// sendMessage 发送消息
func sendMessage(session *ChatSession, userMessage string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if session.IsInterrupted() {
		return "", fmt.Errorf("用户中断")
	}
	ensureChatSystemPromptMessage(session)
	executor := ensureChatExecutor(session)

	if !session.NoInteractive && shouldShowInitialThinkingIndicator(session, executor) {
		if session.Interaction != nil {
			session.Interaction.StartThinking()
		} else {
			fmt.Print("助手正在思考...")
		}
	}

	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if session.RequestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, session.RequestTimeout)
		defer cancel()
	}

	response, err := executor.Execute(ctx, session, userMessage)
	if session.Interaction != nil {
		session.Interaction.ClearThinking()
	} else if err != nil && !session.NoInteractive {
		fmt.Print("\r   \r")
	}
	return response, err
}

func shouldShowInitialThinkingIndicator(session *ChatSession, executor aicliChatExecutor) bool {
	if session == nil || session.NoInteractive {
		return false
	}
	if session.LocalRuntimeHost != nil || session.ActorFirstReady {
		return false
	}
	if _, ok := executor.(*aicliActorChatExecutor); ok {
		return false
	}
	return true
}

func nextLogScope(session *ChatSession, userMessage string) aicliLogScope {
	if session == nil {
		return aicliLogScope{}
	}
	if strings.TrimSpace(userMessage) != "" {
		session.MsgCount++
		session.TurnRequestCount = 0
	}

	turnIndex := session.MsgCount
	if turnIndex <= 0 {
		turnIndex = 1
	}
	session.TurnRequestCount++

	turnID := fmt.Sprintf("turn-%04d", turnIndex)
	return aicliLogScope{
		TurnID:    turnID,
		RequestID: fmt.Sprintf("%s-req-%02d", turnID, session.TurnRequestCount),
	}
}
