package profileinput

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestBuildToolExecutionPolicyDisabledCapabilities(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		DisabledCapabilities: []string{"network", "background_task"},
	})
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.True(t, policy.CapabilityScopeEnabled)

	names := policy.AllowedCapabilityNames()
	require.NotContains(t, names, "network")
	require.NotContains(t, names, "background_task")
	// The floor domains must always survive so the scope never amputates the
	// whole surface.
	require.Contains(t, names, "read_only")
	require.Contains(t, names, "exec_shell")
	// Non-disabled domains keep working.
	require.Contains(t, names, "write_fs")
	require.Contains(t, names, "agent_management")

	// Execution-time behavior: a network tool is rejected, a plain read tool
	// still passes.
	require.Error(t, policy.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapNetwork}))
	require.NoError(t, policy.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapReadOnly}))
}

func TestBuildToolExecutionPolicyDisabledCapabilitiesUnknownDomainErrors(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		DisabledCapabilities: []string{"n0t-a-domain"},
	})
	require.Error(t, err)
	require.Nil(t, policy)
	require.Contains(t, err.Error(), "unknown capability domain")
}

func TestBuildToolExecutionPolicyDisabledCapabilitiesNilWhenEmpty(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{})
	require.NoError(t, err)
	require.Nil(t, policy, "no declarations must keep the nil-policy zero-change contract")
}

func TestBuildToolExecutionPolicyDisabledCapabilitiesCombinesWithDenylist(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		Denylist:             []string{"fetch"},
		DisabledCapabilities: []string{"network"},
	})
	require.NoError(t, err)
	require.NotNil(t, policy)
	// Both layers apply: fetch denied by name, network tools denied by domain.
	require.True(t, policy.DeniedTools["fetch"])
	require.Error(t, policy.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapNetwork}))
}

func TestCapabilitiesComplementOfKeepsFloorDomains(t *testing.T) {
	everything := runtimepolicy.AllUserDisablableCapabilities()
	complement := runtimepolicy.CapabilitiesComplementOf(everything)
	names := make([]string, 0, len(complement))
	for _, capability := range complement {
		names = append(names, string(capability))
	}
	require.ElementsMatch(t, []string{"read_only", "exec_shell"}, names)
}

// 通配能力域（2026-10-16）：能力域是封闭枚举，"*" 展开为全部用户可禁域，
// "prefix*" 按域名前缀匹配（agent_* -> agent_management），与 allowlist/
// denylist 的 toolregistry.MatchPattern 语义一致。
func TestBuildToolExecutionPolicyDisabledCapabilitiesWildcardAll(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		DisabledCapabilities: []string{"*"},
	})
	require.NoError(t, err)
	require.NotNil(t, policy)

	names := policy.AllowedCapabilityNames()
	// 全部用户可禁域被禁；地板域（read_only/exec_shell）必须保留。
	for _, domain := range runtimepolicy.AllUserDisablableCapabilities() {
		require.NotContains(t, names, string(domain))
	}
	require.Contains(t, names, "read_only")
	require.Contains(t, names, "exec_shell")
}

func TestBuildToolExecutionPolicyDisabledCapabilitiesWildcardPrefix(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		DisabledCapabilities: []string{"agent_*", "background_task"},
	})
	require.NoError(t, err)
	require.NotNil(t, policy)

	names := policy.AllowedCapabilityNames()
	require.NotContains(t, names, "agent_management")
	require.NotContains(t, names, "background_task")
	// 前缀只命中 agent_*；其余域不受影响。
	require.Contains(t, names, "network")
	require.Contains(t, names, "write_fs")
}

func TestBuildToolExecutionPolicyDisabledCapabilitiesWildcardNoMatchErrors(t *testing.T) {
	_, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		DisabledCapabilities: []string{"nonexistent_*"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "matches no capability domain")
}
