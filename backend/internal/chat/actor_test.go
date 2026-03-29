package chat

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionActorSubmitPromptUpdatesSession(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	agentCfg := &agent.Config{
		Name:         "actor-test-agent",
		Model:        "gpt-4",
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}
	apiAgent := agent.NewAgentWithLLM(agentCfg, nil, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	result, err := actor.SubmitPrompt(ctx, "Hello there", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.GreaterOrEqual(t, len(updated.History), 1)
	require.Equal(t, "assistant", updated.History[len(updated.History)-1].Role)

	state := actor.State()
	require.NotNil(t, state)
	require.Equal(t, SessionIdle, state.Status)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, events)
}

func TestSessionActorStopReturnsWhenActorNeverStarted(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	session := NewSession("actor-user")
	require.NoError(t, storage.Save(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-stop-test",
		Model:        "gpt-4",
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   NewInMemoryRuntimeStore(8),
		EventStore:   NewInMemoryRuntimeStore(8),
	})
	require.NoError(t, err)

	stopped := make(chan struct{})
	go func() {
		actor.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out stopping actor before start")
	}
}

func TestSessionActorSubmitPrompt_RoutesMatchedSkillBeforeReAct(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-skill-test",
		Model:    "unused-model",
		MaxSteps: 2,
	}, nil, nil)
	require.NoError(t, apiAgent.RegisterSkill(&skill.Skill{
		Name:        "alpha_lookup",
		Description: "Alpha lookup",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"alpha"}, Weight: 1},
		},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return &types.Result{
				Success: true,
				Output:  "SKILL_RUNTIME_OK",
				Skill:   "alpha_lookup",
			}, nil
		}),
	}))

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	result, err := actor.SubmitPrompt(ctx, "please run alpha", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "SKILL_RUNTIME_OK", result.Output)
	require.Equal(t, "alpha_lookup", result.Skill)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.GreaterOrEqual(t, len(updated.History), 2)
	require.Equal(t, "assistant", updated.History[len(updated.History)-1].Role)
	require.Equal(t, "SKILL_RUNTIME_OK", updated.History[len(updated.History)-1].Content)
}

func TestSessionActorSubmitPrompt_BypassesMatchedSkillDuringTeamRun(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-team-skill-bypass-test",
		Model:    "gpt-4",
		MaxSteps: 2,
	}, nil, runtime)
	require.NoError(t, apiAgent.RegisterSkill(&skill.Skill{
		Name:        "alpha_lookup",
		Description: "Alpha lookup",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"alpha"}, Weight: 1},
		},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return &types.Result{
				Success: true,
				Output:  "SKILL_RUNTIME_OK",
				Skill:   "alpha_lookup",
			}, nil
		}),
	}))

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	result, err := actor.SubmitPrompt(ctx, "please run alpha", &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        "team-1",
			AgentID:       "mate-1",
			CurrentTaskID: "task-1",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.NotEqual(t, "alpha_lookup", result.Skill)
	require.NotEqual(t, "SKILL_RUNTIME_OK", result.Output)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.GreaterOrEqual(t, len(updated.History), 2)
	require.Equal(t, "assistant", updated.History[len(updated.History)-1].Role)
	require.NotEqual(t, "SKILL_RUNTIME_OK", updated.History[len(updated.History)-1].Content)
}

func TestSessionActorRewindConversationAppliesCheckpointManagerPlan(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("first"))
	session.AddMessage(*types.NewAssistantMessage("second"))
	session.AddMessage(*types.NewUserMessage("third"))
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-rewind-test",
		Model:        "gpt-4",
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)
	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })
	apiAgent.SetCheckpointManager(checkpoint.NewManager(artifactStore, nil))

	checkpointID, err := artifactStore.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID:    session.ID,
		Reason:       "tool:edit",
		MessageCount: 1,
		Metadata: map[string]interface{}{
			"message_count": 1,
		},
	})
	require.NoError(t, err)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor.RewindTo(ctx, checkpointID, "conversation"))

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Zero(t, updated.HeadOffset)
	require.Len(t, updated.GetMessages(), 1)
	require.Len(t, updated.History, 1)

	state := actor.State()
	require.NotNil(t, state)
	require.Zero(t, state.HeadOffset)
}

func TestSessionActorRewindConversationRestoresExactSnapshotWhenAvailable(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("first"))
	session.AddMessage(*types.NewAssistantMessage("second"))
	session.AddMessage(*types.NewUserMessage("third"))
	session.AddMessage(*types.NewAssistantMessage("fourth"))
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)
	_ = runtime.RegisterProviderAlias("gpt-4", mockProvider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-rewind-snapshot-test",
		Model:        "gpt-4",
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)
	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })
	checkpointMgr := checkpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &checkpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 2,
		Conversation: []types.Message{
			*types.NewUserMessage("first"),
			*types.NewAssistantMessage("second"),
		},
	}
	checkpointID, err := checkpointMgr.AfterMutation(ctx, pending, nil, "")
	require.NoError(t, err)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor.RewindTo(ctx, checkpointID, "conversation"))

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Zero(t, updated.HeadOffset)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "second", messages[1].Content)

	state := actor.State()
	require.NotNil(t, state)
	require.Zero(t, state.HeadOffset)
}

