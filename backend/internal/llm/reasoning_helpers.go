package llm

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	assistantReasoningDetailsKey         = "reasoning_details"
	reasoningMetadataCodexOutputItemsKey = "response_output_items"
	reasoningMetadataAnthropicBlocksKey  = "anthropic_content_blocks"
	reasoningMetadataGeminiPartsKey      = "gemini_parts"
	codexResponseOutputItemsMessageKey   = "response_output_items"
)

func attachReasoningToAssistantMessage(msg map[string]interface{}, reasoning *types.ReasoningBlock) map[string]interface{} {
	if len(msg) == 0 || reasoning == nil {
		return msg
	}
	if strings.TrimSpace(reasoning.DisplayText()) == "" && strings.TrimSpace(reasoning.OpaqueState) == "" && len(reasoning.Metadata) == 0 {
		return msg
	}
	if text := strings.TrimSpace(reasoning.DisplayText()); text != "" {
		if _, exists := msg["reasoning_content"]; !exists {
			msg["reasoning_content"] = text
		}
	}
	if outputItems := canonicalizeCodexOutputItems(reasoning.Metadata[reasoningMetadataCodexOutputItemsKey]); len(outputItems) > 0 {
		msg[codexResponseOutputItemsMessageKey] = outputItems
	}
	if encoded := reasoning.ToMap(); len(encoded) > 0 {
		if metadata := decodeMapAny(encoded["metadata"]); metadata != nil {
			if outputItems := canonicalizeCodexOutputItems(metadata[reasoningMetadataCodexOutputItemsKey]); len(outputItems) > 0 {
				metadata[reasoningMetadataCodexOutputItemsKey] = outputItems
				encoded["metadata"] = metadata
			}
		}
		msg[assistantReasoningDetailsKey] = encoded
	}
	return msg
}

// ReasoningBlockFromAssistantMessage 从 assistant 消息中恢复统一 reasoning 信息。
// 优先使用 reasoning_details，其次回退到 Codex 的 response_output_items，最后才使用纯文本 reasoning_content。
func ReasoningBlockFromAssistantMessage(msg map[string]interface{}) *types.ReasoningBlock {
	if len(msg) == 0 {
		return nil
	}
	if block := types.ReasoningBlockFromMap(msg[assistantReasoningDetailsKey]); block != nil {
		return block
	}
	if block := reasoningBlockFromCodexOutputItems(msg[codexResponseOutputItemsMessageKey]); block != nil {
		return block
	}
	if text, _ := msg["reasoning_content"].(string); strings.TrimSpace(text) != "" {
		return &types.ReasoningBlock{
			Summary:    strings.TrimSpace(text),
			Visibility: types.ReasoningVisibilitySummary,
		}
	}
	if text, _ := msg["reasoning"].(string); strings.TrimSpace(text) != "" {
		return &types.ReasoningBlock{
			Summary:    strings.TrimSpace(text),
			Visibility: types.ReasoningVisibilitySummary,
		}
	}
	return nil
}

func extractReasoningFromAssistantMessage(msg map[string]interface{}) *types.ReasoningBlock {
	return ReasoningBlockFromAssistantMessage(msg)
}

func reasoningFromMessageMetadata(metadata types.Metadata) *types.ReasoningBlock {
	return types.GetReasoningBlock(metadata)
}

func reasoningFromMapMetadata(metadata map[string]interface{}) *types.ReasoningBlock {
	if len(metadata) == 0 {
		return nil
	}
	return types.ReasoningBlockFromMap(metadata[assistantReasoningDetailsKey])
}

func reasoningBlockFromCodexOutputItems(raw interface{}) *types.ReasoningBlock {
	outputItems := canonicalizeCodexOutputItems(raw)
	if len(outputItems) == 0 {
		return nil
	}

	block := &types.ReasoningBlock{
		Format:     "openai_responses",
		Streamable: true,
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			reasoningMetadataCodexOutputItemsKey: outputItems,
		},
	}

	if summary := extractCodexReasoningSummary(outputItems); summary != "" {
		block.Summary = summary
		block.Visibility = types.ReasoningVisibilitySummary
	}

	return block
}

func extractCodexReasoningSummary(outputItems []map[string]interface{}) string {
	if len(outputItems) == 0 {
		return ""
	}

	parts := make([]string, 0, len(outputItems))
	for _, item := range outputItems {
		if item == nil {
			continue
		}
		itemType, _ := item["type"].(string)
		if itemType != "reasoning" {
			continue
		}
		if summary := extractCodexReasoningSummaryParts(item["summary"]); summary != "" {
			parts = append(parts, summary)
		}
	}

	return strings.Join(parts, "\n")
}

