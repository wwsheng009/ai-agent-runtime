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

const (
	apiBatchSnapshotParent = "api-batch-snapshot-parent"
	apiBatchSnapshotTask   = "t-snapshot"
	apiBatchSnapshotChild  = "child-snapshot-1"
)

// newAPIBatchSnapshotFixture 组装 doc1 §7.9 剩余① 的最小宿主：一条已绑定子会话、
// 可通过生产写入口（RecordTaskResult）终结的批任务行。子会话不落 SessionStore
// （与生产一致），因此账本回退（batchTaskSnapshot）是这些状态行的唯一权威来源。
func newAPIBatchSnapshotFixture(t *testing.T) (*sessionAgentController, string) {
	t.Helper()
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	// 空会话库：批任务子会话不落 SessionStore，snapshot 才能走到账本回退分支。
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	now := time.Now().UTC()
	task := subagentbatch.SubagentTaskRecord{
		TaskID:         apiBatchSnapshotTask,
		Status:         subagentbatch.TaskRunning,
		ChildSessionID: apiBatchSnapshotChild,
		TaskType:       "verify",
		TaskSubject:    "核对账本回退",
		Attempt:        2,
		ReadOnly:       true,
		OrderIndex:     1,
		UpdatedAt:      now,
		Version:        1,
	}
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     apiBatchSnapshotParent,
		ParentSessionID: apiBatchSnapshotParent,
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

// finishAPIBatchSnapshotTask 走生产写入口落终态与结果胶囊（CreateBatch 有意不写
// result_json，账本里的结果只来自 RecordTaskResult）。
func finishAPIBatchSnapshotTask(t *testing.T, controller *sessionAgentController, batchID string, status subagentbatch.TaskStatus, summary string, withError bool) {
	t.Helper()
	ctx := context.Background()
	store := controller.handler.getSubagentBatchStore()
	task, err := store.GetTask(ctx, batchID, apiBatchSnapshotTask)
	require.NoError(t, err)
	require.NoError(t, store.RecordTaskResult(ctx, batchID, apiBatchSnapshotTask, task.Version, status, &subagentbatch.TaskResult{
		TaskID:  apiBatchSnapshotTask,
		Success: status == subagentbatch.TaskSucceeded,
		Summary: summary,
	}))
	if !withError {
		return
	}
	// 协调器在 RecordTaskResult 旁路写运维可见的错误列（与 supervision 读取器
	// 测试同口径），这里镜像同一写法。
	task, err = store.GetTask(ctx, batchID, apiBatchSnapshotTask)
	require.NoError(t, err)
	_, err = store.UpdateTask(ctx, batchID, apiBatchSnapshotTask, task.Version, func(task *subagentbatch.SubagentTaskRecord) {
		task.ErrorClass = "provider"
		task.ErrorCode = "rate_limited"
	})
	require.NoError(t, err)
}

// TestBatchTaskSnapshotMapsTerminalLedgerRow 钉住终态账本行的投影口径：
// 成功任务 → exists=true / idle / Output 含结果摘要；未绑定的 id 不伪装命中。
func TestBatchTaskSnapshotMapsTerminalLedgerRow(t *testing.T) {
	controller, batchID := newAPIBatchSnapshotFixture(t)
	finishAPIBatchSnapshotTask(t, controller, batchID, subagentbatch.TaskSucceeded, "账本回退输出", false)
	ctx := toolctx.WithSessionID(context.Background(), apiBatchSnapshotParent)

	snapshot, ok, err := controller.batchTaskSnapshot(ctx, apiBatchSnapshotChild)
	require.NoError(t, err)
	require.True(t, ok, "bound terminal task must project from the ledger")
	require.NotNil(t, snapshot)
	require.True(t, snapshot.Exists)
	require.Equal(t, apiBatchSnapshotChild, snapshot.ID)
	require.Equal(t, string(chat.SessionIdle), snapshot.Status)
	require.Contains(t, snapshot.Output, "账本回退输出", "结果胶囊必须投影到 Output")
	require.Equal(t, string(subagentbatch.TaskSucceeded), snapshot.CurrentTaskStatus)
	require.Equal(t, apiBatchSnapshotTask, snapshot.CurrentTaskID)
	require.Equal(t, 2, snapshot.Attempt)
	require.True(t, snapshot.ReadOnly)
	require.Empty(t, snapshot.Error)

	_, ok, err = controller.batchTaskSnapshot(ctx, "not-a-bound-child")
	require.NoError(t, err)
	require.False(t, ok, "an unbound child id must not fabricate a ledger row")
}

// TestBatchTaskSnapshotKeepsFailureEvidence 钉住失败证据不丢失：failed_with_result
// 映射停止态，仍保留可交付摘要；错误分类以 "class: code" 呈现给父代理。
func TestBatchTaskSnapshotKeepsFailureEvidence(t *testing.T) {
	controller, batchID := newAPIBatchSnapshotFixture(t)
	finishAPIBatchSnapshotTask(t, controller, batchID, subagentbatch.TaskFailedWithResult, "部分交付", true)
	ctx := toolctx.WithSessionID(context.Background(), apiBatchSnapshotParent)

	snapshot, ok, err := controller.batchTaskSnapshot(ctx, apiBatchSnapshotChild)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, snapshot)
	require.Equal(t, string(chat.SessionStopped), snapshot.Status)
	require.Contains(t, snapshot.Output, "部分交付")
	require.Equal(t, "provider: rate_limited", snapshot.Error)
}

