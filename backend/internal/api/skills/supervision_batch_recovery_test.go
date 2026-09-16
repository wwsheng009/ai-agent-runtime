package skills

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// seedAPIRunningBatch 写入一条非终态的后台 batch 行（queued/running），对应
// 「上一个宿主进程崩溃时留在 store 里、没有 worker 去物化它」的状态。
func seedAPIRunningBatch(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-1", BatchID: batchID, ChildSessionID: "child-restart-1", Status: subagentbatch.TaskRunning, UpdatedAt: now},
	}
	_, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       2,
		RunningCount:    1,
		OwnerID:         "dead-worker",
		HeartbeatAt:     now,
		UpdatedAt:       now,
	}, tasks)
	require.NoError(t, err)
}

// TestAPIBatchRecoveryConvergesAndReplaysDelivery 是本缺口的核心断言：启动恢复
// 必须把遗留的非终态 batch 收敛为终态，并把终态通知真正投递到父会话（API 宿主
// 此前既没有恢复入口，也没有 terminal sink）。
func TestAPIBatchRecoveryConvergesAndReplaysDelivery(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-batch-recovery")
	store := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(store)
	seedAPIRunningBatch(t, store, "batch_orphan_api", "sess_batch_recovery")

	ctx := context.Background()
	// grace=0：所有可恢复行都合格（等价于第二趟「宽限期已过」的那次扫描）。
	recovered, replay, err := handler.runAPISubagentBatchRecoveryOnce(ctx, store, 0, apiSubagentBatchRecoveryLimit)
	require.NoError(t, err)
	require.Equal(t, 1, recovered, "a batch left by a dead worker must converge to terminal")
	require.Zero(t, replay.Failed, "terminal mailbox delivery must not fail on the API host")
	require.GreaterOrEqual(t, replay.Delivered+replay.Duplicate, 1,
		"the terminal notification must reach the parent mailbox, not just the supervision store")

	batch, err := store.GetBatch(ctx, "batch_orphan_api")
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, subagentbatch.BatchOrphaned, batch.Status,
		"a batch with no live worker must be marked orphaned, not left running forever")

	// mailbox 消息 id 是跨进程幂等边界：第二趟不得再投递一条重复汇报。
	_, replayAgain, err := handler.runAPISubagentBatchRecoveryOnce(ctx, store, 0, apiSubagentBatchRecoveryLimit)
	require.NoError(t, err)
	require.Zero(t, replayAgain.Failed)
	require.Zero(t, replayAgain.Delivered, "replay must be idempotent per mailbox message id")
}

// TestAPIBatchRecoveryHonorsRestartGrace 守护宽限窗口：比宽限期更新的行可能属于
// 仍在运行的另一个宿主进程，第一趟必须放过它们；宽限期过后（grace=0 等价）才收敛。
func TestAPIBatchRecoveryHonorsRestartGrace(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-batch-grace")
	store := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(store)
	seedAPIRunningBatch(t, store, "batch_fresh_api", "sess_batch_grace")

	ctx := context.Background()
	recovered, _, err := handler.runAPISubagentBatchRecoveryOnce(ctx, store, apiSubagentBatchRestartGrace, apiSubagentBatchRecoveryLimit)
	require.NoError(t, err)
	require.Zero(t, recovered, "a fresh row must survive the first pass (its worker may still be alive)")

	batch, err := store.GetBatch(ctx, "batch_fresh_api")
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, subagentbatch.BatchRunning, batch.Status)

	recovered, _, err = handler.runAPISubagentBatchRecoveryOnce(ctx, store, 0, apiSubagentBatchRecoveryLimit)
	require.NoError(t, err)
	require.Equal(t, 1, recovered, "the delayed pass must catch rows that were too fresh at startup")
}

// TestAPIBatchRecoverySurvivesRestart 是「跨进程」断言：durable store 落盘后，新
// 进程（新 Handler）在同一个目录上必须能收敛上一个进程遗留的行，并完成终态投递。
func TestAPIBatchRecoverySurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	first, _ := newAPISupervisionBudgetTestHandler(t, "api-batch-restart-a")
	closer, err := first.EnableDurableSubagentBatches(dir)
	require.NoError(t, err)
	require.NotNil(t, closer)
	require.NotNil(t, first.injectedSubagentBatchStore(), "the durable store must be injected into the host")
	require.NotNil(t, first.subagentBatchRecoveryStop, "injecting a durable store must start the two-pass recovery")
	seedAPIRunningBatch(t, first.injectedSubagentBatchStore(), "batch_across_restart", "sess_across_restart")
	first.StopSubagentBatchRecovery()
	require.NoError(t, closer.Close())

	second, _ := newAPISupervisionBudgetTestHandler(t, "api-batch-restart-b")
	closerAgain, err := second.EnableDurableSubagentBatches(dir)
	require.NoError(t, err)
	require.NotNil(t, closerAgain)
	t.Cleanup(func() {
		second.StopSubagentBatchRecovery()
		require.NoError(t, closerAgain.Close())
	})

	// 第二趟（宽限期过后）的等价调用：恢复上一个进程遗留的行。
	recovered, replay, err := second.runAPISubagentBatchRecoveryOnce(
		context.Background(), second.injectedSubagentBatchStore(), 0, apiSubagentBatchRecoveryLimit)
	require.NoError(t, err)
	require.Equal(t, 1, recovered, "the durable store must be recoverable by the next process")
	require.Zero(t, replay.Failed)

	batch, err := second.injectedSubagentBatchStore().GetBatch(context.Background(), "batch_across_restart")
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, subagentbatch.BatchOrphaned, batch.Status)
}

// TestNewAPIAgentWiresBatchTerminalSink 守护接线本身（缺口就是「能力在包内、宿主
// 没装」）：API agent 的 coordinator 必须既保留 emitter，又装上 terminal sink。
func TestNewAPIAgentWiresBatchTerminalSink(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-batch-sink-wiring")
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))

	apiAgent := handler.newAPIAgent(&agent.Config{Name: "api-agent", Model: "test-model", MaxSteps: 3})
	require.NotNil(t, apiAgent)
	t.Cleanup(func() { _ = apiAgent.Close() })

	coordinator := apiAgent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	require.True(t, coordinator.HasEmitter(), "injecting a shared coordinator must keep the agent emitter")
	require.True(t, coordinator.HasTerminalSink(),
		"without a terminal sink the batch terminal never reaches the parent mailbox on the API host")
}

// TestStartSubagentBatchRecoveryNoopWithoutInjectedStore 守护未接线宿主：没有
// durable store 时恢复必须是 no-op，绝不能为了扫描而惰性建出 per-process 内存库。
func TestStartSubagentBatchRecoveryNoopWithoutInjectedStore(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	handler.StartSubagentBatchRecovery()

	require.Nil(t, handler.subagentBatchRecoveryStop, "no durable store means no recovery loop")
	require.Nil(t, handler.injectedSubagentBatchStore(), "recovery must not force-create the default store")
	handler.StopSubagentBatchRecovery() // 幂等：未启动时是 no-op
}