func extractCodexReasoningSummaryParts(raw interface{}) string {
	summaryParts := decodeSliceOfMaps(raw)
	if len(summaryParts) == 0 {
		return ""
	}

	parts := make([]string, 0, len(summaryParts))
	for _, part := range summaryParts {
		if part == nil {
			continue
		}
		if partType, _ := part["type"].(string); partType != "summary_text" {
			continue
		}
		if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}

	return strings.Join(parts, "\n")
}

func canonicalizeCodexOutputItems(raw interface{}) []map[string]interface{} {
	items := decodeSliceOfMaps(raw)
	if len(items) == 0 {
		return nil
	}

	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if canonical := canonicalizeCodexOutputItem(item); canonical != nil {
			out = append(out, canonical)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func canonicalizeCodexOutputItem(item map[string]interface{}) map[string]interface{} {
	if len(item) == 0 {
		return nil
	}

	itemType, _ := item["type"].(string)
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "reasoning":
		return canonicalizeCodexReasoningOutputItem(item)
	case "message":
		return canonicalizeCodexMessageOutputItem(item)
	case "function_call":
		return canonicalizeCodexFunctionCallOutputItem(item)
	case "function_call_output":
		return canonicalizeCodexFunctionCallResultOutputItem(item)
	default:
		return cloneCodexOutputMapDroppingKeys(item, "id", "status", "phase")
	}
}

func canonicalizeCodexReasoningOutputItem(item map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{
		"type": "reasoning",
	}
	if summary := canonicalizeCodexSummaryParts(item["summary"]); len(summary) > 0 {
		out["summary"] = summary
	} else if _, exists := item["summary"]; exists {
		out["summary"] = []map[string]interface{}{}
	}
	if encrypted, _ := item["encrypted_content"].(string); strings.TrimSpace(encrypted) != "" {
		out["encrypted_content"] = encrypted
		// Responses replay still expects reasoning.summary, even when the provider
		// only returned opaque encrypted_content and an empty summary array.
		if _, exists := out["summary"]; !exists {
			out["summary"] = []map[string]interface{}{}
		}
	}
	return out
}

func canonicalizeCodexMessageOutputItem(item map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{
		"type": "message",
		"role": "assistant",
	}
	if role, _ := item["role"].(string); strings.TrimSpace(role) != "" {
		out["role"] = strings.TrimSpace(role)
	}
	if content := canonicalizeCodexMessageContentParts(item["content"]); len(content) > 0 {
		out["content"] = content
	}
	return out
}

func canonicalizeCodexFunctionCallOutputItem(item map[string]interface{}) map[string]interface{} {
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) == "" {
		callID, _ = item["id"].(string)
	}

	name, _ := item["name"].(string)
	arguments, _ := item["arguments"].(string)
	if fn := decodeMapAny(item["function"]); fn != nil {
		if strings.TrimSpace(name) == "" {
			name, _ = fn["name"].(string)
		}
		if strings.TrimSpace(arguments) == "" {
			arguments, _ = fn["arguments"].(string)
		}
		if strings.TrimSpace(arguments) == "" {
			if encoded, ok := marshalStableJSONString(fn["arguments"]); ok {
				arguments = encoded
			}
		}
	}

	callID = strings.TrimSpace(callID)
	name = strings.TrimSpace(name)
	if callID == "" || name == "" {
		return cloneCodexOutputMapDroppingKeys(item, "id", "status", "phase")
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}

	return map[string]interface{}{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

func canonicalizeCodexFunctionCallResultOutputItem(item map[string]interface{}) map[string]interface{} {
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) == "" {
		callID, _ = item["id"].(string)
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return cloneCodexOutputMapDroppingKeys(item, "id", "status", "phase")
	}
	return map[string]interface{}{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  item["output"],
	}
}

func canonicalizeCodexSummaryParts(raw interface{}) []map[string]interface{} {
	parts := decodeSliceOfMaps(raw)
	if len(parts) == 0 {
		return nil
	}

	out := make([]map[string]interface{}, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		canonical := cloneCodexOutputMapDroppingKeys(part, "id", "status", "phase")
		if _, exists := canonical["type"]; !exists {
			continue
		}
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func canonicalizeCodexMessageContentParts(raw interface{}) []map[string]interface{} {
	parts := decodeSliceOfMaps(raw)
	if len(parts) == 0 {
		return nil
	}

	out := make([]map[string]interface{}, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		canonical := cloneCodexOutputMapDroppingKeys(part, "id", "status", "phase", "logprobs")
		if _, exists := canonical["type"]; !exists {
			continue
		}
		if annotations := decodeSliceOfMaps(canonical["annotations"]); len(annotations) == 0 {
			delete(canonical, "annotations")
		}
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneCodexOutputMapDroppingKeys(input map[string]interface{}, keys ...string) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	if len(keys) == 0 {
		cloned := make(map[string]interface{}, len(input))
		for key, value := range input {
			cloned[key] = value
		}
		return cloned
	}

	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}

	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		if _, skip := drop[key]; skip {
			continue
		}
		cloned[key] = value
	}
	return cloned
}

func marshalStableJSONString(value interface{}) (string, bool) {
	if value == nil {
		return "", false
	}
	payload, err := json.Marshal(value)
	if err != nil || len(payload) == 0 {
		return "", false
	}
	return string(payload), true
}

func decodeMapAny(raw interface{}) map[string]interface{} {
	switch typed := raw.(type) {
	case map[string]interface{}:
		if len(typed) == 0 {
			return nil
		}
		return typed
	case json.RawMessage:
		var decoded map[string]interface{}
		if err := json.Unmarshal(typed, &decoded); err == nil && len(decoded) > 0 {
			return decoded
		}
	case []byte:
		var decoded map[string]interface{}
		if err := json.Unmarshal(typed, &decoded); err == nil && len(decoded) > 0 {
			return decoded
		}
	}
	return nil
}

func runtimeMessageToAdapterMessage(msg types.Message, protocol string) map[string]interface{} {
	reasoning := reasoningFromMessageMetadata(msg.Metadata)
	toolCalls := encodeRuntimeToolCalls(msg.ToolCalls)
	return buildProtocolMessageMap(msg.Role, msg.Content, toolCalls, msg.ToolCallID, reasoning, protocol)
}

// RuntimeMessagesToProtocolMessages converts normalized runtime messages into
// protocol-specific request payloads.
func RuntimeMessagesToProtocolMessages(messages []types.Message, protocol string) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}

	result := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		result = append(result, runtimeMessageToAdapterMessage(msg, protocol))
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
		result = sanitizeCodexProtocolMessages(result)
	}
	return result
}

