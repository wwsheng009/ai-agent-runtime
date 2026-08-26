package chat

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSessionActorSubmitPromptReturnsAfterActorStopped(t *testing.T) {
	actor := &SessionActor{
		cmdCh: make(chan Command, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	actor.Stop()

	resultCh := make(chan error, 1)
	go func() {
		_, err := actor.SubmitPrompt(context.Background(), "retry", nil)
		resultCh <- err
	}()

	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, ErrSessionActorStopped)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("SubmitPrompt blocked after the actor had stopped")
	}
}

func TestReplaceSessionHistoryAndAdvancePromptCacheEpoch(t *testing.T) {
	session := NewSession("prompt-cache-user")
	session.AddMessage(*types.NewUserMessage("original"))

	previous := replaceSessionHistoryAndAdvancePromptCacheEpoch(session, []types.Message{
		*types.NewUserMessage("replacement"),
	})
	require.Zero(t, previous)
	require.Equal(t, 1, contextIntValue(session.Metadata.Context, agent.PromptCacheEpochSessionContextKey))
	require.Len(t, session.History, 1)
	require.Equal(t, "replacement", session.History[0].Content)

	original := session.GetMessages()
	previous = replaceSessionHistoryAndAdvancePromptCacheEpoch(session, []types.Message{
		*types.NewUserMessage("second replacement"),
	})
	require.Equal(t, 1, previous)
	require.Equal(t, 2, contextIntValue(session.Metadata.Context, agent.PromptCacheEpochSessionContextKey))

	restoreSessionHistoryAndPromptCacheEpoch(session, original, previous)
	require.Equal(t, 1, contextIntValue(session.Metadata.Context, agent.PromptCacheEpochSessionContextKey))
	require.Equal(t, original, session.GetMessages())
}

type blockingRuntimeStateStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingRuntimeStateStore) SaveState(ctx context.Context, state *RuntimeState) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

func (s *blockingRuntimeStateStore) LoadState(ctx context.Context, sessionID string) (*RuntimeState, error) {
	return nil, nil
}

func (s *blockingRuntimeStateStore) DeleteState(ctx context.Context, sessionID string) error {
	return nil
}

func TestSessionActorStopContextTimesOutWhileCommandIsStuckInSaveState(t *testing.T) {
	store := &blockingRuntimeStateStore{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	actor := &SessionActor{
		id:         "stop-timeout-session",
		cmdCh:      make(chan Command, 4),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		stateStore: store,
	}
	actor.Start()

	reply := make(chan error, 1)
	actor.cmdCh <- Interrupt{Ctx: context.Background(), Reply: reply}
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt handler did not reach SaveState")
	}

	stateCh := make(chan *RuntimeState, 1)
	go func() {
		stateCh <- actor.State()
	}()
	select {
	case <-stateCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("State blocked while SaveState was in flight")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	err := actor.StopContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected StopContext deadline, got %v", err)
	}
	if time.Since(startedAt) > 500*time.Millisecond {
		t.Fatalf("StopContext blocked for too long: %s", time.Since(startedAt))
	}

	close(store.release)
	select {
	case <-actor.done:
	case <-time.After(2 * time.Second):
		t.Fatal("actor did not finish stopping after SaveState was released")
	}
}

func TestCloneLoopConfigForRunRequiresBoundTeamTaskForCompleteTask(t *testing.T) {
	base := &agent.LoopReActConfig{
		CompletionRequirement: agent.CompletionRequirementCompleteTask,
	}
	tests := []struct {
		name    string
		runMeta *team.RunMeta
		want    string
	}{
		{
			name: "persisted base without run meta is ignored",
			want: agent.CompletionRequirementNone,
		},
		{
			name: "standalone child requirement is ignored",
			runMeta: &team.RunMeta{
				CompletionRequirement: "complete_task",
			},
			want: agent.CompletionRequirementNone,
		},
		{
			name: "team without current task is ignored",
			runMeta: &team.RunMeta{
				CompletionRequirement: "complete_task",
				Team:                  &team.TeamRunMeta{TeamID: "team-1"},
			},
			want: agent.CompletionRequirementNone,
		},
		{
			name: "bound teammate task keeps explicit requirement",
			runMeta: &team.RunMeta{
				CompletionRequirement: "complete_task",
				Team: &team.TeamRunMeta{
					TeamID:        "team-1",
					CurrentTaskID: "task-1",
				},
			},
			want: agent.CompletionRequirementCompleteTask,
		},
		{
			name: "explicit none overrides persisted base for team run",
			runMeta: &team.RunMeta{
				CompletionRequirement: "none",
				Team: &team.TeamRunMeta{
					TeamID:        "team-1",
					CurrentTaskID: "task-1",
				},
			},
			want: agent.CompletionRequirementNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cloneLoopConfigForRun(base, nil, tt.runMeta)
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.CompletionRequirement)
		})
	}
}

func TestAppendSessionActorToolErrorPayloadDistinguishesRecoveredErrors(t *testing.T) {
	payload := map[string]interface{}{}
	appendSessionActorToolErrorPayload(payload, &agent.Result{
		Success:                   true,
		ToolErrorCount:            3,
		RecoveredToolErrorCount:   2,
		UnrecoveredToolErrorCount: 1,
	})

	require.Equal(t, 3, payload["tool_error_count"])
	require.Equal(t, 2, payload["recovered_tool_error_count"])
	require.Equal(t, 1, payload["unrecovered_tool_error_count"])
}

func TestPersistSessionPreservesBrokerHandleAliasesFromLatestStoredSession(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStorage()
	session := NewSession("alias-owner")
	require.NoError(t, store.Save(ctx, session))

	staleActorSession, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	latestBrokerSession, err := store.Load(ctx, session.ID)
	require.NoError(t, err)

	aliases := map[string]interface{}{
		"sessions": map[string]interface{}{
			"alias_to_actual": map[string]interface{}{
				"session_ref_test": "child-1",
			},
		},
	}
	latestBrokerSession.SetContext(toolbroker.SessionHandleAliasesContextKey, aliases)
	require.NoError(t, store.Update(ctx, latestBrokerSession))

	staleActorSession.Metadata.Title = "actor update"
	hookPrepared := staleActorSession.Clone()
	hookPrepared.Metadata.Title = "hook update"
	actor := &SessionActor{
		sessionStore: store,
		persistHook: func(context.Context, *Session) (*Session, error) {
			return hookPrepared, nil
		},
	}
	require.NoError(t, actor.persistSession(ctx, staleActorSession))

	persisted, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, "hook update", persisted.Metadata.Title)
	got, ok := persisted.GetContext(toolbroker.SessionHandleAliasesContextKey)
	require.True(t, ok)
	assert.Equal(t, aliases, got)
}

