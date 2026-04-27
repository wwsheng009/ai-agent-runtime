package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type gatewayTestResourceManager struct {
	selected    *SelectedResource
	selectCalls int
	results     []gatewayTestRecordedResult
}

type gatewayTestRecordedResult struct {
	success    bool
	statusCode int
	err        string
}

func (m *gatewayTestResourceManager) SelectResource(retryInfo RetryInfo) (*SelectedResource, error) {
	m.selectCalls++
	return m.selected, nil
}

func (m *gatewayTestResourceManager) RecordResult(selected *SelectedResource, success bool, err error, statusCode int, latencyMs int64) {
	record := gatewayTestRecordedResult{
		success:    success,
		statusCode: statusCode,
	}
	if err != nil {
		record.err = err.Error()
	}
	m.results = append(m.results, record)
}

func TestGatewayClient_ConvertTools_CodexIncludesRuntimeTools(t *testing.T) {
	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	tools := []types.ToolDefinition{
		{Name: "bash", Description: "执行 Shell 命令", Parameters: map[string]interface{}{"type": "object"}},
	}

	got := client.convertTools(tools, "codex", "", nil)
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

func TestGatewayClient_ConvertTools_CodexAddsImageGenerationWhenModelCapabilityAllows(t *testing.T) {
	client := &GatewayClient{tokenizer: NewTokenizer("openai")}

	got := client.convertTools(nil, "codex", "gpt-5.4", map[string]agentconfig.ModelCapabilitySpec{
		"gpt-5.4": {
			InputModalities: []string{"text", "image"},
			NativeTools: agentconfig.NativeToolCapabilities{
				ImageGeneration: true,
			},
		},
	})
	if got == nil {
		t.Fatal("expected tools, got nil")
	}

	toolList, ok := got.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map[string]interface{}, got %T", got)
	}

	var sawImageGeneration bool
	for _, tool := range toolList {
		if tool["type"] == "image_generation" {
			sawImageGeneration = true
			assert.Equal(t, "png", tool["output_format"])
		}
	}
	assert.True(t, sawImageGeneration, "expected image_generation native tool in %#v", toolList)
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
		Metadata: map[string]interface{}{
			"trace_id": "trace-1",
			"tool_availability": map[string]interface{}{
				"requires_active_team_run": []string{"read_task_spec"},
			},
		},
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
	if events[0].RequestMetadata["trace_id"] != "trace-1" {
		t.Fatalf("expected request trace metadata, got %+v", events[0].RequestMetadata)
	}
	availability, ok := events[0].RequestMetadata["tool_availability"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_availability metadata, got %+v", events[0].RequestMetadata["tool_availability"])
	}
	requires, ok := availability["requires_active_team_run"].([]string)
	if !ok || len(requires) != 1 || requires[0] != "read_task_spec" {
		t.Fatalf("unexpected requires_active_team_run metadata: %+v", availability["requires_active_team_run"])
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

func TestGatewayClient_CallProvider_UsesConfiguredAPIPathOverride(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "deepseek_beta",
			Type:    "openai",
			BaseURL: server.URL,
			APIPath: "/v1/completions",
		},
		KeyValue: "test-key",
	}

	resp, err := client.callProvider(context.Background(), selected, "deepseek-v4-pro", &LLMRequest{
		Model: "deepseek-v4-pro",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, "/v1/completions", capturedPath)
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

func TestGatewayClient_CallProvider_WithStreamUsesProviderReportedUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`+"\n\n")
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
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 11, resp.Usage.PromptTokens)
	assert.Equal(t, 2, resp.Usage.CompletionTokens)
	assert.Equal(t, 13, resp.Usage.TotalTokens)
	assert.Equal(t, usageSourceProviderReported, resp.Metadata["usage_source"])
}

func TestGatewayClient_CallProvider_SavesGeneratedImagesAndReturnsMetadata(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"resp_image_1",
			"model":"gpt-5.4",
			"stop_reason":"end_turn",
			"output":[
				{
					"type":"image_generation_call",
					"id":"img:1",
					"status":"completed",
					"revised_prompt":"draw a red square",
					"result":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4////fwAJ+wP9KobjigAAAABJRU5ErkJggg=="
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
		}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "codex_ee",
			Type:    "codex",
			BaseURL: server.URL,
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"gpt-5.4": {
					InputModalities: []string{"text", "image"},
					NativeTools: agentconfig.NativeToolCapabilities{
						ImageGeneration: true,
					},
				},
			},
		},
		KeyValue: "test-key",
	}

	outputDir := t.TempDir()
	resp, err := client.callProvider(context.Background(), selected, "gpt-5.4", &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "draw a red square",
		}},
		Metadata: map[string]interface{}{
			MetadataKeyGeneratedImageOutputDir: outputDir,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, GeneratedImageSummary([]GeneratedImage{{SavedPath: filepath.Join(outputDir, "img_1.png")}}), resp.Content)

	tools, ok := capturedBody["tools"].([]interface{})
	require.True(t, ok)
	var sawImageGeneration bool
	for _, raw := range tools {
		tool, ok := raw.(map[string]interface{})
		require.True(t, ok)
		if tool["type"] == "image_generation" {
			sawImageGeneration = true
			assert.Equal(t, "png", tool["output_format"])
		}
	}
	assert.True(t, sawImageGeneration)

	generated := decodeSliceOfMaps(resp.Metadata[MetadataKeyGeneratedImages])
	require.Len(t, generated, 1)
	assert.Equal(t, filepath.Join(outputDir, "img_1.png"), generated[0]["saved_path"])
	_, statErr := os.Stat(filepath.Join(outputDir, "img_1.png"))
	require.NoError(t, statErr)
}

func TestGatewayClient_CallProvider_OmitsCodexMaxOutputTokensWhenProviderDisablesIt(t *testing.T) {
	var capturedBody map[string]interface{}
	disabled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:                    "codex_cli_gpt",
			Type:                    "codex",
			BaseURL:                 server.URL,
			SupportsMaxOutputTokens: &disabled,
		},
		KeyValue: "test-key",
	}

	_, err := client.callProvider(context.Background(), selected, "gpt-5.4", &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		MaxTokens: 4096,
	})
	require.NoError(t, err)

	_, exists := capturedBody["max_output_tokens"]
	assert.False(t, exists, "max_output_tokens should not be forwarded")
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
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"claude-sonnet-4-6": {
					ReasoningEffortBudgets: map[string]int{
						"high": 16384,
					},
				},
			},
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
	assert.Equal(t, "enabled", rawThinking["type"])
	assert.Equal(t, float64(16384), rawThinking["budget_tokens"])
	assert.Equal(t, "interleaved-thinking-2025-05-14", capturedBeta)
}

func TestGatewayClient_CallProvider_OpenAIPreservesReasoningEffort(t *testing.T) {
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
	assert.Equal(t, "xhigh", capturedBody["reasoning_effort"])
}

func TestGatewayClient_CallProvider_UsesConfiguredReasoningModelFlag(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	client := &GatewayClient{tokenizer: NewTokenizer("openai")}
	selected := &SelectedResource{
		Provider: &ProviderResource{
			Name:    "deepseek_openai",
			Type:    "openai",
			BaseURL: server.URL,
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"deepseek-v4-pro": {
					ReasoningModel:   true,
					ReasoningEfforts: []string{"high", "max"},
				},
			},
		},
		KeyValue: "test-key",
	}

	resp, err := client.callProvider(context.Background(), selected, "deepseek-v4-pro", &LLMRequest{
		Model:           "deepseek-v4-pro",
		ReasoningEffort: "max",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	if _, exists := capturedBody["temperature"]; exists {
		t.Fatalf("expected temperature to be omitted for configured reasoning model, got %#v", capturedBody["temperature"])
	}
}

func TestGatewayClientCall_DoesNotRetryInvalidRequestHTTPError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"No tool call found for function call output with call_id call_1.","type":"invalid_request_error","param":"input","code":null}}`)
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "codex_ee",
				Type:    "codex",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(3)

	_, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 400")
	assert.Equal(t, 1, requests)
	assert.Equal(t, 1, rm.selectCalls)
	require.Len(t, rm.results, 1)
	assert.False(t, rm.results[0].success)
	assert.Equal(t, http.StatusBadRequest, rm.results[0].statusCode)
}

