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