func TestSessionActorSubmitPromptReturnsWhenLoopExitsBeforeReply(t *testing.T) {
	actor := &SessionActor{
		cmdCh: make(chan Command, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	actor.startOnce.Do(func() {})

	resultCh := make(chan error, 1)
	go func() {
		_, err := actor.SubmitPrompt(context.Background(), "retry", nil)
		resultCh <- err
	}()

	require.Eventually(t, func() bool { return len(actor.cmdCh) == 1 }, 250*time.Millisecond, time.Millisecond)
	close(actor.done)

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrSessionActorStopped) {
			t.Fatalf("expected stopped actor error, got %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("SubmitPrompt blocked after the actor loop exited")
	}
}

type capturingSequenceProvider struct {
	name         string
	responses    []*llm.LLMResponse
	requests     []*llm.LLMRequest
	callCount    int
	capabilities map[string]agentconfig.ModelCapabilitySpec
}

func (p *capturingSequenceProvider) Name() string {
	return p.name
}

func (p *capturingSequenceProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if req != nil {
		cloned := &llm.LLMRequest{
			Provider:        req.Provider,
			Model:           req.Model,
			Tools:           append([]types.ToolDefinition(nil), req.Tools...),
			MaxTokens:       req.MaxTokens,
			Temperature:     req.Temperature,
			ReasoningEffort: req.ReasoningEffort,
			Thinking:        types.CloneThinkingConfig(req.Thinking),
			Stream:          req.Stream,
		}
		if len(req.Metadata) > 0 {
			cloned.Metadata = make(map[string]interface{}, len(req.Metadata))
			for key, value := range req.Metadata {
				cloned.Metadata[key] = value
			}
		}
		if len(req.Messages) > 0 {
			cloned.Messages = make([]types.Message, len(req.Messages))
			for index := range req.Messages {
				cloned.Messages[index] = *req.Messages[index].Clone()
			}
		}
		p.requests = append(p.requests, cloned)
	}
	if p.callCount >= len(p.responses) {
		return &llm.LLMResponse{Content: "done", Model: p.name}, nil
	}
	response := p.responses[p.callCount]
	p.callCount++
	return response, nil
}

func (p *capturingSequenceProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	response, err := p.Call(ctx, req)
	ch := make(chan llm.StreamChunk, 2)
	go func() {
		defer close(ch)
		if err != nil {
			ch <- llm.StreamChunk{Type: llm.EventTypeError, Error: err.Error(), Done: true}
			return
		}
		if response != nil && strings.TrimSpace(response.Content) != "" {
			ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: response.Content}
		}
		ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	}()
	return ch, nil
}

func (p *capturingSequenceProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *capturingSequenceProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *capturingSequenceProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (p *capturingSequenceProvider) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	capability, ok := llm.ResolveModelCapabilitySpec(requestedModel, p.capabilities)
	return requestedModel, capability, ok
}

type simpleEchoMCPManager struct {
	callCount int
	messages  []string
}

func (m *simpleEchoMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if toolName != "team_echo" {
		return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return skill.ToolInfo{
		Name:          toolName,
		Description:   "Echo tool for tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *simpleEchoMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	message, _ := args["message"].(string)
	m.callCount++
	m.messages = append(m.messages, strings.TrimSpace(message))
	if strings.TrimSpace(message) == "" {
		return "ok", nil
	}
	return "echo: " + strings.TrimSpace(message), nil
}

func (m *simpleEchoMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("team_echo")
	return []skill.ToolInfo{info}
}

func requestMessagesContain(messages []types.Message, role, content string) bool {
	for _, message := range messages {
		if message.Role == role && strings.Contains(message.Content, content) {
			return true
		}
	}
	return false
}

type failingLLMProvider struct {
	name     string
	err      error
	requests []*llm.LLMRequest
}

func (p *failingLLMProvider) Name() string {
	return p.name
}

func (p *failingLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if req != nil {
		p.requests = append(p.requests, &llm.LLMRequest{
			Provider: req.Provider,
			Model:    req.Model,
		})
	}
	return nil, p.err
}

func (p *failingLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, p.err
}

func (p *failingLLMProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *failingLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens: 128000,
		SupportsTools:    true,
	}
}

func (p *failingLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

type cancelBlockingLLMProvider struct {
	name    string
	entered chan struct{}
}

func (p *cancelBlockingLLMProvider) Name() string {
	return p.name
}

func (p *cancelBlockingLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *cancelBlockingLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, fmt.Errorf("streaming is not supported")
}

func (p *cancelBlockingLLMProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *cancelBlockingLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens: 128000,
		SupportsTools:    true,
	}
}

func (p *cancelBlockingLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func TestNewPendingToolInvocationGeneratesDeterministicStandaloneID(t *testing.T) {
	argsJSON := []byte(`{"prompt":"Need confirmation","required":true}`)

	first, err := newPendingToolInvocation(context.Background(), "", toolbroker.ToolAskUserQuestion, argsJSON)
	require.NoError(t, err)
	second, err := newPendingToolInvocation(context.Background(), "", toolbroker.ToolAskUserQuestion, argsJSON)
	require.NoError(t, err)

	require.Equal(t, first.ToolCallID, second.ToolCallID)
	require.True(t, strings.HasPrefix(first.ToolCallID, "toolcall_pending_"))
	require.Len(t, first.BatchToolCalls, 1)
	require.Equal(t, first.ToolCallID, first.BatchToolCalls[0].ToolCallID)
}

func TestNewPendingToolInvocationInfersStableCurrentBatchToolCallID(t *testing.T) {
	firstArgs := map[string]interface{}{"message": "first"}
	secondArgs := map[string]interface{}{"message": "second"}
	firstID := types.DeterministicToolCallID("toolcall_", 0, "team_echo", firstArgs)
	secondID := types.DeterministicToolCallID("toolcall_", 1, "team_echo", secondArgs)

	ctx := agent.WithToolBatchContext(context.Background(), []types.ToolCall{
		{Name: "team_echo", Args: firstArgs},
		{Name: "team_echo", Args: secondArgs},
	}, "", []types.Message{
		*types.NewToolMessage(firstID, "echo: first"),
	})

	first, err := newPendingToolInvocation(ctx, "", "team_echo", []byte(`{"message":"second"}`))
	require.NoError(t, err)
	second, err := newPendingToolInvocation(ctx, "", "team_echo", []byte(`{"message":"second"}`))
	require.NoError(t, err)

	require.Equal(t, secondID, first.ToolCallID)
	require.Equal(t, first.ToolCallID, second.ToolCallID)
	require.Len(t, first.BatchToolCalls, 2)
	require.Equal(t, firstID, first.BatchToolCalls[0].ToolCallID)
	require.Equal(t, secondID, first.BatchToolCalls[1].ToolCallID)
}

func TestSessionActorRequestApprovalExpiresWithStructuredError(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "approval-expiry-user")
	require.NoError(t, err)

	apiAgent := agent.NewAgent(&agent.Config{
		Name:     "approval-expiry-agent",
		Model:    "test-model",
		MaxSteps: 1,
	}, nil)
	runtimeStore := NewInMemoryRuntimeStore(32)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	_, err = actor.RequestApproval(ctx, runtimepolicy.ApprovalRequest{
		ID:         "approval-expiry",
		SessionID:  session.ID,
		ToolCallID: "tool-expiry",
		ToolName:   "write_file",
		ExpiresAt:  time.Now().UTC().Add(40 * time.Millisecond),
	})
	require.Error(t, err)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrApprovalExpired), "unexpected error: %v", err)

	state := actor.State()
	require.NotNil(t, state)
	require.Equal(t, SessionIdle, state.Status)
	require.Nil(t, state.PendingApproval)
	require.Nil(t, state.PendingTool)

	events, listErr := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, listErr)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, EventApprovalResolved, last.Type)
	require.Equal(t, string(runtimeerrors.ErrApprovalExpired), last.Payload["error_code"])
}

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

func TestSessionActorInterruptClearsBusyRuntimeState(t *testing.T) {
	ctx := context.Background()
	runtimeStore := NewInMemoryRuntimeStore(64)
	sessionID := "actor-interrupt-state"
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     sessionID,
		Status:        SessionRunning,
		CurrentTurnID: "turn_busy",
		CurrentRunMeta: &team.RunMeta{
			PermissionMode: "default",
		},
		FrozenTurnTools:    []types.ToolDefinition{{Name: "grep"}},
		FrozenTurnToolsSet: true,
		PendingTool: &PendingToolInvocation{
			ToolCallID: "tool_busy",
			ToolName:   "grep",
		},
		PendingApproval: &runtimepolicy.ApprovalRequest{
			ID:       "approval_busy",
			ToolName: "grep",
		},
		PendingQuestion: &toolbroker.UserQuestionRequest{
			ID:     "question_busy",
			Prompt: "continue?",
		},
	}))

	actor := &SessionActor{
		id:         sessionID,
		stateStore: runtimeStore,
		eventStore: runtimeStore,
		eventBus:   runtimeevents.NewBus(),
	}
	require.NoError(t, actor.loadState(ctx))

	reply := make(chan error, 1)
	actor.handleInterrupt(Interrupt{Reply: reply})
	require.NoError(t, <-reply)

	state, err := runtimeStore.LoadState(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, SessionStopped, state.Status)
	require.Empty(t, state.CurrentTurnID)
	require.Nil(t, state.CurrentRunMeta)
	require.Nil(t, state.PendingTool)
	require.Nil(t, state.PendingApproval)
	require.Nil(t, state.PendingQuestion)
	require.False(t, state.FrozenTurnToolsSet)
	require.Empty(t, state.FrozenTurnTools)
}

