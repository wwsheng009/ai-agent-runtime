package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// 巡检工具族横跨多张并行注册表，任何一张漏项都会让工具面自相矛盾：
//
//	broker 常量（模型可见入口） -> policy essentials（窄 allowlist / 定义过滤）
//	                          -> policy taxonomy（taxonomy-first 能力解析）
//	                          -> policy capability resolver（能力作用域）
//	                          -> toolkit.IsCoreTool（search 投影下的常驻清单）
//
// 以 broker 常量为单一事实源做交叉校验，防止只改一处就「看起来接好了」。
var supervisionToolRegistry = []string{
	ToolSupervisionSnapshot,
	ToolSupervisionDescendants,
	ToolAckLifecycle,
	ToolControlDescendant,
}

func TestSupervisionToolFamilyIsRegisteredConsistently(t *testing.T) {
	resolver := runtimepolicy.DefaultCapabilityResolver{}
	for _, name := range supervisionToolRegistry {
		t.Run(name, func(t *testing.T) {
			require.True(t, runtimepolicy.IsRuntimeOwnedEssentialTool(name),
				"%s must be runtime-owned essential, otherwise a narrow allowlist filters its definition out of the provider request", name)
			require.True(t, toolkit.IsCoreTool(nil, name),
				"%s must stay listed as a builtin core tool when the catalog is projected through search", name)
			_, ok := runtimepolicy.LookupToolTaxonomy(name)
			require.True(t, ok, "%s must have a taxonomy row", name)
			require.NotEmpty(t, resolver.Resolve(runtimepolicy.EvalRequest{ToolName: name}),
				"%s must resolve to a capability set", name)
		})
	}
}

// 宿主广播出来的巡检工具必须同时通过策略的定义过滤。否则会出现
// 「Definitions 里有、模型却看不到」的假接入：宿主以为接好了，窄 allowlist
// 下 filterPolicyBlockedToolDefinitions 会把定义从 provider 请求里摘掉。
func TestAdvertisedSupervisionToolsSurviveNarrowAllowlist(t *testing.T) {
	broker := &Broker{Supervision: &fakeSupervisionController{}}
	defs := broker.Definitions()
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false)
	for _, name := range supervisionToolRegistry {
		_ = supervisionDefinition(t, defs, name)
		require.True(t, policy.AllowsDefinition(name),
			"%s is advertised by the broker but hidden by a narrow allowlist", name)
	}
}
