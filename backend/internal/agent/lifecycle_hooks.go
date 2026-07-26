package agent

import (
	"context"
	"fmt"
	"strings"

	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
)

// dispatchStopHook asks stop hooks whether the agent may finish this turn.
// DecisionBlock returns continueRun=true when step budget remains so the loop
// injects a recovery reminder and keeps going (Grok-style stop gate).
func (loop *ReActLoop) dispatchStopHook(ctx context.Context, sessionID, traceID string, step int, result *Result, reason string) (continueRun bool, message string) {
	if loop == nil || loop.agent == nil {
		return false, ""
	}
	hookMgr := loop.agent.GetHookManager()
	if hookMgr == nil {
		return false, ""
	}
	payload := map[string]interface{}{
		"session_id": sessionID,
		"trace_id":   traceID,
		"step":       step,
		"reason":     reason,
		"success":    result != nil && result.Success,
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
		if result.LimitReached {
			payload["limit_reached"] = true
			payload["limit_reason"] = result.LimitReason
		}
	}
	decision, err := hookMgr.Dispatch(ctx, runtimehooks.EventStop, payload)
	if err != nil {
		// Dispatch already applies fail_open/fail_closed per hook; treat residual
		// errors as non-blocking to avoid hard-crashing a finished turn.
		return false, ""
	}
	if !runtimehooks.IsBlockingAction(decision.Action) {
		return false, strings.TrimSpace(decision.Message)
	}
	if !hasRemainingStepBudget(loop.config.MaxSteps, step) {
		// Cannot continue; surface the block message as soft context only.
		return false, strings.TrimSpace(decision.Message)
	}
	msg := strings.TrimSpace(decision.Message)
	if msg == "" {
		msg = "stop hook blocked agent finish; continue working and address the stop condition"
	}
	return true, msg
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
	decision, err := hookMgr.Dispatch(ctx, runtimehooks.EventPreCompact, cloneHookPayload(payload))
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
	hookMgr.DispatchAsync(ctx, runtimehooks.EventPostCompact, cloneHookPayload(payload))
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
	if msg == "" {
		return "A stop hook blocked finishing this turn. Continue working and resolve the stop condition before concluding."
	}
	return fmt.Sprintf("A stop hook blocked finishing this turn: %s\nContinue working and address the stop condition before concluding.", msg)
}