func TestSessionActorRecoversOrphanedRunningStateAndAcceptsPrompt(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "actor-recovered-running-user")
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:   1,
	})
	mockProvider := NewMockLLMProviderForChat()
	require.NoError(t, runtime.RegisterProvider(mockProvider.Name(), mockProvider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4", mockProvider.Name()))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-recovered-running-agent",
		Model:    "gpt-4",
		MaxSteps: 3,
	}, nil, runtime)
	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionRunning,
		CurrentTurnID: "turn_orphaned",
		CurrentRunMeta: &team.RunMeta{
			PermissionMode: "default",
		},
		FrozenTurnTools:    []types.ToolDefinition{{Name: "grep"}},
		FrozenTurnToolsSet: true,
		UpdatedAt:          time.Now().UTC().Add(-time.Minute),
	}))

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
		RecoverStale: true,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	recovered := actor.State()
	require.NotNil(t, recovered)
	require.Equal(t, SessionStopped, recovered.Status)
	require.Empty(t, recovered.CurrentTurnID)
	require.Nil(t, recovered.CurrentRunMeta)
	require.False(t, recovered.FrozenTurnToolsSet)
	require.Empty(t, recovered.FrozenTurnTools)

	persisted, err := runtimeStore.LoadState(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, SessionStopped, persisted.Status)
	require.Empty(t, persisted.CurrentTurnID)

	result, err := actor.SubmitPrompt(ctx, "continue after restore", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, SessionIdle, actor.State().Status)
}

func TestLoadRuntimeStateForInspectionReconcilesRunningStateWithoutLiveLease(t *testing.T) {
	ctx := context.Background()
	runtimeStore := NewInMemoryRuntimeStore(64)
	sessionID := "inspection-stale-running"
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     sessionID,
		Status:        SessionRunning,
		CurrentTurnID: "turn-stale",
		UpdatedAt:     time.Now().UTC().Add(-time.Minute),
	}))
	_, err := runtimeStore.AcquireLease(ctx, LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "owner-stale",
		OwnerKind: "test",
		TTL:       time.Second,
		Now:       time.Now().UTC().Add(-time.Minute),
	})
	require.NoError(t, err)

	state, err := LoadRuntimeStateForInspection(ctx, runtimeStore, sessionID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, SessionStopped, state.Status)
	require.Empty(t, state.CurrentTurnID)

	persisted, err := runtimeStore.LoadState(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, SessionStopped, persisted.Status)
}

func TestLoadRuntimeStateForInspectionPreservesRunningStateWithLiveLease(t *testing.T) {
	ctx := context.Background()
	runtimeStore := NewInMemoryRuntimeStore(64)
	sessionID := "inspection-live-running"
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     sessionID,
		Status:        SessionRunning,
		CurrentTurnID: "turn-live",
		UpdatedAt:     time.Now().UTC(),
	}))
	_, err := runtimeStore.AcquireLease(ctx, LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "owner-live",
		OwnerKind: "test",
		TTL:       time.Minute,
	})
	require.NoError(t, err)

	state, err := LoadRuntimeStateForInspection(ctx, runtimeStore, sessionID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, SessionRunning, state.Status)
	require.Equal(t, "turn-live", state.CurrentTurnID)
}

func TestSessionActorSubmitPromptRouteOverrideAppliesToNextLLMRequestOnly(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-route-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "base-provider",
		DefaultModel:    "base-model",
		MaxRetries:      0,
	})
	baseProvider := &capturingSequenceProvider{
		name: "base-provider",
		responses: []*llm.LLMResponse{{
			Content: "base reply",
			Model:   "base-model",
		}},
	}
	routeProvider := &capturingSequenceProvider{
		name: "route-provider",
		responses: []*llm.LLMResponse{{
			Content: "route reply",
			Model:   "route-model",
		}},
	}
	require.NoError(t, runtime.RegisterProvider(baseProvider.Name(), baseProvider))
	require.NoError(t, runtime.RegisterProvider(routeProvider.Name(), routeProvider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-route-test",
		Provider: "base-provider",
		Model:    "base-model",
		MaxSteps: 1,
	}, nil, runtime)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   NewInMemoryRuntimeStore(64),
	})
	require.NoError(t, err)

	first, err := actor.SubmitPrompt(ctx, "Use the routed model for this run.", nil, SubmitPromptOption{
		RouteOverride: &RunRouteOverride{
			Provider:        "route-provider",
			Model:           "route-model",
			ReasoningEffort: "high",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, first)

	require.Len(t, routeProvider.requests, 1)
	require.Equal(t, "route-provider", routeProvider.requests[0].Provider)
	require.Equal(t, "route-model", routeProvider.requests[0].Model)
	require.Equal(t, "high", routeProvider.requests[0].ReasoningEffort)
	require.Empty(t, baseProvider.requests)

	second, err := actor.SubmitPrompt(ctx, "Use the base model for this run.", nil)
	require.NoError(t, err)
	require.NotNil(t, second)

	require.Len(t, routeProvider.requests, 1)
	require.Len(t, baseProvider.requests, 1)
	require.Equal(t, "base-provider", baseProvider.requests[0].Provider)
	require.Equal(t, "base-model", baseProvider.requests[0].Model)
	require.Empty(t, baseProvider.requests[0].ReasoningEffort)

	cfg := apiAgent.GetConfig()
	require.NotNil(t, cfg)
	require.Equal(t, "base-provider", cfg.Provider)
	require.Equal(t, "base-model", cfg.Model)
}

func TestSessionActorApproveToolResumeUsesRouteOverrideFromRunMeta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-route-approval-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "base-provider",
		DefaultModel:    "base-model",
		MaxRetries:      0,
	})
	baseProvider := &capturingSequenceProvider{
		name: "base-provider",
		responses: []*llm.LLMResponse{{
			Content: "base reply",
			Model:   "base-model",
		}},
	}
	routeProvider := &capturingSequenceProvider{
		name: "route-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "route needs approval",
				Model:   "route-model",
				ToolCalls: []types.ToolCall{{
					ID:   "tool_route_approval",
					Name: "team_echo",
					Args: map[string]interface{}{"message": "hello"},
				}},
			},
			{
				Content: "route resumed",
				Model:   "route-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(baseProvider.Name(), baseProvider))
	require.NoError(t, runtime.RegisterProvider(routeProvider.Name(), routeProvider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-route-approval-test",
		Provider: "base-provider",
		Model:    "base-model",
		MaxSteps: 3,
	}, &simpleEchoMCPManager{}, runtime)
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
			TeamID:               "team-route-approval",
			AgentID:              "mate-route-approval",
			CurrentTaskID:        "task-route-approval",
			RouteProvider:        "route-provider",
			RouteModel:           "route-model",
			RouteReasoningEffort: "high",
		},
	}
	resultCh := make(chan *agent.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, submitErr := actor1.SubmitPrompt(ctx, "Start routed approval flow.", runMeta, SubmitPromptOption{
			RouteOverride: &RunRouteOverride{
				Provider:        "route-provider",
				Model:           "route-model",
				ReasoningEffort: "high",
			},
		})
		resultCh <- result
		errCh <- submitErr
	}()

	var requestID string
	require.Eventually(t, func() bool {
		state := actor1.State()
		if state == nil || state.PendingApproval == nil || state.PendingTool == nil {
			return false
		}
		if state.Status != SessionWaitingApproval || state.PendingTool.ToolCallID != "tool_route_approval" {
			return false
		}
		requestID = state.PendingApproval.ID
		return requestID != ""
	}, 5*time.Second, 20*time.Millisecond)

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
		return last.Role == "assistant" && last.Content == "route resumed"
	}, 5*time.Second, 20*time.Millisecond)

	require.Len(t, routeProvider.requests, 2)
	for _, request := range routeProvider.requests {
		require.Equal(t, "route-provider", request.Provider)
		require.Equal(t, "route-model", request.Model)
		require.Equal(t, "high", request.ReasoningEffort)
	}
	require.Empty(t, baseProvider.requests)

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("original routed approval submit did not exit after cancellation")
	}
	select {
	case <-resultCh:
	default:
	}
}

func TestRunRouteOverrideFromRunMetaUsesCurrentTeamRoute(t *testing.T) {
	assert.Nil(t, runRouteOverrideFromRunMeta(nil))
	assert.Nil(t, runRouteOverrideFromRunMeta(&team.RunMeta{
		Team: &team.TeamRunMeta{
			Difficulty: "hard",
		},
	}))

	override := runRouteOverrideFromRunMeta(&team.RunMeta{
		Team: &team.TeamRunMeta{
			RouteProvider:        " route-provider ",
			RouteModel:           " route-model ",
			RouteReasoningEffort: " high ",
		},
	})
	require.NotNil(t, override)
	assert.Equal(t, "route-provider", override.Provider)
	assert.Equal(t, "route-model", override.Model)
	assert.Equal(t, "high", override.ReasoningEffort)
}

