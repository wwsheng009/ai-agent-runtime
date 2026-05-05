package llm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
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
	case "image_generation_call":
		return canonicalizeCodexImageGenerationOutputItem(item)
	case "function_call":
		return canonicalizeCodexFunctionCallOutputItem(item)
	case "function_call_output":
		return canonicalizeCodexFunctionCallResultOutputItem(item)
	case "custom_tool_call":
		return canonicalizeCodexCustomToolCallOutputItem(item)
	case "custom_tool_call_output":
		return canonicalizeCodexCustomToolCallResultOutputItem(item)
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

func canonicalizeCodexImageGenerationOutputItem(item map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{
		"type": "image_generation_call",
	}
	if id, _ := item["id"].(string); strings.TrimSpace(id) != "" {
		out["id"] = strings.TrimSpace(id)
	}
	if status, _ := item["status"].(string); strings.TrimSpace(status) != "" {
		out["status"] = strings.TrimSpace(status)
	}
	if revisedPrompt, _ := item["revised_prompt"].(string); strings.TrimSpace(revisedPrompt) != "" {
		out["revised_prompt"] = strings.TrimSpace(revisedPrompt)
	}
	if result, ok := item["result"].(string); ok {
		out["result"] = result
	}
	return out
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

func canonicalizeCodexCustomToolCallOutputItem(item map[string]interface{}) map[string]interface{} {
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) == "" {
		callID, _ = item["id"].(string)
	}
	name, _ := item["name"].(string)
	input, _ := item["input"].(string)

	callID = strings.TrimSpace(callID)
	name = strings.TrimSpace(name)
	if callID == "" || name == "" {
		return cloneCodexOutputMapDroppingKeys(item, "id", "status", "phase")
	}
	return map[string]interface{}{
		"type":    "custom_tool_call",
		"call_id": callID,
		"name":    name,
		"input":   input,
	}
}

func canonicalizeCodexCustomToolCallResultOutputItem(item map[string]interface{}) map[string]interface{} {
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) == "" {
		callID, _ = item["id"].(string)
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return cloneCodexOutputMapDroppingKeys(item, "id", "status", "phase")
	}
	out := map[string]interface{}{
		"type":    "custom_tool_call_output",
		"call_id": callID,
		"output":  item["output"],
	}
	if name, _ := item["name"].(string); strings.TrimSpace(name) != "" {
		out["name"] = strings.TrimSpace(name)
	}
	return out
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

func runtimeMessageToAdapterMessage(msg types.Message, protocol string, providerHint string) map[string]interface{} {
	reasoning := reasoningFromMessageMetadata(msg.Metadata)
	toolCalls := encodeRuntimeToolCalls(msg.ToolCalls)
	return buildProtocolMessageMap(msg.Role, msg.Content, msg.ContentParts, toolCalls, msg.ToolCallID, reasoning, protocol, providerHint, mapFromMetadata(msg.Metadata))
}

// RuntimeMessagesToProtocolMessages converts normalized runtime messages into
// protocol-specific request payloads.
func RuntimeMessagesToProtocolMessages(messages []types.Message, protocol string, providerHints ...string) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}

	providerHint := ""
	for _, hint := range providerHints {
		if trimmed := strings.TrimSpace(hint); trimmed != "" {
			providerHint = trimmed
			break
		}
	}

	result := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		result = append(result, runtimeMessageToAdapterMessage(msg, protocol, providerHint))
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
		result = sanitizeCodexProtocolMessages(result)
	} else if strings.EqualFold(strings.TrimSpace(protocol), "anthropic") {
		result = sanitizeAnthropicProtocolMessages(result)
	}
	return result
}

