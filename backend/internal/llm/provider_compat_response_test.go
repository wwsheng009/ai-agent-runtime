package llm

import "testing"

func TestNormalizeGatewayAssistantMessage_LegacyOpenAIToolCall(t *testing.T) {
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "sensenova",
			Type:    "openai",
			BaseURL: "https://token.sensenova.cn/v1",
		},
	}
	assistantMsg := map[string]interface{}{
		"role":    "assistant",
		"content": "",
		"tool_calls": []map[string]interface{}{
			{
				"name": "list_files",
				"arguments": map[string]interface{}{
					"path": ".",
				},
			},
		},
	}

	normalized := normalizeGatewayAssistantMessage(selected, "openai", "sensenova-6.7-flash-lite", assistantMsg)
	rawToolCalls := normalizeGatewayToolCalls(normalized["tool_calls"])
	toolCalls := (&GatewayClient{}).convertToolCalls(rawToolCalls)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", toolCalls)
	}
	if toolCalls[0].Name != "list_files" {
		t.Fatalf("expected tool call name to be preserved, got %#v", toolCalls[0])
	}
	if toolCalls[0].Args["path"] != "." {
		t.Fatalf("expected tool call arguments to be parsed, got %#v", toolCalls[0].Args)
	}
}
