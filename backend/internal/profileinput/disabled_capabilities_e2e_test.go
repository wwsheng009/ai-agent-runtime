package profileinput

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// TestDisabledCapabilitiesEndToEndRealTools walks the real resolution chain:
// taxonomy-backed tool names resolve to capability domains, and the profile's
// disabled_capabilities must deny them at execution time while untouched tools
// keep passing AllowTool.
func TestDisabledCapabilitiesEndToEndRealTools(t *testing.T) {
	policy, err := BuildToolExecutionPolicy(ResolvedToolPolicy{
		DisabledCapabilities: []string{"network", "background_task", "agent_management"},
	})
	require.NoError(t, err)
	require.NotNil(t, policy)

	// web_search carries RequiresNet in the built-in taxonomy → CapNetwork.
	resolver := runtimepolicy.DefaultCapabilityResolver{}
	require.Contains(t,
		resolver.Resolve(runtimepolicy.EvalRequest{ToolName: "web_search"}),
		runtimepolicy.CapNetwork)
	require.Error(t, policy.AllowCapabilities(
		resolver.Resolve(runtimepolicy.EvalRequest{ToolName: "web_search"})),
		"web_search must be denied once the network domain is disabled")

	// job_start is a control-plane background tool.
	require.Error(t, policy.AllowCapabilities(
		resolver.Resolve(runtimepolicy.EvalRequest{ToolName: "job_start"})),
		"job_start must be denied once background_task is disabled")

	// Local read/write tools stay untouched.
	require.NoError(t, policy.AllowTool("view"))
	require.NoError(t, policy.AllowTool("shell"))
	require.NoError(t, policy.AllowTool("write"))

	// CapabilitiesForTask-derived child scopes must intersect with the profile
	// boundary (a child can never re-enable the network domain).
	child := runtimepolicy.CapabilitiesForTask("research", true, []string{"view", "shell", "web_search"}, nil)
	require.Error(t, policy.AllowCapabilities(child),
		"child scope containing network tools must be rejected under the disabled domain")
}
