// Package driver is the host-neutral goal continuation decision core.
//
// The CLI foreground loop and the runtime-server session_end driver used to
// duplicate the same ordering rules (host gate -> disarmed -> queued input ->
// pending interaction -> goal state -> budget -> continuation/round limits ->
// escalation -> submit). Keep those rules in one pure function so a change in
// one host cannot silently diverge from the other.
//
// The package only decides. Reading sessions, persisting goal mutations,
// submitting turns and emitting UI/audit events stay host-owned.
package driver

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// Action is the host-neutral decision outcome.
type Action string

const (
	// ActionDefer means the driver must yield to a higher-priority condition
	// (host gate, user disarm, queued input, or a parked interaction).
	ActionDefer Action = "defer"
	// ActionStop means no continuation should be submitted.
	ActionStop Action = "stop"
	// ActionSubmitContinuation starts one hidden goal continuation turn.
	ActionSubmitContinuation Action = "submit_continuation"
	// ActionBudgetWrapUp marks the goal budget_limited and submits one wrap-up
	// turn. The host owns the persistence and submission.
	ActionBudgetWrapUp Action = "budget_wrap_up"
	// ActionTerminalWrapUp submits the model-terminal closing turn exactly once.
	ActionTerminalWrapUp Action = "terminal_wrap_up"
	// ActionBlockRoundLimit marks the goal blocked at its configured round
	// ceiling.
	ActionBlockRoundLimit Action = "block_round_limit"
)

// View is the complete decision input for one driver evaluation.
type View struct {
	// HostGateReason is the first CLI/host-specific gate (session_nil,
	// no_interactive, tools_disabled, json_output, interrupted,
	// update_goal_unavailable, runtime_session_missing, ...). Empty means the
	// host is ready to drive.
	HostGateReason string

	// DisarmedReason is non-empty when the user explicitly stopped autonomous
	// continuation and has not re-armed it.
	DisarmedReason string

	// QueuedInput defers continuation while interactive input is queued or
	// draining.
	QueuedInput bool

	// PendingInteraction defers continuation while approval/question/tool work
	// owns the session.
	PendingInteraction bool

	Goal    *goal.SessionGoal
	HasGoal bool

	// BudgetExhausted is true when the active goal has reached its token
	// budget. It is evaluated after the goal-state gate.
	BudgetExhausted bool

	ContinuationLimit int
	ContinuationCount int

	IterationToolAvailable bool
	EscalationAttempted    bool
	DoomLoopAdvised        bool
	NoProgressStrikes      int
}

// Decision is one pure decision result.
type Decision struct {
	Action Action
	Reason string
	Prompt string
	Status goal.Status
}

// Decide applies the shared priority order. It must stay pure: no reads, no
// persistence, no side effects.
func Decide(view View) Decision {
	if reason := strings.TrimSpace(view.HostGateReason); reason != "" {
		return Decision{Action: ActionDefer, Reason: reason}
	}
	if reason := strings.TrimSpace(view.DisarmedReason); reason != "" {
		return Decision{Action: ActionDefer, Reason: "goal_disarmed"}
	}
	if view.QueuedInput {
		return Decision{Action: ActionDefer, Reason: "queued_input"}
	}
	if view.PendingInteraction {
		return Decision{Action: ActionDefer, Reason: "pending_interaction"}
	}
	if !view.HasGoal || view.Goal == nil {
		return Decision{Action: ActionStop, Reason: "goal_missing"}
	}

	status := view.Goal.Status
	if status != goal.StatusActive {
		reason := "goal_" + string(status)
		if view.Goal.CompletedBy == "model" {
			switch status {
			case goal.StatusComplete:
				return Decision{
					Action: ActionTerminalWrapUp,
					Reason: reason,
					Prompt: goal.CompleteWrapUpPrompt,
					Status: status,
				}
			case goal.StatusBlocked:
				return Decision{
					Action: ActionTerminalWrapUp,
					Reason: reason,
					Prompt: goal.BlockedWrapUpPrompt,
					Status: status,
				}
			}
		}
		return Decision{Action: ActionStop, Reason: reason, Status: status}
	}

	if view.BudgetExhausted {
		return Decision{
			Action: ActionBudgetWrapUp,
			Reason: "budget_exhausted",
			Prompt: goal.BudgetWrapUpPrompt,
			Status: status,
		}
	}
	if view.ContinuationLimit > 0 && view.ContinuationCount >= view.ContinuationLimit {
		return Decision{Action: ActionStop, Reason: "continuation_limit", Status: status}
	}
	if goal.RoundLimitReached(view.Goal) {
		return Decision{Action: ActionBlockRoundLimit, Reason: "round_limit", Status: status}
	}

	prompt := goal.ContinuationPrompt
	if goal.DecideIterationEscalation(
		view.NoProgressStrikes,
		view.DoomLoopAdvised,
		view.EscalationAttempted,
		view.IterationToolAvailable,
	) {
		prompt = goal.IterateEscalationPrompt
	}
	return Decision{
		Action: ActionSubmitContinuation,
		Reason: "goal_active",
		Prompt: prompt,
		Status: status,
	}
}
