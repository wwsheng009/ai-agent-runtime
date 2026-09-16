package skills

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// newAPISupervisionBatchStore builds a batch control plane for API-host tests.
func newAPISupervisionBatchStore(t *testing.T) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		DSN: "file:api-batch-progress-" + t.Name() + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// runningAPIBatch persists one active background batch with a single running
// child, i.e. exactly the state the P0-B rollup is supposed to surface.
func runningAPIBatch(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       3,
		CompletedCount:  1,
		RunningCount:    1,
		QueuedCount:     1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		StartedAt:       &now,
	}, []subagentbatch.SubagentTaskRecord{
		{
			TaskID:         "task_1",
			BatchID:        batchID,
			Status:         subagentbatch.TaskRunning,
			ChildSessionID: "child_session_1",
			StartedAt:      &now,
			UpdatedAt:      now,
		},
	})
	require.NoError(t, err)
}

// TestAPIInjectSupervisionPreflight_ProgressRollup 钉住 P0-B 的 API 侧对等能力：
// 宿主级共享 batch store 一旦接线，仍在运行的 batch 必须进入父 turn 的 progress
// 区块——而不是要等终态 lifecycle 行才第一次可见（CLI 宿主早已如此）。
func TestAPIInjectSupervisionPreflight_ProgressRollup(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-progress-rollup")
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	runningAPIBatch(t, handler.getSubagentBatchStore(), "batch_api_progress", "sess_progress")

	ctx := context.Background()
	prompt, err := handler.InjectSupervisionPreflight(ctx, "sess_progress", "USER PROMPT", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "progress:", "an active batch must reach the parent turn")
	require.Contains(t, prompt, "batch_api_progress: 1/3 completed")
	require.Contains(t, prompt, "1 running")
	require.Contains(t, prompt, "child_session_1")
	require.Contains(t, prompt, "USER PROMPT")
}

// TestAPIInjectSupervisionPreflight_ProgressIsScopedPerParent 守护 scope：别的父会话
// 的 batch 绝不能出现在本会话的注入内容里（progress 与 lifecycle 行同口径）。
func TestAPIInjectSupervisionPreflight_ProgressIsScopedPerParent(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-progress-scope")
	store := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(store)
	runningAPIBatch(t, store, "batch_other_parent", "sess_other")

	ctx := context.Background()
	prompt, err := handler.InjectSupervisionPreflight(ctx, "sess_mine", "USER PROMPT", nil)
	require.NoError(t, err)
	require.Equal(t, "USER PROMPT", prompt, "no content for this parent keeps the prompt byte-identical")
}

// TestNewAPIAgentSharesHostBatchStore 守护接线本身：API agent 必须使用宿主级共享
// store（否则每个 agent 各建一份一次性内存 store，progress 投影永远读不到数据），
// 且注入 coordinator 后仍然保留 agent 自身的 runtime-event emitter。
func TestNewAPIAgentSharesHostBatchStore(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	shared := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(shared)

	apiAgent := handler.newAPIAgent(&agent.Config{Name: "api-agent", Model: "test-model", MaxSteps: 3})
	require.NotNil(t, apiAgent)
	t.Cleanup(func() { _ = apiAgent.Close() })

	coordinator := apiAgent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	require.Same(t, shared, coordinator.Store(), "API agents must share the host batch store")
	require.True(t, coordinator.HasEmitter(), "injecting a shared coordinator must keep the agent emitter")
	require.True(t, apiAgent.SubagentBackgroundEnabled())
	require.Same(t, shared, handler.getSubagentBatchStore(), "the host store must not be replaced by agent-local state")
}

// TestAPISupervisionProgressSource_UnwiredHostStaysNil 守护"未接线宿主逐字节
// 不变"：没有可读 batch 控制面时必须返回 nil（BuildDigest 因而跳过 progress
// 区块），而不是造一个永远为空的投影。
func TestAPISupervisionProgressSource_UnwiredHostStaysNil(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	// 显式注销覆盖两类宿主：从未注入控制面的宿主，以及惰性建库失败的宿主
	// （见 getSubagentBatchStore 的 tried/nil 契约）。
	handler.SetSubagentBatchStore(nil)
	require.Nil(t, handler.supervisionProgressSource())

	var bare *Handler
	require.Nil(t, bare.supervisionProgressSource())
}
