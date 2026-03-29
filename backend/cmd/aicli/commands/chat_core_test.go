package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeChatExecutor struct {
	called  bool
	prompt  string
	session *ChatSession
	output  string
	err     error
}

func (f *fakeChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	f.called = true
	f.prompt = prompt
	f.session = session
	return f.output, f.err
}

func TestSendMessage_DelegatesToSharedChatExecutor(t *testing.T) {
	executor := &fakeChatExecutor{output: "shared core response"}
	session := &ChatSession{
		Provider:      config.Provider{Protocol: "codex"},
		cancelCtx:     context.Background(),
		ChatExecutor:  executor,
		NoInteractive: true,
	}

	response, err := sendMessage(session, "hello from cli")
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if response != "shared core response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if !executor.called {
		t.Fatal("expected sendMessage to delegate to ChatExecutor")
	}
	if executor.prompt != "hello from cli" {
		t.Fatalf("unexpected delegated prompt: %q", executor.prompt)
	}
	if executor.session != session {
		t.Fatalf("expected session passthrough")
	}
}

func TestAICLISharedChatExecutor_SyncsRuntimeSessionFromSharedCoreHistory(t *testing.T) {
	originalExecute := executeToolLoop
	defer func() {
		executeToolLoop = originalExecute
	}()

	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		return &runtimechatcore.ToolLoopResult{
			History: []types.Message{
				*types.NewSystemMessage("You are helpful."),
				*types.NewUserMessage("hello"),
				*types.NewAssistantMessage("shared core response"),
			},
			Response: &runtimechatcore.ChatResult{
				Output: "shared core response",
			},
		}, nil
	}

	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "codex_ee",
		Provider:         config.Provider{Enabled: true, Protocol: "codex", BaseURL: "https://example.com"},
		Adapter:          &adapter.CodexAdapter{},
		Model:            "gpt-5.2-code",
		ReasoningEffort:  "medium",
		cancelCtx:        context.Background(),
		NoInteractive:    true,
		FunctionRegistry: functions.NewFunctionRegistry(),
		FunctionCatalog:  newAICLIFunctionCatalog("codex", nil),
		ChatExecutor:     newAICLISharedChatExecutor(),
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
	}

	response, err := session.ChatExecutor.Execute(context.Background(), session, "hello")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if response != "shared core response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if len(session.Messages) != 3 {
		t.Fatalf("expected session messages to be updated from shared core history, got %#v", session.Messages)
	}
	if session.RuntimeSession == nil {
		t.Fatal("expected runtime session to stay attached")
	}
	stored, err := manager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := stored.BuildPreview().MessageCount; got != 3 {
		t.Fatalf("expected persisted history count 3, got %d", got)
	}
}

func TestSendMessage_UsesActorFirstExecutorWhenLocalHostAvailable(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &staticProvider{name: "test-provider", content: "actor output"}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "actor-default-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 2,
		}, nil, llmRuntime)
		a.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy(nil, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Provider:         config.Provider{Protocol: "openai"},
		Model:            "test-model",
		cancelCtx:        context.Background(),
		NoInteractive:    true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ActorFirstReady:  true,
	}
	host.BaseSession = session

	response, err := sendMessage(session, "plan and execute this")
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if response != "actor output" {
		t.Fatalf("unexpected response: %q", response)
	}
	if got := ensureChatExecutor(session); got == nil {
		t.Fatal("expected actor executor to be installed")
	}
}

