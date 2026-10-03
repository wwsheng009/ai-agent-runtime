package llm

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Regression for the HTTP 422 observed after switching a chat session to
// deepseek_anthropic:
//
//	messages[11]: unknown variant `redacted_thinking`, expected one of `text`,
//	`image`, `document`, `thinking`, ...
//
// The transcript carried a redacted_thinking block (stored raw Anthropic
// response blocks replayed on every request). DeepSeek's Anthropic-compatible
// endpoint does not accept that variant, so the provider compatibility layer
// must strip it before the request leaves the runtime.
func TestProviderWrapperConvertRequest_DeepSeekAnthropicStripsRedactedThinking(t *testing.T) {
	provider, err := NewProvider(&ProviderConfig{
		Name:         "deepseek_anthropic",
		Type:         "anthropic",
		BaseURL:      "https://api.deepseek.com/anthropic",
		APIPath:      "/v1/messages",
		DefaultModel: "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	wrapper, ok := provider.(*ProviderWrapper)
	if !ok {
		t.Fatalf("unexpected provider type %T", provider)
	}

	assistant := Message{
		Role:     "assistant",
		Content:  "answer",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:         "anthropic_thinking",
		Summary:        "reason",
		OpaqueState:    "sig_abc",
		ReplayRequired: true,
		Streamable:     true,
		Visibility:     types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"anthropic_content_blocks": []map[string]interface{}{
				{"type": "thinking", "thinking": "reason", "signature": "sig_abc"},
				{"type": "redacted_thinking", "data": "opaque_xyz"},
				{"type": "text", "text": "answer"},
			},
		},
	})

	config := wrapper.convertRequest(ChatRequest{
		Model:  "deepseek-v4-flash",
		Stream: true,
		Messages: []Message{
			{Role: "user", Content: "继续"},
			assistant,
			// A trailing user turn keeps the assistant message from being
			// trimmed as an assistant prefill by the protocol sanitizer.
			{Role: "user", Content: "再看一下"},
		},
	})

	if len(config.Messages) != 3 {
		t.Fatalf("expected all messages preserved, got %#v", config.Messages)
	}
	blocks := decodeSliceOfMaps(config.Messages[1]["content"])
	if len(blocks) != 2 {
		t.Fatalf("expected thinking + text blocks after redaction, got %#v", blocks)
	}
	for _, block := range blocks {
		if block["type"] == "redacted_thinking" {
			t.Fatalf("redacted_thinking must not reach the DeepSeek wire request: %#v", blocks)
		}
	}
	if blocks[0]["type"] != "thinking" || blocks[1]["type"] != "text" {
		t.Fatalf("expected thinking then text order preserved, got %#v", blocks)
	}
}
