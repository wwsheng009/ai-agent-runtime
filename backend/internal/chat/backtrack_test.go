package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestListUserTurnsIndexesVisibleHistory(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewToolMessage("tool_1", "tool result"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
		*types.NewUserMessage("third"),
	}

	turns := ListUserTurns(messages)
	require.Len(t, turns, 3)
	require.Equal(t, 0, turns[0].Index)
	require.Equal(t, 0, turns[0].MessageIndex)
	require.Equal(t, 3, turns[0].EndMessageIndex)
	require.Equal(t, "first", turns[0].Preview)

	require.Equal(t, 1, turns[1].Index)
	require.Equal(t, 3, turns[1].MessageIndex)
	require.Equal(t, 5, turns[1].EndMessageIndex)

	require.Equal(t, 2, turns[2].Index)
	require.Equal(t, 5, turns[2].MessageIndex)
	require.Equal(t, 6, turns[2].EndMessageIndex)
}

func TestPlanBacktrackByMessageID(t *testing.T) {
	first := types.NewUserMessage("first")
	second := types.NewUserMessage("second")
	_ = types.EnsureMessageIdentity(first, "")
	_ = types.EnsureMessageIdentity(second, "")
	messages := []types.Message{
		*first,
		*types.NewAssistantMessage("a1"),
		*second,
		*types.NewAssistantMessage("a2"),
	}

	plan, err := planBacktrack(messages, BacktrackRequest{MessageID: types.MessageID(*second)})
	require.NoError(t, err)
	require.Equal(t, 1, plan.UserTurnIndex)
	require.Equal(t, 2, plan.MessageIndex)
	require.Equal(t, types.MessageID(*second), plan.MessageID)
	require.Equal(t, 2, plan.PrefixLen)
}

func TestListUserTurnsExposesMessageAndTurnIDs(t *testing.T) {
	first := types.NewUserMessage("first")
	second := types.NewUserMessage("second")
	_ = types.EnsureMessageIdentity(first, "")
	_ = types.EnsureMessageIdentity(second, "")
	messages := []types.Message{
		*first,
		*types.NewAssistantMessage("a1"),
		*second,
	}
	turns := ListUserTurns(messages)
	require.Len(t, turns, 2)
	require.Equal(t, types.MessageID(*first), turns[0].MessageID)
	require.Equal(t, types.TurnID(*first), turns[0].TurnID)
	require.Equal(t, types.MessageID(*second), turns[1].MessageID)
	require.Equal(t, types.TurnID(*second), turns[1].TurnID)
	require.NotEqual(t, turns[0].TurnID, turns[1].TurnID)
}

func TestPlanBacktrackFallsBackWhenMessageIDMissingButIndexPresent(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	}
	plan, err := planBacktrack(messages, BacktrackRequest{
		MessageID:     "session-history-2", // synthetic / unknown id
		UserTurnIndex: intPtr(1),
		MessageIndex:  intPtr(2),
	})
	require.NoError(t, err)
	require.Equal(t, 1, plan.UserTurnIndex)
	require.Equal(t, 2, plan.MessageIndex)
}

func TestListUserTurnsAnnotatesCheckpoints(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	}
	checkpoints := []artifact.Checkpoint{
		{ID: "chk_before", MessageCount: 1},
		{ID: "chk_after", MessageCount: 3},
	}
	turns := ListUserTurns(messages, checkpoints)
	require.Len(t, turns, 2)
	// turn0 MessageIndex=0: no checkpoint with MessageCount <= 0
	require.Empty(t, turns[0].BaseCheckpointID)
	require.True(t, turns[0].HasLaterMutation)
	require.Equal(t, []string{"chk_before", "chk_after"}, turns[0].CheckpointIDs)
	// turn1 MessageIndex=2: chk_before(1) is base, chk_after(3) is later
	require.Equal(t, "chk_before", turns[1].BaseCheckpointID)
	require.True(t, turns[1].HasLaterMutation)
	require.Equal(t, []string{"chk_after"}, turns[1].CheckpointIDs)
}

func TestPlanBacktrackDefaultsDropAnchor(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
		*types.NewUserMessage("third"),
		*types.NewAssistantMessage("a3"),
	}

	plan, err := planBacktrack(messages, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.NoError(t, err)
	require.Equal(t, 1, plan.UserTurnIndex)
	require.Equal(t, 2, plan.MessageIndex)
	require.Equal(t, 2, plan.PrefixLen)
	require.Equal(t, 4, plan.RemovedMessageCount)
	require.Equal(t, 2, plan.RemovedUserTurns)
	require.Equal(t, "second", plan.ComposerPrompt)
	require.False(t, plan.IncludeAnchor)
	require.Len(t, plan.Prefix, 2)
	require.Equal(t, "first", plan.Prefix[0].Content)
	require.Equal(t, "a1", plan.Prefix[1].Content)
}