func TestGatewayClientCall_RetriesRetryableHTTPError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"temporary upstream failure"}}`)
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "codex_ee",
				Type:    "codex",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(3)

	_, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all retry attempts failed")
	assert.Equal(t, 3, requests)
	assert.Equal(t, 3, rm.selectCalls)
	require.Len(t, rm.results, 3)
	for _, result := range rm.results {
		assert.False(t, result.success)
		assert.Equal(t, http.StatusInternalServerError, result.statusCode)
	}
}

func TestGatewayClientCall_RetryRuleOverridesMaxRetriesForHTTP503(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"temporary upstream failure"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "openai_ee",
				Type:    "openai",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(1)
	client.SetRetryRules([]RetryRule{
		{
			Name:       "http_5xx_retry",
			Enabled:    true,
			MaxRetries: 2,
			StatusCode: RetryStatusCodeMatcher{Range: "500-504"},
		},
	})

	resp, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 2, requests)
	assert.Equal(t, 2, rm.selectCalls)
	require.Len(t, rm.results, 2)
	assert.False(t, rm.results[0].success)
	assert.True(t, rm.results[1].success)
}

func TestGatewayClientCall_UsesRetryAfterHeaderAndReportsRetryDebugEvent(t *testing.T) {
	requests := 0
	var events []HTTPDebugEvent
	var retryEvents []RetryEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.Header().Set("Retry-After", "0.025")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limit reached"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "openai_ee",
				Type:    "openai",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(2)

	ctx := WithHTTPDebugReporter(context.Background(), func(event HTTPDebugEvent) {
		events = append(events, event)
	})
	ctx = WithRetryEventReporter(ctx, func(event RetryEvent) {
		retryEvents = append(retryEvents, event)
	})
	resp, err := client.Call(ctx, &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 2, requests)
	assert.Equal(t, 2, rm.selectCalls)

	require.Len(t, events, 5)
	assert.Equal(t, "request", events[0].Phase)
	assert.Equal(t, 1, events[0].Attempt)
	assert.Equal(t, 2, events[0].MaxAttempts)
	assert.Equal(t, "response", events[1].Phase)
	assert.Equal(t, 429, events[1].ResponseStatusCode)
	assert.Equal(t, 1, events[1].Attempt)
	assert.Equal(t, "retry", events[2].Phase)
	assert.Equal(t, "rate_limit", events[2].RetryReason)
	assert.EqualValues(t, 25, events[2].RetryDelayMS)
	assert.Equal(t, 1, events[2].Attempt)
	assert.Equal(t, 2, events[2].MaxAttempts)
	assert.Equal(t, "request", events[3].Phase)
	assert.Equal(t, 2, events[3].Attempt)
	assert.Equal(t, "response", events[4].Phase)
	assert.Equal(t, 2, events[4].Attempt)
	assert.Equal(t, 200, events[4].ResponseStatusCode)

	require.Len(t, retryEvents, 1)
	assert.Equal(t, "gateway_client", retryEvents[0].Source)
	assert.Equal(t, "openai_ee", retryEvents[0].Provider)
	assert.Equal(t, "openai", retryEvents[0].Protocol)
	assert.Equal(t, "gpt-5.4-mini", retryEvents[0].Model)
	assert.Equal(t, 1, retryEvents[0].Attempt)
	assert.Equal(t, 2, retryEvents[0].MaxAttempts)
	assert.Equal(t, "rate_limit", retryEvents[0].RetryReason)
	assert.EqualValues(t, 25, retryEvents[0].RetryDelayMS)
	assert.Contains(t, retryEvents[0].Error, "HTTP 429")
}

func TestGatewayClientCall_DoesNotRetryInvalidRequestResponseError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4-mini"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"message":"No tool call found for function call output with call_id call_1.","type":"invalid_request_error"}}`,
			"",
		}, "\n"))
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "codex_ee",
				Type:    "codex",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(3)

	_, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to handle response")
	assert.Equal(t, 1, requests)
	assert.Equal(t, 1, rm.selectCalls)
	require.Len(t, rm.results, 1)
	assert.False(t, rm.results[0].success)
	assert.Equal(t, 0, rm.results[0].statusCode)
}

