package driver

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// EventTypeDecision is the session-scoped audit event emitted after every
// driver evaluation. The payload is deliberately data-minimal: it carries
// handles, state and the decision, never the goal objective or prompt text.
const EventTypeDecision = "goal.driver.decision"

// BuildAuditPayload renders the stable audit row for one Decide call.
func BuildAuditPayload(trigger string, view View, decision Decision) map[string]interface{} {
	payload := map[string]interface{}{
		"schema":                   "goal.driver.decision.v1",
		"trigger":                  strings.TrimSpace(trigger),
		"action":                   string(decision.Action),
		"reason":                   decision.Reason,
		"host_gate_reason":         strings.TrimSpace(view.HostGateReason),
		"disarmed":                 strings.TrimSpace(view.DisarmedReason) != "",
		"disarmed_reason":          strings.TrimSpace(view.DisarmedReason),
		"queued_input":             view.QueuedInput,
		"pending_interaction":      view.PendingInteraction,
		"budget_exhausted":         view.BudgetExhausted,
		"continuation_limit":       view.ContinuationLimit,
		"continuation_count":       view.ContinuationCount,
		"no_progress_strikes":      view.NoProgressStrikes,
		"iteration_tool_available": view.IterationToolAvailable,
		"escalation_attempted":     view.EscalationAttempted,
		"doom_loop_advised":        view.DoomLoopAdvised,
	}
	if view.HasGoal && view.Goal != nil {
		payload["goal_id"] = view.Goal.GoalID
		payload["goal_status"] = string(view.Goal.Status)
		payload["goal_rounds_started"] = view.Goal.RoundsStarted
		payload["goal_max_rounds"] = view.Goal.MaxRounds
		payload["goal_tokens_used"] = view.Goal.TokensUsed
		payload["goal_token_budget"] = view.Goal.TokenBudget
	}
	payload["prompt_kind"] = promptKind(decision)
	return payload
}

func promptKind(decision Decision) string {
	switch {
	case decision.Prompt == goal.IterateEscalationPrompt:
		return "iteration_escalation"
	case decision.Prompt == goal.BudgetWrapUpPrompt:
		return "budget_wrap_up"
	case decision.Prompt == goal.CompleteWrapUpPrompt:
		return "complete_wrap_up"
	case decision.Prompt == goal.BlockedWrapUpPrompt:
		return "blocked_wrap_up"
	case decision.Action == ActionSubmitContinuation:
		return "continuation"
	default:
		return ""
	}
}
