package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// newAPIAgentObligationFixture 组装轻量子代理义务链路的最小 API 宿主：durable
// batch store（挂起记录）+ durable supervision store（子会话终态判读）+ 运行态
// store（turn id 兜底）。与 CLI 侧 newLocalAgentObligationTestHost 同构，读数
// 全部来自宿主自己的控制面而不是打桩。
func newAPIAgentObligationFixture(t *testing.T) (*sessionAgentController, *chat.SessionManager) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)

	// 子会话 run 必须可控：阻塞 provider 让 queued spawn 的异步 run 停在 provider
	// 调用里，Spawn 返回时子会话确定处于 running（而不是因未配置 LLM 立刻失败）。
	provider := newSessionAgentBlockingProvider("api-agent-obligation-blocking-model")
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: provider.Name(),
		MaxRetries:   0,
	})
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(llmRuntime)

	// 挂起写入受 I9 的 IsDurable 门控：内存 DSN 的 batch store（测试常用夹具）
	// 永远不满足条件，所以这里用真实文件库，保证测试走的是生产挂起路径。
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "api-agent-obligations.db"),
	})
	require.NoError(t, err)
	require.True(t, batchStore.IsDurable(), "the park path requires a durable batch store")
	handler.SetSubagentBatchStore(batchStore)

	supervisionStore, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:api-agent-obligations-" + t.Name() + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	handler.SetSupervisionStore(supervisionStore)

	handler.sessionRuntimeStore = chat.NewInMemoryRuntimeStore(16)
	handler.sessionRuntimeStoreKey = agentRuntimeMemoryStoreKey

	// Cleanup 按 LIFO 执行（先注册的后跑）：先放行阻塞 run、停 hub，再停 session
	// manager，最后关两个 store——后台完成回调不会踩到已关闭的库。
	t.Cleanup(func() { _ = batchStore.Close() })
	t.Cleanup(func() { _ = supervisionStore.Close() })
	t.Cleanup(sessionManager.Stop)
	t.Cleanup(func() { handler.getSessionHub().StopAll() })
	t.Cleanup(provider.releaseCall)
	return &sessionAgentController{handler: handler}, sessionManager
}

// seedAPIAgentSessionRunningRow 写入一条非终态的 supervision lifecycle 行，
// 让判读器看到 found=true/terminal=false（而不是"没有行"的未知态）。
func seedAPIAgentSessionRunningRow(t *testing.T, controller *sessionAgentController, rootScopeID, childSessionID string) {
	t.Helper()
	_, err := supervision.ProjectLifecycle(context.Background(), controller.handler.getSupervisionStore(), nil, supervision.LifecycleProjection{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: rootScopeID,
		SubjectKind:           supervision.SubjectAgentSession,
		SubjectID:             childSessionID,
		EventType:             "agent.progress",
		SupervisionState:      supervision.SupervisionRunning,
	})
	require.NoError(t, err)
}

// TestAPISpawnParksQueuedChildObligation 钉住 API 宿主的「派发即挂起」：真实
// Spawn 在 queued（有 message、会真正运行）时必须把子会话登记为父 turn 的
// agent_session: 义务；turn id 在 broker ctx 无注解时从运行态 CurrentTurnID 兜底；
// 重复登记不产生重复义务；判读器缺失时只降级为未挂起（不写不可结算的记录）。
func TestAPISpawnParksQueuedChildObligation(t *testing.T) {
	ctx := context.Background()
	controller, sessionManager := newAPIAgentObligationFixture(t)
	parent, err := sessionManager.Create(ctx, "user-api-agent-obligation-park")
	require.NoError(t, err)
	require.NoError(t, controller.handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID:     parent.ID,
		Status:        chat.SessionRunning,
		CurrentTurnID: "turn-api-spawn-park",
	}))

	result, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{
		ID:      "api-obligation-child",
		Message: "hello child",
	})
	require.NoError(t, err)
	require.True(t, result.Queued, "a spawn with a prompt must be queued and therefore parked")

	store := controller.handler.getSubagentBatchStore()
	record, ok, err := store.GetTurnSuspension(ctx, parent.ID, "turn-api-spawn-park")
	require.NoError(t, err)
	require.True(t, ok, "a queued spawn_agent child must park the parent turn")
	require.Contains(t, record.ObligationIDs, subagentbatch.AgentSessionObligationID("api-obligation-child"))
	require.Equal(t, []string{"api-obligation-child"}, record.ObligationAgentSessionIDs())

	// 同一子会话重复登记（重放/竞态）不产生重复义务。
	controller.parkAgentChildObligation(ctx, parent.ID, "api-obligation-child")
	record, ok, err = store.GetTurnSuspension(ctx, parent.ID, "turn-api-spawn-park")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, record.ObligationIDs, 1)

	// 未装配 supervision store ⇒ 判读器为 nil ⇒ 不写不可结算的挂起记录（I9 保守方向）。
	controller.handler.SetSupervisionStore(nil)
	controller.parkAgentChildObligation(ctx, parent.ID, "api-obligation-child-2")
	record, ok, err = store.GetTurnSuspension(ctx, parent.ID, "turn-api-spawn-park")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotContains(t, record.ObligationIDs, subagentbatch.AgentSessionObligationID("api-obligation-child-2"))
}

