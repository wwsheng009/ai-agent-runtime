package toolbroker

import (
	"context"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// AgentWaitFlow is the host-neutral orchestration shell shared by
// subagent_wait implementations.
//
// Hosts provide four data-plane callbacks:
//   - Ledger reads the caller's durable obligation ledger + wait-budget key.
//   - TargetObligations projects explicit wait targets when no parked ledger
//     exists.
//   - Wait opens the actual mailbox/status window.
//   - TerminalResults materializes one terminal obligation cohort.
//
// Budget accounting, terminal cohort delivery, terminal_delta refresh, and
// probe/peek semantics live here so runtime-server and aicli cannot drift.
type AgentWaitFlow struct {
	Budget       *agentcontrol.WaitBudget
	BudgetLimit  int
	Ledger       func(context.Context) ([]AgentWaitObligation, []string, bool, string)
	TargetLedger func(context.Context, WaitAgentArgs) ([]AgentWaitObligation, []string, bool)
	Wait         func(context.Context, WaitAgentArgs) (*AgentWaitResult, error)
	TerminalRead func(context.Context, AgentWaitObligation) ([]AgentStatusResult, error)
}

func (f AgentWaitFlow) Run(ctx context.Context, args WaitAgentArgs) (*AgentWaitResult, error) {
	if f.Ledger == nil || f.Wait == nil {
		return nil, fmt.Errorf("agent wait flow: ledger/wait callbacks not configured")
	}
	obligations, baseline, pending, budgetKey := f.Ledger(ctx)
	directOut := false
	peek := args.Peek
	if len(obligations) == 0 && f.TargetLedger != nil {
		obligations, baseline, pending = f.TargetLedger(ctx, args)
		directOut = len(obligations) > 0
	}
	waitBudgetLimit := f.BudgetLimit
	if len(obligations) > 0 && !pending && !directOut {
		// No wait window is needed, but an empty result would leave the
		// durable synthesis gate closed. Deliver the whole parked cohort.
		f.Budget.Reset(budgetKey)
		result, err := BuildTerminalAgentWaitResult(ctx, nil, obligations, baseline, f.TerminalRead)
		if result != nil {
			result.Peek = peek
		}
		return result, err
	}
	if !peek && len(obligations) > 0 && pending && budgetKey != "" {
		// Wait budget exhausted: do not open another active window. The
		// ledger view is still returned; the turn-finalization gate remains
		// the backstop.
		if consecutive, exhausted := f.Budget.Exhausted(budgetKey, waitBudgetLimit); exhausted {
			result := FinalizeAgentWaitResult(&AgentWaitResult{}, time.Now())
			result = ApplyAgentWaitLedger(result, obligations, baseline)
			return SuspendAgentWaitResultForBudget(result, consecutive, waitBudgetLimit), nil
		}
	}
	result, err := f.Wait(ctx, args)
	if err != nil || result == nil {
		return result, err
	}
	result = ApplyAgentWaitLedger(result, obligations, baseline)
	if peek {
		if result != nil {
			result.Peek = true
		}
		return result, nil
	}
	if len(obligations) > 0 {
		// Re-read the ledger after the wait: terminal_delta means "became
		// terminal during this segment", so the pre-wait snapshot cannot
		// supply it.
		refreshed, _, refreshedPending, refreshedKey := f.Ledger(ctx)
		switch {
		case refreshedKey == "":
			// Read failed: fail open, this segment does not consume budget.
		case len(refreshed) == 0:
			f.Budget.Reset(budgetKey)
		default:
			obligations = refreshed
			result = ApplyAgentWaitLedger(result, obligations, baseline)
			switch {
			case !refreshedPending:
				f.Budget.Reset(budgetKey)
				return BuildTerminalAgentWaitResult(ctx, result, obligations, baseline, f.TerminalRead)
			case result.Interrupted:
				// steer/ESC ended the window early; it is not a spent segment.
			default:
				consecutive, exhausted := f.Budget.Observe(budgetKey, len(result.TerminalDelta) > 0, waitBudgetLimit)
				result = StampAgentWaitBudget(result, consecutive, waitBudgetLimit)
				if exhausted {
					result = SuspendAgentWaitResultForBudget(result, consecutive, waitBudgetLimit)
				}
			}
		}
	}
	return result, nil
}
