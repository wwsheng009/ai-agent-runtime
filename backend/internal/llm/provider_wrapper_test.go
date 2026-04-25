package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestNewProvider_WorksWithUnifiedRuntimeInterface(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello from wrapper"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "wrapper-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("wrapper-model", provider))

	resp, err := runtime.Call(context.Background(), &LLMRequest{
		Model: "wrapper-model",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello from wrapper", resp.Content)
	assert.Equal(t, "wrapper-model", resp.Model)
	if assert.NotNil(t, resp.Usage) {
		assert.Greater(t, resp.Usage.TotalTokens, 0)
	}

	assert.Greater(t, provider.CountTokens("hello world"), 0)
	assert.True(t, provider.GetCapabilities().SupportsStreaming)

	catalogProvider, ok := provider.(ModelCatalogProvider)
	require.True(t, ok)
	assert.NotEmpty(t, catalogProvider.SupportedModels())
}

func TestNewProvider_UsesConfiguredAPIPathOverride(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
		APIPath: "/v1/completions",
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
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

func TestProviderWrapper_StreamImplementsUnifiedInterface(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello "}}]}

`)
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}

`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	stream, err := provider.Stream(context.Background(), &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "stream this",
		}},
		Stream: true,
	})
	require.NoError(t, err)

	var builder strings.Builder
	var sawDone bool
	for chunk := range stream {
		if chunk.Type == EventTypeText {
			builder.WriteString(chunk.Content)
		}
		if chunk.Type == EventTypeDone && chunk.Done {
			sawDone = true
		}
		if chunk.Type == EventTypeError {
			t.Fatalf("unexpected stream error: %s", chunk.Error)
		}
	}

	assert.Equal(t, "hello world", builder.String())
	assert.True(t, sawDone)
}

func TestProviderWrapper_OpenAICall_ParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"chatcmpl-tool-test",
			"object":"chat.completion",
			"created":1,
			"model":"z-ai/glm5",
			"choices":[
				{
					"index":0,
					"message":{
						"role":"assistant",
						"content":"",
						"tool_calls":[
							{
								"id":"call_1",
								"type":"function",
								"function":{
									"name":"spawn_team",
									"arguments":"{\"teammates\":[{\"name\":\"executor\"}],\"tasks\":[{\"title\":\"task-1\"}]}"
								}
							}
						]
					},
					"finish_reason":"tool_calls"
				}
			],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
		}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "openai",
		BaseURL:      server.URL,
		DefaultModel: "z-ai/glm5",
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "z-ai/glm5",
		Messages: []types.Message{{
			Role:    "user",
			Content: "Create a team now.",
		}},
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "spawn_team", resp.ToolCalls[0].Name)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Empty(t, resp.Content)
}

func TestProviderWrapper_CallUsesConfiguredHTTPProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Proxy-Seen") != "true" {
			http.Error(w, "direct request not expected", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-proxy-test","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello via proxy"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer upstream.Close()

	directTransport := http.DefaultTransport.(*http.Transport).Clone()
	directTransport.Proxy = nil

	var proxyHits int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++

		targetURL := r.URL.String()
		require.NotEmpty(t, targetURL)

		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
		require.NoError(t, err)
		proxyReq.Header = r.Header.Clone()
		proxyReq.Header.Set("X-Proxy-Seen", "true")

		proxyResp, err := directTransport.RoundTrip(proxyReq)
		require.NoError(t, err)
		defer proxyResp.Body.Close()

		for key, values := range proxyResp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(proxyResp.StatusCode)
		_, copyErr := io.Copy(w, proxyResp.Body)
		require.NoError(t, copyErr)
	}))
	defer proxyServer.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: upstream.URL,
		Proxy: &agentconfig.ProxyConfig{
			Enabled: true,
			HTTP:    proxyServer.URL,
		},
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello through proxy",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello via proxy", resp.Content)
	assert.Greater(t, proxyHits, 0)
}

