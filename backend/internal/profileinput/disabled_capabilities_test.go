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
