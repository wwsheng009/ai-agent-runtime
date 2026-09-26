package runtimeapi

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// apiAutoCloseTestScope 是 P1-C 测试统一使用的父会话/根作用域。
const apiAutoCloseTestScope = "api-autoclose-parent"

// apiRecordingActionExecutor 记录控制面真正下发的动作，并把 ready 作为
// ExecutorReadiness 的可控开关：P1-C 的两条契约（「真的走 audit 动作路径」与
// 「执行器未装配就不投影悬空建议」）都靠它断言。
type apiRecordingActionExecutor struct {
	mu      sync.Mutex
	ready   bool
	actions []supervision.ActionRecord
}

func (e *apiRecordingActionExecutor) ExecutorReady() bool {
	return e != nil && e.ready
}

func (e *apiRecordingActionExecutor) Execute(_ context.Context, action supervision.ActionRecord) (supervision.ActionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.actions = append(e.actions, action)
	return supervision.ActionResult{Status: supervision.ActionCompleted, Result: "closed"}, nil
}

func (e *apiRecordingActionExecutor) snapshot() []supervision.ActionRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]supervision.ActionRecord, len(e.actions))
	copy(out, e.actions)
	return out
}

// newAPIAutoCloseTestHandler 装配 P1-C 收敛钩子所需的全部依赖：监督控制面
//（durable store + wake 调度 + 就绪的动作执行器）、共享 batch store，以及可选的
// agents.autoCloseCompleted 策略。
func newAPIAutoCloseTestHandler(t *testing.T, policy string) (*Handler, *apiRecordingActionExecutor, subagentbatch.BatchStore) {
	t.Helper()
	name := "api-autoclose-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	handler, store := newAPISupervisionBudgetTestHandler(t, name)

	executor := &apiRecordingActionExecutor{ready: true}
	handler.SetSupervisionActionService(supervision.NewActionService(store, executor, nil))

	batches := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(batches)
	if policy != "" {
		// 与 CLI 测试宿主同口径：直接装 runtime config 快照（读侧会做 Normalize）。
		handler.runtimeConfig = &runtimecfg.RuntimeConfig{
			Agents: runtimecfg.AgentsConfig{AutoCloseCompleted: policy},
		}
	}
	return handler, executor, batches
}

// seedAPITerminalBatch 写入一个终态 batch 及其任务行：task-1 成功（child-1）、
// task-2 失败（child-2）、task-3 成功但没有子会话 id。
func seedAPITerminalBatch(t *testing.T, store subagentbatch.BatchStore, batchID string, status subagentbatch.BatchStatus) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-1", BatchID: batchID, ChildSessionID: "child-1", Status: subagentbatch.TaskSucceeded, UpdatedAt: now},
		{TaskID: "task-2", BatchID: batchID, ChildSessionID: "child-2", Status: subagentbatch.TaskFailed, UpdatedAt: now},
		{TaskID: "task-3", BatchID: batchID, Status: subagentbatch.TaskSucceeded, UpdatedAt: now},
	}
	created, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     apiAutoCloseTestScope,
		ParentSessionID: apiAutoCloseTestScope,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          status,
		TaskCount:       len(tasks),
		CompletedCount:  2,
		FailedCount:     1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, tasks)
	require.NoError(t, err)
	require.True(t, created)
}

func apiBatchTerminalFor(batchID string, status subagentbatch.BatchStatus) agent.BatchTerminalLifecycle {
	return agent.BatchTerminalLifecycle{
		BatchID:         batchID,
		RootScopeID:     apiAutoCloseTestScope,
		ParentSessionID: apiAutoCloseTestScope,
		Status:          status,
		EventType:       "subagent.batch.done",
		SubjectVersion:  3,
		TaskCount:       3,
		CompletedCount:  2,
		FailedCount:     1,
	}
}

