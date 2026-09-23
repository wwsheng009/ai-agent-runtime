package subagentbatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBuildWaitLedgerReportsDurableObligationState 钉住账本视图的唯一数据源：
// 行的 state/terminal/deadline 全部来自 §6.12 记录 + 批次库回读，而不是调用方
// 传入的摘要（plan §C3-4 统一返回契约 obligations[]）。
func TestBuildWaitLedgerReportsDurableObligationState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	deadline := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	now := time.Now().UTC()
	batch := &SubagentBatch{
		BatchID:         "batch-ledger-running",
		RootScopeID:     "root-1",
		ParentSessionID: "parent-1",
		ParentTurnID:    "turn-1",
		ExecutionMode:   ExecutionModeBackground,
		Status:          BatchRunning,
		BatchDeadline:   deadline,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(ctx, batch, nil)
	require.NoError(t, err)
	require.True(t, created)

	record := &TurnSuspension{
		TurnID:        "turn-1",
		SessionID:     "parent-1",
		RootScopeID:   "root-1",
		ObligationIDs: []string{"batch-ledger-running"},
		ParkedAt:      now,
	}
	rows, err := BuildWaitLedger(ctx, store, record)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "batch-ledger-running", rows[0].ObligationID)
	require.Equal(t, "batch", rows[0].SubjectKind)
	require.Equal(t, "batch-ledger-running", rows[0].SubjectID)
	require.Equal(t, string(BatchRunning), rows[0].State)
	require.False(t, rows[0].Terminal, "a running obligation must stay pending (I1)")
	require.WithinDuration(t, deadline, rows[0].DeadlineAt, time.Second)
}

// TestBuildWaitLedgerSurfacesTerminalAndMissingObligations 覆盖两条安全方向相反
// 的分支：终态行必须报 terminal=true（父可收尾），而账本引用到没有行号的 obligation
// 时必须报 missing 且**非终态**——消失的 obligation 不能证明工作已完成，只能继续挂起。
func TestBuildWaitLedgerSurfacesTerminalAndMissingObligations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Now().UTC()
	finished := now
	batch := &SubagentBatch{
		BatchID:         "batch-ledger-done",
		RootScopeID:     "root-1",
		ParentSessionID: "parent-1",
		ParentTurnID:    "turn-2",
		ExecutionMode:   ExecutionModeBackground,
		Status:          BatchCompleted,
		FinishedAt:      &finished,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(ctx, batch, nil)
	require.NoError(t, err)
	require.True(t, created)

	record := &TurnSuspension{
		TurnID:      "turn-2",
		SessionID:   "parent-1",
		RootScopeID: "root-1",
		// ResumeQueue 是 dispatcher 写入的账本来源；两个 id 都在这里声明。
		ResumeQueue: []string{"batch-ledger-done", "batch-ledger-gone"},
		ParkedAt:    now,
	}
	rows, err := BuildWaitLedger(ctx, store, record)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, string(BatchCompleted), rows[0].State)
	require.True(t, rows[0].Terminal, "a terminal obligation lets the parent finalize")

	require.Equal(t, WaitLedgerStateMissing, rows[1].State)
	require.False(t, rows[1].Terminal, "a vanished obligation must keep the turn parked")
}

// TestBuildWaitLedgerEmptyInputsYieldNoRows 钉住"空账本"的判定输入：没有记录、
// 没有存储、或记录没有 obligation 时都不产生行，调用方据此立即 finalize
// （AC-P2-4e，不空等）。
func TestBuildWaitLedgerEmptyInputsYieldNoRows(t *testing.T) {
	ctx := context.Background()
	rows, err := BuildWaitLedger(ctx, nil, nil)
	require.NoError(t, err)
	require.Empty(t, rows)

	store := newTestStore(t)
	rows, err = BuildWaitLedger(ctx, store, &TurnSuspension{TurnID: "turn-empty", SessionID: "parent-1"})
	require.NoError(t, err)
	require.Empty(t, rows)
}
