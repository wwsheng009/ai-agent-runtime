package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestAPIHostWiresSubagentCapacityResumeGate 守护 A6 的 API 宿主接线：batch 终态
// 桥与 sessionAgentController 共用的唯一 consumer 构造点必须装上 resume 容量门控
// （瞬时容量），并且按 scheduler 指针缓存复用同一实例。
func TestAPIHostWiresSubagentCapacityResumeGate(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-resume-capacity-wiring")
	scheduler := handler.getSupervisionWakeScheduler()
	require.NotNil(t, scheduler)

	consumer := handler.supervisionWakeConsumer(scheduler)
	require.NotNil(t, consumer)
	probe := consumer.ResumeCapacity
	require.NotNil(t, probe, "A6：API 宿主必须注入 resume 容量门控，否则门控是死代码")

	ctx := context.Background()
	req := supervision.ResumeCapacityRequest{TargetParentSessionID: "sess_parent"}

	verdict, err := probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "未配置进程级上限（limiter 为 nil）必须放行")

	limiter := agent.NewSubagentConcurrencyLimiter(1)
	handler.subagentLimiterMu.Lock()
	handler.subagentLimiter = limiter
	handler.subagentLimiterMu.Unlock()
	require.NoError(t, limiter.Acquire(ctx))
	// Release 是幂等保护的（无未归还槽位时忽略），因此 defer 只作失败清理。
	defer limiter.Release()

	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.False(t, verdict.Allowed, "槽位占满时必须拒绝本次 resume（wake 保持 pending 排队）")
	require.Equal(t, supervision.ResumeGateConcurrency, verdict.Reason)
	require.NotEmpty(t, verdict.Detail)

	limiter.Release()
	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "额度释放后同一条 wake 必须能重新投递")

	require.Same(t, consumer, handler.supervisionWakeConsumer(scheduler),
		"scheduler 指针不变时必须复用同一 consumer（门控不重建）")
}

// TestAPIHostWiresResumePolicyDepthGate 守护 A6 **深度半支**的 API 宿主接线：与
// CLI 宿主同口径——ceiling 走 NormalizeAgentsConfig（0 = 未设置 → 默认上限），
// depth 读会话上下文，边界是 >=。
//
// 命中只置 Restricted（Allowed 仍为真，wake 照常投递并消费）；查询失败 fail-open。
func TestAPIHostWiresResumePolicyDepthGate(t *testing.T) {
	ctx := context.Background()
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-resume-policy-wiring")
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	handler.runtimeConfig = &runtimecfg.RuntimeConfig{Agents: runtimecfg.AgentsConfig{MaxDepth: 2}}

	scheduler := handler.getSupervisionWakeScheduler()
	require.NotNil(t, scheduler)
	consumer := handler.supervisionWakeConsumer(scheduler)
	require.NotNil(t, consumer)
	probe := consumer.ResumeCapacity
	require.NotNil(t, probe, "A6：API 宿主必须注入 resume 门控（容量 + 静态策略）")

	save := func(id string, depth int) {
		t.Helper()
		session := chat.NewSession("user-resume-policy")
		session.ID = id
		session.SetContext(toolbroker.AgentSessionContextDepth, depth)
		require.NoError(t, sessionManager.GetStorage().Save(ctx, session))
	}

	// 未达上限：放行且不受限。
	save("api-depth-below", 1)
	verdict, err := probe.CanResume(ctx, supervision.ResumeCapacityRequest{TargetParentSessionID: "api-depth-below"})
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.False(t, verdict.Restricted, "depth < ceiling 的会话不得被限制")

	// 恰好撞上限（边界是 >=）：受限放行，reason/detail 供 digest 渲染。
	save("api-depth-at-ceiling", 2)
	verdict, err = probe.CanResume(ctx, supervision.ResumeCapacityRequest{TargetParentSessionID: "api-depth-at-ceiling"})
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "静态策略不得 defer：wake 必须照常投递并消费")
	require.True(t, verdict.Restricted)
	require.Equal(t, supervision.ResumeGateDepth, verdict.Reason)
	require.Contains(t, verdict.Detail, "depth=2 max=2")

	// 会话不可读：fail-open。
	verdict, err = probe.CanResume(ctx, supervision.ResumeCapacityRequest{TargetParentSessionID: "api-depth-missing"})
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.False(t, verdict.Restricted)
}