func TestSessionActorAnswerQuestionResumesWithoutInMemoryWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-question-resume-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-question-resume-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I need confirmation.",
				Model:   "test-question-resume-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_question",
						Name: toolbroker.ToolAskUserQuestion,
						Args: map[string]interface{}{
							"prompt":   "Need confirmation",
							"required": true,
						},
					},
				},
			},
			{
				Content: "Finished after resume.",
				Model:   "test-question-resume-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-question-resume-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor1, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := actor1.SubmitPrompt(ctx, "Start the flow.", nil)
		submitDone <- submitErr
	}()

	var questionID string
	require.Eventually(t, func() bool {
		state := actor1.State()
		if state == nil || state.PendingQuestion == nil || state.PendingTool == nil {
			return false
		}
		if state.Status != SessionWaitingInput {
			return false
		}
		if state.PendingTool.ToolCallID != "tool_question" {
			return false
		}
		questionID = state.PendingQuestion.ID
		return questionID != ""
	}, 5*time.Second, 20*time.Millisecond)

	pendingSession, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, pendingSession)
	require.Len(t, pendingSession.History, 2)
	require.True(t, pendingSession.History[1].HasToolCalls())
	require.Len(t, pendingSession.History[1].ToolCalls, 1)
	require.Equal(t, "tool_question", pendingSession.History[1].ToolCalls[0].ID)
	require.Equal(t, toolbroker.ToolAskUserQuestion, pendingSession.History[1].ToolCalls[0].Name)

	actor2, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor2.AnswerQuestion(context.Background(), questionID, "yes"))

	require.Eventually(t, func() bool {
		state := actor2.State()
		if state == nil || state.Status != SessionIdle || state.PendingQuestion != nil || state.PendingTool != nil {
			return false
		}
		updated, loadErr := storage.Load(context.Background(), session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		messages := updated.GetMessages()
		if len(messages) == 0 {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after resume."
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original actor submit did not exit after cancellation")
	}
}

func TestSessionActorApproveToolResumesWithoutInMemoryWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-approval-resume-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-approval-resume-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I need approval.",
				Model:   "test-approval-resume-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_approval",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello"},
					},
				},
			},
			{
				Content: "Finished after approval resume.",
				Model:   "test-approval-resume-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &runMetaCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-approval-resume-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)
	apiAgent.SetPermissionEngine(&agent.PermissionEngine{
		Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
			if req.ToolName == "team_echo" {
				return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
			}
			return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
		},
	})

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor1, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	runMeta := &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        "team-approval",
			AgentID:       "mate-approval",
			CurrentTaskID: "task-approval",
		},
	}

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := actor1.SubmitPrompt(ctx, "Start approval flow.", runMeta)
		submitDone <- submitErr
	}()

	var requestID string
	require.Eventually(t, func() bool {
		state := actor1.State()
		if state == nil || state.PendingApproval == nil || state.PendingTool == nil {
			return false
		}
		if state.Status != SessionWaitingApproval {
			return false
		}
		if state.PendingTool.ToolCallID != "tool_approval" {
			return false
		}
		requestID = state.PendingApproval.ID
		return requestID != ""
	}, 5*time.Second, 20*time.Millisecond)

	pendingSession, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, pendingSession)
	require.Len(t, pendingSession.History, 2)
	require.True(t, pendingSession.History[1].HasToolCalls())
	require.Len(t, pendingSession.History[1].ToolCalls, 1)
	require.Equal(t, "tool_approval", pendingSession.History[1].ToolCalls[0].ID)
	require.Equal(t, "team_echo", pendingSession.History[1].ToolCalls[0].Name)

	actor2, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor2.ApproveTool(context.Background(), requestID, true))

	require.Eventually(t, func() bool {
		state := actor2.State()
		if state == nil || state.Status != SessionIdle || state.PendingApproval != nil || state.PendingTool != nil {
			return false
		}
		updated, loadErr := storage.Load(context.Background(), session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		messages := updated.GetMessages()
		if len(messages) == 0 {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after approval resume."
	}, 5*time.Second, 20*time.Millisecond)

	require.Equal(t, 1, mcpManager.callCount)
	require.NotNil(t, mcpManager.lastMeta)
	require.NotNil(t, mcpManager.lastMeta.Team)
	require.Equal(t, "team-approval", mcpManager.lastMeta.Team.TeamID)
	require.Equal(t, "mate-approval", mcpManager.lastMeta.Team.AgentID)
	require.Equal(t, "task-approval", mcpManager.lastMeta.Team.CurrentTaskID)
	assertRuntimeEvent(t, runtimeStore, session.ID, EventToolReceiptRecorded, map[string]string{
		"tool_call_id": "tool_approval",
		"source":       "receipt_store",
	})

	cancel()
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original actor submit did not exit after cancellation")
	}
}