func TestProviderWrapper_CodexCall_SendsAndParsesToolCalls(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"resp_tool_1",
			"model":"gpt-5.2-codex",
			"stop_reason":"tool_call",
			"output":[
				{
					"type":"function_call",
					"call_id":"call_1",
					"name":"spawn_team",
					"arguments":"{\"teammates\":[{\"name\":\"executor\"}],\"tasks\":[{\"title\":\"task-1\",\"goal\":\"run the task\"}]}"
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
		}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.2-codex",
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.2-codex",
		Messages: []types.Message{{
			Role:    "user",
			Content: "Create a team now.",
		}},
		Tools: []types.ToolDefinition{{
			Name:        "spawn_team",
			Description: "Create a team with teammates and tasks.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"teammates": map[string]interface{}{"type": "array"},
					"tasks":     map[string]interface{}{"type": "array"},
				},
			},
		}},
	})
	require.NoError(t, err)

	tools, ok := capturedBody["tools"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, tools)

	var sawSpawnTeam bool
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		require.True(t, ok)
		if toolMap["name"] == "spawn_team" {
			sawSpawnTeam = true
			break
		}
	}
	assert.True(t, sawSpawnTeam, "expected spawn_team in outgoing codex tools payload")
	assert.Equal(t, "auto", capturedBody["tool_choice"])

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "spawn_team", resp.ToolCalls[0].Name)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Empty(t, resp.Content)
}

func TestProviderWrapper_CodexCall_SavesGeneratedImagesAndReturnsMetadata(t *testing.T) {
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

	outputDir := t.TempDir()
	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.4",
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5.4": {
				InputModalities: []string{"text", "image"},
				NativeTools: agentconfig.NativeToolCapabilities{
					ImageGeneration: true,
				},
			},
		},
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "Draw me a red square.",
		}},
		Metadata: map[string]interface{}{
			MetadataKeyGeneratedImageOutputDir: outputDir,
		},
	})
	require.NoError(t, err)
	require.Equal(t, GeneratedImageSummary([]GeneratedImage{{SavedPath: filepath.Join(outputDir, "img_1.png")}}), resp.Content)

	tools, ok := capturedBody["tools"].([]interface{})
	require.True(t, ok)
	var sawImageGeneration bool
	for _, raw := range tools {
		tool, ok := raw.(map[string]interface{})
		require.True(t, ok)
		if tool["type"] == "image_generation" {
			sawImageGeneration = true
			require.Equal(t, "png", tool["output_format"])
		}
	}
	require.True(t, sawImageGeneration, "expected native image_generation tool in request")

	generated := decodeSliceOfMaps(resp.Metadata[MetadataKeyGeneratedImages])
	require.Len(t, generated, 1)
	require.Equal(t, filepath.Join(outputDir, "img_1.png"), generated[0]["saved_path"])
	_, statErr := os.Stat(filepath.Join(outputDir, "img_1.png"))
	require.NoError(t, statErr)
}

func TestProviderWrapper_CodexCall_FollowUpIncludesFunctionCallAndOutput(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"resp_follow_up_1",
			"model":"gpt-5.2-codex",
			"stop_reason":"end_turn",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
			],
			"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}
		}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.2-codex",
	})
	require.NoError(t, err)

	_, err = provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.2-codex",
		Messages: []types.Message{
			{
				Role:    "user",
				Content: "Create a team now.",
			},
			{
				Role: "assistant",
				ToolCalls: []types.ToolCall{{
					ID:   "call_1",
					Name: "spawn_team",
					Args: map[string]interface{}{
						"teammates": []map[string]interface{}{{"name": "executor"}},
						"tasks":     []map[string]interface{}{{"title": "task-1", "goal": "run the task"}},
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    `{"team_id":"team_1"}`,
			},
		},
	})
	require.NoError(t, err)

	input, ok := capturedBody["input"].([]interface{})
	require.True(t, ok)

	var sawFunctionCall bool
	var sawFunctionCallOutput bool
	for _, item := range input {
		itemMap, ok := item.(map[string]interface{})
		require.True(t, ok)
		switch itemMap["type"] {
		case "function_call":
			if itemMap["name"] == "spawn_team" && itemMap["call_id"] == "call_1" {
				sawFunctionCall = true
			}
		case "function_call_output":
			if itemMap["call_id"] == "call_1" {
				sawFunctionCallOutput = true
			}
		}
	}

	assert.True(t, sawFunctionCall, "expected follow-up request to include original function_call item")
	assert.True(t, sawFunctionCallOutput, "expected follow-up request to include function_call_output item")
}