func sanitizeCodexProtocolMessages(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}

	seenToolCalls := make(map[string]struct{})
	filtered := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "assistant":
			filtered = append(filtered, msg)
			for id := range codexMessageToolCallIDs(msg) {
				seenToolCalls[id] = struct{}{}
			}
		case "tool":
			toolCallID, _ := msg["tool_call_id"].(string)
			toolCallID = strings.TrimSpace(toolCallID)
			if toolCallID == "" {
				continue
			}
			if _, ok := seenToolCalls[toolCallID]; !ok {
				continue
			}
			filtered = append(filtered, msg)
		default:
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

func codexMessageToolCallIDs(message map[string]interface{}) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, toolCall := range decodeSliceOfMaps(message["tool_calls"]) {
		if toolCall == nil {
			continue
		}
		if id, ok := toolCall["id"].(string); ok && strings.TrimSpace(id) != "" {
			ids[strings.TrimSpace(id)] = struct{}{}
			continue
		}
		if id, ok := toolCall["call_id"].(string); ok && strings.TrimSpace(id) != "" {
			ids[strings.TrimSpace(id)] = struct{}{}
		}
	}
	for _, item := range decodeSliceOfMaps(message[codexResponseOutputItemsMessageKey]) {
		if item == nil {
			continue
		}
		itemType, _ := item["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(itemType), "function_call") {
			continue
		}
		if id, ok := item["call_id"].(string); ok && strings.TrimSpace(id) != "" {
			ids[strings.TrimSpace(id)] = struct{}{}
			continue
		}
		if id, ok := item["id"].(string); ok && strings.TrimSpace(id) != "" {
			ids[strings.TrimSpace(id)] = struct{}{}
		}
	}
	return ids
}

func providerMessageToAdapterMessage(msg Message, protocol string) map[string]interface{} {
	reasoning := reasoningFromMapMetadata(msg.Metadata)
	if reasoning == nil && strings.TrimSpace(msg.Reasoning) != "" {
		reasoning = &types.ReasoningBlock{
			Summary:    strings.TrimSpace(msg.Reasoning),
			Visibility: types.ReasoningVisibilitySummary,
		}
	}
	toolCalls := encodeProviderToolCalls(msg.ToolCalls)
	return buildProtocolMessageMap(msg.Role, msg.Content, toolCalls, msg.ToolCallID, reasoning, protocol)
}

