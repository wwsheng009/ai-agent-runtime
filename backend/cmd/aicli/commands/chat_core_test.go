package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
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
	if got := assistantPayload["reasoning_content"]; got != "先看目录。" {
		t.Fatalf("expected reasoning_content in openai replay message, got %#v", got)
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

func TestAICLIProviderTurnExecutor_ReplaysDeepSeekReasoningContentAfterToolTurns(t *testing.T) {
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
		ProviderName:  "",
		Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
		Adapter:       recordingAdapter,
		Model:         "deepseek-v4-flash",
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		cancelCtx:     context.Background(),
		NoInteractive: true,
	}

	assistantToolMsg := types.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "ls"},
		},
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistantToolMsg.Metadata, &types.ReasoningBlock{
		Provider:   "deepseek",
		Format:     "openai_compatible",
		Summary:    "Let me inspect the workspace first.",
		Streamable: true,
		Visibility: types.ReasoningVisibilitySummary,
	})

	toolMsg := types.Message{
		Role:       "tool",
		Content:    "目录: .",
		ToolCallID: "call_1",
		Metadata:   types.NewMetadata(),
	}

	assistantSummaryMsg := types.Message{
		Role:     "assistant",
		Content:  "当前目录已经确认。",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistantSummaryMsg.Metadata, &types.ReasoningBlock{
		Provider:   "deepseek",
		Format:     "openai_compatible",
		Summary:    "Now I can see the directory structure. Let me show this to the user.",
		Streamable: true,
		Visibility: types.ReasoningVisibilitySummary,
	})

	userMsg := types.Message{
		Role:     "user",
		Content:  "check git status",
		Metadata: types.NewMetadata(),
	}

	executor := &aicliProviderTurnExecutor{session: session}
	turn, err := executor.Complete(context.Background(), runtimechatcore.ProviderTurnRequest{
		Messages: []types.Message{assistantToolMsg, toolMsg, assistantSummaryMsg, userMsg},
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if turn == nil || turn.Message == nil || turn.Message.Content != "done" {
		t.Fatalf("unexpected turn response: %#v", turn)
	}

	messages, ok := capturedBody["messages"].([]interface{})
	if !ok || len(messages) != 4 {
		t.Fatalf("expected 4 request messages, got %#v", capturedBody["messages"])
	}

	finalAssistantPayload, ok := messages[2].(map[string]interface{})
	if !ok {
		t.Fatalf("expected final assistant payload map, got %T", messages[2])
	}
	if got := finalAssistantPayload["reasoning_content"]; got != "Now I can see the directory structure. Let me show this to the user." {
		t.Fatalf("expected deepseek final assistant reasoning_content replay, got %#v", got)
	}
}

func TestAICLIProviderTurnExecutor_ReplaysDeepSeekEmptyReasoningContentForToolCalls(t *testing.T) {
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
		ProviderName:  "deepseek",
		Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
		Adapter:       recordingAdapter,
		Model:         "deepseek-v4-flash",
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		cancelCtx:     context.Background(),
		NoInteractive: true,
	}

	assistantToolMsg := types.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}

	toolMsg := types.Message{
		Role:       "tool",
		Content:    "diff preview",
		ToolCallID: "call_view",
		Metadata:   types.NewMetadata(),
	}

	userMsg := types.Message{
		Role:     "user",
		Content:  "继续",
		Metadata: types.NewMetadata(),
	}

	executor := &aicliProviderTurnExecutor{session: session}
	turn, err := executor.Complete(context.Background(), runtimechatcore.ProviderTurnRequest{
		Messages: []types.Message{assistantToolMsg, toolMsg, userMsg},
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if turn == nil || turn.Message == nil || turn.Message.Content != "done" {
		t.Fatalf("unexpected turn response: %#v", turn)
	}

	messages, ok := capturedBody["messages"].([]interface{})
	if !ok || len(messages) != 3 {
		t.Fatalf("expected 3 request messages, got %#v", capturedBody["messages"])
	}

	assistantPayload, ok := messages[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected assistant payload map, got %T", messages[0])
	}
	if got, exists := assistantPayload["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty deepseek reasoning_content replay, got exists=%v value=%#v", exists, got)
	}
}

func TestAICLIProviderTurnExecutor_RejectsTruncatedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	recordingAdapter := &recordingProtocolAdapter{
		response: map[string]interface{}{
			"role":          "assistant",
			"content":       "",
			"finish_reason": "length",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_1",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "write",
						"arguments": `{"file_path":"E:\\projects\\ai\\ai-agent-runtime\\backend\\out.txt","content":"hello`,
					},
				},
			},
		},
	}

	session := &ChatSession{
		ProviderName:  "deepseek",
		Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
		Adapter:       recordingAdapter,
		Model:         "deepseek-v4-flash",
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		cancelCtx:     context.Background(),
		NoInteractive: true,
	}

	executor := &aicliProviderTurnExecutor{session: session}
	turn, err := executor.Complete(context.Background(), runtimechatcore.ProviderTurnRequest{
		Messages: []types.Message{
			{
				Role:     "user",
				Content:  "write a large file",
				Metadata: types.NewMetadata(),
			},
		},
	})
	if err == nil {
		t.Fatal("expected truncated tool call response to fail")
	}
	if !strings.Contains(err.Error(), "被 token 限制截断") {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn != nil {
		t.Fatalf("expected no turn response, got %#v", turn)
	}
}

