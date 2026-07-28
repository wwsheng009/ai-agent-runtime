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

func TestToolDefinitionsFingerprintTracksSchemaAndWireMetadata(t *testing.T) {
	base := []types.ToolDefinition{{
		Name:        "apply_patch",
		Description: "Apply a patch",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"patch": map[string]interface{}{"type": "string", "description": "patch text"},
			},
			"required":             []interface{}{"patch"},
			"additionalProperties": false,
		},
		Metadata: map[string]interface{}{
			"type":   "custom",
			"format": map[string]interface{}{"type": "grammar", "syntax": "lark", "definition": "start: /.+/"},
		},
	}}
	fingerprint := ToolDefinitionsFingerprint(base)
	require.NotEmpty(t, fingerprint)

	equivalent := []types.ToolDefinition{{
		Description: "Apply a patch",
		Name:        "apply_patch",
		Metadata: map[string]interface{}{
			"format": map[string]interface{}{"definition": "start: /.+/", "syntax": "lark", "type": "grammar"},
			"type":   "custom",
		},
		Parameters: map[string]interface{}{
			"additionalProperties": false,
			"required":             []interface{}{"patch"},
			"properties": map[string]interface{}{
				"patch": map[string]interface{}{"description": "patch text", "type": "string"},
			},
			"type": "object",
		},
	}}
	require.Equal(t, fingerprint, ToolDefinitionsFingerprint(equivalent), "map insertion order must not affect the fingerprint")

	changedSchema := cloneToolDefinitions(base)
	changedSchema[0].Parameters["required"] = []interface{}{}
	require.NotEqual(t, fingerprint, ToolDefinitionsFingerprint(changedSchema))

	changedMetadata := cloneToolDefinitions(base)
	changedMetadata[0].Metadata["format"].(map[string]interface{})["definition"] = "start: /[a-z]+/"
	require.NotEqual(t, fingerprint, ToolDefinitionsFingerprint(changedMetadata))

	changedName := cloneToolDefinitions(base)
	changedName[0].Name = "apply_diff"
	require.NotEqual(t, fingerprint, ToolDefinitionsFingerprint(changedName))
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
