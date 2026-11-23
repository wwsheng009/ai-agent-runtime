package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRecordMemoryReadIDsCollectsOnlySuccessfulReads(t *testing.T) {
	loop := &ReActLoop{}
	loop.recordMemoryReadIDs([]toolExecutionResult{
		{Call: types.ToolCall{Name: "memory_read", Args: map[string]interface{}{"id": "mem_2"}}},
		{Call: types.ToolCall{Name: "MEMORY_READ", Args: map[string]interface{}{"id": "mem_1"}}},
		{Call: types.ToolCall{Name: "memory_read", Args: map[string]interface{}{"id": "mem_denied"}}, Error: "denied"},
		{Call: types.ToolCall{Name: "memory_search", Args: map[string]interface{}{"query": "ignored"}}},
	})
	require.Equal(t, []string{"mem_1", "mem_2"}, loop.memoryReadIDsSnapshot())
	loop.resetMemoryReadIDs()
	require.Empty(t, loop.memoryReadIDsSnapshot())
}

func TestCoreContextIDIsNotAddedToOrdinaryMemoryIDs(t *testing.T) {
	metadata := map[string]interface{}{
		"project_core_id":      "core_reviewed",
		"project_memory_ids":   []string{"mem_record"},
		"memory_ids":           []interface{}{"mem_recall"},
		"project_core_version": int64(3),
	}
	require.Equal(t, []string{"core_reviewed"}, coreIDsFromContextMetadata(metadata))
	require.Equal(t, []string{"mem_record", "mem_recall"}, memoryIDsFromContextMetadata(metadata))
}
