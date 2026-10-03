package providercompat

import "strings"

// deepSeekAnthropicAdapter contains the wire dialect changes required by the
// DeepSeek Anthropic-compatible Messages endpoint
// (https://api.deepseek.com/anthropic/v1/messages).
//
// DeepSeek documents redacted_thinking as "Not Supported" in its Messages API
// compatibility matrix. The block never originates from DeepSeek itself: it
// enters the transcript when a session previously used a provider that emits
// redacted thinking (or when the runtime's own signature-only replay fallback
// downgraded a thinking block). Switching such a session to deepseek_anthropic
// replays the block verbatim and the strict Rust deserializer rejects the whole
// request with HTTP 422:
//
//	unknown variant `redacted_thinking`, expected one of `text`, `image`,
//	`document`, `thinking`, `server_tool_use`, `tool_use`, `tool_result`, ...
//
// redacted_thinking only carries opaque reasoning bytes, so dropping it loses
// no visible content and is the only lossless wire repair DeepSeek accepts.
// Other unsupported variants (image/document/server_tool_use, ...) are left in
// place: they carry user-visible payload and must surface as a real error
// instead of being silently discarded.
type deepSeekAnthropicAdapter struct {
	BaseAdapter
}

func (deepSeekAnthropicAdapter) Name() string {
	return "anthropic-deepseek"
}

func (deepSeekAnthropicAdapter) Match(ctx Context) bool {
	if ctx.Protocol != "anthropic" {
		return false
	}
	// Match on provider name / base URL only. A bare DeepSeek-looking model id
	// on some other Anthropic-compatible relay is not enough evidence that the
	// endpoint rejects redacted_thinking.
	return IsDeepSeek(ctx.ProviderName, ctx.BaseURL, "")
}

// NormalizeAnthropicCompatibleMessages removes redacted_thinking blocks from
// assistant content arrays. It copies changed messages and never mutates
// canonical/runtime history.
func (deepSeekAnthropicAdapter) NormalizeAnthropicCompatibleMessages(_ Context, messages []map[string]interface{}) ([]map[string]interface{}, bool) {
	if len(messages) == 0 {
		return messages, false
	}

	normalized := append([]map[string]interface{}(nil), messages...)
	writeIndex := 0
	changed := false
	for _, message := range messages {
		updated, keep := stripDeepSeekRedactedThinking(message)
		if !keep {
			changed = true
			continue
		}
		if updated != nil {
			normalized[writeIndex] = updated
			changed = true
		} else {
			normalized[writeIndex] = message
		}
		writeIndex++
	}
	if !changed {
		return messages, false
	}
	return normalized[:writeIndex], true
}

// stripDeepSeekRedactedThinking returns (updated, keep):
//
//   - (nil, true) when the message has no redacted_thinking block;
//   - (copy, true) when the block was removed but other content remains;
//   - (nil, false) when the message consisted exclusively of redacted_thinking
//     blocks. Such a turn has no text, tool_use, or tool_result payload, so it
//     can be dropped without breaking tool_use/tool_result adjacency.
func stripDeepSeekRedactedThinking(message map[string]interface{}) (map[string]interface{}, bool) {
	if len(message) == 0 {
		return nil, true
	}

	var blocks []interface{}
	switch typed := message["content"].(type) {
	case []interface{}:
		blocks = typed
	case []map[string]interface{}:
		blocks = make([]interface{}, 0, len(typed))
		for _, block := range typed {
			blocks = append(blocks, block)
		}
	default:
		// String content (or absent content) cannot carry a redacted block.
		return nil, true
	}

	kept := make([]interface{}, 0, len(blocks))
	removed := false
	for _, raw := range blocks {
		block, ok := raw.(map[string]interface{})
		if ok && isRedactedThinkingBlock(block) {
			removed = true
			continue
		}
		kept = append(kept, raw)
	}
	if !removed {
		return nil, true
	}
	if len(kept) == 0 {
		return nil, false
	}

	updated := cloneMapStringAny(message)
	updated["content"] = kept
	return updated, true
}

func isRedactedThinkingBlock(block map[string]interface{}) bool {
	return strings.EqualFold(strings.TrimSpace(stringValue(block["type"])), "redacted_thinking")
}
