package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimecontext "github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/embedding"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type testMCPManager struct{}

func (m *testMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:        toolName,
		Description: "test tool",
		MCPName:     "test-mcp",
		Enabled:     true,
	}, nil
}

func (m *testMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if prompt, ok := args["prompt"]; ok {
		return fmt.Sprintf("tool:%s prompt:%v", toolName, prompt), nil
	}
	return fmt.Sprintf("tool:%s", toolName), nil
}

func (m *testMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{{
		Name:        "echo_tool",
		Description: "echo tool",
		MCPName:     "test-mcp",
		Enabled:     true,
	}}
}

func (m *testMCPManager) ListMCPs() []*mcpconfig.MCPStatus {
	return []*mcpconfig.MCPStatus{{
		Name:          "test-mcp",
		Type:          "stdio",
		TrustLevel:    mcpconfig.MCPTrustLevelLocal,
		ExecutionMode: "local_mcp",
		Enabled:       true,
		Connected:     true,
		ToolCount:     1,
	}}
}

type failingMCPManager struct{}

func (m *failingMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:        toolName,
		Description: "failing tool",
		MCPName:     "test-mcp",
		Enabled:     true,
	}, nil
}

func (m *failingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("tool %s failed", toolName)
}

func (m *failingMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{{
		Name:        "broken_tool",
		Description: "always fails",
		MCPName:     "test-mcp",
		Enabled:     true,
	}}
}

type blockingCatalogMCPManager struct {
	listStarted chan struct{}
	release     chan struct{}
}

func (m *blockingCatalogMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:        toolName,
		Description: "blocking tool",
		MCPName:     "blocking-mcp",
		Enabled:     true,
	}, nil
}

func (m *blockingCatalogMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *blockingCatalogMCPManager) ListTools() []skill.ToolInfo {
	if m != nil && m.listStarted != nil {
		select {
		case <-m.listStarted:
		default:
			close(m.listStarted)
		}
	}
	if m != nil && m.release != nil {
		<-m.release
	}
	return []skill.ToolInfo{{
		Name:        "blocking_tool",
		Description: "blocks catalog refresh",
		MCPName:     "blocking-mcp",
		Enabled:     true,
	}}
}

type fakeLifecycleMCPManager struct {
	reloadCount int
	startCount  int
	statuses    []*mcpconfig.MCPStatus
	tools       []*mcpregistry.ToolInfo
	observers   []mcpmanager.LifecycleObserver
}

func (m *fakeLifecycleMCPManager) LoadConfig(configPath string) error { return nil }
func (m *fakeLifecycleMCPManager) Start(ctx context.Context) error {
	m.startCount++
	traceID := mcpmanager.TraceIDFromContext(ctx)
	m.emit(mcpmanager.LifecycleEvent{
		Type:    "mcp.transport.connected",
		TraceID: traceID,
		MCPName: "test-mcp",
		Payload: map[string]interface{}{
			"transport_type": "stdio",
			"session_id":     "fake-session",
		},
	})
	m.emit(mcpmanager.LifecycleEvent{
		Type:    "mcp.client.session.connected",
		TraceID: traceID,
		MCPName: "test-mcp",
		Payload: map[string]interface{}{
			"transport_type": "stdio",
			"session_id":     "fake-session",
		},
	})
	m.emit(mcpmanager.LifecycleEvent{
		Type:    "mcp.connected",
		TraceID: traceID,
		MCPName: "test-mcp",
		Payload: map[string]interface{}{"tool_count": len(m.tools)},
	})
	m.emit(mcpmanager.LifecycleEvent{
		Type:    "mcp.tools.loaded",
		TraceID: traceID,
		MCPName: "test-mcp",
		Payload: map[string]interface{}{"tool_count": len(m.tools)},
	})
	return nil
}
func (m *fakeLifecycleMCPManager) Stop() error                        { return nil }
func (m *fakeLifecycleMCPManager) ListTools() []*mcpregistry.ToolInfo { return m.tools }
func (m *fakeLifecycleMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*mcpprotocol.CallToolResult, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *fakeLifecycleMCPManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *fakeLifecycleMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*mcpprotocol.ListResourcesResult, error) {
	return &mcpprotocol.ListResourcesResult{}, nil
}
func (m *fakeLifecycleMCPManager) SetMCPEnabled(name string, enabled bool) error { return nil }
func (m *fakeLifecycleMCPManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	for _, status := range m.statuses {
		if status != nil && status.Name == name {
			return status, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (m *fakeLifecycleMCPManager) ListMCPs() []*mcpconfig.MCPStatus { return m.statuses }
func (m *fakeLifecycleMCPManager) ReloadConfig() error {
	m.reloadCount++
	return nil
}
func (m *fakeLifecycleMCPManager) AddLifecycleObserver(observer mcpmanager.LifecycleObserver) {
	if observer == nil {
		return
	}
	m.observers = append(m.observers, observer)
}

func (m *fakeLifecycleMCPManager) emit(event mcpmanager.LifecycleEvent) {
	for _, observer := range m.observers {
		observer(event)
	}
}

var _ mcpmanager.Manager = (*fakeLifecycleMCPManager)(nil)
var _ mcpmanager.ObservableManager = (*fakeLifecycleMCPManager)(nil)

type testUsageLedgerStore struct {
	records []*entity.TokenUsageHistory
}

func (s *testUsageLedgerStore) Create(history *entity.TokenUsageHistory) error {
	if history == nil {
		return nil
	}
	s.records = append(s.records, history)
	return nil
}

func (s *testUsageLedgerStore) GetSince(since time.Time, limit int) ([]*entity.TokenUsageHistory, error) {
	filtered := make([]*entity.TokenUsageHistory, 0, len(s.records))
	for i := len(s.records) - 1; i >= 0; i-- {
		record := s.records[i]
		if record == nil {
			continue
		}
		if !since.IsZero() && time.Time(record.CreatedAt).Before(since) {
			continue
		}
		filtered = append(filtered, record)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

type testLLMProvider struct {
	name         string
	content      string
	streamChunks []llm.StreamChunk
	callErr      error
	streamErr    error
	healthErr    error
	requests     []*llm.LLMRequest
	capabilities map[string]agentconfig.ModelCapabilitySpec
}

type testSequenceLLMProvider struct {
	name      string
	responses []*llm.LLMResponse
	callCount int
}

func (p *testLLMProvider) Name() string { return p.name }

func (p *testLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	if p.callErr != nil {
		return nil, p.callErr
	}
	return &llm.LLMResponse{
		Content: p.content,
		Model:   p.name,
	}, nil
}

func (p *testLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	chunks := p.streamChunks
	if len(chunks) == 0 {
		chunks = []llm.StreamChunk{{Type: llm.EventTypeText, Content: p.content, Done: true}}
	}
	ch := make(chan llm.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func (p *testLLMProvider) CountTokens(text string) int { return len(text) }

func (p *testLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true}
}

func (p *testLLMProvider) CheckHealth(ctx context.Context) error { return p.healthErr }

func (p *testLLMProvider) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	capability, ok := llm.ResolveModelCapabilitySpec(requestedModel, p.capabilities)
	return requestedModel, capability, ok
}

func (p *testSequenceLLMProvider) Name() string { return p.name }

func (p *testSequenceLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if p.callCount >= len(p.responses) {
		return &llm.LLMResponse{Content: "done", Model: p.name}, nil
	}
	response := p.responses[p.callCount]
	p.callCount++
	return response, nil
}

func (p *testSequenceLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func cloneLLMRequest(req *llm.LLMRequest) *llm.LLMRequest {
	if req == nil {
		return nil
	}
	cloned := &llm.LLMRequest{
		Model:           req.Model,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.ReasoningEffort,
		Thinking:        types.CloneThinkingConfig(req.Thinking),
		Stream:          req.Stream,
	}
	if len(req.Messages) > 0 {
		cloned.Messages = make([]types.Message, len(req.Messages))
		for index := range req.Messages {
			cloned.Messages[index] = *req.Messages[index].Clone()
		}
	}
	if len(req.Tools) > 0 {
		cloned.Tools = append([]types.ToolDefinition(nil), req.Tools...)
	}
	if len(req.Metadata) > 0 {
		cloned.Metadata = make(map[string]interface{}, len(req.Metadata))
		for key, value := range req.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

func (p *testSequenceLLMProvider) CountTokens(text string) int { return len(text) }

func (p *testSequenceLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true}
}

func (p *testSequenceLLMProvider) CheckHealth(ctx context.Context) error { return nil }

func TestExecuteSkill_RunsWorkflow(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "echo-skill",
		Description: "workflow execution test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"echo"},
			Weight: 1,
		}},
		Tools: []string{"echo_tool"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"prompt":"hello workflow"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/echo-skill/execute", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "echo-skill", payload["skill"])
	assert.Equal(t, "completed", payload["status"])

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, true, result["success"])
	assert.Contains(t, result["output"], "hello workflow")
}

func TestExecuteSkill_AddsAdminDebugWarningHeader(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "echo-skill",
		Description: "workflow execution test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"echo"},
			Weight: 1,
		}},
		Tools: []string{"echo_tool"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/echo-skill/execute", bytes.NewReader([]byte(`{"prompt":"hello workflow"}`)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/api/runtime/skills/echo-skill/execute", rec.Header().Get("X-AI-Gateway-Entrypoint"))
	assert.Equal(t, "/api/runtime/skills/echo-skill/execute", rec.Header().Get("X-AI-Gateway-Canonical-Entrypoint"))
	assert.Equal(t, "admin-debug", rec.Header().Get("X-AI-Gateway-Entrypoint-Mode"))
	assert.Contains(t, rec.Header().Get("Warning"), "admin/debug")
	assert.Contains(t, rec.Header().Get("Link"), "/api/runtime/skills/echo-skill/execute")
}

func TestExecuteSkill_DeniesMissingSkillPermission(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "permissioned-echo",
		Description: "workflow permission test",
		Permissions: []string{"shell"},
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"echo"},
			Weight: 1,
		}},
		Tools: []string{"echo_tool"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"prompt":"hello workflow"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/permissioned-echo/execute", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "permissioned-echo", payload["skill"])
	assert.Equal(t, "failed", payload["status"])

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, false, result["success"])
	assert.Contains(t, result["error"], "requires permissions")
	assert.Equal(t, "AGENT_PERMISSION", result["error_code"])
	errorContext := result["error_context"].(map[string]interface{})
	assert.Equal(t, "permissioned-echo", errorContext["skill"])
}

func TestAgentChat_CanonicalRouteUsesAgentFirstEntrypoint(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from canonical route",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, canonicalAgentChatEntrypoint, rec.Header().Get("X-AI-Gateway-Entrypoint"))
	assert.Equal(t, canonicalAgentChatEntrypoint, rec.Header().Get("X-AI-Gateway-Canonical-Entrypoint"))
	assert.Equal(t, "canonical", rec.Header().Get("X-AI-Gateway-Entrypoint-Mode"))
	assert.Empty(t, rec.Header().Get("Warning"))

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "hello from canonical route", result["output"])
}

func TestAgentChat_RuntimeNamespaceDoesNotExposeChatRoute(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from runtime namespace route",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/agent/chat", bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestExecuteSkill_AllowsGrantedSkillPermissionHeader(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "permissioned-echo",
		Description: "workflow permission test",
		Permissions: []string{"shell"},
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"echo"},
			Weight: 1,
		}},
		Tools: []string{"echo_tool"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"prompt":"hello workflow"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/permissioned-echo/execute", bytes.NewReader(body))
	req.Header.Set("X-Skills-Permission", "shell")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "completed", payload["status"])

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, true, result["success"])
	assert.Contains(t, result["output"], "hello workflow")
}

func TestAgentChat_UsesLLMAndPersistsSession(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from llm",
	}))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"user_id":"user-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	sessionIDValue, ok := payload["session_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, sessionIDValue)

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "llm_fallback", payload["source"])
	assert.Equal(t, "llm", result["kind"])
	assert.Equal(t, "llm_fallback", result["source"])
	assert.Equal(t, true, result["success"])
	assert.Equal(t, "hello from llm", result["output"])
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, "llm_fallback", orchestration["source"])
	assert.Equal(t, false, orchestration["route_attempted"])
	assert.Equal(t, float64(0), orchestration["candidate_count"])
	observationSummary := orchestration["observation_summary"].(map[string]interface{})
	assert.Equal(t, float64(0), observationSummary["total_duration_ms"])

	session, err := sessionManager.GetSession(context.Background(), sessionIDValue)
	require.NoError(t, err)
	require.Len(t, session.GetMessages(), 2)
	assert.Equal(t, "user", session.GetMessages()[0].Role)
	assert.Equal(t, "assistant", session.GetMessages()[1].Role)
}

func TestAgentChat_SessionHistoryAutoCompactsBeforeLLMFallback(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &testLLMProvider{
		name:    "provider-a",
		content: "hello from llm",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 120},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))
	handler.SetLLMRuntime(runtime)

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.DefaultProvider = "provider-a"
	runtimeConfig.Agent.DefaultModel = "gpt-5"
	runtimeConfig.Context.KeepRecentMessages = 1
	handler.SetRuntimeConfig(runtimeConfig, "")

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.CreateSession(context.Background(), "user-compact")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage(strings.Repeat("older user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("older assistant context ", 80)),
		*types.NewUserMessage(strings.Repeat("recent user context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("recent assistant context ", 80)),
	})
	require.NoError(t, sessionManager.Update(context.Background(), session))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"continue from here"}],"session_id":"%s","user_id":"user-compact"}`, session.ID))
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, provider.requests, 2)
	require.Equal(t, "compact", provider.requests[0].Metadata["internal_operation"])

	updated, err := sessionManager.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	require.Len(t, updated.GetMessages(), 6)
	require.Equal(t, "compaction", updated.GetMessages()[1].Metadata["context_stage"])
	assert.NotContains(t, updated.GetMessages()[1].Content, "older assistant context older assistant context")
}

func TestAgentChat_ErrorResponseIncludesRequestID(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		callErr: fmt.Errorf(`HTTP 503: {"error":{"message":"Service temporarily unavailable","type":"api_error"}}`),
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	requestID := strings.TrimSpace(rec.Header().Get("X-Request-ID"))
	require.NotEmpty(t, requestID)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, requestID, payload["request_id"])
	assert.Contains(t, payload["error"], "Service temporarily unavailable")
}

func TestAgentChat_UsesExplicitThinkingAndReasoningForLLMFallback(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	provider := &testLLMProvider{
		name:    "test-model",
		content: "hello from llm",
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"model":"test-model","reasoning_effort":"high","thinking":{"type":"enabled","budget_tokens":8192}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, provider.requests, 1)
	assert.Equal(t, "high", provider.requests[0].ReasoningEffort)
	if assert.NotNil(t, provider.requests[0].Thinking) {
		assert.Equal(t, "enabled", provider.requests[0].Thinking.Type)
		if assert.NotNil(t, provider.requests[0].Thinking.BudgetTokens) {
			assert.Equal(t, 8192, *provider.requests[0].Thinking.BudgetTokens)
		}
	}
}

func TestExecuteSkill_UsesThinkingAndReasoningFromOptionsForLLMFallback(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:         "llm-skill",
		Description:  "llm fallback test",
		SystemPrompt: "Use the configured thinking mode.",
		UserPrompt:   "Answer the request.",
	}))

	handler := NewHandler(registry, nil, mcpManager)
	provider := &testLLMProvider{
		name:    "test-model",
		content: "hello from llm",
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"prompt":"hi","options":{"reasoning_effort":"medium","thinking":{"type":"adaptive","effort":"high","budget_tokens":8192}}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/llm-skill/execute", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, provider.requests, 1)
	assert.Equal(t, "medium", provider.requests[0].ReasoningEffort)
	if assert.NotNil(t, provider.requests[0].Thinking) {
		assert.Equal(t, "adaptive", provider.requests[0].Thinking.Type)
		assert.Equal(t, "high", provider.requests[0].Thinking.Effort)
		if assert.NotNil(t, provider.requests[0].Thinking.BudgetTokens) {
			assert.Equal(t, 8192, *provider.requests[0].Thinking.BudgetTokens)
		}
	}
}

func TestAgentChat_EnableReAct_UsesAgentLoopAndPersistsSession(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from react loop",
	}))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"user_id":"user-react","enable_react":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	sessionIDValue, ok := payload["session_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, sessionIDValue)

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "agent_react", payload["source"])
	assert.Equal(t, "agent", result["kind"])
	assert.Equal(t, "agent_react", result["source"])
	assert.Equal(t, true, result["success"])
	assert.Equal(t, "hello from react loop", result["output"])
	assert.NotEmpty(t, result["trace_id"])
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, "agent_react", orchestration["source"])
	assert.Equal(t, false, orchestration["route_attempted"])

	session, err := sessionManager.GetSession(context.Background(), sessionIDValue)
	require.NoError(t, err)
	require.Len(t, session.GetMessages(), 2)
	assert.Equal(t, "user", session.GetMessages()[0].Role)
	assert.Equal(t, "assistant", session.GetMessages()[1].Role)
	assert.Equal(t, "hello from react loop", session.GetMessages()[1].Content)
}

func TestAgentChat_EnableReAct_IncludesWorkspaceContextInModelRequest(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package demo\nfunc SearchDocs() {}\n"), 0o644))

	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	provider := &testLLMProvider{
		name:    "test-model",
		content: "react with workspace",
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"search docs"}],"enable_react":true,"workspace_path":"` + strings.ReplaceAll(tmpDir, `\`, `\\`) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("X-Skills-Permission", "shell")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, provider.requests, 1)

	request := provider.requests[0]
	var sawWorkspaceSummary bool
	var sawRuntimeSummary bool
	for _, message := range request.Messages {
		if message.Role != "system" {
			continue
		}
		if strings.Contains(message.Content, "Workspace context:") &&
			strings.Contains(message.Content, `query="search docs"`) &&
			strings.Contains(message.Content, "SearchDocs") {
			sawWorkspaceSummary = true
		}
		if strings.Contains(message.Content, "Runtime context summary:") &&
			strings.Contains(message.Content, `"workspace_path"`) &&
			strings.Contains(message.Content, `"permissions":["shell"]`) {
			sawRuntimeSummary = true
		}
	}
	assert.True(t, sawWorkspaceSummary, "expected workspace summary in ReAct request")
	assert.True(t, sawRuntimeSummary, "expected runtime context summary in ReAct request")
}

func TestAgentChat_EnableReAct_DoesNotAutoScanDefaultWorkspace(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	provider := &testLLMProvider{
		name:    "test-model",
		content: "react without workspace scan",
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"search docs"}],"enable_react":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, provider.requests, 1)

	request := provider.requests[0]
	for _, message := range request.Messages {
		if message.Role != "system" {
			continue
		}
		assert.NotContains(t, message.Content, "Workspace context:")
		assert.NotContains(t, message.Content, `"workspace_path"`)
	}
}

func TestAgentChat_EnableReAct_SupportsStreamingMode(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from react loop",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"enable_react":true,"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, rec.Body.String(), `"source":"agent_react"`)
	assert.Contains(t, rec.Body.String(), "event: done")
}

func TestAgentChat_EnableReAct_StreamingMode_ReplaysToolEvents(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Model: "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-1",
					Name: "echo_tool",
					Args: map[string]interface{}{"prompt": "hi"},
				}},
			},
			{
				Model:   "test-model",
				Content: "tool finished",
			},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"run tool"}],"enable_react":true,"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"source":"agent_react"`)
	assert.Contains(t, rec.Body.String(), "event: tool_call")
	assert.Contains(t, rec.Body.String(), "event: tool_start")
	assert.Contains(t, rec.Body.String(), "event: tool_end")
	assert.Contains(t, rec.Body.String(), `"name":"echo_tool"`)
	assert.Contains(t, rec.Body.String(), `"status":"tool_call"`)
	assert.Contains(t, rec.Body.String(), `"status":"tool_start"`)
	assert.Contains(t, rec.Body.String(), `"status":"tool_end"`)
	assert.Contains(t, rec.Body.String(), `"arguments":{"prompt":"hi"}`)
}

