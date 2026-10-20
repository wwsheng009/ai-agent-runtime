package hooks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPExecutorMergesInputAndParsesDecision(t *testing.T) {
	var gotServer, gotTool string
	var gotInput map[string]interface{}
	manager := NewManager([]HookConfig{{
		ID:    "mcp",
		Event: EventPreToolUse,
		Exec: ExecConfig{
			Type:   "mcp",
			Server: "lint",
			Tool:   "check",
			Input:  map[string]interface{}{"level": "strict"},
		},
	}})
	manager.SetMCPToolCaller(func(ctx context.Context, server, tool string, input map[string]interface{}) (interface{}, error) {
		gotServer = server
		gotTool = tool
		gotInput = input
		return map[string]interface{}{"decision": "block", "reason": "lint failed"}, nil
	})

	decision, err := manager.Dispatch(context.Background(), EventPreToolUse, map[string]interface{}{"tool_name": "Write"})
	require.NoError(t, err)
	require.Equal(t, DecisionBlock, decision.Action)
	require.Equal(t, "lint failed", decision.Message)
	require.Equal(t, "lint", gotServer)
	require.Equal(t, "check", gotTool)
	require.Equal(t, "strict", gotInput["level"])
	require.NotNil(t, gotInput["hook_input"])
}

func TestMCPExecutorNormalizesHostResultShapes(t *testing.T) {
	tests := []struct {
		name   string
		result interface{}
	}{
		{
			name:   "plain string from skill adapter",
			result: `{"decision":"block","reason":"adapter text"}`,
		},
		{
			name: "protocol envelope",
			result: map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": `{"decision":"block","reason":"envelope text"}`},
				},
			},
		},
		{
			name:   "raw json message",
			result: json.RawMessage(`{"decision":"block","reason":"raw text"}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			manager := NewManager([]HookConfig{{
				ID:    "mcp",
				Event: EventPreToolUse,
				Exec:  ExecConfig{Type: "mcp", Server: "lint", Tool: "check"},
			}})
			manager.SetMCPToolCaller(func(context.Context, string, string, map[string]interface{}) (interface{}, error) {
				return result, nil
			})

			decision, err := manager.Dispatch(context.Background(), EventPreToolUse, map[string]interface{}{"tool_name": "shell"})
			require.NoError(t, err)
			require.Equal(t, DecisionBlock, decision.Action)
			require.Contains(t, decision.Message, "text")
		})
	}
}
