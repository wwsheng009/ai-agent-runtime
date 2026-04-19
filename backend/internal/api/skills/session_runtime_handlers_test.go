package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type synchronizedResponseRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func newSynchronizedResponseRecorder() *synchronizedResponseRecorder {
	return &synchronizedResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *synchronizedResponseRecorder) Header() http.Header {
	return r.ResponseRecorder.Header()
}

func (r *synchronizedResponseRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(statusCode)
}

func (r *synchronizedResponseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}

func (r *synchronizedResponseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *synchronizedResponseRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

type runtimeCommandSequenceProvider struct {
	name      string
	responses []*llm.LLMResponse
	callCount int
}

func (p *runtimeCommandSequenceProvider) Name() string {
	return p.name
}

func (p *runtimeCommandSequenceProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if p.callCount >= len(p.responses) {
		return &llm.LLMResponse{
			Content: "done",
			Model:   p.name,
		}, nil
	}
	response := p.responses[p.callCount]
	p.callCount++
	return response, nil
}

func (p *runtimeCommandSequenceProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *runtimeCommandSequenceProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *runtimeCommandSequenceProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *runtimeCommandSequenceProvider) CheckHealth(ctx context.Context) error {
	return nil
}

type runtimeCommandCapturingMCPManager struct {
	lastMeta *team.RunMeta
}

func (m *runtimeCommandCapturingMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if toolName != "team_echo" {
		return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return skill.ToolInfo{
		Name:          toolName,
		Description:   "Echo tool for runtime command tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *runtimeCommandCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	runCtx, ok := ctx.(context.Context)
	if !ok {
		return nil, fmt.Errorf("unexpected context type %T", ctx)
	}
	meta, ok := team.GetRunMeta(runCtx)
	if !ok || meta == nil {
		return nil, fmt.Errorf("run meta missing")
	}
	m.lastMeta = meta.Clone()
	return "ok", nil
}

func (m *runtimeCommandCapturingMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("team_echo")
	return []skill.ToolInfo{info}
}

type runtimeCommandShellMCPManager struct{}

func (m *runtimeCommandShellMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if toolName != "run_shell_command" {
		return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return skill.ToolInfo{
		Name:          toolName,
		Description:   "Shell-like tool for runtime command tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *runtimeCommandShellMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *runtimeCommandShellMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("run_shell_command")
	return []skill.ToolInfo{info}
}

func TestSubmitSessionRuntimeCommand_SubmitPromptPropagatesRunMeta(t *testing.T) {
	mcpManager := &runtimeCommandCapturingMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-runtime-command-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandSequenceProvider{
		name: "test-runtime-command-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Use the tool.",
				Model:   "test-runtime-command-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_1",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello"},
					},
				},
			},
			{
				Content: "Finished.",
				Model:   "test-runtime-command-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"submit_prompt",
		"prompt":"Use the tool.",
		"run_meta":{
			"team":{
				"team_id":"team-1",
				"agent_id":"mate-1",
				"current_task_id":"task-1"
			}
		}
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := newSynchronizedResponseRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, mcpManager.lastMeta)
	require.NotNil(t, mcpManager.lastMeta.Team)
	assert.Equal(t, "team-1", mcpManager.lastMeta.Team.TeamID)
	assert.Equal(t, "mate-1", mcpManager.lastMeta.Team.AgentID)
	assert.Equal(t, "task-1", mcpManager.lastMeta.Team.CurrentTaskID)
}

func TestSubmitSessionRuntimeCommand_RewindReturnsRestoreResult(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionStorage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(sessionStorage, nil)
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)
	session.AddMessage(*types.NewUserMessage("first"))
	session.AddMessage(*types.NewAssistantMessage("second"))
	session.AddMessage(*types.NewUserMessage("third"))
	require.NoError(t, sessionStorage.Update(context.Background(), session))

	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })

	apiAgent := agent.NewAgent(&agent.Config{
		Name:  "runtime-command-rewind-test",
		Model: "test-model",
	}, nil)
	checkpointMgr := runtimecheckpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &runtimecheckpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 1,
		Conversation: []types.Message{
			*types.NewUserMessage("first"),
		},
	}
	checkpointID, err := checkpointMgr.AfterMutation(context.Background(), pending, nil, "")
	require.NoError(t, err)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return chat.NewSessionActor(sessionID, chat.SessionActorConfig{
			Agent:        apiAgent,
			SessionStore: sessionStorage,
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"rewind",
		"checkpoint_id":"`+checkpointID+`",
		"mode":"conversation"
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"conversation_changed":true`)
	require.Contains(t, rec.Body.String(), `"conversation_exact":true`)
}

func TestSubmitSessionRuntimeCommand_SubmitPromptReturnsAcceptedWhenApprovalIsPending(t *testing.T) {
	mcpManager := &runtimeCommandShellMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-runtime-command-pending-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandSequenceProvider{
		name: "test-runtime-command-pending-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Need shell access.",
				Model:   "test-runtime-command-pending-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_shell_1",
						Name: "run_shell_command",
						Args: map[string]interface{}{"command": "rg pending"},
					},
				},
			},
			{
				Content: "Finished.",
				Model:   "test-runtime-command-pending-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"submit_prompt",
		"prompt":"Inspect the repository."
	}`)).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Body.String(), `"pending":true`)
	assert.Contains(t, rec.Body.String(), `"status":"waiting_approval"`)
}

