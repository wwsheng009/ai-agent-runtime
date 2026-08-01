package providercompat

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// openCodeConsoleGoAdapter contains the narrowly scoped wire dialect changes
// required by the OpenCode Console Go gateway. The profile is explicit so the
// standard OpenAI and Responses formats are never rewritten by inference from
// a model name.
type openCodeConsoleGoAdapter struct {
	BaseAdapter
}

func (openCodeConsoleGoAdapter) Name() string {
	return agentconfig.CompatibilityProfileOpenCodeConsoleGo
}

func (openCodeConsoleGoAdapter) Match(ctx Context) bool {
	return strings.EqualFold(
		strings.TrimSpace(ctx.Profile),
		agentconfig.CompatibilityProfileOpenCodeConsoleGo,
	)
}

// NormalizeOpenAICompatibleMessages translates outgoing developer messages to
// system and restores the standard OpenAI tool_calls type for OpenCode's Chat
// Completions dialect. It copies changed messages and never mutates
// canonical/runtime history.
func (openCodeConsoleGoAdapter) NormalizeOpenAICompatibleMessages(ctx Context, messages []map[string]interface{}) ([]map[string]interface{}, bool) {
	if ctx.Protocol != "openai" || len(messages) == 0 {
		return messages, false
	}

	var normalized []map[string]interface{}
	for index, message := range messages {
		role, _ := message["role"].(string)
		updated := map[string]interface{}(nil)
		if strings.EqualFold(strings.TrimSpace(role), "developer") {
			if normalized == nil {
				normalized = append([]map[string]interface{}(nil), messages...)
			}
			updated = cloneMapStringAny(message)
			updated["role"] = "system"
			normalized[index] = updated
		}
		if fixedToolCalls, changed := normalizeOpenCodeToolCallsType(message["tool_calls"]); changed {
			if normalized == nil {
				normalized = append([]map[string]interface{}(nil), messages...)
			}
			if updated == nil {
				updated = cloneMapStringAny(message)
			}
			updated["tool_calls"] = fixedToolCalls
			normalized[index] = updated
		}
	}
	if normalized == nil {
		return messages, false
	}
	return normalized, true
}

// NormalizeAnthropicCompatibleMessages adapts outgoing messages to the
// Anthropic Messages API dialect the Console Go upstream accepts:
//
//   - residual (non-leading) system/developer instruction messages are
//     projected to user text blocks, because Anthropic message roles are
//     user/assistant only and the upstream rejects unknown roles;
//   - every user message with a plain-string content is rewritten to a single
//     text content block, because the upstream rejects the string shorthand
//     with HTTP 400 while the standard Anthropic Messages API accepts both
//     shapes (a string is defined as shorthand for one text block).
//
// Leading system/developer messages are left untouched so the adapter can fold
// them into the top-level system field. It copies changed messages and never
// mutates canonical/runtime history.
func (openCodeConsoleGoAdapter) NormalizeAnthropicCompatibleMessages(ctx Context, messages []map[string]interface{}) ([]map[string]interface{}, bool) {
	if ctx.Protocol != "anthropic" || len(messages) == 0 {
		return messages, false
	}

	normalized := append([]map[string]interface{}(nil), messages...)
	inLeadingInstructions := true
	writeIndex := 0
	changed := false
	for _, message := range messages {
		role, _ := message["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		isInstruction := role == "system" || role == "developer"
		if isInstruction && inLeadingInstructions {
			normalized[writeIndex] = message
			writeIndex++
			continue
		}
		inLeadingInstructions = false

		switch {
		case isInstruction:
			text := anthropicInstructionTextForCompat(message)
			if text == "" {
				changed = true
				continue
			}
			updated := cloneMapStringAny(message)
			updated["role"] = "user"
			updated["content"] = []map[string]interface{}{
				{"type": "text", "text": text},
			}
			normalized[writeIndex] = updated
			writeIndex++
			changed = true
		case role == "user":
			content, ok := message["content"].(string)
			if !ok || strings.TrimSpace(content) == "" {
				normalized[writeIndex] = message
				writeIndex++
				continue
			}
			updated := cloneMapStringAny(message)
			updated["content"] = []map[string]interface{}{
				{"type": "text", "text": content},
			}
			normalized[writeIndex] = updated
			writeIndex++
			changed = true
		default:
			normalized[writeIndex] = message
			writeIndex++
		}
	}
	normalized = normalized[:writeIndex]
	if !changed {
		return messages, false
	}
	return normalized, true
}

// anthropicInstructionTextForCompat extracts the plain text of a system or
// developer instruction message, mirroring the adapter's instruction folding.
func anthropicInstructionTextForCompat(message map[string]interface{}) string {
	if len(message) == 0 {
		return ""
	}
	switch typed := message["content"].(type) {
	case string:
		return strings.TrimSpace(typed)
	case []map[string]interface{}:
		return anthropicInstructionTextBlocksForCompat(typed)
	case []interface{}:
		parts := make([]map[string]interface{}, 0, len(typed))
		for _, raw := range typed {
			if part, ok := raw.(map[string]interface{}); ok {
				parts = append(parts, part)
			}
		}
		return anthropicInstructionTextBlocksForCompat(parts)
	default:
		return ""
	}
}

func anthropicInstructionTextBlocksForCompat(blocks []map[string]interface{}) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// normalizeOpenCodeToolCallsType restores the strict OpenAI enum value
// "function" on every tool_calls entry. Some upstreams (and raw history replay
// from legacy providers) emit "function_call"; the OpenCode Console Go gateway
// validates the enum and rejects the non-standard spelling with HTTP 400. The
// returned slice is a deep-enough copy (tool call map + nested function map) so
// canonical history is never mutated.
func normalizeOpenCodeToolCallsType(raw interface{}) ([]map[string]interface{}, bool) {
	switch calls := raw.(type) {
	case []map[string]interface{}:
		return normalizeOpenCodeToolCallsMaps(calls)
	case []interface{}:
		maps := make([]map[string]interface{}, 0, len(calls))
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]interface{})
			if !ok {
				return nil, false
			}
			maps = append(maps, call)
		}
		return normalizeOpenCodeToolCallsMaps(maps)
	default:
		return nil, false
	}
}