func TestAICLIProviderTurnExecutor_RejectsUnclosedTruncatedToolCallMarkup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	recordingAdapter := &recordingProtocolAdapter{
		response: map[string]interface{}{
			"role":          "assistant",
			"content":       "前文内容<tool_call>write<arg_key>path</arg_key><arg_value>E:\\projects\\ai\\agent.txt",
			"finish_reason": "length",
		},
	}

	session := &ChatSession{
		ProviderName:  "deepseek",
		Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
		Adapter:       recordingAdapter,
		Model:         "deepseek-v4-flash",
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		cancelCtx:     context.Background(),
		NoInteractive: true,
	}

	executor := &aicliProviderTurnExecutor{session: session}
	turn, err := executor.Complete(context.Background(), runtimechatcore.ProviderTurnRequest{
		Messages: []types.Message{
			{
				Role:     "user",
				Content:  "write a large file",
				Metadata: types.NewMetadata(),
			},
		},
	})
	if err == nil {
		t.Fatal("expected truncated tool call markup to fail")
	}
	if !strings.Contains(err.Error(), "被 token 限制截断") {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn != nil {
		t.Fatalf("expected no turn response, got %#v", turn)
	}
}

func TestToolDefinitionsFromSelection_SortsDefinitionsByName(t *testing.T) {
	selection := &aicliFunctionSelection{
		Schemas: []map[string]interface{}{
			{"name": "write", "description": "write file", "parameters": map[string]interface{}{"type": "object"}},
			{"name": "bash", "description": "run shell", "parameters": map[string]interface{}{"type": "object"}},
			{"name": "edit", "description": "edit file", "parameters": map[string]interface{}{"type": "object"}},
		},
	}

	defs := toolDefinitionsFromSelection(selection)
	if len(defs) != 3 {
		t.Fatalf("expected 3 tool definitions, got %d", len(defs))
	}

	got := make([]string, 0, len(defs))
	for _, def := range defs {
		got = append(got, def.Name)
	}
	if joined := strings.Join(got, ","); joined != "bash,edit,write" {
		t.Fatalf("expected stable tool definition order, got %q", joined)
	}
}

func TestToolDefinitionsFromSelection_PreservesMetadata(t *testing.T) {
	selection := &aicliFunctionSelection{
		Schemas: []map[string]interface{}{
			{
				"name":        "read_task_spec",
				"description": "read task spec",
				"parameters":  map[string]interface{}{"type": "object"},
				"metadata": map[string]interface{}{
					"availability":  "requires_active_team_run",
					"defer_loading": true,
				},
			},
		},
	}

	defs := toolDefinitionsFromSelection(selection)
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(defs))
	}
	if got := defs[0].Metadata["availability"]; got != "requires_active_team_run" {
		t.Fatalf("expected availability metadata, got %#v", defs[0].Metadata)
	}
	if got := defs[0].Metadata["defer_loading"]; got != true {
		t.Fatalf("expected defer_loading metadata, got %#v", defs[0].Metadata)
	}
}

func TestToolDefinitionsToSchemas_SortsDefinitionsByName(t *testing.T) {
	defs := []types.ToolDefinition{
		{Name: "write", Description: "write file", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "bash", Description: "run shell", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "edit", Description: "edit file", Parameters: map[string]interface{}{"type": "object"}},
	}

	schemas := toolDefinitionsToSchemas(defs)
	if len(schemas) != 3 {
		t.Fatalf("expected 3 schemas, got %d", len(schemas))
	}

	got := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		name, _ := schema["name"].(string)
		got = append(got, name)
	}
	if joined := strings.Join(got, ","); joined != "bash,edit,write" {
		t.Fatalf("expected stable schema order, got %q", joined)
	}
}

