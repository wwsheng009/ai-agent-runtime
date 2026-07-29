package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestToolBatchContextPreservesCustomToolCallTransport(t *testing.T) {
	raw := "*** Begin Patch\n*** End Patch"
	ctx := WithToolBatchContext(context.Background(), []types.ToolCall{{
		ID: "call_patch", Type: "custom_tool_call", Name: "apply_patch",
		Args: map[string]interface{}{"patch": raw}, RawInput: raw,
	}}, "call_patch", nil)

	batch, ok := ToolBatchContextFromContext(ctx)
	require.True(t, ok)
	require.Len(t, batch.ToolCalls, 1)
	require.Equal(t, "custom_tool_call", batch.ToolCalls[0].Type)
	require.Equal(t, raw, batch.ToolCalls[0].RawInput)
	require.Equal(t, raw, batch.ToolCalls[0].Args["patch"])
}
