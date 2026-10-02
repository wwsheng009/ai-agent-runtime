package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestOptimizeModelToolSurfacePrefersViewOverReadAlias pins the alias folding:
// the registered `read` alias stays executable, but the model-facing surface
// advertises only the canonical `view` tool so no second identical schema is
// shipped to the provider.
func TestOptimizeModelToolSurfacePrefersViewOverReadAlias(t *testing.T) {
	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil))
	definitions := make([]types.ToolDefinition, 0, 2)
	for _, info := range manager.ListTools() {
		if info.Name == "view" || info.Name == "read" {
			definitions = append(definitions, types.ToolDefinition{
				Name:        info.Name,
				Description: info.Description,
				Parameters:  normalizeToolParameters(info.InputSchema),
			})
		}
	}
	require.Len(t, definitions, 2)

	optimized := optimizeModelToolSurface(definitions)
	require.Len(t, optimized, 1)
	require.Equal(t, "view", optimized[0].Name)
	properties := optimized[0].Parameters["properties"].(map[string]interface{})
	require.Contains(t, properties, "file_path")
	require.Contains(t, properties, "offset")
	require.Contains(t, properties, "limit")
}