func TestProviderWrapper_CallReportsHTTPDebugPayload(t *testing.T) {
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

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.2-codex",
	})
	require.NoError(t, err)

	ctx := WithHTTPDebugReporter(context.Background(), func(event HTTPDebugEvent) {
		events = append(events, event)
	})
	_, err = provider.Call(ctx, &LLMRequest{
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
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "provider_wrapper", events[0].Source)
	assert.Equal(t, "request", events[0].Phase)
	assert.Equal(t, "codex", events[0].Protocol)
	assert.Equal(t, "gpt-5.2-codex", events[0].Model)
	assert.Equal(t, http.MethodPost, events[0].Method)
	assert.Contains(t, events[0].URL, "/v1/responses")
	assert.Greater(t, events[0].RequestBodyBytes, 0)
	assert.Equal(t, "trace-1", events[0].RequestMetadata["trace_id"])
	availability, ok := events[0].RequestMetadata["tool_availability"].(map[string]interface{})
	require.True(t, ok)
	requires, ok := availability["requires_active_team_run"].([]string)
	require.True(t, ok)
	require.Len(t, requires, 1)
	assert.Equal(t, "read_task_spec", requires[0])
	assert.Contains(t, events[0].RequestBody, `"model":"gpt-5.2-codex"`)
	assert.Contains(t, events[0].RequestBody, `"input"`)
	assert.Contains(t, events[0].RequestBody, `"hello"`)
	assert.Contains(t, string(events[0].RequestBodyRaw), `"hello"`)
	assert.Equal(t, "response", events[1].Phase)
	assert.Equal(t, 200, events[1].ResponseStatusCode)
	assert.Contains(t, events[1].ResponseBodyPreview, `"resp_ok_1"`)
	assert.Contains(t, string(events[1].ResponseBodyRaw), `"resp_ok_1"`)
}

func TestProviderWrapper_CallReportsStreamDeltasFromSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	var deltas []string
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		if chunk.Type == EventTypeText {
			deltas = append(deltas, chunk.Content)
		}
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "stream this",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", resp.Content)
	assert.Equal(t, []string{"hello ", "world"}, deltas)
}

func TestProviderWrapper_CallReportsTextDeltasPreserveWhitespaceFromOpenAIStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":" world."},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	var deltas []string
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		if chunk.Type == EventTypeText {
			deltas = append(deltas, chunk.Content)
		}
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "stream this",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world.", resp.Content)
	assert.Equal(t, []string{"Hello", " world."}, deltas)
}

func TestProviderWrapper_CallReportsReasoningDeltasFromSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先看目录。"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"我来查看目录。"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	var chunks []StreamChunk
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "stream this",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "我来查看目录。", resp.Content)
	assert.Equal(t, "先看目录。", resp.Reasoning)
	require.Len(t, chunks, 2)
	assert.Equal(t, EventTypeReasoning, chunks[0].Type)
	assert.Equal(t, "先看目录。", chunks[0].Content)
	assert.Equal(t, EventTypeText, chunks[1].Type)
	assert.Equal(t, "我来查看目录。", chunks[1].Content)
}