func TestSendMessage_ActorFirstEmptyTerminalResponseReturnsError(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &staticProvider{name: "test-provider", content: ""}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "actor-empty-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 2,
		}, nil, llmRuntime)
		a.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy(nil, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Provider:         config.Provider{Protocol: "openai"},
		Model:            "test-model",
		cancelCtx:        context.Background(),
		NoInteractive:    true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ActorFirstReady:  true,
	}
	host.BaseSession = session

	response, err := sendMessage(session, "trigger empty response")
	if err == nil {
		t.Fatal("expected actor-first empty response to fail")
	}
	if response != "" {
		t.Fatalf("expected empty response on error, got %q", response)
	}
	if !strings.Contains(err.Error(), "上游模型返回了空回复") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHumanizeActorExecutorError_AppendsRuntimeHTTPPreview(t *testing.T) {
	session := &ChatSession{runtimeHTTPCapture: &chatRuntimeHTTPCapture{}}
	session.runtimeHTTPCapture.Record(runtimellm.HTTPDebugEvent{
		Source:              "gateway_client",
		Provider:            "codex_ee",
		Protocol:            "codex",
		Model:               "gpt-5.4",
		ResponseStatusCode:  200,
		ResponseBodyPreview: `{"id":"resp_1","output":[{"type":"message"}]}`,
	})

	err := humanizeActorExecutorError(session, fmt.Errorf("upstream model returned an empty reply: no text and no tool calls"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "response_preview=") || !strings.Contains(err.Error(), `"resp_1"`) {
		t.Fatalf("expected runtime HTTP preview in error, got %v", err)
	}
}

func TestAICLISharedChatExecutor_RemainsAvailableAsFallback(t *testing.T) {
	originalExecute := executeToolLoop
	defer func() {
		executeToolLoop = originalExecute
	}()
	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		return &runtimechatcore.ToolLoopResult{
			History: []types.Message{
				*types.NewUserMessage("hello"),
				*types.NewAssistantMessage("legacy output"),
			},
			Response: &runtimechatcore.ChatResult{Output: "legacy output"},
		}, nil
	}

	session := &ChatSession{
		ProviderName:     "codex_ee",
		Provider:         config.Provider{Enabled: true, Protocol: "codex", BaseURL: "https://example.com"},
		Adapter:          &adapter.CodexAdapter{},
		Model:            "gpt-5.2-code",
		ReasoningEffort:  "medium",
		cancelCtx:        context.Background(),
		NoInteractive:    true,
		FunctionRegistry: functions.NewFunctionRegistry(),
		FunctionCatalog:  newAICLIFunctionCatalog("codex", nil),
	}

	response, err := sendMessage(session, "hello")
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if response != "legacy output" {
		t.Fatalf("unexpected response: %q", response)
	}
}

func TestActorFirstSession_DoesNotFallbackAfterFirstTurn(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &staticProvider{name: "test-provider", content: "actor output"}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "actor-default-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 2,
		}, nil, llmRuntime)
		a.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy(nil, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Provider:         config.Provider{Protocol: "openai"},
		Model:            "test-model",
		cancelCtx:        context.Background(),
		NoInteractive:    true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ActorFirstReady:  true,
	}
	host.BaseSession = session

	if _, err := sendMessage(session, "create a team"); err != nil {
		t.Fatalf("first sendMessage failed: %v", err)
	}

	session.ChatExecutor = nil
	session.LocalRuntimeHost = nil
	_, err = sendMessage(session, "continue on the active team")
	if err == nil {
		t.Fatal("expected actor-first session without host to error")
	}
	if err.Error() == "legacy output" || err.Error() == "shared core response" {
		t.Fatalf("unexpected legacy fallback: %v", err)
	}
}

func TestResolveActorExecutorResponse_FallsBackToSyncedAssistantMessage(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{
			History: []types.Message{
				*types.NewUserMessage("create a team"),
				*types.NewAssistantMessage("latest synced answer"),
			},
		},
	}

	got := resolveActorExecutorResponse("", session, "")
	if got != "latest synced answer" {
		t.Fatalf("expected synced assistant fallback, got %q", got)
	}
}

func TestResolveActorExecutorResponse_DoesNotRepeatPreviousAssistantMessage(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{
			History: []types.Message{
				*types.NewUserMessage("create a team"),
				*types.NewAssistantMessage("same answer"),
			},
		},
	}

	got := resolveActorExecutorResponse("", session, "same answer")
	if got != "" {
		t.Fatalf("expected empty fallback for unchanged assistant message, got %q", got)
	}
}

func TestResolveActorExecutorResponse_PreservesProgrammaticOutputAfterRenderedFinalOutput(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{
			ID: "session-1",
			History: []types.Message{
				*types.NewUserMessage("create a team"),
				*types.NewAssistantMessage("streamed answer"),
			},
		},
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"delta": "streamed answer"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"content": "streamed answer"},
	})

	got := resolveActorExecutorResponse("streamed answer", session, "")
	if got != "streamed answer" {
		t.Fatalf("expected actor response text to remain available for programmatic callers, got %q", got)
	}
}

func TestShouldDisplayActorStreamFallback_OnlyForActorExecutor(t *testing.T) {
	if shouldDisplayActorStreamFallback(&ChatSession{Stream: true, ChatExecutor: &aicliActorChatExecutor{}}) != true {
		t.Fatal("expected actor executor stream fallback")
	}
	if shouldDisplayActorStreamFallback(&ChatSession{Stream: true, ChatExecutor: &aicliSharedChatExecutor{}}) {
		t.Fatal("expected shared executor stream to suppress final fallback")
	}
}

type staticProvider struct {
	name    string
	content string
}

func (p *staticProvider) Name() string { return p.name }
func (p *staticProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	return &runtimellm.LLMResponse{Content: p.content, Model: req.Model}, nil
}
func (p *staticProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk)
	close(ch)
	return ch, nil
}
func (p *staticProvider) CountTokens(text string) int { return len(text) }
func (p *staticProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{}
}
func (p *staticProvider) CheckHealth(ctx context.Context) error { return nil }
