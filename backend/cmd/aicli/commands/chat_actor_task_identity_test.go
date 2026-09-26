package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

const localTaskIdentityParent = "parent-task-identity"

// newLocalTaskIdentityRegistry 组装 G4/H7 解析侧的最小 CLI 宿主：父会话自己的
// durable 批任务行（可选是否已绑定 child_session_id）。
func newLocalTaskIdentityRegistry(t *testing.T, withBinding bool) *localActorRegistry {
	t.Helper()
	host := newLocalSupervisionTestHost(t)
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.BaseSession = &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: localTaskIdentityParent},
	}

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
		RootScopeID:     localTaskIdentityParent,
		ParentSessionID: localTaskIdentityParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(context.Background(), batch, []subagentbatch.SubagentTaskRecord{task})
	require.NoError(t, err)
	require.True(t, created)
	return &localActorRegistry{Host: host}
}

// TestResolveLocalAgentTargetSessionIDMapsTaskToChildSession 钉住 G4/H7 在 CLI 宿主
// 的解析入口：运行期 `wait_agent(task_id)` / `read_agent_events(task_id)` 必须能把
// 派发回执上的 task id 解析成子会话。
func TestResolveLocalAgentTargetSessionIDMapsTaskToChildSession(t *testing.T) {
	registry := newLocalTaskIdentityRegistry(t, true)

	resolved, err := registry.resolveLocalAgentTargetSessionID(context.Background(), "t1")
	require.NoError(t, err)
	require.Equal(t, "child-live-1", resolved)
}

// TestResolveLocalAgentTargetSessionIDReportsUnboundTask 钉住降级语义：任务已派发但子
// 会话身份未就绪 ⇒ 可执行错误（而不是静默落到 missing）。
func TestResolveLocalAgentTargetSessionIDReportsUnboundTask(t *testing.T) {
	registry := newLocalTaskIdentityRegistry(t, false)

	_, err := registry.resolveLocalAgentTargetSessionID(context.Background(), "t1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no child session bound yet")
}

// TestResolveLocalAgentTargetSessionIDKeepsPlainIDs 钉住不越权与既有语义：裸 id 原样
// 返回（可能是会话 id），且父会话之外的 task id 不会被解析。
func TestResolveLocalAgentTargetSessionIDKeepsPlainIDs(t *testing.T) {
	registry := newLocalTaskIdentityRegistry(t, true)
	ctx := context.Background()

	resolved, err := registry.resolveLocalAgentTargetSessionID(ctx, "plain-session")
	require.NoError(t, err)
	require.Equal(t, "plain-session", resolved)

	registry.Host.BaseSession.RuntimeSession.ID = "another-parent"
	resolved, err = registry.resolveLocalAgentTargetSessionID(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, "t1", resolved, "a task id outside the caller's scope must not resolve to a foreign child session")
}
