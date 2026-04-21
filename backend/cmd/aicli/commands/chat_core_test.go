package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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

type recordingProtocolAdapter struct {
	builtConfig  adapter.RequestConfig
	response     map[string]interface{}
	responseBody io.Reader
}

func (f *fakeChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	f.called = true
	f.prompt = prompt
	f.session = session
	return f.output, f.err
}

func (a *recordingProtocolAdapter) Name() string { return "recording" }

func (a *recordingProtocolAdapter) BuildRequest(config adapter.RequestConfig) map[string]interface{} {
	a.builtConfig = config
	return map[string]interface{}{
		"model":    config.Model,
		"messages": config.Messages,
		"stream":   config.Stream,
	}
}

func (a *recordingProtocolAdapter) BuildHeaders(adapter.AdapterConfig) map[string]string {
	return map[string]string{"Content-Type": "application/json"}
}

func (a *recordingProtocolAdapter) ExtractResponse(result map[string]interface{}) string { return "" }

func (a *recordingProtocolAdapter) ExtractReasoning(result map[string]interface{}) string { return "" }

func (a *recordingProtocolAdapter) ExtractStreamContent(result map[string]interface{}) string {
	return ""
}

func (a *recordingProtocolAdapter) ExtractStreamReasoning(result map[string]interface{}) string {
	return ""
}

func (a *recordingProtocolAdapter) BuildAssistantMessage(content string, toolCalls []map[string]interface{}, reasoning string) map[string]interface{} {
	return map[string]interface{}{"role": "assistant", "content": content}
}

func (a *recordingProtocolAdapter) ExtractToolCallsFromRawCalls(rawCalls []map[string]interface{}) []adapter.ToolCall {
	return nil
}

func (a *recordingProtocolAdapter) HandleResponse(isStream bool, respBody io.Reader, callbacks adapter.StreamCallbacks) (map[string]interface{}, error) {
	if a.response != nil {
		return a.response, nil
	}
	return map[string]interface{}{"role": "assistant", "content": "ok"}, nil
}

func (a *recordingProtocolAdapter) ProcessResponse(result map[string]interface{}) adapter.ProcessResult {
	return adapter.ProcessResult{}
}

func (a *recordingProtocolAdapter) IsReasoningModel(model string) bool { return false }

func (a *recordingProtocolAdapter) GetAPIPath() string { return "/v1/chat/completions" }

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