func sanitizeOpenAICompatibleProtocolMessages(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}

	filtered := make([]map[string]interface{}, 0, len(messages))
	var (
		pendingMessages []map[string]interface{}
		pendingToolIDs  map[string]struct{}
	)

	flushPending := func() {
		if len(pendingMessages) == 0 || len(pendingToolIDs) != 0 {
			return
		}
		filtered = append(filtered, pendingMessages...)
		pendingMessages = nil
		pendingToolIDs = nil
	}
	resetPending := func() {
		pendingMessages = nil
		pendingToolIDs = nil
	}

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))

		if len(pendingToolIDs) > 0 {
			if role == "tool" {
				toolCallID, _ := msg["tool_call_id"].(string)
				toolCallID = strings.TrimSpace(toolCallID)
				if toolCallID == "" {
					continue
				}
				if _, ok := pendingToolIDs[toolCallID]; !ok {
					continue
				}
				delete(pendingToolIDs, toolCallID)
				pendingMessages = append(pendingMessages, msg)
				flushPending()
				continue
			}

			// A non-tool message interrupted an unfinished assistant tool replay
			// block. Drop the entire incomplete block instead of leaking orphan
			// tool results into the next request.
			resetPending()
		}

		switch role {
		case "assistant":
			toolIDs := protocolMessageToolCallIDs(msg)
			if len(toolIDs) > 0 {
				pendingMessages = []map[string]interface{}{msg}
				pendingToolIDs = toolIDs
				continue
			}
			filtered = append(filtered, msg)
		case "tool":
			// Drop orphan tool messages. OpenAI-compatible providers require tool
			// messages to be immediate responses to a preceding assistant tool_calls
			// message.
			continue
		default:
			filtered = append(filtered, msg)
		}
	}

	// If the transcript ended with an incomplete assistant tool replay block,
	// drop it rather than sending an invalid OpenAI-compatible message chain.
	return filtered
}

func sanitizeSensenovaOpenAICompatibleProtocolMessages(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}

	filtered := make([]map[string]interface{}, 0, len(messages))
	var pendingSystem map[string]interface{}
	flushSystem := func() {
		if pendingSystem == nil {
			return
		}
		filtered = append(filtered, pendingSystem)
		pendingSystem = nil
	}

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if strings.EqualFold(strings.TrimSpace(role), "system") {
			if pendingSystem == nil {
				pendingSystem = cloneMapStringAny(msg)
				continue
			}
			mergeOpenAICompatibleStringContent(pendingSystem, msg)
			continue
		}
		flushSystem()
		filtered = append(filtered, msg)
	}
	flushSystem()

	return filtered
}

func mergeOpenAICompatibleStringContent(dst, src map[string]interface{}) {
	if dst == nil || src == nil {
		return
	}
	existing, _ := dst["content"].(string)
	additional, _ := src["content"].(string)
	if strings.TrimSpace(additional) == "" {
		return
	}
	if strings.TrimSpace(existing) == "" {
		dst["content"] = additional
		return
	}
	dst["content"] = strings.TrimRight(existing, "\n") + "\n\n" + strings.TrimLeft(additional, "\n")
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
			for id := range protocolMessageToolCallIDs(msg) {
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

// sanitizeAnthropicProtocolMessages converts OpenAI-style tool role messages
// into Anthropic-compatible user messages with tool_result content blocks.
// The Anthropic API only accepts "user" and "assistant" roles; tool results
// must be embedded as content blocks inside user messages.
func sanitizeAnthropicProtocolMessages(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}

	// Collect all tool_use IDs declared by assistant messages.
	knownToolUseIDs := make(map[string]struct{})
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
			continue
		}
		// tool_use blocks in assistant content (Anthropic format)
		for _, block := range decodeSliceOfMaps(msg["content"]) {
			blockType, _ := block["type"].(string)
			if strings.ToLower(strings.TrimSpace(blockType)) != "tool_use" {
				continue
			}
			if id, ok := block["id"].(string); ok && strings.TrimSpace(id) != "" {
				knownToolUseIDs[strings.TrimSpace(id)] = struct{}{}
			}
		}
		// OpenAI-style tool_calls on assistant messages
		for id := range protocolMessageToolCallIDs(msg) {
			knownToolUseIDs[id] = struct{}{}
		}
	}

	result := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))

		if role != "tool" {
			result = append(result, msg)
			continue
		}

		// Convert tool message to Anthropic user message with tool_result block.
		toolCallID := strings.TrimSpace(msgValueString(msg, "tool_call_id"))
		if toolCallID == "" {
			// No tool_call_id – drop the orphan.
			continue
		}

		contentText := msgValueString(msg, "content")
		toolResultBlock := map[string]interface{}{
			"type":        "tool_result",
			"tool_use_id": toolCallID,
			"content":     contentText,
		}
		// Preserve tool_name if present so the Anthropic adapter can map it.
		if name := strings.TrimSpace(msgValueString(msg, "name")); name != "" {
			toolResultBlock["name"] = name
		}

		// Merge consecutive tool results into a single user message when possible.
		if last := lastAppendedUserRole(result); last != nil {
			if existingContent, ok := last["content"].([]interface{}); ok {
				last["content"] = append(existingContent, toolResultBlock)
			} else {
				last["content"] = []interface{}{toolResultBlock}
			}
		} else {
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": []interface{}{toolResultBlock},
			})
		}
	}
	return enforceAnthropicMessageAlternation(result)
}

