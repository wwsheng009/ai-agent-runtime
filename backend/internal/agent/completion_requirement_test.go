package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestNormalizeCompletionRequirement(t *testing.T) {
	assert.Equal(t, CompletionRequirementNone, NormalizeCompletionRequirement(""))
	assert.Equal(t, CompletionRequirementNone, NormalizeCompletionRequirement("none"))
	assert.Equal(t, CompletionRequirementCompleteTask, NormalizeCompletionRequirement("complete_task"))
	assert.Equal(t, CompletionRequirementCompleteTask, NormalizeCompletionRequirement(" Complete-Task "))
	assert.Equal(t, CompletionRequirementNone, NormalizeCompletionRequirement("unknown"))
}

func TestHasSuccessfulTaskOutcomeObservation(t *testing.T) {
	assert.False(t, HasSuccessfulTaskOutcomeObservation(nil))
	assert.False(t, HasSuccessfulTaskOutcomeObservation([]types.Observation{
		{Tool: toolbroker.ToolReportTaskOutcome, Success: false},
		{Tool: "view", Success: true},
	}))
	assert.True(t, HasSuccessfulTaskOutcomeObservation([]types.Observation{
		{Tool: "view", Success: true},
		{Tool: toolbroker.ToolReportTaskOutcome, Success: true},
	}))
	assert.True(t, HasSuccessfulTaskOutcomeObservation([]types.Observation{
		{Tool: toolbroker.ToolBlockCurrentTask, Success: true},
	}))
}

func TestReActLoop_CompleteTaskRecoversThenSucceeds(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{Content: "I finished the analysis without calling the outcome tool.", Model: "test-model"},
			{
				Content: "Reporting structured completion.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-outcome",
					Name: toolbroker.ToolReportTaskOutcome,
					Args: map[string]interface{}{
						"task_status": "done",
						"summary":     "analysis complete",
					},
				}},
			},
			{Content: "Task outcome reported.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	// Intentionally omit toolBroker so report_task_outcome exercises the MCP path
	// and remains a unit test of completion recovery (not team store integration).
	agent := NewAgentWithLLM(&Config{
		Name: "worker", Provider: "test-provider", Model: "test-model", MaxSteps: 5,
	}, &completionOutcomeMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:              5,
		EnableToolCalls:       true,
		CompletionRequirement: CompletionRequirementCompleteTask,
	})

	result, err := loop.Run(context.Background(), "complete the worker task")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.NotNil(t, result.CompletionSatisfied)
	assert.True(t, *result.CompletionSatisfied)
	assert.Equal(t, 1, result.CompletionRecoveryTurns)
	require.GreaterOrEqual(t, len(provider.requests), 2)
	foundReminder := false
	for _, req := range provider.requests {
		for _, msg := range req.Messages {
			if msg.Role == "user" &&
				(strings.Contains(msg.Content, "this worker run requires a structured task completion") ||
					strings.Contains(msg.Content, `<system-reminder kind="completion_requirement">`)) {
				foundReminder = true
				assert.True(t, IsSystemReminder(msg), "completion recovery should use unified reminder channel")
				assert.Equal(t, ReminderKindCompletionRequirement, ReminderKindOf(msg))
			}
		}
	}
	assert.True(t, foundReminder, "expected completion requirement reminder in recovery turn")
	assert.True(t, HasSuccessfulTaskOutcomeObservation(result.Observations))
}

func TestReActLoop_CompleteTaskSkipsWhenAlreadyObserved(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Reporting completion first.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-outcome",
					Name: toolbroker.ToolReportTaskOutcome,
					Args: map[string]interface{}{"task_status": "done", "summary": "done"},
				}},
			},
			{Content: "All set.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "worker", Provider: "test-provider", Model: "test-model", MaxSteps: 4,
	}, &completionOutcomeMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:              4,
		EnableToolCalls:       true,
		CompletionRequirement: CompletionRequirementCompleteTask,
	})

	result, err := loop.Run(context.Background(), "finish")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, result.CompletionSatisfied)
	assert.True(t, *result.CompletionSatisfied)
	assert.Zero(t, result.CompletionRecoveryTurns)
	assert.Equal(t, 2, len(provider.requests))
}

func TestReActLoop_CompleteTaskUnsatisfiedAfterRecovery(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{Content: "Done without tools.", Model: "test-model"},
			{Content: "Still done without tools after reminder.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "worker", Provider: "test-provider", Model: "test-model", MaxSteps: 4,
	}, &completionOutcomeMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:              4,
		EnableToolCalls:       true,
		CompletionRequirement: CompletionRequirementCompleteTask,
	})

	result, err := loop.Run(context.Background(), "finish")
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotNil(t, result.CompletionSatisfied)
	assert.False(t, *result.CompletionSatisfied)
	assert.Equal(t, 1, result.CompletionRecoveryTurns)
	assert.Contains(t, result.Error, "complete_task")
	assert.Equal(t, 2, len(provider.requests))
}

// completionOutcomeMCPManager is a minimal MCPManager for completion recovery tests.
// report_task_outcome is normally a broker tool; tests omit the broker so this path
// can mark a successful outcome observation without a team store.
type completionOutcomeMCPManager struct{}

func (m *completionOutcomeMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "completion-mcp",
	}, nil
}

func (m *completionOutcomeMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	_ = ctx
	_ = mcpName
	_ = args
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case toolbroker.ToolReportTaskOutcome, toolbroker.ToolBlockCurrentTask:
		return map[string]interface{}{
			"status":  "done",
			"outcome": "done",
			"summary": "ok",
		}, nil
	default:
		return map[string]interface{}{"ok": true}, nil
	}
}

func (m *completionOutcomeMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: toolbroker.ToolReportTaskOutcome, Description: "Report task outcome", Enabled: true, MCPName: "completion-mcp"},
		{Name: toolbroker.ToolBlockCurrentTask, Description: "Block current task", Enabled: true, MCPName: "completion-mcp"},
	}
}