func TestSessionActorContinueUsesTransientPromptAndStripsIt(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	session.AddMessage(*types.NewAssistantMessage("previous result"))
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-continue-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-continue-model",
		responses: []*llm.LLMResponse{{
			Content: "audit complete",
			Model:   "test-continue-model",
		}},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:  "actor-continue-test-agent",
		Model: "test-continue-model",
	}, nil, runtime)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   NewInMemoryRuntimeStore(64),
	})
	require.NoError(t, err)

	result, err := actor.Continue(ctx, nil, ContinueOption{
		ContinuationPrompt: "hidden completion audit",
		ContinuationMetadata: map[string]interface{}{
			"hidden_goal_audit": true,
		},
		StripMetadataKeys: []string{"hidden_goal_audit"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, provider.requests)
	require.True(t, requestMessagesContain(provider.requests[0].Messages, "user", "hidden completion audit"))

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.False(t, requestMessagesContain(loaded.History, "user", "hidden completion audit"))
	require.True(t, requestMessagesContain(loaded.History, "assistant", "audit complete"))
}

func TestSessionActorSubmitPromptRollsBackPromptWhenInitialLLMRequestFails(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	session.AddMessage(*types.NewUserMessage("previous"))
	session.AddMessage(*types.NewAssistantMessage("ack"))
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "failing-provider",
		DefaultModel:    "gpt-5.5",
		MaxRetries:      0,
	})
	provider := &failingLLMProvider{
		name: "failing-provider",
		err:  fmt.Errorf("HTTP 503: model_not_found"),
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5.5", provider.Name()))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-failure-test",
		Provider: provider.Name(),
		Model:    "gpt-5.5",
		MaxSteps: 3,
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

	result, err := actor.SubmitPrompt(ctx, "ls", nil)
	require.Error(t, err)
	require.NotNil(t, result)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.History, 2)
	require.Equal(t, "previous", updated.History[0].Content)
	require.Equal(t, "ack", updated.History[1].Content)
	require.Len(t, provider.requests, 1)
}

func TestNewSessionActor_DefaultLoopConfigEnablesBoundedParallelTools(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	apiAgent := agent.NewAgent(&agent.Config{
		Name:         "actor-fallback-test",
		Model:        "test-model",
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, nil)

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
	})
	require.NoError(t, err)
	require.NotNil(t, actor)
	require.NotNil(t, actor.loopConfig)
	assert.True(t, actor.loopConfig.EnableParallelTools)
	assert.Equal(t, 4, actor.loopConfig.MaxParallelToolCalls)
}

type reasoningOnlyActorProvider struct{}

func (*reasoningOnlyActorProvider) Name() string { return "reasoning-only-actor-provider" }

func (*reasoningOnlyActorProvider) Call(context.Context, *llm.LLMRequest) (*llm.LLMResponse, error) {
	return &llm.LLMResponse{
		Content: "",
		Model:   "reasoning-only-model",
		ReasoningBlock: &types.ReasoningBlock{
			Provider:   "reasoning-only-actor-provider",
			Format:     "summary",
			Summary:    "authoritative reasoning without assistant text",
			Visibility: types.ReasoningVisibilitySummary,
		},
		Usage: &types.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}, nil
}

func (p *reasoningOnlyActorProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (*reasoningOnlyActorProvider) CountTokens(text string) int { return len(text) / 4 }

func (*reasoningOnlyActorProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens: 128000, MaxOutputTokens: 4096,
		SupportsTools: true, SupportsStreaming: false,
	}
}

func (*reasoningOnlyActorProvider) CheckHealth(context.Context) error { return nil }

func TestSessionActorSubmitPrompt_PublishesAssistantMessageBeforeSessionEnd(t *testing.T) {
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
		Name:         "actor-order-test",
		Model:        "gpt-4",
		MaxSteps:     3,
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

	result, err := actor.SubmitPrompt(ctx, "Hello there", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, strings.TrimSpace(result.Output))

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)

	assistantIndex := -1
	sessionEndIndex := -1
	var sessionStartTurnID string
	var assistantPayload map[string]interface{}
	var assistantDeltas []runtimeevents.Event
	var sessionEndPayload map[string]interface{}
	for index, event := range events {
		switch event.Type {
		case EventSessionStart:
			sessionStartTurnID, _ = event.Payload["turn_id"].(string)
		case EventAssistantDelta:
			assistantDeltas = append(assistantDeltas, event)
		case EventAssistantMessage:
			if assistantIndex < 0 {
				assistantIndex = index
				assistantPayload = event.Payload
			}
		case EventSessionEnd:
			if sessionEndIndex < 0 {
				sessionEndIndex = index
				sessionEndPayload = event.Payload
			}
		}
	}

	require.GreaterOrEqual(t, assistantIndex, 0)
	require.GreaterOrEqual(t, sessionEndIndex, 0)
	require.Less(t, assistantIndex, sessionEndIndex)
	require.NotEmpty(t, sessionStartTurnID)
	require.Equal(t, sessionStartTurnID, result.TurnID)
	require.Equal(t, result.TurnID, assistantPayload["turn_id"])
	require.Equal(t, "snapshot", assistantPayload["mode"])
	if len(assistantDeltas) > 0 {
		streamID, _ := assistantPayload["stream_id"].(string)
		require.NotEmpty(t, streamID)
		for index, event := range assistantDeltas {
			require.Equal(t, result.TurnID, event.Payload["turn_id"])
			require.Equal(t, streamID, event.Payload["stream_id"])
			require.Equal(t, "append", event.Payload["mode"])
			require.EqualValues(t, index+1, event.Payload["sequence"])
		}
		require.EqualValues(t, len(assistantDeltas)+1, assistantPayload["sequence"])
	}
	require.NotNil(t, result.Usage)
	require.Equal(t, result.Usage.PromptTokens, sessionEndPayload["usage_prompt_tokens"])
	require.Equal(t, result.Usage.CompletionTokens, sessionEndPayload["usage_completion_tokens"])
	require.Equal(t, result.Usage.TotalTokens, sessionEndPayload["usage_total_tokens"])
}

func TestSessionActorSubmitPrompt_PublishesReasoningOnlyAssistantMessage(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "actor-reasoning-only-user")
	require.NoError(t, err)

	provider := &reasoningOnlyActorProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: provider.Name(),
		DefaultModel:    "reasoning-only-model",
		MaxRetries:      1,
	})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name: "actor-reasoning-only-test", Provider: provider.Name(),
		Model: "reasoning-only-model", MaxSteps: 3,
	}, nil, runtime)
	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent: apiAgent, LLMRuntime: runtime, SessionStore: storage,
		StateStore: runtimeStore, EventStore: runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	result, err := actor.SubmitPrompt(ctx, "Reason without answer text.", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "", result.Output)
	require.NotNil(t, result.Reasoning)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	assistantIndex, sessionEndIndex := -1, -1
	var assistantPayload map[string]interface{}
	for index, event := range events {
		switch event.Type {
		case EventAssistantMessage:
			assistantIndex = index
			assistantPayload = event.Payload
		case EventSessionEnd:
			sessionEndIndex = index
		}
	}
	require.GreaterOrEqual(t, assistantIndex, 0)
	require.GreaterOrEqual(t, sessionEndIndex, 0)
	require.Less(t, assistantIndex, sessionEndIndex)
	require.Contains(t, assistantPayload, "content")
	require.Equal(t, "", assistantPayload["content"])
	require.Equal(t, "snapshot", assistantPayload["mode"])
	require.Equal(t, result.TurnID, assistantPayload["turn_id"])
	require.Equal(t, result.AssistantStreamID, assistantPayload["stream_id"])
	require.NotEmpty(t, assistantPayload["stream_id"])
	require.EqualValues(t, result.AssistantStreamSequence+1, assistantPayload["sequence"])
	finalReasoning := types.ReasoningBlockFromMap(assistantPayload["reasoning"])
	require.NotNil(t, finalReasoning)
	require.Equal(t, result.Reasoning.RawDisplayText(), finalReasoning.RawDisplayText())
}