func TestToolDefinitionsToSchemas_PreservesMetadata(t *testing.T) {
	defs := []types.ToolDefinition{
		{
			Name:        "read_task_spec",
			Description: "read task spec",
			Parameters:  map[string]interface{}{"type": "object"},
			Metadata: map[string]interface{}{
				"availability":  "requires_active_team_run",
				"defer_loading": true,
			},
		},
	}

	schemas := toolDefinitionsToSchemas(defs)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}
	metadata, ok := schemas[0]["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %#v", schemas[0]["metadata"])
	}
	if got := metadata["availability"]; got != "requires_active_team_run" {
		t.Fatalf("expected availability metadata, got %#v", metadata)
	}
	if got := metadata["defer_loading"]; got != true {
		t.Fatalf("expected defer_loading metadata, got %#v", metadata)
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

func TestHumanizeActorExecutorError_HumanizesPromptPreflightFailure(t *testing.T) {
	err := humanizeActorExecutorError(nil, &agent.PromptPreflightError{
		PromptTokens:     1400,
		PromptBudget:     900,
		Code:             "active_turn_not_compactable",
		Reason:           "active-turn replay cannot be compacted further",
		SuggestedAction:  "请开启新一轮对话、减少上下文，或提高预算。",
		ResolvedProvider: "test-provider",
		ResolvedModel:    "test-model",
		BudgetSource:     "context_max_prompt_tokens",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "本次请求在发送给模型前已被本地拦截") {
		t.Fatalf("expected localized preflight message, got %v", err)
	}
	if !strings.Contains(err.Error(), "provider=test-provider") || !strings.Contains(err.Error(), "model=test-model") {
		t.Fatalf("expected provider/model context in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "建议：请开启新一轮对话、减少上下文，或提高预算。") {
		t.Fatalf("expected suggested action in error, got %v", err)
	}
}

func TestHumanizeActorExecutorError_NoticesRecoveredPromptPreflightHistory(t *testing.T) {
	err := humanizeActorExecutorError(nil, &agent.PromptPreflightError{
		PromptTokens:              1400,
		PromptBudget:              900,
		Code:                      "prompt_still_exceeds_budget_after_compaction",
		Reason:                    "prompt budget still exceeded after active-turn compaction",
		SuggestedAction:           "请继续收缩上下文层、提高预算，或从新的轮次继续。",
		ResolvedProvider:          "test-provider",
		ResolvedModel:             "test-model",
		BudgetSource:              "context_max_prompt_tokens",
		ReplacementHistoryApplied: true,
		ReplacementHistory:        []types.Message{*types.NewUserMessage("继续处理"), *types.NewAssistantMessage("Compacted earlier tool replay in current turn: ...")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "当前会话已自动保存压缩后的上下文，可直接继续下一轮。") {
		t.Fatalf("expected recovery-applied hint in error, got %v", err)
	}
}

func TestRenderSharedChatWarningEvent_ContextCategoryBypassesWarningPrefix(t *testing.T) {
	event := runtimechatcore.ChatEvent{
		Type:    runtimechatcore.EventWarning,
		Content: "[context] shared auto-compact applied mode=local token 1200 -> 240 compacted_messages=6 history_messages=4",
		Metadata: map[string]interface{}{
			"category": "context",
			"name":     "shared_auto_compact",
		},
	}
	if got := renderSharedChatWarningEvent(event); got != event.Content {
		t.Fatalf("expected context event to render raw content, got %q", got)
	}
}

func TestSharedChatAutoCompactChatEvent_Applied(t *testing.T) {
	event, ok := sharedChatAutoCompactChatEvent(&sharedChatAutoCompactReport{
		Result: &compactruntime.Result{
			Mode:               compactruntime.ModeLocal,
			TokenBefore:        1200,
			TokenAfter:         240,
			CompactedMessages:  6,
			ReplacementHistory: []types.Message{*types.NewUserMessage("继续处理"), *types.NewUserMessage("Compacted context from earlier turns: ...")},
		},
		Status: compactruntime.Status{Mode: compactruntime.ModeLocal},
	}, nil)
	if !ok {
		t.Fatal("expected auto compact event")
	}
	if event.Type != runtimechatcore.EventWarning {
		t.Fatalf("expected warning event, got %#v", event)
	}
	if !strings.Contains(event.Content, "[context] shared auto-compact applied") {
		t.Fatalf("unexpected auto compact applied event: %#v", event)
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

func TestAICLIToolExecutor_ExecuteTool_PreservesMetadata(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("codex", registry)
	catalog.RegisterBuiltinToolFunction(&richTestFunction{
		testFunction: testFunction{name: "background_task"},
		metadata: map[string]interface{}{
			toolresult.SourceKey:   toolresult.SourceBroker,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}, runtimetools.ToolDescriptor{
		Name:        "background_task",
		Description: "background task",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	session := &ChatSession{
		FunctionRegistry: registry,
		FunctionCatalog:  catalog,
		cancelCtx:        context.Background(),
	}
	executor := &aicliToolExecutor{session: session}

	result := executor.ExecuteTool(context.Background(), types.ToolCall{
		ID:   "call-1",
		Name: "background_task",
		Args: map[string]interface{}{"command": "git status"},
	})

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content != "ok" {
		t.Fatalf("expected output ok, got %q", result.Content)
	}
	if got := result.Metadata[toolresult.SourceKey]; got != toolresult.SourceBroker {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceBroker, got)
	}
	if got := result.Metadata[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
}

func TestAICLIToolExecutor_ExecuteTool_PreservesMetadataOnError(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("codex", registry)
	catalog.RegisterBuiltinToolFunction(&failingRichTestFunction{
		testFunction: testFunction{name: "background_task"},
		metadata: map[string]interface{}{
			toolresult.SourceKey:   toolresult.SourceBroker,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}, runtimetools.ToolDescriptor{
		Name:        "background_task",
		Description: "background task",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	logger := NewChatLogger("codex", "openai", "test-model", false, "")
	session := &ChatSession{
		FunctionRegistry: registry,
		FunctionCatalog:  catalog,
		Logger:           logger,
		cancelCtx:        context.Background(),
	}
	executor := &aicliToolExecutor{session: session}

	result := executor.ExecuteTool(context.Background(), types.ToolCall{
		ID:   "call-err",
		Name: "background_task",
		Args: map[string]interface{}{"command": "git status"},
	})

	if result.Error == "" {
		t.Fatalf("expected error result, got %+v", result)
	}
	if got := result.Metadata[toolresult.SourceKey]; got != toolresult.SourceBroker {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceBroker, got)
	}
	if got := result.Metadata[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}

	var entry *ChatLogDetail
	for i := range logger.sessionLog.Messages {
		candidate := &logger.sessionLog.Messages[i]
		if candidate.MessageType == "tool_result" {
			entry = candidate
			break
		}
	}
	if entry == nil {
		t.Fatalf("expected tool_result log entry, got %+v", logger.sessionLog.Messages)
	}
	content, ok := entry.Content.(map[string]interface{})
	if !ok {
		t.Fatalf("expected content map, got %#v", entry.Content)
	}
	resultPayload, ok := content["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result payload map, got %#v", content["result"])
	}
	metadata, ok := resultPayload["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %#v", resultPayload["metadata"])
	}
	if got := metadata[toolresult.SourceKey]; got != toolresult.SourceBroker {
		t.Fatalf("expected logged %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceBroker, got)
	}
	if got := metadata[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected logged %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
}

func TestAICLIToolExecutor_RejectsTruncatedArgumentsBeforeCatalogExecution(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("codex", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: "write"}, runtimetools.ToolDescriptor{
		Name:        "write",
		Description: "write file",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	session := &ChatSession{
		FunctionRegistry: registry,
		FunctionCatalog:  catalog,
		cancelCtx:        context.Background(),
	}
	executor := &aicliToolExecutor{session: session}

	result := executor.ExecuteTool(context.Background(), types.ToolCall{
		ID:   "call-truncated",
		Name: "write",
		Args: map[string]interface{}{
			"_raw":         `{"file_path":"E:\\projects\\ai\\ai-agent-runtime\\backend\\out.txt","content":"hello`,
			"_parse_error": "unexpected end of JSON input",
		},
	})

	if result.Error == "" {
		t.Fatal("expected truncated arguments to fail")
	}
	if !strings.Contains(result.Error, "被截断") {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if strings.Contains(result.Content, "ok") {
		t.Fatalf("expected catalog execution to be skipped, got %q", result.Content)
	}
}

func TestAdapterRequestConfig_PropagatesReasoningEffortMetadata(t *testing.T) {
	session := &ChatSession{
		Provider:        config.Provider{Protocol: "codex"},
		Model:           "gpt-5.4-mini",
		ReasoningEffort: "medium",
	}

	req := adapterRequestConfig(session, nil, runtimechatcore.ProviderTurnRequest{Stream: true})
	if req.ReasoningEffort != "medium" {
		t.Fatalf("expected reasoning effort medium, got %q", req.ReasoningEffort)
	}
	if req.Metadata == nil {
		t.Fatal("expected metadata to be populated")
	}
	if got := req.Metadata["reasoning_effort"]; got != "medium" {
		t.Fatalf("expected reasoning_effort metadata medium, got %#v", got)
	}
}

func TestAdapterRequestConfig_CodexInjectsImageGenerationToolWhenModelCapabilityAllows(t *testing.T) {
	session := &ChatSession{
		Provider: config.Provider{
			Protocol: "codex",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4": {
					InputModalities: []string{"text", "image"},
					NativeTools: config.NativeToolCapabilities{
						ImageGeneration: true,
					},
				},
			},
		},
		Model: "gpt-5.4",
	}

	req := adapterRequestConfig(session, nil, runtimechatcore.ProviderTurnRequest{})
	tools, ok := req.Functions.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected codex functions payload, got %T", req.Functions)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 injected native tool, got %#v", tools)
	}
	if tools[0]["type"] != "image_generation" {
		t.Fatalf("expected image_generation tool, got %#v", tools[0])
	}
	if tools[0]["output_format"] != "png" {
		t.Fatalf("expected png output format, got %#v", tools[0]["output_format"])
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
	}, nil)
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

func TestFallbackActorExecutorResponse_IncludesGeneratedImageArtifact(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", true, "")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("SetLogDir failed: %v", err)
	}
	session := &ChatSession{Logger: logger}
	imageDir := currentGeneratedImageArtifactDir(session)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	imagePath := filepath.Join(imageDir, "image_1.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got := fallbackActorExecutorResponse(&agent.Result{
		Success: false,
		Error:   "HTTP 502: stream disconnected before completion",
	}, session)
	if !strings.Contains(got, "已检测到生成图片已保存") {
		t.Fatalf("expected generated image note, got %q", got)
	}
	if !strings.Contains(got, resolveAbsoluteChatPath(imagePath)) {
		t.Fatalf("expected generated image path, got %q", got)
	}
	if !strings.Contains(got, "HTTP 502") {
		t.Fatalf("expected original error, got %q", got)
	}
}

func TestAttemptDirectImageGenerationFallback_UsesDirectImageToolAfterTransientActorFailure(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterFunction(&richTestFunction{
		testFunction: testFunction{name: toolnames.OpenAIImageGenerateToolName},
		metadata: map[string]interface{}{
			runtimellm.MetadataKeyGeneratedImages: []map[string]interface{}{
				{"saved_path": "C:\\temp\\image_1.png"},
			},
		},
	})

	prompt := "我是一个医生，请生成一张女性人体平面图片，标注中医穴位位置。"
	runtimeSession := runtimechat.NewSession("user")
	runtimeSession.ID = "session-1"
	runtimeSession.ReplaceHistory([]types.Message{*types.NewUserMessage(prompt)})
	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		RuntimeSession:   runtimeSession,
	}

	got, ok := attemptDirectImageGenerationFallback(context.Background(), session, prompt, "HTTP 504: CDN节点请求源服务器超时")
	if !ok {
		t.Fatal("expected direct image generation fallback")
	}
	if !strings.Contains(got, "主聊天模型响应失败") || !strings.Contains(got, "ok") {
		t.Fatalf("unexpected fallback output: %q", got)
	}
	if len(session.RuntimeSession.History) == 0 {
		t.Fatal("expected fallback assistant message to be persisted")
	}
	last := session.RuntimeSession.History[len(session.RuntimeSession.History)-1]
	if last.Role != "assistant" || last.Content != "ok" {
		t.Fatalf("unexpected persisted assistant message: %+v", last)
	}
	if last.Metadata["direct_image_generation_fallback"] != true {
		t.Fatalf("expected fallback metadata, got %+v", last.Metadata)
	}
}

func TestFallbackActorExecutorResponse_IgnoresSuccessfulOrNonEmptyOutput(t *testing.T) {
	if got := fallbackActorExecutorResponse(&agent.Result{Success: true, Error: "tool failed"}, nil); got != "" {
		t.Fatalf("expected empty fallback for successful result, got %q", got)
	}
	if got := fallbackActorExecutorResponse(&agent.Result{Success: false, Output: "已有输出", Error: "tool failed"}, nil); got != "" {
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
		Stage:    "tool_requested",
		ToolName: "ls",
		Arguments: map[string]interface{}{
			"path": "docs",
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
	toolStartIndex := strings.Index(rendered, "• Running ls path=docs")
	toolDoneIndex := strings.Index(rendered, "• Ran ls path=docs")
	finalIndex := strings.Index(rendered, "最终答案。")
	if preludeIndex == -1 || toolStartIndex == -1 || toolDoneIndex == -1 || finalIndex == -1 {
		t.Fatalf("expected buffered content, tool lines, and final answer in output, got %q", rendered)
	}
	if !(preludeIndex < toolStartIndex && toolStartIndex < toolDoneIndex && toolDoneIndex < finalIndex) {
		t.Fatalf("expected tool rendering order to stay stable, got %q", rendered)
	}
	if strings.Contains(rendered, "[tool] ls path=docs") || strings.Contains(rendered, "[tool done] ls path=docs") {
		t.Fatalf("expected legacy tool labels to stay suppressed, got %q", rendered)
	}
	if strings.Contains(rendered, chatToolDivider("command start")) || strings.Contains(rendered, chatToolDivider("command end")) {
		t.Fatalf("expected command dividers to stay suppressed, got %q", rendered)
	}
	if strings.Count(rendered, "先说明一下。") != 1 {
		t.Fatalf("expected pre-tool assistant content once, got %q", rendered)
	}
	if strings.Count(rendered, "最终答案。") != 1 {
		t.Fatalf("expected final assistant content once, got %q", rendered)
	}
}

func TestAICLIEventRenderer_ShellToolUsesCompactCommandRendering(t *testing.T) {
	session := &ChatSession{Stream: true}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	renderer := newAICLIEventRenderer(session)
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:    runtimechatcore.EventResult,
		Content: "我先检查构建产物。",
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
		Stage:    "tool_requested",
		ToolName: "execute_shell_command",
		Arguments: map[string]interface{}{
			"command": "Get-Item '.\\\\aicli-cachetest.exe' | Select-Object FullName,Length,LastWriteTime",
		},
	})
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:     runtimechatcore.EventTool,
		Stage:    "tool_result",
		ToolName: "execute_shell_command",
		Arguments: map[string]interface{}{
			"command": "Get-Item '.\\\\aicli-cachetest.exe' | Select-Object FullName,Length,LastWriteTime",
		},
		Output:  "FullName  Length LastWriteTime\n--------  ------ -------------\n.\\\\aicli-cachetest.exe 41346220 2026/4/22 20:39:05",
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

	rendered := output.String()
	if !strings.Contains(rendered, "• Running Get-Item '.\\\\aicli-cachetest.exe' | Select-Object FullName,Length,LastWriteTime") {
		t.Fatalf("expected compact shell running line, got %q", rendered)
	}
	if !strings.Contains(rendered, "• Ran Get-Item '.\\\\aicli-cachetest.exe' | Select-Object FullName,Length,LastWriteTime") {
		t.Fatalf("expected compact shell completion line, got %q", rendered)
	}
	if strings.Contains(rendered, "[tool] execute_shell_command") || strings.Contains(rendered, "[tool done] execute_shell_command") {
		t.Fatalf("expected execute_shell_command labels to stay suppressed, got %q", rendered)
	}
	if strings.Contains(rendered, chatToolDivider("command start")) || strings.Contains(rendered, chatToolDivider("command end")) {
		t.Fatalf("expected command dividers to stay suppressed, got %q", rendered)
	}
}

func TestAICLIEventRenderer_SharedToolResultUsesSourceLabelsAndTighterFolding(t *testing.T) {
	session := &ChatSession{Stream: true}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	renderer := newAICLIEventRenderer(session)
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:     runtimechatcore.EventTool,
		Stage:    "tool_result",
		ToolName: "remote_search",
		Arguments: map[string]interface{}{
			"query": "golang tools",
		},
		Output: "result 1\nresult 2\nresult 3",
		Metadata: map[string]interface{}{
			"tool_source": "mcp",
		},
		Success: true,
	})

	rendered := output.String()
	if !strings.Contains(rendered, "• Ran [mcp] remote_search query=golang tools") {
		t.Fatalf("expected mcp label in shared tool render, got %q", rendered)
	}
	if !strings.Contains(rendered, "  result 1") || !strings.Contains(rendered, "  result 2") {
		t.Fatalf("expected first two result lines, got %q", rendered)
	}
	if strings.Contains(rendered, "result 3") {
		t.Fatalf("expected mcp folding to keep CLI compact, got %q", rendered)
	}
}

func TestAICLIEventRenderer_SharedToolRequestedUsesSourceLabels(t *testing.T) {
	session := &ChatSession{Stream: true}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	renderer := newAICLIEventRenderer(session)
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:     runtimechatcore.EventTool,
		Stage:    "tool_requested",
		ToolName: "list_mcp_resources",
		Metadata: map[string]interface{}{
			"tool_source": "meta",
		},
	})

	rendered := output.String()
	if !strings.Contains(rendered, "• Running [meta] list_mcp_resources") {
		t.Fatalf("expected meta label in shared requested render, got %q", rendered)
	}
}

func TestAICLIEventRenderer_SharedBrokerToolResultUsesSourceLabels(t *testing.T) {
	session := &ChatSession{Stream: true}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	renderer := newAICLIEventRenderer(session)
	renderer.Handle(runtimechatcore.ChatEvent{
		Type:     runtimechatcore.EventTool,
		Stage:    "tool_result",
		ToolName: "background_task",
		Arguments: map[string]interface{}{
			"command": "git status",
		},
		Output: "job_id=job-1\nstatus=queued\nrestart_policy=fail",
		Metadata: map[string]interface{}{
			"tool_source": "broker",
		},
		Success: true,
	})

	rendered := output.String()
	if !strings.Contains(rendered, "• Ran [broker] background_task command=git status") {
		t.Fatalf("expected broker label in shared tool render, got %q", rendered)
	}
	if !strings.Contains(rendered, "  job_id=job-1") || !strings.Contains(rendered, "  status=queued") {
		t.Fatalf("expected first two broker result lines, got %q", rendered)
	}
	if strings.Contains(rendered, "restart_policy=fail") {
		t.Fatalf("expected broker folding to keep CLI compact, got %q", rendered)
	}
}

func TestAICLISharedChatExecutor_PassesActiveTurnTokenBudgetToToolLoop(t *testing.T) {
	original := executeToolLoop
	defer func() {
		executeToolLoop = original
	}()

	var captured runtimechatcore.ToolLoopRequest
	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		captured = req
		return &runtimechatcore.ToolLoopResult{
			Response: &runtimechatcore.ChatResult{Output: "done"},
			History: []types.Message{
				*types.NewUserMessage("继续处理"),
				*types.NewAssistantMessage("done"),
			},
		}, nil
	}

	session := &ChatSession{
		DisableTools: true,
		Model:        "gpt-5",
		Provider: config.Provider{
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5": {
					MaxContextTokens: 10000,
					AutoCompactRatio: 0.8,
				},
			},
		},
	}

	executor := newAICLISharedChatExecutor()
	output, err := executor.Execute(context.Background(), session, "继续处理")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "done" {
		t.Fatalf("unexpected output: %q", output)
	}
	if captured.ActiveTurnMaxTokens != 8000 {
		t.Fatalf("expected active turn token budget 8000, got %d", captured.ActiveTurnMaxTokens)
	}
	if captured.PromptBudgetSource != "model_capability_auto_compact_ratio" {
		t.Fatalf("expected prompt budget source to be model_capability_auto_compact_ratio, got %q", captured.PromptBudgetSource)
	}
	if captured.ResolvedModel != "gpt-5" {
		t.Fatalf("expected resolved model gpt-5, got %q", captured.ResolvedModel)
	}
	if captured.ModelCapabilityMaxContextTokens != 10000 {
		t.Fatalf("expected max context tokens 10000, got %d", captured.ModelCapabilityMaxContextTokens)
	}
	if captured.ModelCapabilityAutoCompactRatio != 0.8 {
		t.Fatalf("expected auto compact ratio 0.8, got %v", captured.ModelCapabilityAutoCompactRatio)
	}
	if captured.CountTokens == nil {
		t.Fatal("expected shared chat executor to pass a CountTokens callback")
	}
}

