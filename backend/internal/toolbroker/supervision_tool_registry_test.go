package toolbroker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// 巡检工具族横跨多张并行注册表，任何一张漏项都会让工具面自相矛盾：
//
//	broker 常量（模型可见入口） -> registry Essential（观测/目录标记）
//	                          -> policy taxonomy（taxonomy-first 能力解析）
//	                          -> policy capability resolver（能力作用域）
//	                          -> toolkit.IsCoreTool（search 投影下的常驻清单）
//
// 以 broker 常量为单一事实源做交叉校验，防止只改一处就「看起来接好了」。
//
// 表分成两层：advertised 是模型能看到的入口，每个能力只留一个名字；
// retired 是不再广播、但 dispatch 与已落库指针仍指向的定义名，故四张策略表
// 必须继续注册。旧拼写（ack_lifecycle / control_descendant 等）自 2026-10
// 起不再被任何一层接受，已从本表移除。
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

// supervisionToolRegistry 是「可调用名字」全集：可见面 + 退役面。
var supervisionToolRegistry = append(
	append([]string{}, advertisedSupervisionToolRegistry...),
	retiredSupervisionToolRegistry...,
)

func TestSupervisionToolFamilyIsRegisteredConsistently(t *testing.T) {
	resolver := runtimepolicy.DefaultCapabilityResolver{}
	for _, name := range supervisionToolRegistry {
		t.Run(name, func(t *testing.T) {
			require.True(t, toolregistry.IsEssential(name),
				"%s must be declared Essential in the registry (observation/catalog metadata)", name)
			require.True(t, toolkit.IsCoreTool(nil, name),
				"%s must stay listed as a builtin core tool when the catalog is projected through search", name)
			_, ok := runtimepolicy.LookupToolTaxonomy(name)
			require.True(t, ok, "%s must have a taxonomy row", name)
			require.NotEmpty(t, resolver.Resolve(runtimepolicy.EvalRequest{ToolName: name}),
				"%s must resolve to a capability set", name)
		})
	}
}

// 宿主广播出来的巡检工具必须同时通过策略的定义过滤。essential 豁免移除后
// （2026-10-16），窄 allowlist 会裁掉未列出的 essential 定义——需要控制面的
// 名单用通配（["*"] / ["subagent_*"]）放行；这里钉住"窄名单裁、通配留"。
func TestAdvertisedSupervisionToolsGatedByAllowlist(t *testing.T) {
	broker := &Broker{Supervision: &fakeSupervisionController{}}
	defs := broker.Definitions()
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false)
	wildcard := runtimepolicy.NewToolExecutionPolicy([]string{"subagent_*", "supervision_*", "read_agent_result"}, false)
	for _, name := range advertisedSupervisionToolRegistry {
		_ = supervisionDefinition(t, defs, name)
		require.False(t, policy.AllowsDefinition(name),
			"%s: a narrow allowlist must hide the definition of an essential tool it does not list", name)
		require.True(t, wildcard.AllowsDefinition(name),
			"%s: the wildcard allowlist must keep the advertised definition visible", name)
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
