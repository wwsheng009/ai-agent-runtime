package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
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

func TestBacktrackPickerCommandResultHasNoTranscriptDocument(t *testing.T) {
	result := newBacktrackPickerCommandResult()
	if result.OpenBacktrackPicker == nil {
		t.Fatal("backtrack picker result is missing its typed effect")
	}
	if result.Action != CommandContinue {
		t.Fatalf("backtrack picker action=%v want CommandContinue", result.Action)
	}
	if got := result.Document(); len(got.Blocks) != 0 {
		t.Fatalf("backtrack picker unexpectedly committed a transcript document: %#v", got)
	}
}

func TestStructuredBacktrackApplyCarriesTypedMutationEffect(t *testing.T) {
	result, handled := executeStructuredBacktrackQueryCommand(&ChatSession{}, `/backtrack 3 --both --edit "revised" --submit`)
	if !handled {
		t.Fatal("direct backtrack apply was not claimed by the structured dispatcher")
	}
	if result.ApplyBacktrack == nil {
		t.Fatal("direct backtrack apply is missing its typed mutation effect")
	}
	if got := result.Document(); len(got.Blocks) != 0 {
		t.Fatalf("backtrack apply unexpectedly committed a pre-mutation document: %#v", got)
	}
	request := result.ApplyBacktrack.Request
	if request.UserTurnIndex == nil || *request.UserTurnIndex != 3 {
		t.Fatalf("typed request user turn = %#v, want 3", request.UserTurnIndex)
	}
	if request.Mode != runtimechat.BacktrackModeBoth || request.EditPrompt != "revised" || !request.AutoSubmit || request.PreviewOnly {
		t.Fatalf("typed request = %#v, want both/revised/submit/apply", request)
	}
}

func TestStructuredRewindApplyRemainsFenced(t *testing.T) {
	if _, handled := executeStructuredBacktrackQueryCommand(&ChatSession{}, "/rewind 3 --apply"); handled {
		t.Fatal("numeric /rewind unexpectedly entered backtrack apply transaction")
	}
}

func TestApplySelectedBacktrackReplacesUnifiedCanonicalTranscript(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ctx := context.Background()
	storage := runtimechat.NewInMemoryStorage()
	persisted := runtimechat.NewSession("backtrack-apply-user")
	persisted.ID = "unified-backtrack-apply"
	persisted.AddMessage(*runtimetypes.NewUserMessage("surviving prompt"))
	persisted.AddMessage(*runtimetypes.NewAssistantMessage("surviving answer"))
	persisted.AddMessage(*runtimetypes.NewUserMessage("removed prompt"))
	persisted.AddMessage(*runtimetypes.NewAssistantMessage("removed answer"))
	if err := storage.Save(ctx, persisted); err != nil {
		t.Fatalf("save backtrack fixture: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	hub := runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        runtimeagent.NewAgent(&runtimeagent.Config{Name: "backtrack-apply"}, nil),
			SessionStore: storage,
			StateStore:   runtimechat.NewInMemoryRuntimeStore(8),
		})
	})
	t.Cleanup(hub.StopAll)

	session := &ChatSession{
		SessionManager:   manager,
		RuntimeSession:   persisted,
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}
	if err := replaceRuntimeMessages(session, persisted.GetMessages()); err != nil {
		t.Fatalf("seed canonical messages: %v", err)
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 30)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	printVisibleChatHistory(session, "")
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	actor, err := chatActorForSession(ctx, session)
	if err != nil {
		t.Fatalf("get backtrack actor: %v", err)
	}
	turns, err := actor.ListTurns(ctx)
	if err != nil || len(turns) != 2 {
		t.Fatalf("list turns = (%#v, %v), want two turns", turns, err)
	}
	applySelectedBacktrack(session, actor, ctx, turns[1], runtimechat.BacktrackModeConversation)
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)

	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	got := transcript.String()
	for _, want := range []string{
		"已回退到 user turn 1：上方旧消息已失效",
		"surviving prompt",
		"backtrack apply: turn=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unified backtrack transcript is missing %q:\n%s", want, got)
		}
	}
	for _, cell := range state.Transcript.Cells {
		if strings.Contains(cell.Source, "backtrack apply:") {
			continue
		}
		for _, removed := range []string{"removed prompt", "removed answer"} {
			if strings.Contains(cell.Source, removed) {
				t.Fatalf("truncated content remained in canonical Scene cell as %q: %#v", removed, cell)
			}
		}
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified backtrack populated legacy historyWindow: %#v", got)
	}
	if !strings.Contains(terminal.String(), "backtrack apply: turn=1") {
		t.Fatalf("TerminalSession did not render backtrack result: %q", terminal.String())
	}
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

func TestHandleInteractiveBacktrackSelectUnifiedTeardownFailsClosed(t *testing.T) {
	session := &ChatSession{TerminalSession: ui.NewTerminalSession(&bytes.Buffer{})}
	raw := captureStdout(t, func() {
		require.False(t, handleInteractiveBacktrackSelect(session))
	})
	if raw != "" {
		t.Fatalf("unified backtrack shortcut revived legacy stdout: %q", raw)
	}
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