// apiConvergenceNotifications 只返回 P1-C 收敛钩子投影的行（事件类型为
// agent_close_recommended），避免与批次级 lifecycle 行混在一起断言。
func apiConvergenceNotifications(t *testing.T, store supervision.Store, childSessionID string) []supervision.Notification {
	t.Helper()
	rows, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     apiAutoCloseTestScope,
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       childSessionID,
		IncludeResolved: true,
		Limit:           10,
	})
	require.NoError(t, err)
	out := make([]supervision.Notification, 0, len(rows))
	for _, row := range rows {
		if row.EventType == apiBatchConvergenceEventType {
			out = append(out, row)
		}
	}
	return out
}

// TestAPIAutoCloseCompletedPolicy 钉住 P1-C 的降级口径：解析
// agents.autoCloseCompleted 时，任何「读不到 / 读不懂」都必须回落 off，绝不能把
// 拼写错误或未装配解读成「自动关闭所有子会话」。
func TestAPIAutoCloseCompletedPolicy(t *testing.T) {
	var nilHandler *Handler
	require.Equal(t, runtimecfg.AutoClosePolicyOff, nilHandler.apiAutoCloseCompletedPolicy())
	require.Equal(t, runtimecfg.AutoClosePolicyOff,
		NewHandler(nil, nil, nil).apiAutoCloseCompletedPolicy(),
		"a host without runtime config must not invent a policy")

	for _, tc := range []struct {
		configured string
		want       string
	}{
		{"", runtimecfg.AutoClosePolicyOff},
		{"  ", runtimecfg.AutoClosePolicyOff},
		{runtimecfg.AutoClosePolicyOff, runtimecfg.AutoClosePolicyOff},
		{runtimecfg.AutoClosePolicyCompleted, runtimecfg.AutoClosePolicyCompleted},
		{"  BATCH_TERMINAL ", runtimecfg.AutoClosePolicyBatchTerminal},
		{"everything", runtimecfg.AutoClosePolicyOff},
	} {
		t.Run("configured="+tc.configured, func(t *testing.T) {
			handler := NewHandler(nil, nil, nil)
			handler.runtimeConfig = &runtimecfg.RuntimeConfig{
				Agents: runtimecfg.AgentsConfig{AutoCloseCompleted: tc.configured},
			}
			require.Equal(t, tc.want, handler.apiAutoCloseCompletedPolicy())
		})
	}
}

