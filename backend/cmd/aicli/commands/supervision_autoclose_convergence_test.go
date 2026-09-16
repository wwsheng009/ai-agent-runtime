package commands

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// recordingActionExecutor 记录控制面真正下发的动作，并把 ready 状态作为
// ExecutorReadiness 的可控开关——P1-C 的两条契约（"真的走 audit 动作路径"与
// "执行器未装配就不投影悬空建议"）都靠它断言。
type recordingActionExecutor struct {
	mu      sync.Mutex
	ready   bool
	actions []supervision.ActionRecord
}

func (e *recordingActionExecutor) ExecutorReady() bool {
	return e != nil && e.ready
}

func (e *recordingActionExecutor) Execute(_ context.Context, action supervision.ActionRecord) (supervision.ActionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.actions = append(e.actions, action)
	return supervision.ActionResult{Status: supervision.ActionCompleted, Result: "closed"}, nil
}

func (e *recordingActionExecutor) snapshot() []supervision.ActionRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]supervision.ActionRecord, len(e.actions))
	copy(out, e.actions)
	return out
}

// newAutoCloseTestHost 装配 P1-C 收敛钩子所需的全部依赖：监督控制面
//（含 durable store 与 wake 调度）、subagent batch store，以及一个就绪的
// 动作执行器。policy 即 agents.autoCloseCompleted。
func newAutoCloseTestHost(t *testing.T, policy string) (*localChatRuntimeHost, *recordingActionExecutor, subagentbatch.BatchStore) {
	t.Helper()
	host := newLocalSupervisionTestHost(t)
	host.RuntimeConfig = &runtimecfg.RuntimeConfig{
		Agents: runtimecfg.AgentsConfig{AutoCloseCompleted: policy},
	}
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "subagent_batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	host.SubagentBatches = store

	executor := &recordingActionExecutor{ready: true}
	host.Supervision.SetActionExecutor(executor)
	return host, executor, store
}

// seedBatchWithTasks 写入一个终态 batch 及其任务行；child session id 统一为
// "child-<taskID>"，便于断言"只 close 成功的那个子会话"。
func seedBatchWithTasks(t *testing.T, store subagentbatch.BatchStore, batchID string, status subagentbatch.BatchStatus, taskStatus map[string]subagentbatch.TaskStatus) {
	t.Helper()
	ctx := context.Background()
	now := subagentbatch.Now()
	tasks := make([]subagentbatch.SubagentTaskRecord, 0, len(taskStatus))
	order := 0
	for taskID, taskState := range taskStatus {
		order++
		tasks = append(tasks, subagentbatch.SubagentTaskRecord{
			TaskID:         taskID,
			BatchID:        batchID,
			ChildSessionID: "child-" + taskID,
			Role:           "writer",
			Difficulty:     "easy",
			Status:         taskState,
			OrderIndex:     order,
			Spec:           []byte(`{"id":"` + taskID + `"}`),
			UpdatedAt:      now,
			Version:        1,
		})
	}
	batch := &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ParentTurnID:    "turn-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          status,
		TaskCount:       len(tasks),
		CompletedCount:  len(tasks),
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created)
}

// convergenceNotifications 只返回 P1-C 收敛钩子投影的行（事件类型为
// agent_close_recommended），避免与其它 lifecycle 行混在一起断言。
func convergenceNotifications(t *testing.T, host *localChatRuntimeHost) []supervision.Notification {
	t.Helper()
	rows, err := host.Supervision.Store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentSession,
		IncludeResolved:       true,
		Limit:                 20,
	})
	require.NoError(t, err)
	out := make([]supervision.Notification, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(row.EventType, localBatchConvergenceEventType) {
			out = append(out, row)
		}
	}
	return out
}

func autoCloseTerminal(eventStatus subagentbatch.BatchStatus, subjectVersion int64) agent.BatchTerminalLifecycle {
	return agent.BatchTerminalLifecycle{
		BatchID:         "batch-1",
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          eventStatus,
		SubjectVersion:  subjectVersion,
	}
}