// TestAPIAgentSessionObligationSettlesAfterTerminalProjection 钉住结算判读器：
// 未终态（found=true/terminal=false）子会话让 TurnObligationsSettledWith=false；
// ProjectAgentCompletion 写入 terminated lifecycle 行后才结清；nil 判读器下
// agent_session: 义务永不结清（保守方向）。
func TestAPIAgentSessionObligationSettlesAfterTerminalProjection(t *testing.T) {
	ctx := context.Background()
	controller, sessionManager := newAPIAgentObligationFixture(t)
	parent, err := sessionManager.Create(ctx, "user-api-agent-obligation-settle")
	require.NoError(t, err)
	child, err := sessionManager.Create(ctx, "user-api-agent-obligation-settle-child")
	require.NoError(t, err)

	store := controller.handler.getSubagentBatchStore()
	resolver := controller.handler.agentSessionObligationResolver()
	require.NotNil(t, resolver, "a wired supervision store must expose the child-session resolver")

	seedAPIAgentSessionRunningRow(t, controller, parent.ID, child.ID)

	record := &subagentbatch.TurnSuspension{
		TurnID:        "turn-api-settle",
		SessionID:     parent.ID,
		ObligationIDs: []string{subagentbatch.AgentSessionObligationID(child.ID)},
	}
	settled, err := subagentbatch.TurnObligationsSettledWith(ctx, store, record, resolver)
	require.NoError(t, err)
	require.False(t, settled, "a non-terminal child keeps the parked turn")

	controller.projectAgentCompletion(ctx, parent.ID, child.ID, "completed", "agent.completed")

	settled, err = subagentbatch.TurnObligationsSettledWith(ctx, store, record, resolver)
	require.NoError(t, err)
	require.True(t, settled, "a terminal lifecycle projection settles the parked turn")

	settled, err = subagentbatch.TurnObligationsSettledWith(ctx, store, record, nil)
	require.NoError(t, err)
	require.False(t, settled, "without a resolver an agent_session obligation never settles")
}

// TestAPIWaitLedgerIncludesAgentSessionObligation 钉住 wait 账本：挂起记录里的
// agent_session: 义务在 API 宿主同样以 agent_session 行出现，非终态保持 pending
// （I1 不给 finalize），子会话终态投影后转为 terminal/closed。
func TestAPIWaitLedgerIncludesAgentSessionObligation(t *testing.T) {
	ctx := context.Background()
	controller, sessionManager := newAPIAgentObligationFixture(t)
	parent, err := sessionManager.Create(ctx, "user-api-agent-obligation-ledger")
	require.NoError(t, err)
	child, err := sessionManager.Create(ctx, "user-api-agent-obligation-ledger-child")
	require.NoError(t, err)

	store := controller.handler.getSubagentBatchStore()
	require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-api-ledger",
		SessionID:     parent.ID,
		RootScopeID:   parent.ID,
		ObligationIDs: []string{subagentbatch.AgentSessionObligationID(child.ID)},
		ParkedAt:      time.Now().UTC(),
	}))
	require.NoError(t, controller.handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID:       parent.ID,
		Status:          chat.SessionIdle,
		SuspendedTurnID: "turn-api-ledger",
	}))
	seedAPIAgentSessionRunningRow(t, controller, parent.ID, child.ID)

	waitCtx := toolctx.WithSessionID(ctx, parent.ID)
	obligations, baseline, pending, key := controller.waitLedger(waitCtx)
	require.True(t, pending, "a live child keeps the ledger pending (I1)")
	require.Equal(t, parent.ID+"|turn-api-ledger", key)
	require.Empty(t, baseline, "a running child is not part of the terminal baseline")
	require.Len(t, obligations, 1)
	require.Equal(t, subagentbatch.AgentSessionObligationID(child.ID), obligations[0].ObligationID)
	require.Equal(t, "agent_session", obligations[0].SubjectKind)
	require.Equal(t, child.ID, obligations[0].SubjectID)
	require.False(t, obligations[0].Terminal)
	require.Equal(t, "active", obligations[0].State)

	controller.projectAgentCompletion(ctx, parent.ID, child.ID, "completed", "agent.completed")

	obligations, baseline, pending, _ = controller.waitLedger(waitCtx)
	require.False(t, pending, "a terminal child drains the ledger")
	require.Equal(t, []string{subagentbatch.AgentSessionObligationID(child.ID)}, baseline)
	require.Len(t, obligations, 1)
	require.True(t, obligations[0].Terminal)
	require.Equal(t, "closed", obligations[0].State)
}

