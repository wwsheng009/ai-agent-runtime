package llm

import (
	"strings"
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

func TestResolveUnifiedTokenUsage_OpenAINestedUsageDetails(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"usage":{"prompt_tokens":13413,"completion_tokens":254,"total_tokens":13667,"prompt_tokens_details":{"cached_tokens":11008},"completion_tokens_details":{"reasoning_tokens":97}}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 13413, usage.PromptTokens)
	require.Equal(t, 254, usage.CompletionTokens)
	require.Equal(t, 13667, usage.TotalTokens)
	require.Equal(t, 11008, usage.CachedTokens)
	require.Equal(t, 11008, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
	require.Equal(t, 97, usage.ReasoningTokens)
}

func TestResolveUnifiedTokenUsage_OpenAIReportsZeroCacheRead(t *testing.T) {
	usage, _ := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"usage":{"prompt_tokens":11139,"completion_tokens":212,"total_tokens":11351,"prompt_tokens_details":{"cached_tokens":0}}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Zero(t, usage.CachedTokens)
	require.Zero(t, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
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

func TestResolveUnifiedTokenUsage_AnthropicCacheReadTokensCountTowardTotal(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"anthropic",
		[]byte(`{"usage":{"input_tokens":780,"output_tokens":28,"cache_read_input_tokens":512}}`),
		nil,
		nil,
		"",
		NewTokenizer("anthropic"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 780, usage.PromptTokens)
	require.Equal(t, 28, usage.CompletionTokens)
	require.Equal(t, 512, usage.CachedTokens)
	require.Equal(t, 512, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
	require.Equal(t, 1320, usage.TotalTokens)
}

func TestResolveUnifiedTokenUsage_AnthropicCacheCreationIsNotCacheHit(t *testing.T) {
	usage, _ := resolveUnifiedTokenUsage(
		"anthropic",
		[]byte(`{"usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":400}}`),
		nil,
		nil,
		"",
		NewTokenizer("anthropic"),
	)
	require.NotNil(t, usage)
	require.Zero(t, usage.CachedTokens)
	require.Zero(t, usage.CacheReadTokens)
	require.Equal(t, 400, usage.CacheCreationTokens)
	require.False(t, usage.CacheReadReported)
	require.True(t, usage.CacheCreationReported)
	require.Equal(t, 520, usage.TotalTokens)
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

func TestEstimateTokenUsageIncludesToolArgumentsAndStructuredContent(t *testing.T) {
	tokenizer := NewTokenizer("openai")
	message := types.Message{
		Role: "assistant",
		ContentParts: []types.ContentPart{{
			Type: types.ContentPartText,
			Text: strings.Repeat("structured content ", 20),
		}},
		ToolCalls: []types.ToolCall{{
			ID:   "call-1",
			Name: "write_file",
			Args: map[string]interface{}{"content": strings.Repeat("payload ", 40)},
		}},
		Metadata: types.NewMetadata(),
	}

	usage := estimateTokenUsage("openai", tokenizer, []types.Message{message}, "ok")
	flat := estimateTokenUsage("openai", tokenizer, []types.Message{{Role: "assistant"}}, "ok")
	require.Greater(t, usage.PromptTokens, flat.PromptTokens)
}

func TestEstimateChatTokenUsageIncludesStructuredReplay(t *testing.T) {
	tokenizer := NewTokenizer("openai")
	message := Message{
		Role: "assistant",
		ContentParts: []types.ContentPart{{
			Type: types.ContentPartText,
			Text: strings.Repeat("structured content ", 20),
		}},
		ToolCalls: []ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: ToolCallFunc{
				Name:      "write_file",
				Arguments: strings.Repeat("payload ", 40),
			},
		}},
		Reasoning: strings.Repeat("reasoning ", 20),
	}

	usage := estimateChatTokenUsage("openai", tokenizer, []Message{message}, "ok")
	flat := estimateChatTokenUsage("openai", tokenizer, []Message{{Role: "assistant"}}, "ok")
	require.Greater(t, usage.PromptTokens, flat.PromptTokens)
}

func TestTokenUsageToMap_PreservesCanonicalAndAliasFields(t *testing.T) {
	usageMap := TokenUsageToMap(&types.TokenUsage{
		PromptTokens:          11,
		CompletionTokens:      4,
		TotalTokens:           15,
		CachedTokens:          2,
		CacheReadTokens:       2,
		CacheCreationTokens:   5,
		CacheReadReported:     true,
		CacheCreationReported: true,
		ReasoningTokens:       3,
	})

	require.NotNil(t, usageMap)
	require.Equal(t, 11, usageMap["prompt_tokens"])
	require.Equal(t, 11, usageMap["input_tokens"])
	require.Equal(t, 4, usageMap["completion_tokens"])
	require.Equal(t, 4, usageMap["output_tokens"])
	require.Equal(t, 15, usageMap["total_tokens"])
	require.Equal(t, 2, usageMap["cached_tokens"])
	require.Equal(t, 2, usageMap["cache_read_input_tokens"])
	require.Equal(t, 5, usageMap["cache_creation_input_tokens"])
	require.Equal(t, true, usageMap["cache_read_reported"])
	require.Equal(t, true, usageMap["cache_creation_reported"])
	require.Equal(t, 3, usageMap["reasoning_tokens"])
}