func TestPlanBacktrackIncludeAnchor(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	}

	plan, err := planBacktrack(messages, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		IncludeAnchor: true,
	})
	require.NoError(t, err)
	require.Equal(t, 3, plan.PrefixLen)
	require.True(t, plan.IncludeAnchor)
	require.Equal(t, 1, plan.RemovedMessageCount)
	require.Equal(t, 0, plan.RemovedUserTurns)
	require.Equal(t, "second", plan.Prefix[2].Content)
}

func TestPlanBacktrackEditPromptIgnoresIncludeAnchor(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	}

	plan, err := planBacktrack(messages, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		IncludeAnchor: true,
		EditPrompt:    "second edited",
	})
	require.NoError(t, err)
	require.False(t, plan.IncludeAnchor)
	require.Equal(t, 2, plan.PrefixLen)
	require.Equal(t, "second edited", plan.ComposerPrompt)
	require.Equal(t, "second edited", plan.EditedPrompt)
}

func TestPlanBacktrackByMessageIndex(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	}
	plan, err := planBacktrack(messages, BacktrackRequest{MessageIndex: intPtr(2)})
	require.NoError(t, err)
	require.Equal(t, 1, plan.UserTurnIndex)
	require.Equal(t, 2, plan.MessageIndex)
}

func TestPlanBacktrackRejectsNonUserMessageIndex(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
	}
	_, err := planBacktrack(messages, BacktrackRequest{MessageIndex: intPtr(1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "message_index")
}

func newBacktrackTestActor(t *testing.T, history []types.Message) (*SessionActor, SessionStorage, *Session) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "backtrack-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	for _, msg := range history {
		session.AddMessage(msg)
	}
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-backtrack-test",
		Model:        "gpt-4",
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(func() { actor.Stop() })
	return actor, storage, session
}

func TestSessionActorBacktrackMiddleUserTurn(t *testing.T) {
	ctx := context.Background()
	actor, storage, session := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
		*types.NewUserMessage("third"),
		*types.NewAssistantMessage("a3"),
	})

	result, submit, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeConversation,
	})
	require.NoError(t, err)
	require.Nil(t, submit)
	require.NotNil(t, result)
	require.Equal(t, 2, result.TruncatedToMessageCount)
	require.Equal(t, 4, result.RemovedMessageCount)
	require.Equal(t, 2, result.RemovedUserTurns)
	require.Equal(t, "second", result.ComposerPrompt)
	require.Contains(t, result.EventsEmitted, EventBacktrackStarted)
	require.Contains(t, result.EventsEmitted, EventBacktrackFinished)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "a1", messages[1].Content)
	require.Zero(t, updated.HeadOffset)

	state := actor.State()
	require.NotNil(t, state)
	require.Equal(t, SessionIdle, state.Status)
	require.Empty(t, state.CurrentTurnID)
	require.Nil(t, state.PendingApproval)
	require.Nil(t, state.PendingTool)
	require.Nil(t, state.PendingQuestion)
}

func TestSessionActorBacktrackFirstUserTurn(t *testing.T) {
	ctx := context.Background()
	actor, storage, session := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	})

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{UserTurnIndex: intPtr(0)})
	require.NoError(t, err)
	require.Equal(t, 0, result.TruncatedToMessageCount)
	require.Equal(t, 3, result.RemovedMessageCount)
	require.Equal(t, "first", result.ComposerPrompt)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, updated.GetMessages())
}

func TestSessionActorBacktrackLastUserTurnDropsTrailingAssistant(t *testing.T) {
	ctx := context.Background()
	actor, storage, session := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
		*types.NewToolMessage("tool_1", "tool-output"),
	})

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.NoError(t, err)
	require.Equal(t, 2, result.TruncatedToMessageCount)
	require.Equal(t, 3, result.RemovedMessageCount)
	require.Equal(t, 1, result.RemovedUserTurns)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "a1", messages[1].Content)
}

func TestSessionActorBacktrackEditPromptWithoutAutoSubmit(t *testing.T) {
	ctx := context.Background()
	actor, storage, session := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})

	result, submit, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		EditPrompt:    "second rewritten",
	})
	require.NoError(t, err)
	require.Nil(t, submit)
	require.Equal(t, "second rewritten", result.EditedPrompt)
	require.Equal(t, "second rewritten", result.ComposerPrompt)
	require.False(t, result.AutoSubmitted)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "a1", messages[1].Content)
}

