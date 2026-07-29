package chat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPendingToolInvocationPreservesCustomToolTransport(t *testing.T) {
	raw := "*** Begin Patch\n*** End Patch"
	argsJSON := json.RawMessage(`{"patch":"*** Begin Patch\n*** End Patch"}`)
	ctx := runtimeagent.WithToolBatchContext(context.Background(), []types.ToolCall{{
		ID: "call_patch", Type: "custom_tool_call", Name: "apply_patch",
		Args: map[string]interface{}{"patch": raw}, RawInput: raw,
	}}, "call_patch", nil)

	pending, err := newPendingToolInvocation(ctx, "call_patch", "apply_patch", argsJSON)
	require.NoError(t, err)
	require.Equal(t, "custom_tool_call", pending.ToolType)
	require.Equal(t, raw, pending.RawInput)
	require.Len(t, pending.BatchToolCalls, 1)
	require.Equal(t, "custom_tool_call", pending.BatchToolCalls[0].ToolType)
	require.Equal(t, raw, pending.BatchToolCalls[0].RawInput)

	cloned := clonePendingToolInvocation(pending, false)
	require.Equal(t, pending.ToolType, cloned.ToolType)
	require.Equal(t, pending.RawInput, cloned.RawInput)
	require.Equal(t, pending.BatchToolCalls[0], cloned.BatchToolCalls[0])

	calls := pendingRuntimeToolCalls(nil, cloned)
	require.Len(t, calls, 1)
	require.Equal(t, "custom_tool_call", calls[0].Type)
	require.Equal(t, raw, calls[0].RawInput)
}

func TestApplyPendingToolPatchedArgsUpdatesCustomRawInput(t *testing.T) {
	pending := &PendingToolInvocation{
		ToolCallID: "call_patch", ToolType: "custom_tool_call", ToolName: "apply_patch",
		RawInput: "old", BatchToolCalls: []PendingToolCall{{
			ToolCallID: "call_patch", ToolType: "custom_tool_call", ToolName: "apply_patch", RawInput: "old",
		}},
	}
	patched := json.RawMessage(`{"patch":"new patch"}`)
	applyPendingToolPatchedArgs(pending, patched)

	require.JSONEq(t, string(patched), string(pending.ArgsJSON))
	require.Equal(t, "new patch", pending.RawInput)
	require.JSONEq(t, string(patched), string(pending.BatchToolCalls[0].ArgsJSON))
	require.Equal(t, "new patch", pending.BatchToolCalls[0].RawInput)
}