func TestSubmitSessionRuntimeCommand_SubmitPromptCompletesWithBootstrapWiring(t *testing.T) {
	bootstrap, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config: runtimecfg.DefaultRuntimeConfig(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = bootstrap.Stop()
	})

	handler := NewHandler(bootstrap.Registry(), bootstrap.Loader(), nil)
	bootstrap.ApplyToSkillsHandler(handler)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-runtime-bootstrap-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandSequenceProvider{
		name: "test-runtime-bootstrap-model",
		responses: []*llm.LLMResponse{
			{
				Content: "hi",
				Model:   "test-runtime-bootstrap-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	session, err := bootstrap.SessionManager().Create(context.Background(), "user-bootstrap")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"submit_prompt",
		"prompt":"Reply with exactly hi."
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"output":"hi"`)
}

func TestSessionAgentHTTP_SpawnAndStatus(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)

	parentSession, err := sessionManager.Create(context.Background(), "user-agent-http")
	require.NoError(t, err)
	parentSession.AddMessage(*types.NewUserMessage("parent history"))
	require.NoError(t, sessionManager.Update(context.Background(), parentSession))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	spawnReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+parentSession.ID+"/agents", strings.NewReader(`{
		"agent_type":"explorer",
		"fork_context":true
	}`))
	spawnRec := httptest.NewRecorder()
	router.ServeHTTP(spawnRec, spawnReq)
	require.Equal(t, http.StatusCreated, spawnRec.Code)

	var spawnPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(spawnRec.Body.Bytes(), &spawnPayload))
	agentPayload := spawnPayload["agent"].(map[string]interface{})
	childID := agentPayload["session_id"].(string)
	require.NotEmpty(t, childID)
	assert.Equal(t, parentSession.ID, agentPayload["parent_session_id"])
	assert.Equal(t, "explorer", agentPayload["agent_type"])

	statusReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+parentSession.ID+"/agents/"+childID, nil)
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	require.Equal(t, http.StatusOK, statusRec.Code)

	var statusPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &statusPayload))
	statusAgent := statusPayload["agent"].(map[string]interface{})
	assert.Equal(t, childID, statusAgent["session_id"])
	assert.Equal(t, parentSession.ID, statusAgent["parent_session_id"])
	assert.Equal(t, "explorer", statusAgent["agent_type"])
	assert.Equal(t, float64(1), statusAgent["message_count"])
}

func TestSessionAgentHTTP_SendWaitAndEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-agent-http-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandSequenceProvider{
		name: "test-agent-http-model",
		responses: []*llm.LLMResponse{
			{Content: "child done", Model: "test-agent-http-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	parentSession, err := sessionManager.Create(context.Background(), "user-agent-http")
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	spawnReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+parentSession.ID+"/agents", strings.NewReader(`{}`))
	spawnRec := httptest.NewRecorder()
	router.ServeHTTP(spawnRec, spawnReq)
	require.Equal(t, http.StatusCreated, spawnRec.Code)
	var spawnPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(spawnRec.Body.Bytes(), &spawnPayload))
	childID := spawnPayload["agent"].(map[string]interface{})["session_id"].(string)

	inputReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+parentSession.ID+"/agents/"+childID+"/input", strings.NewReader(`{
		"message":"say child done"
	}`))
	inputRec := httptest.NewRecorder()
	router.ServeHTTP(inputRec, inputReq)
	require.Equal(t, http.StatusAccepted, inputRec.Code)

	var agentResult map[string]interface{}
	require.Eventually(t, func() bool {
		waitReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+parentSession.ID+"/agents/wait", strings.NewReader(`{
			"ids":["`+childID+`"],
			"timeout_ms":5000
		}`))
		waitRec := httptest.NewRecorder()
		router.ServeHTTP(waitRec, waitReq)
		if waitRec.Code != http.StatusOK {
			return false
		}

		var waitPayload map[string]interface{}
		if err := json.Unmarshal(waitRec.Body.Bytes(), &waitPayload); err != nil {
			return false
		}
		waitResult, ok := waitPayload["result"].(map[string]interface{})
		if !ok {
			return false
		}
		currentAgentResult, ok := waitResult["agent"].(map[string]interface{})
		if !ok {
			return false
		}
		output, _ := currentAgentResult["output"].(string)
		if strings.TrimSpace(output) != "child done" {
			return false
		}
		agentResult = currentAgentResult
		return true
	}, 5*time.Second, 50*time.Millisecond)
	require.NotNil(t, agentResult)
	assert.Equal(t, childID, agentResult["session_id"])
	assert.Equal(t, "idle", agentResult["status"])
	assert.Equal(t, "child done", agentResult["output"])

	eventsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+parentSession.ID+"/agents/"+childID+"/events?after_seq=0&limit=20&wait_ms=0", nil)
	var eventsResult map[string]interface{}
	require.Eventually(t, func() bool {
		eventsRec := httptest.NewRecorder()
		router.ServeHTTP(eventsRec, eventsReq)
		if eventsRec.Code != http.StatusOK {
			return false
		}

		var eventsPayload map[string]interface{}
		if err := json.Unmarshal(eventsRec.Body.Bytes(), &eventsPayload); err != nil {
			return false
		}
		currentEventsResult, ok := eventsPayload["result"].(map[string]interface{})
		if !ok {
			return false
		}
		count, ok := currentEventsResult["count"].(float64)
		if !ok || int(count) < 1 {
			return false
		}
		eventsResult = currentEventsResult
		return true
	}, 5*time.Second, 50*time.Millisecond)
	require.NotNil(t, eventsResult)
	assert.Equal(t, childID, eventsResult["session_id"])
	assert.GreaterOrEqual(t, int(eventsResult["count"].(float64)), 1)
	assert.GreaterOrEqual(t, int(eventsResult["latest_seq"].(float64)), 1)
}

func TestListSessionToolReceiptsReturnsPersistedReceipts(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	require.NoError(t, runtimeStore.SaveToolReceipt(context.Background(), chat.ToolExecutionReceipt{
		SessionID:   "session-receipts",
		ToolCallID:  "tool_receipt_1",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"stored receipt","tool_call_id":"tool_receipt_1","metadata":{}}`),
	}))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-receipts/runtime/tool-receipts", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"tool_call_id":"tool_receipt_1"`)
	assert.Contains(t, rec.Body.String(), `"tool_name":"team_echo"`)
}