func TestSessionActorBacktrackEditPromptWithAutoSubmit(t *testing.T) {
	ctx := context.Background()
	actor, storage, session := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})

	result, submit, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		EditPrompt:    "second rewritten",
		AutoSubmit:    true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.AutoSubmitted)
	require.NotNil(t, submit)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	messages := updated.GetMessages()
	require.GreaterOrEqual(t, len(messages), 3)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "a1", messages[1].Content)
	require.Equal(t, "user", messages[2].Role)
	require.Equal(t, "second rewritten", messages[2].Content)

	state := actor.State()
	require.NotNil(t, state)
	require.Equal(t, SessionIdle, state.Status)
}

func TestSessionActorPreviewBacktrackDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	actor, storage, session := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})

	preview, err := actor.PreviewBacktrack(ctx, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.NoError(t, err)
	require.True(t, preview.PreviewOnly)
	require.Equal(t, 2, preview.TruncatedToMessageCount)
	require.Equal(t, 2, preview.RemovedMessageCount)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.GetMessages(), 4)
}

func TestSessionActorBacktrackRejectsBusySession(t *testing.T) {
	ctx := context.Background()
	actor, _, _ := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	})

	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRunning
		state.CurrentTurnID = "turn_busy"
		state.UpdatedAt = time.Now().UTC()
		return nil
	}))

	_, _, err := actor.Backtrack(ctx, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "session is busy")
}

func TestSessionActorListTurns(t *testing.T) {
	ctx := context.Background()
	actor, _, _ := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	})

	turns, err := actor.ListTurns(ctx)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.Equal(t, "first", turns[0].Preview)
	require.Equal(t, "second", turns[1].Preview)
}

func TestSessionActorBacktrackClearsStaleRuntimeState(t *testing.T) {
	ctx := context.Background()
	actor, _, _ := newBacktrackTestActor(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	})

	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionIdle
		state.CurrentTurnID = "stale-turn"
		state.CurrentCheckpointID = "chk_stale"
		state.FrozenTurnToolsSet = true
		state.FrozenTurnTools = []types.ToolDefinition{{Name: "grep"}}
		state.PendingTool = &PendingToolInvocation{ToolCallID: "tool_1", ToolName: "grep"}
		state.PendingApproval = &ApprovalRequest{ID: "apr_1", ToolName: "grep"}
		state.PendingQuestion = &UserQuestionRequest{ID: "q_1", Prompt: "continue?"}
		state.UpdatedAt = time.Now().UTC()
		return nil
	}))

	_, _, err := actor.Backtrack(ctx, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.NoError(t, err)

	state := actor.State()
	require.NotNil(t, state)
	require.Equal(t, SessionIdle, state.Status)
	require.Empty(t, state.CurrentTurnID)
	require.False(t, state.FrozenTurnToolsSet)
	require.Empty(t, state.FrozenTurnTools)
	require.Nil(t, state.PendingTool)
	require.Nil(t, state.PendingApproval)
	require.Nil(t, state.PendingQuestion)
}

func TestPlanBacktrackModeBothMapsCheckpoints(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	}
	checkpoints := []artifact.Checkpoint{
		{ID: "chk_base", MessageCount: 1},
		{ID: "chk_later", MessageCount: 3},
	}

	result, prefix, err := PlanBacktrack("sess", messages, checkpoints, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeBoth,
	})
	require.NoError(t, err)
	require.Equal(t, BacktrackModeBoth, result.Mode)
	require.Equal(t, "chk_base", result.BaseCheckpointID)
	require.Equal(t, []string{"chk_later"}, result.LaterCheckpointIDs)
	require.Len(t, prefix, 2)
	require.Empty(t, result.Warnings)
}

func TestPlanBacktrackModeBothWarnsWithoutBaseCheckpoint(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
	}
	result, _, err := PlanBacktrack("sess", messages, nil, BacktrackRequest{
		UserTurnIndex: intPtr(0),
		Mode:          BacktrackModeBoth,
	})
	require.NoError(t, err)
	require.Empty(t, result.BaseCheckpointID)
	require.NotEmpty(t, result.Warnings)
	require.Contains(t, result.Warnings[0], "code restore skipped")
}

func TestPlanBacktrackModeBothLegacyMetadataMessageCount(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	}
	checkpoints := []artifact.Checkpoint{
		{
			ID: "chk_legacy_base",
			Metadata: map[string]interface{}{
				"message_count": float64(1),
			},
		},
		{
			ID: "chk_legacy_later",
			Metadata: map[string]interface{}{
				"message_count": "3",
			},
		},
	}

	result, _, err := PlanBacktrack("sess", messages, checkpoints, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeCode,
	})
	require.NoError(t, err)
	require.Equal(t, "chk_legacy_base", result.BaseCheckpointID)
	require.Equal(t, []string{"chk_legacy_later"}, result.LaterCheckpointIDs)
}