// TestAPIProjectAgentCompletionWakesSettledParkedTurn 钉住 API 宿主的结算缺边
// 镜像：成功（info/closed）终态不排生命周期 wake，所以完成桥必须在 durable
// 账本显示"挂起 turn 的每个义务都已知终态"时排一笔结算 wake；只结清一部分时
// 必须保持停放（运行中的子会话没有生命周期行，不能当"已终态"跳过）。
func TestAPIProjectAgentCompletionWakesSettledParkedTurn(t *testing.T) {
	ctx := context.Background()
	controller, sessionManager := newAPIAgentObligationFixture(t)
	parent, err := sessionManager.Create(ctx, "user-api-settled-wake")
	require.NoError(t, err)
	childA, err := sessionManager.Create(ctx, "user-api-settled-wake-a")
	require.NoError(t, err)
	childB, err := sessionManager.Create(ctx, "user-api-settled-wake-b")
	require.NoError(t, err)

	handler := controller.handler
	scheduler := supervision.NewWakeScheduler(handler.getSupervisionStore(), supervision.WakeSchedulerConfig{})
	handler.SetSupervisionWakeScheduler(scheduler)
	// 与既有 API wake 测试同做法：投递被计数而不是真起 turn。Runnable 先钉成
	// false，让结算 wake 保持 durable、可观察；再翻成 true 走同一次排空。
	var deliveries int
	handler.supervisionWakeMu.Lock()
	handler.supervisionWake = &supervision.WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return false },
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries++
			return nil
		},
	}
	handler.supervisionWakeMu.Unlock()

	parkCtx := agent.WithTurnID(ctx, "turn-api-settled")
	controller.parkAgentChildObligation(parkCtx, parent.ID, childA.ID)
	controller.parkAgentChildObligation(parkCtx, parent.ID, childB.ID)
	record, ok, err := handler.getSubagentBatchStore().GetTurnSuspension(ctx, parent.ID, "turn-api-settled")
	require.NoError(t, err)
	require.True(t, ok, "queued spawn children must park the API turn")
	require.Len(t, record.ObligationIDs, 2)

	controller.projectAgentCompletion(ctx, parent.ID, childA.ID, "completed", "agent.completed")

	pending, err := handler.getSupervisionStore().ListWakePending(ctx, supervision.WakeFilter{
		TargetParentSessionID: parent.ID, UnclaimedOnly: true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "一笔终态不能让仍欠第二个子会话的 turn 提前醒来")

	controller.projectAgentCompletion(ctx, parent.ID, childB.ID, "completed", "agent.completed")

	pending, err = handler.getSupervisionStore().ListWakePending(ctx, supervision.WakeFilter{
		TargetParentSessionID: parent.ID, UnclaimedOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, supervision.WakeReasonObligationSettled, pending[0].WakeReason)
	require.Equal(t, "turn-api-settled", pending[0].TurnID)
	require.Equal(t, 0, deliveries, "父会话 runnable 之前结算 wake 必须保持 durable")

	handler.supervisionWakeMu.Lock()
	handler.supervisionWake.Runnable = func(context.Context, string, string, string) bool { return true }
	handler.supervisionWakeMu.Unlock()
	require.NoError(t, controller.wakeSupervisedParent(ctx, parent.ID, parent.ID))
	require.Equal(t, 1, deliveries, "下一次 runnable 迁移必须恰好投递一次结算恢复")
}
