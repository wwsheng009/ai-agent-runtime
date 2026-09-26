package runtimeapi

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestAPIBatchLifecycleProjector_FailedBatchIsDurable 钉住 API 宿主对后台 batch
// 终态的对等能力（此前只有 CLI 有线）：失败 batch 必须落成 critical/unresolved
// 的生命周期通知，并留下未决 wake——即便父 actor 此刻不可运行（父忙/不存在），
// 投影也不能报错或把 wake 丢掉，否则 runtime-server 的父/leader agent 永远等不到
// "batch 结束"的汇报点。
func TestAPIBatchLifecycleProjector_FailedBatchIsDurable(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-batch-projector-failed")
	ctx := context.Background()

	err := handler.apiBatchLifecycleProjector()(ctx, agent.BatchTerminalLifecycle{
		BatchID:         "batch_api_failed",
		RootScopeID:     "sess_parent",
		ParentSessionID: "sess_parent",
		Status:          subagentbatch.BatchFailed,
		EventType:       "subagent.batch.failed",
		SubjectVersion:  3,
		TaskCount:       3,
		CompletedCount:  1,
		FailedCount:     2,
		Error:           "task task_2 failed",
	})
	require.NoError(t, err, "父不可运行是 durable 控制面的正常结果，投影本身不得报错")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID: "sess_parent",
		SubjectID:   "batch_api_failed",
	})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, supervision.SeverityCritical, notifications[0].Severity)
	require.Equal(t, supervision.ResolutionUnresolved, notifications[0].ResolutionState)
	require.Equal(t, supervision.SubjectAgentRun, notifications[0].SubjectKind)
	require.Contains(t, notifications[0].Reason, "batch_api_failed")
	require.Contains(t, notifications[0].Reason, "task task_2 failed")

	wakes, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "sess_parent",
		TargetParentSessionID: "sess_parent",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, wakes, "critical 终态的 wake 必须保持 durable 待投递")

	// 真正的价值断言：父会话下一轮 preflight 的 digest 必须带上这条行，否则
	// "投影成功"只等于写库，模型仍然看不到。
	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           "sess_parent",
		TargetParentSessionID: "sess_parent",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Contains(t, digest.Text, "batch_api_failed")
}

// TestAPIBatchLifecycleProjector_CleanSuccessRecommendsClose 固定成功终态的口径：
// info/closed，推荐动作为 close（模型面提示，row 本身只允许 inspect——plan §4.1.1），
// 且不产生 critical 告警。
func TestAPIBatchLifecycleProjector_CleanSuccessRecommendsClose(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-batch-projector-done")
	ctx := context.Background()

	err := handler.apiBatchLifecycleProjector()(ctx, agent.BatchTerminalLifecycle{
		BatchID:         "batch_api_done",
		RootScopeID:     "sess_parent",
		ParentSessionID: "sess_parent",
		Status:          subagentbatch.BatchCompleted,
		EventType:       "subagent.batch.done",
		SubjectVersion:  5,
		TaskCount:       3,
		CompletedCount:  3,
	})
	require.NoError(t, err)

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:     "sess_parent",
		SubjectID:       "batch_api_done",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, supervision.SeverityInfo, notifications[0].Severity)
	require.Equal(t, supervision.ResolutionClosed, notifications[0].ResolutionState)
	require.Equal(t, string(supervision.ActionClose), notifications[0].RecommendedAction)

	// 与设计口径一致：成功终态是 info/closed 行，不制造 critical/action-required
	// 噪声，因此 preflight digest 不会为它注入告警行（父侧靠 progress rollup /
	// 巡查 / supervision_descendants 看到"已完成，可 close"的提示）。
	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           "sess_parent",
		TargetParentSessionID: "sess_parent",
	})
	require.NoError(t, err)
	require.Equal(t, 0, digest.ActionRequired)
	require.NotContains(t, digest.Text, "batch_api_done")
}

