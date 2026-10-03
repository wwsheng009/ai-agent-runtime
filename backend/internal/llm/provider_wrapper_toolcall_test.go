package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// OpenAI Chat Completions 的纯 tool-call 流（无文本增量）也必须向
// StreamReporter 上报 tool_call 增量，否则上层首字时间无从打点。
func TestProviderWrapper_CallReportsToolCallChunkForOpenAIStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"git status\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	var toolCallChunks, textChunks int
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		switch chunk.Type {
		case EventTypeToolCall:
			toolCallChunks++
		case EventTypeText:
			textChunks++
		}
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "run git status",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 0, textChunks, "tool-call-only stream must not fabricate text deltas")
	assert.GreaterOrEqual(t, toolCallChunks, 1, "tool-call stream must surface a tool_call chunk for TTFT")
}

// 纯文本流不得产生 tool_call 增量，避免首字信号来源被污染。
func TestProviderWrapper_CallDoesNotReportToolCallChunkForTextStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	var toolCallChunks, textChunks int
	ctx := WithStreamReporter(context.Background(), func(chunk StreamChunk) {
		switch chunk.Type {
		case EventTypeToolCall:
			toolCallChunks++
		case EventTypeText:
			textChunks++
		}
	})

	resp, err := provider.Call(ctx, &LLMRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{{
			Role:    "user",
			Content: "say hello",
		}},
		Stream: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.GreaterOrEqual(t, textChunks, 1)
	assert.Equal(t, 0, toolCallChunks, "text-only stream must not emit tool_call chunks")
}