func TestListSessionToolReceiptsUsesExactLookupForToolCallID(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	require.NoError(t, runtimeStore.SaveToolReceipt(context.Background(), chat.ToolExecutionReceipt{
		SessionID:   "session-receipts-filter",
		ToolCallID:  "tool_receipt_old",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"old receipt","tool_call_id":"tool_receipt_old","metadata":{}}`),
		CreatedAt:   time.Now().UTC().Add(-1 * time.Minute),
	}))
	require.NoError(t, runtimeStore.SaveToolReceipt(context.Background(), chat.ToolExecutionReceipt{
		SessionID:   "session-receipts-filter",
		ToolCallID:  "tool_receipt_new",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"new receipt","tool_call_id":"tool_receipt_new","metadata":{}}`),
		CreatedAt:   time.Now().UTC(),
	}))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-receipts-filter/runtime/tool-receipts?tool_call_id=tool_receipt_old&limit=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"tool_call_id":"tool_receipt_old"`)
	assert.NotContains(t, rec.Body.String(), `"tool_call_id":"tool_receipt_new"`)
	assert.Contains(t, rec.Body.String(), `"count":1`)
}

func TestListSessionRuntimeEventsIncludesToolReceiptLedgerEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventToolReceiptRecorded,
		SessionID: "session-runtime-events",
		ToolName:  "team_echo",
		Payload: map[string]interface{}{
			"tool_call_id": "tool_receipt_1",
			"source":       "receipt_store",
			"receipt": map[string]interface{}{
				"session_id":   "session-runtime-events",
				"tool_call_id": "tool_receipt_1",
				"tool_name":    "team_echo",
				"created_at":   time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
			},
		},
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-events/runtime/events", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"tool_receipt_recorded"`)
	assert.Contains(t, rec.Body.String(), `"tool_call_id":"tool_receipt_1"`)
	assert.Contains(t, rec.Body.String(), `"source":"receipt_store"`)
}

