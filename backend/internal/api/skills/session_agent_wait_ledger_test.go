package skills

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

const apiWaitLedgerTestSession = "api-wait-ledger-parent"

// newAPIWaitLedgerFixture 组装 wait_agent 账本视图的最小 API 宿主：真实 batch store
// 上的 §6.12 挂起记录 + 指向该 turn 的运行态派生缓存（RuntimeState.SuspendedTurnID）。
// 账本读数必须来自宿主自己的控制面，所以夹具用真 store 而不是打桩。
func newAPIWaitLedgerFixture(t *testing.T, batchStatus subagentbatch.BatchStatus, suspendedTurnID string) (*sessionAgentController, string) {
	t.Helper()
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	handler.sessionRuntimeStore = chat.NewInMemoryRuntimeStore(16)
	handler.sessionRuntimeStoreKey = agentRuntimeMemoryStoreKey

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     apiWaitLedgerTestSession,
		ParentSessionID: apiWaitLedgerTestSession,
		ParentTurnID:    suspendedTurnID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          batchStatus,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := handler.getSubagentBatchStore().CreateBatch(ctx, batch, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, handler.getSubagentBatchStore().ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        suspendedTurnID,
		SessionID:     apiWaitLedgerTestSession,
		RootScopeID:   apiWaitLedgerTestSession,
		ObligationIDs: []string{batch.BatchID},
		ParkedAt:      now,
	}))
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID:       apiWaitLedgerTestSession,
		Status:          chat.SessionIdle,
		SuspendedTurnID: suspendedTurnID,
	}))
	return &sessionAgentController{handler: handler}, batch.BatchID
}

// TestSessionAgentControllerWaitFinalizesDrainedLedger 钉住 AC-P2-4e 在 API 宿主上的
// 落点：账本非空且 obligation 全部终态 ⇒ 立即返回 finalize，不空等观测窗口。
func TestSessionAgentControllerWaitFinalizesDrainedLedger(t *testing.T) {
	controller, batchID := newAPIWaitLedgerFixture(t, subagentbatch.BatchCompleted, "turn-parked")
	ctx := toolctx.WithSessionID(context.Background(), apiWaitLedgerTestSession)

	startedAt := time.Now()
	result, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{ID: "api-child", TimeoutMs: 30000})
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

// TestSessionAgentControllerWaitLedgerKeepsPendingRows 钉住 I1 的拦截方向：还有非终态
// obligation 时账本必须报 pending，且附到结果上也绝不给 finalize。
func TestSessionAgentControllerWaitLedgerKeepsPendingRows(t *testing.T) {
	controller, batchID := newAPIWaitLedgerFixture(t, subagentbatch.BatchRunning, "turn-parked")
	ctx := toolctx.WithSessionID(context.Background(), apiWaitLedgerTestSession)

	obligations, baseline, pending := controller.waitLedger(ctx)
	require.True(t, pending)
	require.Empty(t, baseline, "a running obligation is not part of the terminal baseline")
	require.Len(t, obligations, 1)
	require.Equal(t, batchID, obligations[0].ObligationID)
	require.False(t, obligations[0].Terminal)
	require.Equal(t, string(subagentbatch.BatchRunning), obligations[0].State)

	result := toolbroker.ApplyAgentWaitLedger(&toolbroker.AgentWaitResult{TimedOut: true}, obligations, baseline)
	require.NotEqual(t, "finalize", result.NextAction,
		"pending obligations must never let the parent finalize (I1)")
	require.Equal(t, 0, result.TerminalCount)
}

// TestSessionAgentControllerWaitLedgerFailsOpen 钉住 plan §13.8：任一步判读取不到
// 证据都退化为空账本（旧语义），绝不凭空报账本。
func TestSessionAgentControllerWaitLedgerFailsOpen(t *testing.T) {
	controller, _ := newAPIWaitLedgerFixture(t, subagentbatch.BatchCompleted, "turn-parked")

	// 1) 未注入调用方会话（toolctx 缺失）⇒ 空账本。
	obligations, baseline, pending := controller.waitLedger(context.Background())
	require.Empty(t, obligations)
	require.Empty(t, baseline)
	require.False(t, pending)

	ctx := toolctx.WithSessionID(context.Background(), apiWaitLedgerTestSession)

	// 2) 运行态派生缓存里没有挂起 turn ⇒ 空账本。
	require.NoError(t, controller.handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: apiWaitLedgerTestSession,
		Status:    chat.SessionIdle,
	}))
	obligations, baseline, pending = controller.waitLedger(ctx)
	require.Empty(t, obligations)
	require.Empty(t, baseline)
	require.False(t, pending)

	// 3) 宿主没有可读的 batch 控制面 ⇒ 空账本（只读探测不得触发懒加载建库）。
	controller.handler.SetSubagentBatchStore(nil)
	require.NoError(t, controller.handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID:       apiWaitLedgerTestSession,
		Status:          chat.SessionIdle,
		SuspendedTurnID: "turn-parked",
	}))
	require.Nil(t, controller.handler.peekSubagentBatchStore())
	obligations, baseline, pending = controller.waitLedger(ctx)
	require.Empty(t, obligations)
	require.Empty(t, baseline)
	require.False(t, pending)
}

// TestPeekSubagentBatchStoreDoesNotLazyCreate 钉住只读探测与惰性访问器的分工：账本
// 判读走 peek（不建库），宿主自己的批量路径仍走 get（首次调用建默认库）。
func TestPeekSubagentBatchStoreDoesNotLazyCreate(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	require.Nil(t, handler.peekSubagentBatchStore(), "the read path must not create a store")
	require.NotNil(t, handler.getSubagentBatchStore())
	require.NotNil(t, handler.peekSubagentBatchStore())
}
