package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestResolveUnifiedTokenUsage_OpenAIJSON(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 3, usage.PromptTokens)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 7, usage.TotalTokens)
}

func TestResolveUnifiedTokenUsage_OpenAIJSONWithCachedAndReasoningTokens(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"cached_tokens":2,"reasoning_tokens":1}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 3, usage.PromptTokens)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 7, usage.TotalTokens)
	require.Equal(t, 2, usage.CachedTokens)
	require.Equal(t, 1, usage.ReasoningTokens)
}

func TestResolveUnifiedTokenUsage_AnthropicJSON(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"anthropic",
		[]byte(`{"usage":{"input_tokens":8,"output_tokens":2}}`),
		nil,
		nil,
		"",
		NewTokenizer("anthropic"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 8, usage.PromptTokens)
	require.Equal(t, 2, usage.CompletionTokens)
	require.Equal(t, 10, usage.TotalTokens)
}

func TestResolveUnifiedTokenUsage_GeminiJSON(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"gemini",
		[]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 10, usage.PromptTokens)
	require.Equal(t, 5, usage.CompletionTokens)
	require.Equal(t, 15, usage.TotalTokens)
}

func TestResolveUnifiedTokenUsage_SSEPayload(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2,\"total_tokens\":13}}\n\n"+
			"data: [DONE]\n\n"),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 11, usage.PromptTokens)
	require.Equal(t, 2, usage.CompletionTokens)
	require.Equal(t, 13, usage.TotalTokens)
}

func TestResolveUnifiedTokenUsage_CodexNestedSSEUsage(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"codex",
		[]byte("event: response.completed\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n"),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 12, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 15, usage.TotalTokens)
}

func TestResolveUnifiedTokenUsage_FallsBackToLocalEstimate(t *testing.T) {
	tokenizer := NewTokenizer("openai")
	messages := []types.Message{
		*types.NewUserMessage("hello"),
	}

	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`),
		nil,
		messages,
		"ok",
		tokenizer,
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceLocalEstimate, source)
	require.Greater(t, usage.PromptTokens, 0)
	require.Greater(t, usage.CompletionTokens, 0)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
}
