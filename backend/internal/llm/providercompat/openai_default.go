package providercompat

import (
	"encoding/json"
	"strings"

	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

// NormalizeOpenAICompatibleMessages projects outgoing developer instructions
// to system for every OpenAI-compatible endpoint without an explicit wire
// profile. Developer is the canonical role for turn-context instructions
// (fact ledger, frozen goal, prompt layers), but many OpenAI-compatible
// gateways only accept the strict enum system/user/assistant/tool and reject
// developer with HTTP 400 (retryable=false). System is the universally
// accepted equivalent (OpenAI documents developer messages as the
// replacement for system), so projecting at send time avoids the 400 while
// keeping destination semantics close. Endpoints that distinguish developer
// from system can opt into a richer profile later. Explicit profiles such as
// opencode-console-go run earlier in the chain and already projected; this
// pass is idempotent. When a provider-specific adapter like sensenova merges
// system messages first, a developer instruction may land as its own system
// message rather than joining the merged system block — acceptable, since
// previously it went out verbatim and strict gateways rejected it. It copies
// changed messages and never mutates canonical/runtime history.
func (openAIDefaultAdapter) NormalizeOpenAICompatibleMessages(ctx Context, messages []map[string]interface{}) ([]map[string]interface{}, bool) {
	if !strings.EqualFold(strings.TrimSpace(ctx.Protocol), "openai") || len(messages) == 0 {
		return messages, false
	}
	normalized := []map[string]interface{}(nil)
	for index, message := range messages {
		role, _ := message["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "developer") {
			continue
		}
		if normalized == nil {
			normalized = append([]map[string]interface{}(nil), messages...)
		}
		updated := cloneMapStringAny(message)
		updated["role"] = "system"
		normalized[index] = updated
	}
	if normalized == nil {
		return messages, false
	}
	return normalized, true
}

func (openAIDefaultAdapter) NormalizeAssistantMessage(_ Context, message map[string]interface{}) (map[string]interface{}, bool) {
	return normalizeOpenAICompatibleAssistantMessage(message)
}

func (openAIDefaultAdapter) PrepareRequestBody(_ Context, body map[string]interface{}) (map[string]interface{}, bool) {
	return normalizeOpenAICompatibleRequestBody(body)
}

func (openAIDefaultAdapter) NormalizeProcessResult(_ Context, result *llmadapter.ProcessResult) bool {
	if result == nil {
		return false
	}
	changed := false
	if normalized, ok := normalizeOpenAICompatibleToolCalls(result.ToolCalls); ok {
		if toolCalls, ok := normalized.([]map[string]interface{}); ok {
			result.ToolCalls = toolCalls
			result.HasToolCalls = len(toolCalls) > 0
			changed = true
		}
	}
	if strings.TrimSpace(result.Reasoning) != "" && result.ReasoningBlock == nil {
		result.ReasoningBlock = &runtimetypes.ReasoningBlock{
			Format:     "openai_compatible",
			Summary:    strings.TrimSpace(result.Reasoning),
			Streamable: true,
			Visibility: runtimetypes.ReasoningVisibilitySummary,
		}
		changed = true
	}
	return changed
}

func (openAIDefaultAdapter) NormalizeStreamChunk(_ Context, chunk map[string]interface{}) (map[string]interface{}, bool) {
	return normalizeOpenAICompatibleStreamChunk(chunk)
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

func normalizeOpenAICompatibleRequestBody(body map[string]interface{}) (map[string]interface{}, bool) {
	if len(body) == 0 {
		return body, false
	}
	messages := decodeSliceOfMaps(body["messages"])
	if len(messages) == 0 {
		return body, false
	}

	normalizedMessages := make([]map[string]interface{}, len(messages))
	changed := false
	for i, message := range messages {
		normalized, ok := normalizeOpenAICompatibleAssistantMessage(message)
		normalizedMessages[i] = normalized
		changed = changed || ok
	}
	if !changed {
		return body, false
	}
	normalizedBody := cloneMapStringAny(body)
	normalizedBody["messages"] = normalizedMessages
	return normalizedBody, true
}

func normalizeOpenAICompatibleStreamChunk(chunk map[string]interface{}) (map[string]interface{}, bool) {
	if len(chunk) == 0 {
		return chunk, false
	}
	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return chunk, false
	}

	normalizedChunk := chunk
	changed := false
	normalizedChoices := make([]interface{}, len(choices))
	copy(normalizedChoices, choices)

	for i, choice := range choices {
		choiceMap, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}
		delta, ok := choiceMap["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		normalizedDelta, deltaChanged := normalizeOpenAICompatibleDelta(delta)
		if !deltaChanged {
			continue
		}
		nextChoice := cloneMapStringAny(choiceMap)
		nextChoice["delta"] = normalizedDelta
		normalizedChoices[i] = nextChoice
		if !changed {
			normalizedChunk = cloneMapStringAny(chunk)
			changed = true
		}
	}

	if changed {
		normalizedChunk["choices"] = normalizedChoices
	}
	return normalizedChunk, changed
}

func normalizeOpenAICompatibleDelta(delta map[string]interface{}) (map[string]interface{}, bool) {
	if len(delta) == 0 {
		return delta, false
	}
	normalized := delta
	changed := false
	ensureMutable := func() map[string]interface{} {
		if !changed {
			normalized = cloneMapStringAny(delta)
			changed = true
		}
		return normalized
	}

	if reasoning, ok := delta["reasoning"].(string); ok {
		if _, exists := delta["reasoning_content"]; !exists {
			ensureMutable()["reasoning_content"] = reasoning
		}
	}
	if toolCalls, ok := normalizeOpenAICompatibleStreamToolCalls(delta["tool_calls"]); ok {
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

func normalizeOpenAICompatibleStreamToolCalls(raw interface{}) (interface{}, bool) {
	switch calls := raw.(type) {
	case []map[string]interface{}:
		normalized := make([]map[string]interface{}, len(calls))
		changed := false
		for i, call := range calls {
			next, ok := normalizeOpenAICompatibleStreamToolCall(call)
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
			next, ok := normalizeOpenAICompatibleStreamToolCall(callMap)
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
		if rawType, hasType := mutable["type"]; !hasType || !isOpenAICompatibleToolCallType(rawType) {
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
	if rawType, hasType := call["type"]; !hasType || !isOpenAICompatibleToolCallType(rawType) {
		ensureMutable()["type"] = "function"
	}
	return normalized, changed
}

func normalizeOpenAICompatibleStreamToolCall(call map[string]interface{}) (map[string]interface{}, bool) {
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
			"name": strings.TrimSpace(name),
		}
		if arguments, ok := normalizeOpenAICompatibleStreamToolArguments(call["arguments"]); ok {
			function["arguments"] = arguments
		}
		mutable := ensureMutable()
		mutable["function"] = function
		if rawType, hasType := mutable["type"]; !hasType || !isOpenAICompatibleToolCallType(rawType) {
			mutable["type"] = "function"
		}
		return normalized, true
	}

	normalizedFunction := function
	if arguments, ok := normalizeOpenAICompatibleStreamToolArguments(function["arguments"]); ok {
		normalizedFunction = cloneMapStringAny(function)
		normalizedFunction["arguments"] = arguments
		ensureMutable()["function"] = normalizedFunction
	}
	if rawType, hasType := call["type"]; !hasType || !isOpenAICompatibleToolCallType(rawType) {
		ensureMutable()["type"] = "function"
	}
	return normalized, changed
}

// isOpenAICompatibleToolCallType reports whether a tool_calls[].type value is
// already acceptable on the OpenAI Chat Completions wire. The standard enum is
// "function"; "custom_tool_call" is the project's own extension used by the
// codex protocol for custom tools and is deliberately left untouched. Any other
// spelling (notably "function_call", the Responses API item type that leaks in
// when raw history is replayed) is coerced to "function".
func isOpenAICompatibleToolCallType(raw interface{}) bool {
	toolType := strings.ToLower(strings.TrimSpace(stringValue(raw)))
	return toolType == "function" || toolType == "custom_tool_call"
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

func normalizeOpenAICompatibleStreamToolArguments(raw interface{}) (string, bool) {
	switch value := raw.(type) {
	case nil:
		return "", false
	case string:
		return value, false
	default:
		payload, err := json.Marshal(value)
		if err != nil || len(payload) == 0 || string(payload) == "null" {
			return "", false
		}
		return string(payload), true
	}
}
