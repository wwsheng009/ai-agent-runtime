package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// TestApplyLocalChildAgentdefToolPolicyKeepsShellForBuiltinRoles 钉住 2026-09-30
// 真机缺口：内建 general/plan/explore 子代理经 agentdef 派生策略时，role 必须传进
// DeriveChildForTask。此前走 DeriveChild（role=""）时能力地板只有
// read_only+write_fs，子代理继承到 shell 工具面后被执行门禁拒绝——真机原文
// "policy:capability not allowed by execution policy: exec_shell"（general 子代理
// 因此完全无法运行任何 shell 命令，连 go test 都跑不了）。
func TestApplyLocalChildAgentdefToolPolicyKeepsShellForBuiltinRoles(t *testing.T) {
	for _, agentType := range []string{"general", "plan", "explore"} {
		parentPolicy := agent.NewToolExecutionPolicy(nil, false)
		parentPolicy.SetCapabilityScope([]runtimepolicy.Capability{
			runtimepolicy.CapReadOnly,
			runtimepolicy.CapWriteFS,
			runtimepolicy.CapExecShell,
			runtimepolicy.CapNetwork,
		})
		session := &ChatSession{ToolPolicy: parentPolicy}
		apiAgent := agent.NewAgent(&agent.Config{Name: "child", Provider: "test", Model: "test"}, nil)
		apiAgent.SetToolExecutionPolicy(parentPolicy)

		applyLocalChildAgentdefToolPolicy(apiAgent, agentType, session, t.TempDir(), t.TempDir())

		child := apiAgent.GetToolExecutionPolicy()
		require.NotNil(t, child)
		require.NoError(t, child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapExecShell}),
			"agent_type %q must keep exec_shell after the agentdef derivation", agentType)
	}
}

// TestApplyLocalChildReadOnlyPolicyNarrowsToParentCapabilities 钉住只读派生的
// 「绝不宽于父会话」：父策略没有 network/agent_management 时，read_only 子代理
// 不能因为派生边界的默认清单而拿到这些能力（此前 SetCapabilityScope 直接替换，
// 会静默放宽）。
func TestApplyLocalChildReadOnlyPolicyNarrowsToParentCapabilities(t *testing.T) {
	parentPolicy := agent.NewToolExecutionPolicy(nil, false)
	parentPolicy.SetCapabilityScope([]runtimepolicy.Capability{
		runtimepolicy.CapReadOnly,
		runtimepolicy.CapExecShell,
	})
	apiAgent := agent.NewAgent(&agent.Config{Name: "child", Provider: "test", Model: "test"}, nil)
	apiAgent.SetToolExecutionPolicy(parentPolicy)

	applyLocalChildReadOnlyPolicy(apiAgent, true)

	child := apiAgent.GetToolExecutionPolicy()
	require.NotNil(t, child)
	require.True(t, child.ReadOnly)
	require.NoError(t, child.AllowCapabilities([]runtimepolicy.Capability{
		runtimepolicy.CapReadOnly,
		runtimepolicy.CapExecShell,
	}))
	require.Error(t, child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapNetwork}),
		"read-only derivation must not grant network the parent never had")
	require.Error(t, child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapAgentManagement}),
		"read-only derivation must not grant agent-management the parent never had")
	require.True(t, parentPolicy.CapabilityScopeEnabled, "parent scope stays untouched")
}
