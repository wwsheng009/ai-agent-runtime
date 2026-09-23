package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const localWaitLedgerTestSession = "parent-session"

// newLocalWaitLedgerHost 组装 wait_agent 账本视图的最小宿主：真实 batch store 上的
// §6.12 挂起记录 + 指向该 turn 的派生缓存（RuntimeState.SuspendedTurnID）+ mailbox
// 等待所需的 EventStore。账本读数必须来自 durable 控制面，所以夹具用真 store。
func newLocalWaitLedgerHost(t *testing.T, batchStatus subagentbatch.BatchStatus, suspendedTurnID string) (*localChatRuntimeHost, string) {
	t.Helper()
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localWaitLedgerTestSession}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localWaitLedgerTestSession,
		ParentSessionID: localWaitLedgerTestSession,
		ParentTurnID:    suspendedTurnID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          batchStatus,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        suspendedTurnID,
		SessionID:     localWaitLedgerTestSession,
		RootScopeID:   localWaitLedgerTestSession,
		ObligationIDs: []string{batch.BatchID},
		ParkedAt:      now,
	}))
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:       localWaitLedgerTestSession,
		Status:          runtimechat.SessionIdle,
		SuspendedTurnID: suspendedTurnID,
	}))
	return host, batch.BatchID
}

// TestWaitAgentReturnsFinalizeWithoutBlockingOnTerminalLedger 钉住 AC-P2-4e：
// 账本非空且 obligation 全部终态 ⇒ 立即返回 finalize，不空等整个观测窗口。
func TestWaitAgentReturnsFinalizeWithoutBlockingOnTerminalLedger(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchCompleted, "turn-parked")
	registry := newLocalActorRegistry(host)

	startedAt := time.Now()
	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 30000})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.TimedOut, "an all-terminal ledger must not report a timeout")
	require.Equal(t, "finalize", result.NextAction)
	require.Len(t, result.Obligations, 1)
	require.Equal(t, batchID, result.Obligations[0].ObligationID)
	require.Equal(t, "batch", result.Obligations[0].SubjectKind)
	require.True(t, result.Obligations[0].Terminal)
	require.Equal(t, 1, result.TerminalCount)
	require.Empty(t, result.TerminalDelta, "nothing finished during a wait that never blocked")
	require.Less(t, time.Since(startedAt), 5*time.Second,
		"an all-terminal ledger must return immediately instead of blocking")
}

// TestWaitAgentKeepsPendingLedgerAndNeverFinalizes 钉住 I1 的拦截方向：还有非终态
// obligation 时等待照旧进行（不提前返回），且结果必须带账本、绝不给 finalize。
func TestWaitAgentKeepsPendingLedgerAndNeverFinalizes(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchRunning, "turn-parked")
	registry := newLocalActorRegistry(host)

	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 40})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.TimedOut, "a pending obligation keeps the legacy observation window")
	require.Len(t, result.Obligations, 1)
	require.Equal(t, batchID, result.Obligations[0].ObligationID)
	require.False(t, result.Obligations[0].Terminal)
	require.Equal(t, string(subagentbatch.BatchRunning), result.Obligations[0].State)
	require.NotEqual(t, "finalize", result.NextAction,
		"pending obligations must never let the parent finalize (I1)")
}

// TestWaitAgentWithoutParkedTurnKeepsLegacySemantics 钉住 fail-open：没有挂起 turn
// 时账本为空，等待结果不新增账本字段（plan §13.8：按"未挂起"处理）。
func TestWaitAgentWithoutParkedTurnKeepsLegacySemantics(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localWaitLedgerTestSession}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)
	registry := newLocalActorRegistry(host)

	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 40})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.TimedOut)
	require.Empty(t, result.Obligations)
	require.Equal(t, 0, result.TerminalCount)
}
