package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayClient_ConvertTools_CodexIncludesRuntimeTools(t *testing.T) {
	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	tools := []types.ToolDefinition{
		{Name: "bash", Description: "执行 Shell 命令", Parameters: map[string]interface{}{"type": "object"}},
	}

	got := client.convertTools(tools, "codex")
	if got == nil {
		t.Fatal("expected tools, got nil")
	}
	toolList, ok := got.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map[string]interface{}, got %T", got)
	}
	if len(toolList) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(toolList))
	}
	if toolList[0]["name"] != "bash" {
		t.Fatalf("expected bash, got %v", toolList[0]["name"])
	}
	params, ok := toolList[0]["parameters"].(map[string]interface{})
	if !ok || params == nil {
		t.Fatalf("expected parameters map, got %T", toolList[0]["parameters"])
	}
	if params["type"] != "object" {
		t.Fatalf("expected type=object, got %v", params["type"])
	}
	if toolList[1]["name"] != "list_mcp_resources" {
		t.Fatalf("expected list_mcp_resources, got %v", toolList[1]["name"])
	}
}

func TestGatewayClient_CallProviderReportsHTTPDebugPayload(t *testing.T) {
	var events []HTTPDebugEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"resp_ok_1",
			"model":"gpt-5.2-codex",
			"stop_reason":"end_turn",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}
			],
			"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}
		}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "codex_ee",
			Type:    "codex",
			BaseURL: server.URL,
		},
		KeyValue: "test-key",
	}
	ctx := WithHTTPDebugReporter(context.Background(), func(event HTTPDebugEvent) {
		events = append(events, event)
	})

	_, err := client.callProvider(ctx, selected, "gpt-5.2-codex", &LLMRequest{
		Model: "gpt-5.2-codex",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("callProvider failed: %v", err)
	}
	if len(events) < 2 {
		t.Fatal("expected debug events")
	}
	if events[0].Source != "gateway_client" {
		t.Fatalf("expected gateway_client source, got %q", events[0].Source)
	}
	if events[0].Phase != "request" {
		t.Fatalf("expected request phase, got %q", events[0].Phase)
	}
	if events[0].Provider != "codex_ee" {
		t.Fatalf("expected provider codex_ee, got %q", events[0].Provider)
	}
	if events[0].Protocol != "codex" {
		t.Fatalf("expected codex protocol, got %q", events[0].Protocol)
	}
	if events[0].Method != http.MethodPost {
		t.Fatalf("expected POST method, got %q", events[0].Method)
	}
	if events[0].RequestBodyBytes == 0 {
		t.Fatal("expected request body bytes")
	}
	if want := `"hello"`; !strings.Contains(events[0].RequestBody, want) {
		t.Fatalf("expected request body to contain %s, got %s", want, events[0].RequestBody)
	}
	if events[1].Phase != "response" {
		t.Fatalf("expected response phase, got %q", events[1].Phase)
	}
	if events[1].ResponseStatusCode != 200 {
		t.Fatalf("expected response status 200, got %d", events[1].ResponseStatusCode)
	}
	if !strings.Contains(events[1].ResponseBodyPreview, `"resp_ok_1"`) {
		t.Fatalf("expected response preview to contain response id, got %q", events[1].ResponseBodyPreview)
	}
}

func TestGatewayClient_CallProvider_WithStreamAggregatesSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`,
			"",
			"event: response.output_item.added",
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello"}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","stop_reason":"end_turn"}}`,
			"",
		}, "\n"))
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "codex_ee",
			Type:    "codex",
			BaseURL: server.URL,
		},
		KeyValue: "test-key",
	}

	resp, err := client.callProvider(context.Background(), selected, "gpt-5.4", &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello", resp.Content)
	assert.Empty(t, resp.ToolCalls)
}

func TestGatewayClient_CallProvider_AnthropicReasoningEffortMapsToThinking(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedBeta string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBeta = r.Header.Get("anthropic-beta")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "anthropic_ee",
			Type:    "anthropic",
			BaseURL: server.URL,
		},
		KeyValue: "test-key",
	}

	resp, err := client.callProvider(context.Background(), selected, "claude-sonnet-4-6", &LLMRequest{
		Model:           "claude-sonnet-4-6",
		ReasoningEffort: "high",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)

	rawThinking, ok := capturedBody["thinking"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "adaptive", rawThinking["type"])
	assert.Equal(t, "high", rawThinking["effort"])
	assert.Empty(t, capturedBeta)
}

func TestGatewayClient_CallProvider_OpenAINormalizesReasoningEffort(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "openai_ee",
			Type:    "openai",
			BaseURL: server.URL,
		},
		KeyValue: "test-key",
	}

	resp, err := client.callProvider(context.Background(), selected, "gpt-5.4", &LLMRequest{
		Model:           "gpt-5.4",
		ReasoningEffort: "xhigh",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, "high", capturedBody["reasoning_effort"])
}
