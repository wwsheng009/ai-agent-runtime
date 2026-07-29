package chatcore

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestCloneMessagePreservesCustomToolCallTransport(t *testing.T) {
	raw := "*** Begin Patch\n*** End Patch"
	message := &types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
		ID: "call_patch", Type: "custom_tool_call", Name: "apply_patch",
		Args: map[string]interface{}{"patch": raw}, RawInput: raw,
	}}}

	cloned := cloneMessage(message)
	require.NotSame(t, message, cloned)
	require.Len(t, cloned.ToolCalls, 1)
	require.Equal(t, "custom_tool_call", cloned.ToolCalls[0].Type)
	require.Equal(t, raw, cloned.ToolCalls[0].RawInput)
}
