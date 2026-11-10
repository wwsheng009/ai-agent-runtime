package chat

import (
	"testing"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// The allocation-free struct key must be semantically identical to the legacy
// string key. Lock the equivalence for role normalization, content and tool
// signatures so a future map-key optimization cannot silently change checkpoint
// identity.
func TestCanonicalMessageSubstanceStructMatchesStringKey(t *testing.T) {
	messages := []runtimetypes.Message{
		{Role: "user", Content: "hello"},
		{Role: " user ", Content: "hello"},
		{Role: "assistant", Content: "hello"},
		{Role: "assistant", Content: ""},
		{Role: "tool", Content: "result", ToolCallID: "call-1"},
		{Role: "assistant", Content: "with tool", ToolCalls: []runtimetypes.ToolCall{{Name: "search"}}},
		{Role: "assistant", Content: "with other tool", ToolCalls: []runtimetypes.ToolCall{{Name: "read"}}},
	}
	for left := range messages {
		for right := range messages {
			structEqual := canonicalMessageSubstanceOf(messages[left]) == canonicalMessageSubstanceOf(messages[right])
			stringEqual := canonicalMessageSubstanceKey(messages[left]) == canonicalMessageSubstanceKey(messages[right])
			if structEqual != stringEqual {
				t.Fatalf("messages %d/%d substance equality diverged: struct=%v string=%v",
					left, right, structEqual, stringEqual)
			}
		}
	}
}
