package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	usageSourceProviderReported = "provider_reported"
	usageSourceLocalEstimate    = "local_estimate"
)

// ExtractTokenUsageFromResponseBody 从上游响应体中提取统一后的 token usage。
// 它会处理常见的顶层 usage、嵌套 response.usage、Gemini usageMetadata，
// 以及 SSE 流里最后一个携带 usage 的事件。
func ExtractTokenUsageFromResponseBody(body []byte) *types.TokenUsage {
	return extractUsageFromResponseBody(body)
}

// ExtractTokenUsageFromValue 从任意嵌套值中提取统一后的 token usage。
func ExtractTokenUsageFromValue(value interface{}) *types.TokenUsage {
	return normalizeUsageValue(value)
}

// TokenUsageToMap converts a normalized token usage into a map that preserves
// common provider aliases such as prompt/input and completion/output.
func TokenUsageToMap(usage *types.TokenUsage) map[string]interface{} {
	if usage == nil || usage.IsZero() {
		return nil
	}

	result := make(map[string]interface{}, 8)
	if usage.PromptTokens > 0 {
		result["prompt_tokens"] = usage.PromptTokens
		result["input_tokens"] = usage.PromptTokens
	}
	if usage.CompletionTokens > 0 {
		result["completion_tokens"] = usage.CompletionTokens
		result["output_tokens"] = usage.CompletionTokens
	}
	if usage.TotalTokens > 0 {
		result["total_tokens"] = usage.TotalTokens
	}
	if usage.CachedTokens > 0 {
		result["cached_tokens"] = usage.CachedTokens
	}
	cacheReadTokens := usage.CacheReadTokens
	if cacheReadTokens == 0 {
		cacheReadTokens = usage.CachedTokens
	}
	if cacheReadTokens > 0 {
		result["cache_read_input_tokens"] = cacheReadTokens
	}
	if usage.CacheCreationTokens > 0 {
		result["cache_creation_input_tokens"] = usage.CacheCreationTokens
	}
	if usage.CacheReadReported {
		result["cache_read_reported"] = true
	}
	if usage.CacheCreationReported {
		result["cache_creation_reported"] = true
	}
	if usage.ReasoningTokens > 0 {
		result["reasoning_tokens"] = usage.ReasoningTokens
	}
	return result
}

func resolveUnifiedTokenUsage(
	protocol string,
	body []byte,
	assistantMsg map[string]interface{},
	requestMessages []types.Message,
	responseContent string,
	tokenizer *Tokenizer,
) (*types.TokenUsage, string) {
	if usage := extractUsageFromResponseBody(body); usage != nil {
		return usage, usageSourceProviderReported
	}
	if usage := normalizeUsageValue(assistantMsg["usage"]); usage != nil {
		return usage, usageSourceProviderReported
	}
	return estimateTokenUsage(protocol, tokenizer, requestMessages, responseContent), usageSourceLocalEstimate
}

func resolveUnifiedChatTokenUsage(
	protocol string,
	body []byte,
	assistantMsg map[string]interface{},
	requestMessages []Message,
	responseContent string,
	tokenizer *Tokenizer,
) (*types.TokenUsage, string) {
	if usage := extractUsageFromResponseBody(body); usage != nil {
		return usage, usageSourceProviderReported
	}
	if usage := normalizeUsageValue(assistantMsg["usage"]); usage != nil {
		return usage, usageSourceProviderReported
	}
	return estimateChatTokenUsage(protocol, tokenizer, requestMessages, responseContent), usageSourceLocalEstimate
}

func extractUsageFromResponseBody(body []byte) *types.TokenUsage {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}

	if looksLikeSSEPayload(trimmed) {
		return extractUsageFromSSEPayload(trimmed)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil
	}
	return normalizeUsageValue(payload)
}

func looksLikeSSEPayload(body []byte) bool {
	return bytes.Contains(body, []byte("\ndata:")) || bytes.HasPrefix(body, []byte("data:")) || bytes.Contains(body, []byte("\nevent:"))
}

func extractUsageFromSSEPayload(body []byte) *types.TokenUsage {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)

	var last *types.TokenUsage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		if usage := normalizeUsageValue(payload); usage != nil {
			last = usage
		}
	}
	return last
}