func TestAgentChat_EnableReAct_ExposesSubagentSummary(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Delegate.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "spawn_subagents",
						Args: map[string]interface{}{
							"agents": []interface{}{
								map[string]interface{}{
									"id":        "child-1",
									"role":      "researcher",
									"goal":      "Inspect logs",
									"read_only": true,
								},
							},
						},
					},
				},
			},
			{
				Content: "Child summary.",
				Model:   "test-model",
			},
			{
				Content: "Parent final answer.",
				Model:   "test-model",
			},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"inspect logs"}],"enable_react":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	subagentSummary := result["subagent_summary"].(map[string]interface{})
	assert.Equal(t, float64(1), subagentSummary["batches"])
	assert.Equal(t, float64(1), subagentSummary["count"])
	assert.Equal(t, float64(1), subagentSummary["successful"])
	assert.Contains(t, subagentSummary["roles"].([]interface{}), "researcher")

	orchestration := result["orchestration"].(map[string]interface{})
	observationSummary := orchestration["observation_summary"].(map[string]interface{})
	assert.Equal(t, float64(1), observationSummary["subagent_batches"])
	assert.Equal(t, float64(1), observationSummary["subagent_count"])
	assert.Equal(t, float64(1), observationSummary["subagent_successful"])
	assert.Contains(t, observationSummary["subagent_roles"].([]interface{}), "researcher")
}