func TestAICLISharedChatExecutor_HumanizesPromptPreflightFailureFromToolLoop(t *testing.T) {
	original := executeToolLoop
	defer func() {
		executeToolLoop = original
	}()

	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		return nil, &agent.PromptPreflightError{
			PromptTokens:     1600,
			PromptBudget:     900,
			Code:             "active_turn_not_compactable",
			Reason:           "active-turn replay cannot be compacted further",
			SuggestedAction:  "请开启新一轮对话、减少上下文，或提高预算。",
			ResolvedProvider: "shared-provider",
			ResolvedModel:    "shared-model",
			BudgetSource:     "model_capability_auto_compact_token_limit",
		}
	}

	session := &ChatSession{
		DisableTools: true,
		Model:        "shared-model",
		ProviderName: "shared-provider",
	}

	_, err := newAICLISharedChatExecutor().Execute(context.Background(), session, "继续处理")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "本次请求在发送给模型前已被本地拦截") {
		t.Fatalf("expected humanized preflight error, got %v", err)
	}
	if !strings.Contains(err.Error(), "provider=shared-provider") || !strings.Contains(err.Error(), "model=shared-model") {
		t.Fatalf("expected provider/model metadata in humanized error, got %v", err)
	}
}

func TestAICLISharedChatExecutor_AppliesPromptPreflightRecoveryHistoryBeforeReturningError(t *testing.T) {
	original := executeToolLoop
	originalCompact := autoCompactSharedChatHistory
	defer func() {
		executeToolLoop = original
		autoCompactSharedChatHistory = originalCompact
	}()
	autoCompactSharedChatHistory = maybeAutoCompactSharedChatHistory

	compacted := types.NewAssistantMessage("Compacted earlier tool replay in current turn:\n- earlier tool output summarized")
	compacted.Metadata["active_turn_compaction"] = true

	replacementHistory := []types.Message{
		*types.NewUserMessage("继续处理"),
		*compacted,
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "AGENTS ..."),
	}

	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		return nil, &agent.PromptPreflightError{
			PromptTokens:        1600,
			PromptBudget:        900,
			Code:                "prompt_still_exceeds_budget_after_compaction",
			Reason:              "prompt budget still exceeded after active-turn compaction",
			SuggestedAction:     "请继续收缩上下文层、提高预算，或从新的轮次继续。",
			ResolvedProvider:    "shared-provider",
			ResolvedModel:       "shared-model",
			BudgetSource:        "model_capability_auto_compact_token_limit",
			ActiveTurnCompacted: true,
			ReplacementHistory:  replacementHistory,
		}
	}

	originalMessages, err := buildAICLIMessagesFromRuntimeHistory([]types.Message{
		*types.NewUserMessage("继续处理"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", "README ..."),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "AGENTS ..."),
	})
	if err != nil {
		t.Fatalf("buildAICLIMessagesFromRuntimeHistory failed: %v", err)
	}

	session := &ChatSession{
		DisableTools: true,
		Model:        "shared-model",
		ProviderName: "shared-provider",
		Provider: config.Provider{
			Protocol:     "openai",
			DefaultModel: "shared-model",
		},
		Messages: originalMessages,
	}

	_, err = newAICLISharedChatExecutor().Execute(context.Background(), session, "继续处理")
	if err == nil {
		t.Fatal("expected error")
	}
	if len(session.Messages) != 4 {
		t.Fatalf("expected replacement history to be applied to session messages, got %#v", session.Messages)
	}
	metadata, _ := session.Messages[1]["metadata"].(map[string]interface{})
	if metadata == nil || metadata["active_turn_compaction"] != true {
		t.Fatalf("expected compacted assistant summary metadata in session messages, got %#v", session.Messages[1])
	}
	if !strings.Contains(err.Error(), "当前会话已自动保存压缩后的上下文，可直接继续下一轮。") {
		t.Fatalf("expected recovery-applied hint in humanized error, got %v", err)
	}
	if !strings.Contains(err.Error(), "本次请求在发送给模型前已被本地拦截") {
		t.Fatalf("expected humanized preflight error, got %v", err)
	}
}

