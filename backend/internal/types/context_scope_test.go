package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsRequestScopedContextMessage(t *testing.T) {
	cases := []struct {
		name    string
		message Message
		want    bool
	}{
		{name: "plain user turn", message: *NewUserMessage("检查为什么写入侧会有重复项"), want: false},
		{name: "plain assistant turn", message: *NewAssistantMessage("先看写入链路"), want: false},
		{name: "message without metadata", message: Message{Role: "user", Content: "x"}, want: false},
		{name: "context snapshot", message: Message{Metadata: Metadata{MetadataKeyContextSnapshot: true}}, want: true},
		{name: "transient prompt", message: Message{Metadata: Metadata{MetadataKeyTransientPrompt: true}}, want: true},
		{name: "staged layer", message: Message{Metadata: Metadata{MetadataKeyContextStage: "fact_ledger"}}, want: true},
		{
			name:    "staged layer with padding and mixed case",
			message: Message{Metadata: Metadata{MetadataKeyContextStage: "  Recall "}},
			want:    true,
		},
		{
			name:    "compaction summary stays durable",
			message: Message{Metadata: Metadata{MetadataKeyContextStage: "compaction"}},
			want:    false,
		},
		{
			name:    "unknown stage stays durable",
			message: Message{Metadata: Metadata{MetadataKeyContextStage: "user_note"}},
			want:    false,
		},
		{
			name:    "snapshot flag off is not a marker",
			message: Message{Metadata: Metadata{MetadataKeyContextSnapshot: false, MetadataKeyContextStage: "compaction"}},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsRequestScopedContextMessage(tc.message))
		})
	}
}

func TestMarkRequestScopedContext(t *testing.T) {
	message := NewUserMessage("continue the parent turn")
	MarkRequestScopedContext(message, "request_prefix")
	require.Equal(t, "request_prefix", message.Metadata.GetString(MetadataKeyContextStage, ""))
	require.True(t, message.Metadata.GetBool(MetadataKeyContextSnapshot, false))
	require.True(t, IsRequestScopedContextMessage(*message))

	bare := &Message{}
	MarkRequestScopedContext(bare, "")
	require.True(t, bare.Metadata.GetBool(MetadataKeyContextSnapshot, false))
	_, staged := bare.Metadata[MetadataKeyContextStage]
	require.False(t, staged, "an empty stage must not be written")

	MarkRequestScopedContext(nil, "workspace")
}