// TestAPIBatchLifecycleProjector_RequiresControlPlane 固定"无 durable 控制面"
// 时的失败措辞：投影是 best-effort，但绝不能静默成功（否则接线缺失会被当成
// 已投影，重演本缺口）。
func TestAPIBatchLifecycleProjector_RequiresControlPlane(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	err := handler.apiBatchLifecycleProjector()(context.Background(), agent.BatchTerminalLifecycle{
		BatchID:         "batch_api_unwired",
		RootScopeID:     "sess_parent",
		ParentSessionID: "sess_parent",
		Status:          subagentbatch.BatchFailed,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
}

// TestNewAPIAgentWiresBatchLifecycleProjector 守护接线本身（缺口就是"能力在包内、
// 宿主没接线"）：durable 控制面就绪的新 agent 必须带上终态桥；无 store 的宿主保持
// 现状（与 supervision 工具门控同口径）。
func TestNewAPIAgentWiresBatchLifecycleProjector(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-batch-projector-wiring")
	wired := handler.newAPIAgent(&agent.Config{Name: "wiring-on", Model: "test-model"})
	require.False(t, agentBatchLifecycleProjectorIsNil(t, wired),
		"控制面就绪时必须装 batch 终态桥，否则后台 batch 终态不进 store/digest/wake")

	bare := NewHandler(skill.NewRegistry(nil), nil, nil)
	unwired := bare.newAPIAgent(&agent.Config{Name: "wiring-off", Model: "test-model"})
	require.True(t, agentBatchLifecycleProjectorIsNil(t, unwired),
		"无 durable store 的宿主必须保持既有行为（不装桥、不产生新写入）")
}

func agentBatchLifecycleProjectorIsNil(t *testing.T, apiAgent *agent.Agent) bool {
	t.Helper()
	require.NotNil(t, apiAgent)
	field := reflect.ValueOf(apiAgent).Elem().FieldByName("batchLifecycleProjector")
	require.True(t, field.IsValid(), "agent.Agent must keep the batchLifecycleProjector field for this guard")
	return field.IsNil()
}

// TestAPIBatchLifecycleProjector_ResumesParkedTurnOnCriticalTerminal 钉住 G1 修复
// （2026-09-26 审计 `docs/plan/spawn-subagent-same-turn-loop-gap-audit-20260926.md`）：
// 父 turn 已挂起（awaiting_obligations，零 goroutine 等子任务）时，critical 终态
// wake 必须按"同一 turn 的 resume episode"投递出去，而不是被 Busy() 门判成
// "父不可运行"、把 wake 滞留成 durable 行（CLI 宿主早已用 AcceptsResume()）。
// 修复前：drainSupervisedParentWake 返回 ErrWakeParentBusy → 投影按 durable 正常
// 结果静默返回，投递次数为 0，父会话永远等不到"批次失败"的汇报点。
func TestAPIBatchLifecycleProjector_ResumesParkedTurnOnCriticalTerminal(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-batch-parked-resume")
	ctx := context.Background()
	const (
		parentSession = "sess_parked"
		parkedTurnID  = "turn_parked"
		batchID       = "batch_api_parked"
	)

	// §6.12 挂起记录的唯一事实来源是 durable batch 控制面（actor 的 turnSuspended
	// 回查它；缺了它 parked 状态会自愈成"未挂起"）。
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	now := time.Now().UTC()
	created, err := handler.getSubagentBatchStore().CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSession,
		ParentSessionID: parentSession,
		ParentTurnID:    parkedTurnID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, handler.getSubagentBatchStore().ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        parkedTurnID,
		SessionID:     parentSession,
		RootScopeID:   parentSession,
		ObligationIDs: []string{batchID},
		ParkedAt:      now,
	}))

	// 真实 actor + 挂起态：Busy()==true（不接受并发新 turn）但
	// AcceptsResume()==true（接受同一 turn 的 resume episode）。
	runtimeStore := chat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID:       parentSession,
		Status:          chat.SessionIdle,
		SuspendedTurnID: parkedTurnID,
	}))
	handler.sessionRuntimeStore = runtimeStore
	apiAgent := handler.newAPIAgent(&agent.Config{Name: "api-parked-agent", Model: "test-model", MaxSteps: 3})
	require.NotNil(t, apiAgent)
	t.Cleanup(func() { _ = apiAgent.Close() })
	actor, err := chat.NewSessionActor(parentSession, chat.SessionActorConfig{
		Agent:      apiAgent,
		StateStore: runtimeStore,
		EventStore: runtimeStore,
	})
	require.NoError(t, err)
	handler.sessionHub = chat.NewSessionHub(func(string) (*chat.SessionActor, error) { return actor, nil })
	t.Cleanup(handler.sessionHub.StopAll)
	_, err = handler.sessionHub.GetOrCreate(parentSession)
	require.NoError(t, err)
	require.NoError(t, actor.UpdateStateForTest(ctx, func(state *chat.RuntimeState) error {
		state.Status = chat.SessionIdle
		state.SuspendedTurnID = parkedTurnID
		return nil
	}))
	summary, ok := actor.StateSummary()
	require.True(t, ok)
	require.True(t, summary.Busy(), "precondition: 挂起 turn 不得接受并发新 turn")
	require.True(t, summary.AcceptsResume(), "precondition: 挂起 turn 必须接受 resume episode")

	// 只替换"提示词落到会话上"的提交口（被测的是 admission 门与 prompt 选择，
	// 不是 agent 执行）；Runnable / Deliver / DeliverResume 全部保留生产闭包。
	consumer := handler.supervisionWakeConsumer(handler.getSupervisionWakeScheduler())
	require.NotNil(t, consumer)
	require.NotNil(t, consumer.DeliverResume,
		"G2：API 宿主必须接 DeliverResume，否则 resume 上下文永远到不了父 turn 的 prompt")
	deliveries := 0
	prompts := make([]string, 0, 1)
	handler.supervisionWakeSubmit = func(_ context.Context, _, prompt string) error {
		deliveries++
		prompts = append(prompts, prompt)
		return nil
	}
	// G3：投递成功的 resume 必须在宿主总线上播报一次 turn.resumed（挂起 → 恢复的
	// 可观测闭环）；投递失败路径见 supervision 包的单测（不播报）。
	var resumed []runtimeevents.Event
	handler.getRuntimeEventBus().Subscribe(runtimeevents.EventTurnResumed, func(event runtimeevents.Event) {
		resumed = append(resumed, event)
	})

	err = handler.apiBatchLifecycleProjector()(ctx, agent.BatchTerminalLifecycle{
		BatchID:         batchID,
		RootScopeID:     parentSession,
		ParentSessionID: parentSession,
		Status:          subagentbatch.BatchFailed,
		EventType:       "subagent.batch.failed",
		SubjectVersion:  2,
		TaskCount:       1,
		FailedCount:     1,
		Error:           "child failed",
	})
	require.NoError(t, err)
	require.Equal(t, 1, deliveries,
		"parked 父会话必须收到 critical 终态的同一 turn resume，而不是让 wake 滞留 durable")
	require.Len(t, prompts, 1)
	// G2 / AC-P3-1c：投递的 prompt 必须携带同一 turn 的账本判据与 rollup，
	// 而不是只有"去看摘要"的 legacy 提示。
	require.Contains(t, prompts[0], "[supervision] resume")
	require.Contains(t, prompts[0], "turn_id="+parkedTurnID)
	require.Contains(t, prompts[0], "所有 obligation 均已终态：请直接产出终局报告")
	require.Contains(t, prompts[0], batchID)
	require.Len(t, resumed, 1, "投递成功的 resume 必须播报一次 turn.resumed")
	require.Equal(t, parentSession, resumed[0].SessionID)
	require.Equal(t, parkedTurnID, resumed[0].Payload["turn_id"])
	require.Equal(t, supervision.ResumeTriggerTerminal, resumed[0].Payload["trigger"])
	require.Equal(t, 0, resumed[0].Payload["pending_count"])
	require.Equal(t, "failed", resumed[0].Payload["status"])
	require.Equal(t, true, resumed[0].Payload["terminal"])

	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           parentSession,
		TargetParentSessionID: parentSession,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "投递成功后 wake 必须离开 pending（认领 + release）")
}
