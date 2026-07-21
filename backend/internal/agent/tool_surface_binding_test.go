package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestToolDefinitionsFingerprintIsOrderSensitive(t *testing.T) {
	first := types.ToolDefinition{
		Name:        "view",
		Description: "View a file",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
		},
	}
	second := types.ToolDefinition{Name: "grep", Description: "Search files"}

	fingerprint := ToolDefinitionsFingerprint([]types.ToolDefinition{first, second})
	require.NotEmpty(t, fingerprint)
	require.Equal(t, fingerprint, ToolDefinitionsFingerprint([]types.ToolDefinition{first, second}))
	require.NotEqual(t, fingerprint, ToolDefinitionsFingerprint([]types.ToolDefinition{second, first}))

	first.Description = "View one file"
	require.NotEqual(t, fingerprint, ToolDefinitionsFingerprint([]types.ToolDefinition{first, second}))
}

func TestBuildToolSurfaceEligibilityKeyTracksPermissionPolicyAndCatalog(t *testing.T) {
	catalog := []types.ToolDefinition{
		{Name: "view", Description: "View a file"},
		{Name: "grep", Description: "Search files"},
	}
	policy := NewToolExecutionPolicy([]string{"view", "grep"}, true)

	base := BuildToolSurfaceEligibilityKey("plan", policy, catalog)
	require.NotEmpty(t, base)
	require.Equal(t, base, BuildToolSurfaceEligibilityKey("plan", policy, []types.ToolDefinition{catalog[1], catalog[0]}), "catalog ordering must not invalidate an equivalent eligibility set")
	require.NotEqual(t, base, BuildToolSurfaceEligibilityKey("accept_edits", policy, catalog))

	widerPolicy := NewToolExecutionPolicy([]string{"view", "grep"}, false)
	require.NotEqual(t, base, BuildToolSurfaceEligibilityKey("plan", widerPolicy, catalog))

	changedCatalog := append([]types.ToolDefinition(nil), catalog...)
	changedCatalog[0].Description = "View file content"
	require.NotEqual(t, base, BuildToolSurfaceEligibilityKey("plan", policy, changedCatalog))
}