func TestSessionActorInterruptConvergesStoppedStateAndTelemetry(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "actor-cancel-user")
	require.NoError(t, err)

	provider := &cancelBlockingLLMProvider{
		name:    "cancel-blocking-provider",
		entered: make(chan struct{}, 1),
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 1})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-cancel-test",
		Provider: provider.Name(),
		Model:    "test-model",
		MaxSteps: 3,
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
	t.Cleanup(actor.Stop)

	type submitResponse struct {
		result *agent.Result
		err    error
	}
	responseCh := make(chan submitResponse, 1)
	go func() {
		result, submitErr := actor.SubmitPrompt(ctx, "wait until interrupted", nil)
		responseCh <- submitResponse{result: result, err: submitErr}
	}()

	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, actor.Interrupt(ctx))

	var response submitResponse
	select {
	case response = <-responseCh:
	case <-time.After(2 * time.Second):
		t.Fatal("SubmitPrompt did not return after interrupt")
	}
	require.ErrorIs(t, response.err, context.Canceled)
	require.NotNil(t, response.result)
	require.GreaterOrEqual(t, response.result.Steps, 1)
	require.Greater(t, response.result.Duration.GetDuration(), time.Duration(0))

	state := actor.State()
	require.Equal(t, SessionStopped, state.Status)
	require.Empty(t, state.CurrentTurnID)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	var sessionEnd map[string]interface{}
	for _, event := range events {
		if event.Type == EventSessionEnd {
			sessionEnd = event.Payload
		}
	}
	require.NotNil(t, sessionEnd)
	require.Equal(t, SessionStopped, sessionEnd["status"])
	require.Equal(t, "user_interrupt", sessionEnd["cancel_source"])
	require.GreaterOrEqual(t, sessionEnd["steps"].(int), 1)
	require.Greater(t, sessionEnd["duration"].(int64), int64(0))
}

func TestAppendStructuredRunErrorPayload_UsesPromptPreflightMetadata(t *testing.T) {
	payload := map[string]interface{}{
		"error": "prompt preflight budget exceeded",
	}
	err := &agent.PromptPreflightError{
		PromptTokens:                  1400,
		PromptBudget:                  900,
		Code:                          "active_turn_not_compactable",
		Reason:                        "active-turn replay cannot be compacted further",
		SuggestedAction:               "请开启新一轮对话、减少上下文，或提高预算。",
		ResolvedProvider:              "test-provider",
		ResolvedModel:                 "test-model",
		ActiveTurnMessageCount:        5,
		LatestReplayBlockMessageCount: 2,
		ReplacementHistory: []types.Message{
			*types.NewUserMessage("继续处理"),
			*types.NewAssistantMessage("Compacted earlier tool replay in current turn: ..."),
		},
		ReplacementHistoryApplied: true,
	}

	appendStructuredRunErrorPayload(payload, err)

	require.Equal(t, "prompt_preflight", payload["error_type"])
	require.Equal(t, "active_turn_not_compactable", payload["failure_reason_code"])
	require.Equal(t, "active-turn replay cannot be compacted further", payload["failure_reason"])
	require.Equal(t, "请开启新一轮对话、减少上下文，或提高预算。", payload["suggested_action"])
	require.Equal(t, "test-provider", payload["resolved_provider"])
	require.Equal(t, "test-model", payload["resolved_model"])
	require.Equal(t, true, payload["replacement_history_available"])
	require.Equal(t, true, payload["replacement_history_applied"])
	require.Equal(t, 2, payload["replacement_history_message_count"])
}

func TestSessionActorMaybeAutoCompactSessionReplacesHistory(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-compact-user")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage(strings.Repeat("older user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("older assistant context ", 80)),
		*types.NewUserMessage(strings.Repeat("recent user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("recent assistant context ", 80)),
	})
	setRuntimeSessionObservedTokenUsage(session, 140)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "compact-provider",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capturingSequenceProvider{
		name: "compact-provider",
		responses: []*llm.LLMResponse{
			{Content: "Preserve the prior investigation details and continue from the latest turn.", Model: "gpt-5"},
		},
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 120},
		},
	}
	require.NoError(t, runtime.RegisterProvider("compact-provider", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "compact-provider"))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-compact-test",
		Provider: "compact-provider",
		Model:    "gpt-5",
		Options: map[string]interface{}{
			"context_keep_recent_messages": 1,
		},
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

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	actor.maybeAutoCompactSession(ctx, loaded, "trace-compact", nil, false)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.History, 3)
	require.Equal(t, "system", updated.History[0].Role)
	require.Equal(t, "user", updated.History[1].Role)
	require.Equal(t, "user", updated.History[2].Role)
	require.Equal(t, "compaction", updated.History[1].Metadata["context_stage"])
	require.Contains(t, updated.History[2].Content, "recent user context")
	require.Equal(t, 140, runtimeSessionObservedTokenUsage(updated))
	require.Equal(t, 1, provider.callCount)
	// Compaction must not change the user-visible title: the derived title
	// stays sticky and generation is tracked as diagnostic context only.
	require.Equal(t, sessionTitleSourceDerived, updated.Metadata.TitleSource)
	require.Contains(t, updated.Metadata.Title, "older user context")
	require.NotContains(t, updated.Metadata.Title, " · compact")
	require.NotContains(t, updated.Metadata.Title, "Preserve the prior investigation")
	require.Equal(t, 1, contextIntValue(updated.Metadata.Context, ContextCompactGeneration))
	require.Equal(t, 1, contextIntValue(updated.Metadata.Context, agent.PromptCacheEpochSessionContextKey))

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	eventTypes := make([]string, 0, len(events))
	for _, event := range events {
		eventTypes = append(eventTypes, event.Type)
	}
	require.Contains(t, eventTypes, EventSessionCompactStarted)
	require.Contains(t, eventTypes, EventSessionCompactCompleted)
}

func TestSessionActorMaybeAutoCompactSessionUsesObservedContextSnapshot(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-compact-observed-context")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage("short user context"),
		*types.NewAssistantMessage("short assistant context"),
	})
	setRuntimeSessionObservedTokenUsage(session, 140)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "compact-provider",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capturingSequenceProvider{
		name: "compact-provider",
		responses: []*llm.LLMResponse{
			{Content: "Keep the compacted facts.", Model: "gpt-5"},
		},
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 120},
		},
	}
	require.NoError(t, runtime.RegisterProvider("compact-provider", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "compact-provider"))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-compact-observed-context-test",
		Provider: "compact-provider",
		Model:    "gpt-5",
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

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	actor.maybeAutoCompactSession(ctx, loaded, "trace-compact-observed", nil, false)

	require.Equal(t, 1, provider.callCount)
	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	var started runtimeevents.Event
	for _, event := range events {
		if event.Type == EventSessionCompactStarted {
			started = event
			break
		}
	}
	require.Equal(t, EventSessionCompactStarted, started.Type)
	require.Equal(t, 140, started.Payload["token_before"])
	require.Equal(t, 120, started.Payload["trigger_token_limit"])
}

func TestSessionActorMaybeAutoCompactSessionIgnoresCumulativeTokenCount(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-compact-cumulative-ignored")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("sys"),
		*types.NewUserMessage("u"),
		*types.NewAssistantMessage("a"),
	})
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	session.Metadata.Context["aicli_token_count"] = 99999
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "compact-provider",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capturingSequenceProvider{
		name: "compact-provider",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 120},
		},
	}
	require.NoError(t, runtime.RegisterProvider("compact-provider", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "compact-provider"))
	require.Less(t, countRuntimeChatContextTokens(runtime, session.GetMessages()), 120)

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-compact-cumulative-ignored-test",
		Provider: "compact-provider",
		Model:    "gpt-5",
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

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	actor.maybeAutoCompactSession(ctx, loaded, "trace-compact-cumulative", nil, false)

	require.Equal(t, 0, provider.callCount)
	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, EventSessionCompactSkipped, last.Type)
	require.Equal(t, "below_limit", last.Payload["reason"])
}

func TestRuntimeSessionActiveContextTokensAddsPendingAfterLastAssistant(t *testing.T) {
	session := NewSession("actor-compact-pending-context")
	setRuntimeSessionObservedTokenUsage(session, 100)
	history := []types.Message{
		*types.NewSystemMessage("sys"),
		*types.NewUserMessage("earlier user"),
		*types.NewAssistantMessage("earlier assistant"),
		*types.NewUserMessage(strings.Repeat("pending user context ", 80)),
	}

	tokens, hasObserved := runtimeSessionActiveContextTokens(nil, session, history)

	require.True(t, hasObserved)
	require.Greater(t, tokens, 100)
}

