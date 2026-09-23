package policy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 巡检工具族在 policy 包内有三张并行注册表：
//
//  1. IsRuntimeOwnedEssentialTool —— 窄 allowlist 下保留定义 + 执行豁免；
//     agent/loop.go 的 filterPolicyBlockedToolDefinitions 按允许结果把定义从
//     provider 请求里摘掉，所以漏项 = 模型看不见该工具（比执行被拒更糟），
//     而 prompt 引导词仍在让模型调用它。
//  2. knownToolTaxonomy —— taxonomy-first 的能力解析入口。
//  3. normalizeToolName 别名表 + 能力表 —— 模型少写下划线时的归一化。
//
// 下面用字面量而不是 toolbroker 常量：toolbroker 依赖 policy，反向 import 会成环。
// 可见面（每个能力只留一个名字）+ 退役但仍可调用的旧名字：两张表都必须继续
// 注册，否则旧名字会在窄 allowlist 下被静默摘掉定义，或丢掉 taxonomy / 能力行。
var supervisionToolFamily = []string{
	"supervision_snapshot",
	"supervision_descendants",
	"read_agent_result",
	"ack_lifecycle",
	"control_descendant",
	"subagent_status",
	"subagent_inspect_task",
	"subagent_ack_lifecycle",
	"subagent_control",
}

func TestSupervisionToolsAreRuntimeOwnedEssentials(t *testing.T) {
	for _, name := range supervisionToolFamily {
		if !IsRuntimeOwnedEssentialTool(name) {
			t.Errorf("%s must be runtime-owned essential: a narrow --allow-tool/profile allowlist would otherwise filter its definition out of the provider request", name)
		}
	}
}

func TestSupervisionToolsSurviveNarrowAllowlist(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "grep"}, false)
	for _, name := range supervisionToolFamily {
		if err := policy.AllowTool(name); err != nil {
			t.Errorf("expected %s to stay executable under a narrow allowlist, got %v", name, err)
		}
		if !policy.AllowsDefinition(name) {
			t.Errorf("expected %s to stay visible under a narrow allowlist", name)
		}
	}
}

func TestSupervisionToolAliasesNormalize(t *testing.T) {
	for alias, want := range map[string]string{
		"supervisionsnapshot":    "supervision_snapshot",
		"supervisionSnapshot":    "supervision_snapshot",
		"supervisiondescendants": "supervision_descendants",
		"supervisionDescendants": "supervision_descendants",
		"readagentresult":        "read_agent_result",
		"readAgentResult":        "read_agent_result",
		"acklifecycle":           "subagent_ack_lifecycle",
		"ackLifecycle":           "subagent_ack_lifecycle",
		"ack_lifecycle":          "subagent_ack_lifecycle",
		"subagentacklifecycle":   "subagent_ack_lifecycle",
		"subagentAckLifecycle":   "subagent_ack_lifecycle",
		"controldescendant":      "subagent_control",
		"controlDescendant":      "subagent_control",
		"control_descendant":     "subagent_control",
		"subagentcontrol":        "subagent_control",
		"subagentControl":        "subagent_control",
		"subagentstatus":         "subagent_status",
		"subagentStatus":         "subagent_status",
		"agent_status":           "subagent_status",
		"subagentinspecttask":    "subagent_inspect_task",
		"subagentInspectTask":    "subagent_inspect_task",
		"subagent_inspect":       "subagent_inspect_task",
		"inspecttask":            "subagent_inspect_task",
	} {
		if got := normalizeToolName(alias); got != want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", alias, got, want)
		}
	}
}

func TestSupervisionToolsHaveTaxonomy(t *testing.T) {
	for _, name := range supervisionToolFamily {
		if _, ok := LookupToolTaxonomy(name); !ok {
			t.Errorf("%s must have a taxonomy row", name)
		}
	}
}

func TestSupervisionCapabilityScope(t *testing.T) {
	resolver := DefaultCapabilityResolver{}
	for _, name := range []string{"supervision_snapshot", "supervision_descendants", "read_agent_result"} {
		require.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: name}),
			"%s is an observation-only read", name)
	}
	for _, name := range []string{"subagent_status", "subagent_inspect_task"} {
		require.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: name}),
			"%s is an observation-only read: the ledger view and the deep look never mutate the control plane", name)
	}
	for _, name := range []string{"ack_lifecycle", "control_descendant", "subagent_ack_lifecycle", "subagent_control"} {
		require.Equal(t, []Capability{CapReadOnly, CapAgentManagement}, resolver.Resolve(EvalRequest{ToolName: name}),
			"%s mutates the durable supervision control plane", name)
	}

	// The taxonomy-first path must not silently downgrade the control-plane
	// writes to read-only: a session scoped to read_only alone must not be able
	// to acknowledge or control anything.
	readOnlyOnly := NewCapabilityScopedToolExecutionPolicy(nil, []Capability{CapReadOnly})
	for _, name := range []string{"ack_lifecycle", "control_descendant", "subagent_ack_lifecycle", "subagent_control"} {
		if err := readOnlyOnly.AllowTool(name); err == nil {
			t.Errorf("expected %s to require agent_management, not just read_only", name)
		}
	}
	for _, name := range []string{"supervision_snapshot", "supervision_descendants", "read_agent_result"} {
		if err := readOnlyOnly.AllowTool(name); err != nil {
			t.Errorf("expected %s to be allowed under read_only scope, got %v", name, err)
		}
	}
}

// taxonomy-first 查找会先命中 knownToolTaxonomy，所以控制面能力若只写在
// 名字 switch 的回退分支里，那条分支对已有 taxonomy 行的工具永远不会执行。
// capabilitiesFromTaxonomy 必须复用同一张能力表。
func TestSupervisionCapabilitiesAreNotDowngradedByTaxonomyPath(t *testing.T) {
	resolver := DefaultCapabilityResolver{}
	for _, name := range supervisionToolFamily {
		tax, ok := LookupToolTaxonomy(name)
		require.True(t, ok, "%s must have a taxonomy row", name)
		require.Equal(t, resolver.Resolve(EvalRequest{ToolName: name}), capabilitiesFromTaxonomy(tax),
			"%s: taxonomy-first resolution and capabilitiesFromTaxonomy disagree", name)
	}

	for _, name := range []string{"ack_lifecycle", "control_descendant", "subagent_ack_lifecycle", "subagent_control"} {
		require.Equal(t, []Capability{CapReadOnly, CapAgentManagement},
			capabilitiesFromTaxonomy(ToolTaxonomy{Name: name, Kind: types.ToolKindControl}),
			"%s must keep agent_management when resolved through the taxonomy table", name)
	}
}
