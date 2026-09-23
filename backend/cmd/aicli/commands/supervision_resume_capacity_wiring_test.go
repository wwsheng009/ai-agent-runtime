package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestLocalHostWiresSubagentCapacityResumeGate 守护 A6 的宿主接线（缺口就是
// "能力在包内、宿主没接线"）：控制面就绪的 CLI 宿主必须给 wake consumer 装上
// resume 容量门控；未配置 agents.maxThreads 时保持旧行为（放行），槽位占满时
// 拒绝并发门——wake 因此保持 pending 排队而不是被消费掉。
func TestLocalHostWiresSubagentCapacityResumeGate(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)
	host.wireLocalSupervisionWakeConsumer()
	require.NotNil(t, host.supervisionWake, "控制面就绪时必须装配 wake consumer")

	probe := host.supervisionWake.ResumeCapacity
	require.NotNil(t, probe, "A6：宿主必须注入 resume 容量门控，否则门控是死代码")

	ctx := context.Background()
	req := supervision.ResumeCapacityRequest{TargetParentSessionID: "root-session"}

	verdict, err := probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "未配置进程级上限（limiter 为 nil）必须放行")

	limiter := agent.NewSubagentConcurrencyLimiter(1)
	host.subagentLimiterMu.Lock()
	host.subagentLimiter = limiter
	host.subagentLimiterMu.Unlock()
	require.NoError(t, limiter.Acquire(ctx))
	// Release 是幂等保护的（无未归还槽位时忽略），因此 defer 只作失败清理。
	defer limiter.Release()

	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.False(t, verdict.Allowed, "槽位占满时必须拒绝本次 resume")
	require.Equal(t, supervision.ResumeGateConcurrency, verdict.Reason)
	require.NotEmpty(t, verdict.Detail)

	limiter.Release()
	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "额度释放后同一条 wake 必须能重新投递")
}

// TestLocalHostWiresResumePolicyDepthGate 守护 A6 **深度半支**的宿主接线：同一个
// consumer 的 ResumeCapacity 必须把静态策略（深度）也接进去，并按宿主自己的口径
// 判定——ceiling 走 NormalizeAgentsConfig（0 = 未设置 → 默认上限），depth 读会话
// 上下文，边界是 >=。
//
// 静态策略命中只置 Restricted，Allowed 仍为真：wake 照常投递并消费（不排队、不
// 丢），digest 据此告知"本回合不得再派发"。查询失败必须 fail-open——既不能卡住
// supervision，也不能误报受限（假预告会让本可派发的回合自我禁足）。
func TestLocalHostWiresResumePolicyDepthGate(t *testing.T) {
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)
	host.SessionStore = runtimechat.NewInMemoryStorage()
	host.RuntimeConfig = &runtimecfg.RuntimeConfig{Agents: runtimecfg.AgentsConfig{MaxDepth: 2}}
	host.wireLocalSupervisionWakeConsumer()
	require.NotNil(t, host.supervisionWake, "控制面就绪时必须装配 wake consumer")

	probe := host.supervisionWake.ResumeCapacity
	require.NotNil(t, probe, "A6：宿主必须注入 resume 门控（容量 + 静态策略）")

	save := func(id string, depth int) {
		t.Helper()
		session := runtimechat.NewSession("tester")
		session.ID = id
		session.SetContext(toolbroker.AgentSessionContextDepth, depth)
		require.NoError(t, host.SessionStore.Save(ctx, session))
	}

	// 未达上限：放行且不受限。
	save("cli-depth-below", 1)
	verdict, err := probe.CanResume(ctx, supervision.ResumeCapacityRequest{TargetParentSessionID: "cli-depth-below"})
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.False(t, verdict.Restricted, "depth < ceiling 的会话不得被限制")

	// 恰好撞上限（边界是 >=）：受限放行，reason/detail 供 digest 渲染。
	save("cli-depth-at-ceiling", 2)
	verdict, err = probe.CanResume(ctx, supervision.ResumeCapacityRequest{TargetParentSessionID: "cli-depth-at-ceiling"})
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "静态策略不得 defer：wake 必须照常投递并消费")
	require.True(t, verdict.Restricted)
	require.Equal(t, supervision.ResumeGateDepth, verdict.Reason)
	require.Contains(t, verdict.Detail, "depth=2 max=2")

	// 会话不可读：fail-open。
	verdict, err = probe.CanResume(ctx, supervision.ResumeCapacityRequest{TargetParentSessionID: "cli-depth-missing"})
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.False(t, verdict.Restricted)
}