func TestAICLIProviderTurnExecutor_UsesSanitizedProtocolMessagesForSharedReplay(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	recordingAdapter := &recordingProtocolAdapter{
		response: map[string]interface{}{
			"role":    "assistant",
			"content": "done",
		},
	}

	session := &ChatSession{
		ProviderName:  "nvidia",
		Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
		Adapter:       recordingAdapter,
		Model:         "z-ai/glm4.7",
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		cancelCtx:     context.Background(),
		NoInteractive: true,
	}

	assistantMsg := types.Message{
		Role:    "assistant",
		Content: "我来查看当前目录。",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "ls"},
		},
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistantMsg.Metadata, &types.ReasoningBlock{
		Format:     "openai_compatible",
		Summary:    "先看目录。",
		Streamable: true,
		Visibility: types.ReasoningVisibilitySummary,
	})
	assistantMsg.Metadata.Set(chatRuntimeMessageRawJSONKey, `{"role":"assistant","content":"我来查看当前目录。","metadata":{"reasoning_details":{"summary":"raw reasoning"}},"tool_calls":[{"id":"call_1","type":"function","function":{"name":"ls","arguments":"{\"raw\":true}"}}]}`)

	toolMsg := types.Message{
		Role:       "tool",
		Content:    "目录: .",
		ToolCallID: "call_1",
		Metadata:   types.NewMetadata(),
	}
	toolMsg.Metadata["artifact_refs"] = []string{"art_1"}
	toolMsg.Metadata.Set(chatRuntimeMessageRawJSONKey, `{"role":"tool","content":"目录: .","tool_call_id":"call_1","metadata":{"artifact_refs":["art_1"]}}`)

	executor := &aicliProviderTurnExecutor{session: session}
	turn, err := executor.Complete(context.Background(), runtimechatcore.ProviderTurnRequest{
		Messages: []types.Message{assistantMsg, toolMsg},
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if turn == nil || turn.Message == nil || turn.Message.Content != "done" {
		t.Fatalf("unexpected turn response: %#v", turn)
	}

	messages, ok := capturedBody["messages"].([]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("expected 2 request messages, got %#v", capturedBody["messages"])
	}

	assistantPayload, ok := messages[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected assistant payload map, got %T", messages[0])
	}
	if _, exists := assistantPayload["metadata"]; exists {
		t.Fatalf("did not expect raw metadata in assistant replay message: %#v", assistantPayload)
	}
	if _, exists := assistantPayload["reasoning_content"]; exists {
		t.Fatalf("did not expect reasoning_content in openai replay message: %#v", assistantPayload)
	}
	toolCalls, ok := assistantPayload["tool_calls"].([]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call in assistant replay message, got %#v", assistantPayload["tool_calls"])
	}
	firstCall, ok := toolCalls[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool call map, got %T", toolCalls[0])
	}
	functionPayload, ok := firstCall["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload map, got %#v", firstCall["function"])
	}
	if got := functionPayload["arguments"]; got != "{}" {
		t.Fatalf("expected empty tool args to normalize to {}, got %#v", got)
	}

	toolPayload, ok := messages[1].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool payload map, got %T", messages[1])
	}
	if _, exists := toolPayload["metadata"]; exists {
		t.Fatalf("did not expect raw metadata in tool replay message: %#v", toolPayload)
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
	session.runtimeHTTPCapture.SetArtifactDir(t.TempDir())
	session.runtimeHTTPCapture.RecordArtifactPath("request", "E:\\logs\\001_request_gateway_client.json")
	session.runtimeHTTPCapture.RecordArtifactPath("response", "E:\\logs\\001_response_gateway_client.json")
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
	if !strings.Contains(err.Error(), "request_artifact=E:\\logs\\001_request_gateway_client.json") {
		t.Fatalf("expected request artifact path in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "response_artifact=E:\\logs\\001_response_gateway_client.json") {
		t.Fatalf("expected response artifact path in error, got %v", err)
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

func TestFallbackActorExecutorResponse_UsesResultErrorWhenNoOutput(t *testing.T) {
	got := fallbackActorExecutorResponse(&agent.Result{
		Success: false,
		Error:   `搜索失败: Get "https://html.duckduckgo.com/html/?q=%E5%A4%A9%E6%B0%94%E9%A2%84%E6%8A%A5": dial tcp 31.13.94.36:443: connectex: A connection attempt failed`,
	})
	if !strings.Contains(got, "这次处理没有生成后续回复。") {
		t.Fatalf("expected fallback preface, got %q", got)
	}
	if !strings.Contains(got, "搜索失败: Get") {
		t.Fatalf("expected fallback to include original error, got %q", got)
	}
	if !strings.Contains(got, "请根据上面的信息重试") {
		t.Fatalf("expected retry hint, got %q", got)
	}
}

func TestFallbackActorExecutorResponse_IgnoresSuccessfulOrNonEmptyOutput(t *testing.T) {
	if got := fallbackActorExecutorResponse(&agent.Result{Success: true, Error: "tool failed"}); got != "" {
		t.Fatalf("expected empty fallback for successful result, got %q", got)
	}
	if got := fallbackActorExecutorResponse(&agent.Result{Success: false, Output: "已有输出", Error: "tool failed"}); got != "" {
		t.Fatalf("expected empty fallback when output already exists, got %q", got)
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

func TestAICLIEventRenderer_StreamsReasoningBeforeResult(t *testing.T) {
	session := &ChatSession{Stream: true}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.liveStreamFn = func() bool { return true }
	session.Interaction.streamRuneDelay = 0
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)
	renderer := newAICLIEventRenderer(session)

	renderer.Handle(runtimechatcore.ChatEvent{
		Type:    runtimechatcore.EventPlanning,
		Content: "先看目录。",
	})
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:    runtimechatcore.EventResult,
		Content: "我来查看。",
	})
	renderer.Finalize(&runtimechatcore.ChatResult{Output: "我来查看。"}, nil)

	rendered := output.String()
	reasoningIndex := strings.Index(rendered, "先看目录。")
	contentIndex := strings.Index(rendered, "我来查看。")
	if reasoningIndex == -1 || contentIndex == -1 {
		t.Fatalf("expected reasoning and content in output, got %q", rendered)
	}
	if reasoningIndex > contentIndex {
		t.Fatalf("expected reasoning before content, got %q", rendered)
	}
	if !strings.Contains(rendered, chatToolDivider("reasoning")) || !strings.Contains(rendered, chatToolDivider("end reasoning")) {
		t.Fatalf("expected unified reasoning block, got %q", rendered)
	}
	if strings.Contains(rendered, "--- Thinking ---") || strings.Contains(rendered, "--- End Thinking ---") {
		t.Fatalf("expected legacy reasoning markers to be removed, got %q", rendered)
	}
}

func TestAICLIEventRenderer_Finalize_NonStreamRendersReasoningBeforeContentWithInteraction(t *testing.T) {
	session := &ChatSession{Stream: false}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	renderer := newAICLIEventRenderer(session)
	finalMessage := &types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(finalMessage.Metadata, &types.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "openai_compatible",
		Summary:    "先输出 reasoning，再输出正文。",
		Visibility: types.ReasoningVisibilitySummary,
	})
	finalMessage.Metadata.Set(chatcoreReasoningMetadataKey, "先输出 reasoning，再输出正文。")

	renderer.Finalize(&runtimechatcore.ChatResult{Output: "Hello!"}, finalMessage)
	renderChatResponse(session, "Hello!")

	rendered := output.String()
	reasoningIndex := strings.Index(rendered, "先输出 reasoning，再输出正文。")
	contentIndex := strings.Index(rendered, "Hello!")
	if reasoningIndex == -1 || contentIndex == -1 {
		t.Fatalf("expected reasoning and content in output, got %q", rendered)
	}
	if reasoningIndex > contentIndex {
		t.Fatalf("expected reasoning before content, got %q", rendered)
	}
	if !strings.Contains(rendered, chatToolDivider("reasoning")) {
		t.Fatalf("expected unified reasoning divider, got %q", rendered)
	}
	if strings.Contains(rendered, "--- Thinking ---") {
		t.Fatalf("expected non-stream path to use unified reasoning render, got %q", rendered)
	}
}

func TestAICLIEventRenderer_ToolBatchUsesUnifiedInteractionRendering(t *testing.T) {
	session := &ChatSession{Stream: true}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	renderer := newAICLIEventRenderer(session)
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:    runtimechatcore.EventResult,
		Content: "先说明一下。",
	})
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:  runtimechatcore.EventTool,
		Stage: "batch_start",
		Metadata: map[string]interface{}{
			"call_count": 1,
		},
	})
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:     runtimechatcore.EventTool,
		Stage:    "tool_result",
		ToolName: "ls",
		Arguments: map[string]interface{}{
			"path": "docs",
		},
		Output:  "目录: docs\n📁 aicli/ · 📁 architecture/\n统计: 0 个文件, 2 个目录",
		Success: true,
	})
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:  runtimechatcore.EventTool,
		Stage: "batch_end",
		Metadata: map[string]interface{}{
			"success_count": 1,
			"error_count":   0,
		},
	})
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:    runtimechatcore.EventResult,
		Content: "最终答案。",
	})
	renderer.Finalize(&runtimechatcore.ChatResult{Output: "最终答案。"}, nil)

	rendered := output.String()
	preludeIndex := strings.Index(rendered, "先说明一下。")
	batchStartIndex := strings.Index(rendered, chatToolDivider("command start"))
	toolDoneIndex := strings.Index(rendered, "[tool done] ls path=docs")
	batchEndIndex := strings.LastIndex(rendered, chatToolDivider("command end"))
	waitingIndex := strings.Index(rendered, "[thinking] 等待中...")
	finalIndex := strings.Index(rendered, "最终答案。")
	if preludeIndex == -1 || batchStartIndex == -1 || toolDoneIndex == -1 || batchEndIndex == -1 || waitingIndex == -1 || finalIndex == -1 {
		t.Fatalf("expected buffered content, tool batch, waiting hint, and final answer in output, got %q", rendered)
	}
	if !(preludeIndex < batchStartIndex && batchStartIndex < toolDoneIndex && toolDoneIndex < batchEndIndex && batchEndIndex < waitingIndex && waitingIndex < finalIndex) {
		t.Fatalf("expected tool batch rendering order to stay stable, got %q", rendered)
	}
	if strings.Count(rendered, "先说明一下。") != 1 {
		t.Fatalf("expected pre-tool assistant content once, got %q", rendered)
	}
	if strings.Count(rendered, "最终答案。") != 1 {
		t.Fatalf("expected final assistant content once, got %q", rendered)
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
