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
	beginChatUserTurn(session, userMessage)
	executor := ensureChatExecutor(session)
	resetChatTurnTokenUsage(session)

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

	stopEscWatcher := startChatEscapeInterruptWatcher(session)
	defer stopEscWatcher()
	response, err := executor.Execute(ctx, session, userMessage)
	if session.Interaction != nil {
		session.Interaction.ClearThinking()
	} else if err != nil && !session.NoInteractive {
		fmt.Print("\r   \r")
	}
	if err != nil {
		if shouldAutoContinueAfterGoalTurnError(session, err) {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] initial turn ended with error; starting auto continuation error=%q", err.Error()), false)
			continueCtx, continueCancel := goalAutoContinuationAttemptContext(ctx, session)
			continueErr := maybeAutoContinueActiveGoal(continueCtx, session, executor)
			continueCancel()
			if continueErr != nil {
				reportGoalAutoContinuationWarning(session, continueErr)
				return response, err
			}
			return response, nil
		}
		return response, err
	}
	if continueErr := maybeAutoContinueActiveGoal(ctx, session, executor); continueErr != nil {
		reportGoalAutoContinuationWarning(session, continueErr)
	}
	return response, nil
}

func finishSuccessfulChatSend(session *ChatSession, response string, noInteractive bool) {
	if session == nil {
		return
	}
	handledByStreamFinalize := finalizeInteractiveActorStreamIfNeeded(session, response)
	if shouldDisplayFinalResponse(session, response) && !handledByStreamFinalize && !wasInteractiveActorResponseAlreadyRendered(session) {
		renderChatResponse(session, response)
	} else if session.Stream && !noInteractive && !handledByStreamFinalize {
		beginDirectInteractiveOutput(session)
		fmt.Println()
	}

	session.ImagePaths = nil
	if session.Logger != nil && session.Logger.logDir != "" {
		if err := session.Logger.FlushSession(); err != nil {
			writeChatLogSaveError(session, err)
		}
	} else {
		writeChatLogBufferedMarker(session)
	}
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
		if session.turnPrimed && session.TurnRequestCount == 0 {
			session.turnPrimed = false
		} else {
			beginChatUserTurn(session, userMessage)
			session.turnPrimed = false
		}
	}

	turnIndex := session.MsgCount
	if turnIndex <= 0 {
		turnIndex = 1
	}
	session.TurnRequestCount++
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}

	turnID := fmt.Sprintf("turn-%04d", turnIndex)
	return aicliLogScope{
		TurnID:    turnID,
		RequestID: fmt.Sprintf("%s-req-%02d", turnID, session.TurnRequestCount),
	}
}

func beginChatUserTurn(session *ChatSession, userMessage string) {
	if session == nil || strings.TrimSpace(userMessage) == "" {
		return
	}
	session.MsgCount++
	session.TurnRequestCount = 0
	session.turnPrimed = true
	session.StatusMessageCount = countChatStatusMessages(session.Messages) + 1
	resetChatTurnTokenUsage(session)
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}