func TestSessionActorAnswerQuestionSelfHealsWhenToolResultAlreadyExists(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("Start the flow."))
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "tool_question",
		Name: toolbroker.ToolAskUserQuestion,
		Args: map[string]interface{}{"prompt": "Need confirmation", "required": true},
	}}
	session.AddMessage(*assistant)
	session.AddMessage(*types.NewToolMessage("tool_question", "Already answered"))
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-self-heal-question-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-self-heal-question-model",
		responses: []*llm.LLMResponse{
			{Content: "Finished after self-heal.", Model: "test-self-heal-question-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-self-heal-question-test",
		Model:        provider.Name(),
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionWaitingInput,
		CurrentTurnID: "turn_question_self_heal",
		PendingTool: &PendingToolInvocation{
			ToolCallID: "tool_question",
			ToolName:   toolbroker.ToolAskUserQuestion,
			ArgsJSON:   []byte(`{"prompt":"Need confirmation","required":true}`),
			CreatedAt:  time.Now().UTC(),
		},
		PendingQuestion: &UserQuestionRequest{
			ID:        "question_self_heal",
			SessionID: session.ID,
			Prompt:    "Need confirmation",
			Required:  true,
			CreatedAt: time.Now().UTC(),
		},
		UpdatedAt: time.Now().UTC(),
	}))

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor.AnswerQuestion(context.Background(), "question_self_heal", "yes"))

	require.Eventually(t, func() bool {
		state := actor.State()
		if state == nil || state.Status != SessionIdle || state.PendingQuestion != nil || state.PendingTool != nil {
			return false
		}
		updated, loadErr := storage.Load(context.Background(), session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		messages := updated.GetMessages()
		if len(messages) != 4 {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after self-heal."
	}, 5*time.Second, 20*time.Millisecond)
}

func TestSessionActorApproveToolAvoidsDuplicateExecutionWhenResumeAlreadyStarted(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("Start approval flow."))
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "tool_approval",
		Name: "team_echo",
		Args: map[string]interface{}{"message": "hello"},
	}}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-self-heal-approval-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-self-heal-approval-model",
		responses: []*llm.LLMResponse{
			{Content: "Finished after conservative approval resume.", Model: "test-self-heal-approval-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &runMetaCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-self-heal-approval-test",
		Model:        provider.Name(),
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionWaitingApproval,
		CurrentTurnID: "turn_approval_self_heal",
		PendingTool: &PendingToolInvocation{
			ToolCallID:         "tool_approval",
			ToolName:           "team_echo",
			ArgsJSON:           []byte(`{"message":"hello"}`),
			ExecutionState:     PendingToolExecutionStarted,
			ExecutionStartedAt: time.Now().UTC(),
			CreatedAt:          time.Now().UTC(),
		},
		PendingApproval: &ApprovalRequest{
			ID:         "tool_approval",
			SessionID:  session.ID,
			ToolCallID: "tool_approval",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"hello"}`),
			Reason:     "manual approval",
		},
		UpdatedAt: time.Now().UTC(),
	}))

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor.ApproveTool(context.Background(), "tool_approval", true))

	require.Eventually(t, func() bool {
		state := actor.State()
		if state == nil || state.Status != SessionIdle || state.PendingApproval != nil || state.PendingTool != nil {
			return false
		}
		updated, loadErr := storage.Load(context.Background(), session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		messages := updated.GetMessages()
		if len(messages) < 4 {
			return false
		}
		if messages[2].Role != "tool" {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after conservative approval resume."
	}, 5*time.Second, 20*time.Millisecond)

	require.Equal(t, 0, mcpManager.callCount)
}

func TestSessionActorApproveToolUsesStoredReceiptWhenExecutionCompleted(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("Start approval flow."))
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "tool_approval_receipt",
		Name: "team_echo",
		Args: map[string]interface{}{"message": "hello"},
	}}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-approval-receipt-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-approval-receipt-model",
		responses: []*llm.LLMResponse{
			{Content: "Finished after receipt recovery.", Model: "test-approval-receipt-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &runMetaCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-approval-receipt-test",
		Model:        provider.Name(),
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	toolMessage := types.NewToolMessage("tool_approval_receipt", "stored receipt")
	receipt, err := encodePendingToolResultMessage(toolMessage)
	require.NoError(t, err)

	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionWaitingApproval,
		CurrentTurnID: "turn_approval_receipt",
		PendingTool: &PendingToolInvocation{
			ToolCallID:           "tool_approval_receipt",
			ToolName:             "team_echo",
			ArgsJSON:             []byte(`{"message":"hello"}`),
			ExecutionState:       PendingToolExecutionCompleted,
			ResultMessageJSON:    receipt,
			ExecutionCompletedAt: time.Now().UTC(),
			CreatedAt:            time.Now().UTC(),
		},
		PendingApproval: &ApprovalRequest{
			ID:         "tool_approval_receipt",
			SessionID:  session.ID,
			ToolCallID: "tool_approval_receipt",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"hello"}`),
			Reason:     "manual approval",
		},
		UpdatedAt: time.Now().UTC(),
	}))

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor.ApproveTool(context.Background(), "tool_approval_receipt", true))

	require.Eventually(t, func() bool {
		state := actor.State()
		if state == nil || state.Status != SessionIdle || state.PendingApproval != nil || state.PendingTool != nil {
			return false
		}
		updated, loadErr := storage.Load(context.Background(), session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		messages := updated.GetMessages()
		if len(messages) < 4 {
			return false
		}
		if messages[2].Role != "tool" || messages[2].Content != "stored receipt" {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after receipt recovery."
	}, 5*time.Second, 20*time.Millisecond)

	assertRuntimeEvent(t, runtimeStore, session.ID, EventToolReceiptReplayed, map[string]string{
		"tool_call_id": "tool_approval_receipt",
		"source":       "runtime_state",
	})
	require.Equal(t, 0, mcpManager.callCount)
}

func TestSessionActorApproveToolUsesIndependentReceiptStore(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("Start approval flow."))
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "tool_approval_independent_receipt",
		Name: "team_echo",
		Args: map[string]interface{}{"message": "hello"},
	}}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-approval-independent-receipt-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-approval-independent-receipt-model",
		responses: []*llm.LLMResponse{
			{Content: "Finished after independent receipt recovery.", Model: "test-approval-independent-receipt-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &runMetaCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-approval-independent-receipt-test",
		Model:        provider.Name(),
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	toolMessage := types.NewToolMessage("tool_approval_independent_receipt", "stored independent receipt")
	receipt, err := encodePendingToolResultMessage(toolMessage)
	require.NoError(t, err)

	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionWaitingApproval,
		CurrentTurnID: "turn_approval_independent_receipt",
		PendingTool: &PendingToolInvocation{
			ToolCallID: "tool_approval_independent_receipt",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"hello"}`),
			CreatedAt:  time.Now().UTC(),
		},
		PendingApproval: &ApprovalRequest{
			ID:         "tool_approval_independent_receipt",
			SessionID:  session.ID,
			ToolCallID: "tool_approval_independent_receipt",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"hello"}`),
			Reason:     "manual approval",
		},
		UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, runtimeStore.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   session.ID,
		ToolCallID:  "tool_approval_independent_receipt",
		ToolName:    "team_echo",
		MessageJSON: receipt,
		CreatedAt:   time.Now().UTC(),
	}))

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	require.NoError(t, actor.ApproveTool(context.Background(), "tool_approval_independent_receipt", true))

	require.Eventually(t, func() bool {
		state := actor.State()
		if state == nil || state.Status != SessionIdle || state.PendingApproval != nil || state.PendingTool != nil {
			return false
		}
		updated, loadErr := storage.Load(context.Background(), session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		messages := updated.GetMessages()
		if len(messages) < 4 {
			return false
		}
		if messages[2].Role != "tool" || messages[2].Content != "stored independent receipt" {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after independent receipt recovery."
	}, 5*time.Second, 20*time.Millisecond)

	receiptAfter, err := runtimeStore.GetToolReceipt(ctx, session.ID, "tool_approval_independent_receipt")
	require.NoError(t, err)
	assert.Nil(t, receiptAfter)
	assertRuntimeEvent(t, runtimeStore, session.ID, EventToolReceiptReplayed, map[string]string{
		"tool_call_id": "tool_approval_independent_receipt",
		"source":       "receipt_store",
	})
	require.Equal(t, 0, mcpManager.callCount)
}

func assertRuntimeEvent(t *testing.T, store EventStore, sessionID, eventType string, payload map[string]string) {
	t.Helper()

	events, err := store.ListEvents(context.Background(), sessionID, 0, 0)
	require.NoError(t, err)

	var matched *runtimeevents.Event
	for i := range events {
		if events[i].Type != eventType {
			continue
		}
		ok := true
		for key, expected := range payload {
			if stringPayloadValue(events[i].Payload, key) != expected {
				ok = false
				break
			}
		}
		if ok {
			matched = &events[i]
			break
		}
	}

	if matched == nil {
		t.Fatalf("event %s with payload %v not found", eventType, payload)
	}
}

func stringPayloadValue(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}
