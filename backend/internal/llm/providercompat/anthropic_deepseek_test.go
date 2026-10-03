package providercompat

import (
	"testing"
)

func deepSeekAnthropicContext() Context {
	return Context{
		ProviderName: "deepseek_anthropic",
		Protocol:     "anthropic",
		BaseURL:      "https://api.deepseek.com/anthropic",
		Model:        "deepseek-v4-flash",
	}
}

// DeepSeek's Anthropic-compatible endpoint rejects redacted_thinking with HTTP
// 422 ("unknown variant `redacted_thinking`"). A session moved from a provider
// that emits redacted thinking must not replay that block to DeepSeek.
func TestDeepSeekAnthropicAdapterDropsRedactedThinking(t *testing.T) {
	thinking := map[string]interface{}{"type": "thinking", "thinking": "reason", "signature": "sig"}
	redacted := map[string]interface{}{"type": "redacted_thinking", "data": "opaque-sig"}
	text := map[string]interface{}{"type": "text", "text": "answer"}
	toolUse := map[string]interface{}{"type": "tool_use", "id": "call_1", "name": "view", "input": map[string]interface{}{}}

	messages := []map[string]interface{}{
		{"role": "user", "content": "检查"},
		{"role": "assistant", "content": []map[string]interface{}{thinking, redacted, text, toolUse}},
	}

	got := NormalizeAnthropicCompatibleMessages(deepSeekAnthropicContext(), messages)
	if len(got) != 2 {
		t.Fatalf("expected both messages preserved, got %d: %#v", len(got), got)
	}
	blocks := decodeMaps(got[1]["content"])
	if len(blocks) != 3 {
		t.Fatalf("expected redacted_thinking removed, got %#v", blocks)
	}
	for _, block := range blocks {
		if block["type"] == "redacted_thinking" {
			t.Fatalf("redacted_thinking must not survive DeepSeek normalization: %#v", blocks)
		}
	}
	if blocks[0]["type"] != "thinking" || blocks[1]["type"] != "text" || blocks[2]["type"] != "tool_use" {
		t.Fatalf("expected thinking/text/tool_use order preserved, got %#v", blocks)
	}

	// Canonical history must never be mutated by wire normalization.
	original := decodeMaps(messages[1]["content"])
	if len(original) != 4 || original[1]["type"] != "redacted_thinking" {
		t.Fatalf("input message was mutated: %#v", original)
	}
}

// JSON-decoded transcripts carry content as []interface{}; both shapes must be
// normalized.
func TestDeepSeekAnthropicAdapterHandlesInterfaceSliceContent(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "redacted_thinking", "data": "opaque"},
				map[string]interface{}{"type": "text", "text": "hi"},
			},
		},
	}

	got := NormalizeAnthropicCompatibleMessages(deepSeekAnthropicContext(), messages)
	blocks := decodeMaps(got[0]["content"])
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("expected only the text block to remain, got %#v", blocks)
	}
}

// A turn that consisted exclusively of opaque reasoning has no visible or
// replayable payload; dropping the whole message keeps the wire request valid.
func TestDeepSeekAnthropicAdapterDropsRedactedOnlyMessage(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": []map[string]interface{}{{"type": "redacted_thinking", "data": "opaque"}}},
		{"role": "user", "content": "continue"},
	}

	got := NormalizeAnthropicCompatibleMessages(deepSeekAnthropicContext(), messages)
	if len(got) != 2 {
		t.Fatalf("expected redacted-only assistant turn to be dropped, got %#v", got)
	}
	for _, message := range got {
		if message["role"] == "assistant" {
			t.Fatalf("assistant message without payload must not be replayed: %#v", got)
		}
	}
}

// Providers outside DeepSeek keep the official Anthropic replay format,
// including redacted_thinking blocks.
func TestAnthropicRedactedThinkingPreservedForNonDeepSeekProviders(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "assistant", "content": []map[string]interface{}{{"type": "redacted_thinking", "data": "opaque"}}},
	}

	cases := []Context{
		{ProviderName: "anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-4-6"},
		{ProviderName: "mimo_anthropic", Protocol: "anthropic", BaseURL: "https://token-plan-cn.xiaomimimo.com/anthropic", Model: "claude-sonnet-4-6"},
	}
	for _, ctx := range cases {
		got := NormalizeAnthropicCompatibleMessages(ctx, messages)
		blocks := decodeMaps(got[0]["content"])
		if len(blocks) != 1 || blocks[0]["type"] != "redacted_thinking" {
			t.Fatalf("%s: redacted_thinking must be preserved, got %#v", ctx.ProviderName, got)
		}
	}
}

// The adapter must not touch non-Anthropic protocols, even for DeepSeek.
func TestDeepSeekAnthropicAdapterIgnoresOpenAIProtocol(t *testing.T) {
	ctx := deepSeekAnthropicContext()
	ctx.Protocol = "openai"
	messages := []map[string]interface{}{
		{"role": "assistant", "content": []map[string]interface{}{{"type": "redacted_thinking", "data": "opaque"}}},
	}

	got := NormalizeAnthropicCompatibleMessages(ctx, messages)
	blocks := decodeMaps(got[0]["content"])
	if len(blocks) != 1 || blocks[0]["type"] != "redacted_thinking" {
		t.Fatalf("openai protocol must bypass the anthropic adapter, got %#v", got)
	}
}

func TestDeepSeekAnthropicAdapterNoRedactedNoChange(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": []map[string]interface{}{{"type": "thinking", "thinking": "t"}, {"type": "text", "text": "ok"}}},
		{"role": "assistant", "content": "plain string"},
	}

	got := NormalizeAnthropicCompatibleMessages(deepSeekAnthropicContext(), messages)
	if len(got) != len(messages) {
		t.Fatalf("expected messages unchanged, got %#v", got)
	}
	blocks := decodeMaps(got[1]["content"])
	if len(blocks) != 2 {
		t.Fatalf("expected thinking/text untouched, got %#v", blocks)
	}
}

func decodeMaps(raw interface{}) []map[string]interface{} {
	switch typed := raw.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		result := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if block, ok := item.(map[string]interface{}); ok {
				result = append(result, block)
			}
		}
		return result
	default:
		return nil
	}
}
