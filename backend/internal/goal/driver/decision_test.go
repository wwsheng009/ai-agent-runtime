package driver

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func activeGoal() *goal.SessionGoal {
	return &goal.SessionGoal{
		GoalID:    "goal-1",
		Status:    goal.StatusActive,
		MaxRounds: 0,
	}
}

func TestDecidePriorityAndLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		view   View
		action Action
		reason string
		prompt string
	}{
		{
			name:   "host gate wins",
			view:   View{HostGateReason: "tools_disabled", DisarmedReason: "user_interrupt"},
			action: ActionDefer,
			reason: "tools_disabled",
		},
		{
			name:   "disarmed before queued",
			view:   View{DisarmedReason: "user_interrupt", QueuedInput: true},
			action: ActionDefer,
			reason: "goal_disarmed",
		},
		{
			name:   "queued before pending",
			view:   View{QueuedInput: true, PendingInteraction: true},
			action: ActionDefer,
			reason: "queued_input",
		},
		{
			name:   "pending before goal state",
			view:   View{PendingInteraction: true},
			action: ActionDefer,
			reason: "pending_interaction",
		},
		{
			name:   "missing goal",
			view:   View{},
			action: ActionStop,
			reason: "goal_missing",
		},
		{
			name: "paused goal",
			view: View{
				HasGoal: true,
				Goal:    &goal.SessionGoal{Status: goal.StatusPaused},
			},
			action: ActionStop,
			reason: "goal_paused",
		},
		{
			name: "model completed goal wraps up",
			view: View{
				HasGoal: true,
				Goal:    &goal.SessionGoal{Status: goal.StatusComplete, CompletedBy: "model"},
			},
			action: ActionTerminalWrapUp,
			reason: "goal_complete",
			prompt: goal.CompleteWrapUpPrompt,
		},
		{
			name: "user completed goal stops",
			view: View{
				HasGoal: true,
				Goal:    &goal.SessionGoal{Status: goal.StatusComplete, CompletedBy: "user"},
			},
			action: ActionStop,
			reason: "goal_complete",
		},
		{
			name: "model blocked goal wraps up",
			view: View{
				HasGoal: true,
				Goal:    &goal.SessionGoal{Status: goal.StatusBlocked, CompletedBy: "model"},
			},
			action: ActionTerminalWrapUp,
			reason: "goal_blocked",
			prompt: goal.BlockedWrapUpPrompt,
		},
		{
			name: "budget before continuation limit",
			view: View{
				HasGoal: true, Goal: activeGoal(),
				BudgetExhausted: true, ContinuationLimit: 1, ContinuationCount: 1,
			},
			action: ActionBudgetWrapUp,
			reason: "budget_exhausted",
			prompt: goal.BudgetWrapUpPrompt,
		},
		{
			name: "continuation limit",
			view: View{
				HasGoal: true, Goal: activeGoal(),
				ContinuationLimit: 2, ContinuationCount: 2,
			},
			action: ActionStop,
			reason: "continuation_limit",
		},
		{
			name: "round limit",
			view: View{
				HasGoal: true,
				Goal: &goal.SessionGoal{
					Status: goal.StatusActive, MaxRounds: 2, RoundsStarted: 2,
				},
			},
			action: ActionBlockRoundLimit,
			reason: "round_limit",
		},
		{
			name:   "default continuation",
			view:   View{HasGoal: true, Goal: activeGoal()},
			action: ActionSubmitContinuation,
			reason: "goal_active",
			prompt: goal.ContinuationPrompt,
		},
		{
			name: "escalation on no progress",
			view: View{
				HasGoal: true, Goal: activeGoal(),
				NoProgressStrikes:      goal.IterationEscalationStrikeThreshold,
				IterationToolAvailable: true,
			},
			action: ActionSubmitContinuation,
			reason: "goal_active",
			prompt: goal.IterateEscalationPrompt,
		},
		{
			name: "escalation once",
			view: View{
				HasGoal: true, Goal: activeGoal(),
				NoProgressStrikes:      goal.IterationEscalationStrikeThreshold,
				IterationToolAvailable: true,
				EscalationAttempted:    true,
			},
			action: ActionSubmitContinuation,
			reason: "goal_active",
			prompt: goal.ContinuationPrompt,
		},
		{
			name: "escalation on doom loop",
			view: View{
				HasGoal: true, Goal: activeGoal(),
				DoomLoopAdvised: true, IterationToolAvailable: true,
			},
			action: ActionSubmitContinuation,
			reason: "goal_active",
			prompt: goal.IterateEscalationPrompt,
		},
		{
			name: "no escalation without tool",
			view: View{
				HasGoal: true, Goal: activeGoal(),
				NoProgressStrikes: goal.IterationEscalationStrikeThreshold,
			},
			action: ActionSubmitContinuation,
			reason: "goal_active",
			prompt: goal.ContinuationPrompt,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tc.view)
			if got.Action != tc.action || got.Reason != tc.reason || got.Prompt != tc.prompt {
				t.Fatalf("Decide() = %+v, want action=%s reason=%s prompt=%q",
					got, tc.action, tc.reason, tc.prompt)
			}
		})
	}
}
