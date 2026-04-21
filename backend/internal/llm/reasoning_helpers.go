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
	if encoded := reasoning.ToMap(); len(encoded) > 0 {
		msg[assistantReasoningDetailsKey] = encoded
	}
	return msg
}

func extractReasoningFromAssistantMessage(msg map[string]interface{}) *types.ReasoningBlock {
	if len(msg) == 0 {
		return nil
	}
	if block := types.ReasoningBlockFromMap(msg[assistantReasoningDetailsKey]); block != nil {
		return block
	}
	if text, _ := msg["reasoning_content"].(string); strings.TrimSpace(text) != "" {
		return &types.ReasoningBlock{
			Summary:    strings.TrimSpace(text),
			Visibility: types.ReasoningVisibilitySummary,
		}
	}
	return nil
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
	return result
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
	if reasoning == nil || !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return message
	}
	if outputItems := decodeSliceOfMaps(reasoning.Metadata[reasoningMetadataCodexOutputItemsKey]); len(outputItems) > 0 {
		message[codexResponseOutputItemsMessageKey] = outputItems
		delete(message, "reasoning_content")
		delete(message, "tool_calls")
		if strings.TrimSpace(content) == "" {
			message["content"] = ""
		}
	}
	return message
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