// enforceAnthropicMessageAlternation merges consecutive same-role messages
// so that the Anthropic API receives strictly alternating user/assistant turns.
func enforceAnthropicMessageAlternation(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) <= 1 {
		return messages
	}

	result := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))

		if len(result) > 0 {
			last := result[len(result)-1]
			lastRole, _ := last["role"].(string)
			lastRole = strings.ToLower(strings.TrimSpace(lastRole))

			if role == lastRole {
				// Merge into previous message
				mergeAnthropicSameRoleMessage(last, msg)
				continue
			}
		}
		result = append(result, msg)
	}
	return result
}

// mergeAnthropicSameRoleMessage merges content from src into dst when both
// share the same role. Handles both string and []interface{} content.
func mergeAnthropicSameRoleMessage(dst, src map[string]interface{}) {
	srcContent := src["content"]
	dstContent := dst["content"]

	// Normalize both to []interface{} of content blocks
	dstBlocks := normalizeToContentBlocks(dstContent)
	srcBlocks := normalizeToContentBlocks(srcContent)

	dst["content"] = append(dstBlocks, srcBlocks...)
}

// normalizeToContentBlocks converts content (string or []interface{}) into a
// uniform []interface{} of content blocks.
func normalizeToContentBlocks(content interface{}) []interface{} {
	switch c := content.(type) {
	case nil:
		return nil
	case string:
		if c == "" {
			return nil
		}
		return []interface{}{map[string]interface{}{"type": "text", "text": c}}
	case []map[string]interface{}:
		result := make([]interface{}, 0, len(c))
		for _, item := range c {
			if item == nil {
				continue
			}
			result = append(result, item)
		}
		return result
	case []interface{}:
		return c
	default:
		return []interface{}{map[string]interface{}{"type": "text", "text": fmt.Sprintf("%v", c)}}
	}
}

func msgValueString(msg map[string]interface{}, key string) string {
	if msg == nil {
		return ""
	}
	value, ok := msg[key]
	if !ok || value == nil {
		return ""
	}
	s, ok := value.(string)
	if ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func lastAppendedUserRole(messages []map[string]interface{}) map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}
	last := messages[len(messages)-1]
	role, _ := last["role"].(string)
	if !strings.EqualFold(strings.TrimSpace(role), "user") {
		return nil
	}
	return last
}

