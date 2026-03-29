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
	assert.Contains(t, events[0].RequestBody, `"model":"gpt-5.2-codex"`)
	assert.Contains(t, events[0].RequestBody, `"input"`)
	assert.Contains(t, events[0].RequestBody, `"hello"`)
	assert.Equal(t, "response", events[1].Phase)
	assert.Equal(t, 200, events[1].ResponseStatusCode)
	assert.Contains(t, events[1].ResponseBodyPreview, `"resp_ok_1"`)
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
