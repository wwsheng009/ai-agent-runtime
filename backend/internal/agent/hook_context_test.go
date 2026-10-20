package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func TestReActLoopInjectsHookAdditionalContextPromptOnly(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		defaultModel: "test-model",
		responses: []*llm.LLMResponse{
			{Content: "done", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 2, EnableToolCalls: false})
	session := newTestHistorySession("session-hook-context")

	ctx := WithHookAdditionalContext(context.Background(), "extra hook context")
	result, err := loop.RunWithSession(ctx, "finish the task", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, provider.requests, 1)

	request := joinedMessageContents(provider.requests[0].Messages)
	require.Contains(t, request, "extra hook context")
	persisted := joinedMessageContents(session.GetMessages())
	require.NotContains(t, persisted, "extra hook context")
}
