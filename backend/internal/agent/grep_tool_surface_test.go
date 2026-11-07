package agent

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestOptimizeModelToolSurfaceKeepsGrepDepthOutOfCodexWireSchema(t *testing.T) {
	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil))
	var rawGrep types.ToolDefinition
	for _, info := range manager.ListTools() {
		if info.Name == "grep" {
			rawGrep = types.ToolDefinition{
				Name:        info.Name,
				Description: info.Description,
				Parameters:  normalizeToolParameters(info.InputSchema),
			}
			break
		}
	}
	require.Equal(t, "grep", rawGrep.Name)
	require.Contains(t, rawGrep.Parameters["properties"], "max_depth")

	optimized := optimizeModelToolSurface([]types.ToolDefinition{rawGrep})
	require.Len(t, optimized, 1)
	properties := optimized[0].Parameters["properties"].(map[string]interface{})
	expectedNames := []string{
		"pattern", "patterns", "path", "paths", "glob",
		"literal", "ignore_case", "case_sensitive", "word",
		"context", "files_with_matches", "count",
		"hidden", "no_ignore", "rg_args",
	}
	require.Equal(t, sortedMapKeys(properties), sortedStrings(expectedNames))
	require.NotContains(t, properties, "max_depth")
	require.NotContains(t, properties, "max_count")

	request := (&adapter.CodexAdapter{}).BuildRequest(adapter.RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "search"}},
		Functions: []map[string]interface{}{{
			"type":        "function",
			"name":        optimized[0].Name,
			"description": optimized[0].Description,
			"parameters":  optimized[0].Parameters,
		}},
	})
	tools := request["tools"].([]map[string]interface{})
	require.Len(t, tools, 1)
	wireParameters := tools[0]["parameters"].(map[string]interface{})
	wireProperties := wireParameters["properties"].(map[string]interface{})
	require.Equal(t, sortedMapKeys(wireProperties), sortedStrings(expectedNames))
	require.Equal(t, false, tools[0]["strict"])
	require.NotContains(t, wireParameters, "required")
	require.Equal(t, "integer", wireProperties["context"].(map[string]interface{})["type"])
	require.NotContains(t, wireProperties, "max_depth")
}

// TestManagerDoesNotRegisterRGTool pins that `rg` is not a toolkit alias:
// it is a real shell command, and a same-named tool would make shell rg
// searches ambiguous. The model-facing surface therefore only carries `grep`.
func TestManagerDoesNotRegisterRGTool(t *testing.T) {
	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil))
	definitions := make([]types.ToolDefinition, 0, 1)
	for _, info := range manager.ListTools() {
		require.NotEqual(t, "rg", info.Name, "rg must stay a shell command, not a toolkit tool")
		if info.Name == "grep" {
			definitions = append(definitions, types.ToolDefinition{
				Name:        info.Name,
				Description: info.Description,
				Parameters:  normalizeToolParameters(info.InputSchema),
			})
		}
	}
	require.Len(t, definitions, 1)

	optimized := optimizeModelToolSurface(definitions)
	require.Len(t, optimized, 1)
	require.Equal(t, "grep", optimized[0].Name)
	properties := optimized[0].Parameters["properties"].(map[string]interface{})
	require.Contains(t, properties, "pattern")
	require.NotContains(t, properties, "max_depth")
}

func sortedMapKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(values []string) []string {
	cloned := append([]string(nil), values...)
	sort.Strings(cloned)
	return cloned
}