func TestAICLISharedChatExecutor_AutoCompactsHistoryBeforeToolLoop(t *testing.T) {
	originalExecute := executeToolLoop
	originalCompact := autoCompactSharedChatHistory
	defer func() {
		executeToolLoop = originalExecute
		autoCompactSharedChatHistory = originalCompact
	}()

	compactedHistory := []types.Message{
		*types.NewUserMessage("较新的问题"),
		*types.NewUserMessage("Compacted context from earlier turns:\n- 关键结论 A\n- 关键结论 B"),
	}
	compactedHistory[1].Metadata["context_stage"] = "compaction"
	compactedHistory[1].Metadata["compact_mode"] = compactruntime.ModeLocal

	autoCompactSharedChatHistory = func(ctx context.Context, session *ChatSession, history []types.Message) ([]types.Message, *sharedChatAutoCompactReport, error) {
		if len(history) == 0 {
			t.Fatal("expected original history to be provided to auto compaction")
		}
		return compactedHistory, &sharedChatAutoCompactReport{
			Result: &compactruntime.Result{
				Mode:               compactruntime.ModeLocal,
				TokenBefore:        1200,
				TokenAfter:         240,
				CompactedMessages:  6,
				ReplacementHistory: compactedHistory,
			},
			Status: compactruntime.Status{
				Mode:   compactruntime.ModeLocal,
				Reason: "above_limit",
			},
		}, nil
	}

	var captured runtimechatcore.ToolLoopRequest
	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		captured = req
		return &runtimechatcore.ToolLoopResult{
			Response: &runtimechatcore.ChatResult{Output: "done"},
			History: append(append([]types.Message(nil), req.History...),
				*types.NewAssistantMessage("done"),
			),
		}, nil
	}

	originalMessages, err := buildAICLIMessagesFromRuntimeHistory([]types.Message{
		*types.NewUserMessage("很早之前的问题"),
		*types.NewAssistantMessage("很早之前的回答"),
		*types.NewUserMessage("较新的问题"),
	})
	if err != nil {
		t.Fatalf("buildAICLIMessagesFromRuntimeHistory failed: %v", err)
	}

	session := &ChatSession{
		DisableTools: true,
		Model:        "shared-model",
		ProviderName: "shared-provider",
		Messages:     originalMessages,
	}

	output, err := newAICLISharedChatExecutor().Execute(context.Background(), session, "继续处理")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "done" {
		t.Fatalf("unexpected output: %q", output)
	}
	if len(captured.History) != len(compactedHistory) {
		t.Fatalf("expected tool loop to receive compacted history, got %#v", captured.History)
	}
	if stage := captured.History[1].Metadata.GetString("context_stage", ""); stage != "compaction" {
		t.Fatalf("expected compacted history marker in tool loop request, got %#v", captured.History)
	}
}

