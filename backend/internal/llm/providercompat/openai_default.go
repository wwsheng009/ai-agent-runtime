package providercompat

import (
	"encoding/json"
	"strings"
)

type openAIDefaultAdapter struct {
	BaseAdapter
}

func (openAIDefaultAdapter) Name() string {
	return "openai-default"
}

func (openAIDefaultAdapter) Match(ctx Context) bool {
	return ctx.Protocol == "openai"
}

func (openAIDefaultAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return []string{"low", "medium", "high", "xhigh", "none"}, true
}

func (openAIDefaultAdapter) LoginModelUsesDefaultReasoningEfforts(_ Context, modelID string) (bool, bool) {
	return LooksLikeOpenAIReasoningModel(modelID), true
}

func (openAIDefaultAdapter) NormalizeAssistantMessage(_ Context, message map[string]interface{}) (map[string]interface{}, bool) {
	return normalizeOpenAICompatibleAssistantMessage(message)
}

func normalizeOpenAICompatibleAssistantMessage(message map[string]interface{}) (map[string]interface{}, bool) {
	if len(message) == 0 {
		return message, false
	}

	normalized := message
	changed := false
	ensureMutable := func() map[string]interface{} {
		if !changed {
			normalized = cloneMapStringAny(message)
			changed = true
		}
		return normalized
	}

	if reasoning, ok := message["reasoning"].(string); ok {
		if _, exists := message["reasoning_content"]; !exists {
			ensureMutable()["reasoning_content"] = reasoning
		}
	}

	if toolCalls, ok := normalizeOpenAICompatibleToolCalls(message["tool_calls"]); ok {
		ensureMutable()["tool_calls"] = toolCalls
	}

	return normalized, changed
}

func normalizeOpenAICompatibleToolCalls(raw interface{}) (interface{}, bool) {
	switch calls := raw.(type) {
	case []map[string]interface{}:
		normalized := make([]map[string]interface{}, len(calls))
		changed := false
		for i, call := range calls {
			next, ok := normalizeOpenAICompatibleToolCall(call)
			normalized[i] = next
			changed = changed || ok
		}
		if changed {
			return normalized, true
		}
	case []interface{}:
		normalized := make([]interface{}, len(calls))
		changed := false
		for i, call := range calls {
			callMap, ok := call.(map[string]interface{})
			if !ok {
				normalized[i] = call
				continue
			}
			next, ok := normalizeOpenAICompatibleToolCall(callMap)
			normalized[i] = next
			changed = changed || ok
		}
		if changed {
			return normalized, true
		}
	}
	return raw, false
}

func normalizeOpenAICompatibleToolCall(call map[string]interface{}) (map[string]interface{}, bool) {
	if len(call) == 0 {
		return call, false
	}

	normalized := call
	changed := false
	ensureMutable := func() map[string]interface{} {
		if !changed {
			normalized = cloneMapStringAny(call)
			changed = true
		}
		return normalized
	}

	function, hasFunction := call["function"].(map[string]interface{})
	if !hasFunction {
		name, hasName := call["name"].(string)
		if !hasName || strings.TrimSpace(name) == "" {
			return call, false
		}
		function = map[string]interface{}{
			"name":      strings.TrimSpace(name),
			"arguments": "{}",
		}
		if arguments, ok := normalizeOpenAICompatibleToolArguments(call["arguments"]); ok {
			function["arguments"] = arguments
		}
		mutable := ensureMutable()
		mutable["function"] = function
		if _, hasType := mutable["type"]; !hasType {
			mutable["type"] = "function"
		}
		return normalized, true
	}

	normalizedFunction := function
	if arguments, ok := normalizeOpenAICompatibleToolArguments(function["arguments"]); ok {
		normalizedFunction = cloneMapStringAny(function)
		normalizedFunction["arguments"] = arguments
		ensureMutable()["function"] = normalizedFunction
	}
	if _, hasType := call["type"]; !hasType {
		ensureMutable()["type"] = "function"
	}
	return normalized, changed
}

func normalizeOpenAICompatibleToolArguments(raw interface{}) (string, bool) {
	switch value := raw.(type) {
	case nil:
		return "{}", true
	case string:
		normalized := NormalizeToolCallArguments(value)
		return normalized, normalized != value
	default:
		payload, err := json.Marshal(value)
		if err != nil || len(payload) == 0 || string(payload) == "null" {
			return "{}", true
		}
		return string(payload), true
	}
}
