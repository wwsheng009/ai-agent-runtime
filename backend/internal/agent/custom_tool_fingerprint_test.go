package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPromptMessageFingerprintIncludesCustomToolTransport(t *testing.T) {
	base := types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
		ID: "call_patch", Name: "apply_patch", Args: map[string]interface{}{"patch": "same"},
	}}}
	custom := *base.Clone()
	custom.ToolCalls[0].Type = "custom_tool_call"
	custom.ToolCalls[0].RawInput = "same"

	require.NotEqual(t, promptMessageFingerprint([]types.Message{base}), promptMessageFingerprint([]types.Message{custom}))
	changedRaw := *custom.Clone()
	changedRaw.ToolCalls[0].RawInput = "different"
	require.NotEqual(t, promptMessageFingerprint([]types.Message{custom}), promptMessageFingerprint([]types.Message{changedRaw}))
}
