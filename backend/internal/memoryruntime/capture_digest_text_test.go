package memoryruntime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptureDigestTextRedactsRolesAndBoundsEachRole(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 60)
	transcript := Transcript{Messages: []TranscriptMessage{
		{Seq: 1, Role: "user", Content: "keep this " + secret},
		{Seq: 2, Role: "tool", Content: "tool output must not be stored"},
		{Seq: 3, Role: "assistant", Content: "durable assistant answer"},
		{Seq: 4, Role: "USER", Content: "second user message"},
	}}

	text, err := captureDigestText(transcript, Config{
		TurnDigestStoreText:         true,
		TurnDigestMaxUserChars:      12,
		TurnDigestMaxAssistantChars: 10,
	})
	require.NoError(t, err)
	require.NotContains(t, text.UserText, secret)
	require.NotContains(t, text.UserText, "tool output")
	require.NotContains(t, text.AssistantText, "tool output")
	require.NotEmpty(t, text.UserText)
	require.NotEmpty(t, text.AssistantText)
	require.True(t, text.TextTruncated)
}

func TestCaptureDigestTextDisabledDoesNotProjectTranscript(t *testing.T) {
	text, err := captureDigestText(Transcript{Messages: []TranscriptMessage{
		{Role: "user", Content: "must not be persisted"},
		{Role: "assistant", Content: "must not be persisted either"},
	}}, Config{TurnDigestStoreText: false})
	require.NoError(t, err)
	require.Empty(t, text.UserText)
	require.Empty(t, text.AssistantText)
	require.False(t, text.TextTruncated)
}