func TestGetRuntimeTrace_ReturnsEventsForTrace(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I will call a tool first.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "echo_tool",
						Args: map[string]interface{}{"prompt": "trace me"},
					},
				},
			},
			{
				Content: "tool finished",
				Model:   "test-model",
			},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"run tool"}],"enable_react":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	traceID, ok := result["trace_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, traceID)
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:    "recall.performed",
		TraceID: traceID,
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	traceReq := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/"+traceID+"?limit=20", nil)
	traceReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	traceRec := httptest.NewRecorder()
	router.ServeHTTP(traceRec, traceReq)
	require.Equal(t, http.StatusOK, traceRec.Code)

	var tracePayload map[string]interface{}
	require.NoError(t, json.Unmarshal(traceRec.Body.Bytes(), &tracePayload))
	assert.Equal(t, traceID, tracePayload["trace_id"])
	assert.NotEmpty(t, tracePayload["events"])

	summary := tracePayload["summary"].(map[string]interface{})
	eventTypes := summary["event_types"].(map[string]interface{})
	assert.Contains(t, eventTypes, "tool.requested")
	assert.Contains(t, eventTypes, "tool.completed")
	execution := summary["execution"].(map[string]interface{})
	assert.Equal(t, float64(1), execution["tool_requested"])
	assert.Equal(t, float64(1), execution["tool_completed"])
	assert.Equal(t, float64(1), execution["tool_reduced"])
	provenance := summary["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), provenance["recall_with_source_refs"])
	assert.Contains(t, provenance["profile_resource_refs"].([]interface{}), "profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json")
	assert.Equal(t, float64(1), provenance["profile_resource_count"])
	assert.Equal(t, float64(1), provenance["profile_memory_count"])
	assert.Contains(t, provenance["profile_resource_labels"].([]interface{}), "memory:memory.json")
}

func TestGetRuntimeTrace_SummarizesPatchDecisionAudit(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:      "patch.decision",
		TraceID:   "trace-patch-audit",
		AgentName: "agent-a",
		SessionID: "session-a",
		ToolName:  "spawn_subagents",
		Payload: map[string]interface{}{
			"patch_decision":        "approved_override",
			"patch_decision_policy": "strict",
			"patch_approval": map[string]interface{}{
				"ticket_id": "CAB-700",
				"approver":  "release-manager",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	traceReq := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/trace-patch-audit?limit=20", nil)
	traceReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	traceRec := httptest.NewRecorder()
	router.ServeHTTP(traceRec, traceReq)
	require.Equal(t, http.StatusOK, traceRec.Code)

	var tracePayload map[string]interface{}
	require.NoError(t, json.Unmarshal(traceRec.Body.Bytes(), &tracePayload))
	summary := tracePayload["summary"].(map[string]interface{})
	governance := summary["governance"].(map[string]interface{})
	assert.Equal(t, float64(1), governance["patch_decisions"])
	assert.Equal(t, float64(1), governance["patch_approved_override"])
	assert.Equal(t, float64(1), governance["patch_approvals_with_ticket"])
	patchGovernance := tracePayload["patch_governance"].(map[string]interface{})
	assert.Equal(t, float64(1), patchGovernance["decisions"])
	assert.Equal(t, float64(1), patchGovernance["approved_override"])
	assert.Equal(t, float64(1), patchGovernance["approvals_with_ticket"])
	patchPolicies := governance["patch_policies"].(map[string]interface{})
	assert.Equal(t, float64(1), patchPolicies["strict"])
	tickets := summary["patch_approval_tickets"].([]interface{})
	require.Len(t, tickets, 1)
	assert.Equal(t, "CAB-700", tickets[0])
}

func TestGetRuntimeTrace_IncludesCheckpointCreatedProvenance(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:      "checkpoint_created",
		TraceID:   "trace-checkpoint-provenance",
		SessionID: "session-checkpoint-provenance",
		ToolName:  "execute_shell_command",
		Payload: map[string]interface{}{
			"checkpoint_id": "chk_profile_1",
			"source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	traceReq := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/trace-checkpoint-provenance?limit=20", nil)
	traceReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	traceRec := httptest.NewRecorder()
	router.ServeHTTP(traceRec, traceReq)
	require.Equal(t, http.StatusOK, traceRec.Code)

	var tracePayload map[string]interface{}
	require.NoError(t, json.Unmarshal(traceRec.Body.Bytes(), &tracePayload))
	summary := tracePayload["summary"].(map[string]interface{})
	eventTypes := summary["event_types"].(map[string]interface{})
	assert.Equal(t, float64(1), eventTypes["checkpoint_created"])
	provenance := summary["provenance"].(map[string]interface{})
	assert.Contains(t, provenance["profile_resource_refs"].([]interface{}), "profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json")

	events := tracePayload["events"].([]interface{})
	require.Len(t, events, 1)
	event := events[0].(map[string]interface{})
	payload := event["payload"].(map[string]interface{})
	assert.Equal(t, "chk_profile_1", payload["checkpoint_id"])
	assert.Contains(t, payload["source_refs"].([]interface{}), "profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json")
}

func TestGetRuntimeTraces_ReturnsRecentSummaries(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:      "tool.requested",
		TraceID:   "trace-alpha",
		AgentName: "agent-a",
		SessionID: "session-a",
		ToolName:  "read_logs",
	})
	bus.Publish(runtimeevents.Event{
		Type:      "mcp.transport.connected",
		TraceID:   "trace-beta",
		AgentName: "mcp-manager",
		Payload: map[string]interface{}{
			"mcp_name":       "echo-test",
			"transport_type": "websocket",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:     "tool.denied",
		TraceID:  "trace-beta",
		ToolName: "write_file",
		Payload: map[string]interface{}{
			"policy": "read_only",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      "patch.decision",
		TraceID:   "trace-beta",
		AgentName: "agent-a",
		Payload: map[string]interface{}{
			"patch_decision":        "approved_override",
			"patch_decision_policy": "strict",
			"patch_approval": map[string]interface{}{
				"ticket_id": "CAB-100",
			},
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:     "tool.reduced",
		TraceID:  "trace-beta",
		ToolName: "run_tests",
		Payload: map[string]interface{}{
			"reducer":            "go_test_json",
			"artifact_ref_count": 1,
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "subagent.batch.started",
		TraceID: "trace-beta",
	})
	bus.Publish(runtimeevents.Event{
		Type:      "context.profile.injected",
		TraceID:   "trace-beta",
		SessionID: "session-b",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces?trace_prefix=trace-&event_type=mcp.&limit=10", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["count"])

	traces := payload["traces"].([]interface{})
	require.Len(t, traces, 1)
	trace := traces[0].(map[string]interface{})
	assert.Equal(t, "trace-beta", trace["trace_id"])
	assert.Equal(t, float64(6), trace["event_count"])
	assert.Contains(t, trace["mcp_names"].([]interface{}), "echo-test")
	assert.Contains(t, trace["transport_types"].([]interface{}), "websocket")
	governance := trace["governance"].(map[string]interface{})
	assert.Equal(t, float64(1), governance["denied_events"])
	assert.Equal(t, float64(1), governance["tool_denied"])
	assert.Equal(t, float64(1), governance["patch_decisions"])
	assert.Equal(t, float64(1), governance["patch_approved_override"])
	assert.Equal(t, float64(1), governance["patch_approvals_with_ticket"])
	assert.Contains(t, trace["patch_approval_tickets"].([]interface{}), "CAB-100")
	policies := governance["policies"].(map[string]interface{})
	assert.Equal(t, float64(1), policies["read_only"])
	patchPolicies := governance["patch_policies"].(map[string]interface{})
	assert.Equal(t, float64(1), patchPolicies["strict"])
	execution := trace["execution"].(map[string]interface{})
	assert.Equal(t, float64(1), execution["tool_reduced"])
	assert.Equal(t, float64(1), execution["artifact_refs"])
	assert.Equal(t, float64(1), execution["subagent_batches"])
	provenance := trace["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), provenance["profile_context_injected"])
	assert.Contains(t, provenance["profile_resource_refs"].([]interface{}), "profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json")
	assert.Equal(t, float64(2), provenance["profile_resource_count"])
	assert.Equal(t, float64(1), provenance["profile_memory_count"])
	assert.Equal(t, float64(1), provenance["profile_notes_count"])
	assert.Contains(t, provenance["profile_resource_labels"].([]interface{}), "memory:memory.json")
	kinds := provenance["profile_resource_kinds"].(map[string]interface{})
	assert.Equal(t, float64(1), kinds["memory"])
	assert.Equal(t, float64(1), kinds["notes"])
}

func TestGetRuntimeTraceStats_ReturnsAggregates(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:      "tool.requested",
		TraceID:   "trace-alpha",
		AgentName: "agent-a",
		SessionID: "session-a",
		ToolName:  "read_logs",
	})
	bus.Publish(runtimeevents.Event{
		Type:      "mcp.transport.connected",
		TraceID:   "trace-alpha",
		AgentName: "mcp-manager",
		Payload: map[string]interface{}{
			"mcp_name":       "echo-test",
			"transport_type": "websocket",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      "tool.completed",
		TraceID:   "trace-beta",
		AgentName: "agent-b",
		SessionID: "session-b",
		ToolName:  "run_tests",
	})
	bus.Publish(runtimeevents.Event{
		Type:    "subagent.denied",
		TraceID: "trace-beta",
		Payload: map[string]interface{}{
			"policy": "single_writer",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "patch.decision",
		TraceID: "trace-beta",
		Payload: map[string]interface{}{
			"patch_decision":        "blocked",
			"patch_decision_policy": "warn",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:     "tool.reduced",
		TraceID:  "trace-beta",
		ToolName: "run_tests",
		Payload: map[string]interface{}{
			"reducer":            "go_test_json",
			"artifact_ref_count": 1,
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "subagent.batch.started",
		TraceID: "trace-beta",
	})
	bus.Publish(runtimeevents.Event{
		Type:    "subagent.started",
		TraceID: "trace-beta",
		Payload: map[string]interface{}{
			"role": "verifier",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "recall.performed",
		TraceID: "trace-beta",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/stats?trace_prefix=trace-&limit=10", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	stats := payload["stats"].(map[string]interface{})
	assert.Equal(t, float64(2), stats["trace_count"])
	assert.Equal(t, float64(9), stats["event_count"])

	eventTypes := stats["event_types"].(map[string]interface{})
	assert.Equal(t, float64(1), eventTypes["tool.requested"])
	assert.Equal(t, float64(1), eventTypes["mcp.transport.connected"])

	agents := stats["agents"].(map[string]interface{})
	assert.Equal(t, float64(1), agents["agent-a"])
	assert.Equal(t, float64(1), agents["agent-b"])

	mcps := stats["mcp_names"].(map[string]interface{})
	assert.Equal(t, float64(1), mcps["echo-test"])

	transports := stats["transport_types"].(map[string]interface{})
	assert.Equal(t, float64(1), transports["websocket"])

	governance := stats["governance"].(map[string]interface{})
	assert.Equal(t, float64(1), governance["denied_events"])
	assert.Equal(t, float64(1), governance["subagent_denied"])
	assert.Equal(t, float64(1), governance["patch_decisions"])
	assert.Equal(t, float64(1), governance["patch_blocked"])
	policies := governance["policies"].(map[string]interface{})
	assert.Equal(t, float64(1), policies["single_writer"])
	patchPolicies := governance["patch_policies"].(map[string]interface{})
	assert.Equal(t, float64(1), patchPolicies["warn"])
	patchGovernance := payload["patch_governance"].(map[string]interface{})
	assert.Equal(t, float64(1), patchGovernance["decisions"])
	assert.Equal(t, float64(1), patchGovernance["blocked"])
	assert.Equal(t, float64(0), patchGovernance["approved_override"])
	execution := stats["execution"].(map[string]interface{})
	assert.Equal(t, float64(1), execution["tool_reduced"])
	assert.Equal(t, float64(1), execution["artifact_refs"])
	assert.Equal(t, float64(1), execution["subagent_batches"])
	assert.Equal(t, float64(1), execution["subagent_started"])
	assert.Equal(t, float64(1), execution["reducers"].(map[string]interface{})["go_test_json"])
	assert.Equal(t, float64(1), execution["subagent_roles"].(map[string]interface{})["verifier"])
	provenance := stats["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), provenance["recall_with_source_refs"])
	assert.Contains(t, provenance["profile_resource_refs"].([]interface{}), "profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json")
	assert.Equal(t, float64(1), provenance["profile_resource_count"])
	assert.Equal(t, float64(1), provenance["profile_memory_count"])
	assert.Contains(t, provenance["profile_resource_labels"].([]interface{}), "memory:memory.json")

	latestTraceIDs := stats["latest_trace_ids"].([]interface{})
	require.Len(t, latestTraceIDs, 2)
	assert.Equal(t, "trace-beta", latestTraceIDs[0])
}

func TestListRuntimeEvents_FiltersByProfileResourceKind(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:    "context.profile.injected",
		TraceID: "trace-memory-event",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "recall.performed",
		TraceID: "trace-notes-event",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/events?profile_resource_kind=notes&limit=10", nil)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["count"])
	filters := payload["filters"].(map[string]interface{})
	assert.Equal(t, "notes", filters["profile_resource_kind"])
	provenance := payload["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), provenance["profile_notes_count"])
	assert.Equal(t, float64(1), provenance["profile_resource_count"])
	events := payload["events"].([]interface{})
	require.Len(t, events, 1)
	assert.Equal(t, "trace-notes-event", events[0].(map[string]interface{})["trace_id"])
}

func TestGetRuntimeTraces_FiltersByToolName(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:     "tool.requested",
		TraceID:  "trace-alpha",
		ToolName: "read_logs",
	})
	bus.Publish(runtimeevents.Event{
		Type:     "tool.reduced",
		TraceID:  "trace-beta",
		ToolName: "run_tests",
		Payload: map[string]interface{}{
			"reducer":            "go_test_json",
			"artifact_ref_count": 1,
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces?tool_name=run_tests&limit=10", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["count"])
	filters := payload["filters"].(map[string]interface{})
	assert.Equal(t, "run_tests", filters["tool_name"])
	traces := payload["traces"].([]interface{})
	require.Len(t, traces, 1)
	trace := traces[0].(map[string]interface{})
	assert.Equal(t, "trace-beta", trace["trace_id"])
	execution := trace["execution"].(map[string]interface{})
	assert.Equal(t, float64(1), execution["tool_reduced"])
}

func TestGetRuntimeTraceGovernance_ReturnsDeniedView(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:      "tool.denied",
		TraceID:   "trace-denied",
		AgentName: "agent-a",
		SessionID: "session-a",
		ToolName:  "write_file",
		Payload: map[string]interface{}{
			"policy":   "read_only",
			"reason":   "read-only policy blocks write-like tool: write_file",
			"mcp_name": "local-mcp",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      "patch.decision",
		TraceID:   "trace-audit",
		AgentName: "agent-b",
		SessionID: "session-b",
		ToolName:  "spawn_subagents",
		Payload: map[string]interface{}{
			"patch_decision":        "approved_override",
			"patch_decision_policy": "strict",
			"patch_approval": map[string]interface{}{
				"ticket_id": "CAB-22",
				"approver":  "ops",
			},
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "tool.completed",
		TraceID: "trace-ok",
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/governance?trace_prefix=trace-&limit=10", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(2), payload["count"])

	stats := payload["stats"].(map[string]interface{})
	assert.Equal(t, float64(2), stats["trace_count"])
	assert.Equal(t, float64(1), stats["denied_events"])
	assert.Equal(t, float64(1), stats["tool_denied"])
	assert.Equal(t, float64(1), stats["patch_decisions"])
	assert.Equal(t, float64(1), stats["patch_approved_override"])
	assert.Equal(t, float64(1), stats["patch_approvals_with_ticket"])
	patchGovernance := payload["patch_governance"].(map[string]interface{})
	assert.Equal(t, float64(1), patchGovernance["decisions"])
	assert.Equal(t, float64(1), patchGovernance["approved_override"])
	assert.Equal(t, float64(1), patchGovernance["approvals_with_ticket"])
	policies := stats["policies"].(map[string]interface{})
	assert.Equal(t, float64(1), policies["read_only"])
	patchPolicies := stats["patch_policies"].(map[string]interface{})
	assert.Equal(t, float64(1), patchPolicies["strict"])
	reasons := stats["reasons"].(map[string]interface{})
	assert.Equal(t, float64(1), reasons["read-only policy blocks write-like tool: write_file"])

	traces := payload["traces"].([]interface{})
	require.Len(t, traces, 2)
	assert.Equal(t, "trace-audit", traces[0].(map[string]interface{})["trace_id"])
	assert.Equal(t, "trace-denied", traces[1].(map[string]interface{})["trace_id"])
	assert.Contains(t, traces[0].(map[string]interface{})["patch_approval_tickets"].([]interface{}), "CAB-22")
	auditGovernance := traces[0].(map[string]interface{})["governance"].(map[string]interface{})
	assert.Equal(t, float64(1), auditGovernance["patch_decisions"])
	assert.Equal(t, float64(1), auditGovernance["patch_approved_override"])
	governance := traces[1].(map[string]interface{})["governance"].(map[string]interface{})
	assert.Equal(t, float64(1), governance["denied_events"])
}

func TestGetRuntimeTraceGovernance_FiltersByProfileResourceKind(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{
		Type:    "patch.decision",
		TraceID: "trace-memory",
		Payload: map[string]interface{}{
			"patch_decision": "blocked",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "checkpoint_created",
		TraceID: "trace-memory",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "tool.denied",
		TraceID: "trace-notes",
		Payload: map[string]interface{}{
			"policy": "read_only",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:    "recall.performed",
		TraceID: "trace-notes",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/governance?trace_prefix=trace-&profile_resource_kind=memory&limit=10", nil)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["count"])
	provenance := payload["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), provenance["profile_memory_count"])
	assert.Equal(t, float64(1), provenance["profile_resource_count"])
	traces := payload["traces"].([]interface{})
	require.Len(t, traces, 1)
	assert.Equal(t, "trace-memory", traces[0].(map[string]interface{})["trace_id"])
}

func TestApplyAgentExecutionPolicy_ConfiguresReadOnlySandbox(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetMutationPolicy(MutationPolicy{ReadOnly: true})

	agentInstance := handler.newAPIAgent(&agent.Config{
		Name:     "api-agent",
		Model:    "test-model",
		MaxSteps: 3,
	})

	workspacePath := t.TempDir()
	handler.applyAgentExecutionPolicy(agentInstance, workspacePath, nil, nil)

	policy := agentInstance.GetToolExecutionPolicy()
	require.NotNil(t, policy)
	assert.True(t, policy.ReadOnly)
	require.NotNil(t, policy.Sandbox)
	sandboxCfg := policy.Sandbox.Config()
	assert.Equal(t, []string{workspacePath}, sandboxCfg.AllowedPaths)
	assert.Equal(t, []string{workspacePath}, sandboxCfg.ReadOnlyPaths)
	assert.Contains(t, sandboxCfg.DeniedCommands, "bash")
	assert.Contains(t, sandboxCfg.DeniedCommands, "powershell")
}

func TestApplyAgentExecutionPolicy_MergesRuntimeSandboxConfig(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	agentInstance := handler.newAPIAgent(&agent.Config{
		Name:     "api-agent",
		Model:    "test-model",
		MaxSteps: 3,
	})

	runtimeCfg := runtimecfg.DefaultRuntimeConfig()
	runtimeCfg.Sandbox = runtimeexecutor.SandboxConfig{
		Enabled:          true,
		MaxExecutionTime: 15 * time.Second,
		AllowedCommands:  []string{"git"},
		DeniedCommands:   []string{"powershell"},
		EnvWhitelist:     []string{"PATH"},
		AllowedHosts:     []string{"example.com"},
		DeniedHosts:      []string{"localhost"},
	}

	handler.applyAgentExecutionPolicy(agentInstance, "", runtimeCfg, nil)

	policy := agentInstance.GetToolExecutionPolicy()
	require.NotNil(t, policy)
	require.NotNil(t, policy.Sandbox)
	sandboxCfg := policy.Sandbox.Config()
	assert.Equal(t, 15*time.Second, sandboxCfg.MaxExecutionTime)
	assert.Equal(t, []string{"git"}, sandboxCfg.AllowedCommands)
	assert.Equal(t, []string{"powershell"}, sandboxCfg.DeniedCommands)
	assert.Equal(t, []string{"PATH"}, sandboxCfg.EnvWhitelist)
	assert.Equal(t, []string{"example.com"}, sandboxCfg.AllowedHosts)
	assert.Equal(t, []string{"localhost"}, sandboxCfg.DeniedHosts)
}

func TestAgentChat_ProfileInjectsSystemPrompt(t *testing.T) {
	profileRoot := t.TempDir()
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: tester
agents:
  tester: {}
`)
	writeProfileFile(filepath.Join(profileRoot, "runtime.yaml"), "agent:\n  defaultModel: test-model\n")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "system.md"), "Profile system prompt.")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "memory", "memory.json"), `{"summary":"cached profile memory"}`)
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "context", "notes.md"), "Profile investigation notes.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	provider := &testLLMProvider{name: "test-model", content: "hello from llm"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":%q,"messages":[{"role":"user","content":"hi"}]}`, profileRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)
	messages := provider.requests[0].Messages
	require.NotEmpty(t, messages)
	foundSystem := false
	foundRuntimeContext := false
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, "Profile system prompt.") {
			foundSystem = true
		}
		if message.Role == "developer" &&
			strings.Contains(message.Content, "cached profile memory") &&
			strings.Contains(message.Content, "Profile investigation notes.") {
			foundRuntimeContext = true
		}
	}
	assert.True(t, foundSystem, "expected structured system instruction message")
	assert.True(t, foundRuntimeContext, "expected structured developer context instruction message")
}

func TestAgentChat_CodexBuildsStructuredInstructionMessagesFromProfileAndWorkspace(t *testing.T) {
	profileRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: tester
agents:
  tester: {}
`)
	writeProfileFile(filepath.Join(profileRoot, "runtime.yaml"), "agent:\n  defaultModel: test-model\n")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "system.md"), "Profile system prompt.")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "tools.md"), "Prefer rg when available.")
	writeProfileFile(filepath.Join(workspaceRoot, "AGENTS.md"), "Stay within the workspace boundary.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	provider := &testLLMProvider{name: "codex", content: "hello from llm"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultProvider: "codex", DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("codex", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":%q,"provider":"codex","workspace_path":%q,"messages":[{"role":"user","content":"hi"}]}`, profileRoot, workspaceRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)

	messages := provider.requests[0].Messages
	foundSystem := false
	foundDeveloper := false
	foundUser := false
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, "Profile system prompt.") {
			foundSystem = true
			assert.Equal(t, "base", message.Metadata["prompt_layer"])
		}
		if message.Role == "developer" && strings.Contains(message.Content, "Prefer rg when available.") {
			foundDeveloper = true
			assert.Equal(t, "developer", message.Metadata["prompt_layer"])
		}
		if message.Role == "developer" && strings.Contains(message.Content, "Stay within the workspace boundary.") {
			foundUser = true
			assert.Equal(t, "user", message.Metadata["prompt_layer"])
		}
	}
	assert.True(t, foundSystem, "expected structured system instruction message")
	assert.True(t, foundDeveloper, "expected structured developer instruction message")
	assert.True(t, foundUser, "expected structured user instruction message")
	layout, _ := provider.requests[0].Metadata["prompt_layout"].(string)
	assert.Contains(t, layout, "[base/system]")
	assert.Contains(t, layout, "[developer/developer]")
	assert.Contains(t, layout, "[user/developer]")
}

func TestAgentChat_ProfileSetsSessionContext(t *testing.T) {
	profileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(profileRoot, "profile.yaml"), []byte(`profile:
  name: dev
  default_agent: tester
agents:
  tester: {}
`), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	provider := &testLLMProvider{name: "test-model", content: "hello from llm"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":%q,"messages":[{"role":"user","content":"hi"}]}`, profileRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	sessionID := payload["session_id"].(string)
	require.NotEmpty(t, sessionID)

	session, err := handler.sessionManager.Get(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, session.Metadata.Context)
	assert.Equal(t, profileRoot, session.Metadata.Context[apiProfileContextReference])
	assert.Equal(t, "dev", session.Metadata.Context[apiProfileContextName])
	assert.Equal(t, "tester", session.Metadata.Context[apiProfileContextAgent])
	absRoot, err := filepath.Abs(profileRoot)
	require.NoError(t, err)
	assert.Equal(t, absRoot, session.Metadata.Context[apiProfileContextRoot])
}

func TestStreamLLMChat_AttachesSessionMetadataForPromptCaching(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	provider := &testLLMProvider{name: "test-model", content: "hello from llm"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	session := chat.NewSession("user-1")
	recorder := httptest.NewRecorder()
	err := handler.streamLLMChat(
		context.Background(),
		recorder,
		session,
		"agent-1",
		"test-model",
		"hello",
		[]types.Message{{Role: "user", Content: "hello"}},
		"",
		nil,
		false,
		nil,
		"",
		UsageScope{},
		0,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, provider.requests)
	require.NotNil(t, provider.requests[0].Metadata)
	assert.Equal(t, session.ID, provider.requests[0].Metadata["session_id"])
}

func TestAgentChat_ProfileAddsContextPackLayerToRuntimeSummary(t *testing.T) {
	profileRoot := t.TempDir()
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: tester
agents:
  tester: {}
`)
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "system.md"), "Profile system prompt.")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "memory", "memory.json"), `{"summary":"cached profile memory"}`)
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "context", "notes.md"), "Profile investigation notes.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	provider := &testLLMProvider{name: "test-model", content: "hello from llm"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":%q,"messages":[{"role":"user","content":"hi"}],"enable_react":true}`, profileRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)

	found := false
	systemContents := make([]string, 0)
	profileAssistantFound := false
	for _, message := range provider.requests[0].Messages {
		if message.Role == "system" {
			systemContents = append(systemContents, message.Content)
			if strings.Contains(message.Content, "Runtime context summary:") &&
				strings.Contains(message.Content, `"context_pack"`) &&
				strings.Contains(message.Content, `"profile"`) &&
				strings.Contains(message.Content, `cached profile memory`) &&
				strings.Contains(message.Content, `Profile investigation notes.`) {
				found = true
			}
		}
		if message.Role == "assistant" &&
			strings.Contains(message.Content, "Profile context:") &&
			strings.Contains(message.Content, "cached profile memory") &&
			strings.Contains(message.Content, "Profile investigation notes.") {
			profileAssistantFound = true
		}
	}
	assert.True(t, found, "expected runtime context summary to include profile context pack layer\n%s", strings.Join(systemContents, "\n---\n"))
	assert.True(t, profileAssistantFound, "expected explicit profile context message in ReAct request")
}

func TestSessionActor_UsesProfileContextFromSession(t *testing.T) {
	profileRoot := t.TempDir()
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: tester
agents:
  tester: {}
`)
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "system.md"), "Profile system prompt.")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "memory", "memory.json"), `{"summary":"cached profile memory"}`)
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "context", "notes.md"), "Profile investigation notes.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	provider := &testLLMProvider{name: "test-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	session, err := handler.sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)
	session.SetContext(apiProfileContextReference, profileRoot)
	require.NoError(t, handler.sessionManager.Update(context.Background(), session))

	actor, err := handler.buildSessionActor(session.ID)
	require.NoError(t, err)
	defer actor.Stop()

	_, err = actor.SubmitPrompt(context.Background(), "hi", nil)
	require.NoError(t, err)

	require.NotEmpty(t, provider.requests)
	found := false
	profileContextFound := false
	for _, message := range provider.requests[0].Messages {
		if message.Role == "system" &&
			strings.Contains(message.Content, "Profile system prompt.") {
			found = true
		}
		if message.Role == "assistant" &&
			strings.Contains(message.Content, "Profile context:") &&
			strings.Contains(message.Content, "cached profile memory") &&
			strings.Contains(message.Content, "Profile investigation notes.") {
			profileContextFound = true
		}
	}
	assert.True(t, found, "expected profile system prompt in session actor request")
	assert.True(t, profileContextFound, "expected explicit profile context layer in session actor request")
}

func TestBuildSessionActor_DoesNotAutoScanDefaultWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package demo\nfunc SearchDocs() {}\n"), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	provider := &testLLMProvider{name: "test-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = tmpDir
	handler.SetRuntimeConfig(runtimeConfig, "")

	session, err := handler.sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)

	actor, err := handler.buildSessionActor(session.ID)
	require.NoError(t, err)

	_, err = actor.SubmitPrompt(context.Background(), "search docs", nil)
	require.NoError(t, err)

	require.NotEmpty(t, provider.requests)
	for _, message := range provider.requests[0].Messages {
		if message.Role != "system" {
			continue
		}
		assert.NotContains(t, message.Content, "Workspace context:")
		assert.NotContains(t, message.Content, `"workspace_path"`)
	}
}

func TestBuildSessionActor_DoesNotRefreshRuntimeToolCatalog(t *testing.T) {
	mcpManager := &blockingCatalogMCPManager{
		listStarted: make(chan struct{}),
		release:     make(chan struct{}),
	}
	defer close(mcpManager.release)

	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	provider := &testLLMProvider{name: "test-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	session, err := handler.sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)

	type actorResult struct {
		actor *chat.SessionActor
		err   error
	}
	done := make(chan actorResult, 1)
	go func() {
		actor, buildErr := handler.buildSessionActor(session.ID)
		done <- actorResult{actor: actor, err: buildErr}
	}()

	select {
	case result := <-done:
		require.NoError(t, result.err)
	case <-mcpManager.listStarted:
		t.Fatal("buildSessionActor unexpectedly refreshed the runtime tool catalog")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("buildSessionActor did not return promptly")
	}
}

func TestAgentChat_ProfileAutoRoutesToWorkspaceProfile(t *testing.T) {
	workspaceRoot := t.TempDir()
	profileRoot := filepath.Join(workspaceRoot, ".gagent", "profiles", "explore")
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: explore
  default_agent: explorer
agents:
  explorer: {}
`)
	writeProfileFile(filepath.Join(profileRoot, "agents", "explorer", "prompts", "system.md"), "Explore profile prompt.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	provider := &testLLMProvider{name: "test-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":"auto","workspace_path":%q,"messages":[{"role":"user","content":"search docs"}]}`, workspaceRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)
	messages := provider.requests[0].Messages
	require.NotEmpty(t, messages)
	assert.True(t, messageListContainsText(messages, "Explore profile prompt."))
}

func TestAgentChat_ProfileAutoPrefersWorkspaceProfileOverRegistry(t *testing.T) {
	workspaceRoot := t.TempDir()
	registryRoot := t.TempDir()
	workspaceProfile := filepath.Join(workspaceRoot, ".gagent", "profiles", "explore")
	registryProfile := filepath.Join(registryRoot, "explore")
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(workspaceProfile, "profile.yaml"), `profile:
  name: explore
  default_agent: explorer
agents:
  explorer: {}
`)
	writeProfileFile(filepath.Join(workspaceProfile, "agents", "explorer", "prompts", "system.md"), "Workspace explore prompt.")

	writeProfileFile(filepath.Join(registryProfile, "profile.yaml"), `profile:
  name: explore
  default_agent: explorer
agents:
  explorer: {}
`)
	writeProfileFile(filepath.Join(registryProfile, "agents", "explorer", "prompts", "system.md"), "Registry explore prompt.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(registryRoot),
	})

	provider := &testLLMProvider{name: "test-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":"auto","workspace_path":%q,"messages":[{"role":"user","content":"search docs"}]}`, workspaceRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)
	messages := provider.requests[0].Messages
	require.NotEmpty(t, messages)
	assert.True(t, messageListContainsText(messages, "Workspace explore prompt."))
	assert.False(t, messageListContainsText(messages, "Registry explore prompt."))
}

func TestAgentChat_InvalidWorkspacePathReturnsBadRequest(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from llm",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"workspace_path":"Z:/definitely/not/exist"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to scan workspace path")
}

func TestAgentChat_WorkspacePathBuildsContextAndStillUsesLLM(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(`package demo
func SearchDocs() {}
`), 0o644))

	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello from llm",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"search docs"}],"workspace_path":"` + strings.ReplaceAll(tmpDir, `\`, `\\`) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "hello from llm")
}

func messageListContainsText(messages []types.Message, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

func TestAgentChat_PlannerPreferredIncludesPlan(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-skill",
		Description: "planned workflow skill",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "workflow"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan workflow for me"}],"enable_routing":true,"planning_mode":"planner_preferred"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, "planner_preferred", planning["mode"])
	assert.Equal(t, true, planning["attempted"])
	assert.Equal(t, "workflow", planning["planning_source"])
	assert.Equal(t, float64(1), planning["step_count"])
	assert.Equal(t, float64(1), planning["subagent_task_count"])

	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, true, orchestration["planning_attempted"])
	assert.Equal(t, "workflow", orchestration["planning_source"])
	assert.Equal(t, float64(1), orchestration["plan_step_count"])
	assert.Equal(t, float64(1), orchestration["subagent_task_count"])
}

func TestAgentChat_PlannerPreferredIncludesSubagentTasks(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-writer-skill",
		Description: "planned workflow skill with writer and verifier",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "writer", "verify"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{
				ID:   "step_write",
				Name: "write implementation",
				Tool: "write_file",
			},
			{
				ID:        "step_verify",
				Name:      "verify changes",
				Tool:      "run_tests",
				DependsOn: []string{"step_write"},
			},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan writer verify flow"}],"enable_routing":true,"planning_mode":"planner_preferred"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, float64(2), planning["subagent_task_count"])
	subagentTasks := planning["subagent_tasks"].([]interface{})
	require.Len(t, subagentTasks, 2)
	firstTask := subagentTasks[0].(map[string]interface{})
	secondTask := subagentTasks[1].(map[string]interface{})
	assert.Equal(t, "writer", firstTask["role"])
	assert.Equal(t, "verifier", secondTask["role"])
	assert.Contains(t, secondTask["depends_on"].([]interface{}), "step_write")
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, float64(2), orchestration["subagent_task_count"])
}

func TestAgentChat_PlannerPreferredCanExecutePlannedSubagents(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-exec-skill",
		Description: "planned workflow skill with execution",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "execute", "verify"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Verify implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "run_tests", Args: map[string]interface{}{"target": "./..."}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan execute verify flow"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "agent_planned_subagents", result["source"])
	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, true, planning["subagent_execution_requested"])
	assert.Equal(t, true, planning["subagent_execution_eligible"])
	assert.Equal(t, true, planning["subagent_execution_attempted"])
	assert.Equal(t, float64(2), planning["subagent_result_count"])
	assert.Equal(t, float64(1), planning["subagent_patch_count"])
	assert.Equal(t, float64(1), planning["subagent_applied_patch_count"])
	assert.Equal(t, float64(1), planning["subagent_verified_patch_count"])
	assert.Equal(t, float64(0), planning["subagent_needs_review_patch_count"])
	assert.Equal(t, "approved", planning["patch_decision"])
	assert.Equal(t, true, planning["patch_decision_required"])
	subagentSummary := result["subagent_summary"].(map[string]interface{})
	assert.Equal(t, float64(2), subagentSummary["count"])
	assert.Contains(t, subagentSummary["roles"].([]interface{}), "writer")
	assert.Contains(t, subagentSummary["roles"].([]interface{}), "verifier")
	assert.Equal(t, float64(1), subagentSummary["applied_patch_count"])
	assert.Equal(t, float64(1), subagentSummary["verified_patch_count"])
	assert.Equal(t, float64(0), subagentSummary["needs_review_patch_count"])
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, true, orchestration["subagent_execution_requested"])
	assert.Equal(t, true, orchestration["subagent_execution_eligible"])
	assert.Equal(t, true, orchestration["subagent_execution_attempted"])
	assert.Equal(t, "approved", orchestration["patch_decision"])
	assert.Equal(t, true, orchestration["route_matched"])
}

func TestAgentChat_PlannerPreferredPatchDecisionBlockedWhenVerifierFails(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-exec-skill-blocked",
		Description: "planned workflow skill with verifier failure",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "execute", "blocked"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Attempt writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "blocked"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan execute blocked flow"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "blocked", payload["status"])
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "agent_planned_subagents", result["source"])
	assert.Equal(t, false, result["success"])

	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, "blocked", planning["patch_decision"])
	assert.Equal(t, true, planning["patch_decision_required"])
	assert.Equal(t, float64(1), planning["subagent_patch_count"])
	assert.Equal(t, float64(0), planning["subagent_verified_patch_count"])
	assert.Equal(t, float64(1), planning["subagent_needs_review_patch_count"])
	assert.Contains(t, planning["patch_decision_reason"], "requires manual review")

	subagentSummary := result["subagent_summary"].(map[string]interface{})
	assert.Equal(t, float64(1), subagentSummary["needs_review_patch_count"])

	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, "blocked", orchestration["patch_decision"])
	assert.Contains(t, orchestration["subagent_execution_blocked_reason"], "requires manual review")
}

func TestAgentChat_PlannerPreferredPatchDecisionWarnPolicy(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-exec-skill-warn",
		Description: "planned workflow skill with verifier failure",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "warn", "flow"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Attempt writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "blocked"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan warn flow"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true,"patch_decision_policy":"warn"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "completed", payload["status"])
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, true, result["success"])

	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, "blocked", planning["patch_decision"])
	assert.Equal(t, "warn", planning["patch_decision_policy"])

	orchestration := result["orchestration"].(map[string]interface{})
	blockedReason, _ := orchestration["subagent_execution_blocked_reason"].(string)
	assert.Empty(t, blockedReason)
}

func TestAgentChat_PlannerPreferredPatchDecisionManualOverride(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-exec-skill-override",
		Description: "planned workflow skill with verifier failure",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "override", "flow"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Attempt writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "blocked"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan override flow"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true,"approve_blocked_patches":true,"patch_approval_note":"human reviewed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "completed", payload["status"])
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, true, result["success"])

	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, "approved_override", planning["patch_decision"])
	assert.Equal(t, true, planning["patch_decision_required"])
	assert.Equal(t, true, planning["patch_decision_override_applied"])
	assert.Contains(t, planning["patch_decision_reason"], "human reviewed")
}

func TestAgentChat_PlannerPreferredPatchApprovalObjectReturned(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-exec-skill-approval-object",
		Description: "planned workflow skill with verifier failure",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "approval", "object"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Attempt writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "blocked"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan approval object"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true,"patch_approval":{"approved":true,"ticket_id":"CAB-456","approver":"release-manager","reason":"approved during CAB"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, "approved_override", planning["patch_decision"])
	assert.Equal(t, true, planning["patch_decision_override_applied"])
	approval := planning["patch_approval"].(map[string]interface{})
	assert.Equal(t, "CAB-456", approval["ticket_id"])
	assert.Equal(t, "release-manager", approval["approver"])
	assert.Equal(t, "approved during CAB", approval["reason"])

	traceID := result["trace_id"].(string)
	events := handler.getRuntimeEventBus().Trace(traceID, 20)
	require.NotEmpty(t, events)
	var patchDecisionEvent *runtimeevents.Event
	for i := range events {
		if events[i].Type == "patch.decision" {
			patchDecisionEvent = &events[i]
			break
		}
	}
	require.NotNil(t, patchDecisionEvent)
	assert.Equal(t, "approved_override", patchDecisionEvent.Payload["patch_decision"])
	assert.Equal(t, true, patchDecisionEvent.Payload["patch_decision_override"])
	approvalPayload := patchDecisionEvent.Payload["patch_approval"].(map[string]interface{})
	assert.Equal(t, "CAB-456", approvalPayload["ticket_id"])
	assert.Equal(t, "release-manager", approvalPayload["approver"])
}

func TestAgentChat_PlannedSubagentExecutionBlockedWithoutWriteApproval(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-write-skill",
		Description: "planned workflow skill with writer",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "write", "approval"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "fallback llm response",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan write approval flow"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, true, planning["subagent_execution_requested"])
	assert.Equal(t, false, planning["subagent_execution_eligible"])
	assert.Equal(t, false, planning["subagent_execution_attempted"])
	assert.Contains(t, planning["subagent_execution_blocked_reason"], "requires explicit write approval")
}

func TestAgentChat_PlannedSubagentExecutionBlockedByReadOnlyPolicy(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-readonly-skill",
		Description: "planned workflow skill with writer",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "blocked", "writer"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetMutationPolicy(MutationPolicy{ReadOnly: true})

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "fallback llm response",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan blocked writer flow"}],"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	planning := result["planning"].(map[string]interface{})
	assert.Equal(t, true, planning["subagent_execution_requested"])
	assert.Equal(t, false, planning["subagent_execution_eligible"])
	assert.Equal(t, false, planning["subagent_execution_attempted"])
	assert.Contains(t, planning["subagent_execution_blocked_reason"], "read-only policy blocks planned writer subagent execution")

	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, true, orchestration["subagent_execution_requested"])
	assert.Equal(t, false, orchestration["subagent_execution_eligible"])
	assert.Equal(t, false, orchestration["subagent_execution_attempted"])
}

func TestSessionEndpoints_Lifecycle(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions", bytes.NewReader([]byte(`{"user_id":"user-42","title":"demo"}`)))
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var createPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &createPayload))
	sessionData := createPayload["session"].(map[string]interface{})
	sessionIDValue := sessionData["id"].(string)
	require.NotEmpty(t, sessionIDValue)

	require.NoError(t, sessionManager.AddMessage(context.Background(), sessionIDValue, *types.NewUserMessage("hello")))
	require.NoError(t, sessionManager.AddMessage(context.Background(), sessionIDValue, *types.NewAssistantMessage("hi there")))

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions?user_id=user-42", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	assert.Contains(t, listRec.Body.String(), sessionIDValue)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+sessionIDValue, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Contains(t, getRec.Body.String(), "user-42")

	historyReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+sessionIDValue+"/history", nil)
	historyRec := httptest.NewRecorder()
	router.ServeHTTP(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code)
	assert.Contains(t, historyRec.Body.String(), "hello")
	assert.Contains(t, historyRec.Body.String(), "hi there")

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/stats?user_id=user-42", nil)
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)
	assert.Contains(t, statsRec.Body.String(), "totalMessages")

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/runtime/sessions/"+sessionIDValue, nil)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code)

	_, err := sessionManager.GetSession(context.Background(), sessionIDValue)
	assert.Error(t, err)
}

func TestGetSessionHistory_EmptyHistoryUsesEmptyArray(t *testing.T) {
	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.CreateSession(context.Background(), "empty-history-user")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/history", nil)
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.GetSessionHistory(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	history, ok := payload["history"].([]interface{})
	require.True(t, ok)
	assert.Len(t, history, 0)
	assert.Equal(t, float64(0), payload["count"])
}

func TestSessionEndpoints_SearchUpdateAndBatchOperations(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	createOne := func(userID, title string) string {
		body := []byte(fmt.Sprintf(`{"user_id":%q,"title":%q}`, userID, title))
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusCreated, rec.Code)
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		return payload["session"].(map[string]interface{})["id"].(string)
	}

	sessionOne := createOne("user-batch", "first")
	sessionTwo := createOne("user-batch", "second")

	require.NoError(t, sessionManager.AddTag(context.Background(), sessionOne, "support"))
	require.NoError(t, sessionManager.AddTag(context.Background(), sessionTwo, "support"))
	require.NoError(t, sessionManager.AddMessage(context.Background(), sessionOne, *types.NewUserMessage("hello")))
	require.NoError(t, sessionManager.AddMessage(context.Background(), sessionOne, *types.NewAssistantMessage("world")))

	updateReq := httptest.NewRequest(http.MethodPatch, "/api/runtime/sessions/"+sessionOne, bytes.NewReader([]byte(`{"title":"renamed","tags_add":["priority"],"context":{"ticket":"INC-42"},"state":"idle"}`)))
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	require.Equal(t, http.StatusOK, updateRec.Code)
	assert.Contains(t, updateRec.Body.String(), "renamed")
	assert.Contains(t, updateRec.Body.String(), "priority")
	assert.Contains(t, updateRec.Body.String(), "INC-42")

	searchReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/search", bytes.NewReader([]byte(`{"user_id":"user-batch","tags":["support"],"state":"idle"}`)))
	searchRec := httptest.NewRecorder()
	router.ServeHTTP(searchRec, searchReq)
	require.Equal(t, http.StatusOK, searchRec.Code)
	assert.Contains(t, searchRec.Body.String(), sessionOne)
	assert.NotContains(t, searchRec.Body.String(), sessionTwo)

	clearReq := httptest.NewRequest(http.MethodDelete, "/api/runtime/sessions/"+sessionOne+"/history", nil)
	clearRec := httptest.NewRecorder()
	router.ServeHTTP(clearRec, clearReq)
	require.Equal(t, http.StatusOK, clearRec.Code)
	updatedSession, err := sessionManager.GetSession(context.Background(), sessionOne)
	require.NoError(t, err)
	assert.Len(t, updatedSession.GetMessages(), 0)

	archiveReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionOne+"/archive", nil)
	archiveRec := httptest.NewRecorder()
	router.ServeHTTP(archiveRec, archiveReq)
	require.Equal(t, http.StatusOK, archiveRec.Code)
	assert.Contains(t, archiveRec.Body.String(), "archived")

	activateReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionOne+"/activate", nil)
	activateRec := httptest.NewRecorder()
	router.ServeHTTP(activateRec, activateReq)
	require.Equal(t, http.StatusOK, activateRec.Code)
	assert.Contains(t, activateRec.Body.String(), "active")

	closeReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionOne+"/close", nil)
	closeRec := httptest.NewRecorder()
	router.ServeHTTP(closeRec, closeReq)
	require.Equal(t, http.StatusOK, closeRec.Code)
	assert.Contains(t, closeRec.Body.String(), "closed")

	batchArchiveReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/batch/archive", bytes.NewReader([]byte(fmt.Sprintf(`{"session_ids":[%q,%q]}`, sessionOne, sessionTwo))))
	batchArchiveRec := httptest.NewRecorder()
	router.ServeHTTP(batchArchiveRec, batchArchiveReq)
	require.Equal(t, http.StatusOK, batchArchiveRec.Code)
	assert.Contains(t, batchArchiveRec.Body.String(), sessionOne)
	assert.Contains(t, batchArchiveRec.Body.String(), sessionTwo)

	batchDeleteReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/batch/delete", bytes.NewReader([]byte(fmt.Sprintf(`{"session_ids":[%q,%q]}`, sessionOne, sessionTwo))))
	batchDeleteRec := httptest.NewRecorder()
	router.ServeHTTP(batchDeleteRec, batchDeleteReq)
	require.Equal(t, http.StatusOK, batchDeleteRec.Code)
	assert.Contains(t, batchDeleteRec.Body.String(), sessionOne)
	assert.Contains(t, batchDeleteRec.Body.String(), sessionTwo)

	_, err = sessionManager.GetSession(context.Background(), sessionOne)
	assert.Error(t, err)
	_, err = sessionManager.GetSession(context.Background(), sessionTwo)
	assert.Error(t, err)
}

func TestRegisterRoutes_FixedPathsNotCapturedByNameRoute(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=test", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"query":"test"`)
}

func TestSearchSkills_AutoFallsBackToEmbedding(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "semantic-search-skill",
		Description: "search customer orders in sap",
		Category:    "erp",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=search%20customer%20orders%20in%20sap&category=erp", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "auto", payload["requested_mode"])
	assert.Equal(t, "semantic", payload["resolved_mode"])
	assert.Equal(t, true, payload["used_embedding"])
	assert.Equal(t, float64(1), payload["count"])

	matches := payload["matches"].([]interface{})
	require.Len(t, matches, 1)
	match := matches[0].(map[string]interface{})
	assert.Equal(t, "embedding", match["matched_by"])

	results := payload["results"].([]interface{})
	require.Len(t, results, 1)
	assert.Equal(t, "semantic-search-skill", results[0].(map[string]interface{})["name"])
}

func TestSearchSkills_LexicalModeSkipsEmbedding(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "semantic-only-skill",
		Description: "search customer orders in sap",
		Category:    "erp",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=search%20customer%20orders%20in%20sap&mode=lexical", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "lexical", payload["requested_mode"])
	assert.Equal(t, "lexical", payload["resolved_mode"])
	assert.Equal(t, false, payload["used_embedding"])
	assert.Equal(t, float64(0), payload["count"])
}

func TestGetStats_IncludesEmbeddingStats(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "stats-skill",
		Description: "search customer orders in sap",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	embeddingPayload := payload["embedding"].(map[string]interface{})
	assert.Equal(t, true, embeddingPayload["enabled"])
	stats := embeddingPayload["stats"].(map[string]interface{})
	assert.Equal(t, float64(1), stats["indexSize"])
	searchPayload := payload["search"].(map[string]interface{})
	assert.Equal(t, float64(0), searchPayload["total_requests"])
}

func TestGetStats_IncludesRuntimeStatus(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultProvider: "test-provider", DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-provider", &testLLMProvider{
		name:    "test-provider",
		content: "ok",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	runtimePayload := payload["runtime"].(map[string]interface{})
	assert.Equal(t, "test-provider", runtimePayload["default_provider"])
	assert.Equal(t, "test-model", runtimePayload["default_model"])
	assert.Equal(t, float64(1), runtimePayload["provider_count"])
	assert.Equal(t, float64(1), runtimePayload["mcp_count"])
	mcps := runtimePayload["mcps"].([]interface{})
	first := mcps[0].(map[string]interface{})
	assert.Equal(t, "local", first["trust_level"])
	assert.Equal(t, "local_mcp", first["execution_mode"])
}

func TestGetRuntimeStatus_RequiresAdmin(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")
	runtimeCfg := runtimecfg.DefaultRuntimeConfig()
	runtimeCfg.Catalog.Backend = "sqlite"
	runtimeCfg.Catalog.SnapshotPath = filepath.Join(t.TempDir(), "runtime-catalog.sqlite")
	handler.SetRuntimeConfig(runtimeCfg, "")
	defer func() {
		if handler.runtimeToolCatalog != nil {
			_ = handler.runtimeToolCatalog.Close()
		}
	}()
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:    "patch.decision",
		TraceID: "trace-status",
		Payload: map[string]interface{}{
			"patch_decision":        "approved_override",
			"patch_decision_policy": "strict",
			"patch_approval": map[string]interface{}{
				"ticket_id": "CAB-900",
			},
		},
	})
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:    "context.profile.injected",
		TraceID: "trace-status",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	rec := httptest.NewRecorder()
	handler.GetRuntimeStatus(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	authorizedReq := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	authorizedReq.Header.Set("X-Forwarded-For", "10.0.0.5")
	authorizedReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	authorizedRec := httptest.NewRecorder()
	handler.GetRuntimeStatus(authorizedRec, authorizedReq)
	require.Equal(t, http.StatusOK, authorizedRec.Code)
	assert.Contains(t, authorizedRec.Body.String(), `"runtime"`)
	assert.Contains(t, authorizedRec.Body.String(), `"patch_governance"`)
	assert.Contains(t, authorizedRec.Body.String(), `"tool_catalog"`)
	assert.Contains(t, authorizedRec.Body.String(), `"backend":"sqlite"`)
	assert.Contains(t, authorizedRec.Body.String(), `"approvals_with_ticket":1`)
	assert.Contains(t, authorizedRec.Body.String(), `"context"`)
	assert.Contains(t, authorizedRec.Body.String(), `"provenance"`)
	assert.Contains(t, authorizedRec.Body.String(), `"profile_context_injected":1`)
	assert.Contains(t, authorizedRec.Body.String(), `"profile_resource_count":1`)
	assert.Contains(t, authorizedRec.Body.String(), `"profile_resource_labels":["memory:memory.json"]`)
	_, err := os.Stat(runtimeCfg.Catalog.SnapshotPath)
	require.NoError(t, err)
}

func TestGetRuntimeModels_ListsProviderAliases(t *testing.T) {
	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "openai",
		DefaultModel:    "gpt-4o",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider("openai", &testLLMProvider{
		name:    "openai",
		content: "openai response",
	}))
	require.NoError(t, runtime.RegisterProvider("anthropic", &testLLMProvider{
		name:    "anthropic",
		content: "anthropic response",
	}))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4o", "openai"))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4.1", "openai"))
	require.NoError(t, runtime.RegisterProviderAlias("claude-3-7-sonnet", "anthropic"))
	handler.SetLLMRuntime(runtime)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/models", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.20")
	rec := httptest.NewRecorder()

	handler.GetRuntimeModels(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "openai", payload["default_provider"])
	assert.Equal(t, "gpt-4o", payload["default_model"])
	assert.Equal(t, float64(3), payload["count"])

	providers, ok := payload["providers"].([]interface{})
	require.True(t, ok)
	require.Len(t, providers, 2)

	anthropic := providers[0].(map[string]interface{})
	openai := providers[1].(map[string]interface{})

	assert.Equal(t, "anthropic", anthropic["name"])
	assert.Equal(t, "claude-3-7-sonnet", anthropic["default_model"])
	assert.Equal(t, []interface{}{"claude-3-7-sonnet"}, anthropic["models"])

	assert.Equal(t, "openai", openai["name"])
	assert.Equal(t, "gpt-4o", openai["default_model"])
	assert.Equal(t, []interface{}{"gpt-4.1", "gpt-4o"}, openai["models"])
}

func TestGetRuntimeModels_PreservesProviderModelCatalogWhenAliasesOverlap(t *testing.T) {
	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-b",
		DefaultModel:    "shared-model",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider("provider-a", &testLLMProvider{
		name:    "provider-a",
		content: "provider a response",
	}))
	require.NoError(t, runtime.RegisterProvider("provider-b", &testLLMProvider{
		name:    "provider-b",
		content: "provider b response",
	}))
	require.NoError(t, runtime.RegisterProviderAlias("shared-model", "provider-a"))
	require.NoError(t, runtime.RegisterProviderAlias("shared-model", "provider-b"))
	require.NoError(t, runtime.RegisterProviderAlias("provider-a-only", "provider-a"))
	require.NoError(t, runtime.RegisterProviderAlias("provider-b-only", "provider-b"))
	handler.SetLLMRuntime(runtime)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/models", nil)
	rec := httptest.NewRecorder()

	handler.GetRuntimeModels(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	providers, ok := payload["providers"].([]interface{})
	require.True(t, ok)
	require.Len(t, providers, 2)

	providerA := providers[0].(map[string]interface{})
	providerB := providers[1].(map[string]interface{})

	assert.Equal(t, "provider-a", providerA["name"])
	assert.Equal(t, []interface{}{"provider-a-only", "shared-model"}, providerA["models"])

	assert.Equal(t, "provider-b", providerB["name"])
	assert.Equal(t, "shared-model", providerB["default_model"])
	assert.Equal(t, []interface{}{"provider-b-only", "shared-model"}, providerB["models"])
}

func TestGetRuntimeStatus_IncludesProfileMetadata(t *testing.T) {
	profileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(profileRoot, "profile.yaml"), []byte(`profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	query := url.Values{}
	query.Set("profile", profileRoot)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status?"+query.Encode(), nil)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.GetRuntimeStatus(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	profilePayload, ok := payload["profile"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, profileRoot, profilePayload["reference"])

	resolved, ok := profilePayload["resolved"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "coder", resolved["agent_id"])
	absRoot, err := filepath.Abs(profileRoot)
	require.NoError(t, err)
	assert.Equal(t, absRoot, resolved["profile_root"])
}

func TestGetRuntimeHealth_IncludesProfileMetadata(t *testing.T) {
	profileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(profileRoot, "profile.yaml"), []byte(`profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	query := url.Values{}
	query.Set("profile", profileRoot)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/health?"+query.Encode(), nil)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.GetRuntimeHealth(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	profilePayload, ok := payload["profile"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, profileRoot, profilePayload["reference"])
}

func TestGetRuntimeTraces_IncludesProfileMetadata(t *testing.T) {
	profileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(profileRoot, "profile.yaml"), []byte(`profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	query := url.Values{}
	query.Set("profile", profileRoot)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces?"+query.Encode(), nil)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.GetRuntimeTraces(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	profilePayload, ok := payload["profile"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, profileRoot, profilePayload["reference"])
}

func TestGetRuntimeTrace_IncludesProfileMetadata(t *testing.T) {
	profileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(profileRoot, "profile.yaml"), []byte(`profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	traceID := "trace-profile-meta"
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:    "trace.test",
		TraceID: traceID,
	})

	query := url.Values{}
	query.Set("profile", profileRoot)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/"+traceID+"?"+query.Encode(), nil)
	req = mux.SetURLVars(req, map[string]string{"trace_id": traceID})
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.GetRuntimeTrace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	profilePayload, ok := payload["profile"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, profileRoot, profilePayload["reference"])
}

func TestValidateRuntimeConfig_IncludesProfileMetadata(t *testing.T) {
	profileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(profileRoot, "profile.yaml"), []byte(`profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`), 0o644))

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	query := url.Values{}
	query.Set("profile", profileRoot)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/validate?"+query.Encode(), nil)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.ValidateRuntimeConfig(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	profilePayload, ok := payload["profile"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, profileRoot, profilePayload["reference"])
}

func TestContextSnapshotFromRuntimeConfig_ResolvesLayers(t *testing.T) {
	config := runtimecfg.DefaultRuntimeConfig()
	config.Context.Profile = runtimecontext.BudgetProfileHot
	config.Context.CompactionMode = runtimecontext.CompactionModeLedgerPreferred
	config.Context.RecallMode = runtimecontext.RecallModeBroad
	config.Context.ObservationMode = runtimecontext.ObservationModeAll
	config.Context.KeepRecentMessages = 4
	config.Context.MaxObservationItems = 7
	config.Context.MaxRecallResults = 6

	snapshot := contextSnapshotFromRuntimeConfig(config)
	assert.Equal(t, runtimecontext.BudgetProfileHot, snapshot["profile"])
	assert.Equal(t, runtimecontext.BudgetProfileCompact, snapshot["resolved_profile"])
	assert.Equal(t, runtimecontext.CompactionModeLedgerPreferred, snapshot["compaction_mode"])
	assert.Equal(t, runtimecontext.RecallModeBroad, snapshot["recall_mode"])
	assert.Equal(t, 4, snapshot["keep_recent_messages"])
	assert.Equal(t, 7, snapshot["max_observation_items"])
	assert.Equal(t, 6, snapshot["max_recall_results"])

	layers, ok := snapshot["layers"].(runtimecontext.LayerPlan)
	require.True(t, ok)
	assert.Equal(t, runtimecontext.BudgetProfileCompact, layers.Profile)
	assert.Equal(t, "hot", layers.Hot.Name)
	assert.Equal(t, 4, layers.Hot.MaxMessages)
	assert.Equal(t, "warm", layers.Warm.Name)
	assert.Equal(t, runtimecontext.ObservationModeAll, layers.Warm.Mode)
	assert.Equal(t, "cold", layers.Cold.Name)
	assert.Contains(t, layers.Cold.Mode, runtimecontext.RecallModeBroad)
}

func TestGetRuntimeHealth_ReturnsSummary(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "healthy-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("healthy-model", &testLLMProvider{
		name:    "healthy-model",
		content: "ok",
	}))
	require.NoError(t, runtime.RegisterProvider("bad-model", &testLLMProvider{
		name:      "bad-model",
		content:   "bad",
		healthErr: fmt.Errorf("provider offline"),
	}))
	handler.SetLLMRuntime(runtime)
	runtimeCfg := runtimecfg.DefaultRuntimeConfig()
	runtimeCfg.Context.Profile = "extended"
	runtimeCfg.Context.CompactionMode = "ledger_preferred"
	runtimeCfg.Context.RecallMode = "broad"
	runtimeCfg.Context.ObservationMode = "all"
	runtimeCfg.Context.MinCompactionMessages = 2
	runtimeCfg.Context.MinRecallQueryLength = 5
	runtimeCfg.Context.LedgerLoadLimit = 14
	runtimeCfg.Context.MaxPromptTokens = 18000
	runtimeCfg.Context.MaxMessages = 32
	handler.SetRuntimeConfig(runtimeCfg, "")
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:    "patch.decision",
		TraceID: "trace-health",
		Payload: map[string]interface{}{
			"patch_decision":        "blocked",
			"patch_decision_policy": "warn",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/health", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.GetRuntimeHealth(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"healthy":false`)
	assert.Contains(t, rec.Body.String(), `"degraded_providers":1`)
	assert.Contains(t, rec.Body.String(), `"unhealthy_providers":1`)
	assert.Contains(t, rec.Body.String(), `provider bad-model degraded: provider offline`)
	assert.Contains(t, rec.Body.String(), `"patch_governance"`)
	assert.Contains(t, rec.Body.String(), `"blocked":1`)
	assert.Contains(t, rec.Body.String(), `"profile":"extended"`)
	assert.Contains(t, rec.Body.String(), `"compaction_mode":"ledger_preferred"`)
	assert.Contains(t, rec.Body.String(), `"recall_mode":"broad"`)
	assert.Contains(t, rec.Body.String(), `"min_recall_query_length":5`)
	assert.Contains(t, rec.Body.String(), `"max_prompt_tokens":18000`)
}

func TestReloadRuntimeMCPs_ReloadsUnderlyingManager(t *testing.T) {
	lifecycleManager := &fakeLifecycleMCPManager{
		statuses: []*mcpconfig.MCPStatus{{
			Name:      "test-mcp",
			Type:      "stdio",
			Enabled:   true,
			Connected: true,
			ToolCount: 1,
		}},
		tools: []*mcpregistry.ToolInfo{{
			MCPName: "test-mcp",
			Enabled: true,
			Tool: &mcpprotocol.Tool{
				Name:        "read_logs",
				Description: "Read logs",
			},
		}},
	}
	adapter := skill.NewMCPAdapter(lifecycleManager)
	registry := skill.NewRegistry(adapter)
	handler := NewHandler(registry, nil, adapter)
	handler.SetAdminToken("secret-token")

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/mcps/reload", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.ReloadRuntimeMCPs(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, lifecycleManager.reloadCount)
	assert.Equal(t, 1, lifecycleManager.startCount)
	assert.Contains(t, rec.Body.String(), `"reloaded":true`)
	assert.Contains(t, rec.Body.String(), `"trace_id":"trace_`)
	assert.Contains(t, rec.Body.String(), `"tool_count":1`)
	assert.Contains(t, rec.Body.String(), `"added":0`)
}

func TestReloadRuntimeMCPs_EmitsTraceableLifecycleEvents(t *testing.T) {
	lifecycleManager := &fakeLifecycleMCPManager{
		statuses: []*mcpconfig.MCPStatus{{
			Name:      "test-mcp",
			Type:      "stdio",
			Enabled:   true,
			Connected: true,
			ToolCount: 1,
		}},
		tools: []*mcpregistry.ToolInfo{{
			MCPName: "test-mcp",
			Enabled: true,
			Tool: &mcpprotocol.Tool{
				Name:        "read_logs",
				Description: "Read logs",
			},
		}},
	}
	adapter := skill.NewMCPAdapter(lifecycleManager)
	registry := skill.NewRegistry(adapter)
	handler := NewHandler(registry, nil, adapter)
	handler.SetAdminToken("secret-token")

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	reloadReq := httptest.NewRequest(http.MethodPost, "/api/runtime/mcps/reload", nil)
	reloadReq.Header.Set("X-Forwarded-For", "10.0.0.5")
	reloadReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	reloadRec := httptest.NewRecorder()
	router.ServeHTTP(reloadRec, reloadReq)
	require.Equal(t, http.StatusOK, reloadRec.Code)

	var reloadPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(reloadRec.Body.Bytes(), &reloadPayload))
	traceID, ok := reloadPayload["trace_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, traceID)

	traceReq := httptest.NewRequest(http.MethodGet, "/api/runtime/traces/"+traceID, nil)
	traceReq.Header.Set("X-Forwarded-For", "10.0.0.5")
	traceReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	traceRec := httptest.NewRecorder()
	router.ServeHTTP(traceRec, traceReq)
	require.Equal(t, http.StatusOK, traceRec.Code)

	body := traceRec.Body.String()
	assert.Contains(t, body, `"mcp.reload.started"`)
	assert.Contains(t, body, `"mcp.transport.connected"`)
	assert.Contains(t, body, `"mcp.client.session.connected"`)
	assert.Contains(t, body, `"mcp.connected"`)
	assert.Contains(t, body, `"mcp.tools.loaded"`)
	assert.Contains(t, body, `"mcp.catalog.refreshed"`)
	assert.Contains(t, body, `"added"`)
	assert.Contains(t, body, `"mcp.reload.completed"`)
}

func TestValidateRuntimeConfig_ReturnsWarnings(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/validate", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.ValidateRuntimeConfig(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"validation"`)
	assert.Contains(t, rec.Body.String(), `"llm runtime is not configured"`)
	assert.Contains(t, rec.Body.String(), `"session manager is not configured"`)
}

func TestValidateRuntimeConfig_IncludesConfigFileWarnings(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetAdminToken("secret-token")

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	missingPath := filepath.Join(t.TempDir(), "missing-runtime.yaml")
	handler.SetRuntimeConfig(runtimeConfig, missingPath)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/validate", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()

	handler.ValidateRuntimeConfig(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "runtime config file not found")
	escapedPath := strings.ReplaceAll(missingPath, "\\", "\\\\")
	assert.Contains(t, rec.Body.String(), escapedPath)
}

func TestGetSearchStats_TracksModesAndEmbeddingUsage(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "semantic-search-stats-skill",
		Description: "search customer orders in sap",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	searchReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=search%20customer%20orders%20in%20sap", nil)
	searchRec := httptest.NewRecorder()
	router.ServeHTTP(searchRec, searchReq)
	require.Equal(t, http.StatusOK, searchRec.Code)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search/stats", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	searchPayload := payload["search"].(map[string]interface{})
	assert.Equal(t, float64(1), searchPayload["total_requests"])
	assert.Equal(t, float64(1), searchPayload["embedding_requests"])
	assert.Equal(t, "auto", searchPayload["last_requested_mode"])
	assert.Equal(t, "semantic", searchPayload["last_resolved_mode"])
	assert.Equal(t, true, searchPayload["last_used_embedding"])

	requestedModeCount := searchPayload["requested_mode_count"].(map[string]interface{})
	assert.Equal(t, float64(1), requestedModeCount["auto"])
	resolvedModeCount := searchPayload["resolved_mode_count"].(map[string]interface{})
	assert.Equal(t, float64(1), resolvedModeCount["semantic"])
}

func TestReindexSearchIndex_RebuildsEmbeddingIndex(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "reindex-skill",
		Description: "search customer orders in sap",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/search/reindex", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, true, payload["reindexed"])
	searchPayload := payload["search"].(map[string]interface{})
	assert.Equal(t, float64(1), searchPayload["reindex_count"])
	assert.Equal(t, "success", searchPayload["last_reindex_status"])
}

func TestGetSearchStats_RequiresAdminToken(t *testing.T) {
	observability.GlobalMetrics = observability.NewRegistry()
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetSearchAdminToken("secret-token")

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search/stats", nil)
	req.RemoteAddr = "10.0.0.5:9000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	authorizedReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search/stats", nil)
	authorizedReq.RemoteAddr = "10.0.0.5:9000"
	authorizedReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	authorizedRec := httptest.NewRecorder()
	router.ServeHTTP(authorizedRec, authorizedReq)
	require.Equal(t, http.StatusOK, authorizedRec.Code)

	forbiddenCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSearchAdminActions, map[string]string{
		observability.LabelAction:     "search_stats",
		observability.LabelOutcome:    "forbidden",
		observability.LabelAccessMode: "denied",
	})
	assert.Equal(t, float64(1), forbiddenCounter.Get())

	successCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSearchAdminActions, map[string]string{
		observability.LabelAction:     "search_stats",
		observability.LabelOutcome:    "success",
		observability.LabelAccessMode: "token",
	})
	assert.Equal(t, float64(1), successCounter.Get())
}

func TestPreviewPromptLayout_RequiresAdminToken(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSearchAdminToken("secret-token")

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/debug/prompt-layout", bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	req.RemoteAddr = "10.0.0.5:9000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	authorizedReq := httptest.NewRequest(http.MethodPost, "/api/runtime/debug/prompt-layout", bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	authorizedReq.RemoteAddr = "10.0.0.5:9000"
	authorizedReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	authorizedRec := httptest.NewRecorder()
	router.ServeHTTP(authorizedRec, authorizedReq)
	require.Equal(t, http.StatusOK, authorizedRec.Code)
}

func TestPreviewPromptLayout_ReturnsStructuredLayout(t *testing.T) {
	profileRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: tester
agents:
  tester: {}
`)
	writeProfileFile(filepath.Join(profileRoot, "runtime.yaml"), "agent:\n  defaultModel: test-model\n")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "system.md"), "Profile system prompt.")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "tools.md"), "Prefer rg when available.")
	writeProfileFile(filepath.Join(workspaceRoot, "AGENTS.md"), "Stay within the workspace boundary.")

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"profile":%q,"provider":"codex","workspace_path":%q,"messages":[{"role":"user","content":"hi"}]}`, profileRoot, workspaceRoot)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/debug/prompt-layout", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:9000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "codex", payload["provider"])
	assert.Equal(t, workspaceRoot, payload["workspace_path"])
	layout, _ := payload["prompt_layout"].(string)
	assert.Contains(t, layout, "[base/system]")
	assert.Contains(t, layout, "[developer/developer]")
	assert.Contains(t, layout, "[user/developer]")

	fragments, ok := payload["fragments"].([]interface{})
	require.True(t, ok)
	require.Len(t, fragments, 3)

	instructionMessages, ok := payload["instruction_messages"].([]interface{})
	require.True(t, ok)
	require.Len(t, instructionMessages, 3)
}

func TestReindexSearchIndex_CooldownReturnsRateLimit(t *testing.T) {
	observability.GlobalMetrics = observability.NewRegistry()
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "cooldown-skill",
		Description: "search customer orders in sap",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)
	handler.SetSearchReindexCooldown(time.Hour)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	firstReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/search/reindex", nil)
	firstReq.RemoteAddr = "127.0.0.1:1234"
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, firstReq)
	require.Equal(t, http.StatusOK, firstRec.Code)

	secondReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/search/reindex", nil)
	secondReq.RemoteAddr = "127.0.0.1:1234"
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, secondReq)
	require.Equal(t, http.StatusTooManyRequests, secondRec.Code)
	assert.NotEmpty(t, secondRec.Header().Get("Retry-After"))

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(secondRec.Body.Bytes(), &payload))
	assert.Equal(t, "search reindex cooldown active", payload["error"])
	assert.Greater(t, payload["retry_after_seconds"].(float64), 0.0)

	rateLimitedCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSearchAdminActions, map[string]string{
		observability.LabelAction:     "search_reindex",
		observability.LabelOutcome:    "rate_limited",
		observability.LabelAccessMode: "loopback",
	})
	assert.Equal(t, float64(1), rateLimitedCounter.Get())

	reindexRunsCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSearchReindexRuns, map[string]string{
		observability.LabelAction:     "search_reindex",
		observability.LabelOutcome:    "success",
		observability.LabelAccessMode: "loopback",
	})
	assert.Equal(t, float64(1), reindexRunsCounter.Get())
}

