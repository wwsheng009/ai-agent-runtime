package commands

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// fakeCloseExecutor 记录宿主真正下发的关闭目标：P1-C 的验收标准是"开关开启
// 时确实调用关闭通道、关闭时零调用"，只有计数真实执行器的调用才钉得住。
type fakeCloseExecutor struct {
	mu     sync.Mutex
	closed []string
}

func (f *fakeCloseExecutor) Execute(_ context.Context, a supervision.ActionRecord) (supervision.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, a.TargetID)
	return supervision.ActionResult{Status: supervision.ActionCompleted, Result: "closed by fake executor"}, nil
}

func (f *fakeCloseExecutor) closedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

func newLocalBatchConvergeHost(t *testing.T, policy string, tasks []subagentbatch.SubagentTaskRecord) (*localChatRuntimeHost, *fakeCloseExecutor, string) {
	t.Helper()
	ctx := context.Background()
	// 授权钩子放行：本测试只验证收敛路径本身，root-scope 授权由 evaluator/
	// 授权器的单测覆盖。
	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{
		Authorize: func(context.Context, string, string, string, string, string) error { return nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })

	host := &localChatRuntimeHost{
		Supervision:       plane,
		supervisionConfig: supervision.DefaultConfig(),
		RuntimeConfig: &runtimecfg.RuntimeConfig{
			Agents: runtimecfg.AgentsConfig{AutoCloseCompleted: policy},
		},
	}
	host.SubagentBatches = newTestSubagentBatchStore(t)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     "root-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		FinishedAt:      &now,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	for i := range tasks {
		tasks[i].BatchID = batch.BatchID
		tasks[i].OrderIndex = i + 1
		if tasks[i].UpdatedAt.IsZero() {
			tasks[i].UpdatedAt = now
		}
		if tasks[i].Version == 0 {
			tasks[i].Version = 1
		}
	}
	batch.TaskCount = len(tasks)
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created, "batch fixture must be created")

	executor := &fakeCloseExecutor{}
	plane.Actions.SetExecutor(executor)
	return host, executor, batch.BatchID
}

func batchTerminalFor(batchID string) agent.BatchTerminalLifecycle {
	return agent.BatchTerminalLifecycle{
		BatchID:         batchID,
		RootScopeID:     "root-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		EventType:       "subagent.batch.completed",
		SubjectVersion:  1,
	}
}

func convergingBatchTasks() []subagentbatch.SubagentTaskRecord {
	return []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-1", ChildSessionID: "child-1", Status: subagentbatch.TaskSucceeded},
		{TaskID: "task-2", ChildSessionID: "child-2", Status: subagentbatch.TaskFailed},
		{TaskID: "task-3", Status: subagentbatch.TaskSucceeded},
		{TaskID: "task-4", ChildSessionID: "child-1", Status: subagentbatch.TaskSucceeded},
	}
}

func listConvergenceRows(t *testing.T, host *localChatRuntimeHost, childSessionID string) []supervision.Notification {
	t.Helper()
	rows, err := host.Supervision.Store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     "root-session",
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       childSessionID,
		IncludeResolved: true,
		Limit:           10,
	})
	require.NoError(t, err)
	out := make([]supervision.Notification, 0, len(rows))
	for _, row := range rows {
		// 只取收敛建议行：成功 close 之后控制面还会在同一个子会话上投影一条
		// action_close_resolution 回执行，它不属于"收敛建议"。
		if strings.EqualFold(row.EventType, localBatchConvergenceEventType) {
			out = append(out, row)
		}
	}
	return out
}

