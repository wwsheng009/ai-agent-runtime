package commands

import (
	"context"
	"fmt"
	"strings"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
)

// sendMessage 发送消息
func sendMessage(session *ChatSession, userMessage string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if session.IsInterrupted() {
		return "", fmt.Errorf("用户中断")
	}
	// Codex-aligned: only notify when the agent is truly waiting for the user.
	// Queued follow-up input starts the next turn immediately, so a completion
	// bell at that boundary would feel like false attention.
	defer func() {
		if shouldNotifyChatTurnComplete(session) {
			notifyChatSound(session, chatSoundTurnComplete)
		}
	}()
	if session.Interaction != nil {
		session.Interaction.StartWaiting()
	}
	turnSucceeded := false
	defer func() {
		if session.Interaction == nil {
			return
		}
		if turnSucceeded {
			// The API/actor turn is complete. Freeze the live elapsed clock into
			// the persistent "Worked for <duration>" summary.
			session.Interaction.CompleteWaiting()
		} else {
			// Failed and interrupted turns must not publish a success summary.
			session.Interaction.ClearWaiting()
		}
	}()
	stopBusyInputCapture := startBusyQueuedInputCapture(session)
	defer stopBusyInputCapture()
	ensureChatSystemPromptMessage(session)
	beginChatUserTurn(session, userMessage)
	executor, err := ensureChatExecutor(session)
	if err != nil {
		logChatTurnFailureIfUnrecorded(session, userMessage, err)
		flushChatSessionLog(session)
		return "", err
	}
	resetChatTurnTokenUsage(session)

	if !session.NoInteractive && shouldShowInitialThinkingIndicator(session, executor) {
		if session.Interaction != nil {
			session.Interaction.StartThinking()
		} else if !unifiedInteractiveOutputMustFailClosed(session) {
			fmt.Print("助手正在思考...")
		}
	}

	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if session.RequestTimeout > 0 {
		ctx, cancel = runtimeexecution.WithTimeoutSource(ctx, session.RequestTimeout, runtimeexecution.TimeoutSourceChatTurnDeadline)
		defer cancel()
	}

	stopEscWatcher := startChatEscapeInterruptWatcher(session)
	defer stopEscWatcher()
	response, err := executor.Execute(ctx, session, userMessage)
	if session.Interaction != nil {
		session.Interaction.ClearThinking()
	} else if err != nil && !session.NoInteractive && !unifiedInteractiveOutputMustFailClosed(session) {
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
			turnSucceeded = true
			return response, nil
		}
		logChatTurnFailureIfUnrecorded(session, userMessage, err)
		flushChatSessionLog(session)
		return response, err
	}
	if continueErr := maybeAutoContinueActiveGoal(ctx, session, executor); continueErr != nil {
		reportGoalAutoContinuationWarning(session, continueErr)
	}
	turnSucceeded = true
	return response, nil
}

func finishSuccessfulChatSend(session *ChatSession, response string, noInteractive bool) {
	if session == nil {
		return
	}
	clearChatTurnRecovery(session)
	alreadyRendered := wasInteractiveActorResponseAlreadyRendered(session, response)
	handledByStreamFinalize := false
	if !alreadyRendered {
		handledByStreamFinalize = finalizeInteractiveActorStreamIfNeeded(session, response)
	}
	if shouldDisplayFinalResponse(session, response) && !handledByStreamFinalize && !alreadyRendered {
		renderChatResponse(session, response)
	} else if session.Stream && !noInteractive && !handledByStreamFinalize && !alreadyRendered {
		// Route the trailing blank through the surface so ClearPrompt shrink
		// debt is flushed here instead of attaching to the next content write.
		printDirectInteractiveOutput(session, "\n")
	}

	session.ImagePaths = nil
	refreshChatComposerContext(session)
	flushChatSessionLog(session)
}

func flushChatSessionLog(session *ChatSession) {
	if session == nil {
		return
	}
	syncChatLoggerSessionMetadata(session)
	if session.Logger != nil && session.Logger.logDir != "" {
		if err := session.Logger.FlushSession(); err != nil {
			writeChatLogSaveError(session, err)
		}
		return
	}
	writeChatLogBufferedMarker(session)
}

func syncChatLoggerSessionMetadata(session *ChatSession) {
	if session == nil || session.Logger == nil || session.RuntimeSession == nil {
		return
	}
	title := strings.TrimSpace(session.RuntimeSession.Metadata.Title)
	if preview := session.RuntimeSession.BuildPreview(); preview != nil && strings.TrimSpace(preview.Title) != "" {
		title = preview.Title
	}
	session.Logger.SetRuntimeSessionMetadata(session.RuntimeSession.ID, title)
}

func logChatTurnFailureIfUnrecorded(session *ChatSession, userMessage string, turnErr error) {
	logChatFailureIfUnrecorded(session, userMessage, turnErr, "chat_turn")
}

func logActorExecutorFailureIfUnrecorded(session *ChatSession, prompt string, turnErr error) {
	logChatFailureIfUnrecorded(session, prompt, turnErr, "actor_executor")
}

func logChatFailureIfUnrecorded(session *ChatSession, userMessage string, turnErr error, path string) {
	if session == nil || session.Logger == nil || session.Logger.sessionLog == nil || turnErr == nil {
		return
	}
	if len(session.Logger.sessionLog.Messages) > 0 {
		return
	}
	if strings.TrimSpace(path) == "" {
		path = "chat_turn"
	}
	scope := aicliLogScope{TurnID: "turn-error", RequestID: "turn-error-req-01"}
	session.Logger.LogRequest(scope, map[string]interface{}{
		"prompt":   userMessage,
		"provider": session.ProviderName,
		"protocol": session.Provider.GetProtocol(),
		"model":    session.Model,
		"base_url": session.BaseURL,
		"path":     path,
	})
	session.Logger.LogResponse(scope, map[string]interface{}{
		"error": turnErr.Error(),
		"path":  path,
	}, nil, session.Stream, turnErr, 0)
}

func shouldShowInitialThinkingIndicator(session *ChatSession, executor aicliChatExecutor) bool {
	if session == nil || session.NoInteractive {
		return false
	}
	if session.LocalRuntimeHost != nil || session.ActorFirstReady {
		return false
	}
	if descriptor, ok := chatRuntimeExecutorDescriptor(executor); ok && descriptor.RuntimeEvents {
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
	if session.Logger != nil && session.Logger.sessionLog != nil && strings.TrimSpace(session.Logger.sessionLog.InitialMessage) == "" {
		session.Logger.SetInitialMessage(userMessage)
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
