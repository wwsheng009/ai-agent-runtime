package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-26 真机 E2E：父模型拿派发回执里最显眼的 batch id 直接
// wait_agent(batch_id)，旧路径只报 target_not_found。现在 batch id 必须先展开为
// 它早绑定 child_session_id 的 task 子会话，再走既有解析/快照路径。
func TestWaitAgentOnDispatchBatchIDExpandsToTaskChildren(t *testing.T) {
	host, _ := newLocalWaitLedgerHost(t, subagentbatch.BatchCompleted, "turn-batch-wait")
	ctx := context.Background()
	// 批任务子会话不落 SessionStore：显式装上（空）会话存储，才能让
	// agentSnapshot 走到批任务账本回退分支。
	host.SessionStore = runtimechat.NewInMemoryStorage()
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localWaitLedgerTestSession,
		ParentSessionID: localWaitLedgerTestSession,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		TaskCount:       1,
		CompletedCount:  1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{{
		TaskID:         "task-batch-wait",
		BatchID:        batch.BatchID,
		ChildSessionID: "child-batch-wait",
		Status:         subagentbatch.TaskSucceeded,
		OrderIndex:     0,
		UpdatedAt:      now,
		Version:        1,
	}}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created)

	registry := newLocalActorRegistry(host)
	expanded, err := registry.expandLocalBatchWaitTargets(ctx, []string{batch.BatchID, "unknown-id"})
	require.NoError(t, err)
	require.Equal(t, []string{"child-batch-wait", "unknown-id"}, expanded,
		"a batch id expands to its bound child sessions; unknown ids stay for the missing-target guidance")

	result, err := registry.waitForLocalAgentObserved(ctx, toolbroker.WaitAgentArgs{
		ID:        batch.BatchID,
		TimeoutMs: 2000,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, result.NextAction, "target_not_found",
		"waiting on a dispatch batch id must resolve through its task children")
	require.Equal(t, 1, result.ReadyCount)
	require.Len(t, result.Agents, 1)
	require.Equal(t, "child-batch-wait", result.Agents[0].SessionID)
	require.True(t, result.Agents[0].Exists)
}