// TestLocalBatchConvergeHintRendersToolAndChildSessions 钉住 P1-C 的模型面契约：
// BatchDone 行是 resolution=closed，evaluator 只允许 inspect，因此收敛提示必须
// 指向 broker 工具 close_agent 并带上真实子会话 id，而不是让模型对终态行发
// control 动作。
func TestLocalBatchConvergeHintRendersToolAndChildSessions(t *testing.T) {
	ctx := context.Background()
	host, _, batchID := newLocalBatchConvergeHost(t, runtimecfg.AutoClosePolicyOff, convergingBatchTasks())

	hint := localBatchConvergeHint(ctx, host, batchID)
	require.Contains(t, hint, "close_agent")
	require.Contains(t, hint, "child-1")
	require.Contains(t, hint, "child-2")
	require.Equal(t, 1, strings.Count(hint, "child-1"), "child sessions are deduplicated")
	require.NotContains(t, hint, "task-3", "tasks without a child session must not leak task ids")

	require.Equal(t,
		"converge by closing the finished child sessions with close_agent",
		localBatchConvergeHint(ctx, nil, batchID),
		"an unwired host still gets the executable instruction")
	require.Equal(t,
		"converge by closing the finished child sessions with close_agent",
		localBatchConvergeHint(ctx, host, "   "),
		"a missing batch id must not produce a dangling hint")
}

func TestLocalConvergeTerminalBatchChildrenHonorsPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("off by default projects nothing and closes nothing", func(t *testing.T) {
		host, executor, batchID := newLocalBatchConvergeHost(t, runtimecfg.AutoClosePolicyOff, convergingBatchTasks())

		localConvergeTerminalBatchChildren(ctx, host, batchTerminalFor(batchID))

		require.Empty(t, executor.closedIDs(), "the default policy must keep host behavior unchanged")
		require.Empty(t, listConvergenceRows(t, host, "child-1"), "an off policy must not project convergence rows")
	})

	t.Run("batch_terminal closes succeeded children exactly once", func(t *testing.T) {
		host, executor, batchID := newLocalBatchConvergeHost(t, runtimecfg.AutoClosePolicyBatchTerminal, convergingBatchTasks())
		terminal := batchTerminalFor(batchID)

		localConvergeTerminalBatchChildren(ctx, host, terminal)
		require.Equal(t, []string{"child-1"}, executor.closedIDs(),
			"only the succeeded child with a session id is closed; failed children stay for the parent")

		rows := listConvergenceRows(t, host, "child-1")
		require.Len(t, rows, 1)
		require.Equal(t, localBatchConvergenceEventType, rows[0].EventType)
		require.Equal(t, supervision.ResolutionClosed, rows[0].ResolutionState,
			"a successful close resolves the source row, which is what makes replays idempotent")
		require.Empty(t, listConvergenceRows(t, host, "child-2"), "failed children get no close recommendation")

		// 重放（startup recovery + 实时投影）不得重复关闭、不得重复写 audit。
		localConvergeTerminalBatchChildren(ctx, host, terminal)
		require.Equal(t, []string{"child-1"}, executor.closedIDs())
	})

	t.Run("completed policy ignores failed batches", func(t *testing.T) {
		host, executor, batchID := newLocalBatchConvergeHost(t, runtimecfg.AutoClosePolicyCompleted, convergingBatchTasks())
		terminal := batchTerminalFor(batchID)
		terminal.Status = subagentbatch.BatchFailed

		localConvergeTerminalBatchChildren(ctx, host, terminal)

		require.Empty(t, executor.closedIDs(),
			"the completed policy only converges cleanly finished batches; failures stay visible")
	})

	t.Run("batch_terminal converges all terminal children", func(t *testing.T) {
		host, executor, batchID := newLocalBatchConvergeHost(t, runtimecfg.AutoClosePolicyBatchTerminal, convergingBatchTasks())
		terminal := batchTerminalFor(batchID)
		terminal.Status = subagentbatch.BatchFailed

		localConvergeTerminalBatchChildren(ctx, host, terminal)

		require.Equal(t, []string{"child-1"}, executor.closedIDs(),
			"the batch_terminal policy converges succeeded children even when the batch itself failed")
	})
}