// newAPIPreDispatchFailureFixture 组装 doc1 §7.10 新遗留① 的场景：批次在派发前
// 就失败（如 single-writer 策略拒绝），任务行终态但没有、也永远不会有
// child_session_id（与建批次时直接落终态行的生产形态一致）。
func newAPIPreDispatchFailureFixture(t *testing.T) (*sessionAgentController, string) {
	t.Helper()
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	now := time.Now().UTC()
	task := subagentbatch.SubagentTaskRecord{
		TaskID:      "t-predispatch",
		Status:      subagentbatch.TaskFailed,
		TaskType:    "implement",
		TaskSubject: "single-writer 被拒",
		ErrorClass:  "policy",
		ErrorCode:   "single_writer_rejected",
		OrderIndex:  1,
		UpdatedAt:   now,
		Version:     1,
	}
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     apiBatchSnapshotParent,
		ParentSessionID: apiBatchSnapshotParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		TaskCount:       1,
		FailedCount:     1,
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

// TestWaitAgentTaskIDProducesPreDispatchFailureObservation 钉住 doc1 §7.10 新遗留①：
// 派发前失败的终态任务（无子会话绑定）应当：
//  1. 解析层不再返回 "retry shortly" 硬错，而是按可寻址 id 透传；
//  2. wait_agent(task_id) 返回可读的失败观测（stopped + 错误分类 + 任务身份），
//     让父代理直接读到批次失败原因，而不是无意义重试。
func TestWaitAgentTaskIDProducesPreDispatchFailureObservation(t *testing.T) {
	controller, _ := newAPIPreDispatchFailureFixture(t)
	ctx := toolctx.WithSessionID(context.Background(), apiBatchSnapshotParent)

	resolved, err := controller.resolveTargetSessionID(ctx, "t-predispatch")
	require.NoError(t, err, "terminal unbound task must not surface an executable retry error")
	require.Equal(t, "t-predispatch", resolved)

	result, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{ID: "t-predispatch"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ReadyCount, "pre-dispatch failure is terminal, so wait must return it as ready")
	require.NotContains(t, result.NextAction, "retry shortly")
	require.Len(t, result.Agents, 1)
	observation := result.Agents[0]
	require.True(t, observation.Exists)
	require.Equal(t, "t-predispatch", observation.ID)
	require.Equal(t, string(chat.SessionStopped), observation.Status)
	require.Equal(t, string(subagentbatch.TaskFailed), observation.CurrentTaskStatus)
	require.Equal(t, "policy: single_writer_rejected", observation.Error)
}

// TestWaitAgentObligationsDirectFromTargetsWithoutParkedRecord 钉住 doc1 §7.9
// 剩余②：没有 §6.12 挂起记录时，wait_agent 仍按显式等待的目标直出 obligations[]
// （行形状与挂起路径同源），而不是只给 agent 投影；未知目标不得臆造行。
func TestWaitAgentObligationsDirectFromTargetsWithoutParkedRecord(t *testing.T) {
	controller, batchID := newAPIBatchSnapshotFixture(t)
	ctx := toolctx.WithSessionID(context.Background(), apiBatchSnapshotParent)

	result, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{ID: apiBatchSnapshotChild, TimeoutMs: 50})
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

	missing, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{ID: "not-a-bound-child", TimeoutMs: 50})
	require.NoError(t, err)
	require.NotNil(t, missing)
	require.Empty(t, missing.Obligations, "unknown targets must not fabricate ledger rows")
}

// TestWaitAgentDirectObligationsFinalizeWhenTargetTerminal 钉住直出账本与收尾门的
// 配合：目标批次已终态时，wait_agent 立即返回 finalize，且 obligations[] 带终态行
// （baseline 语义），不会留下"永远再等一次"的空转。
func TestWaitAgentDirectObligationsFinalizeWhenTargetTerminal(t *testing.T) {
	controller, batchID := newAPIBatchSnapshotFixture(t)
	finishAPIBatchSnapshotTask(t, controller, batchID, subagentbatch.TaskSucceeded, "账本回退输出", false)
	ctx := context.Background()
	store := controller.handler.getSubagentBatchStore()
	batch, err := store.GetBatch(ctx, batchID)
	require.NoError(t, err)
	_, err = store.UpdateBatch(ctx, batchID, batch.Version, func(b *subagentbatch.SubagentBatch) {
		b.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)

	waitCtx := toolctx.WithSessionID(ctx, apiBatchSnapshotParent)
	result, err := controller.Wait(waitCtx, toolbroker.WaitAgentArgs{ID: apiBatchSnapshotChild, TimeoutMs: 5000})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.TimedOut, "an all-terminal ledger must finalize immediately")
	require.Len(t, result.Obligations, 1)
	require.True(t, result.Obligations[0].Terminal)
	require.Equal(t, string(subagentbatch.BatchCompleted), result.Obligations[0].State)
	require.Equal(t, "finalize", result.NextAction)
}
