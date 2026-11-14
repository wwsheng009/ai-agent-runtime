package toolbroker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSuspendAgentWaitResultForBudgetStampsSuspendVerdict 钉住 §16.3 的
// next_action=suspend 判据：预算耗尽时不谎报超时、不结束 turn，但会明确要求
// 停止新的活动等待窗口。
func TestSuspendAgentWaitResultForBudgetStampsSuspendVerdict(t *testing.T) {
	result := &AgentWaitResult{TimedOut: true, PendingCount: 1}
	got := SuspendAgentWaitResultForBudget(result, 2, 2)
	require.True(t, got.WaitBudgetExhausted)
	require.True(t, got.ExecutionContinues)
	require.True(t, strings.HasPrefix(got.NextAction, "suspend:"),
		"next_action must carry the suspend verdict, got %q", got.NextAction)
	require.Contains(t, got.NextAction, "maxConsecutiveWaitWithoutProgress=2")
	require.True(t, got.TimedOut, "an expired observation window still reports itself")
}

// A drained child wait must not suspend ongoing parent work. Normalize old
// finalize hints without bringing their parent-finalization semantics back.
func TestSuspendAgentWaitResultForBudgetPreservesParentContinuation(t *testing.T) {
	for _, action := range []string{ContinueParentWorkNextAction, "finalize"} {
		got := SuspendAgentWaitResultForBudget(&AgentWaitResult{NextAction: action}, 2, 2)
		require.False(t, got.WaitBudgetExhausted)
		require.Equal(t, ContinueParentWorkNextAction, got.NextAction)
	}
}

func TestWaitBudgetContinuationHintCannotHidePendingObligations(t *testing.T) {
	rows := []AgentWaitObligation{{ObligationID: "pending-child"}}
	agent := SuspendAgentWaitResultForBudget(&AgentWaitResult{
		NextAction: ContinueParentWorkNextAction, Obligations: rows,
	}, 2, 2)
	require.True(t, agent.WaitBudgetExhausted)
	require.Contains(t, agent.NextAction, "suspend:")
	team := SuspendWaitTeamResultForBudget(&WaitTeamResult{
		NextAction: ContinueParentWorkNextAction, Obligations: rows,
	}, 2, 2)
	require.True(t, team.WaitBudgetExhausted)
	require.Contains(t, team.NextAction, "suspend:")

	team = SuspendWaitTeamResultForBudget(&WaitTeamResult{
		Terminal: true, NextAction: ContinueParentWorkNextAction,
		Obligations: []AgentWaitObligation{{ObligationID: "done", Terminal: true}},
	}, 2, 2)
	require.False(t, team.WaitBudgetExhausted)
	require.Equal(t, ContinueParentWorkNextAction, team.NextAction)
}

// TestSuspendAgentWaitResultForBudgetKeepsLedgerView 钉住账本视图不被预算判据冲掉：
// 模型仍要看到 obligations / terminal_delta 才能决定巡检还是收尾。
func TestSuspendAgentWaitResultForBudgetKeepsLedgerView(t *testing.T) {
	result := ApplyAgentWaitLedger(&AgentWaitResult{TimedOut: true}, []AgentWaitObligation{
		{ObligationID: "batch-1", SubjectKind: "batch", State: "running"},
		{ObligationID: "batch-2", SubjectKind: "batch", State: "completed", Terminal: true},
	}, nil)
	require.Len(t, result.Obligations, 2)

	got := SuspendAgentWaitResultForBudget(result, 2, 2)
	require.Len(t, got.Obligations, 2)
	require.Equal(t, "batch-2", got.TerminalDelta[0])
	require.True(t, strings.HasPrefix(got.NextAction, "suspend:"))
}
