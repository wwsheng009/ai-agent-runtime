package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A terminal child result is input to the parent's work, not a request to stop
// it. Exercise the real loop across more than the former two-denial budget.
func TestReActLoop_ChildCompletionDoesNotStopParentWork(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "canceled", "timed_out"} {
		t.Run(status, func(t *testing.T) {
			wait := &toolbroker.AgentWaitResult{
				Agents: []toolbroker.AgentStatusResult{{
					ID: "child", SessionID: "child-session",
					CurrentTaskID: "task", CurrentTaskStatus: status,
					Output: "child result requiring parent review",
				}},
				Obligations: []toolbroker.AgentWaitObligation{{
					ObligationID: "batch-child", SubjectKind: "batch", Terminal: true,
				}},
			}
			toolbroker.AttachSynthesisPacket(wait)
			// Older hosts / persisted results may still send this field. It
			// must never regain authority over the parent's tool execution.
			wait.FinalizationRequired = true
			encoded, err := json.Marshal(wait)
			require.NoError(t, err)
			manager := &childCompletionMCPManager{waitOutput: string(encoded)}
			provider := &SequenceLLMProvider{name: "test-provider"}
			for i, name := range childCompletionToolNames {
				provider.responses = append(provider.responses, &llm.LLMResponse{
					Model: "test-model",
					ToolCalls: []types.ToolCall{{
						ID: name, Name: name,
						Args: map[string]interface{}{"sequence": i},
					}},
				})
			}
			provider.responses = append(provider.responses, &llm.LLMResponse{
				Model: "test-model", Content: "Parent integration and verification finished.",
			})
			runtime := llm.NewLLMRuntime(nil)
			require.NoError(t, runtime.RegisterProvider("test-provider", provider))
			ag := NewAgentWithLLM(&Config{
				Name: "parent", Provider: "test-provider", Model: "test-model",
			}, manager, runtime)
			loop := NewReActLoop(ag, runtime, &LoopReActConfig{
				MaxSteps: len(provider.responses), EnableToolCalls: true,
			})

			result, err := loop.Run(context.Background(), "Review the child result and finish the remaining parent work.")
			require.NoError(t, err)
			require.True(t, result.Success, "parent must finish normally: %+v", result)
			require.False(t, result.LimitReached)
			require.Empty(t, result.Error)
			require.Zero(t, result.ToolErrorCount)
			require.Equal(t, childCompletionToolNames, manager.calls)
			require.Equal(t, len(provider.responses), provider.callCount)
			require.Equal(t, "Parent integration and verification finished.", result.Output)
		})
	}
}

var childCompletionToolNames = []string{
	toolbroker.ToolSubagentWait, "apply_patch", "shell",
	toolbroker.ToolSubagentApplyWorktree, toolbroker.ToolSubagentClose, toolbroker.ToolSubagentSpawn,
}

type childCompletionMCPManager struct {
	MockMCPManager
	waitOutput string
	calls      []string
}

func (m *childCompletionMCPManager) ListTools() []skill.ToolInfo {
	var tools []skill.ToolInfo
	for _, name := range childCompletionToolNames {
		tools = append(tools, skill.ToolInfo{Name: name, Enabled: true})
	}
	return tools
}

func (m *childCompletionMCPManager) CallTool(_ interface{}, _, name string, _ map[string]interface{}) (interface{}, error) {
	m.calls = append(m.calls, name)
	if name == toolbroker.ToolSubagentWait {
		return m.waitOutput, nil
	}
	return "ok", nil
}