func TestSessionActorMaybeAutoCompactSessionSkipsWhenContextBelowLimit(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-compact-zero-used")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage(strings.Repeat("older user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("older assistant context ", 80)),
		*types.NewUserMessage(strings.Repeat("recent user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("recent assistant context ", 80)),
	})
	setRuntimeSessionObservedTokenUsage(session, 0)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "compact-provider",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capturingSequenceProvider{
		name: "compact-provider",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 100000},
		},
	}
	require.NoError(t, runtime.RegisterProvider("compact-provider", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "compact-provider"))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-compact-zero-used-test",
		Provider: "compact-provider",
		Model:    "gpt-5",
		Options: map[string]interface{}{
			"context_keep_recent_messages": 1,
		},
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

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	actor.maybeAutoCompactSession(ctx, loaded, "trace-compact-zero", nil, false)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.History, 5)
	require.Equal(t, 0, runtimeSessionObservedTokenUsage(updated))
	require.Equal(t, 0, provider.callCount)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, EventSessionCompactSkipped, last.Type)
	require.Equal(t, "below_limit", last.Payload["reason"])
	tokenBefore, ok := last.Payload["token_before"].(int)
	require.True(t, ok)
	require.Greater(t, tokenBefore, 0)
}

func TestSessionActorMaybeAutoCompactSessionSkipsPendingToolState(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-compact-pending")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage(strings.Repeat("older user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("older assistant context ", 80)),
		*types.NewUserMessage(strings.Repeat("latest user context ", 80)),
	})
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "compact-provider",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capturingSequenceProvider{
		name: "compact-provider",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 80},
		},
	}
	require.NoError(t, runtime.RegisterProvider("compact-provider", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "compact-provider"))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-compact-pending-test",
		Provider: "compact-provider",
		Model:    "gpt-5",
		Options: map[string]interface{}{
			"context_keep_recent_messages": 1,
		},
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
	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.PendingTool = &PendingToolInvocation{ToolCallID: "tool-1", ToolName: "echo"}
		return nil
	}))

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	original := loaded.GetMessages()
	actor.maybeAutoCompactSession(ctx, loaded, "trace-pending", nil, false)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, original, updated.GetMessages())
	require.Equal(t, 0, provider.callCount)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	found := false
	for _, event := range events {
		if event.Type == EventSessionCompactSkipped && event.Payload["reason"] == "pending_tool" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestSessionActorCompactForcesCompaction(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-manual-compact")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage(strings.Repeat("older user context ", 40)),
		*types.NewAssistantMessage(strings.Repeat("older assistant context ", 40)),
		*types.NewUserMessage(strings.Repeat("latest user context ", 20)),
	})
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "compact-provider",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capturingSequenceProvider{
		name: "compact-provider",
		responses: []*llm.LLMResponse{
			{Content: "Preserve the earlier context and continue from the latest turn.", Model: "gpt-5"},
		},
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 5000},
		},
	}
	require.NoError(t, runtime.RegisterProvider("compact-provider", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "compact-provider"))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-manual-compact-test",
		Provider: "compact-provider",
		Model:    "gpt-5",
		Options: map[string]interface{}{
			"context_keep_recent_messages": 1,
		},
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
	stableTools := []types.ToolDefinition{{
		Name:        "view",
		Description: "read a file",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"file_path"},
		},
	}}
	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.StableToolSurface = cloneRuntimeToolDefinitions(stableTools)
		state.StableToolSurfaceSet = true
		return nil
	}))

	result, status, err := actor.Compact(ctx, compactruntime.ModeLocal)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, compactruntime.ModeLocal, result.Mode)
	require.Equal(t, compactruntime.ModeLocal, status.Mode)
	require.Equal(t, 1, provider.callCount)
	require.Len(t, provider.requests, 1)
	require.Equal(t, stableTools, provider.requests[0].Tools,
		"manual compaction must retain the session-frozen tool surface")
	require.Equal(t, "none", provider.requests[0].Metadata["tool_choice"])

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.History, 3)
	require.Equal(t, "compaction", updated.History[1].Metadata["context_stage"])
	require.Equal(t, "user", updated.History[2].Role)
	require.Equal(t, 0, runtimeSessionObservedTokenUsage(updated))
}

func TestSessionActorSubmitPrompt_EmitsLimitNoticeToSessionAndEvents(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-step-limit-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-step-limit-model",
		responses: []*llm.LLMResponse{
			{
				Content: "先调用工具。",
				Model:   "test-step-limit-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_limit",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello"},
					},
				},
			},
			{
				Content: "这条回复不应出现。",
				Model:   "test-step-limit-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-step-limit-test",
		Model:        provider.Name(),
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}, &runMetaCapturingMCPManager{}, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	result, err := actor.SubmitPrompt(ctx, "Start the flow.", &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        "team-limit",
			AgentID:       "mate-limit",
			CurrentTaskID: "task-limit",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.True(t, result.LimitReached)
	require.Contains(t, result.Output, "maxSteps=1")

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotEmpty(t, updated.History)
	require.Equal(t, "assistant", updated.History[len(updated.History)-1].Role)
	require.Equal(t, result.Output, updated.History[len(updated.History)-1].Content)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	var (
		sawAssistantMessage bool
		sawSessionEnd       bool
	)
	for _, event := range events {
		switch event.Type {
		case EventAssistantMessage:
			if content, _ := event.Payload["content"].(string); content == result.Output {
				limitReached, _ := event.Payload["limit_reached"].(bool)
				stepLimit, _ := event.Payload["step_limit"].(int)
				require.True(t, limitReached)
				require.Equal(t, 1, stepLimit)
				sawAssistantMessage = true
			}
		case EventSessionEnd:
			limitReached, _ := event.Payload["limit_reached"].(bool)
			stepLimit, _ := event.Payload["step_limit"].(int)
			success, _ := event.Payload["success"].(bool)
			if limitReached && stepLimit == 1 {
				require.False(t, success)
				sawSessionEnd = true
			}
		}
	}
	require.True(t, sawAssistantMessage)
	require.True(t, sawSessionEnd)
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

func TestSessionActorSubmitPrompt_DoesNotDirectRouteToolWorkflow(t *testing.T) {
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
		Name:     "actor-workflow-route-guard-test",
		Model:    "gpt-4",
		MaxSteps: 2,
	}, nil, runtime)
	require.NoError(t, apiAgent.RegisterSkill(&skill.Skill{
		Name:        "run_shell_command",
		Description: "Run a shell workflow",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"shell command"}, Weight: 1},
		},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "run_command",
			Tool: "shell",
			Args: map[string]interface{}{"command": "{{prompt}}"},
		}}},
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

	prompt := "Running shell commands=[3]\nCaptured output only; analyze why the agent stopped."
	result, err := actor.SubmitPrompt(ctx, prompt, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Greater(t, result.Steps, 0, "the prompt must reach the LLM loop")
	require.NotEqual(t, "run_shell_command", result.Skill)
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
	checkpointMgr.ConversationSnapshot = true
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

func TestSessionActorAnswerQuestionResumesWithFrozenToolSurfaceWithinSameTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-question-frozen-tools-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-question-frozen-tools-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I will create a team and ask a question.",
				Model:   "test-question-frozen-tools-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_spawn_team",
						Name: toolbroker.ToolSpawnTeam,
						Args: map[string]interface{}{
							"team_id":    "team-frozen-tools",
							"auto_start": false,
						},
					},
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
				Content: "Finished after question resume.",
				Model:   "test-question-frozen-tools-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-question-frozen-tools-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)
	apiAgent.SetToolBroker(&toolbroker.Broker{TeamStore: teamStore})

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
		if !state.FrozenTurnToolsSet {
			return false
		}
		questionID = state.PendingQuestion.ID
		return questionID != ""
	}, 5*time.Second, 20*time.Millisecond)

	state := actor1.State()
	require.NotNil(t, state)
	assert.True(t, state.FrozenTurnToolsSet)
	frozenNames := toolDefinitionNames(state.FrozenTurnTools)
	assert.Contains(t, frozenNames, toolbroker.ToolSpawnTeam)
	assert.NotContains(t, frozenNames, toolbroker.ToolReadTaskSpec)
	assert.NotContains(t, frozenNames, toolbroker.ToolReadTaskContext)
	assert.NotContains(t, frozenNames, toolbroker.ToolSendTeamMessage)

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
		return last.Role == "assistant" && last.Content == "Finished after question resume."
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original actor submit did not exit after cancellation")
	}

	require.Len(t, provider.requests, 2)
	firstNames := toolDefinitionNames(provider.requests[0].Tools)
	secondNames := toolDefinitionNames(provider.requests[1].Tools)
	assert.Equal(t, firstNames, secondNames)
	assert.Contains(t, secondNames, toolbroker.ToolSpawnTeam)
	assert.NotContains(t, secondNames, toolbroker.ToolReadTaskSpec)
	assert.NotContains(t, secondNames, toolbroker.ToolReadTaskContext)
	assert.NotContains(t, secondNames, toolbroker.ToolSendTeamMessage)
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

	cancel()
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original actor submit did not exit after turn cancellation")
	}
	require.Eventually(t, func() bool {
		state := actor1.State()
		return state != nil && state.Status == SessionWaitingApproval &&
			state.PendingApproval != nil && state.PendingApproval.ID == requestID &&
			state.PendingTool != nil && state.PendingTool.ToolCallID == "tool_approval" &&
			state.CurrentRunMeta != nil && state.CurrentRunMeta.Team != nil &&
			state.FrozenTurnToolsSet
	}, 2*time.Second, 20*time.Millisecond)

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

}

