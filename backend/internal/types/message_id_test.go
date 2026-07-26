package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureMessageIdentityAssignsMessageAndTurnForUser(t *testing.T) {
	msg := NewUserMessage("hello")
	require.True(t, EnsureMessageIdentity(msg, ""))
	require.True(t, strings.HasPrefix(MessageID(*msg), "msg_"))
	require.True(t, strings.HasPrefix(TurnID(*msg), "turn_"))
	// Idempotent
	require.False(t, EnsureMessageIdentity(msg, "other_turn"))
	require.NotEqual(t, "other_turn", TurnID(*msg))
}

func TestEnsureMessageIdentityInheritsTurnForAssistant(t *testing.T) {
	msg := NewAssistantMessage("reply")
	require.True(t, EnsureMessageIdentity(msg, "turn_parent"))
	require.True(t, strings.HasPrefix(MessageID(*msg), "msg_"))
	require.Equal(t, "turn_parent", TurnID(*msg))
}

func TestEnsureMessageIdentityPromotesLegacyID(t *testing.T) {
	msg := NewUserMessage("legacy")
	msg.Metadata.Set("id", "legacy-msg-1")
	require.True(t, EnsureMessageIdentity(msg, ""))
	require.Equal(t, "legacy-msg-1", MessageID(*msg))
	require.Equal(t, "legacy-msg-1", msg.Metadata.GetString(MetadataKeyMessageID, ""))
}

func TestEnsureHistoryMessageIdentitiesChainsTurns(t *testing.T) {
	history := []Message{
		*NewUserMessage("u1"),
		*NewAssistantMessage("a1"),
		*NewToolMessage("tc1", "tool out"),
		*NewUserMessage("u2"),
		*NewAssistantMessage("a2"),
	}
	require.True(t, EnsureHistoryMessageIdentities(history))

	u1Turn := TurnID(history[0])
	require.NotEmpty(t, u1Turn)
	require.Equal(t, u1Turn, TurnID(history[1]))
	require.Equal(t, u1Turn, TurnID(history[2]))

	u2Turn := TurnID(history[3])
	require.NotEmpty(t, u2Turn)
	require.NotEqual(t, u1Turn, u2Turn)
	require.Equal(t, u2Turn, TurnID(history[4]))

	ids := map[string]struct{}{}
	for _, msg := range history {
		id := MessageID(msg)
		require.NotEmpty(t, id)
		_, exists := ids[id]
		require.False(t, exists, "message ids must be unique")
		ids[id] = struct{}{}
	}

	require.False(t, EnsureHistoryMessageIdentities(history))
}
