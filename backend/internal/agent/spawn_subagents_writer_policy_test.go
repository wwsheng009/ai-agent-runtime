package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// The model-visible spawn_subagents contract must carry the writer governance
// text and the write_paths field: the arbiter only rewards callers that know
// disjoint declarations restore parallelism (see internal/agent/scheduler.go).
func TestSpawnSubagentsSchemaAdvertisesWriterPolicy(t *testing.T) {
	definition := spawnSubagentsToolDefinition(true, "", DefaultCapabilities())
	assert.Contains(t, definition.Description, runtimepolicy.SubagentWriterPolicyDescription)

	parameters, ok := definition.Parameters["properties"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents Parameters.properties missing")
	agents, ok := parameters["agents"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents property missing")
	items, ok := agents["items"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents items missing")
	itemProperties, ok := items["properties"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents item properties missing")

	writePaths, ok := itemProperties["write_paths"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents[].write_paths must be advertised")
	assert.Contains(t, writePaths["description"], "disjoint")

	readOnly, ok := itemProperties["read_only"].(map[string]interface{})
	require.True(t, ok, "spawn_subagents agents[].read_only missing")
	assert.Contains(t, readOnly["description"], "leave it unset for implementer/writer tasks")
}