func TestAICLISharedChatExecutor_AutoCompactFailureDoesNotBlockRequest(t *testing.T) {
	originalExecute := executeToolLoop
	originalCompact := autoCompactSharedChatHistory
	defer func() {
		executeToolLoop = originalExecute
		autoCompactSharedChatHistory = originalCompact
	}()

	autoCompactSharedChatHistory = func(ctx context.Context, session *ChatSession, history []types.Message) ([]types.Message, *sharedChatAutoCompactReport, error) {
		return history, &sharedChatAutoCompactReport{
			Status: compactruntime.Status{
				Mode:   compactruntime.ModeLocal,
				Reason: "summary_generation_failed",
			},
		}, fmt.Errorf("compact summary failed")
	}

	var captured runtimechatcore.ToolLoopRequest
	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		captured = req
		return &runtimechatcore.ToolLoopResult{
			Response: &runtimechatcore.ChatResult{Output: "done"},
			History: append(append([]types.Message(nil), req.History...),
				*types.NewAssistantMessage("done"),
			),
		}, nil
	}

	originalHistory := []types.Message{
		*types.NewUserMessage("原始问题"),
		*types.NewAssistantMessage("原始回答"),
	}
	originalMessages, err := buildAICLIMessagesFromRuntimeHistory(originalHistory)
	if err != nil {
		t.Fatalf("buildAICLIMessagesFromRuntimeHistory failed: %v", err)
	}

	session := &ChatSession{
		DisableTools: true,
		Model:        "shared-model",
		ProviderName: "shared-provider",
		Messages:     originalMessages,
	}

	output, err := newAICLISharedChatExecutor().Execute(context.Background(), session, "继续处理")
	if err != nil {
		t.Fatalf("Execute failed despite compaction error: %v", err)
	}
	if output != "done" {
		t.Fatalf("unexpected output: %q", output)
	}
	if len(captured.History) != len(originalHistory) {
		t.Fatalf("expected executor to continue with original history, got %#v", captured.History)
	}
	if captured.History[0].Content != "原始问题" {
		t.Fatalf("expected original history to be preserved on auto compact failure, got %#v", captured.History)
	}
}

func TestCountSharedChatMessagesTokens_IncludesToolCallPayloads(t *testing.T) {
	messages := []types.Message{
		{
			Role:    "assistant",
			Content: "Need to inspect the repository.",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_1",
					Name: "read_file",
					Args: map[string]interface{}{"path": "README.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", "README content"),
	}

	withoutToolArgs := estimateSharedChatTokenCount(messages[0].Role) +
		estimateSharedChatTokenCount(messages[0].Content) +
		estimateSharedChatTokenCount(messages[0].ToolCallID) +
		estimateSharedChatTokenCount(messages[1].Role) +
		estimateSharedChatTokenCount(messages[1].Content) +
		estimateSharedChatTokenCount(messages[1].ToolCallID) + 8

	withToolArgs := countSharedChatMessagesTokens(messages)
	if withToolArgs <= withoutToolArgs {
		t.Fatalf("expected tool call payloads to contribute to token estimate, got with=%d without=%d", withToolArgs, withoutToolArgs)
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
