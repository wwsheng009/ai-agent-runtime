package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const localWaitLedgerTestSession = "parent-session"

// newLocalWaitLedgerHost 组装 wait_agent 账本视图的最小宿主：真实 batch store 上的
// §6.12 挂起记录 + 指向该 turn 的派生缓存（RuntimeState.SuspendedTurnID）+ mailbox
// 等待所需的 EventStore。账本读数必须来自 durable 控制面，所以夹具用真 store。
func newLocalWaitLedgerHost(t *testing.T, batchStatus subagentbatch.BatchStatus, suspendedTurnID string) (*localChatRuntimeHost, string) {
	t.Helper()
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localWaitLedgerTestSession}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localWaitLedgerTestSession,
		ParentSessionID: localWaitLedgerTestSession,
		ParentTurnID:    suspendedTurnID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          batchStatus,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        suspendedTurnID,
		SessionID:     localWaitLedgerTestSession,
		RootScopeID:   localWaitLedgerTestSession,
		ObligationIDs: []string{batch.BatchID},
		ParkedAt:      now,
	}))
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:       localWaitLedgerTestSession,
		Status:          runtimechat.SessionIdle,
		SuspendedTurnID: suspendedTurnID,
	}))
	return host, batch.BatchID
}

// TestWaitAgentReturnsFinalizeWithoutBlockingOnTerminalLedger 钉住 AC-P2-4e：
// 账本非空且 obligation 全部终态 ⇒ 立即返回 finalize，不空等整个观测窗口。
func TestWaitAgentReturnsFinalizeWithoutBlockingOnTerminalLedger(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchCompleted, "turn-parked")
	registry := newLocalActorRegistry(host)

	startedAt := time.Now()
	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 30000})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.TimedOut, "an all-terminal ledger must not report a timeout")
	require.Equal(t, "finalize", result.NextAction)
	require.Len(t, result.Obligations, 1)
	require.Equal(t, batchID, result.Obligations[0].ObligationID)
	require.Equal(t, "batch", result.Obligations[0].SubjectKind)
	require.True(t, result.Obligations[0].Terminal)
	require.Equal(t, 1, result.TerminalCount)
	require.Empty(t, result.TerminalDelta, "nothing finished during a wait that never blocked")
	require.Less(t, time.Since(startedAt), 5*time.Second,
		"an all-terminal ledger must return immediately instead of blocking")
}

// TestLocalAgentSnapshotFallsBackToBatchTaskRow 钉住 2026-09-26 真机 E2E 缺口：
// 子代理会话（subagent_<task>_<uuid>）**不落 SessionStore**——身份与结果只在批任务
// 账本行上。wait_agent(task_id) 解析出该子会话 id 后若只看会话库，永远是
// missing/exists=false（父代理拿不到状态与输出）；必须回退到账本行投影，且终态
// 任务要落在 wait 的就绪集合里。
func TestLocalAgentSnapshotFallsBackToBatchTaskRow(t *testing.T) {
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localWaitLedgerTestSession}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.SessionStore = runtimechat.NewInMemoryStorage()
	registry := newLocalActorRegistry(host)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localWaitLedgerTestSession,
		ParentSessionID: localWaitLedgerTestSession,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	childSessionID := "subagent_t1_0123456789abcdef"
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{
		{
			TaskID:         "t1",
			BatchID:        batch.BatchID,
			OrderIndex:     1,
			UpdatedAt:      now,
			ChildSessionID: childSessionID,
			Status:         subagentbatch.TaskSucceeded,
		},
	})
	require.NoError(t, err)
	require.True(t, created)

	result, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{ID: childSessionID, TimeoutMs: 5000})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Agent)
	require.True(t, result.Agent.Exists, "账本行必须被投影成存在的主体，而不是 missing")
	require.Equal(t, string(runtimechat.SessionIdle), result.Agent.Status)
	require.Equal(t, "t1", result.Agent.CurrentTaskID)
	require.Equal(t, string(subagentbatch.TaskSucceeded), result.Agent.CurrentTaskStatus)
	// Output 走 task.ResultSummary（协调器 settle 时落账），本夹具只钉投影主体
	// 与状态；摘要读取路径由账本行字段直通，无需在断言里重复存储行为。
	require.False(t, result.TimedOut)
	require.Equal(t, 1, result.ReadyCount)
	require.Equal(t, []string{childSessionID}, result.ReadyIDs)
}

