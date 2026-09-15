package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInheritMessageIdentitiesRestoresRebuiltTranscript(t *testing.T) {
	previous := []Message{
		*NewUserMessage("检查为什么写入侧会有重复项"),
		*NewAssistantMessage("先看写入链路"),
		*NewToolMessage("call-1", "grep 输出"),
	}
	require.True(t, EnsureHistoryMessageIdentities(previous))

	rebuilt := []Message{
		*NewUserMessage("检查为什么写入侧会有重复项"),
		*NewAssistantMessage("先看写入链路"),
		*NewToolMessage("call-1", "grep 输出"),
	}
	require.True(t, InheritMessageIdentities(previous, rebuilt, false))
	require.Equal(t, MessageID(previous[1]), MessageID(rebuilt[1]))
	require.Equal(t, TurnID(previous[1]), TurnID(rebuilt[1]))
	require.Equal(t, MessageID(previous[2]), MessageID(rebuilt[2]))
	require.Equal(t, TurnID(previous[2]), TurnID(rebuilt[2]))
	// A user message opens a new turn: its identity is deliberately not reused,
	// so a genuine repeat of the same text still becomes a new durable turn.
	require.NotEqual(t, MessageID(previous[0]), MessageID(rebuilt[0]))
}

func TestInheritMessageIdentitiesStopsAtSurplusRebuilds(t *testing.T) {
	previous := []Message{*NewAssistantMessage("same"), *NewAssistantMessage("same")}
	require.True(t, EnsureHistoryMessageIdentities(previous))

	rebuilt := []Message{
		*NewAssistantMessage("same"),
		*NewAssistantMessage("same"),
		*NewAssistantMessage("same"),
	}
	require.True(t, InheritMessageIdentities(previous, rebuilt, false))
	require.Equal(t, MessageID(previous[0]), MessageID(rebuilt[0]))
	require.Equal(t, MessageID(previous[1]), MessageID(rebuilt[1]))
	require.Empty(t, MessageID(rebuilt[2]), "a surplus rebuild must stay unassigned and append as a new row")
}

func TestInheritMessageIdentitiesKeepsExistingIdentity(t *testing.T) {
	previous := []Message{*NewAssistantMessage("hi")}
	require.True(t, EnsureHistoryMessageIdentities(previous))

	rebuilt := []Message{*NewAssistantMessage("hi")}
	rebuilt[0].Metadata.Set(MetadataKeyMessageID, "msg_manual")
	require.False(t, InheritMessageIdentities(previous, rebuilt, false))
	require.Equal(t, "msg_manual", MessageID(rebuilt[0]))
}

func TestMessageSubstanceKeyMirrorsCanonicalKey(t *testing.T) {
	message := *NewAssistantMessage("output")
	message.ToolCalls = []ToolCall{{ID: "call-1", Name: "grep"}}
	require.Equal(t, "assistant\x00output\x00call-1\x1fgrep\n", MessageSubstanceKey(message))
	// A tool result row keys on its call id.
	require.Equal(t, "tool\x00output\x00", MessageSubstanceKey(*NewToolMessage("call-1", "output")))
	// Role casing and padding are normed away, content is not.
	padded := *NewUserMessage("Hello")
	padded.Role = "  USER "
	require.Equal(t, "user\x00Hello\x00", MessageSubstanceKey(padded))
	require.NotEqual(t, MessageSubstanceKey(padded), MessageSubstanceKey(*NewUserMessage("hello")))
}