func normalizeUsageValue(value interface{}) *types.TokenUsage {
	switch raw := value.(type) {
	case map[string]interface{}:
		if usage := tokenUsageFromKnownFields(raw); usage != nil {
			return usage
		}
		for _, key := range []string{"usage", "usageMetadata", "response"} {
			if usage := normalizeUsageValue(raw[key]); usage != nil {
				return usage
			}
		}
		for _, nested := range raw {
			if usage := normalizeUsageValue(nested); usage != nil {
				return usage
			}
		}
	case map[string]int64:
		converted := make(map[string]interface{}, len(raw))
		for key, entry := range raw {
			converted[key] = entry
		}
		return normalizeUsageValue(converted)
	case map[string]int:
		converted := make(map[string]interface{}, len(raw))
		for key, entry := range raw {
			converted[key] = entry
		}
		return normalizeUsageValue(converted)
	case []interface{}:
		for _, entry := range raw {
			if usage := normalizeUsageValue(entry); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func tokenUsageFromKnownFields(raw map[string]interface{}) *types.TokenUsage {
	if len(raw) == 0 {
		return nil
	}

	// OpenAI-compatible providers commonly put cached/reasoning counts in
	// prompt_tokens_details and completion_tokens_details. Read those details
	// together with the top-level counters instead of returning early after the
	// first top-level field match.
	promptDetails := firstMapValue(raw, "prompt_tokens_details", "prompt_token_details", "input_tokens_details")
	completionDetails := firstMapValue(raw, "completion_tokens_details", "output_tokens_details")

	promptTokens := firstPositiveInt(
		raw["prompt_tokens"],
		raw["input_tokens"],
		raw["promptTokenCount"],
	)
	completionTokens := firstPositiveInt(
		raw["completion_tokens"],
		raw["output_tokens"],
		raw["candidatesTokenCount"],
	)
	promptCachedTokens := firstPositiveInt(
		raw["cached_tokens"],
		raw["cache_tokens"],
		raw["prompt_cached_tokens"],
		promptDetails["cached_tokens"],
		promptDetails["cache_tokens"],
		promptDetails["prompt_cached_tokens"],
	)
	cacheReadInputTokens := firstPositiveInt(
		raw["cache_read_input_tokens"],
		raw["cacheReadInputTokens"],
		promptDetails["cache_read_input_tokens"],
		promptDetails["cacheReadInputTokens"],
	)
	cacheCreationInputTokens := firstPositiveInt(
		raw["cache_creation_input_tokens"],
		raw["cacheCreationInputTokens"],
		promptDetails["cache_creation_input_tokens"],
		promptDetails["cacheCreationInputTokens"],
	)
	cacheReadReported := hasAnyMapKey(raw, "cached_tokens", "cache_tokens", "prompt_cached_tokens", "cache_read_input_tokens", "cacheReadInputTokens") ||
		hasAnyMapKey(promptDetails, "cached_tokens", "cache_tokens", "prompt_cached_tokens", "cache_read_input_tokens", "cacheReadInputTokens")
	cacheCreationReported := hasAnyMapKey(raw, "cache_creation_input_tokens", "cacheCreationInputTokens") ||
		hasAnyMapKey(promptDetails, "cache_creation_input_tokens", "cacheCreationInputTokens")
	if cacheReadInputTokens < promptCachedTokens {
		cacheReadInputTokens = promptCachedTokens
	}
	cachedTokens := cacheReadInputTokens
	reasoningTokens := firstPositiveInt(
		raw["reasoning_tokens"],
		raw["reasoningTokenCount"],
		raw["thinking_tokens"],
		completionDetails["reasoning_tokens"],
		completionDetails["reasoningTokenCount"],
		completionDetails["thinking_tokens"],
	)
	totalTokens := firstPositiveInt(
		raw["total_tokens"],
		raw["totalTokenCount"],
	)

	if promptTokens == 0 && completionTokens == 0 && totalTokens == 0 && cachedTokens == 0 && cacheCreationInputTokens == 0 && reasoningTokens == 0 {
		return nil
	}
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
		// OpenAI prompt_tokens already includes cached input; Anthropic's
		// input_tokens excludes cache read/creation input. Only add the latter
		// when the provider used input_tokens-style accounting.
		if _, hasInputTokens := raw["input_tokens"]; hasInputTokens {
			totalTokens += cacheReadInputTokens + cacheCreationInputTokens
		}
	}
	if promptTokens == 0 && totalTokens > completionTokens {
		promptTokens = totalTokens - completionTokens
	}
	if completionTokens == 0 && totalTokens > promptTokens {
		completionTokens = totalTokens - promptTokens
	}

	return &types.TokenUsage{
		PromptTokens:          promptTokens,
		CompletionTokens:      completionTokens,
		TotalTokens:           totalTokens,
		CachedTokens:          cachedTokens,
		CacheReadTokens:       cacheReadInputTokens,
		CacheCreationTokens:   cacheCreationInputTokens,
		CacheReadReported:     cacheReadReported,
		CacheCreationReported: cacheCreationReported,
		ReasoningTokens:       reasoningTokens,
	}
}

func firstMapValue(raw map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		if value, ok := raw[key].(map[string]interface{}); ok {
			return value
		}
	}
	return nil
}

func hasAnyMapKey(raw map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if _, ok := raw[key]; ok {
			return true
		}
	}
	return false
}

func estimateTokenUsage(protocol string, tokenizer *Tokenizer, requestMessages []types.Message, responseContent string) *types.TokenUsage {
	if tokenizer == nil {
		tokenizer = NewTokenizer(providerTokenizerStrategy(protocol))
	}

	promptTokens := 0
	completionTokens := 0
	if tokenizer != nil {
		promptTokens = countTypedMessagesTokens(tokenizer, requestMessages)
		completionTokens = tokenizer.Count(responseContent)
	}

	return &types.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func estimateChatTokenUsage(protocol string, tokenizer *Tokenizer, requestMessages []Message, responseContent string) *types.TokenUsage {
	if tokenizer == nil {
		tokenizer = NewTokenizer(providerTokenizerStrategy(protocol))
	}

	promptTokens := 0
	completionTokens := 0
	if tokenizer != nil {
		promptTokens = countChatMessagesTokens(tokenizer, requestMessages)
		completionTokens = tokenizer.Count(responseContent)
	}

	return &types.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func chatUsageFromTokenUsage(usage *types.TokenUsage) Usage {
	if usage == nil {
		return Usage{}
	}
	return Usage{
		PromptTokens:          usage.PromptTokens,
		CompletionTokens:      usage.CompletionTokens,
		TotalTokens:           usage.TotalTokens,
		CachedTokens:          usage.CachedTokens,
		CacheReadTokens:       usage.CacheReadTokens,
		CacheCreationTokens:   usage.CacheCreationTokens,
		CacheReadReported:     usage.CacheReadReported,
		CacheCreationReported: usage.CacheCreationReported,
		ReasoningTokens:       usage.ReasoningTokens,
	}
}

func firstPositiveInt(values ...interface{}) int {
	for _, value := range values {
		if number := intValue(value); number > 0 {
			return number
		}
	}
	return 0
}

func intValue(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	case uint:
		return int(v)
	case uint8:
		return int(v)
	case uint16:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}