// TestWaitAgentKeepsPendingLedgerAndNeverFinalizes 钉住 I1 的拦截方向：还有非终态
// obligation 时等待照旧进行（不提前返回），且结果必须带账本、绝不给 finalize。
func TestWaitAgentKeepsPendingLedgerAndNeverFinalizes(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchRunning, "turn-parked")
	registry := newLocalActorRegistry(host)

	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 40})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.TimedOut, "a pending obligation keeps the legacy observation window")
	require.Len(t, result.Obligations, 1)
	require.Equal(t, batchID, result.Obligations[0].ObligationID)
	require.False(t, result.Obligations[0].Terminal)
	require.Equal(t, string(subagentbatch.BatchRunning), result.Obligations[0].State)
	require.NotEqual(t, "finalize", result.NextAction,
		"pending obligations must never let the parent finalize (I1)")
}

// TestWaitAgentWithoutParkedTurnKeepsLegacySemantics 钉住 fail-open：没有挂起 turn
// 时账本为空，等待结果不新增账本字段（plan §13.8：按"未挂起"处理）。
func TestWaitAgentWithoutParkedTurnKeepsLegacySemantics(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: localWaitLedgerTestSession}}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.EventStore = runtimechat.NewInMemoryRuntimeStore(16)
	registry := newLocalActorRegistry(host)

	result, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 40})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.TimedOut)
	require.Empty(t, result.Obligations)
	require.Equal(t, 0, result.TerminalCount)
}

// TestWaitAgentWaitBudgetEnforcesSuspendAfterNoProgress 钉住 §16.2/§16.3 的新判据：
// 连续 agents.maxConsecutiveWaitWithoutProgress 次无进展等待后，宿主不再打开新的
// 活动等待窗口，立即返回 next_action=suspend（账本视图照旧，I1 继续兜底收尾）。
func TestWaitAgentWaitBudgetEnforcesSuspendAfterNoProgress(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchRunning, "turn-parked")
	// 用 [30ms, 60ms] 的观测窗口把"等待段"压到测试可承受的尺度；预算仍取默认 2。
	host.RuntimeConfig = &runtimecfg.RuntimeConfig{Agents: runtimecfg.AgentsConfig{
		DefaultWaitTimeoutMs:              30,
		MinWaitTimeoutMs:                  30,
		MaxWaitTimeoutMs:                  60,
		MaxConsecutiveWaitWithoutProgress: 2,
	}}
	registry := newLocalActorRegistry(host)
	ctx := context.Background()

	// 第 1 次无进展等待：正常开窗（超时返回），预算计 1。
	first, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 30})
	require.NoError(t, err)
	require.False(t, first.WaitBudgetExhausted, "the first no-progress wait still opens a window")
	require.True(t, first.TimedOut)

	// 第 2 次无进展等待：达到上限，结果携带挂起判据（账本不丢）。
	second, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 30})
	require.NoError(t, err)
	require.True(t, second.WaitBudgetExhausted)
	require.Contains(t, second.NextAction, "suspend:")
	require.Len(t, second.Obligations, 1)
	require.Equal(t, batchID, second.Obligations[0].ObligationID)

	// 第 3 次：预算已耗尽 ⇒ 不再开窗，立即返回且不谎报超时。
	startedAt := time.Now()
	third, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{MailboxOnly: true, TimeoutMs: 30})
	require.NoError(t, err)
	require.True(t, third.WaitBudgetExhausted)
	require.False(t, third.TimedOut, "no observation window was opened, so nothing timed out")
	require.Contains(t, third.NextAction, "suspend:")
	require.Less(t, time.Since(startedAt), 500*time.Millisecond,
		"an exhausted wait budget must return immediately instead of blocking")
}
