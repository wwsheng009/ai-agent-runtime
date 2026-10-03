package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sort"
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
	if usage.UncachedInputTokens > 0 {
		result["uncached_input_tokens"] = usage.UncachedInputTokens
	}
	if usage.InputTotalTokens > 0 {
		result["input_total_tokens"] = usage.InputTotalTokens
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

	// 跨事件合并原始 usage 后一次性归一化。不能逐事件归一化再取最后一个事件：
	// Anthropic 流式把 input_tokens/cache_* 放在 message_start、把累计
	// output_tokens 放在 message_delta，最后覆盖会丢掉输入侧全部字段
	// （OpenAI 只在末尾 chunk 给全量 usage，Gemini 每 chunk 给累计值，
	// 合并语义对这三者都成立）。
	var merged map[string]interface{}
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
		rawUsage := selectRawUsageMap(payload)
		if rawUsage == nil {
			continue
		}
		merged = mergeRawUsageMaps(merged, rawUsage)
	}
	if merged == nil {
		return nil
	}
	return tokenUsageFromKnownFields(merged)
}

// selectRawUsageMap 返回 value 中会被 tokenUsageFromKnownFields 识别的原始 usage
// 映射，查找顺序与 normalizeUsageValue 一致（先自身，再 usage/usageMetadata/
// response，最后任意嵌套值）；嵌套遍历按 key 排序保证确定性。
func selectRawUsageMap(value interface{}) map[string]interface{} {
	switch raw := value.(type) {
	case map[string]interface{}:
		if len(raw) == 0 {
			return nil
		}
		if tokenUsageFromKnownFields(raw) != nil {
			return raw
		}
		for _, key := range []string{"usage", "usageMetadata", "response"} {
			if nested := selectRawUsageMap(raw[key]); nested != nil {
				return nested
			}
		}
		keys := make([]string, 0, len(raw))
		for key := range raw {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if nested := selectRawUsageMap(raw[key]); nested != nil {
				return nested
			}
		}
	case map[string]int64:
		converted := make(map[string]interface{}, len(raw))
		for key, entry := range raw {
			converted[key] = entry
		}
		return selectRawUsageMap(converted)
	case map[string]int:
		converted := make(map[string]interface{}, len(raw))
		for key, entry := range raw {
			converted[key] = entry
		}
		return selectRawUsageMap(converted)
	case []interface{}:
		for _, entry := range raw {
			if nested := selectRawUsageMap(entry); nested != nil {
				return nested
			}
		}
	}
	return nil
}

// mergeRawUsageMaps 合并同一响应内多个事件的原始 usage：数值逐字段取最大
// （事件里的 usage 要么是累计总量，要么只携带分片字段），嵌套明细递归合并，
// 布尔取或。0 值事件不会覆盖先前已上报的字段。
func mergeRawUsageMaps(base, next map[string]interface{}) map[string]interface{} {
	if base == nil {
		return cloneRawUsageMap(next)
	}
	if next == nil {
		return base
	}
	merged := cloneRawUsageMap(base)
	for key, value := range next {
		existing, ok := merged[key]
		if !ok || existing == nil {
			merged[key] = value
			continue
		}
		merged[key] = mergeRawUsageValue(existing, value)
	}
	return merged
}

func mergeRawUsageValue(existing, value interface{}) interface{} {
	if value == nil {
		// 后续事件显式 null 不覆盖已上报字段（见 usage_normalizer 合并语义）。
		return existing
	}
	switch next := value.(type) {
	case map[string]interface{}:
		if prev, ok := existing.(map[string]interface{}); ok {
			return mergeRawUsageMaps(prev, next)
		}
	case bool:
		if prev, ok := existing.(bool); ok {
			return prev || next
		}
	default:
		if prevNumber, ok := rawUsageNumber(existing); ok {
			if nextNumber, ok := rawUsageNumber(value); ok {
				if nextNumber > prevNumber {
					return value
				}
				return existing
			}
		}
	}
	return value
}

func cloneRawUsageMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func rawUsageNumber(value interface{}) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	}
	return 0, false
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
		// DeepSeek: prompt_tokens = prompt_cache_hit_tokens + prompt_cache_miss_tokens，
		// hit 是输入总量里命中缓存的子集（等价 prompt_tokens_details.cached_tokens）。
		raw["prompt_cache_hit_tokens"],
		// Gemini usageMetadata: cachedContentTokenCount 是 promptTokenCount 的
		// 缓存命中子集（promptTokenCount 仍为含缓存的输入总量）。
		raw["cachedContentTokenCount"],
		promptDetails["cached_tokens"],
		promptDetails["cache_tokens"],
		promptDetails["prompt_cached_tokens"],
		promptDetails["prompt_cache_hit_tokens"],
		promptDetails["cachedContentTokenCount"],
	)
	// DeepSeek 显式上报未缓存输入（prompt_cache_miss_tokens），无需做减法。
	explicitUncachedInputTokens := firstPositiveInt(
		raw["prompt_cache_miss_tokens"],
		promptDetails["prompt_cache_miss_tokens"],
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
	cacheReadReported := hasAnyMapKey(raw, "cached_tokens", "cache_tokens", "prompt_cached_tokens", "cache_read_input_tokens", "cacheReadInputTokens", "prompt_cache_hit_tokens", "cachedContentTokenCount") ||
		hasAnyMapKey(promptDetails, "cached_tokens", "cache_tokens", "prompt_cached_tokens", "cache_read_input_tokens", "cacheReadInputTokens", "prompt_cache_hit_tokens", "cachedContentTokenCount")
	cacheCreationReported := hasAnyMapKey(raw, "cache_creation_input_tokens", "cacheCreationInputTokens") ||
		hasAnyMapKey(promptDetails, "cache_creation_input_tokens", "cacheCreationInputTokens")
	if cacheReadInputTokens < promptCachedTokens {
		cacheReadInputTokens = promptCachedTokens
	}
	cachedTokens := cacheReadInputTokens
	uncachedInputTokens := resolveUncachedInputTokens(promptTokens, promptCachedTokens, explicitUncachedInputTokens)
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
	// 不含式口径判定：Anthropic 系（含 DeepSeek-Anthropic 兼容端点）用
	// input_tokens 表示"新增输入"，且从不回报 total；OpenAI/Responses/Gemini
	// 都会同时回报 total（包含式口径），故以「有 input_tokens 但没有 total」
	// 作为不含式的判据，并与 total 的补算规则保持同一条件。
	_, hasInputTokens := raw["input_tokens"]
	exclusiveCacheInput := hasInputTokens && totalTokens == 0 && (cacheReadReported || cacheCreationReported)
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
		// OpenAI prompt_tokens already includes cached input; Anthropic's
		// input_tokens excludes cache read/creation input. Only add the latter
		// when the provider used input_tokens-style accounting.
		if exclusiveCacheInput {
			totalTokens += cacheReadInputTokens + cacheCreationInputTokens
		}
	}
	if promptTokens == 0 && totalTokens > completionTokens {
		promptTokens = totalTokens - completionTokens
	}
	if completionTokens == 0 && totalTokens > promptTokens {
		completionTokens = totalTokens - promptTokens
	}
	// 输入总量（比率分母）：包含式即 prompt；不含式的 prompt 只是新增输入，
	// 完整输入 = prompt + cache_read + cache_creation。
	inputTotalTokens := promptTokens
	if exclusiveCacheInput {
		inputTotalTokens = promptTokens + cacheReadInputTokens + cacheCreationInputTokens
	}

	return &types.TokenUsage{
		PromptTokens:          promptTokens,
		CompletionTokens:      completionTokens,
		TotalTokens:           totalTokens,
		CachedTokens:          cachedTokens,
		CacheReadTokens:       cacheReadInputTokens,
		CacheCreationTokens:   cacheCreationInputTokens,
		UncachedInputTokens:   uncachedInputTokens,
		InputTotalTokens:      inputTotalTokens,
		CacheReadReported:     cacheReadReported,
		CacheCreationReported: cacheCreationReported,
		ReasoningTokens:       reasoningTokens,
	}
}

// resolveUncachedInputTokens 统一「未缓存输入」口径（跨协议语义见 types.TokenUsage）：
//   - DeepSeek 等显式上报 miss 的协议直接采用；
//   - 报告了包含式缓存命中量（OpenAI prompt_tokens_details.cached_tokens /
//     Responses input_tokens_details.cached_tokens / Gemini cachedContentTokenCount）
//     时，未缓存输入 = 输入总量 - 命中量；
//   - 其余情况输入总量本身即未缓存输入：OpenAI 未上报任何缓存字段，或
//     Anthropic 的 input_tokens 本就不含 cache read/creation。
//
// 注意：不能用 raw["cache_read_input_tokens"] 参与减法——该字段是 Anthropic
// 的独立(不含式)口径，减它会得到负数或错误值。
func resolveUncachedInputTokens(promptTokens, inclusiveCachedTokens, explicitUncachedTokens int) int {
	if explicitUncachedTokens > 0 {
		return explicitUncachedTokens
	}
	if promptTokens <= 0 {
		return 0
	}
	if inclusiveCachedTokens > 0 {
		if promptTokens <= inclusiveCachedTokens {
			return 0
		}
		return promptTokens - inclusiveCachedTokens
	}
	return promptTokens
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
		UncachedInputTokens:   usage.UncachedInputTokens,
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