func protocolMessageToolCallIDs(message map[string]interface{}) map[string]struct{} {
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
		normalizedType := strings.ToLower(strings.TrimSpace(itemType))
		if normalizedType != "function_call" && normalizedType != "custom_tool_call" {
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

func providerMessageToAdapterMessage(msg Message, protocol string, providerHint string) map[string]interface{} {
	reasoning := reasoningFromMapMetadata(msg.Metadata)
	if reasoning == nil && strings.TrimSpace(msg.Reasoning) != "" {
		reasoning = &types.ReasoningBlock{
			Summary:    strings.TrimSpace(msg.Reasoning),
			Visibility: types.ReasoningVisibilitySummary,
		}
	}
	toolCalls := encodeProviderToolCalls(msg.ToolCalls)
	return buildProtocolMessageMap(msg.Role, msg.Content, msg.ContentParts, toolCalls, msg.ToolCallID, reasoning, protocol, providerHint, msg.Metadata)
}

func buildProtocolMessageMap(role, content string, contentParts []types.ContentPart, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock, protocol string, providerHint string, messageMetadata map[string]interface{}) map[string]interface{} {
	// contentParts carries structured multimodal content (text + images).
	// Protocol builders currently still read from messageMetadata for backward
	// compatibility; a future refactor will migrate them to use contentParts
	// directly, eliminating the metadata sideband for images.
	_ = contentParts

	normalizedProtocol := strings.ToLower(strings.TrimSpace(protocol))
	switch normalizedProtocol {
	case "codex":
		return buildCodexProtocolMessage(role, content, toolCalls, toolCallID, reasoning, messageMetadata)
	case "anthropic":
		return buildAnthropicProtocolMessage(role, content, toolCalls, toolCallID, reasoning, messageMetadata)
	case "gemini":
		return buildGeminiProtocolMessage(role, content, toolCalls, toolCallID, reasoning, messageMetadata)
	default:
		return buildOpenAIProtocolMessage(role, content, toolCalls, toolCallID, reasoning, providerHint, messageMetadata)
	}
}

func buildOpenAIProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock, providerHint string, messageMetadata map[string]interface{}) map[string]interface{} {
	message := map[string]interface{}{
		"role": role,
	}
	if images := ExtractLocalInputImages(messageMetadata); len(images) > 0 && strings.EqualFold(role, "user") {
		parts := make([]map[string]interface{}, 0, len(images)+1)
		if strings.TrimSpace(content) != "" {
			parts = append(parts, map[string]interface{}{
				"type": "text",
				"text": content,
			})
		}
		for _, image := range images {
			dataURL, err := localInputImageDataURL(image)
			if err != nil || strings.TrimSpace(dataURL) == "" {
				continue
			}
			parts = append(parts, map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": dataURL},
			})
		}
		if len(parts) > 0 {
			message["content"] = parts
		} else {
			message["content"] = content
		}
	} else {
		message["content"] = content
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if strings.TrimSpace(toolCallID) != "" {
		message["tool_call_id"] = strings.TrimSpace(toolCallID)
	}
	if name, ok := stringMetadataValue(messageMetadata, "name"); ok {
		message["name"] = name
	}
	if strings.EqualFold(strings.TrimSpace(role), "assistant") {
		if prefix, ok := boolMetadataValue(messageMetadata, "prefix"); ok {
			message["prefix"] = prefix
		}
		if reasoningContent, ok := stringMetadataValueAllowEmpty(messageMetadata, "reasoning_content"); ok {
			message["reasoning_content"] = reasoningContent
		} else if reasoningContent, ok := replayableOpenAIReasoningContent(toolCalls, reasoning, providerHint); ok {
			message["reasoning_content"] = reasoningContent
		}
	}
	return message
}

func buildCodexProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock, messageMetadata map[string]interface{}) map[string]interface{} {
	if strings.EqualFold(strings.TrimSpace(role), "user") {
		if parts := codexUserContentParts(content, messageMetadata); len(parts) > 0 {
			return map[string]interface{}{
				"role":    role,
				"content": parts,
			}
		}
		return buildOpenAIProtocolMessage(role, content, toolCalls, toolCallID, reasoning, "", nil)
	}

	message := buildOpenAIProtocolMessage(role, content, toolCalls, toolCallID, reasoning, "", nil)
	if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return message
	}

	outputItems := codexProtocolOutputItems(content, toolCalls, reasoning, messageMetadata)
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

