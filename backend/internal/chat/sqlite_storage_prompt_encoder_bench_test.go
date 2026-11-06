package chat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPromptPayloadEncoderMatchesJSONMarshal(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("继续"),
		*types.NewAssistantMessage("answer"),
		{
			Role:    "assistant",
			Content: "tool call",
			ToolCalls: []types.ToolCall{{
				ID:   "call-1",
				Name: "shell",
				Args: map[string]interface{}{"command": "go test ./...", "nested": map[string]any{"ok": true}},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "{\"result\":\"<html>&\"}"},
	}
	encoder := newPromptPayloadEncoder()
	for _, message := range messages {
		want, err := json.Marshal(message)
		require.NoError(t, err)
		got, err := encoder.encode(message)
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "reusable encoder must preserve Marshal bytes")
	}
}

func BenchmarkPromptProjectionMarshal(b *testing.B) {
	messages := make([]types.Message, 64)
	for index := range messages {
		messages[index] = *types.NewUserMessage("prompt projection benchmark message")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for index := range messages {
			if _, err := json.Marshal(messages[index]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkPromptProjectionReusableEncoder(b *testing.B) {
	messages := make([]types.Message, 64)
	for index := range messages {
		messages[index] = *types.NewUserMessage("prompt projection benchmark message")
	}
	encoder := newPromptPayloadEncoder()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for index := range messages {
			if _, err := encoder.encode(messages[index]); err != nil {
				b.Fatal(err)
			}
		}
	}
}