func TestAgentChat_StreamSSE(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "hello world",
		streamChunks: []llm.StreamChunk{
			{Type: llm.EventTypeReasoning, Content: "thinking...", Done: false},
			{Type: llm.EventTypeToolStart, Content: "search", ToolCall: &types.ToolCall{ID: "tool-1", Name: "search", Args: map[string]interface{}{"query": "weather"}}, Done: false},
			{Type: llm.EventTypeToolCall, Delta: &types.ToolCall{ID: "tool-1", Name: "search", Args: map[string]interface{}{"query": "weather"}}, Done: false},
			{Type: llm.EventTypeToolEnd, Content: "search complete", ToolCall: &types.ToolCall{ID: "tool-1", Name: "search", Args: map[string]interface{}{"query": "weather"}}, Done: false},
			{Type: llm.EventTypeText, Content: "hello ", Done: false},
			{Type: llm.EventTypeText, Content: "world", Done: false},
			{Type: llm.EventTypeDone, Done: true},
		},
	}))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"stream please"}],"user_id":"stream-user","stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, rec.Body.String(), "event: meta")
	assert.Contains(t, rec.Body.String(), "event: orchestration")
	assert.Contains(t, rec.Body.String(), "event: reasoning")
	assert.Contains(t, rec.Body.String(), "event: tool_call")
	assert.Contains(t, rec.Body.String(), "event: tool_start")
	assert.Contains(t, rec.Body.String(), "event: tool_end")
	assert.Contains(t, rec.Body.String(), "event: chunk")
	assert.Contains(t, rec.Body.String(), "event: result")
	assert.Contains(t, rec.Body.String(), "event: done")
	assert.Contains(t, rec.Body.String(), "hello ")
	assert.Contains(t, rec.Body.String(), "world")
	assert.Contains(t, rec.Body.String(), `"source":"llm_stream"`)
	assert.Contains(t, rec.Body.String(), `"route_attempted":false`)
	assert.Contains(t, rec.Body.String(), `"schema_version":"skill_runtime.sse.v1"`)
	assert.Contains(t, rec.Body.String(), `"sequence":1`)
	assert.Contains(t, rec.Body.String(), `"reasoning":{"content":"thinking..."`)
	assert.Contains(t, rec.Body.String(), `"tool":{"args":{"query":"weather"}`)
	assert.Contains(t, rec.Body.String(), `"name":"search"`)
	assert.Contains(t, rec.Body.String(), `"status":"tool_start"`)
	assert.Contains(t, rec.Body.String(), `"status":"tool_call"`)
	assert.Contains(t, rec.Body.String(), `"status":"tool_end"`)

	bodyText := rec.Body.String()
	marker := `"session_id":"`
	index := strings.Index(bodyText, marker)
	require.NotEqual(t, -1, index)
	start := index + len(marker)
	end := strings.Index(bodyText[start:], `"`)
	require.NotEqual(t, -1, end)
	sessionIDValue := bodyText[start : start+end]

	session, err := sessionManager.GetSession(context.Background(), sessionIDValue)
	require.NoError(t, err)
	require.Len(t, session.GetMessages(), 2)
	assert.Equal(t, "stream please", session.GetMessages()[0].Content)
	assert.Equal(t, "hello world", session.GetMessages()[1].Content)
}

