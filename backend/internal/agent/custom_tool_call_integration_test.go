package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestCodexCustomApplyPatchCallExecutesEndToEnd(t *testing.T) {
	root := t.TempDir()
	patchText := "*** Begin Patch\n*** Add File: hello.txt\n+hello from custom tool\n*** End Patch"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvent := func(event string, payload map[string]interface{}) {
			encoded, err := json.Marshal(payload)
			require.NoError(t, err)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
		}
		writeEvent("response.created", map[string]interface{}{
			"type":     "response.created",
			"response": map[string]interface{}{"id": "resp_patch", "model": "gpt-5.4"},
		})
		writeEvent("response.output_item.added", map[string]interface{}{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]interface{}{
				"type": "custom_tool_call", "id": "item_patch", "call_id": "call_patch",
				"name": "apply_patch", "input": "", "status": "in_progress",
			},
		})
		writeEvent("response.custom_tool_call_input.done", map[string]interface{}{
			"type": "response.custom_tool_call_input.done", "output_index": 0,
			"item_id": "item_patch", "call_id": "call_patch", "input": patchText,
		})
		writeEvent("response.output_item.done", map[string]interface{}{
			"type": "response.output_item.done", "output_index": 0,
			"item": map[string]interface{}{
				"type": "custom_tool_call", "id": "item_patch", "call_id": "call_patch",
				"name": "apply_patch", "input": patchText, "status": "completed",
			},
		})
		writeEvent("response.completed", map[string]interface{}{
			"type":     "response.completed",
			"response": map[string]interface{}{"id": "resp_patch", "status": "completed", "stop_reason": "tool_call"},
		})
	}))
	defer server.Close()

	provider, err := llm.NewProvider(&llm.ProviderConfig{
		Type: "codex", BaseURL: server.URL, DefaultModel: "gpt-5.4", MaxRetries: 0,
	})
	require.NoError(t, err)

	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(nil, &runtimecfg.RuntimeConfig{
		Workspace: runtimecfg.WorkspaceConfig{Enabled: true, Root: root},
	}))
	info, err := manager.FindTool("apply_patch")
	require.NoError(t, err)
	response, err := provider.Call(context.Background(), &llm.LLMRequest{
		Model: "gpt-5.4", Stream: true,
		Messages: []types.Message{{Role: "user", Content: "add hello.txt"}},
		Tools: []types.ToolDefinition{{
			Name: info.Name, Description: info.Description, Parameters: info.InputSchema, Metadata: info.Metadata,
		}},
	})
	require.NoError(t, err)
	require.Len(t, response.ToolCalls, 1)
	call := response.ToolCalls[0]
	require.Equal(t, "custom_tool_call", call.Type)
	require.Equal(t, patchText, call.RawInput)
	require.Equal(t, patchText, call.Args["_raw"])
	require.NotContains(t, call.Args, "_parse_error")

	agent := &Agent{
		config:     &Config{Name: "test-agent", Model: "gpt-5.4", Options: map[string]interface{}{"tool_base_path": root}},
		mcpManager: manager,
	}
	message, err := agent.ExecuteToolCall(context.Background(), "session_patch", call, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, message)

	content, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello from custom tool\n", string(content))
}

func TestFreeformPatchBindingRunsBeforeSandboxPolicy(t *testing.T) {
	root := t.TempDir()
	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil))
	agent := &Agent{config: &Config{Name: "sandbox-agent"}, mcpManager: manager}
	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), nil)
	outside := filepath.Join(root, "..", "outside.txt")
	raw := "*** Begin Patch\n*** Add File: " + outside + "\n+blocked\n*** End Patch"

	call := loop.bindFreeformToolCall(context.Background(), types.ToolCall{
		ID: "call_patch", Type: "custom_tool_call", Name: "apply_patch",
		Args: map[string]interface{}{"_raw": raw}, RawInput: raw,
	})
	require.Equal(t, raw, call.Args["patch"])
	require.NotContains(t, call.Args, "_raw")

	info, err := manager.FindTool("apply_patch")
	require.NoError(t, err)
	policy := runtimepolicy.NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled: true, AllowedPaths: []string{root},
	})
	require.Error(t, policy.AllowToolCall(info, call.Args))
}
