package skills

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
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