func TestAgentChat_StreamSSE_AgentRouteResult(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "route-skill",
		Description: "route streaming test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"route"},
			Weight: 1,
		}},
		Tools: []string{"echo_tool"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"please route this"}],"user_id":"route-user","stream":true,"enable_routing":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: meta")
	assert.Contains(t, rec.Body.String(), "event: orchestration")
	assert.Contains(t, rec.Body.String(), "event: route")
	assert.Contains(t, rec.Body.String(), "event: observation")
	assert.Contains(t, rec.Body.String(), "event: result")
	assert.Contains(t, rec.Body.String(), "event: chunk")
	assert.Contains(t, rec.Body.String(), "event: done")
	assert.Contains(t, rec.Body.String(), `"source":"agent_route"`)
	assert.Contains(t, rec.Body.String(), `"kind":"agent"`)
	assert.Contains(t, rec.Body.String(), `"skill":"route-skill"`)
	assert.Contains(t, rec.Body.String(), `"route_attempted":true`)
	assert.Contains(t, rec.Body.String(), `"route_matched":true`)
	assert.Contains(t, rec.Body.String(), `"candidate_count":1`)
	assert.Contains(t, rec.Body.String(), `"matched_by":"keyword:route"`)
	assert.Contains(t, rec.Body.String(), `"selection_reason":"selected"`)
	assert.Contains(t, rec.Body.String(), `"step":"step_1"`)
	assert.Contains(t, rec.Body.String(), `"tool":"echo_tool"`)
	assert.Contains(t, rec.Body.String(), `"duration_ms":`)
	assert.Contains(t, rec.Body.String(), `"schema_version":"skill_runtime.sse.v1"`)
}

