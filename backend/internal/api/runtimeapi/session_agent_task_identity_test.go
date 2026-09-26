package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	chat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

const apiTaskIdentityParent = "api-task-identity-parent"

// newAPITaskIdentityFixture 组装 G4/H7 解析侧的最小 API 宿主：父会话自己的 durable
// 批任务行（可选是否已绑定 child_session_id）。
func newAPITaskIdentityFixture(t *testing.T, withBinding bool) (*sessionAgentController, string) {
	t.Helper()
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))

	now := time.Now().UTC()
	task := subagentbatch.SubagentTaskRecord{
		TaskID:     "t1",
		Status:     subagentbatch.TaskRunning,
		OrderIndex: 1,
		UpdatedAt:  now,
		Version:    1,
	}
	if withBinding {
		task.ChildSessionID = "child-live-1"
	}
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     apiTaskIdentityParent,
		ParentSessionID: apiTaskIdentityParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := handler.getSubagentBatchStore().CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{task})
	require.NoError(t, err)
	require.True(t, created)
	return &sessionAgentController{handler: handler}, batch.BatchID
}

// TestResolveTargetSessionIDMapsTaskToChildSession 钉住 G4/H7 的解析入口：派发回执
// 只给 task id，运行期必须能把它解析成子会话，`wait_agent(task_id)` /
// `read_agent_events(task_id)` 才不会报 missing / 0 events。
func TestResolveTargetSessionIDMapsTaskToChildSession(t *testing.T) {
	controller, _ := newAPITaskIdentityFixture(t, true)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	resolved, err := controller.resolveTargetSessionID(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, "child-live-1", resolved)
}

// TestResolveTargetSessionIDReportsUnboundTask 钉住降级语义：任务已派发但子会话身份
// 尚未绑定 ⇒ 可执行错误（"身份未就绪"），而不是被当成"未知会话"静默走到 missing。
func TestResolveTargetSessionIDReportsUnboundTask(t *testing.T) {
	controller, _ := newAPITaskIdentityFixture(t, false)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	_, err := controller.resolveTargetSessionID(ctx, "t1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no child session bound yet")
	require.Contains(t, err.Error(), "t1")
}

// TestResolveTargetSessionIDKeepsUnknownIDs 钉住不越权：既不是 task 也不是路径的裸 id
// 仍按既有语义原样返回（可能是会话 id）；跨父会话的 task id 不得被解析出来。
func TestResolveTargetSessionIDKeepsUnknownIDs(t *testing.T) {
	controller, _ := newAPITaskIdentityFixture(t, true)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	resolved, err := controller.resolveTargetSessionID(ctx, "some-session")
	require.NoError(t, err)
	require.Equal(t, "some-session", resolved)

	foreign := toolctx.WithSessionID(context.Background(), "another-parent")
	resolved, err = controller.resolveTargetSessionID(foreign, "t1")
	require.NoError(t, err)
	require.Equal(t, "t1", resolved, "a task id outside the caller's scope must not resolve to a foreign child session")
}

// 2026-09-26 真机 E2E（API 宿主对齐 CLI）：wait_agent(batch_id) 必须先把 batch id
// 展开成它早绑定 child_session_id 的 task 子会话，而不是报 target_not_found。
func TestExpandBatchWaitTargetsMapsBatchToTaskChildren(t *testing.T) {
	controller, batchID := newAPITaskIdentityFixture(t, true)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	expanded, err := controller.expandBatchWaitTargets(ctx, []string{batchID, "some-session"})
	require.NoError(t, err)
	require.Equal(t, []string{"child-live-1", "some-session"}, expanded)

	foreign := toolctx.WithSessionID(context.Background(), "another-parent")
	expanded, err = controller.expandBatchWaitTargets(foreign, []string{batchID})
	require.NoError(t, err)
	require.Equal(t, []string{batchID}, expanded,
		"a batch id outside the caller's scope must not expand to a foreign child session")
}

func TestWaitForAgentStatusResolvesBatchIDThroughTaskChildren(t *testing.T) {
	controller, _ := newAPITaskIdentityFixture(t, true)
	handler := controller.handler
	// 空会话库：批任务子会话不落 SessionStore，snapshot 才能走到账本回退分支。
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     apiTaskIdentityParent,
		ParentSessionID: apiTaskIdentityParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		TaskCount:       1,
		CompletedCount:  1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		Version:         1,
	}
	created, err := handler.getSubagentBatchStore().CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{{
		TaskID:         "task-batch-wait",
		BatchID:        batch.BatchID,
		ChildSessionID: "child-batch-wait",
		Status:         subagentbatch.TaskSucceeded,
		OrderIndex:     0,
		UpdatedAt:      now,
		Version:        1,
	}})
	require.NoError(t, err)
	require.True(t, created)

	result, err := controller.waitForAgentStatus(ctx, toolbroker.WaitAgentArgs{
		ID:        batch.BatchID,
		TimeoutMs: 2000,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, result.NextAction, "target_not_found")
	require.Equal(t, 1, result.ReadyCount)
	require.Len(t, result.Agents, 1)
	require.Equal(t, "child-batch-wait", result.Agents[0].SessionID)
	require.True(t, result.Agents[0].Exists)
}
