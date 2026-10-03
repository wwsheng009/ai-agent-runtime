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

func TestResolveUnifiedTokenUsage_CodexInputDetailsCachedTokensDriveUncachedInput(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"codex",
		[]byte(`{"usage":{"input_tokens":82322,"input_tokens_details":{"cached_tokens":81408,"cache_write_tokens":0},"output_tokens":163,"output_tokens_details":{"reasoning_tokens":12},"total_tokens":82485}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 82322, usage.PromptTokens)
	require.Equal(t, 81408, usage.CachedTokens)
	require.Equal(t, 81408, usage.CacheReadTokens)
	require.Equal(t, 914, usage.UncachedInputTokens)
	require.Equal(t, 163, usage.CompletionTokens)
	require.Equal(t, 12, usage.ReasoningTokens)
}

func TestResolveUnifiedTokenUsage_DeepSeekCacheHitAndMissTokens(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"usage":{"prompt_tokens":16,"completion_tokens":10,"total_tokens":26,"prompt_tokens_details":{"cached_tokens":10},"prompt_cache_hit_tokens":10,"prompt_cache_miss_tokens":6}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 10, usage.CachedTokens)
	require.Equal(t, 10, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
	require.Equal(t, 6, usage.UncachedInputTokens)
}

func TestResolveUnifiedTokenUsage_AnthropicInputTokensAreAlreadyUncached(t *testing.T) {
	usage, _ := resolveUnifiedTokenUsage(
		"anthropic",
		[]byte(`{"usage":{"input_tokens":780,"output_tokens":28,"cache_read_input_tokens":512}}`),
		nil,
		nil,
		"",
		NewTokenizer("anthropic"),
	)
	require.NotNil(t, usage)
	// Anthropic 的 input_tokens 不含 cache read：未缓存输入就是 input_tokens 本身，
	// 不能拿它去减 cache_read_input_tokens。
	require.Equal(t, 780, usage.UncachedInputTokens)
	// 输入总量（比率分母）= input + cache_read + cache_creation。
	require.Equal(t, 1292, usage.InputTotalTokens)
}

func TestResolveUnifiedTokenUsage_GeminiCachedContent(t *testing.T) {
	usage, _ := resolveUnifiedTokenUsage(
		"gemini",
		[]byte(`{"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":20,"cachedContentTokenCount":900,"totalTokenCount":1020}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, 900, usage.CachedTokens)
	require.Equal(t, 900, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
	require.Equal(t, 100, usage.UncachedInputTokens)
	// Gemini 是包含式口径：promptTokenCount 已含缓存命中，输入总量即 prompt。
	require.Equal(t, 1000, usage.InputTotalTokens)
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

func TestResolveUnifiedTokenUsage_AnthropicSSESplitUsageMerged(t *testing.T) {
	body := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":780,"cache_creation_input_tokens":0,"cache_read_input_tokens":512,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":28}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")
	usage, source := resolveUnifiedTokenUsage("anthropic", body, nil, nil, "", NewTokenizer("anthropic"))
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	// message_start 的输入/缓存字段不能被 message_delta 的输出分片覆盖。
	require.Equal(t, 780, usage.PromptTokens)
	require.Equal(t, 28, usage.CompletionTokens)
	require.Equal(t, 512, usage.CacheReadTokens)
	require.Equal(t, 1320, usage.TotalTokens)
	// Anthropic 的 input_tokens 不含 cache read：未缓存输入就是输入本身。
	require.Equal(t, 780, usage.UncachedInputTokens)
	require.Equal(t, 1292, usage.InputTotalTokens)
}

func TestResolveUnifiedTokenUsage_SSEZeroUsageEventDoesNotOverride(t *testing.T) {
	body := []byte("data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"total_tokens\":105,\"prompt_tokens_details\":{\"cached_tokens\":80}}}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n")
	usage, _ := resolveUnifiedTokenUsage("openai", body, nil, nil, "", NewTokenizer("openai"))
	require.NotNil(t, usage)
	require.Equal(t, 100, usage.PromptTokens)
	require.Equal(t, 5, usage.CompletionTokens)
	require.Equal(t, 80, usage.CachedTokens)
	require.Equal(t, 20, usage.UncachedInputTokens)
}

func TestResolveUnifiedTokenUsage_GeminiSSECumulativeUsage(t *testing.T) {
	body := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"a\"}]}}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":5,\"totalTokenCount\":105}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"b\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1000,\"candidatesTokenCount\":20,\"cachedContentTokenCount\":900,\"totalTokenCount\":1020}}\n\n")
	usage, _ := resolveUnifiedTokenUsage("gemini", body, nil, nil, "", NewTokenizer("openai"))
	require.NotNil(t, usage)
	require.Equal(t, 1000, usage.PromptTokens)
	require.Equal(t, 20, usage.CompletionTokens)
	require.Equal(t, 900, usage.CachedTokens)
	require.Equal(t, 900, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
	require.Equal(t, 100, usage.UncachedInputTokens)
	require.Equal(t, 1020, usage.TotalTokens)
	require.Equal(t, 1000, usage.InputTotalTokens)
}

// 真实样本（chat-logs 2026-10-02）：DeepSeek 直接上报 hit/miss，miss 即未缓存输入。
func TestResolveUnifiedTokenUsage_DeepSeekRealSample(t *testing.T) {
	usage, source := resolveUnifiedTokenUsage(
		"openai",
		[]byte(`{"usage":{"prompt_tokens":113475,"completion_tokens":2582,"total_tokens":116057,"prompt_tokens_details":{"cached_tokens":113280},"completion_tokens_details":{"reasoning_tokens":1666},"prompt_cache_hit_tokens":113280,"prompt_cache_miss_tokens":195}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, usageSourceProviderReported, source)
	require.Equal(t, 113475, usage.PromptTokens)
	require.Equal(t, 113280, usage.CacheReadTokens)
	require.True(t, usage.CacheReadReported)
	require.Equal(t, 195, usage.UncachedInputTokens)
	require.Equal(t, 113475, usage.InputTotalTokens)
}

// 真实样本（chat-logs 2026-09-27）：Responses 的 cache_write 计入 input_tokens，
// 未缓存输入只扣缓存命中（cached_tokens），不扣写入。
func TestResolveUnifiedTokenUsage_CodexRealCacheWriteSample(t *testing.T) {
	usage, _ := resolveUnifiedTokenUsage(
		"codex",
		[]byte(`{"usage":{"input_tokens":19637,"input_tokens_details":{"cache_write_tokens":101,"cached_tokens":19533},"output_tokens":18,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":19655}}`),
		nil,
		nil,
		"",
		NewTokenizer("openai"),
	)
	require.NotNil(t, usage)
	require.Equal(t, 19637, usage.PromptTokens)
	require.Equal(t, 19533, usage.CachedTokens)
	require.Equal(t, 104, usage.UncachedInputTokens)
	require.Equal(t, 19637, usage.InputTotalTokens)
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
