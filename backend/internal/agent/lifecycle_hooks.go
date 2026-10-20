package agent

import (
	"context"
	"fmt"
	"strings"

	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// dispatchStopHook asks stop hooks whether the agent may finish this turn.
// DecisionBlock returns continueRun=true when step budget remains so the loop
// injects a recovery reminder and keeps going (Grok-style stop gate).
func (loop *ReActLoop) dispatchStopHook(ctx context.Context, sessionID, traceID string, step int, result *Result, reason string) (continueRun bool, message string, stop bool) {
	if loop == nil || loop.agent == nil {
		return false, "", false
	}
	hookMgr := loop.agent.GetHookManager()
	if hookMgr == nil {
		return false, "", false
	}
	payload := map[string]interface{}{
		"session_id":          sessionID,
		"turn_id":             TurnIDFromContext(ctx),
		"trace_id":            traceID,
		"step":                step,
		"reason":              reason,
		"success":             result != nil && result.Success,
		"hook_event_name":     "Stop",
		"stop_hook_active":    loop.stopHookContinued,
		"stop_hook_continues": loop.stopHookContinues,
	}
	if result != nil {
		if strings.TrimSpace(result.Output) != "" {
			payload["output"] = result.Output
			payload["last_assistant_message"] = result.Output
		}
		if strings.TrimSpace(result.Error) != "" {
			payload["error"] = result.Error
		}
		payload["observation_count"] = len(result.Observations)
		payload["tool_error_count"] = result.ToolErrorCount
		payload["surface_mismatch_count"] = result.SurfaceMismatchCount
		if result.LimitReached {
			payload["limit_reached"] = true
			payload["limit_reason"] = result.LimitReason
		}
	}
	decision, err := hookMgr.Dispatch(ctx, runtimehooks.EventStop, payload)
	if err != nil {
		// Dispatch already applies fail_open/fail_closed per hook; treat residual
		// errors as non-blocking to avoid hard-crashing a finished turn.
		return false, "", false
	}
	if decision.Action == runtimehooks.DecisionStop {
		return false, strings.TrimSpace(decision.Message), true
	}
	if !runtimehooks.IsBlockingAction(decision.Action) {
		return false, strings.TrimSpace(decision.Message), false
	}
	// Bound how many times one Stop hook may force another step. A hook that
	// always returns block (a bug, or a condition that never becomes true) would
	// otherwise loop until the step budget is exhausted; with an unlimited
	// maxSteps that is effectively forever. Codex leaves this to the hook via
	// stop_hook_active, which is easy to get wrong.
	if loop.stopHookContinues >= maxStopHookContinues {
		msg := strings.TrimSpace(decision.Message)
		if msg == "" {
			msg = "stop hook keeps blocking; giving up so the turn can finish"
		}
		return false, msg + " (stop hook continuation limit reached)", false
	}
	if !hasRemainingStepBudget(loop.config.MaxSteps, step) {
		// Cannot continue; surface the block message as soft context only.
		return false, strings.TrimSpace(decision.Message), false
	}
	loop.stopHookContinued = true
	loop.stopHookContinues++
	msg := strings.TrimSpace(decision.Message)
	if msg == "" {
		msg = "stop hook blocked agent finish; continue working and address the stop condition"
	}
	return true, msg, false
}

// dispatchStopFailureHook notifies hooks that a run ended unsuccessfully.
// This is always async / non-blocking.
func (loop *ReActLoop) dispatchStopFailureHook(ctx context.Context, sessionID, traceID string, step int, result *Result, reason string) {
	if loop == nil || loop.agent == nil {
		return
	}
	hookMgr := loop.agent.GetHookManager()
	if hookMgr == nil {
		return
	}
	payload := map[string]interface{}{
		"session_id": sessionID,
		"trace_id":   traceID,
		"step":       step,
		"reason":     reason,
		"success":    false,
	}
	if result != nil {
		if strings.TrimSpace(result.Output) != "" {
			payload["output"] = result.Output
		}
		if strings.TrimSpace(result.Error) != "" {
			payload["error"] = result.Error
		}
		payload["observation_count"] = len(result.Observations)
		payload["tool_error_count"] = result.ToolErrorCount
		payload["surface_mismatch_count"] = result.SurfaceMismatchCount
		if result.LimitReached {
			payload["limit_reached"] = true
			payload["limit_reason"] = result.LimitReason
		}
		if result.CompletionSatisfied != nil {
			payload["completion_satisfied"] = *result.CompletionSatisfied
		}
	}
	hookMgr.DispatchAsync(ctx, runtimehooks.EventStopFailure, payload)
}

// dispatchPreCompactHook returns false when compaction should be skipped.
func (a *Agent) dispatchPreCompactHook(ctx context.Context, payload map[string]interface{}) (allow bool, message string) {
	if a == nil {
		return true, ""
	}
	hookMgr := a.GetHookManager()
	if hookMgr == nil {
		return true, ""
	}
	decision, err := hookMgr.Dispatch(ctx, runtimehooks.EventPreCompact, payload)
	if err != nil {
		return true, ""
	}
	if runtimehooks.IsBlockingAction(decision.Action) {
		msg := strings.TrimSpace(decision.Message)
		if msg == "" {
			msg = "pre_compact hook blocked compaction"
		}
		return false, msg
	}
	return true, strings.TrimSpace(decision.Message)
}

// dispatchPostCompactHook notifies hooks after a successful compaction.
func (a *Agent) dispatchPostCompactHook(ctx context.Context, payload map[string]interface{}) {
	if a == nil {
		return
	}
	hookMgr := a.GetHookManager()
	if hookMgr == nil {
		return
	}
	hookMgr.DispatchAsync(ctx, runtimehooks.EventPostCompact, payload)
}

// DispatchUserPromptSubmit runs synchronous user-prompt hooks before the turn
// starts. A blocking decision aborts the turn; additional context is carried
// on the returned context and injected into the model request only.
func (a *Agent) DispatchUserPromptSubmit(ctx context.Context, payload map[string]interface{}) (bool, string, context.Context) {
	if a == nil {
		return true, "", ctx
	}
	hookMgr := a.GetHookManager()
	if hookMgr == nil {
		return true, "", ctx
	}
	decision, err := hookMgr.DispatchSync(ctx, runtimehooks.EventUserPromptSubmit, cloneHookPayload(payload))
	if err != nil {
		return true, "", ctx
	}
	if runtimehooks.IsBlockingAction(decision.Action) || decision.Action == runtimehooks.DecisionStop {
		message := strings.TrimSpace(decision.Message)
		if message == "" {
			message = "user_prompt_submit hook blocked the turn"
		}
		return false, message, ctx
	}
	if contextText := decision.ExtraContext["additional_context"]; strings.TrimSpace(contextText) != "" {
		ctx = WithHookAdditionalContext(ctx, contextText)
	}
	return true, strings.TrimSpace(decision.Message), ctx
}

// DispatchSessionStart runs synchronous session-start hooks. Codex treats
// continue:false as "do not start this session"; the runtime maps that to a
// blocked turn. Plain stdout becomes additional model context.
func (a *Agent) DispatchSessionStart(ctx context.Context, payload map[string]interface{}) (bool, string, context.Context) {
	if a == nil {
		return true, "", ctx
	}
	hookMgr := a.GetHookManager()
	if hookMgr == nil {
		return true, "", ctx
	}
	decision, err := hookMgr.DispatchSync(ctx, runtimehooks.EventSessionStart, cloneHookPayload(payload))
	if err != nil {
		return true, "", ctx
	}
	if runtimehooks.IsBlockingAction(decision.Action) || decision.Action == runtimehooks.DecisionStop {
		message := strings.TrimSpace(decision.Message)
		if message == "" {
			message = "session_start hook stopped the session"
		}
		return false, message, ctx
	}
	if contextText := decision.ExtraContext["additional_context"]; strings.TrimSpace(contextText) != "" {
		ctx = WithHookAdditionalContext(ctx, contextText)
	}
	return true, strings.TrimSpace(decision.Message), ctx
}

func cloneHookPayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	return out
}

func stopHookRecoveryMessage(hookMessage string) string {
	msg := strings.TrimSpace(hookMessage)
	var body string
	if msg == "" {
		body = "A stop hook blocked finishing this turn. Continue working and resolve the stop condition before concluding."
	} else {
		body = fmt.Sprintf("A stop hook blocked finishing this turn: %s\nContinue working and address the stop condition before concluding.", msg)
	}
	// R3: unified <system-reminder> envelope.
	return FormatSystemReminder(ReminderKindStopHook, body)
}

// newStopHookReminderMessage builds a durable stop-gate recovery reminder.
func newStopHookReminderMessage(hookMessage string) *types.Message {
	return NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindStopHook,
		Body:    stripSystemReminderEnvelope(stopHookRecoveryMessage(hookMessage)),
		Durable: true,
		Extra: types.Metadata{
			"stop_hook_block": true,
		},
	})
}
