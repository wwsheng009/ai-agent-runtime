package providercompat

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

func TestNormalizeStreamChunk_StripsResponseMarkers(t *testing.T) {
	ctx := Context{
		Protocol:        "openai",
		Profile:         "opencode-console-go-2026-07",
		Model:           "minimax-m3",
		ResponseMarkers: []string{"]<]minimax[>["},
	}
	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"role":    "assistant",
					"content": ":]<]minimax[>[<tool_call>\n]<]minimax[>[<invoke name=\"grep\">",
				},
			},
		},
	}
	normalized := NormalizeStreamChunk(ctx, chunk)
	delta := normalized["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	got := delta["content"].(string)
	want := ":<tool_call>\n<invoke name=\"grep\">"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestNormalizeStreamChunk_StripsReasoningContentMarkers(t *testing.T) {
	ctx := Context{
		Protocol:        "openai",
		Model:           "minimax-m3",
		ResponseMarkers: []string{"]<]minimax[>["},
	}
	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"reasoning_content": "a]<]minimax[>[b",
					"content":          "plain",
				},
			},
		},
	}
	normalized := NormalizeStreamChunk(ctx, chunk)
	delta := normalized["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if got := delta["reasoning_content"].(string); got != "ab" {
		t.Fatalf("reasoning_content = %q, want ab", got)
	}
	if got := delta["content"].(string); got != "plain" {
		t.Fatalf("content must stay untouched, got %q", got)
	}
}

func TestNormalizeStreamChunk_NoMarkersPassesThrough(t *testing.T) {
	ctx := Context{Protocol: "openai"}
	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{"content": "hello"},
			},
		},
	}
	normalized := NormalizeStreamChunk(ctx, chunk)
	delta := normalized["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if got := delta["content"].(string); got != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
}

func TestNormalizeStreamChunk_MarkerInToolCallChunkRecoversToolCall(t *testing.T) {
	ctx := Context{
		Protocol:        "openai",
		Model:           "minimax-m3",
		ResponseMarkers: []string{"]<]minimax[>["},
	}
	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "]<]minimax[>[<invoke name=\"grep\">]<]minimax[>[<path>C:\\Users\\vince\\.aicli]<]minimax[>[</path>]<]minimax[>[</invoke>",
				},
			},
		},
	}
	normalized := NormalizeStreamChunk(ctx, chunk)
	delta := normalized["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	got := delta["content"].(string)
	want := "<invoke name=\"grep\"><path>C:\\Users\\vince\\.aicli</path></invoke>"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// TestStreamReaderMarkerStrip_RecoversToolCallMarkup replays the failing
// session shape end to end: a providercompat-wrapped stream containing
// "]<]minimax[>[ " markers inside a <tool_call> block must yield a valid
// tool call instead of a polluted tool name.
func TestStreamReaderMarkerStrip_RecoversToolCallMarkup(t *testing.T) {
	ctx := Context{
		Protocol:        "openai",
		Profile:         "opencode-console-go-2026-07",
		Model:           "minimax-m3",
		ResponseMarkers: []string{"]<]minimax[>["},
	}
	sse := strings.Join([]string{
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"minimax-m3","choices":[{"index":0,"delta":{"content":"Let me search:]<]minimax[>[<tool_call>"}}]}`,
		"",
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"minimax-m3","choices":[{"index":0,"delta":{"content":"]<]minimax[>[grep]<]minimax[>[<arg_key>path</arg_key>]<]minimax[>[<arg_value>C:\\Users\\vince\\.aicli\\chat-logs</arg_value>"}}]}`,
		"",
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"minimax-m3","choices":[{"index":0,"delta":{"content":"]<]minimax[>[<arg_key>pattern</arg_key>]<]minimax[>[<arg_value>x-opencode-session</arg_value>]<]minimax[>[</tool_call>"},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	reader := NormalizeStreamReader(ctx, strings.NewReader(sse))
	msg, err := (&llmadapter.OpenAIAdapter{}).HandleResponse(true, reader, llmadapter.StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if content, _ := msg["content"].(string); content != "Let me search:" {
		t.Fatalf("content = %q, want %q", content, "Let me search:")
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload, got %#v", toolCalls[0]["function"])
	}
	if name, _ := fn["name"].(string); name != "grep" {
		t.Fatalf("expected tool name grep, got %q", name)
	}
	argsJSON, _ := fn["arguments"].(string)
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("arguments are not valid JSON: %q (%v)", argsJSON, err)
	}
	if got, _ := args["path"].(string); got != `C:\Users\vince\.aicli\chat-logs` {
		t.Fatalf("unexpected path: %q", got)
	}
	if got, _ := args["pattern"].(string); got != "x-opencode-session" {
		t.Fatalf("unexpected pattern: %q", got)
	}
	_ = reader.(io.Closer).Close()
}