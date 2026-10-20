package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type hookRewriteMCPManager struct {
	calls int
	args  map[string]interface{}
}

func (m *hookRewriteMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true, MCPName: "test"}, nil
}

func (m *hookRewriteMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.calls++
	m.args = args
	return "ok", nil
}

func (m *hookRewriteMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{{Name: "read_file", Description: "read", Enabled: true, MCPName: "test"}}
}

func TestReActLoopPreToolUseHookRewritesArgs(t *testing.T) {
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["hook_event_name"] != "PreToolUse" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "allow",
    "updatedInput": {"path": "safe.txt"},
    "additionalContext": "hook says hello"
  }
}`))
	}))
	defer hookServer.Close()

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		defaultModel: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "run tool",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-1",
					Name: "read_file",
					Args: map[string]interface{}{"path": "unsafe.txt"},
				}},
			},
			{Content: "done", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	mcpManager := &hookRewriteMCPManager{}
	apiAgent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
	}, mcpManager, llmRuntime)
	apiAgent.SetHookManager(runtimehooks.NewManager([]runtimehooks.HookConfig{{
		ID:    "rewrite",
		Event: runtimehooks.EventPreToolUse,
		Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL, Method: http.MethodPost},
	}}))

	loop := NewReActLoop(apiAgent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableToolCalls: true,
	})
	result, err := loop.RunWithSession(context.Background(), "read it", newTestHistorySession("session-hook-rewrite"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, mcpManager.calls)
	require.Equal(t, "safe.txt", mcpManager.args["path"])
	require.Len(t, provider.requests, 2)
	require.Contains(t, joinedMessageContents(provider.requests[1].Messages), "hook says hello")
}
