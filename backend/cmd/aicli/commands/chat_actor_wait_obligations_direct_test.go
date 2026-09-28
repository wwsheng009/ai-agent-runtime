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

const localDirectObligationsParent = "local-direct-obligations-parent"

// newLocalDirectObligationsRegistry 组装 doc1 §7.9 剩余② 的最小 CLI 宿主：
// 一条运行中的批任务行 + 空 Runtime 状态（没有 §6.12 挂起记录）。
func newLocalDirectObligationsRegistry(t *testing.T) (*localActorRegistry, string, string) {
	t.Helper()
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localDirectObligationsParent}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.SessionStore = runtimechat.NewInMemoryStorage()

	now := time.Now().UTC()
	childSessionID := "subagent_t1_0123456789abcdef"
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localDirectObligationsParent,
		ParentSessionID: localDirectObligationsParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(context.Background(), batch, []subagentbatch.SubagentTaskRecord{{
		TaskID:         "t1",
		BatchID:        batch.BatchID,
		OrderIndex:     1,
		UpdatedAt:      now,
		Status:         subagentbatch.TaskRunning,
		ChildSessionID: childSessionID,
	}})
	require.NoError(t, err)
	require.True(t, created)
	return newLocalActorRegistry(host), batch.BatchID, childSessionID
}

// TestLocalWaitAgentObligationsDirectFromTargetsWithoutParkedRecord 钉住 doc1 §7.9
// 剩余② 在 CLI 宿主的镜像：无挂起记录时，wait_agent 仍按显式目标直出 obligations[]
// （行形状与挂起路径同源），待办行保持 I1 语义（不给 finalize）。
func TestLocalWaitAgentObligationsDirectFromTargetsWithoutParkedRecord(t *testing.T) {
	registry, batchID, childSessionID := newLocalDirectObligationsRegistry(t)

	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{ID: childSessionID, TimeoutMs: 50})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.TimedOut)
	require.Len(t, result.Obligations, 1)
	require.Equal(t, batchID, result.Obligations[0].ObligationID)
	require.Equal(t, "batch", result.Obligations[0].SubjectKind)
	require.Equal(t, string(subagentbatch.BatchRunning), result.Obligations[0].State)
	require.False(t, result.Obligations[0].Terminal)
	require.NotEqual(t, "finalize", result.NextAction,
		"pending direct-out obligations must keep I1 semantics")
}

// TestLocalWaitAgentDirectObligationsFinalizeWhenTargetTerminal 钉住直出账本与收尾门
// 的配合：目标批次已终态时，wait_agent 立即返回 finalize 且带终态义务行。
func TestLocalWaitAgentDirectObligationsFinalizeWhenTargetTerminal(t *testing.T) {
	registry, batchID, childSessionID := newLocalDirectObligationsRegistry(t)
	ctx := context.Background()
	store := registry.Host.SubagentBatches

	task, err := store.GetTask(ctx, batchID, "t1")
	require.NoError(t, err)
	require.NoError(t, store.RecordTaskResult(ctx, batchID, "t1", task.Version, subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID: "t1", Success: true, Summary: "直出账本",
	}))
	batch, err := store.GetBatch(ctx, batchID)
	require.NoError(t, err)
	_, err = store.UpdateBatch(ctx, batchID, batch.Version, func(b *subagentbatch.SubagentBatch) {
		b.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)

	result, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{ID: childSessionID, TimeoutMs: 5000})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.TimedOut, "an all-terminal ledger must finalize immediately")
	require.Len(t, result.Obligations, 1)
	require.True(t, result.Obligations[0].Terminal)
	require.Equal(t, string(subagentbatch.BatchCompleted), result.Obligations[0].State)
	require.Equal(t, "finalize", result.NextAction)
}
