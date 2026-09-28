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

const localPreDispatchParent = "local-predispatch-parent"

// TestLocalWaitAgentTaskIDProducesPreDispatchFailureObservation 钉住 doc1 §7.10
// 新遗留① 在 CLI 宿主的镜像：派发前失败（如 single-writer 拒绝）的终态任务没有、
// 也永远不会有 child_session_id，`wait_agent(task_id)` 必须给出可读的失败观测
// （stopped + 错误分类 + 任务身份），而不是让模型 "retry shortly"。
func TestLocalWaitAgentTaskIDProducesPreDispatchFailureObservation(t *testing.T) {
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localPreDispatchParent}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.SessionStore = runtimechat.NewInMemoryStorage()
	registry := newLocalActorRegistry(host)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localPreDispatchParent,
		ParentSessionID: localPreDispatchParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		TaskCount:       1,
		FailedCount:     1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{{
		TaskID:      "t-predispatch",
		BatchID:     batch.BatchID,
		OrderIndex:  1,
		UpdatedAt:   now,
		Status:      subagentbatch.TaskFailed,
		TaskType:    "implement",
		TaskSubject: "single-writer 被拒",
		ErrorClass:  "policy",
		ErrorCode:   "single_writer_rejected",
	}})
	require.NoError(t, err)
	require.True(t, created)

	resolved, err := registry.resolveLocalAgentTargetSessionID(ctx, "t-predispatch")
	require.NoError(t, err, "terminal unbound task must not surface an executable retry error")
	require.Equal(t, "t-predispatch", resolved)

	result, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{ID: "t-predispatch", TimeoutMs: 5000})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Agent)
	require.True(t, result.Agent.Exists)
	require.Equal(t, string(runtimechat.SessionStopped), result.Agent.Status)
	require.Equal(t, string(subagentbatch.TaskFailed), result.Agent.CurrentTaskStatus)
	require.Equal(t, "policy: single_writer_rejected", result.Agent.Error)
	require.Equal(t, 1, result.ReadyCount)
	require.NotContains(t, result.NextAction, "retry shortly")
	require.False(t, result.TimedOut)
}