func TestGatewayClientCall_DoesNotRetryMissingRequiredParameterResponseError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4-mini"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"message":"Missing required parameter: 'input[11].summary'.","code":"missing_required_parameter"}}`,
			"",
		}, "\n"))
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "codex_ee",
				Type:    "codex",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(3)

	_, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Missing required parameter")
	assert.Equal(t, 1, requests)
	assert.Equal(t, 1, rm.selectCalls)
	require.Len(t, rm.results, 1)
	assert.False(t, rm.results[0].success)
	assert.Equal(t, 0, rm.results[0].statusCode)
}

func TestGatewayClientCall_WithStream_RetriesIncompleteStreamBeforeFirstDelta(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{}}]}`+"\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "openai_ee",
				Type:    "openai",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(2)

	resp, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "hello", resp.Content)
	assert.Equal(t, 2, requests)
	assert.Equal(t, 2, rm.selectCalls)
	require.Len(t, rm.results, 2)
	assert.False(t, rm.results[0].success)
	assert.True(t, rm.results[1].success)
}

func TestGatewayClientCall_WithStream_DoesNotRetryAfterTextDelta(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}`+"\n\n")
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "openai_ee",
				Type:    "openai",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(3)

	_, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to handle stream response")
	assert.Contains(t, err.Error(), "stream disconnected before completion")
	assert.Equal(t, 1, requests)
	assert.Equal(t, 1, rm.selectCalls)
	require.Len(t, rm.results, 1)
	assert.False(t, rm.results[0].success)
}

func TestGatewayClientCall_WithStream_RetriesAfterReasoningOnlyDeltaWithoutContent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先确认上下文。"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "openai_ee",
				Type:    "openai",
				BaseURL: server.URL,
			},
			KeyValue: "test-key",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")
	client.SetMaxRetries(2)

	resp, err := client.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "retry reasoning only",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "hello", resp.Content)
	assert.Equal(t, 2, requests)
	assert.Equal(t, 2, rm.selectCalls)
	require.Len(t, rm.results, 2)
	assert.False(t, rm.results[0].success)
	assert.True(t, rm.results[1].success)
}