func TestApprovalRequestedEventPayloadIncludesRunMetaRouteAndPermission(t *testing.T) {
	state := &RuntimeState{
		CurrentRunMeta: &team.RunMeta{
			PermissionMode: string(runtimepolicy.ModeBypassPermissions),
			Team: &team.TeamRunMeta{
				TeamID:               "team-approval",
				AgentID:              "mate-approval",
				CurrentTaskID:        "task-approval",
				Difficulty:           "hard",
				DifficultySource:     "classifier",
				DifficultyRationale:  "requires parallel edits",
				RouteProvider:        "openai",
				RouteModel:           "gpt-test",
				RouteReasoningEffort: "high",
				RouteSource:          "difficulty_level",
				RouteWarnings:        []string{"provider_fallback_parent"},
				RouteFallbackUsed:    true,
				RouteFallbackReason:  "primary unavailable",
			},
		},
	}
	pending := &ApprovalRequest{ToolCallID: "tool_approval"}
	payload := approvalRequestedEventPayload(state, pending, runtimepolicy.ApprovalRequest{
		ID:         "approval-1",
		ToolCallID: "tool_approval",
		ToolName:   "team_echo",
		Reason:     "manual approval",
		RiskLevel:  "medium",
	})

	require.Equal(t, "approval-1", payload["request_id"])
	require.Equal(t, "tool_approval", payload["tool_call_id"])
	require.Equal(t, "team_echo", payload["tool_name"])
	require.Equal(t, "manual approval", payload["reason"])
	require.Equal(t, "medium", payload["risk_level"])
	require.Equal(t, string(runtimepolicy.ModeBypassPermissions), payload["permission_mode"])
	require.Equal(t, "team-approval", payload["team_id"])
	require.Equal(t, "mate-approval", payload["agent_id"])
	require.Equal(t, "mate-approval", payload["teammate_id"])
	require.Equal(t, "task-approval", payload["task_id"])
	require.Equal(t, "hard", payload["difficulty"])
	require.Equal(t, "classifier", payload["difficulty_source"])
	require.Equal(t, "requires parallel edits", payload["difficulty_rationale"])
	require.Equal(t, "openai", payload["route_provider"])
	require.Equal(t, "gpt-test", payload["route_model"])
	require.Equal(t, "high", payload["route_reasoning_effort"])
	require.Equal(t, "difficulty_level", payload["route_source"])
	require.Equal(t, []string{"provider_fallback_parent"}, payload["route_warnings"])
	require.Equal(t, true, payload["fallback_used"])
	require.Equal(t, "primary unavailable", payload["fallback_reason"])
}

func TestApprovalResolvedEventPayloadIncludesRunMetaRouteAndPermission(t *testing.T) {
	state := &RuntimeState{
		PendingApproval: &ApprovalRequest{
			ToolCallID: "tool_approval",
			ToolName:   "team_echo",
		},
		CurrentRunMeta: &team.RunMeta{
			PermissionMode: string(runtimepolicy.ModeBypassPermissions),
			Team: &team.TeamRunMeta{
				TeamID:               "team-approval",
				AgentID:              "mate-approval",
				CurrentTaskID:        "task-approval",
				Difficulty:           "expert",
				RouteProvider:        "openai",
				RouteModel:           "gpt-test",
				RouteReasoningEffort: "high",
				RouteSource:          "difficulty_level",
				RouteWarnings:        []string{"provider_fallback_parent"},
				RouteFallbackUsed:    true,
				RouteFallbackReason:  "primary unavailable",
			},
		},
	}
	payload := approvalResolvedEventPayload(state, "approval-1", true)

	require.Equal(t, "approval-1", payload["request_id"])
	require.Equal(t, true, payload["allowed"])
	require.Equal(t, "tool_approval", payload["tool_call_id"])
	require.Equal(t, "team_echo", payload["tool_name"])
	require.Equal(t, string(runtimepolicy.ModeBypassPermissions), payload["permission_mode"])
	require.Equal(t, "team-approval", payload["team_id"])
	require.Equal(t, "mate-approval", payload["agent_id"])
	require.Equal(t, "mate-approval", payload["teammate_id"])
	require.Equal(t, "task-approval", payload["task_id"])
	require.Equal(t, "expert", payload["difficulty"])
	require.Equal(t, "openai", payload["route_provider"])
	require.Equal(t, "gpt-test", payload["route_model"])
	require.Equal(t, "high", payload["route_reasoning_effort"])
	require.Equal(t, "difficulty_level", payload["route_source"])
	require.Equal(t, []string{"provider_fallback_parent"}, payload["route_warnings"])
	require.Equal(t, true, payload["fallback_used"])
	require.Equal(t, "primary unavailable", payload["fallback_reason"])
}

func TestSessionActorApproveToolRecoversRemainingSiblingCallsBeforeResumingModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-multi-pending-approval-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-multi-pending-approval-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I need approval for the first tool.",
				Model:   "test-multi-pending-approval-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_approval_a",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello-a"},
					},
					{
						ID:   "tool_approval_b",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello-b"},
					},
				},
			},
			{Content: "Finished after multi-tool recovery.", Model: "test-multi-pending-approval-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &simpleEchoMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-multi-pending-approval-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)
	apiAgent.SetPermissionEngine(&agent.PermissionEngine{
		Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
			if req.ToolCallID == "tool_approval_a" {
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

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := actor1.SubmitPrompt(ctx, "Start approval flow.", nil)
		submitDone <- submitErr
	}()

	var requestID string
	require.Eventually(t, func() bool {
		state := actor1.State()
		if state == nil || state.PendingApproval == nil || state.PendingTool == nil {
			return false
		}
		if state.Status != SessionWaitingApproval || state.PendingTool.ToolCallID != "tool_approval_a" {
			return false
		}
		requestID = state.PendingApproval.ID
		return requestID != ""
	}, 5*time.Second, 20*time.Millisecond)

	state := actor1.State()
	require.NotNil(t, state)
	require.NotNil(t, state.PendingTool)
	require.Len(t, state.PendingTool.BatchToolCalls, 2)

	pendingSession, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, pendingSession)
	require.Len(t, pendingSession.History, 2)
	require.True(t, pendingSession.History[1].HasToolCalls())
	require.Len(t, pendingSession.History[1].ToolCalls, 2)

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
		if len(messages) < 5 {
			return false
		}
		if messages[2].Role != "tool" || messages[2].ToolCallID != "tool_approval_a" {
			return false
		}
		if messages[3].Role != "tool" || messages[3].ToolCallID != "tool_approval_b" {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after multi-tool recovery."
	}, 5*time.Second, 20*time.Millisecond)

	updated, err := storage.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Len(t, updated.History, 5)
	require.Equal(t, "tool_approval_a", updated.History[2].ToolCallID)
	require.Contains(t, updated.History[2].Content, "echo: hello-a")
	require.Equal(t, "tool_approval_b", updated.History[3].ToolCallID)
	require.Contains(t, updated.History[3].Content, "echo: hello-b")
	require.Equal(t, 2, mcpManager.callCount)
	require.Equal(t, []string{"hello-a", "hello-b"}, mcpManager.messages)

	require.Len(t, provider.requests, 2)
	require.Len(t, provider.requests[1].Messages, 5)
	require.Equal(t, "system", provider.requests[1].Messages[0].Role)
	require.True(t, provider.requests[1].Messages[2].HasToolCalls())
	require.Len(t, provider.requests[1].Messages[2].ToolCalls, 2)
	require.Equal(t, "tool_approval_a", provider.requests[1].Messages[3].ToolCallID)
	require.Contains(t, provider.requests[1].Messages[3].Content, "echo: hello-a")
	require.Equal(t, "tool_approval_b", provider.requests[1].Messages[4].ToolCallID)
	require.Contains(t, provider.requests[1].Messages[4].Content, "echo: hello-b")
	require.NotContains(t, provider.requests[1].Messages[4].Content, "pending-tool recovery")

	cancel()
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original actor submit did not exit after cancellation")
	}
}