func TestAgentChat_StreamSSE_AgentRouteResult_WithPlanning(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "route-plan-skill",
		Description: "route streaming planning test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"planroute"},
			Weight: 1,
		}},
		Tools: []string{"echo_tool"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "step_1",
			Name: "echo",
			Tool: "echo_tool",
		}}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"please planroute this"}],"user_id":"route-user","stream":true,"enable_routing":true,"planning_mode":"planner_preferred"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: planning")
	assert.Contains(t, rec.Body.String(), `"mode":"planner_preferred"`)
	assert.Contains(t, rec.Body.String(), `"planning_source":"workflow"`)
	assert.Contains(t, rec.Body.String(), `"plan_step_count":1`)
	assert.Contains(t, rec.Body.String(), `"subagent_task_count":1`)
	assert.Contains(t, rec.Body.String(), `"subagent_execution_requested":false`)
	assert.Contains(t, rec.Body.String(), `"subagent_execution_attempted":false`)
}

func TestAgentChat_RouteFallbackIncludesOrchestration(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "fallback response",
	}))
	handler.SetLLMRuntime(runtime)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"no route here"}],"user_id":"fallback-user","enable_routing":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "llm_fallback", payload["source"])
	result := payload["result"].(map[string]interface{})
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, "llm_fallback", orchestration["source"])
	assert.Equal(t, true, orchestration["route_attempted"])
	assert.Equal(t, false, orchestration["route_matched"])
	assert.Equal(t, float64(0), orchestration["candidate_count"])
	assert.Equal(t, "no_matching_skill", orchestration["fallback_reason"])
	assert.Equal(t, "fallback response", result["output"])
}

func TestAgentChat_StreamSSE_LLMResult_WithPlanning(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:         "test-model",
		content:      "planned fallback response",
		streamChunks: []llm.StreamChunk{{Type: llm.EventTypeText, Content: "planned ", Done: false}, {Type: llm.EventTypeText, Content: "fallback", Done: false}, {Type: llm.EventTypeDone, Done: true}},
	}))
	handler.SetLLMRuntime(runtime)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"no route but plan this"}],"user_id":"planner-stream-user","stream":true,"planning_mode":"planner_preferred"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: planning")
	assert.Contains(t, rec.Body.String(), `"mode":"planner_preferred"`)
	assert.Contains(t, rec.Body.String(), `"planning_source":"tool_catalog"`)
	assert.Contains(t, rec.Body.String(), `"step_count":1`)
	assert.Contains(t, rec.Body.String(), `"subagent_task_count":1`)
	assert.Contains(t, rec.Body.String(), `"subagent_execution_requested":false`)
	assert.Contains(t, rec.Body.String(), `"subagent_execution_attempted":false`)
	assert.Contains(t, rec.Body.String(), `"source":"llm_stream"`)
}

func TestAgentChat_StreamSSE_ExecutesPlannedSubagents(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "planned-stream-exec-skill",
		Description: "planned workflow skill with streaming execution",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"plan", "stream"},
			Weight: 1,
		}},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify changes", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}))
	handler := NewHandler(registry, nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Verify implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "run_tests", Args: map[string]interface{}{"target": "./..."}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"plan and stream"}],"stream":true,"enable_routing":true,"planning_mode":"planner_preferred","execute_planned_subagents":true,"allow_write_planned_subagents":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: planning")
	assert.Contains(t, rec.Body.String(), "event: subagent")
	assert.Contains(t, rec.Body.String(), `"subagent_execution_requested":true`)
	assert.Contains(t, rec.Body.String(), `"subagent_execution_attempted":true`)
	assert.Contains(t, rec.Body.String(), `"patch_decision":"approved"`)
	assert.Contains(t, rec.Body.String(), `"subagent_patch_count":1`)
	assert.Contains(t, rec.Body.String(), `"subagent_applied_patch_count":1`)
	assert.Contains(t, rec.Body.String(), `"subagent_verified_patch_count":1`)
	assert.Contains(t, rec.Body.String(), `"source":"agent_planned_subagents"`)
	assert.Contains(t, rec.Body.String(), `"role":"writer"`)
	assert.Contains(t, rec.Body.String(), `"role":"verifier"`)
	assert.Contains(t, rec.Body.String(), `"apply_status":"applied"`)
	assert.Contains(t, rec.Body.String(), `"verification_status":"verified"`)
	assert.Contains(t, rec.Body.String(), "event: done")
}

func TestAgentChat_UsesEmbeddingRouterForSemanticRoute(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "semantic-route-skill",
		Description: "search customer orders in sap",
		Triggers: []skill.Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return &types.Result{
				Success: true,
				Output:  "semantic route matched",
			}, nil
		}),
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "fallback-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("fallback-model", &testLLMProvider{
		name:    "fallback-model",
		content: "fallback response",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"search customer orders in sap"}],"enable_routing":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "agent_route", payload["source"])

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "agent", result["kind"])
	assert.Equal(t, "semantic-route-skill", result["skill"])
	assert.Equal(t, "semantic route matched", result["output"])
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, true, orchestration["route_attempted"])
	assert.Equal(t, true, orchestration["route_matched"])
	assert.Equal(t, float64(1), orchestration["candidate_count"])
	candidates := orchestration["route_candidates"].([]interface{})
	require.Len(t, candidates, 1)
	candidate := candidates[0].(map[string]interface{})
	assert.Equal(t, "semantic-route-skill", candidate["skill"])
	assert.Equal(t, "embedding", candidate["matched_by"])
	assert.Equal(t, true, candidate["chosen"])
	capabilityCandidates := orchestration["capability_candidates"].([]interface{})
	require.Len(t, capabilityCandidates, 1)
	selectedCapability := orchestration["capability"].(map[string]interface{})
	assert.Equal(t, "semantic-route-skill", selectedCapability["name"])
}

func TestAgentChat_AgentRouteFailureIncludesObservationDetails(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "broken-skill",
		Description: "failing route test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"broken"},
			Weight: 1,
		}},
		Tools: []string{"broken_tool"},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			observation := types.NewObservation("step_fail", "broken_tool")
			observation.MarkFailure("tool broken_tool failed")
			return (&types.Result{
				Success:      false,
				Output:       "",
				Observations: []types.Observation{*observation},
			}), nil
		}),
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"broken route now"}],"enable_routing":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "agent_route", payload["source"])

	result := payload["result"].(map[string]interface{})
	assert.Equal(t, "agent", result["kind"])
	assert.Equal(t, false, result["success"])
	orchestration := result["orchestration"].(map[string]interface{})
	assert.Equal(t, true, orchestration["route_attempted"])
	assert.Equal(t, true, orchestration["route_matched"])
	assert.Equal(t, float64(1), orchestration["candidate_count"])
	observationSummary := orchestration["observation_summary"].(map[string]interface{})
	assert.Equal(t, float64(1), observationSummary["count"])
	assert.Equal(t, float64(1), observationSummary["failed"])
	assert.Equal(t, float64(0), observationSummary["successful"])
	assert.Equal(t, "broken_tool", observationSummary["failed_tools"].([]interface{})[0])
	failedDetails := observationSummary["failed_details"].([]interface{})
	require.Len(t, failedDetails, 1)
	failed := failedDetails[0].(map[string]interface{})
	assert.Equal(t, "broken_tool", failed["tool"])
	assert.Contains(t, failed["error"], "tool broken_tool failed")
	assert.GreaterOrEqual(t, failed["duration_ms"].(float64), 0.0)
	assert.GreaterOrEqual(t, observationSummary["total_duration_ms"].(float64), 0.0)
	assert.GreaterOrEqual(t, observationSummary["average_duration_ms"].(float64), 0.0)
}

func TestListCapabilities_ReturnsDescriptors(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "capability-skill",
		Description: "capability test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"capability"},
			Weight: 1,
		}},
	}))

	handler := NewHandler(registry, nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/capabilities", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	caps := payload["capabilities"].([]interface{})
	require.GreaterOrEqual(t, len(caps), 1)
}

func TestSkillsAPI_E2EMatrix_BasicPaths(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "matrix-skill",
		Description: "semantic probe for matrix",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"matrix"},
			Weight: 1,
		}, {
			Type:   "embedding",
			Values: []string{"semantic"},
			Weight: 1,
		}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return &types.Result{Success: true, Output: "matrix-ok", Skill: "matrix-skill"}, nil
		}),
	}))

	embeddingIndex, err := embedding.NewVectorIndex(nil)
	require.NoError(t, err)
	embeddingRouter, err := skill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
	require.NoError(t, err)
	require.NoError(t, embeddingRouter.IndexSkills())

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "matrix-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("matrix-model", &testLLMProvider{name: "matrix-model", content: "matrix response"}))

	handler := NewHandler(registry, nil, mcpManager)
	handler.SetEmbeddingRouter(embeddingRouter)
	handler.SetLLMRuntime(runtime)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	searchReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=matrix", nil)
	searchRec := httptest.NewRecorder()
	router.ServeHTTP(searchRec, searchReq)
	require.Equal(t, http.StatusOK, searchRec.Code)

	semanticReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=semantic+probe&mode=semantic", nil)
	semanticRec := httptest.NewRecorder()
	router.ServeHTTP(semanticRec, semanticReq)
	require.Equal(t, http.StatusOK, semanticRec.Code)

	execBody := []byte(`{"prompt":"run matrix"}`)
	execReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/matrix-skill/execute", bytes.NewReader(execBody))
	execRec := httptest.NewRecorder()
	router.ServeHTTP(execRec, execReq)
	require.Equal(t, http.StatusOK, execRec.Code)

	createSessionReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions", bytes.NewReader([]byte(`{"user_id":"matrix-user"}`)))
	createSessionRec := httptest.NewRecorder()
	router.ServeHTTP(createSessionRec, createSessionReq)
	require.Equal(t, http.StatusCreated, createSessionRec.Code)
	var sessionPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(createSessionRec.Body.Bytes(), &sessionPayload))
	session := sessionPayload["session"].(map[string]interface{})
	sessionID := session["id"].(string)

	agentBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"matrix skill"}],"enable_routing":true,"session_id":"%s"}`, sessionID)
	agentReq := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader([]byte(agentBody)))
	agentRec := httptest.NewRecorder()
	router.ServeHTTP(agentRec, agentReq)
	require.Equal(t, http.StatusOK, agentRec.Code)

	historyReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+sessionID+"/history", nil)
	historyRec := httptest.NewRecorder()
	router.ServeHTTP(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code)
}

func TestHotReloadEndpoints_Lifecycle(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	skillDir := t.TempDir()
	loader.SetSkillDir(skillDir)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hot-skill
description: hot reload test
version: 1.0.0
triggers:
  - type: keyword
    values: ["hot"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hot hydrated."), 0o644))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	startReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/hot-reload/start", bytes.NewReader([]byte(`{"dir":"`+strings.ReplaceAll(skillDir, "\\", "\\\\")+`"}`)))
	startReq.RemoteAddr = "127.0.0.1:1234"
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)
	require.Equal(t, http.StatusOK, startRec.Code)
	assert.Contains(t, startRec.Body.String(), `"started":true`)
	assert.Contains(t, startRec.Body.String(), `"watching":true`)
	assert.Contains(t, startRec.Body.String(), `"skillCount":1`)
	require.Equal(t, 1, registry.Count())
	loadedSkill, ok := registry.Get("hot-skill")
	require.True(t, ok)
	require.NotNil(t, loadedSkill.Source)
	assert.True(t, loadedSkill.Source.DiscoveryOnly)
	assert.Equal(t, "", loadedSkill.SystemPrompt)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	assert.Contains(t, listRec.Body.String(), `"systemPrompt":"You are hot hydrated."`)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/hot-reload/stats", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)
	assert.Contains(t, statsRec.Body.String(), `"watching":true`)

	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hot-skill-updated
description: hot reload test
version: 1.0.0
triggers:
  - type: keyword
    values: ["hot"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	reloadReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/hot-reload/reload", nil)
	reloadReq.RemoteAddr = "127.0.0.1:1234"
	reloadRec := httptest.NewRecorder()
	router.ServeHTTP(reloadRec, reloadReq)
	require.Equal(t, http.StatusOK, reloadRec.Code)
	assert.Contains(t, reloadRec.Body.String(), `"reloaded":true`)
	_, exists := registry.Get("hot-skill-updated")
	assert.True(t, exists)

	stopReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/hot-reload/stop", nil)
	stopReq.RemoteAddr = "127.0.0.1:1234"
	stopRec := httptest.NewRecorder()
	router.ServeHTTP(stopRec, stopReq)
	require.Equal(t, http.StatusOK, stopRec.Code)
	assert.Contains(t, stopRec.Body.String(), `"stopped":true`)
	assert.Contains(t, stopRec.Body.String(), `"watching":false`)
}

func TestGetStats_IncludesSkillDirs(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	extraDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir, extraDir})

	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "stats-skill",
		Description: "stats test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"stats"},
			Weight: 1,
		}},
	}))
	registeredSkill, ok := registry.Get("stats-skill")
	require.True(t, ok)
	registeredSkill.SetSource(filepath.Join(systemDir, "stats-skill.yaml"), systemDir, skill.SkillSourceLayerSystem)

	handler := NewHandler(registry, loader, mcpManager)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/stats", nil)
	rec := httptest.NewRecorder()

	handler.GetStats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawDirs, ok := payload["skill_dirs"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawDirs, 2)
	assert.Equal(t, systemDir, rawDirs[0])
	assert.Equal(t, extraDir, rawDirs[1])
	rawStats, ok := payload["stats"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawStats, 1)
	stat, ok := rawStats[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, systemDir, stat["source_dir"])
	assert.Equal(t, skill.SkillSourceLayerSystem, stat["source_layer"])
}

func TestHotReloadEndpoints_StartWithMultipleDirs(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	systemDir := t.TempDir()
	extraDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(systemDir, "system.yaml"), []byte(`name: system-skill
