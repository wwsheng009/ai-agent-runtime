package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestAPIWaitLedgerFallsBackToContextTurnID 钉住直连 /api/agent/chat 路径的账本
// 可见性：该路径不持久化 RuntimeState（SuspendedTurnID 为空），wait 账本必须用
// 调用 ctx 上的 turn 注解兜底定位挂起记录——与 parkAgentChildObligation 同源。
// 没有这条兜底，Web 回合派发的轻量子会话/批次义务在 wait_agent 里永远不可见。
func TestAPIWaitLedgerFallsBackToContextTurnID(t *testing.T) {
	ctx := context.Background()
	controller, sessionManager := newAPIAgentObligationFixture(t)
	parent, err := sessionManager.Create(ctx, "user-api-agent-ledger-ctx")
	require.NoError(t, err)
	child, err := sessionManager.Create(ctx, "user-api-agent-ledger-ctx-child")
	require.NoError(t, err)

	store := controller.handler.getSubagentBatchStore()
	require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-api-ctx",
		SessionID:     parent.ID,
		RootScopeID:   parent.ID,
		ObligationIDs: []string{subagentbatch.AgentSessionObligationID(child.ID)},
		ParkedAt:      time.Now().UTC(),
	}))
	seedAPIAgentSessionRunningRow(t, controller, parent.ID, child.ID)

	// 不写 RuntimeState：只有 ctx 上的 turn 注解可用。
	waitCtx := agent.WithTurnID(toolctx.WithSessionID(ctx, parent.ID), "turn-api-ctx")
	obligations, baseline, pending, key := controller.waitLedger(waitCtx)
	require.True(t, pending, "a live child keeps the ledger pending (I1)")
	require.Equal(t, parent.ID+"|turn-api-ctx", key)
	require.Empty(t, baseline, "a running child is not part of the terminal baseline")
	require.Len(t, obligations, 1)
	require.Equal(t, subagentbatch.AgentSessionObligationID(child.ID), obligations[0].ObligationID)
	require.Equal(t, "agent_session", obligations[0].SubjectKind)
	require.Equal(t, child.ID, obligations[0].SubjectID)
	require.False(t, obligations[0].Terminal)
}