func normalizeOpenCodeToolCallsMaps(calls []map[string]interface{}) ([]map[string]interface{}, bool) {
	if len(calls) == 0 {
		return nil, false
	}

	var normalized []map[string]interface{}
	for index, call := range calls {
		if strings.EqualFold(strings.TrimSpace(stringValue(call["type"])), "function") {
			continue
		}
		if normalized == nil {
			normalized = append([]map[string]interface{}(nil), calls...)
		}
		cloned := cloneMapStringAny(call)
		if function, ok := call["function"].(map[string]interface{}); ok {
			cloned["function"] = cloneMapStringAny(function)
		}
		cloned["type"] = "function"
		normalized[index] = cloned
	}
	if normalized == nil {
		return nil, false
	}
	return normalized, true
}

// PrepareRequestBody flattens an assistant Responses message only when every
// content item is a losslessly concatenable output_text part. Any mixed,
// unknown, image, reasoning, or tool content stays in the standard shape.
func (openCodeConsoleGoAdapter) PrepareRequestBody(ctx Context, body map[string]interface{}) (map[string]interface{}, bool) {
	if ctx.Protocol != "codex" || len(body) == 0 {
		return body, false
	}

	switch input := body["input"].(type) {
	case []map[string]interface{}:
		normalized, changed := normalizeOpenCodeResponsesInputMaps(input)
		if !changed {
			return body, false
		}
		updated := cloneMapStringAny(body)
		updated["input"] = normalized
		return updated, true
	case []interface{}:
		normalized, changed := normalizeOpenCodeResponsesInputAny(input)
		if !changed {
			return body, false
		}
		updated := cloneMapStringAny(body)
		updated["input"] = normalized
		return updated, true
	default:
		return body, false
	}
}

func normalizeOpenCodeResponsesInputMaps(input []map[string]interface{}) ([]map[string]interface{}, bool) {
	var normalized []map[string]interface{}
	for index, item := range input {
		flattened, changed := flattenOpenCodeAssistantContent(item)
		if !changed {
			continue
		}
		if normalized == nil {
			normalized = append([]map[string]interface{}(nil), input...)
		}
		normalized[index] = flattened
	}
	return normalized, normalized != nil
}

func normalizeOpenCodeResponsesInputAny(input []interface{}) ([]interface{}, bool) {
	var normalized []interface{}
	for index, rawItem := range input {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		flattened, changed := flattenOpenCodeAssistantContent(item)
		if !changed {
			continue
		}
		if normalized == nil {
			normalized = append([]interface{}(nil), input...)
		}
		normalized[index] = flattened
	}
	return normalized, normalized != nil
}

func flattenOpenCodeAssistantContent(item map[string]interface{}) (map[string]interface{}, bool) {
	if !strings.EqualFold(strings.TrimSpace(stringValue(item["type"])), "message") ||
		!strings.EqualFold(strings.TrimSpace(stringValue(item["role"])), "assistant") {
		return item, false
	}

	content, ok := outputTextContent(item["content"])
	if !ok {
		return item, false
	}
	updated := cloneMapStringAny(item)
	updated["content"] = content
	return updated, true
}

func outputTextContent(raw interface{}) (string, bool) {
	var parts []map[string]interface{}
	switch typed := raw.(type) {
	case []map[string]interface{}:
		parts = typed
	case []interface{}:
		parts = make([]map[string]interface{}, 0, len(typed))
		for _, rawPart := range typed {
			part, ok := rawPart.(map[string]interface{})
			if !ok {
				return "", false
			}
			parts = append(parts, part)
		}
	default:
		return "", false
	}
	if len(parts) == 0 {
		return "", false
	}

	var text strings.Builder
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(stringValue(part["type"])), "output_text") {
			return "", false
		}
		value, ok := part["text"].(string)
		if !ok {
			return "", false
		}
		text.WriteString(value)
	}
	return text.String(), true
}

func stringValue(value interface{}) string {
	result, _ := value.(string)
	return result
}