func TestSessionActorApproveToolPersistsEarlierSiblingResultsBeforePendingPause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-multi-pending-second-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-multi-pending-second-model",
		responses: []*llm.LLMResponse{
			{
				Content: "The second tool needs approval.",
				Model:   "test-multi-pending-second-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_before",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "before"},
					},
					{
						ID:   "tool_pending",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "pending"},
					},
				},
			},
			{Content: "Finished after pending-second recovery.", Model: "test-multi-pending-second-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &simpleEchoMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-multi-pending-second-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)
	apiAgent.SetPermissionEngine(&agent.PermissionEngine{
		Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
			if req.ToolCallID == "tool_pending" {
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

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := actor1.SubmitPrompt(ctx, "Start approval flow.", nil)
		submitDone <- submitErr
	}()

	var requestID string
	require.Eventually(t, func() bool {
		state := actor1.State()
		if state == nil || state.PendingApproval == nil || state.PendingTool == nil {
			return false
		}
		if state.Status != SessionWaitingApproval || state.PendingTool.ToolCallID != "tool_pending" {
			return false
		}
		requestID = state.PendingApproval.ID
		return requestID != ""
	}, 5*time.Second, 20*time.Millisecond)

	state := actor1.State()
	require.NotNil(t, state)
	require.NotNil(t, state.PendingTool)
	require.Len(t, state.PendingTool.BatchToolCalls, 2)

	pendingSession, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, pendingSession)
	require.Len(t, pendingSession.History, 3)
	require.True(t, pendingSession.History[1].HasToolCalls())
	require.Len(t, pendingSession.History[1].ToolCalls, 2)
	require.Equal(t, "tool_before", pendingSession.History[2].ToolCallID)
	require.Contains(t, pendingSession.History[2].Content, "echo: before")

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
		if len(messages) < 5 {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after pending-second recovery."
	}, 5*time.Second, 20*time.Millisecond)

	updated, err := storage.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Len(t, updated.History, 5)
	require.Equal(t, "tool_before", updated.History[2].ToolCallID)
	require.Contains(t, updated.History[2].Content, "echo: before")
	require.Equal(t, "tool_pending", updated.History[3].ToolCallID)
	require.Contains(t, updated.History[3].Content, "echo: pending")
	require.Equal(t, 2, mcpManager.callCount)
	require.Equal(t, []string{"before", "pending"}, mcpManager.messages)

	require.Len(t, provider.requests, 2)
	require.Len(t, provider.requests[1].Messages, 5)
	require.Equal(t, "system", provider.requests[1].Messages[0].Role)
	require.True(t, provider.requests[1].Messages[2].HasToolCalls())
	require.Len(t, provider.requests[1].Messages[2].ToolCalls, 2)
	require.Equal(t, "tool_before", provider.requests[1].Messages[3].ToolCallID)
	require.Contains(t, provider.requests[1].Messages[3].Content, "echo: before")
	require.Equal(t, "tool_pending", provider.requests[1].Messages[4].ToolCallID)
	require.Contains(t, provider.requests[1].Messages[4].Content, "echo: pending")
	require.NotContains(t, provider.requests[1].Messages[4].Content, "pending-tool recovery")

	cancel()
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original actor submit did not exit after cancellation")
	}
}

func TestSessionActorApproveToolRecoversBatchFromSessionWhenPendingStateHasNoBatch(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)
	session.AddMessage(*types.NewUserMessage("Start approval flow."))
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{
		{
			ID:   "tool_legacy_a",
			Name: "team_echo",
			Args: map[string]interface{}{"message": "legacy-a"},
		},
		{
			ID:   "tool_legacy_b",
			Name: "team_echo",
			Args: map[string]interface{}{"message": "legacy-b"},
		},
	}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-legacy-pending-batch-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-legacy-pending-batch-model",
		responses: []*llm.LLMResponse{
			{Content: "Finished after legacy pending recovery.", Model: "test-legacy-pending-batch-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &simpleEchoMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-legacy-pending-batch-test",
		Model:        provider.Name(),
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionWaitingApproval,
		CurrentTurnID: "turn_legacy_pending_batch",
		PendingTool: &PendingToolInvocation{
			ToolCallID: "tool_legacy_a",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"legacy-a"}`),
			CreatedAt:  time.Now().UTC(),
		},
		PendingApproval: &ApprovalRequest{
			ID:         "tool_legacy_a",
			SessionID:  session.ID,
			ToolCallID: "tool_legacy_a",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"legacy-a"}`),
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

	require.NoError(t, actor.ApproveTool(context.Background(), "tool_legacy_a", true))

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
		if len(messages) < 5 {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && last.Content == "Finished after legacy pending recovery."
	}, 5*time.Second, 20*time.Millisecond)

	updated, err := storage.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Len(t, updated.History, 5)
	require.Equal(t, "tool_legacy_a", updated.History[2].ToolCallID)
	require.Contains(t, updated.History[2].Content, "echo: legacy-a")
	require.Equal(t, "tool_legacy_b", updated.History[3].ToolCallID)
	require.Contains(t, updated.History[3].Content, "echo: legacy-b")
	require.Equal(t, 2, mcpManager.callCount)
	require.Equal(t, []string{"legacy-a", "legacy-b"}, mcpManager.messages)

	require.Len(t, provider.requests, 1)
	require.Len(t, provider.requests[0].Messages, 5)
	require.Equal(t, "system", provider.requests[0].Messages[0].Role)
	require.True(t, provider.requests[0].Messages[2].HasToolCalls())
	require.Len(t, provider.requests[0].Messages[2].ToolCalls, 2)
	require.Equal(t, "tool_legacy_a", provider.requests[0].Messages[3].ToolCallID)
	require.Contains(t, provider.requests[0].Messages[3].Content, "echo: legacy-a")
	require.Equal(t, "tool_legacy_b", provider.requests[0].Messages[4].ToolCallID)
	require.Contains(t, provider.requests[0].Messages[4].Content, "echo: legacy-b")
	require.NotContains(t, provider.requests[0].Messages[4].Content, "pending-tool recovery")
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

func TestPublishToolReceiptEventStoresOnlyMessageDiagnostics(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	actor := &SessionActor{id: "receipt-event-session", eventStore: store}
	messageJSON := []byte(`{"role":"tool","content":"large result","tool_call_id":"call-1"}`)

	actor.publishToolReceiptEvent(EventToolReceiptRecorded, "trace-1", "receipt_store", ToolExecutionReceipt{
		SessionID:   actor.id,
		ToolCallID:  "call-1",
		ToolName:    "shell",
		MessageJSON: messageJSON,
		CreatedAt:   time.Now().UTC(),
	})

	events, err := store.ListEvents(context.Background(), actor.id, 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	receiptPayload, ok := events[0].Payload["receipt"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, receiptPayload, "message_json")
	assert.Equal(t, len(messageJSON), receiptPayload["message_bytes"])
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(messageJSON)), receiptPayload["message_sha256"])
	assert.Equal(t, "call-1", receiptPayload["tool_call_id"])
	assert.Equal(t, "shell", receiptPayload["tool_name"])
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

func toolDefinitionNames(defs []types.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}