func buildProtocolMessageMap(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock, protocol string) map[string]interface{} {
	normalizedProtocol := strings.ToLower(strings.TrimSpace(protocol))
	switch normalizedProtocol {
	case "codex":
		return buildCodexProtocolMessage(role, content, toolCalls, toolCallID, reasoning)
	case "anthropic":
		return buildAnthropicProtocolMessage(role, content, toolCalls, toolCallID, reasoning)
	case "gemini":
		return buildGeminiProtocolMessage(role, content, toolCalls, toolCallID, reasoning)
	default:
		return buildOpenAIProtocolMessage(role, content, toolCalls, toolCallID, reasoning)
	}
}

func buildOpenAIProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock) map[string]interface{} {
	message := map[string]interface{}{
		"role":    role,
		"content": content,
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if strings.TrimSpace(toolCallID) != "" {
		message["tool_call_id"] = strings.TrimSpace(toolCallID)
	}
	return message
}

func buildCodexProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock) map[string]interface{} {
	message := buildOpenAIProtocolMessage(role, content, toolCalls, toolCallID, reasoning)
	if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return message
	}

	outputItems := codexProtocolOutputItems(content, toolCalls, reasoning)
	if len(outputItems) > 0 {
		message[codexResponseOutputItemsMessageKey] = outputItems
		delete(message, "reasoning_content")
		delete(message, "tool_calls")
		if strings.TrimSpace(content) == "" {
			message["content"] = ""
		}
	}
	return message
}

