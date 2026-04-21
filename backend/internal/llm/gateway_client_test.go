package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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
	want := `"hello"`
	if !strings.Contains(events[0].RequestBody, want) {
		t.Fatalf("expected request body to contain %s, got %s", want, events[0].RequestBody)
	}
	if !strings.Contains(string(events[0].RequestBodyRaw), want) {
		t.Fatalf("expected raw request body to contain %s, got %s", want, string(events[0].RequestBodyRaw))
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
	if !strings.Contains(string(events[1].ResponseBodyRaw), `"resp_ok_1"`) {
		t.Fatalf("expected raw response body to contain response id, got %q", string(events[1].ResponseBodyRaw))
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

func TestGatewayClient_CallProvider_WithStreamReportsReasoningDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先确认仓库结构。"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"我来继续检查。"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
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

	var chunks []StreamChunk
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	})

	resp, err := client.callProvider(ctx, selected, "gpt-4o-mini", &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "我来继续检查。", resp.Content)
	assert.Equal(t, "先确认仓库结构。", resp.Reasoning)
	require.Len(t, chunks, 2)
	assert.Equal(t, EventTypeReasoning, chunks[0].Type)
	assert.Equal(t, "先确认仓库结构。", chunks[0].Content)
	assert.Equal(t, EventTypeText, chunks[1].Type)
	assert.Equal(t, "我来继续检查。", chunks[1].Content)
}

func TestGatewayClient_CallProvider_WithStreamReportsRawResponseBody(t *testing.T) {
	var events []HTTPDebugEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
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
	ctx := WithHTTPDebugReporter(context.Background(), func(event HTTPDebugEvent) {
		events = append(events, event)
	})

	resp, err := client.callProvider(ctx, selected, "gpt-4o-mini", &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world", resp.Content)
	require.Len(t, events, 2)
	assert.Equal(t, "request", events[0].Phase)
	assert.Equal(t, "response", events[1].Phase)
	assert.Contains(t, string(events[1].ResponseBodyRaw), `"content":"Hello"`)
	assert.Contains(t, string(events[1].ResponseBodyRaw), `[DONE]`)
}

func TestGatewayClient_CallProvider_PreservesTypedOpenAIToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ls","arguments":"{\"depth\":1,\"path\":\"E:\\\\projects\\\\ai\\\\ai-agent-runtime\"}"}}]},"finish_reason":"tool_calls"}]}`)
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

	resp, err := client.callProvider(context.Background(), selected, "gpt-4o-mini", &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Equal(t, "ls", resp.ToolCalls[0].Name)
	assert.Equal(t, float64(1), resp.ToolCalls[0].Args["depth"])
	assert.Equal(t, `E:\projects\ai\ai-agent-runtime`, resp.ToolCalls[0].Args["path"])
}

func TestGatewayClient_CallProvider_WithStreamPreservesTypedOpenAIToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ls","arguments":"{\"depth\":1}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
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

	resp, err := client.callProvider(context.Background(), selected, "gpt-4o-mini", &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Equal(t, "ls", resp.ToolCalls[0].Name)
	assert.Equal(t, float64(1), resp.ToolCalls[0].Args["depth"])
}

func TestGatewayClient_CallProvider_WithStreamReportsTextDeltasPreserveWhitespaceFromOpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":" world."},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
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

	var chunks []StreamChunk
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	})

	resp, err := client.callProvider(ctx, selected, "gpt-4o-mini", &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world.", resp.Content)
	require.Len(t, chunks, 2)
	assert.Equal(t, EventTypeText, chunks[0].Type)
	assert.Equal(t, "Hello", chunks[0].Content)
	assert.Equal(t, EventTypeText, chunks[1].Type)
	assert.Equal(t, " world.", chunks[1].Content)
}

func TestGatewayClient_CallProvider_WithStreamPreservesWhitespaceFromResponsesAPI(t *testing.T) {
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
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","output_index":0,"delta":" world."}`,
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

	var chunks []StreamChunk
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	})

	resp, err := client.callProvider(ctx, selected, "gpt-5.4", &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world.", resp.Content)
	require.Len(t, chunks, 2)
	assert.Equal(t, EventTypeText, chunks[0].Type)
	assert.Equal(t, "Hello", chunks[0].Content)
	assert.Equal(t, EventTypeText, chunks[1].Type)
	assert.Equal(t, " world.", chunks[1].Content)
}

func TestGatewayClient_StreamProviderEmitsReasoningChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先梳理需求。"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"开始处理。"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
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

	stream, err := client.streamProvider(context.Background(), selected, "gpt-4o-mini", &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)

	var chunks []StreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
		if chunk.Type == EventTypeError && chunk.Error != "" {
			t.Fatalf("unexpected stream error: %s", chunk.Error)
		}
	}

	require.Len(t, chunks, 3)
	assert.Equal(t, EventTypeReasoning, chunks[0].Type)
	assert.Equal(t, "先梳理需求。", chunks[0].Content)
	assert.Equal(t, EventTypeText, chunks[1].Type)
	assert.Equal(t, "开始处理。", chunks[1].Content)
	assert.Equal(t, EventTypeError, chunks[2].Type)
	assert.Empty(t, chunks[2].Error)
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
