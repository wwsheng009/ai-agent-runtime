package agent

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// collectSurfaceDefinitions pulls the model-facing definitions for the given
// tool names from a fresh default manager.
func collectSurfaceDefinitions(t *testing.T, names ...string) map[string]types.ToolDefinition {
	t.Helper()
	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil))
	all := make([]types.ToolDefinition, 0, len(names))
	for _, info := range manager.ListTools() {
		all = append(all, types.ToolDefinition{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  normalizeToolParameters(info.InputSchema),
		})
	}
	optimized := optimizeModelToolSurface(all)
	result := map[string]types.ToolDefinition{}
	for _, def := range optimized {
		for _, name := range names {
			if def.Name == name {
				result[name] = def
			}
		}
	}
	for _, name := range names {
		require.Contains(t, result, name, "surface must keep %s", name)
	}
	return result
}

func propertyNames(t *testing.T, def types.ToolDefinition) []string {
	t.Helper()
	properties, ok := def.Parameters["properties"].(map[string]interface{})
	require.True(t, ok, "%s: properties must be a map", def.Name)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestOptimizeModelToolSurfaceAicliExecWhitelist(t *testing.T) {
	def := collectSurfaceDefinitions(t, "aicli_exec")["aicli_exec"]
	require.Equal(t, []string{
		"allow_nested", "cwd", "disable_tools", "log_dir", "output_bytes_cap",
		"permission_mode", "prompt", "session_dir", "timeout", "timeout_ms", "yolo",
	}, propertyNames(t, def))
	require.Contains(t, def.Description, "argv")
	require.NotContains(t, def.Description, "debug_http")
}

func TestOptimizeModelToolSurfaceShellWhitelist(t *testing.T) {
	def := collectSurfaceDefinitions(t, "shell")["shell"]
	require.Equal(t, []string{
		"command", "commands", "max_parallel", "parallel", "stop_on_error",
		"timeout_ms", "timeout_sec", "workdir",
	}, propertyNames(t, def))
	require.Contains(t, def.Description, "commands")
	require.NotContains(t, def.Description, "detach")
}

func TestOptimizeModelToolSurfaceViewWhitelist(t *testing.T) {
	def := collectSurfaceDefinitions(t, "view")["view"]
	require.Equal(t, []string{"file_path", "files", "limit", "offset", "symbol"}, propertyNames(t, def))
	// The nested files[].item schema must survive compaction verbatim.
	filesSchema, ok := def.Parameters["properties"].(map[string]interface{})["files"].(map[string]interface{})
	require.True(t, ok)
	items, ok := filesSchema["items"].(map[string]interface{})
	require.True(t, ok)
	itemProps, ok := items["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, itemProps, "file_path")
	require.Contains(t, itemProps, "offset")
	require.Contains(t, itemProps, "limit")
	require.Equal(t, []string{"file_path"}, items["required"])
}

func TestOptimizeModelToolSurfaceTodosShortDescription(t *testing.T) {
	def := collectSurfaceDefinitions(t, "todos")["todos"]
	require.Less(t, len(def.Description), 260, "todos description must be the compact phrase")
	require.Contains(t, def.Description, "in_progress")
}

// TestOptimizeModelToolSurfaceCompactToolWireBytes pins per-tool wire-size
// ceilings so future schema growth cannot silently regress the disclosure
// budget. Raise a ceiling deliberately (with a benchmark note), never silently.
func TestOptimizeModelToolSurfaceCompactToolWireBytes(t *testing.T) {
	ceilings := map[string]int{
		"aicli_exec": 2600,
		"shell":      2800,
		"view":       1700,
		"todos":      1600,
	}
	defs := collectSurfaceDefinitions(t, "aicli_exec", "shell", "view", "todos")
	for name, ceiling := range ceilings {
		def := defs[name]
		raw, err := json.Marshal(map[string]interface{}{
			"name":        def.Name,
			"description": def.Description,
			"parameters":  def.Parameters,
		})
		require.NoError(t, err)
		require.Less(t, len(raw), ceiling,
			"%s wire size %d must stay under %d bytes", name, len(raw), ceiling)
	}
}
