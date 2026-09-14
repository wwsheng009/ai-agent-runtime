package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A read-only child request must not advertise tools the execution policy
// denies; otherwise the model calls them, fails at execution, and burns turns on
// a boundary it cannot cross (observed with a write schema in a read-only child).
func TestFilterPolicyBlockedToolDefinitionsDropsReadOnlyWriteSurface(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	tools := []types.ToolDefinition{
		{Name: "write"},
		{Name: "apply_patch"},
		{Name: "append_write"},
		{Name: "background_task"},
		{Name: "shell"},
		{Name: "view"},
	}

	filtered, blocked := filterPolicyBlockedToolDefinitions(tools, policy)
	require.ElementsMatch(t, []string{"write", "apply_patch", "append_write", "background_task"}, blocked)
	names := make(map[string]bool, len(filtered))
	for _, def := range filtered {
		names[def.Name] = true
	}

	for _, blocked := range []string{"write", "apply_patch", "append_write", "background_task"} {
		require.False(t, names[blocked], "%s must not be advertised to a read-only child", blocked)
	}
	for _, kept := range []string{"shell", "view"} {
		require.True(t, names[kept], "%s must stay available to a read-only child", kept)
	}
}

func TestFilterPolicyBlockedToolDefinitionsDropsDelegationToolsWhenBlocked(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	policy.BlockDelegation = true
	tools := []types.ToolDefinition{
		{Name: "spawn_agent"},
		{Name: "spawn_subagents"},
		{Name: "view"},
	}

	filtered, blocked := filterPolicyBlockedToolDefinitions(tools, policy)
	require.ElementsMatch(t, []string{"spawn_agent", "spawn_subagents"}, blocked)
	require.Len(t, filtered, 1)
	require.Equal(t, "view", filtered[0].Name)
}

func TestFilterPolicyBlockedToolDefinitionsKeepsSurfaceWithoutPolicy(t *testing.T) {
	tools := []types.ToolDefinition{{Name: "write"}, {Name: "view"}}
	filtered, blocked := filterPolicyBlockedToolDefinitions(tools, nil)
	require.Equal(t, tools, filtered)
	require.Empty(t, blocked)
}

// spawn_agent and spawn_subagents must serve the same read_only contract text.
func TestSpawnSubagentsReadOnlyDescriptionMatchesPolicyConstant(t *testing.T) {
	definition := spawnSubagentsToolDefinition()
	properties, ok := definition.Parameters["properties"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents properties missing")
	agents, ok := properties["agents"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents property missing")
	items, ok := agents["items"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents items missing")
	itemProperties, ok := items["properties"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents item properties missing")
	readOnly, ok := itemProperties["read_only"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents read_only property missing")
	require.Equal(t, runtimepolicy.ReadOnlyChildOptionDescription, readOnly["description"])
}

func TestRenderSubagentResultsSurfacesReadOnlyNarrowing(t *testing.T) {
	text := renderSubagentResults([]SubagentResult{{
		ID:                    "writer-1",
		Success:               false,
		Summary:               "goal requires file writes",
		ReadOnly:              true,
		ReadOnlyFilteredTools: []string{"write", "apply_patch"},
	}})
	require.Contains(t, text, "read-only: the child was denied these requested write-like tools: write, apply_patch")
}