func codexUserContentParts(content string, messageMetadata map[string]interface{}) []map[string]interface{} {
	images := ExtractLocalInputImages(messageMetadata)
	if len(images) == 0 {
		return nil
	}

	parts := make([]map[string]interface{}, 0, len(images)+1)
	if strings.TrimSpace(content) != "" {
		parts = append(parts, map[string]interface{}{
			"type": "input_text",
			"text": content,
		})
	}
	for _, image := range images {
		dataURL, err := localInputImageDataURL(image)
		if err != nil || strings.TrimSpace(dataURL) == "" {
			continue
		}
		parts = append(parts, map[string]interface{}{
			"type":      "input_image",
			"image_url": dataURL,
		})
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func codexProtocolOutputItems(content string, toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock, messageMetadata map[string]interface{}) []map[string]interface{} {
	if reasoning != nil {
		if outputItems := canonicalizeCodexOutputItems(reasoning.Metadata[reasoningMetadataCodexOutputItemsKey]); len(outputItems) > 0 {
			return hydrateCodexImageGenerationResults(outputItems, messageMetadata)
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

func hydrateCodexImageGenerationResults(outputItems []map[string]interface{}, messageMetadata map[string]interface{}) []map[string]interface{} {
	if len(outputItems) == 0 {
		return nil
	}
	generatedImages := decodeSliceOfMaps(messageMetadata[MetadataKeyGeneratedImages])
	if len(generatedImages) == 0 {
		return outputItems
	}

	savedPaths := make(map[string]string, len(generatedImages))
	for _, image := range generatedImages {
		id := strings.TrimSpace(stringValue(image["id"]))
		savedPath := strings.TrimSpace(stringValue(image["saved_path"]))
		if id != "" && savedPath != "" {
			savedPaths[id] = savedPath
		}
	}
	if len(savedPaths) == 0 {
		return outputItems
	}

	hydrated := make([]map[string]interface{}, 0, len(outputItems))
	changed := false
	for _, item := range outputItems {
		cloned := cloneDeepMapStringAny(item)
		if !strings.EqualFold(strings.TrimSpace(stringValue(cloned["type"])), codexImageGenerationCallType) {
			hydrated = append(hydrated, cloned)
			continue
		}
		if strings.TrimSpace(stringValue(cloned["result"])) != "" {
			hydrated = append(hydrated, cloned)
			continue
		}

		id := strings.TrimSpace(stringValue(cloned["id"]))
		savedPath := strings.TrimSpace(savedPaths[id])
		if id == "" || savedPath == "" {
			hydrated = append(hydrated, cloned)
			continue
		}

		bytes, err := os.ReadFile(savedPath)
		if err != nil || len(bytes) == 0 {
			hydrated = append(hydrated, cloned)
			continue
		}

		cloned["result"] = base64.StdEncoding.EncodeToString(bytes)
		status := strings.ToLower(strings.TrimSpace(stringValue(cloned["status"])))
		if status == "" || status == "generating" || status == "in_progress" {
			cloned["status"] = "completed"
		}
		hydrated = append(hydrated, cloned)
		changed = true
	}

	if !changed {
		return outputItems
	}
	return hydrated
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
	toolType, _ := toolCall["type"].(string)
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		toolType = "function_call"
	}
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
	input, _ := toolCall["input"].(string)
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}

	if toolType == "custom_tool_call" {
		if strings.TrimSpace(input) == "" {
			input = arguments
		}
		return map[string]interface{}{
			"type":    "custom_tool_call",
			"call_id": callID,
			"name":    name,
			"input":   input,
		}
	}

	return map[string]interface{}{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

func buildAnthropicProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock, messageMetadata map[string]interface{}) map[string]interface{} {
	message := map[string]interface{}{
		"role": role,
	}
	if strings.TrimSpace(toolCallID) != "" {
		message["tool_call_id"] = strings.TrimSpace(toolCallID)
	}
	if !strings.EqualFold(strings.TrimSpace(role), "user") {
		message["content"] = content
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

	// User message: if images present in metadata, construct multimodal content blocks
	images := ExtractLocalInputImages(messageMetadata)
	if len(images) == 0 {
		message["content"] = content
		return message
	}

	blocks := make([]map[string]interface{}, 0, len(images)+1)
	if strings.TrimSpace(content) != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}
	for _, image := range images {
		dataURL, err := localInputImageDataURL(image)
		if err != nil || strings.TrimSpace(dataURL) == "" {
			continue
		}
		// Anthropic uses base64 content blocks with media_type
		parts := strings.SplitN(dataURL, ",", 2)
		if len(parts) != 2 {
			continue
		}
		header := parts[0]
		encoded := parts[1]
		mediaType := "image/png"
		if idx := strings.Index(header, ":"); idx >= 0 {
			mediaType = strings.TrimSuffix(header[idx+1:], ";base64")
		}
		blocks = append(blocks, map[string]interface{}{
			"type":   "image",
			"source": map[string]interface{}{"type": "base64", "media_type": mediaType, "data": encoded},
		})
	}
	if len(blocks) > 0 {
		message["content"] = blocks
	} else {
		message["content"] = content
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
	if len(args) == 0 {
		args = map[string]interface{}{}
	}
	return map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": args,
	}
}

func buildGeminiProtocolMessage(role, content string, toolCalls []map[string]interface{}, toolCallID string, reasoning *types.ReasoningBlock, messageMetadata map[string]interface{}) map[string]interface{} {
	message := map[string]interface{}{
		"role": role,
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
		// User or tool message
		if strings.EqualFold(strings.TrimSpace(role), "user") {
			images := ExtractLocalInputImages(messageMetadata)
			if len(images) > 0 {
				parts := make([]map[string]interface{}, 0, len(images)+1)
				if strings.TrimSpace(content) != "" {
					parts = append(parts, map[string]interface{}{
						"text": content,
					})
				}
				for _, image := range images {
					dataURL, err := localInputImageDataURL(image)
					if err != nil || strings.TrimSpace(dataURL) == "" {
						continue
					}
					// Gemini uses inline_data with mime_type and data
					partsSlice := strings.SplitN(dataURL, ",", 2)
					if len(partsSlice) != 2 {
						continue
					}
					header := partsSlice[0]
					encoded := partsSlice[1]
					mimeType := "image/png"
					if idx := strings.Index(header, ":"); idx >= 0 {
						mimeType = strings.TrimSuffix(header[idx+1:], ";base64")
					}
					parts = append(parts, map[string]interface{}{
						"inline_data": map[string]interface{}{
							"mime_type": mimeType,
							"data":      encoded,
						},
					})
				}
				if len(parts) > 0 {
					message["parts"] = parts
				} else {
					message["content"] = content
				}
				return message
			}
		}
		message["content"] = content
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
	if len(args) == 0 {
		args = map[string]interface{}{}
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

func stringMetadataValue(metadata map[string]interface{}, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	value, ok := metadata[key].(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func stringMetadataValueAllowEmpty(metadata map[string]interface{}, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	value, exists := metadata[key]
	if !exists {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func boolMetadataValue(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	value, ok := metadata[key].(bool)
	if !ok {
		return false, false
	}
	return value, true
}

func replayableOpenAIReasoningContent(toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock, providerHint string) (string, bool) {
	provider := strings.ToLower(strings.TrimSpace(providerHint))
	if reasoning != nil {
		if normalized := strings.ToLower(strings.TrimSpace(reasoning.Provider)); normalized != "" {
			provider = normalized
		}
		if strings.HasPrefix(provider, "deepseek") {
			return reasoning.RawDisplayText(), true
		}
		if len(toolCalls) == 0 && !reasoning.ReplayRequired {
			return "", false
		}
		if text := strings.TrimSpace(reasoning.DisplayText()); text != "" {
			return text, true
		}
		return "", false
	}
	if strings.HasPrefix(provider, "deepseek") && len(toolCalls) > 0 {
		return "", true
	}
	return "", false
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
				"arguments": normalizeToolCallArgumentsJSON(call.Function.Arguments),
			},
		})
	}
	return result
}

func normalizeToolCallArgumentsJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return "{}"
	}
	return raw
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