func TestSessionActorBacktrackBothRestoresLaterMutations(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))

	actor, storage, session, checkpointMgr := newBacktrackTestActorWithCheckpoints(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
		*types.NewUserMessage("third"),
		*types.NewAssistantMessage("a3"),
	})

	// Base checkpoint after first assistant reply (MessageCount=2 means visible prefix length 2).
	// Disk must already reflect post-mutation content when AfterMutation runs.
	require.NoError(t, os.WriteFile(path, []byte("mid"), 0o644))
	baseID, err := checkpointMgr.AfterMutation(ctx, &checkpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "write",
		ToolCallID:   "tool_base",
		MessageCount: 2,
		Conversation: []types.Message{
			*types.NewUserMessage("first"),
			*types.NewAssistantMessage("a1"),
		},
		Paths: []string{path},
		Snapshots: map[string]*checkpoint.FileSnapshot{
			path: {
				Path:         path,
				Op:           "update",
				Before:       "before",
				BeforeExists: true,
			},
		},
	}, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, baseID)

	// Later mutation after second turn.
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))
	laterID, err := checkpointMgr.AfterMutation(ctx, &checkpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "write",
		ToolCallID:   "tool_later",
		MessageCount: 4,
		Conversation: []types.Message{
			*types.NewUserMessage("first"),
			*types.NewAssistantMessage("a1"),
			*types.NewUserMessage("second"),
			*types.NewAssistantMessage("a2"),
		},
		Paths: []string{path},
		Snapshots: map[string]*checkpoint.FileSnapshot{
			path: {
				Path:         path,
				Op:           "update",
				Before:       "mid",
				BeforeExists: true,
			},
		},
	}, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, laterID)

	result, submit, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1), // message_index=2 → base=chk with count 2, later=chk with count 4
		Mode:          BacktrackModeBoth,
	})
	require.NoError(t, err)
	require.Nil(t, submit)
	require.NotNil(t, result)
	require.Equal(t, 2, result.TruncatedToMessageCount)
	require.Equal(t, baseID, result.BaseCheckpointID)
	require.Contains(t, result.LaterCheckpointIDs, laterID)
	require.NotNil(t, result.CodeRestore)
	require.Contains(t, result.CodeRestore.AppliedPaths, path)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "mid", string(content))

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "a1", messages[1].Content)

	state := actor.State()
	require.NotNil(t, state)
	require.Equal(t, SessionIdle, state.Status)
	require.Equal(t, baseID, state.CurrentCheckpointID)
}

func TestSessionActorBacktrackBothSkipsCodeWhenNoCheckpoint(t *testing.T) {
	ctx := context.Background()
	actor, storage, session, _ := newBacktrackTestActorWithCheckpoints(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	})

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeBoth,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.BaseCheckpointID)
	require.Nil(t, result.CodeRestore)
	require.True(t, hasWarningContaining(result.Warnings, "code restore skipped"))

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.GetMessages(), 2)
}

func TestSessionActorBacktrackCodeOnlyLeavesHistoryIntact(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "only-code.txt")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))

	actor, storage, session, checkpointMgr := newBacktrackTestActorWithCheckpoints(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})

	require.NoError(t, os.WriteFile(path, []byte("mid"), 0o644))
	baseID, err := checkpointMgr.AfterMutation(ctx, &checkpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "write",
		ToolCallID:   "tool_base",
		MessageCount: 2,
		Paths:        []string{path},
		Snapshots: map[string]*checkpoint.FileSnapshot{
			path: {
				Path:         path,
				Op:           "update",
				Before:       "before",
				BeforeExists: true,
			},
		},
	}, nil, "")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))
	_, err = checkpointMgr.AfterMutation(ctx, &checkpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "write",
		ToolCallID:   "tool_later",
		MessageCount: 4,
		Paths:        []string{path},
		Snapshots: map[string]*checkpoint.FileSnapshot{
			path: {
				Path:         path,
				Op:           "update",
				Before:       "mid",
				BeforeExists: true,
			},
		},
	}, nil, "")
	require.NoError(t, err)

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeCode,
	})
	require.NoError(t, err)
	require.Equal(t, baseID, result.BaseCheckpointID)
	require.NotNil(t, result.CodeRestore)
	require.Contains(t, result.CodeRestore.AppliedPaths, path)

	// code-only must not truncate conversation
	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.GetMessages(), 4)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "mid", string(content))
}

func newBacktrackTestActorWithCheckpoints(t *testing.T, history []types.Message) (*SessionActor, SessionStorage, *Session, *checkpoint.Manager) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "backtrack-code-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	for _, msg := range history {
		session.AddMessage(msg)
	}
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-backtrack-code-test",
		Model:        "gpt-4",
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)

	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })
	checkpointMgr := checkpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(func() { actor.Stop() })
	return actor, storage, session, checkpointMgr
}

func hasWarningContaining(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}
