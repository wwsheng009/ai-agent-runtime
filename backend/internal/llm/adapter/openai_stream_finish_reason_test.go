package adapter

import (
	"strings"
	"testing"
)

// A complete text answer mislabeled as finish_reason=tool_calls must be treated
// as a final stop. Retrying it cannot recover a missing tool call and only
// duplicates the already-streamed answer.
func TestOpenAIHandleResponse_StreamNormalizesContentOnlyToolCallsFinish(t *testing.T) {
	msg, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"final answer"},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if got, _ := msg["content"].(string); got != "final answer" {
		t.Fatalf("content = %q, want final answer", got)
	}
	if got, _ := msg["finish_reason"].(string); got != "stop" {
		t.Fatalf("finish_reason = %q, want normalized stop", got)
	}
	if calls, _ := msg["tool_calls"].([]map[string]interface{}); len(calls) != 0 {
		t.Fatalf("tool_calls = %#v, want none", calls)
	}
}

// An empty tool-call response is still a protocol error: there is no text to
// preserve and no call to execute, so replay/retry remains the right recovery.
func TestOpenAIHandleResponse_StreamKeepsMissingToolCallForEmptyContent(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected missing_tool_call protocol error")
	}
	if !strings.Contains(err.Error(), "missing_tool_call") {
		t.Fatalf("error = %v, want missing_tool_call", err)
	}
}
