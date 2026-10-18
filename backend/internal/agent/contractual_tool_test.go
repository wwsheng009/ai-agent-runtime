package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A child that asks for a non-empty task surface receives the runtime contract
// tool even when the parent profile's allowlist omitted it.
func TestResolveChildToolSurfaceAddsContractualTool(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	parent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"view", "shell"}, false))

	resolved, childPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "contractual-add",
		Goal:           "inspect",
		ToolsWhitelist: []string{"view"},
	})
	require.NoError(t, err)
	resolved = finalizeChildToolSurface(resolved, childPolicy, agentToolSurface(parent))
	require.Contains(t, resolved.ToolsWhitelist, "artifact_read")
	require.NoError(t, childPolicy.AllowTool("artifact_read"))
	for _, warning := range resolved.RouteWarnings {
		require.NotContains(t, warning, "granted_by_output_contract",
			"contractual grant is normal behavior, not a route warning")
	}
}

// The documented "explicitly empty = no tools" contract wins, and an explicit
// deny still suppresses the contract grant.
func TestResolveChildToolSurfaceContractualToolRespectsEmptyAndDeny(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	parent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"view", "shell"}, false))

	empty, emptyPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "contractual-empty",
		Goal:           "no tools",
		ToolsWhitelist: []string{},
	})
	require.NoError(t, err)
	empty = finalizeChildToolSurface(empty, emptyPolicy, agentToolSurface(parent))
	require.NotContains(t, empty.ToolsWhitelist, "artifact_read")

	denyPolicy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	denyPolicy.DeniedTools = map[string]bool{"artifact_read": true}
	parent.SetToolExecutionPolicy(denyPolicy)

	denied, deniedChildPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "contractual-denied",
		Goal:           "inspect",
		ToolsWhitelist: []string{"view"},
	})
	require.NoError(t, err)
	denied = finalizeChildToolSurface(denied, deniedChildPolicy, agentToolSurface(parent))
	require.NotContains(t, denied.ToolsWhitelist, "artifact_read")
}

// A known live surface that does not register artifact_read must not have the
// tool appended: the prompt/whitelist and the request schema must agree.
func TestResolveChildToolSurfaceSkipsContractualToolAbsentFromSurface(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, &MockCatalogMCPManager{})
	parent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"read_file"}, false))

	resolved, childPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "contractual-absent",
		Goal:           "inspect",
		ToolsWhitelist: []string{"read_file"},
	})
	require.NoError(t, err)
	resolved = finalizeChildToolSurface(resolved, childPolicy, agentToolSurface(parent))
	require.NotContains(t, resolved.ToolsWhitelist, "artifact_read")
}