func TestProviderWrapper_CallWithStreamReportsRawResponseBody(t *testing.T) {
	var events []HTTPDebugEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	ctx := WithHTTPDebugReporter(context.Background(), func(event HTTPDebugEvent) {
		events = append(events, event)
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "stream this",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", resp.Content)
	require.Len(t, events, 2)
	assert.Equal(t, "request", events[0].Phase)
	assert.Equal(t, "response", events[1].Phase)
	assert.Contains(t, string(events[1].ResponseBodyRaw), `"content":"hello "`)
	assert.Contains(t, string(events[1].ResponseBodyRaw), `[DONE]`)
}

func TestProviderWrapper_CallReplaysDeepSeekEmptyReasoningContentForToolCalls(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "deepseek-v4-flash",
		Messages: []types.Message{
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []types.ToolCall{
					{ID: "call_view", Name: "view"},
				},
				Metadata: types.NewMetadata(),
			},
			{
				Role:       "tool",
				Content:    "diff preview",
				ToolCallID: "call_view",
				Metadata:   types.NewMetadata(),
			},
			{
				Role:     "user",
				Content:  "继续",
				Metadata: types.NewMetadata(),
			},
		},
		ReasoningEffort: "high",
		Metadata: map[string]interface{}{
			"thinking": map[string]interface{}{"type": "enabled"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	require.Len(t, messages, 3)

	assistantPayload, ok := messages[0].(map[string]interface{})
	require.True(t, ok)
	got, exists := assistantPayload["reasoning_content"]
	require.True(t, exists, "expected reasoning_content to be present in replay payload")
	assert.Equal(t, "", got)
}

func TestProviderWrapper_CallPreservesExplicitEmptyReasoningContentInResponseMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "deepseek-v4-flash",
		Messages: []types.Message{{
			Role:    "user",
			Content: "继续",
		}},
		Stream: true,
		Metadata: map[string]interface{}{
			"thinking": map[string]interface{}{"type": "enabled"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)

	got, exists := resp.Metadata["reasoning_content"]
	require.True(t, exists, "expected empty reasoning_content to survive stream aggregation")
	assert.Equal(t, "", got)
}

func TestProviderWrapper_StreamEmitsReasoningChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先整理上下文。"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"开始处理。"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	stream, err := provider.Stream(context.Background(), &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "stream this",
		}},
		Stream: true,
	})
	require.NoError(t, err)

	var got []StreamChunk
	for chunk := range stream {
		got = append(got, chunk)
		if chunk.Type == EventTypeError && chunk.Error != "" {
			t.Fatalf("unexpected stream error: %s", chunk.Error)
		}
	}

	require.Len(t, got, 3)
	assert.Equal(t, EventTypeReasoning, got[0].Type)
	assert.Equal(t, "先整理上下文。", got[0].Content)
	assert.Equal(t, EventTypeText, got[1].Type)
	assert.Equal(t, "开始处理。", got[1].Content)
	assert.Equal(t, EventTypeDone, got[2].Type)
}

func TestProviderWrapper_CodexCall_WithStreamAggregatesSSEResponse(t *testing.T) {
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

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.4",
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
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

func TestProviderWrapper_CodexCall_WithStreamPreservesWhitespaceAcrossTextDeltas(t *testing.T) {
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

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.4",
	})
	require.NoError(t, err)

	var deltas []string
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		if chunk.Type == EventTypeText {
			deltas = append(deltas, chunk.Content)
		}
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world.", resp.Content)
	assert.Equal(t, []string{"Hello", " world."}, deltas)
}

func TestProviderWrapper_CodexCall_PropagatesPromptCacheFieldsFromMetadata(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","stop_reason":"end_turn"}}`,
			"",
		}, "\n"))
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.4",
	})
	require.NoError(t, err)

	_, err = provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Metadata: map[string]interface{}{
			"session_id": "session-123",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "session-123", capturedBody["prompt_cache_key"])
}

func TestProviderWrapper_CodexCall_WithStreamReturnsProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`,
			"",
			"event: error",
			`data: {"type":"error","code":"internal_server_error","message":"connection reset by peer"}`,
			"",
			"event: response.failed",
			`data: {"status":"failed","error":{"message":"no available resource: no available key/provider"}}`,
			"",
		}, "\n"))
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "codex",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.4",
	})
	require.NoError(t, err)

	_, err = provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
		Stream: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to handle stream response")
	assert.Contains(t, err.Error(), "no available resource: no available key/provider")
}

func TestProviderWrapper_AnthropicCall_PropagatesThinkingToBodyAndHeader(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedBeta string
	budget := 8192

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBeta = r.Header.Get("anthropic-beta")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "anthropic",
		BaseURL:      server.URL,
		DefaultModel: "claude-sonnet-4-6",
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "claude-sonnet-4-6",
		Thinking: &ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
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
	assert.Equal(t, float64(8192), rawThinking["budget_tokens"])
	assert.Equal(t, "interleaved-thinking-2025-05-14", capturedBeta)
}

func TestProviderWrapper_OpenAICall_MapsThinkingToReasoningEffort(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:         "openai",
		BaseURL:      server.URL,
		DefaultModel: "gpt-5.4",
	})
	require.NoError(t, err)

	budget := 32000
	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model: "gpt-5.4",
		Thinking: &ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, "high", capturedBody["reasoning_effort"])
}