// TestAPIConvergeTerminalBatchChildrenHonorsPolicy 覆盖 P1-C 收敛钩子的四条契约。
func TestAPIConvergeTerminalBatchChildrenHonorsPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("off by default projects nothing and closes nothing", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyOff)
		seedAPITerminalBatch(t, batches, "batch_off", subagentbatch.BatchCompleted)

		handler.apiConvergeTerminalBatchChildren(ctx, apiBatchTerminalFor("batch_off", subagentbatch.BatchCompleted))

		require.Empty(t, executor.snapshot(), "the default policy must keep host behavior unchanged")
		require.Empty(t, apiConvergenceNotifications(t, handler.getSupervisionStore(), "child-1"),
			"an off policy must not project convergence rows")
	})

	t.Run("batch_terminal closes succeeded children exactly once", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyBatchTerminal)
		seedAPITerminalBatch(t, batches, "batch_terminal", subagentbatch.BatchCompleted)
		terminal := apiBatchTerminalFor("batch_terminal", subagentbatch.BatchCompleted)

		handler.apiConvergeTerminalBatchChildren(ctx, terminal)

		actions := executor.snapshot()
		require.Len(t, actions, 1, "only the succeeded child with a session id is closed")
		require.Equal(t, supervision.ActionClose, actions[0].Action)
		require.Equal(t, "child-1", actions[0].TargetID)
		require.Equal(t, apiAutoCloseTestScope, actions[0].RootScopeID)
		require.Contains(t, actions[0].Reason, "batch_terminal")
		require.Contains(t, actions[0].Reason, runtimecfg.AutoClosePolicyBatchTerminal)

		rows := apiConvergenceNotifications(t, handler.getSupervisionStore(), "child-1")
		require.Len(t, rows, 1)
		require.Equal(t, apiBatchConvergenceEventType, rows[0].EventType)
		require.Equal(t, supervision.ResolutionClosed, rows[0].ResolutionState,
			"a successful close resolves the source row, which is what makes replays idempotent")
		require.Empty(t, apiConvergenceNotifications(t, handler.getSupervisionStore(), "child-2"),
			"failed children get no close recommendation")

		// 重放（startup recovery + 实时投影）不得重复关闭、不得重复写 audit。
		handler.apiConvergeTerminalBatchChildren(ctx, terminal)
		require.Len(t, executor.snapshot(), 1)
	})

	t.Run("completed policy ignores failed batches", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyCompleted)
		seedAPITerminalBatch(t, batches, "batch_failed", subagentbatch.BatchFailed)

		handler.apiConvergeTerminalBatchChildren(ctx, apiBatchTerminalFor("batch_failed", subagentbatch.BatchFailed))

		require.Empty(t, executor.snapshot(),
			"the completed policy only converges cleanly finished batches; failures stay visible")
	})

	t.Run("batch_terminal converges succeeded children of failed batches", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyBatchTerminal)
		seedAPITerminalBatch(t, batches, "batch_partial", subagentbatch.BatchFailed)

		handler.apiConvergeTerminalBatchChildren(ctx, apiBatchTerminalFor("batch_partial", subagentbatch.BatchFailed))

		actions := executor.snapshot()
		require.Len(t, actions, 1,
			"the batch_terminal policy converges succeeded children even when the batch itself failed")
		require.Equal(t, "child-1", actions[0].TargetID)
	})

	t.Run("no executor means no dangling recommendation", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyBatchTerminal)
		executor.ready = false
		seedAPITerminalBatch(t, batches, "batch_no_executor", subagentbatch.BatchCompleted)

		handler.apiConvergeTerminalBatchChildren(ctx, apiBatchTerminalFor("batch_no_executor", subagentbatch.BatchCompleted))

		require.Empty(t, executor.snapshot())
		require.Empty(t, apiConvergenceNotifications(t, handler.getSupervisionStore(), "child-1"),
			"a dangling recommendation is worse than none")
	})
}

// TestAPIBatchLifecycleProjectorConvergesSucceededChildren 是接线断言：真实入口
//（apiBatchLifecycleProjector）在 batch_terminal 策略下必须同时落批次终态行与子
// 会话收敛动作，而 off 策略下逐字节保持仅投影行为。
func TestAPIBatchLifecycleProjectorConvergesSucceededChildren(t *testing.T) {
	ctx := context.Background()

	t.Run("batch_terminal closes through the projector", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyBatchTerminal)
		seedAPITerminalBatch(t, batches, "batch_projector", subagentbatch.BatchCompleted)

		require.NoError(t, handler.apiBatchLifecycleProjector()(ctx,
			apiBatchTerminalFor("batch_projector", subagentbatch.BatchCompleted)))

		require.Len(t, executor.snapshot(), 1)
		require.Equal(t, "child-1", executor.snapshot()[0].TargetID)
	})

	t.Run("off keeps projector-only behavior", func(t *testing.T) {
		handler, executor, batches := newAPIAutoCloseTestHandler(t, runtimecfg.AutoClosePolicyOff)
		seedAPITerminalBatch(t, batches, "batch_projector_off", subagentbatch.BatchCompleted)

		require.NoError(t, handler.apiBatchLifecycleProjector()(ctx,
			apiBatchTerminalFor("batch_projector_off", subagentbatch.BatchCompleted)))

		require.Empty(t, executor.snapshot())
		rows, err := handler.getSupervisionStore().ListNotifications(ctx, supervision.NotificationFilter{
			RootScopeID: apiAutoCloseTestScope,
			SubjectID:   "batch_projector_off",
			// 干净完成的批次行 resolution=closed，必须显式包含已解析行。
			IncludeResolved: true,
		})
		require.NoError(t, err)
		require.Len(t, rows, 1, "the batch terminal row itself must still be projected")
	})
}