description: hot reload system
version: 1.0.0
triggers:
  - type: keyword
    values: ["system"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(extraDir, "extra.yaml"), []byte(`name: extra-skill
description: hot reload extra
version: 1.0.0
triggers:
  - type: keyword
    values: ["extra"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := fmt.Sprintf(`{"dirs":["%s","%s"]}`, strings.ReplaceAll(systemDir, "\\", "\\\\"), strings.ReplaceAll(extraDir, "\\", "\\\\"))
	startReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/hot-reload/start", bytes.NewReader([]byte(body)))
	startReq.RemoteAddr = "127.0.0.1:1234"
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)
	require.Equal(t, http.StatusOK, startRec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(startRec.Body.Bytes(), &payload))
	assert.Equal(t, true, payload["started"])
	rawDirs, ok := payload["dirs"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawDirs, 2)
	assert.Equal(t, systemDir, rawDirs[0])
	assert.Equal(t, extraDir, rawDirs[1])
	assert.Equal(t, 2, registry.Count())

	stopReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/hot-reload/stop", nil)
	stopReq.RemoteAddr = "127.0.0.1:1234"
	stopRec := httptest.NewRecorder()
	router.ServeHTTP(stopRec, stopReq)
	require.Equal(t, http.StatusOK, stopRec.Code)
}

func TestListSkills_IncludesSourceMetadata(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	sourceDir := t.TempDir()
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "list-skill",
		Description: "source metadata test",
		Triggers: []skill.Trigger{{
			Type:   "keyword",
			Values: []string{"list"},
			Weight: 1,
		}},
	}))
	skillItem, ok := registry.Get("list-skill")
	require.True(t, ok)
	skillItem.SetSource(filepath.Join(sourceDir, "list-skill.yaml"), sourceDir, skill.SkillSourceLayerExternal)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills", nil)
	rec := httptest.NewRecorder()
	handler.ListSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawSkills, ok := payload["skills"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawSkills, 1)
	item, ok := rawSkills[0].(map[string]interface{})
	require.True(t, ok)
	source, ok := item["source"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, sourceDir, source["dir"])
	assert.Equal(t, skill.SkillSourceLayerExternal, source["layer"])
}

func TestListSkills_HydratesDiscoveryOnlySkill(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	sourceDir := t.TempDir()
	skillDir := filepath.Join(sourceDir, "hydrated-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hydrated-skill
description: hydrated discovery skill
triggers:
  - type: keyword
    values: ["hydrate"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hydrated."), 0o644))
	require.NoError(t, loader.DiscoverAllWithRegistry([]string{sourceDir}, registry))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills", nil)
	rec := httptest.NewRecorder()
	handler.ListSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawSkills := payload["skills"].([]interface{})
	item := rawSkills[0].(map[string]interface{})
	assert.Equal(t, "You are hydrated.", item["systemPrompt"])
}

func TestGetSkill_HydratesDiscoveryOnlySkill(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	sourceDir := t.TempDir()
	skillDir := filepath.Join(sourceDir, "hydrated-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hydrated-skill
description: hydrated discovery skill
triggers:
  - type: keyword
    values: ["hydrate"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte(`# System
You are hydrated.

# User
Reply from hydrated skill.`), 0o644))
	require.NoError(t, loader.DiscoverAllWithRegistry([]string{sourceDir}, registry))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/hydrated-skill", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "You are hydrated.", payload["systemPrompt"])
	assert.Equal(t, "Reply from hydrated skill.", payload["userPrompt"])
}

func TestCreateSkill_AssignsRuntimeSource(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)

	body := `{
		"name":"runtime-skill",
		"description":"runtime source test",
		"triggers":[{"type":"keyword","values":["runtime"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	skillItem, ok := registry.Get("runtime-skill")
	require.True(t, ok)
	require.NotNil(t, skillItem.Source)
	assert.Equal(t, skill.SkillSourceLayerRuntime, skillItem.Source.Layer)
}

func TestCreateSkill_PersistsToExternalDir(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	externalDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir, externalDir})
	handler := NewHandler(registry, loader, mcpManager)

	body := `{
		"name":"persisted-skill",
		"description":"persist test",
		"triggers":[{"type":"keyword","values":["persist"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills?persist=true", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	skillItem, ok := registry.Get("persisted-skill")
	require.True(t, ok)
	require.NotNil(t, skillItem.Source)
	assert.Equal(t, skill.SkillSourceLayerExternal, skillItem.Source.Layer)
	assert.Contains(t, skillItem.Source.Path, filepath.Join(externalDir, "persisted-skill"))
	_, err := os.Stat(skillItem.Source.Path)
	require.NoError(t, err)
}

func TestCreateSkill_PersistsPromptToCompanionMarkdown(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	externalDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir, externalDir})
	handler := NewHandler(registry, loader, mcpManager)

	body := `{
		"name":"persisted-prompt-skill",
		"description":"persist prompt test",
		"systemPrompt":"You are a persisted prompt skill.",
		"userPrompt":"Return only the result.",
		"triggers":[{"type":"keyword","values":["persist"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills?persist=true", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	skillItem, ok := registry.Get("persisted-prompt-skill")
	require.True(t, ok)
	require.NotNil(t, skillItem.Source)
	assert.Equal(t, promptPathForSource(skillItem.Source.Path), skillItem.Source.PromptPath)

	promptPath := filepath.Join(filepath.Dir(skillItem.Source.Path), "prompt.md")
	promptBytes, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "# System")
	assert.Contains(t, string(promptBytes), "You are a persisted prompt skill.")
	assert.Contains(t, string(promptBytes), "# User")
	assert.Contains(t, string(promptBytes), "Return only the result.")

	manifestBytes, err := os.ReadFile(skillItem.Source.Path)
	require.NoError(t, err)
	assert.NotContains(t, string(manifestBytes), "systemPrompt")
	assert.NotContains(t, string(manifestBytes), "userPrompt")
}

func TestCreateSkill_RejectsPersistToSystemDir(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir})
	handler := NewHandler(registry, loader, mcpManager)

	body := `{
		"name":"invalid-persist",
		"description":"persist test",
		"triggers":[{"type":"keyword","values":["persist"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills?persist=true&target_dir="+systemDir, bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "persist target cannot be the system skill directory")
}

func TestListSkills_FiltersBySourceLayer(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)

	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "system-skill",
		Description: "system source",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"system"}, Weight: 1}},
	}))
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "external-skill",
		Description: "external source",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"external"}, Weight: 1}},
	}))
	systemSkill, _ := registry.Get("system-skill")
	externalSkill, _ := registry.Get("external-skill")
	systemSkill.SetSource("system.yaml", filepath.Clean("C:/skills/system"), skill.SkillSourceLayerSystem)
	externalSkill.SetSource("external.yaml", filepath.Clean("C:/skills/external"), skill.SkillSourceLayerExternal)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills?source_layer=external", nil)
	rec := httptest.NewRecorder()
	handler.ListSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawSkills, ok := payload["skills"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawSkills, 1)
	item := rawSkills[0].(map[string]interface{})
	assert.Equal(t, "external-skill", item["name"])
}

func TestSearchSkills_FiltersBySourceDir(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "system-shell",
		Description: "system shell",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"shell"}, Weight: 1}},
	}))
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "external-shell",
		Description: "external shell",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"shell"}, Weight: 1}},
	}))
	systemSkill, _ := registry.Get("system-shell")
	externalSkill, _ := registry.Get("external-shell")
	systemSkill.SetSource("system.yaml", filepath.Clean("C:/skills/system/system-shell"), skill.SkillSourceLayerSystem)
	externalSkill.SetSource("external.yaml", filepath.Clean("C:/skills/external/external-shell"), skill.SkillSourceLayerExternal)

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=shell&source_dir=C:/skills/external", nil)
	rec := httptest.NewRecorder()
	handler.SearchSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawResults, ok := payload["results"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawResults, 1)
	item := rawResults[0].(map[string]interface{})
	assert.Equal(t, "external-shell", item["name"])
}

func TestSearchSkills_HydratesDiscoveryOnlyResults(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	sourceDir := t.TempDir()
	skillDir := filepath.Join(sourceDir, "hydrated-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hydrated-skill
description: hydrated discovery skill
triggers:
  - type: keyword
    values: ["hydrate"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hydrated."), 0o644))
	require.NoError(t, loader.DiscoverAllWithRegistry([]string{sourceDir}, registry))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=hydrate", nil)
	rec := httptest.NewRecorder()
	handler.SearchSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawResults := payload["results"].([]interface{})
	item := rawResults[0].(map[string]interface{})
	assert.Equal(t, "You are hydrated.", item["systemPrompt"])
	rawMatches := payload["matches"].([]interface{})
	match := rawMatches[0].(map[string]interface{})
	matchSkill := match["skill"].(map[string]interface{})
	assert.Equal(t, "You are hydrated.", matchSkill["systemPrompt"])
}

func TestReloadSkills_LoadsConfiguredDirs(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	externalDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir, externalDir})
	handler := NewHandler(registry, loader, mcpManager)

	require.NoError(t, os.WriteFile(filepath.Join(systemDir, "system.yaml"), []byte(`name: reload-system
description: system
version: 1.0.0
triggers:
  - type: keyword
    values: ["reload"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(externalDir, "external.yaml"), []byte(`name: reload-external
description: external
version: 1.0.0
triggers:
  - type: keyword
    values: ["reload"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/reload", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ReloadSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, registry.Count())
	assert.Contains(t, rec.Body.String(), `"total_skills":2`)
}

func TestExportSkills_HydratesDiscoveryOnlySkills(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	sourceDir := t.TempDir()
	skillDir := filepath.Join(sourceDir, "hydrated-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hydrated-skill
description: hydrated discovery skill
triggers:
  - type: keyword
    values: ["hydrate"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hydrated."), 0o644))
	require.NoError(t, loader.DiscoverAllWithRegistry([]string{sourceDir}, registry))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/export", nil)
	rec := httptest.NewRecorder()
	handler.ExportSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	rawSkills := payload["skills"].([]interface{})
	item := rawSkills[0].(map[string]interface{})
	assert.Equal(t, "You are hydrated.", item["systemPrompt"])
}

func TestReloadSkills_UsesRequestDirs(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)

	systemDir := t.TempDir()
	externalDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(systemDir, "system.yaml"), []byte(`name: reload-system
description: system
version: 1.0.0
triggers:
  - type: keyword
    values: ["reload"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(externalDir, "external.yaml"), []byte(`name: reload-external
description: external
version: 1.0.0
triggers:
  - type: keyword
    values: ["reload"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	body := fmt.Sprintf(`{"dirs":["%s","%s"]}`, strings.ReplaceAll(systemDir, "\\", "\\\\"), strings.ReplaceAll(externalDir, "\\", "\\\\"))
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/reload", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ReloadSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, registry.Count())
	assert.Contains(t, rec.Body.String(), `"skill_dirs"`)
}

func TestCreateSkill_RequiresAdminToken(t *testing.T) {
	observability.GlobalMetrics = observability.NewRegistry()
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")

	body := `{
		"name":"protected-skill",
		"description":"auth test",
		"triggers":[{"type":"keyword","values":["auth"],"weight":1}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "10.0.0.5:9000"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	authorizedReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills", bytes.NewReader([]byte(body)))
	authorizedReq.RemoteAddr = "10.0.0.5:9000"
	authorizedReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	authorizedRec := httptest.NewRecorder()
	handler.CreateSkill(authorizedRec, authorizedReq)
	require.Equal(t, http.StatusCreated, authorizedRec.Code)

	forbiddenCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillMutationActions, map[string]string{
		observability.LabelAction:     skillMutationActionCreate,
		observability.LabelOutcome:    "forbidden",
		observability.LabelAccessMode: "denied",
	})
	assert.Equal(t, float64(1), forbiddenCounter.Get())

	successCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillMutationActions, map[string]string{
		observability.LabelAction:     skillMutationActionCreate,
		observability.LabelOutcome:    "success",
		observability.LabelAccessMode: "token",
	})
	assert.Equal(t, float64(1), successCounter.Get())
}

func TestReloadSkills_RequiresAdminToken(t *testing.T) {
	observability.GlobalMetrics = observability.NewRegistry()
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	skillDir := t.TempDir()
	loader.SetSkillDir(skillDir)
	handler := NewHandler(registry, loader, mcpManager)
	handler.SetSearchAdminToken("secret-token")

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/reload", nil)
	req.RemoteAddr = "10.0.0.5:9000"
	rec := httptest.NewRecorder()
	handler.ReloadSkills(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	authorizedReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/reload", nil)
	authorizedReq.RemoteAddr = "10.0.0.5:9000"
	authorizedReq.Header.Set("Authorization", "Bearer secret-token")
	authorizedRec := httptest.NewRecorder()
	handler.ReloadSkills(authorizedRec, authorizedReq)
	require.Equal(t, http.StatusOK, authorizedRec.Code)

	forbiddenCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillMutationActions, map[string]string{
		observability.LabelAction:     skillMutationActionReload,
		observability.LabelOutcome:    "forbidden",
		observability.LabelAccessMode: "denied",
	})
	assert.Equal(t, float64(1), forbiddenCounter.Get())

	successCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillMutationActions, map[string]string{
		observability.LabelAction:     skillMutationActionReload,
		observability.LabelOutcome:    "success",
		observability.LabelAccessMode: "token",
	})
	assert.Equal(t, float64(1), successCounter.Get())
}

func TestCreateSkill_AllowsAdminRoleFromJWTClaims(t *testing.T) {
	observability.GlobalMetrics = observability.NewRegistry()
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        "jwt-admin-secret",
		RoleClaims:       []string{"roles"},
		AdminRoles:       []string{"skills-admin"},
	})

	body := `{
		"name":"role-protected-skill",
		"description":"role auth test",
		"triggers":[{"type":"keyword","values":["auth"],"weight":1}]
	}`

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"roles": []string{"skills-admin"},
	})
	tokenString, err := token.SignedString([]byte("jwt-admin-secret"))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "10.0.0.5:9000"
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	successCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillMutationActions, map[string]string{
		observability.LabelAction:     skillMutationActionCreate,
		observability.LabelOutcome:    "success",
		observability.LabelAccessMode: "role",
	})
	assert.Equal(t, float64(1), successCounter.Get())
}

func TestGetAuthPolicy_ReturnsScopeResolverSnapshot(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        "jwt-secret",
		TenantHeaders:    []string{"X-Tenant-ID"},
		ProjectHeaders:   []string{"X-Project-ID"},
		UserHeaders:      []string{"X-User-ID"},
		RoleHeaders:      []string{"X-Role"},
		TenantClaims:     []string{"tenant_id"},
		ProjectClaims:    []string{"project_id"},
		UserClaims:       []string{"sub"},
		RoleClaims:       []string{"roles"},
		AdminRoles:       []string{"skills-admin"},
		APIKeyScopes: map[string]UsageScope{
			"secret-scope-key": {TenantID: "tenant-a", ProjectID: "project-a", UserID: "alice"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/auth/policy", nil)
	req.RemoteAddr = "10.0.0.5:9000"
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	handler.GetAuthPolicy(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	policy := payload["policy"].(map[string]interface{})
	assert.Equal(t, true, policy["enabled"])
	assert.Equal(t, true, policy["jwt_claims_enabled"])
	assert.Equal(t, true, policy["jwt_secret_configured"])
	assert.Equal(t, float64(1), policy["api_key_scope_count"])
	assert.Contains(t, policy["admin_roles"].([]interface{}), "skills-admin")
}

func TestGetGovernancePolicy_ReturnsUnifiedPolicies(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")
	handler.SetMutationPolicy(MutationPolicy{ReadOnly: true, DisableImport: true})
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 12,
	})
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        "jwt-secret",
		AdminRoles:       []string{"skills-admin"},
	})
	handler.SetAuthPolicyPersister(func(policy ScopeResolverConfig, changedBy string) error { return nil })
	handler.SetUsagePolicyPersister(func(policy UsagePolicy, changedBy string) error { return nil })
	handler.SetMutationPolicyPersister(func(policy MutationPolicy, changedBy string) error { return nil })
	handler.SetUsageLedgerStore(&testUsageLedgerStore{})
	handler.SetSearchReindexCooldown(45 * time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/governance/policy", nil)
	req.RemoteAddr = "10.0.0.5:9000"
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	handler.GetGovernancePolicy(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	mutation := payload["mutation_policy"].(map[string]interface{})
	assert.Equal(t, true, mutation["read_only"])
	usage := payload["usage_policy"].(map[string]interface{})
	assert.Equal(t, true, usage["quota_enabled"])
	assert.Equal(t, float64(12), usage["default_max_requests"])
	auth := payload["auth_policy"].(map[string]interface{})
	assert.Equal(t, true, auth["enabled"])
	persistence := payload["persistence"].(map[string]interface{})
	assert.Equal(t, true, persistence["auth_policy_enabled"])
	assert.Equal(t, true, persistence["usage_policy_enabled"])
	assert.Equal(t, true, persistence["mutation_policy_enabled"])
	assert.Equal(t, true, persistence["usage_ledger_enabled"])
	searchAdmin := payload["search_admin"].(map[string]interface{})
	assert.Equal(t, true, searchAdmin["admin_token_configured"])
	assert.Equal(t, float64(45), searchAdmin["reindex_cooldown_seconds"])
}

func TestAuthPolicyEndpoints_UpdateAndDelete(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        "jwt-secret",
		AdminRoles:       []string{"skills-admin"},
		APIKeyScopes: map[string]UsageScope{
			"scope-key-a": {TenantID: "tenant-a", ProjectID: "project-a", UserID: "alice"},
		},
	})

	updateReq := httptest.NewRequest(http.MethodPut, "/api/runtime/auth/policy", bytes.NewReader([]byte(`{
		"role_headers":["X-Role"],
		"role_claims":["roles"],
		"admin_roles":["platform-admin"],
		"api_key_scopes":{"scope-key-b":{"tenant_id":"tenant-b","project_id":"project-b","user_id":"bob"}}
	}`)))
	updateReq.RemoteAddr = "10.0.0.5:9000"
	updateReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	updateRec := httptest.NewRecorder()
	handler.UpdateAuthPolicy(updateRec, updateReq)
	require.Equal(t, http.StatusOK, updateRec.Code)

	var updatePayload map[string]interface{}
	require.NoError(t, json.Unmarshal(updateRec.Body.Bytes(), &updatePayload))
	policy := updatePayload["policy"].(map[string]interface{})
	assert.Contains(t, policy["admin_roles"].([]interface{}), "platform-admin")
	assert.Equal(t, float64(2), policy["api_key_scope_count"])

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/runtime/auth/policy", bytes.NewReader([]byte(`{"field":"api_key_scope","key":"scope-key-a"}`)))
	deleteReq.RemoteAddr = "10.0.0.5:9000"
	deleteReq.Header.Set("Authorization", "Bearer secret-token")
	deleteRec := httptest.NewRecorder()
	handler.DeleteAuthPolicyEntry(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code)

	var deletePayload map[string]interface{}
	require.NoError(t, json.Unmarshal(deleteRec.Body.Bytes(), &deletePayload))
	assert.Equal(t, true, deletePayload["deleted"])
	deletePolicy := deletePayload["policy"].(map[string]interface{})
	assert.Equal(t, float64(1), deletePolicy["api_key_scope_count"])
}

func TestUpdateAuthPolicy_RevertsOnPersistError(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:    true,
		AdminRoles: []string{"skills-admin"},
	})
	handler.SetAuthPolicyPersister(func(policy ScopeResolverConfig, changedBy string) error {
		return fmt.Errorf("persist failed")
	})

	updateReq := httptest.NewRequest(http.MethodPut, "/api/runtime/auth/policy", bytes.NewReader([]byte(`{
		"admin_roles":["platform-admin"]
	}`)))
	updateReq.RemoteAddr = "10.0.0.5:9000"
	updateReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	updateRec := httptest.NewRecorder()
	handler.UpdateAuthPolicy(updateRec, updateReq)
	require.Equal(t, http.StatusInternalServerError, updateRec.Code)

	policy := handler.getScopeResolverConfig()
	assert.Equal(t, []string{"skills-admin"}, policy.AdminRoles)
}

func TestUpdateUsagePolicy_RevertsOnPersistError(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 5,
	})
	handler.SetUsagePolicyPersister(func(policy UsagePolicy, changedBy string) error {
		return fmt.Errorf("persist failed")
	})

	updateReq := httptest.NewRequest(http.MethodPut, "/api/runtime/usage/policy", bytes.NewReader([]byte(`{
		"default_max_requests":10
	}`)))
	updateReq.RemoteAddr = "10.0.0.5:9000"
	updateReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	updateRec := httptest.NewRecorder()
	handler.UpdateUsagePolicy(updateRec, updateReq)
	require.Equal(t, http.StatusInternalServerError, updateRec.Code)

	policy := handler.getUsagePolicy()
	assert.Equal(t, 5, policy.DefaultMaxRequests)
}

func TestUpdateMutationPolicy_RevertsOnPersistError(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetSearchAdminToken("secret-token")
	handler.SetMutationPolicy(MutationPolicy{
		ReadOnly: true,
	})
	handler.SetMutationPolicyPersister(func(policy MutationPolicy, changedBy string) error {
		return fmt.Errorf("persist failed")
	})

	updateReq := httptest.NewRequest(http.MethodPut, "/api/runtime/mutation/policy", bytes.NewReader([]byte(`{
		"read_only":false,
		"disable_import":true
	}`)))
	updateReq.RemoteAddr = "10.0.0.5:9000"
	updateReq.Header.Set("X-Skills-Admin-Token", "secret-token")
	updateRec := httptest.NewRecorder()
	handler.UpdateMutationPolicy(updateRec, updateReq)
	require.Equal(t, http.StatusInternalServerError, updateRec.Code)

	policy := handler.getMutationPolicy()
	assert.True(t, policy.ReadOnly)
	assert.False(t, policy.DisableImport)
}

func TestUpdateSkill_PersistsBackToExistingExternalSource(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	externalDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir, externalDir})
	handler := NewHandler(registry, loader, mcpManager)

	sourceDir := filepath.Join(externalDir, "persisted-skill")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	sourcePath := filepath.Join(sourceDir, "skill.yaml")

	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "persisted-skill",
		Description: "old description",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"old"}, Weight: 1}},
	}))
	existingSkill, _ := registry.Get("persisted-skill")
	existingSkill.SetSource(sourcePath, sourceDir, skill.SkillSourceLayerExternal)

	body := `{
		"name":"persisted-skill",
		"description":"new description",
		"triggers":[{"type":"keyword","values":["new"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/runtime/skills/persisted-skill", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	req = mux.SetURLVars(req, map[string]string{"name": "persisted-skill"})
	rec := httptest.NewRecorder()
	handler.UpdateSkill(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	data, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "new description")
}

func TestDeleteSkill_DeleteFileRemovesExternalManifest(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)

	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "skill.yaml")
	require.NoError(t, os.WriteFile(sourcePath, []byte("name: deletable-skill"), 0o644))

	require.NoError(t, registry.Register(&skill.Skill{
		Name:         "deletable-skill",
		Description:  "delete test",
		SystemPrompt: "You are deletable.",
		Triggers:     []skill.Trigger{{Type: "keyword", Values: []string{"delete"}, Weight: 1}},
	}))
	skillItem, _ := registry.Get("deletable-skill")
	skillItem.SetSource(sourcePath, sourceDir, skill.SkillSourceLayerExternal)
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "prompt.md"), []byte("You are deletable."), 0o644))
	skillItem.SetPromptSource(filepath.Join(sourceDir, "prompt.md"))

	req := httptest.NewRequest(http.MethodDelete, "/api/runtime/skills/deletable-skill?delete_file=true", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req = mux.SetURLVars(req, map[string]string{"name": "deletable-skill"})
	rec := httptest.NewRecorder()
	handler.DeleteSkill(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"file_deleted":true`)
	_, err := os.Stat(sourcePath)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(sourceDir, "prompt.md"))
	assert.True(t, os.IsNotExist(err))
}

