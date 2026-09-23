package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplyAgentWaitLedgerFinalizesOnlyWhenNothingIsPending 钉住 I1 的唯一放行
// 方向：账本行全部终态才允许 next_action=finalize（AC-P2-4e），且 terminal_delta
// 只报等待段基线之后完成的行。
func TestApplyAgentWaitLedgerFinalizesOnlyWhenNothingIsPending(t *testing.T) {
	rows := []AgentWaitObligation{
		{ObligationID: "batch-1", SubjectKind: "batch", State: "completed", Terminal: true},
		{ObligationID: "batch-2", SubjectKind: "batch", State: "failed", Terminal: true},
	}
	result := ApplyAgentWaitLedger(
		&AgentWaitResult{NextAction: "stop_empty_event_poll: legacy guidance"},
		rows,
		[]string{"batch-1"},
	)
	require.Len(t, result.Obligations, 2)
	require.Equal(t, 2, result.TerminalCount)
	require.Equal(t, []string{"batch-2"}, result.TerminalDelta,
		"terminal_delta must report only obligations that finished after the baseline")
	require.Equal(t, "finalize", result.NextAction,
		"an all-terminal ledger is decisive: there is nothing left to wait for")
}

// TestApplyAgentWaitLedgerKeepsParentFromFinalizingWhilePending 钉住 I1 的拦截
// 方向：只要还有非终态 obligation，就绝不给出 finalize，也不覆盖已有指引。
func TestApplyAgentWaitLedgerKeepsParentFromFinalizingWhilePending(t *testing.T) {
	rows := []AgentWaitObligation{
		{ObligationID: "batch-1", SubjectKind: "batch", State: "running"},
		{ObligationID: "batch-2", SubjectKind: "batch", State: "completed", Terminal: true},
	}
	result := ApplyAgentWaitLedger(&AgentWaitResult{NextAction: "consume_events: legacy guidance"}, rows, nil)
	require.Equal(t, 1, result.TerminalCount)
	require.Equal(t, []string{"batch-2"}, result.TerminalDelta)
	require.Equal(t, "consume_events: legacy guidance", result.NextAction,
		"pending obligations must never overwrite existing guidance with finalize")
}

// TestApplyAgentWaitLedgerFillsGuidanceWhenEmpty 覆盖没有既有指引时的保守兜底：
// 超时 ⇒ continue_wait，未超时（有活动）⇒ inspect。
func TestApplyAgentWaitLedgerFillsGuidanceWhenEmpty(t *testing.T) {
	rows := []AgentWaitObligation{{ObligationID: "batch-1", State: "running"}}
	require.Equal(t, "continue_wait",
		ApplyAgentWaitLedger(&AgentWaitResult{TimedOut: true}, rows, nil).NextAction)
	require.Equal(t, "inspect",
		ApplyAgentWaitLedger(&AgentWaitResult{}, rows, nil).NextAction)
}

// TestApplyAgentWaitLedgerIsIdempotent 钉住重复附加不会重复计数：等待外壳可能
// 在多处包装里都调用它，TerminalCount 必须始终等于终态行数。
func TestApplyAgentWaitLedgerIsIdempotent(t *testing.T) {
	rows := []AgentWaitObligation{
		{ObligationID: "batch-1", State: "completed", Terminal: true},
		{ObligationID: "batch-2", State: "running"},
	}
	result := ApplyAgentWaitLedger(&AgentWaitResult{}, rows, nil)
	result = ApplyAgentWaitLedger(result, rows, nil)
	require.Equal(t, 1, result.TerminalCount)
	require.Equal(t, []string{"batch-1"}, result.TerminalDelta)
	require.Len(t, result.Obligations, 2)
}

// TestApplyAgentWaitLedgerIgnoresEmptyLedger 钉住旧语义不变：没有账本时等待结果
// 逐字段保持原样（未挂起/非 durable/读失败都走这条路，plan §13.8 fail-open）。
func TestApplyAgentWaitLedgerIgnoresEmptyLedger(t *testing.T) {
	result := ApplyAgentWaitLedger(&AgentWaitResult{NextAction: "unchanged", ReadyCount: 1}, nil, nil)
	require.Empty(t, result.Obligations)
	require.Equal(t, 0, result.TerminalCount)
	require.Equal(t, "unchanged", result.NextAction)
	require.Equal(t, 1, result.ReadyCount)

	require.Nil(t, ApplyAgentWaitLedger(nil, []AgentWaitObligation{{ObligationID: "batch-1"}}, nil))
}

// TestSummarizeAgentWaitLedgerCountsPendingAndDelta 钉住两条等待路径共用的算法
// 本身（AC-P2-4g 共享实现）：非终态行计入 pending，超出基线的终态行计入 delta。
func TestSummarizeAgentWaitLedgerCountsPendingAndDelta(t *testing.T) {
	view := SummarizeAgentWaitLedger([]AgentWaitObligation{
		{ObligationID: "row-1", Terminal: true},
		{ObligationID: "row-2", Terminal: true},
		{ObligationID: "row-3"},
	}, []string{"row-1", " "})
	require.Equal(t, 1, view.PendingCount)
	require.Equal(t, 2, view.TerminalCount)
	require.Equal(t, []string{"row-2"}, view.TerminalDelta)
	require.Len(t, view.Obligations, 3)
}

// TestApplyWaitTeamLedgerSharesAgentLedgerComputation 钉住 AC-P2-4g：team 视图复用
// wait_agent 的行/计数/增量算法，但 finalize 的放行条件更窄——只有团队本身终态
// 且无待办任务行时才给出，避免把"尚未规划出任务的在跑团队"误判为可以收尾。
func TestApplyWaitTeamLedgerSharesAgentLedgerComputation(t *testing.T) {
	rows := []AgentWaitObligation{
		{ObligationID: "task-1", SubjectKind: "team_task", State: "done", Terminal: true},
		{ObligationID: "task-2", SubjectKind: "team_task", State: "running"},
	}

	// 有待办行 ⇒ 绝不 finalize，且不覆盖既有指引。
	pending := ApplyWaitTeamLedger(&WaitTeamResult{NextAction: "team execution continues"}, rows, nil)
	require.Len(t, pending.Obligations, 2)
	require.Equal(t, 1, pending.TerminalCount)
	require.Equal(t, 1, pending.PendingCount)
	require.Equal(t, []string{"task-1"}, pending.TerminalDelta)
	require.Equal(t, "team execution continues", pending.NextAction)

	// 团队终态 + 全部行终态 ⇒ 与 wait_agent 同一放行方向。
	drained := ApplyWaitTeamLedger(&WaitTeamResult{Terminal: true}, rows[:1], nil)
	require.Equal(t, 1, drained.TerminalCount)
	require.Equal(t, 0, drained.PendingCount)
	require.Equal(t, "finalize", drained.NextAction)

	// 非终态团队即使没有待办行也不得给 finalize（fail-open 方向）。
	running := ApplyWaitTeamLedger(&WaitTeamResult{Terminal: false}, rows[:1], nil)
	require.NotEqual(t, "finalize", running.NextAction)

	// 空账本 ⇒ 逐字段保持原样。
	untouched := ApplyWaitTeamLedger(&WaitTeamResult{NextAction: "unchanged"}, nil, nil)
	require.Empty(t, untouched.Obligations)
	require.Equal(t, 0, untouched.TerminalCount)
	require.Equal(t, "unchanged", untouched.NextAction)

	require.Nil(t, ApplyWaitTeamLedger(nil, rows, nil))
}
