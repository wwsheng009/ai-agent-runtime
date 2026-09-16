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
var supervisionToolFamily = []string{
	"supervision_snapshot",
	"supervision_descendants",
	"ack_lifecycle",
	"control_descendant",
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
		"acklifecycle":           "ack_lifecycle",
		"controldescendant":      "control_descendant",
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
	for _, name := range []string{"supervision_snapshot", "supervision_descendants"} {
		require.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: name}),
			"%s is an observation-only read", name)
	}
	for _, name := range []string{"ack_lifecycle", "control_descendant"} {
		require.Equal(t, []Capability{CapReadOnly, CapAgentManagement}, resolver.Resolve(EvalRequest{ToolName: name}),
			"%s mutates the durable supervision control plane", name)
	}

	// The taxonomy-first path must not silently downgrade the control-plane
	// writes to read-only: a session scoped to read_only alone must not be able
	// to acknowledge or control anything.
	readOnlyOnly := NewCapabilityScopedToolExecutionPolicy(nil, []Capability{CapReadOnly})
	for _, name := range []string{"ack_lifecycle", "control_descendant"} {
		if err := readOnlyOnly.AllowTool(name); err == nil {
			t.Errorf("expected %s to require agent_management, not just read_only", name)
		}
	}
	for _, name := range []string{"supervision_snapshot", "supervision_descendants"} {
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

	for _, name := range []string{"ack_lifecycle", "control_descendant"} {
		require.Equal(t, []Capability{CapReadOnly, CapAgentManagement},
			capabilitiesFromTaxonomy(ToolTaxonomy{Name: name, Kind: types.ToolKindControl}),
			"%s must keep agent_management when resolved through the taxonomy table", name)
	}
}
