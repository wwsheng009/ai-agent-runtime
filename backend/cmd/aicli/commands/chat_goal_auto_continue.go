package commands

import (
	"context"
	"fmt"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	defaultGoalAutoContinuationLimit = 5
	goalAutoContinuationPrompt       = "Continue working toward the active persistent goal. First audit whether the objective is already fully achieved; if it is, call update_goal with status=complete and a concise evidence-based summary. If required work remains, do the next necessary step now. Do not merely say that you will inspect files, run commands, or continue later: either call the necessary tool, call update_goal when complete, or state the concrete blocker."
	goalContinuationMetadataKey      = "aicli_goal_continuation"
)

type goalAutoContinuationDecision struct {
	Continue   bool
	Reason     string
	GoalStatus runtimegoal.Status
}

func maybeAutoContinueActiveGoal(ctx context.Context, session *ChatSession, executor aicliChatExecutor) error {
	if session == nil || executor == nil {
		return nil
	}
	continuer, ok := goalAutoContinuationExecutor(executor)
	if !ok {
		writeSessionDebugInfo(session, "[goal] auto continuation skipped reason=executor_without_continue_goal", false)
		return nil
	}
	for i := 0; i < defaultGoalAutoContinuationLimit; i++ {
		decision, err := shouldAutoContinueActiveGoalDecision(session)
		if err != nil {
			return err
		}
		if !decision.Continue {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation stopped reason=%s attempt=%d", decision.Reason, i+1), false)
			return nil
		}
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation attempt=%d limit=%d status=%s", i+1, defaultGoalAutoContinuationLimit, decision.GoalStatus), false)
		attemptCtx, cancel := goalAutoContinuationAttemptContext(ctx, session)
		_, continueErr := continuer.ContinueGoal(attemptCtx, session)
		cancel()
		if continueErr != nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation retry_after_error attempt=%d error=%q", i+1, continueErr.Error()), false)
			continue
		}
	}
	decision, err := shouldAutoContinueActiveGoalDecision(session)
	if err != nil {
		return err
	}
	if decision.Continue {
		reportGoalAutoContinuationLimitReached(session, defaultGoalAutoContinuationLimit)
	} else {
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation stopped reason=%s attempt=%d", decision.Reason, defaultGoalAutoContinuationLimit), false)
	}
	return nil
}

func shouldAutoContinueAfterGoalTurnError(session *ChatSession, err error) bool {
	if err == nil {
		return false
	}
	decision, decisionErr := shouldAutoContinueActiveGoalDecision(session)
	if decisionErr != nil || !decision.Continue {
		return false
	}
	return true
}

func goalAutoContinuationAttemptContext(ctx context.Context, session *ChatSession) (context.Context, context.CancelFunc) {
	base := context.Background()
	if session != nil && session.cancelCtx != nil && session.cancelCtx.Err() == nil {
		base = session.cancelCtx
	} else if ctx != nil && ctx.Err() == nil {
		base = ctx
	}
	if session != nil && session.RequestTimeout > 0 {
		return context.WithTimeout(base, session.RequestTimeout)
	}
	return base, func() {}
}

func goalAutoContinuationExecutor(executor aicliChatExecutor) (aicliGoalContinuationExecutor, bool) {
	continuer, ok := executor.(aicliGoalContinuationExecutor)
	return continuer, ok
}

func shouldAutoContinueActiveGoal(session *ChatSession) (bool, error) {
	decision, err := shouldAutoContinueActiveGoalDecision(session)
	if err != nil {
		return false, err
	}
	return decision.Continue, nil
}

func shouldAutoContinueActiveGoalDecision(session *ChatSession) (goalAutoContinuationDecision, error) {
	if session == nil {
		return goalAutoContinuationDecision{Reason: "session_nil"}, nil
	}
	if session.NoInteractive {
		return goalAutoContinuationDecision{Reason: "no_interactive"}, nil
	}
	if session.DisableTools {
		return goalAutoContinuationDecision{Reason: "tools_disabled"}, nil
	}
	if session.JSONOutput {
		return goalAutoContinuationDecision{Reason: "json_output"}, nil
	}
	if session.IsInterrupted() {
		return goalAutoContinuationDecision{Reason: "interrupted"}, nil
	}
	if session.RuntimeSession == nil {
		return goalAutoContinuationDecision{Reason: "runtime_session_missing"}, nil
	}
	if session.SessionManager == nil {
		return goalAutoContinuationDecision{Reason: "session_manager_missing"}, nil
	}
	if !canCurrentChatPathUpdateGoal(session) {
		return goalAutoContinuationDecision{Reason: "update_goal_unavailable"}, nil
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		return goalAutoContinuationDecision{Reason: "queued_input"}, nil
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil {
		return goalAutoContinuationDecision{}, err
	}
	if !ok || goal == nil {
		return goalAutoContinuationDecision{Reason: "goal_missing"}, nil
	}
	if goal.Status != runtimegoal.StatusActive {
		return goalAutoContinuationDecision{Reason: "goal_" + string(goal.Status), GoalStatus: goal.Status}, nil
	}
	return goalAutoContinuationDecision{Continue: true, Reason: "goal_active", GoalStatus: goal.Status}, nil
}

func reportGoalAutoContinuationWarning(session *ChatSession, err error) {
	if err == nil {
		return
	}
	message := fmt.Sprintf("[goal] 自动审计未完成: %v", err)
	writeSessionDebugInfo(session, message, false)
	if session == nil || !shouldRenderInteractiveOutput(session) {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderAsyncLine(message)
		return
	}
	fmt.Println(message)
}

func reportGoalAutoContinuationLimitReached(session *ChatSession, limit int) {
	if limit <= 0 {
		return
	}
	message := fmt.Sprintf("[goal] 自动续跑已达到上限(%d)，当前 goal 仍为 active", limit)
	writeSessionDebugInfo(session, message, false)
	if session == nil || !shouldRenderInteractiveOutput(session) {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderAsyncLine(message)
		return
	}
	fmt.Println(message)
}

func goalContinuationInstructionMessage(prompt string) runtimetypes.Message {
	message := *runtimetypes.NewUserMessage(prompt)
	message.Metadata.Set(goalContinuationMetadataKey, true)
	return message
}

func stripGoalContinuationInstructionMessages(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}
	stripped := make([]runtimetypes.Message, 0, len(messages))
	for _, message := range messages {
		if message.Metadata != nil {
			if value, ok := message.Metadata.Get(goalContinuationMetadataKey); ok {
				if hidden, _ := value.(bool); hidden {
					continue
				}
			}
		}
		stripped = append(stripped, message)
	}
	return stripped
}
