package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

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

func TestGatewayClientResponseNormalization_StreamAndNonStreamConsistent(t *testing.T) {
	const model = "sensenova-6.7-flash-lite"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if stream, _ := requestBody["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"think\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"list_files\",\"arguments\":{\"path\":\".\"}}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"sensenova-6.7-flash-lite","choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning":"think","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_files","arguments":{"path":"."}}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "sensenova",
			Type:    "openai",
			BaseURL: server.URL,
		},
		KeyValue: "test-key",
	}
	request := &LLMRequest{
		Model: model,
		Messages: []types.Message{{
			Role:    "user",
			Content: "ls",
		}},
	}

	nonStream, err := client.callProvider(context.Background(), selected, model, request)
	if err != nil {
		t.Fatalf("non-stream call: %v", err)
	}
	streamRequest := *request
	streamRequest.Stream = true
	stream, err := client.callProvider(context.Background(), selected, model, &streamRequest)
	if err != nil {
		t.Fatalf("stream call: %v", err)
	}

	if nonStream.Content != stream.Content || nonStream.Reasoning != stream.Reasoning {
		t.Fatalf("expected stream/non-stream content and reasoning to match, non-stream=%#v stream=%#v", nonStream, stream)
	}
	if nonStream.ReasoningBlock == nil || stream.ReasoningBlock == nil {
		t.Fatalf("expected reasoning blocks to be preserved, non-stream=%#v stream=%#v", nonStream.ReasoningBlock, stream.ReasoningBlock)
	}
	if nonStream.ReasoningBlock.DisplayText() != stream.ReasoningBlock.DisplayText() {
		t.Fatalf("expected reasoning block text to match, non-stream=%#v stream=%#v", nonStream.ReasoningBlock, stream.ReasoningBlock)
	}
	if len(nonStream.ToolCalls) != 1 || len(stream.ToolCalls) != 1 {
		t.Fatalf("expected one tool call in both responses, non-stream=%#v stream=%#v", nonStream.ToolCalls, stream.ToolCalls)
	}
	if nonStream.ToolCalls[0].Name != stream.ToolCalls[0].Name || stream.ToolCalls[0].Name != "list_files" {
		t.Fatalf("expected tool call names to match, non-stream=%#v stream=%#v", nonStream.ToolCalls, stream.ToolCalls)
	}
	if nonStream.ToolCalls[0].Args["path"] != "." || stream.ToolCalls[0].Args["path"] != "." {
		t.Fatalf("expected tool call arguments to be normalized, non-stream=%#v stream=%#v", nonStream.ToolCalls, stream.ToolCalls)
	}
}