func TestDeleteSkill_DeleteFileRejectsSystemSkill(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)

	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "system-skill",
		Description: "system delete test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"delete"}, Weight: 1}},
	}))
	skillItem, _ := registry.Get("system-skill")
	skillItem.SetSource("C:/system/skill.yaml", "C:/system", skill.SkillSourceLayerSystem)

	req := httptest.NewRequest(http.MethodDelete, "/api/runtime/skills/system-skill?delete_file=true", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req = mux.SetURLVars(req, map[string]string{"name": "system-skill"})
	rec := httptest.NewRecorder()
	handler.DeleteSkill(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateSkill_ReadOnlyPolicyBlocksMutation(t *testing.T) {
	observability.GlobalMetrics = observability.NewRegistry()
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetMutationPolicy(MutationPolicy{ReadOnly: true})

	body := `{
		"name":"blocked-skill",
		"description":"read only test",
		"triggers":[{"type":"keyword","values":["blocked"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, registry.Count())
	assert.Contains(t, rec.Body.String(), "read-only")

	disabledCounter := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillMutationActions, map[string]string{
		observability.LabelAction:     skillMutationActionCreate,
		observability.LabelOutcome:    "disabled",
		observability.LabelAccessMode: "loopback",
	})
	assert.Equal(t, float64(1), disabledCounter.Get())
}

func TestCreateSkill_PersistDisabledBlocksPersist(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	loader.SetSkillDirs([]string{t.TempDir(), t.TempDir()})
	handler := NewHandler(registry, loader, mcpManager)
	handler.SetMutationPolicy(MutationPolicy{DisablePersist: true})

	body := `{
		"name":"persist-blocked-skill",
		"description":"persist disabled test",
		"triggers":[{"type":"keyword","values":["persist"],"weight":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills?persist=true", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.CreateSkill(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, registry.Count())
	assert.Contains(t, rec.Body.String(), "persistence is disabled")
}

func TestImportSkills_DisabledByPolicy(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetMutationPolicy(MutationPolicy{DisableImport: true})

	body := `{"skills":[{"name":"imported-skill","description":"blocked import","triggers":[{"type":"keyword","values":["import"],"weight":1}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/import", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ImportSkills(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "import is disabled")
}

func TestImportSkills_PersistsToExternalDirWithPromptCompanion(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	systemDir := t.TempDir()
	externalDir := t.TempDir()
	loader.SetSkillDirs([]string{systemDir, externalDir})
	handler := NewHandler(registry, loader, mcpManager)

	body := `{"skills":[{"name":"imported-persisted-skill","description":"persisted import","systemPrompt":"You are imported.","userPrompt":"Return imported.","triggers":[{"type":"keyword","values":["import"],"weight":1}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/import?persist=true", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ImportSkills(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["imported"])
	assert.Equal(t, float64(1), payload["persisted"])
	assert.Equal(t, float64(0), payload["failed"])

	importedSkill, ok := registry.Get("imported-persisted-skill")
	require.True(t, ok)
	require.NotNil(t, importedSkill.Source)
	assert.Equal(t, skill.SkillSourceLayerExternal, importedSkill.Source.Layer)

	promptPath := promptPathForSource(importedSkill.Source.Path)
	promptBytes, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "# System")
	assert.Contains(t, string(promptBytes), "You are imported.")
	assert.Contains(t, string(promptBytes), "# User")
	assert.Contains(t, string(promptBytes), "Return imported.")
}

func TestReloadSkills_DisabledByPolicy(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	loader.SetSkillDir(t.TempDir())
	handler := NewHandler(registry, loader, mcpManager)
	handler.SetMutationPolicy(MutationPolicy{DisableReloadOps: true})

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/reload", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ReloadSkills(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "reload is disabled")
}

func TestHotReloadStart_DisabledByPolicy(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	loader := skill.NewLoader(mcpManager)
	handler := NewHandler(registry, loader, mcpManager)
	handler.SetMutationPolicy(MutationPolicy{DisableHotReload: true})

	body := fmt.Sprintf(`{"dir":"%s"}`, strings.ReplaceAll(t.TempDir(), "\\", "\\\\"))
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/hot-reload/start", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.StartHotReload(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "hot reload is disabled")
}

func TestGetStats_IncludesMutationPolicy(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetMutationPolicy(MutationPolicy{
		ReadOnly:         true,
		DisableImport:    true,
		DisablePersist:   true,
		DisableReloadOps: true,
		DisableHotReload: true,
	})
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 10,
		DefaultMaxTokens:   1000,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/stats", nil)
	rec := httptest.NewRecorder()
	handler.GetStats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	policy, ok := payload["mutation_policy"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, policy["read_only"])
	assert.Equal(t, true, policy["disable_import"])
	assert.Equal(t, true, policy["disable_persist"])
	assert.Equal(t, true, policy["disable_reload_ops"])
	assert.Equal(t, true, policy["disable_hot_reload"])
	usagePolicy, ok := payload["usage_policy"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, usagePolicy["tracking_enabled"])
	assert.Equal(t, true, usagePolicy["quota_enabled"])
	assert.Equal(t, float64(10), usagePolicy["default_max_requests"])
	assert.Equal(t, float64(1000), usagePolicy["default_max_tokens"])
}

func TestExecuteSkill_RequestQuotaExceeded(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "quota-skill",
		Description: "quota test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"quota"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "quota ok").WithSkill("quota-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 1,
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	firstReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/quota-skill/execute", bytes.NewReader([]byte(`{"prompt":"one","user_id":"quota-user"}`)))
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, firstReq)
	require.Equal(t, http.StatusOK, firstRec.Code)

	secondReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/quota-skill/execute", bytes.NewReader([]byte(`{"prompt":"two","user_id":"quota-user"}`)))
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, secondReq)
	require.Equal(t, http.StatusTooManyRequests, secondRec.Code)
	assert.Contains(t, secondRec.Body.String(), "request quota exceeded")

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?user_id=quota-user", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(1), usage["request_count"])
	quota := payload["quota"].(map[string]interface{})
	assert.Equal(t, float64(0), quota["remaining_requests"])
}

func TestAgentChat_TokenQuotaExceeded(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:  true,
		QuotaEnabled:     true,
		DefaultMaxTokens: 1,
	})

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testLLMProvider{
		name:    "test-model",
		content: "should not run",
	}))
	handler.SetLLMRuntime(runtime)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"this should exceed token quota"}],"user_id":"token-user"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "token quota exceeded")

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?user_id=token-user", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(0), usage["request_count"])
}

func TestUsageStats_ResetUser(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "usage-skill",
		Description: "usage test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"usage"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "usage ok").WithSkill("usage-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{TrackingEnabled: true})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	execReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/usage-skill/execute", bytes.NewReader([]byte(`{"prompt":"record","user_id":"usage-user"}`)))
	execRec := httptest.NewRecorder()
	router.ServeHTTP(execRec, execReq)
	require.Equal(t, http.StatusOK, execRec.Code)

	resetReq := httptest.NewRequest(http.MethodPost, "/api/runtime/usage/reset", bytes.NewReader([]byte(`{"user_id":"usage-user"}`)))
	resetReq.RemoteAddr = "127.0.0.1:1234"
	resetRec := httptest.NewRecorder()
	router.ServeHTTP(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code)
	assert.Contains(t, resetRec.Body.String(), `"reset":true`)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?user_id=usage-user", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(0), usage["request_count"])
}

func TestExecuteSkill_RequestQuotaScopedByTenantProject(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "scoped-quota-skill",
		Description: "scoped quota test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"quota"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "scoped ok").WithSkill("scoped-quota-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 1,
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	firstReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/scoped-quota-skill/execute", bytes.NewReader([]byte(`{"prompt":"one","tenant_id":"tenant-a","project_id":"project-a","user_id":"scope-user"}`)))
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, firstReq)
	require.Equal(t, http.StatusOK, firstRec.Code)

	secondReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/scoped-quota-skill/execute", bytes.NewReader([]byte(`{"prompt":"two","tenant_id":"tenant-a","project_id":"project-a","user_id":"scope-user"}`)))
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, secondReq)
	require.Equal(t, http.StatusTooManyRequests, secondRec.Code)
	assert.Contains(t, secondRec.Body.String(), "scope_key")

	thirdReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/scoped-quota-skill/execute", bytes.NewReader([]byte(`{"prompt":"three","tenant_id":"tenant-a","project_id":"project-b","user_id":"scope-user"}`)))
	thirdRec := httptest.NewRecorder()
	router.ServeHTTP(thirdRec, thirdReq)
	require.Equal(t, http.StatusOK, thirdRec.Code)
}

func TestGetUsageStats_FiltersByTenantProjectScope(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "usage-scope-skill",
		Description: "usage scope test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"usage"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "usage scope ok").WithSkill("usage-scope-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{TrackingEnabled: true})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	reqA := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/usage-scope-skill/execute", bytes.NewReader([]byte(`{"prompt":"one","tenant_id":"tenant-a","project_id":"project-a","user_id":"scope-user"}`)))
	recA := httptest.NewRecorder()
	router.ServeHTTP(recA, reqA)
	require.Equal(t, http.StatusOK, recA.Code)

	reqB := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/usage-scope-skill/execute", bytes.NewReader([]byte(`{"prompt":"two","tenant_id":"tenant-b","project_id":"project-b","user_id":"scope-user"}`)))
	recB := httptest.NewRecorder()
	router.ServeHTTP(recB, reqB)
	require.Equal(t, http.StatusOK, recB.Code)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?tenant_id=tenant-a&project_id=project-a&user_id=scope-user", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	scope := payload["scope"].(map[string]interface{})
	assert.Equal(t, "tenant-a", scope["tenant_id"])
	assert.Equal(t, "project-a", scope["project_id"])
	assert.Equal(t, "scope-user", scope["user_id"])
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(1), usage["request_count"])
	quota := payload["quota"].(map[string]interface{})
	assert.Equal(t, "tenant-a/project-a/scope-user", quota["scope_key"])
}

func TestGetUsageStats_ResolvesQuotaPolicyPrecedence(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)

	tenantLimit := 2
	projectLimit := 1
	userLimit := 3
	handler.SetUsagePolicy(UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 5,
		TenantQuotas: map[string]UsageQuotaLimit{
			"tenant-a": {MaxRequests: &tenantLimit},
		},
		ProjectQuotas: map[string]UsageQuotaLimit{
			"tenant-a/project-a": {MaxRequests: &projectLimit},
		},
		UserQuotas: map[string]UsageQuotaLimit{
			"tenant-a/project-a/alice": {MaxRequests: &userLimit},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	checkQuota := func(path string) map[string]interface{} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		return payload["quota"].(map[string]interface{})
	}

	userQuota := checkQuota("/api/runtime/usage/stats?tenant_id=tenant-a&project_id=project-a&user_id=alice")
	assert.Equal(t, float64(3), userQuota["max_requests"])
	assert.Equal(t, "user", userQuota["resolved_from"])

	projectQuota := checkQuota("/api/runtime/usage/stats?tenant_id=tenant-a&project_id=project-a&user_id=bob")
	assert.Equal(t, float64(1), projectQuota["max_requests"])
	assert.Equal(t, "project", projectQuota["resolved_from"])

	tenantQuota := checkQuota("/api/runtime/usage/stats?tenant_id=tenant-a&project_id=project-b&user_id=bob")
	assert.Equal(t, float64(2), tenantQuota["max_requests"])
	assert.Equal(t, "tenant", tenantQuota["resolved_from"])

	defaultQuota := checkQuota("/api/runtime/usage/stats?tenant_id=tenant-b&project_id=project-z&user_id=bob")
	assert.Equal(t, float64(5), defaultQuota["max_requests"])
	assert.Equal(t, "default", defaultQuota["resolved_from"])
}

func TestUsagePolicyEndpoints_UpdateAndDelete(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/policy", nil)
	getReq.RemoteAddr = "127.0.0.1:1234"
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)

	updateBody := []byte(`{
		"tracking_enabled": true,
		"quota_enabled": true,
		"default_max_requests": 7,
		"tenants": {
			"tenant-a": {"max_requests": 3}
		},
		"projects": {
			"tenant-a/project-a": {"max_requests": 2}
		},
		"users": {
			"tenant-a/project-a/alice": {"max_requests": 1}
		}
	}`)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/runtime/usage/policy", bytes.NewReader(updateBody))
	updateReq.RemoteAddr = "127.0.0.1:1234"
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	require.Equal(t, http.StatusOK, updateRec.Code)

	var updatePayload map[string]interface{}
	require.NoError(t, json.Unmarshal(updateRec.Body.Bytes(), &updatePayload))
	policy := updatePayload["policy"].(map[string]interface{})
	assert.Equal(t, float64(7), policy["default_max_requests"])
	assert.Equal(t, true, policy["quota_enabled"])
	tenants := policy["tenants"].(map[string]interface{})
	assert.Contains(t, tenants, "tenant-a")

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?tenant_id=tenant-a&project_id=project-a&user_id=alice", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)
	var statsPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &statsPayload))
	quota := statsPayload["quota"].(map[string]interface{})
	assert.Equal(t, float64(1), quota["max_requests"])
	assert.Equal(t, "user", quota["resolved_from"])

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/runtime/usage/policy", bytes.NewReader([]byte(`{"level":"user","key":"tenant-a/project-a/alice"}`)))
	deleteReq.RemoteAddr = "127.0.0.1:1234"
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code)

	statsReq2 := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?tenant_id=tenant-a&project_id=project-a&user_id=alice", nil)
	statsReq2.RemoteAddr = "127.0.0.1:1234"
	statsRec2 := httptest.NewRecorder()
	router.ServeHTTP(statsRec2, statsReq2)
	require.Equal(t, http.StatusOK, statsRec2.Code)
	var statsPayload2 map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec2.Body.Bytes(), &statsPayload2))
	quota2 := statsPayload2["quota"].(map[string]interface{})
	assert.Equal(t, float64(2), quota2["max_requests"])
	assert.Equal(t, "project", quota2["resolved_from"])
}

func TestGetUsageLedger_ReturnsPersistedRecords(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "ledger-skill",
		Description: "ledger test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"ledger"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "ledger ok").WithSkill("ledger-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{TrackingEnabled: true})
	handler.SetUsageLedgerStore(&testUsageLedgerStore{})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	execReq := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/ledger-skill/execute", bytes.NewReader([]byte(`{"prompt":"record ledger","tenant_id":"tenant-a","project_id":"project-a","user_id":"alice"}`)))
	execRec := httptest.NewRecorder()
	router.ServeHTTP(execRec, execReq)
	require.Equal(t, http.StatusOK, execRec.Code)

	ledgerReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/ledger?tenant_id=tenant-a&project_id=project-a&user_id=alice&entrypoint=execute&skill=ledger-skill", nil)
	ledgerReq.RemoteAddr = "127.0.0.1:1234"
	ledgerRec := httptest.NewRecorder()
	router.ServeHTTP(ledgerRec, ledgerReq)
	require.Equal(t, http.StatusOK, ledgerRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(ledgerRec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["count"])
	records := payload["records"].([]interface{})
	require.Len(t, records, 1)
	record := records[0].(map[string]interface{})
	assert.Equal(t, true, record["success"])
	metadata := record["metadata"].(map[string]interface{})
	assert.Equal(t, "tenant-a", metadata["tenant_id"])
	assert.Equal(t, "project-a", metadata["project_id"])
	assert.Equal(t, "alice", metadata["user_id"])
	assert.Equal(t, "execute", metadata["entrypoint"])
	assert.Equal(t, "ledger-skill", metadata["skill"])
}

func TestExecuteSkill_ResolvesScopeFromHeaders(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "header-skill",
		Description: "header scope test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"header"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "header ok").WithSkill("header-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{TrackingEnabled: true})
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:        true,
		TenantHeaders:  []string{"X-Tenant-ID"},
		ProjectHeaders: []string{"X-Project-ID"},
		UserHeaders:    []string{"X-User-ID"},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/header-skill/execute", bytes.NewReader([]byte(`{"prompt":"record header scope"}`)))
	req.Header.Set("X-Tenant-ID", "tenant-header")
	req.Header.Set("X-Project-ID", "project-header")
	req.Header.Set("X-User-ID", "user-header")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?tenant_id=tenant-header&project_id=project-header&user_id=user-header", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(1), usage["request_count"])
}

func TestExecuteSkill_ResolvesScopeFromAPIKeyBinding(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "apikey-skill",
		Description: "api key scope test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"apikey"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "apikey ok").WithSkill("apikey-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{TrackingEnabled: true})
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"secret-scope-key": {TenantID: "tenant-api", ProjectID: "project-api", UserID: "user-api"},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/apikey-skill/execute", bytes.NewReader([]byte(`{"prompt":"record api key scope"}`)))
	req.Header.Set("Authorization", "Bearer secret-scope-key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?tenant_id=tenant-api&project_id=project-api&user_id=user-api", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(1), usage["request_count"])
}

func TestExecuteSkill_ResolvesScopeFromJWTClaims(t *testing.T) {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "jwt-skill",
		Description: "jwt scope test",
		Triggers:    []skill.Trigger{{Type: "keyword", Values: []string{"jwt"}, Weight: 1}},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "jwt ok").WithSkill("jwt-skill"), nil
		}),
	}))

	handler := NewHandler(registry, skill.NewLoader(mcpManager), mcpManager)
	handler.SetUsagePolicy(UsagePolicy{TrackingEnabled: true})
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        "jwt-secret",
		TenantClaims:     []string{"tenant_id"},
		ProjectClaims:    []string{"project_id"},
		UserClaims:       []string{"sub"},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant_id":  "tenant-jwt",
		"project_id": "project-jwt",
		"sub":        "user-jwt",
	})
	tokenString, err := token.SignedString([]byte("jwt-secret"))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/jwt-skill/execute", bytes.NewReader([]byte(`{"prompt":"record jwt scope"}`)))
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/usage/stats?tenant_id=tenant-jwt&project_id=project-jwt&user_id=user-jwt", nil)
	statsReq.RemoteAddr = "127.0.0.1:1234"
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &payload))
	usage := payload["usage"].(map[string]interface{})
	assert.Equal(t, float64(1), usage["request_count"])
}

func TestBuildOrchestrationPayload_AgentResultCountsObservedTools(t *testing.T) {
	observation := types.NewObservation("step_1_tool_0", "spawn_team")
	observation.MarkSuccess()

	payload := buildOrchestrationPayload("agent_react", false, nil, &agent.Result{
		Success:      true,
		Steps:        2,
		Observations: []types.Observation{*observation},
	}, nil, "")

	assert.Equal(t, 1, payload["tool_call_count"])
}

func promptPathForSource(sourcePath string) string {
	return filepath.Join(filepath.Dir(sourcePath), "prompt.md")
}
