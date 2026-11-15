package toolbroker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestAgentWaitFlowDeliversTerminalCohortWithoutOpeningWindow(t *testing.T) {
	obligation := AgentWaitObligation{
		ObligationID: "batch-1",
		SubjectKind:  "batch",
		SubjectID:    "batch-1",
		State:        "completed",
		Terminal:     true,
	}
	waitCalls := 0
	flow := AgentWaitFlow{
		Budget:      &agentcontrol.WaitBudget{},
		BudgetLimit: 2,
		Ledger: func(context.Context) ([]AgentWaitObligation, []string, bool, string) {
			return []AgentWaitObligation{obligation}, nil, false, "turn-1"
		},
		Wait: func(context.Context, WaitAgentArgs) (*AgentWaitResult, error) {
			waitCalls++
			return &AgentWaitResult{}, nil
		},
		TerminalRead: func(context.Context, AgentWaitObligation) ([]AgentStatusResult, error) {
			return []AgentStatusResult{{ID: "child-1", SessionID: "child-1", Status: "completed", Exists: true}}, nil
		},
	}
	result, err := flow.Run(context.Background(), WaitAgentArgs{Peek: true})
	require.NoError(t, err)
	require.Zero(t, waitCalls, "a terminal cohort must not open a wait window")
	require.True(t, result.Peek)
	require.Equal(t, 1, result.ReadyCount)
	require.Equal(t, "child-1", result.MatchedSessionID)
}

func TestAgentWaitFlowRefusesExhaustedBudgetBeforeWaiting(t *testing.T) {
	obligation := AgentWaitObligation{ObligationID: "batch-1", SubjectKind: "batch", SubjectID: "batch-1", State: "running"}
	budget := &agentcontrol.WaitBudget{}
	budget.Observe("turn-1", false, 1)
	waitCalls := 0
	flow := AgentWaitFlow{
		Budget:      budget,
		BudgetLimit: 1,
		Ledger: func(context.Context) ([]AgentWaitObligation, []string, bool, string) {
			return []AgentWaitObligation{obligation}, nil, true, "turn-1"
		},
		Wait: func(context.Context, WaitAgentArgs) (*AgentWaitResult, error) {
			waitCalls++
			return &AgentWaitResult{}, nil
		},
	}
	result, err := flow.Run(context.Background(), WaitAgentArgs{})
	require.NoError(t, err)
	require.Zero(t, waitCalls, "an exhausted budget must not open another window")
	require.True(t, result.WaitBudgetExhausted)
	require.Equal(t, 1, result.WaitBudgetConsecutive)
	require.Equal(t, 1, result.WaitBudgetLimit)
	require.Len(t, result.Obligations, 1)
	require.False(t, result.Obligations[0].Terminal)
}

func TestAgentWaitFlowRefreshesLedgerAndResetsBudgetOnTerminal(t *testing.T) {
	obligation := AgentWaitObligation{ObligationID: "batch-1", SubjectKind: "batch", SubjectID: "batch-1", State: "running"}
	refreshed := obligation
	refreshed.State = "completed"
	refreshed.Terminal = true
	ledgerCalls := 0
	budget := &agentcontrol.WaitBudget{}
	budget.Observe("turn-1", false, 5)
	flow := AgentWaitFlow{
		Budget:      budget,
		BudgetLimit: 5,
		Ledger: func(context.Context) ([]AgentWaitObligation, []string, bool, string) {
			ledgerCalls++
			if ledgerCalls == 1 {
				return []AgentWaitObligation{obligation}, nil, true, "turn-1"
			}
			return []AgentWaitObligation{refreshed}, nil, false, "turn-1"
		},
		Wait: func(context.Context, WaitAgentArgs) (*AgentWaitResult, error) {
			return &AgentWaitResult{TerminalDelta: []string{"batch-1"}}, nil
		},
		TerminalRead: func(context.Context, AgentWaitObligation) ([]AgentStatusResult, error) {
			return []AgentStatusResult{{ID: "child-1", SessionID: "child-1", Status: "completed", Exists: true}}, nil
		},
	}
	result, err := flow.Run(context.Background(), WaitAgentArgs{})
	require.NoError(t, err)
	require.Equal(t, 2, ledgerCalls)
	require.False(t, result.WaitBudgetExhausted)
	require.Equal(t, 1, result.ReadyCount)
	consecutive, exhausted := budget.Exhausted("turn-1", 5)
	require.False(t, exhausted, "terminal refresh must reset the budget")
	require.Zero(t, consecutive)
}

func TestAgentWaitFlowInterruptedWindowDoesNotConsumeBudget(t *testing.T) {
	obligation := AgentWaitObligation{ObligationID: "batch-1", SubjectKind: "batch", SubjectID: "batch-1", State: "running"}
	budget := &agentcontrol.WaitBudget{}
	flow := AgentWaitFlow{
		Budget:      budget,
		BudgetLimit: 1,
		Ledger: func(context.Context) ([]AgentWaitObligation, []string, bool, string) {
			return []AgentWaitObligation{obligation}, nil, true, "turn-1"
		},
		Wait: func(context.Context, WaitAgentArgs) (*AgentWaitResult, error) {
			return &AgentWaitResult{Interrupted: true}, nil
		},
	}
	result, err := flow.Run(context.Background(), WaitAgentArgs{})
	require.NoError(t, err)
	require.False(t, result.WaitBudgetExhausted)
	consecutive, exhausted := budget.Exhausted("turn-1", 1)
	require.False(t, exhausted)
	require.Zero(t, consecutive)
}