func codexProtocolOutputItems(content string, toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock) []map[string]interface{} {
	if reasoning != nil {
		if outputItems := canonicalizeCodexOutputItems(reasoning.Metadata[reasoningMetadataCodexOutputItemsKey]); len(outputItems) > 0 {
			return outputItems
		}
	}

	items := make([]map[string]interface{}, 0, len(toolCalls)+2)
	if reasoningText := codexReasoningSummary(reasoning); reasoningText != "" {
		items = append(items, map[string]interface{}{
			"type": "reasoning",
			"summary": []map[string]interface{}{
				{
					"type": "summary_text",
					"text": reasoningText,
				},
			},
		})
	}
	if text := strings.TrimSpace(content); text != "" {
		items = append(items, map[string]interface{}{
			"type": "message",
			"role": "assistant",
			"content": []map[string]interface{}{
				{
					"type": "output_text",
					"text": text,
				},
			},
		})
	}
	for _, toolCall := range toolCalls {
		if item := codexProtocolFunctionCallItem(toolCall); item != nil {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func codexReasoningSummary(reasoning *types.ReasoningBlock) string {
	if reasoning == nil {
		return ""
	}
	return strings.TrimSpace(reasoning.DisplayText())
}

func codexProtocolFunctionCallItem(toolCall map[string]interface{}) map[string]interface{} {
	if len(toolCall) == 0 {
		return nil
	}

	function, _ := toolCall["function"].(map[string]interface{})
	name, _ := function["name"].(string)
	if strings.TrimSpace(name) == "" {
		name, _ = toolCall["name"].(string)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}

	callID, _ := toolCall["id"].(string)
	if strings.TrimSpace(callID) == "" {
		callID, _ = toolCall["call_id"].(string)
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil
	}

	arguments, _ := function["arguments"].(string)
	if strings.TrimSpace(arguments) == "" {
		arguments, _ = toolCall["arguments"].(string)
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}

	return map[string]interface{}{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

func buildAnthropicProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock) map[string]interface{} {
	message := map[string]interface{}{
		"role":    role,
		"content": content,
	}
	if strings.TrimSpace(toolCallID) != "" {
		message["tool_call_id"] = strings.TrimSpace(toolCallID)
	}
	if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return message
	}

	if reasoning != nil {
		if blocks := decodeSliceOfMaps(reasoning.Metadata[reasoningMetadataAnthropicBlocksKey]); len(blocks) > 0 {
			message["content"] = blocks
			return message
		}
	}

	blocks := make([]map[string]interface{}, 0, len(toolCalls)+2)
	if reasoning != nil && (strings.TrimSpace(reasoning.DisplayText()) != "" || strings.TrimSpace(reasoning.OpaqueState) != "") {
		block := map[string]interface{}{
			"type": "thinking",
		}
		if text := strings.TrimSpace(reasoning.DisplayText()); text != "" {
			block["thinking"] = text
		}
		if opaque := strings.TrimSpace(reasoning.OpaqueState); opaque != "" {
			block["signature"] = opaque
		}
		blocks = append(blocks, block)
	}
	if strings.TrimSpace(content) != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}
	for _, toolCall := range toolCalls {
		if block := anthropicToolUseBlock(toolCall); block != nil {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) > 0 {
		message["content"] = blocks
	}
	return message
}

func anthropicToolUseBlock(toolCall map[string]interface{}) map[string]interface{} {
	if len(toolCall) == 0 {
		return nil
	}
	function, _ := toolCall["function"].(map[string]interface{})
	name, _ := function["name"].(string)
	if strings.TrimSpace(name) == "" {
		name, _ = toolCall["name"].(string)
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}
	id, _ := toolCall["id"].(string)
	args := decodeJSONObject(function["arguments"])
	if len(args) == 0 {
		args = decodeJSONObject(toolCall["arguments"])
	}
	return map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": args,
	}
}

func buildGeminiProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock) map[string]interface{} {
	message := map[string]interface{}{
		"role":    role,
		"content": content,
	}
	if strings.TrimSpace(toolCallID) != "" {
		message["tool_call_id"] = strings.TrimSpace(toolCallID)
	}
	if reasoning != nil {
		if parts := decodeSliceOfMaps(reasoning.Metadata[reasoningMetadataGeminiPartsKey]); len(parts) > 0 {
			message["parts"] = parts
			return message
		}
	}
	if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return message
	}
	parts := make([]map[string]interface{}, 0, len(toolCalls)+2)
	if reasoning != nil && (strings.TrimSpace(reasoning.DisplayText()) != "" || strings.TrimSpace(reasoning.OpaqueState) != "") {
		part := map[string]interface{}{
			"thought": true,
		}
		if text := strings.TrimSpace(reasoning.DisplayText()); text != "" {
			part["text"] = text
		}
		if opaque := strings.TrimSpace(reasoning.OpaqueState); opaque != "" {
			part["thoughtSignature"] = opaque
		}
		parts = append(parts, part)
	}
	if strings.TrimSpace(content) != "" {
		parts = append(parts, map[string]interface{}{
			"text": content,
		})
	}
	for _, toolCall := range toolCalls {
		if part := geminiFunctionCallPart(toolCall); part != nil {
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 {
		message["parts"] = parts
	}
	return message
}

func geminiFunctionCallPart(toolCall map[string]interface{}) map[string]interface{} {
	if len(toolCall) == 0 {
		return nil
	}
	function, _ := toolCall["function"].(map[string]interface{})
	name, _ := function["name"].(string)
	if strings.TrimSpace(name) == "" {
		name, _ = toolCall["name"].(string)
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}
	args := decodeJSONObject(function["arguments"])
	if len(args) == 0 {
		args = decodeJSONObject(toolCall["arguments"])
	}
	part := map[string]interface{}{
		"functionCall": map[string]interface{}{
			"name": name,
			"args": args,
		},
	}
	if id, _ := toolCall["id"].(string); strings.TrimSpace(id) != "" {
		part["functionCall"].(map[string]interface{})["id"] = id
	}
	return part
}

func encodeRuntimeToolCalls(calls []types.ToolCall) []map[string]interface{} {
	if len(calls) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(calls))
	for _, call := range calls {
		argsJSON := "{}"
		if len(call.Args) > 0 {
			if argsBytes, err := json.Marshal(call.Args); err == nil && len(argsBytes) > 0 && string(argsBytes) != "null" {
				argsJSON = string(argsBytes)
			}
		}
		result = append(result, map[string]interface{}{
			"id":   call.ID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      call.Name,
				"arguments": argsJSON,
			},
		})
	}
	return result
}

func encodeProviderToolCalls(calls []ToolCall) []map[string]interface{} {
	if len(calls) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(calls))
	for _, call := range calls {
		result = append(result, map[string]interface{}{
			"id":   call.ID,
			"type": call.Type,
			"function": map[string]interface{}{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return result
}

func decodeSliceOfMaps(value interface{}) []map[string]interface{} {
	switch typed := value.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		result := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]interface{}); ok {
				result = append(result, mapped)
			}
		}
		return result
	default:
		return nil
	}
}

func decodeJSONObject(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return decoded
		}
	}
	return nil
}
