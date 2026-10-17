package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P1 INV-1 (session-message-identity-protocol-20261017): identities are granted
// at creation. Identical content must never share a message id, and every
// granted row carries the run's turn id.
func TestBuilderIdentityGrantsUniqueIDsForIdenticalContent(t *testing.T) {
	identity := NewBuilderIdentity("turn_run1")
	builder := NewMessageBuilderWithIdentity(nil, identity)
	calls := []types.ToolCall{{ID: "call_a", Name: "team_echo"}}

	builder.AppendAssistantAction("turn one tool call", calls, nil, nil)
	builder.AppendToolResults(calls, []ToolResultPayload{{ToolCallID: "call_a", Content: "ok"}})
	builder.AppendAssistantAction("turn one tool call", calls, nil, nil) // identical content
	builder.AppendToolResults(calls, []ToolResultPayload{{ToolCallID: "call_a", Content: "ok"}})

	messages := builder.Messages()
	var ids []string
	for _, message := range messages {
		id := types.MessageID(message)
		require.NotEmpty(t, id, "every created message must carry a granted message_id")
		require.Equal(t, "turn_run1", types.TurnID(message), "every created message must carry the run turn_id")
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			require.NotEqual(t, ids[i], ids[j],
				"identical content must never share a message_id (rows %d/%d)", i, j)
		}
	}
}

// nil identity keeps the historical behavior exactly: no ids minted, no turn
// stamped (ReplaceHistory's inherit layer remains the fallback until P3/P4).
func TestBuilderWithoutIdentityKeepsHistoricalBehavior(t *testing.T) {
	builder := NewMessageBuilder(nil)
	builder.AppendAssistantAction("hello", nil, nil, nil)
	builder.AppendToolResults(nil, nil)

	messages := builder.Messages()
	require.Len(t, messages, 1)
	require.Empty(t, types.MessageID(messages[0]))
	require.Empty(t, types.TurnID(messages[0]))
}

// Rows that already carry an identity keep it (replay/inherit scenarios must
// not be rewritten by the grant layer).
func TestBuilderIdentityKeepsExistingIDs(t *testing.T) {
	identity := NewBuilderIdentity("turn_run1")
	stamped := types.NewAssistantMessage("carry me")
	stamped.Metadata = types.NewMetadata()
	stamped.Metadata.Set(types.MetadataKeyMessageID, "msg_manual")
	stamped.Metadata.Set(types.MetadataKeyTurnID, "turn_manual")

	builder := NewMessageBuilderWithIdentity(nil, identity)
	builder.Add(*stamped)
	messages := builder.Messages()
	require.Len(t, messages, 1)
	require.Equal(t, "msg_manual", types.MessageID(messages[0]))
	require.Equal(t, "turn_manual", types.TurnID(messages[0]))
}