// TestLocalConvergeTerminalBatchChildrenClosesOnlySucceededChild 钉住 P1-C 的
// 主路径：干净完成的 batch 只收敛「任务成功」的子会话，且关闭动作真的经过
// 控制面 action audit（不是只投影一条建议）。
func TestLocalConvergeTerminalBatchChildrenClosesOnlySucceededChild(t *testing.T) {
	host, executor, store := newAutoCloseTestHost(t, runtimecfg.AutoClosePolicyCompleted)
	seedBatchWithTasks(t, store, "batch-1", subagentbatch.BatchCompleted, map[string]subagentbatch.TaskStatus{
		"ok":  subagentbatch.TaskSucceeded,
		"bad": subagentbatch.TaskFailed,
	})

	localConvergeTerminalBatchChildren(context.Background(), host, autoCloseTerminal(subagentbatch.BatchCompleted, 4))

	notifications := convergenceNotifications(t, host)
	require.Len(t, notifications, 1, "only the succeeded child gets a convergence row")
	require.Equal(t, "child-ok", notifications[0].SubjectID)
	require.Equal(t, string(supervision.ActionClose), notifications[0].RecommendedAction)
	// 收敛行被控制面真正执行掉：resolution receipt 落地后它不再要求父 agent
	// 动作，也不会在 digest 里持续告警。
	require.Equal(t, supervision.ResolutionClosed, notifications[0].ResolutionState)
	require.False(t, notifications[0].ActionRequired(), "auto-close must settle the row instead of leaving it dangling")

	actions := executor.snapshot()
	require.Len(t, actions, 1, "the convergence row must be executed, not only projected")
	require.Equal(t, supervision.ActionClose, actions[0].Action)
	require.Equal(t, "child-ok", actions[0].TargetID)
	require.Contains(t, actions[0].Reason, "batch-1")
	require.Contains(t, actions[0].Reason, runtimecfg.AutoClosePolicyCompleted)

	// 幂等：重放（startup recovery + 实时投影）不得重复关闭或重复写 audit。
	localConvergeTerminalBatchChildren(context.Background(), host, autoCloseTerminal(subagentbatch.BatchCompleted, 4))
	require.Len(t, convergenceNotifications(t, host), 1)
	require.Len(t, executor.snapshot(), 1)
}

// TestLocalConvergeTerminalBatchChildrenPolicyMatrix 钉住两档策略的差别：
// completed 只收敛干净完成的批次；batch_terminal 在部分失败时也收敛，但同样
// 只关闭成功的子会话。
func TestLocalConvergeTerminalBatchChildrenPolicyMatrix(t *testing.T) {
	t.Run("completed skips partial failure", func(t *testing.T) {
		host, executor, store := newAutoCloseTestHost(t, runtimecfg.AutoClosePolicyCompleted)
		seedBatchWithTasks(t, store, "batch-1", subagentbatch.BatchPartiallyCompleted, map[string]subagentbatch.TaskStatus{
			"ok":  subagentbatch.TaskSucceeded,
			"bad": subagentbatch.TaskFailed,
		})
		localConvergeTerminalBatchChildren(context.Background(), host, autoCloseTerminal(subagentbatch.BatchPartiallyCompleted, 2))
		require.Empty(t, convergenceNotifications(t, host))
		require.Empty(t, executor.snapshot())
	})

	t.Run("batch_terminal converges partial failure but leaves the failed child", func(t *testing.T) {
		host, executor, store := newAutoCloseTestHost(t, runtimecfg.AutoClosePolicyBatchTerminal)
		seedBatchWithTasks(t, store, "batch-1", subagentbatch.BatchPartiallyCompleted, map[string]subagentbatch.TaskStatus{
			"ok":  subagentbatch.TaskSucceeded,
			"bad": subagentbatch.TaskFailed,
		})
		localConvergeTerminalBatchChildren(context.Background(), host, autoCloseTerminal(subagentbatch.BatchPartiallyCompleted, 2))
		notifications := convergenceNotifications(t, host)
		require.Len(t, notifications, 1)
		require.Equal(t, "child-ok", notifications[0].SubjectID)
		require.Len(t, executor.snapshot(), 1)
		require.Equal(t, "child-ok", executor.snapshot()[0].TargetID)
	})
}

// TestLocalConvergeTerminalBatchChildrenStaysInertWhenDisabledOrUnwired 钉住
// "默认 off 逐字节不变"与"执行器未装配不投影悬空建议"两条降级契约。
func TestLocalConvergeTerminalBatchChildrenStaysInertWhenDisabledOrUnwired(t *testing.T) {
	t.Run("off policy is a no-op", func(t *testing.T) {
		host, executor, store := newAutoCloseTestHost(t, runtimecfg.AutoClosePolicyOff)
		seedBatchWithTasks(t, store, "batch-1", subagentbatch.BatchCompleted, map[string]subagentbatch.TaskStatus{
			"ok": subagentbatch.TaskSucceeded,
		})
		localConvergeTerminalBatchChildren(context.Background(), host, autoCloseTerminal(subagentbatch.BatchCompleted, 1))
		require.Empty(t, convergenceNotifications(t, host))
		require.Empty(t, executor.snapshot())
	})

	t.Run("missing executor projects nothing", func(t *testing.T) {
		host, executor, store := newAutoCloseTestHost(t, runtimecfg.AutoClosePolicyBatchTerminal)
		executor.ready = false
		seedBatchWithTasks(t, store, "batch-1", subagentbatch.BatchCompleted, map[string]subagentbatch.TaskStatus{
			"ok": subagentbatch.TaskSucceeded,
		})
		localConvergeTerminalBatchChildren(context.Background(), host, autoCloseTerminal(subagentbatch.BatchCompleted, 1))
		require.Empty(t, convergenceNotifications(t, host), "a dangling close suggestion is worse than none")
		require.Empty(t, executor.snapshot())
	})
}
