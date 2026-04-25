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
	totalTokens := firstPositiveInt(
		raw["total_tokens"],
		raw["totalTokenCount"],
	)

	if promptTokens == 0 && completionTokens == 0 && totalTokens == 0 {
		return nil
	}
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}
	if promptTokens == 0 && totalTokens > completionTokens {
		promptTokens = totalTokens - completionTokens
	}
	if completionTokens == 0 && totalTokens > promptTokens {
		completionTokens = totalTokens - promptTokens
	}

	return &types.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
}

func estimateTokenUsage(protocol string, tokenizer *Tokenizer, requestMessages []types.Message, responseContent string) *types.TokenUsage {
	if tokenizer == nil {
		tokenizer = NewTokenizer(providerTokenizerStrategy(protocol))
	}

	promptTokens := 0
	completionTokens := 0
	if tokenizer != nil {
		promptTokens = tokenizer.CountMessages(convertToInterfaceSlice(requestMessages))
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
		promptTokens = tokenizer.CountMessages(convertChatMessagesToInterfaceSlice(requestMessages))
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
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
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

func convertChatMessagesToInterfaceSlice(messages []Message) []interface{} {
	converted := make([]interface{}, len(messages))
	for i, msg := range messages {
		converted[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
			"name":    "",
		}
	}
	return converted
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