func TestListSessionRuntimeEventsIncludesProfileProvenanceEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      "context.profile.injected",
		SessionID: "session-runtime-events-profile",
		TraceID:   "trace-profile-events",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      "recall.performed",
		SessionID: "session-runtime-events-profile",
		TraceID:   "trace-profile-events",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-events-profile/runtime/events", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	events := payload["events"].([]interface{})
	require.Len(t, events, 2)

	byType := make(map[string]map[string]interface{}, len(events))
	for _, raw := range events {
		event := raw.(map[string]interface{})
		byType[event["type"].(string)] = event
	}

	injected := byType["context.profile.injected"]
	require.NotNil(t, injected)
	injectedProvenance := injected["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), injectedProvenance["profile_context_injected"])
	assert.Equal(t, float64(1), injectedProvenance["profile_memory_count"])
	assert.Equal(t, float64(1), injectedProvenance["profile_resource_count"])
	assert.Contains(t, injectedProvenance["profile_resource_labels"].([]interface{}), "memory:memory.json")

	recall := byType["recall.performed"]
	require.NotNil(t, recall)
	recallProvenance := recall["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), recallProvenance["recall_with_source_refs"])
	assert.Equal(t, float64(1), recallProvenance["profile_notes_count"])
	assert.Equal(t, float64(1), recallProvenance["profile_resource_count"])
	assert.Contains(t, recallProvenance["profile_resource_labels"].([]interface{}), "notes:notes.md")
}

func TestListSessionRuntimeEventsIncludesCheckpointCreatedEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      "checkpoint_created",
		SessionID: "session-runtime-events-checkpoint",
		TraceID:   "trace-checkpoint-events",
		ToolName:  "execute_shell_command",
		Payload: map[string]interface{}{
			"checkpoint_id": "chk_profile_1",
			"source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-events-checkpoint/runtime/events", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	events := payload["events"].([]interface{})
	require.Len(t, events, 1)

	event := events[0].(map[string]interface{})
	assert.Equal(t, "checkpoint_created", event["type"])
	provenance := event["provenance"].(map[string]interface{})
	assert.Equal(t, float64(1), provenance["profile_memory_count"])
	assert.Equal(t, float64(1), provenance["profile_resource_count"])
	assert.Contains(t, provenance["profile_resource_labels"].([]interface{}), "memory:memory.json")
}

func TestListSessionRuntimeEventsReturnsEmptyArrayWhenNoEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-events-empty/runtime/events", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, float64(0), payload["count"])
	events, ok := payload["events"].([]interface{})
	require.True(t, ok, "expected events to be an array, got %#v", payload["events"])
	assert.Len(t, events, 0)
}

func TestStreamSessionRuntimeEventsIncludesToolReceiptLedgerEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventToolReceiptReplayed,
		SessionID: "session-runtime-stream",
		ToolName:  "team_echo",
		Payload: map[string]interface{}{
			"tool_call_id": "tool_receipt_2",
			"source":       "runtime_state",
			"receipt": map[string]interface{}{
				"session_id":   "session-runtime-stream",
				"tool_call_id": "tool_receipt_2",
				"tool_name":    "team_echo",
			},
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-stream/runtime/stream", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-stream"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(rec.BodyString(), `"type":"tool_receipt_replayed"`)
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}

	assert.Contains(t, rec.BodyString(), "event: runtime_event")
	assert.Contains(t, rec.BodyString(), `"type":"tool_receipt_replayed"`)
	assert.Contains(t, rec.BodyString(), `"trace_id":""`)
	assert.Contains(t, rec.BodyString(), `"agent_name":""`)
	assert.Contains(t, rec.BodyString(), `"session_id":"session-runtime-stream"`)
	assert.Contains(t, rec.BodyString(), `"tool_name":"team_echo"`)
	assert.Contains(t, rec.BodyString(), `"tool_call_id":"tool_receipt_2"`)
	assert.Contains(t, rec.BodyString(), `"source":"runtime_state"`)
	assert.NotContains(t, rec.BodyString(), `"data":{"type":"tool_receipt_replayed"`)
}

func TestStreamSessionRuntimeEventsIncludesCompactProvenanceSummary(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      "context.profile.injected",
		SessionID: "session-runtime-stream-provenance",
		TraceID:   "trace-stream-provenance",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-stream-provenance/runtime/stream", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-stream-provenance"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(rec.BodyString(), `"type":"context.profile.injected"`)
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}

	assert.Contains(t, rec.BodyString(), `"provenance":{`)
	assert.Contains(t, rec.BodyString(), `"profile_context_injected":1`)
	assert.Contains(t, rec.BodyString(), `"profile_memory_count":1`)
	assert.Contains(t, rec.BodyString(), `"profile_resource_count":1`)
	assert.Contains(t, rec.BodyString(), `"profile_resource_labels":["memory:memory.json"]`)
	assert.NotContains(t, rec.BodyString(), `"data":{"type":"context.profile.injected"`)
}
