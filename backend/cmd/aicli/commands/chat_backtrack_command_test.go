package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestParseChatBacktrackArgsPreview(t *testing.T) {
	req, apply, err := parseChatBacktrackArgs(`1 --both --edit "hello world"`)
	require.NoError(t, err)
	require.False(t, apply)
	require.NotNil(t, req.UserTurnIndex)
	require.Equal(t, 1, *req.UserTurnIndex)
	require.Equal(t, runtimechat.BacktrackModeBoth, req.Mode)
	require.Equal(t, "hello world", req.EditPrompt)
	require.True(t, req.PreviewOnly)
}

func TestParseChatBacktrackArgsApplySubmit(t *testing.T) {
	req, apply, err := parseChatBacktrackArgs("0 --apply --submit")
	require.NoError(t, err)
	require.True(t, apply)
	require.True(t, req.AutoSubmit)
	require.False(t, req.PreviewOnly)
	require.Equal(t, runtimechat.BacktrackModeConversation, req.Mode)
}

func TestParseChatBacktrackArgsRejectsUnknown(t *testing.T) {
	_, _, err := parseChatBacktrackArgs("1 --nope")
	require.Error(t, err)
}

func TestTokenizeBacktrackArgsQuotes(t *testing.T) {
	tokens := tokenizeBacktrackArgs(`2 --edit "a b c" --apply`)
	require.Equal(t, []string{"2", "--edit", "a b c", "--apply"}, tokens)
}

func TestBuildBacktrackFullScreenItems(t *testing.T) {
	turns := []runtimechat.UserTurn{
		{
			Index:            0,
			MessageIndex:     1,
			Preview:          "first prompt",
			MessageID:        "msg_aaa",
			HasLaterMutation: true,
			BaseCheckpointID: "chk_base_1",
		},
		{
			Index:        1,
			MessageIndex: 3,
			Preview:      "second prompt with more text to truncate in the title column",
			MessageID:    "msg_bbb",
		},
	}
	items := buildBacktrackFullScreenItems(turns)
	require.Len(t, items, 2)
	require.Contains(t, items[0].Title, "[0]")
	require.Contains(t, items[0].Title, "first prompt")
	require.Contains(t, items[0].Detail, "has_later_mutation")
	require.Contains(t, items[0].Preview, "message_id: msg_aaa")
	require.Contains(t, items[1].SearchText, "second prompt")
	require.Contains(t, formatBacktrackPickerSubtitle(2), "共 2 个 user turn")
}

func TestHandleInteractiveBacktrackSelectNonInteractive(t *testing.T) {
	session := &ChatSession{NoInteractive: true}
	// Should not panic; returns false without opening a picker.
	require.False(t, handleInteractiveBacktrackSelect(session))
}

func TestHandleBacktrackAuditListMissingRuntime(t *testing.T) {
	session := &ChatSession{}
	// No runtime session / host: returns false without panic.
	require.False(t, handleBacktrackAuditList(session))
}

func TestPrintBacktrackTombstoneFormatsSummary(t *testing.T) {
	// Ensure printer accepts zero-time and short IDs without panicking.
	printBacktrackTombstone(0, runtimechat.BacktrackTombstone{
		ID:                      "bt_deadbeefcafe",
		Mode:                    runtimechat.BacktrackModeConversation,
		UserTurnIndex:           1,
		MessageIndex:            2,
		RemovedMessageCount:     2,
		RemovedUserTurns:        1,
		TruncatedToMessageCount: 2,
		AnchorPreview:           "second prompt",
		BaseCheckpointID:        "chk_base_123456",
	})
}

// TestReplayVisibleChatHistoryAfterTruncation guards the fix for "回退后界面上
// 仍保留旧消息": after a backtrack apply, the truncated canonical history must be
// replayed into the transcript (resume convention), and removed turns must not
// appear in the replay output.
func TestReplayVisibleChatHistoryAfterTruncation(t *testing.T) {
	session := &ChatSession{SystemPromptText: "Profile system prompt."}
	// Simulate post-backtrack CLI state: durable + CLI history already truncated
	// to the anchor turn (system prefix + anchor user message + its reply). The
	// removed turns only exist above the truncation point and must not replay.
	require.NoError(t, replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("Profile system prompt."),
		*runtimetypes.NewUserMessage("锚点问题"),
		*runtimetypes.NewAssistantMessage("锚点回答"),
	}))

	var output string
	output = captureStdout(t, func() {
		count := replayVisibleChatHistoryAfterTruncation(session, "已回退到 user turn 1")
		if count != 2 {
			t.Fatalf("expected 2 replayed visible messages, got %d", count)
		}
	})

	for _, expected := range []string{"已回退到 user turn 1", "锚点问题", "锚点回答"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected replay output to contain %q, got:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "被移除的旧消息") {
		t.Fatalf("replay leaked removed-turn content:\n%s", output)
	}
}

func TestReplayVisibleChatHistoryAfterTruncationSkipsSystemOnly(t *testing.T) {
	session := &ChatSession{SystemPromptText: "Profile system prompt."}
	require.NoError(t, replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("Profile system prompt."),
	}))
	output := captureStdout(t, func() {
		if count := replayVisibleChatHistoryAfterTruncation(session, "已回退到 user turn 0"); count != 0 {
			t.Fatalf("expected 0 replayed messages for system-only history, got %d", count)
		}
	})
	if strings.TrimSpace(output) != "" {
		t.Fatalf("expected empty output for system-only history, got:\n%s", output)
	}
}
