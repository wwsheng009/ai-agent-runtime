package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Live-fire regression (2026-10-17, second round): the cut condition compared
// index against len(previous), but the rebuilt window interleaves
// request-scoped developer rows between turns, so a new user turn could sit at
// an index still below len(previous) and the inheritance ran past it — the new
// turn's tool result (byte-identical to the previous turn's, same query, same
// response pool) inherited the OLD id and the canonical transcript dropped the
// whole turn again. The boundary is now the first user message whose substance
// the previous transcript cannot back.
func TestInheritBeforeNewTurnsCutsAtUnbackedUserTurn(t *testing.T) {
	toolPayload := "identical tool payload"
	previous := []Message{
		*withID(NewUserMessage("search once"), "msg_u1", "turn_1"),
		*withID(&Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "web_search"}}}, "msg_a1", "turn_1"),
		*withID(NewToolMessage("c1", toolPayload), "msg_t1", "turn_1"),
		*withID(NewAssistantMessage("answer"), "msg_f1", "turn_1"),
	}
	// Rebuilt window: previous rows + developer request-scoped layer + NEW turn
	// (user2, dev, assistant-with-tool-call, tool result repeating turn 1's
	// payload verbatim, final answer).
	assistantToolCall := &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c2", Name: "web_search"}}}
	toolRepeat := NewToolMessage("c2", toolPayload)
	rebuilt := []Message{
		*NewUserMessage("search once"),
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "web_search"}}},
		*NewToolMessage("c1", toolPayload),
		*NewAssistantMessage("answer"),
		*NewUserMessage("search once again"),        // new user turn at index 4 < len(previous)
		{Role: "developer", Content: "fact ledger"}, // request-scoped layer
		*assistantToolCall,
		*toolRepeat, // byte-identical to turn 1's tool result
		*NewAssistantMessage("answer 2"),
	}

	changed := InheritMessageIdentitiesBeforeNewTurns(previous, rebuilt)
	require.True(t, changed)
	for index := 4; index < len(rebuilt); index++ {
		require.Empty(t, MessageID(rebuilt[index]),
			"messages after the new user turn must mint fresh identities, inherited index=%d role=%s", index, rebuilt[index].Role)
	}
	// The retained prefix still inherits.
	require.NotEmpty(t, MessageID(rebuilt[1]))
	require.Equal(t, "turn_1", TurnID(rebuilt[3]))
}

func withID(message *Message, messageID, turnID string) *Message {
	if message.Metadata == nil {
		message.Metadata = NewMetadata()
	}
	message.Metadata.Set(MetadataKeyMessageID, messageID)
	message.Metadata.Set(MetadataKeyTurnID, turnID)
	return message
}
