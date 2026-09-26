package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

const apiTaskIdentityParent = "api-task-identity-parent"

// newAPITaskIdentityFixture 组装 G4/H7 解析侧的最小 API 宿主：父会话自己的 durable
// 批任务行（可选是否已绑定 child_session_id）。
func newAPITaskIdentityFixture(t *testing.T, withBinding bool) *sessionAgentController {
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
	return &sessionAgentController{handler: handler}
}

// TestResolveTargetSessionIDMapsTaskToChildSession 钉住 G4/H7 的解析入口：派发回执
// 只给 task id，运行期必须能把它解析成子会话，`wait_agent(task_id)` /
// `read_agent_events(task_id)` 才不会报 missing / 0 events。
func TestResolveTargetSessionIDMapsTaskToChildSession(t *testing.T) {
	controller := newAPITaskIdentityFixture(t, true)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	resolved, err := controller.resolveTargetSessionID(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, "child-live-1", resolved)
}

// TestResolveTargetSessionIDReportsUnboundTask 钉住降级语义：任务已派发但子会话身份
// 尚未绑定 ⇒ 可执行错误（"身份未就绪"），而不是被当成"未知会话"静默走到 missing。
func TestResolveTargetSessionIDReportsUnboundTask(t *testing.T) {
	controller := newAPITaskIdentityFixture(t, false)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	_, err := controller.resolveTargetSessionID(ctx, "t1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no child session bound yet")
	require.Contains(t, err.Error(), "t1")
}

// TestResolveTargetSessionIDKeepsUnknownIDs 钉住不越权：既不是 task 也不是路径的裸 id
// 仍按既有语义原样返回（可能是会话 id）；跨父会话的 task id 不得被解析出来。
func TestResolveTargetSessionIDKeepsUnknownIDs(t *testing.T) {
	controller := newAPITaskIdentityFixture(t, true)
	ctx := toolctx.WithSessionID(context.Background(), apiTaskIdentityParent)

	resolved, err := controller.resolveTargetSessionID(ctx, "some-session")
	require.NoError(t, err)
	require.Equal(t, "some-session", resolved)

	foreign := toolctx.WithSessionID(context.Background(), "another-parent")
	resolved, err = controller.resolveTargetSessionID(foreign, "t1")
	require.NoError(t, err)
	require.Equal(t, "t1", resolved, "a task id outside the caller's scope must not resolve to a foreign child session")
}
