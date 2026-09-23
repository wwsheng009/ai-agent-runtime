package toolbroker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
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
//
// 表分成两层：advertised 是模型能看到的入口，每个能力只留一个名字；
// legacy 是退役名字，仍然可调用（dispatch / 别名 / 已落库审计记录都还指向
// 它们），因此必须继续留在 essentials / core / taxonomy / capability 四张表里。
var advertisedSupervisionToolRegistry = []string{
	ToolSubagentStatus,
	ToolSubagentInspectTask,
	ToolAckLifecycle,
	ToolControlDescendant,
}

// retiredSupervisionToolRegistry 是仍然可调用、但不再广播的定义名：dispatch 与
// 已落库的指针都还指向它们，所以四张策略表必须继续注册。
var retiredSupervisionToolRegistry = []string{
	ToolSupervisionSnapshot,
	ToolSupervisionDescendants,
	ToolReadAgentResult,
}

// aliasOnlySupervisionToolRegistry 是只有别名、没有定义的名字：它们只作为
// normalizeToolName 的输入拼写存在，必须归一化到可见面名字。
var aliasOnlySupervisionToolRegistry = []string{
	"ack_lifecycle",
	"control_descendant",
}

// supervisionToolRegistry 是「可调用名字」全集：可见面 + 退役面 + 别名面。
var supervisionToolRegistry = append(
	append(append([]string{}, advertisedSupervisionToolRegistry...), retiredSupervisionToolRegistry...),
	aliasOnlySupervisionToolRegistry...,
)

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
	for _, name := range advertisedSupervisionToolRegistry {
		_ = supervisionDefinition(t, defs, name)
		require.True(t, policy.AllowsDefinition(name),
			"%s is advertised by the broker but hidden by a narrow allowlist", name)
	}
}

// 可见面去重：同一能力在工具表里只能有一个名字。退役名字一旦重新出现，
// 模型就要在「哪个才是正牌入口」上做无信息量的猜测。
func TestAdvertisedSupervisionSurfaceHasNoRetiredDuplicates(t *testing.T) {
	broker := &Broker{Supervision: &fakeSupervisionController{}}
	names := toolDefinitionNames(broker.Definitions())
	// 生产过滤集合与测试清单必须一致，否则可见面断言会对着过期清单通过。
	retired := legacySupervisionToolNames()
	require.Len(t, retired, len(retiredSupervisionToolRegistry))
	for _, name := range retiredSupervisionToolRegistry {
		require.True(t, retired[name], "%s must be in the production retired set", name)
		require.NotContains(t, names, name,
			"%s is a retired duplicate of an advertised tool and must not be in the tool table", name)
	}
	for _, name := range advertisedSupervisionToolRegistry {
		require.Contains(t, names, name, "%s is the surviving entry point and must stay advertised", name)
	}
}

// 退役只针对可见面：dispatch、别名归一化与已落库的审计记录都还指向旧名字，
// 所以旧名字必须保持可调用——否则「重命名」会变成「破坏兼容」。
func TestRetiredSupervisionNamesStayCallable(t *testing.T) {
	controller := &fakeSupervisionController{
		snapshot: &supervision.Snapshot{},
		digest:   &supervision.Digest{},
	}
	broker := &Broker{Supervision: controller}

	for _, call := range []struct {
		name string
		args map[string]interface{}
	}{
		{ToolSupervisionSnapshot, map[string]interface{}{}},
		{ToolSupervisionDescendants, map[string]interface{}{}},
		{ToolReadAgentResult, map[string]interface{}{"id": "child-1"}},
	} {
		if _, _, err := broker.Execute(context.Background(), "parent-session", call.name, call.args); err != nil {
			t.Fatalf("%s must stay callable after the consolidation: %v", call.name, err)
		}
	}
	require.Equal(t, 3, controller.calls,
		"every retired name must still reach the host controller through dispatch")
}
