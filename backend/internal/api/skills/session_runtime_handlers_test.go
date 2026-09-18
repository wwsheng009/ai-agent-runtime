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
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
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

func TestSessionRuntimeToolDefinitionsUsesStableSurface(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(64)
	require.NoError(t, store.SaveState(context.Background(), &chat.RuntimeState{
		SessionID: "session-stable-tools",
		Status:    chat.SessionIdle,
		StableToolSurface: []types.ToolDefinition{
			{Name: "get_goal", Description: "Read goal"},
			{Name: "update_goal", Description: "Complete goal"},
		},
		StableToolSurfaceSet: true,
	}))

	handler := &Handler{sessionRuntimeStore: store}
	tools := handler.sessionRuntimeToolDefinitions(context.Background(), "session-stable-tools")
	require.Len(t, tools, 2)
	assert.Equal(t, "get_goal", tools[0].Name)
	assert.Equal(t, "update_goal", tools[1].Name)
}

type runtimeCommandCapturingProvider struct {
	runtimeCommandSequenceProvider
	requests []*llm.LLMRequest
}

func (p *runtimeCommandCapturingProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, req)
	return p.runtimeCommandSequenceProvider.Call(ctx, req)
}

type runtimeCommandCapturingMCPManager struct {
	lastMeta *team.RunMeta
}

func TestPublishTeamSessionExecutionFailure_UsesWrappedTraceAndMetadata(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	err := team.WrapSessionExecutionError(assert.AnError, &team.SessionResult{
		TraceID:   "trace-team-plan-preflight",
		ErrorType: "prompt_preflight",
		ErrorMetadata: map[string]interface{}{
			"failure_reason_code":         "prompt_still_exceeds_budget_after_compaction",
			"replacement_history_applied": true,
		},
	})
	payload := map[string]interface{}{
		"team_id": "team-1",
	}

	traceID := handler.publishTeamSessionExecutionFailure("team.plan.failed", "trace-request", payload, err)
	assert.Equal(t, "trace-team-plan-preflight", traceID)

	events := handler.getRuntimeEventBus().Trace(traceID, 10)
	require.NotEmpty(t, events)
	event := events[0]
	assert.Equal(t, "team.plan.failed", event.Type)
	assert.Equal(t, "prompt_preflight", event.Payload["error_type"])
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", event.Payload["failure_reason_code"])
	assert.Equal(t, true, event.Payload["replacement_history_applied"])
}

func TestSessionResultFromActorRun_PromptPreflightCarriesStructuredErrorMetadata(t *testing.T) {
	sessionResult := sessionResultFromActorRun(&agent.Result{
		Success: false,
		Output:  "partial output",
		Error:   "prompt preflight budget exceeded",
		TraceID: "trace-session-preflight",
		Steps:   2,
	}, &agent.PromptPreflightError{
		PromptTokens:                  12000,
		PromptBudget:                  9000,
		BudgetSource:                  "model_capability_auto_compact_token_limit",
		ResolvedProvider:              "CODEX_LOCAL",
		ResolvedModel:                 "codex-gpt-5.4",
		Code:                          "prompt_still_exceeds_budget_after_compaction",
		Reason:                        "prompt exceeds configured budget",
		ReplacementHistoryApplied:     true,
		ReplacementHistory:            []types.Message{*types.NewSystemMessage("system"), *types.NewAssistantMessage("summary")},
		ActiveTurnCompacted:           true,
		ActiveTurnMessageCount:        8,
		LatestReplayBlockMessageCount: 3,
	})
	require.NotNil(t, sessionResult)
	assert.Equal(t, "trace-session-preflight", sessionResult.TraceID)
	assert.Equal(t, "prompt_preflight", sessionResult.ErrorType)
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", sessionResult.ErrorMetadata["failure_reason_code"])
	assert.Equal(t, true, sessionResult.ErrorMetadata["replacement_history_applied"])
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

type runtimeToolSurfaceMCPManager struct{}

func (m *runtimeToolSurfaceMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	for _, info := range m.ListTools() {
		if info.Name == toolName {
			return info, nil
		}
	}
	return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
}

func (m *runtimeToolSurfaceMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *runtimeToolSurfaceMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{
			Name:        "remote_echo",
			Description: "Echo tool exposed by runtime-server",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{"type": "string"},
				},
			},
			Metadata: map[string]interface{}{
				"source": "test",
			},
			MCPName: "test-mcp",
			Enabled: true,
		},
	}
}

func TestListSessionRuntimeTools_ReturnsMCPToolSurface(t *testing.T) {
	mcpManager := &runtimeToolSurfaceMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-tools/runtime/tools", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var decoded struct {
		SessionID string                 `json:"session_id"`
		Tools     []types.ToolDefinition `json:"tools"`
		Count     int                    `json:"count"`
		Source    string                 `json:"source"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&decoded))
	require.Equal(t, "session-tools", decoded.SessionID)
	require.Equal(t, "runtime_server", decoded.Source)
	require.Equal(t, 1, decoded.Count)
	require.Len(t, decoded.Tools, 1)
	require.Equal(t, "remote_echo", decoded.Tools[0].Name)
	require.Equal(t, "Echo tool exposed by runtime-server", decoded.Tools[0].Description)
	require.Equal(t, "object", decoded.Tools[0].Parameters["type"])
	require.Equal(t, "test", decoded.Tools[0].Metadata["source"])
	require.NotEqual(t, "update_goal", decoded.Tools[0].Name)
}

func TestListSessionRuntimeTools_NoMCPManagerReturnsEmptySurface(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-tools/runtime/tools", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "session-tools"})
	rec := httptest.NewRecorder()

	handler.ListSessionRuntimeTools(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var decoded struct {
		Tools []types.ToolDefinition `json:"tools"`
		Count int                    `json:"count"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&decoded))
	require.Equal(t, 0, decoded.Count)
	require.Empty(t, decoded.Tools)
}

func TestListSessionRuntimeTools_IncludesGoalToolsForManagedSession(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/tools", nil)
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.ListSessionRuntimeTools(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var decoded struct {
		Tools []types.ToolDefinition `json:"tools"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&decoded))
	require.True(t, runtimeToolDefinitionsContain(decoded.Tools, runtimegoal.GetToolName))
	require.True(t, runtimeToolDefinitionsContain(decoded.Tools, runtimegoal.UpdateToolName))
}

func TestListSessionRuntimeTools_DisabledToolsReturnsEmptySurface(t *testing.T) {
	mcpManager := &runtimeToolSurfaceMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	session.Metadata.Context[sessionmeta.DisableTools] = true
	require.NoError(t, sessionManager.Update(context.Background(), session))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/tools", nil)
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.ListSessionRuntimeTools(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var decoded struct {
		Tools []types.ToolDefinition `json:"tools"`
		Count int                    `json:"count"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&decoded))
	require.Equal(t, 0, decoded.Count)
	require.Empty(t, decoded.Tools)
}

func runtimeToolDefinitionsContain(tools []types.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func runtimeMessagesContain(messages []types.Message, role, content string) bool {
	for _, message := range messages {
		if message.Role == role && strings.Contains(message.Content, content) {
			return true
		}
	}
	return false
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

func TestSubmitSessionRuntimeCommand_GoalCapabilityCompletesGoal(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-runtime-goal-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandCapturingProvider{
		runtimeCommandSequenceProvider: runtimeCommandSequenceProvider{
			name: "test-runtime-goal-model",
			responses: []*llm.LLMResponse{
				{
					Content: "The goal is complete.",
					Model:   "test-runtime-goal-model",
					ToolCalls: []types.ToolCall{{
						ID:   "tool_goal_1",
						Name: runtimegoal.UpdateToolName,
						Args: map[string]interface{}{
							"status":  string(runtimegoal.StatusComplete),
							"summary": "runtime-server goal test completed",
						},
					}},
				},
				{
					Content: "Done.",
					Model:   "test-runtime-goal-model",
				},
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)
	goal, err := runtimegoal.NewSessionGoal(session.ID, "Complete the runtime-server goal test", time.Now())
	require.NoError(t, err)
	_, err = runtimegoal.NewMetadataStore().PutPersistent(context.Background(), sessionManager.GetStorage(), session.ID, goal, runtimegoal.MutationUser)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"submit_prompt",
		"prompt":"Audit and complete the goal."
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := newSynchronizedResponseRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)
	require.True(t, runtimeToolDefinitionsContain(provider.requests[0].Tools, runtimegoal.UpdateToolName))

	loaded, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	completed, ok, err := runtimegoal.NewMetadataStore().Get(loaded)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, runtimegoal.StatusComplete, completed.Status)
	require.Equal(t, "model", completed.CompletedBy)
	require.Equal(t, "runtime-server goal test completed", completed.CompletionSummary)
}

func TestSubmitSessionRuntimeCommand_ContinueUsesTransientPromptAndStripsIt(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-runtime-continue-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandCapturingProvider{
		runtimeCommandSequenceProvider: runtimeCommandSequenceProvider{
			name: "test-runtime-continue-model",
			responses: []*llm.LLMResponse{{
				Content: "runtime audit complete",
				Model:   "test-runtime-continue-model",
			}},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)
	session.AddMessage(*types.NewAssistantMessage("previous result"))
	require.NoError(t, sessionManager.Update(context.Background(), session))

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"continue",
		"prompt":"hidden runtime completion audit",
		"continuation_metadata":{"hidden_goal_audit":true},
		"strip_metadata_keys":["hidden_goal_audit"]
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := newSynchronizedResponseRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, provider.requests)
	require.True(t, runtimeMessagesContain(provider.requests[0].Messages, "user", "hidden runtime completion audit"))
	loaded, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	require.False(t, runtimeMessagesContain(loaded.History, "user", "hidden runtime completion audit"))
	require.True(t, runtimeMessagesContain(loaded.History, "assistant", "runtime audit complete"))
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
	checkpointMgr.ConversationSnapshot = true
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

func TestCompactCommandPayloadMapsFullResult(t *testing.T) {
	usage := &types.TokenUsage{
		UsageSource:      "provider",
		PromptTokens:     120,
		CompletionTokens: 30,
		TotalTokens:      150,
	}
	payload := compactCommandPayload(&compactruntime.Result{
		Mode:              "local",
		Phase:             compactruntime.PhasePreTurn,
		ResolvedProvider:  "openai",
		ResolvedModel:     "gpt-test",
		TriggerTokenLimit: 6800,
		MaxContextTokens:  8000,
		TokenBefore:       7000,
		TokenAfter:        2100,
		Usage:             usage,
		UsageSource:       "provider",
		CompactedMessages: 12,
		CheckpointIDs:     []string{"ckpt-1", "ckpt-2"},
	}, compactruntime.Status{
		Mode:              "local",
		Phase:             compactruntime.PhasePreTurn,
		ResolvedProvider:  "openai",
		ResolvedModel:     "gpt-test",
		TriggerTokenLimit: 6800,
		MaxContextTokens:  8000,
		TokenBefore:       7000,
	})

	status, ok := payload["status"].(map[string]interface{})
	require.True(t, ok, "status payload must be a map")
	assert.Equal(t, "local", status["mode"])
	assert.Equal(t, compactruntime.PhasePreTurn, status["phase"])
	assert.Equal(t, "", status["reason"])
	assert.Equal(t, "openai", status["provider"])
	assert.Equal(t, "gpt-test", status["model"])
	assert.Equal(t, 6800, status["trigger_token_limit"])
	assert.Equal(t, 8000, status["max_context_tokens"])
	assert.Equal(t, 7000, status["token_before"])
	assert.NotContains(t, status, "token_after", "status 只暴露压缩前快照")

	result, ok := payload["result"].(map[string]interface{})
	require.True(t, ok, "result payload must be a map")
	assert.Equal(t, "local", result["mode"])
	assert.Equal(t, 2100, result["token_after"])
	assert.Equal(t, 12, result["compacted_messages"])
	assert.Equal(t, []string{"ckpt-1", "ckpt-2"}, result["checkpoint_ids"])
	assert.Equal(t, "provider", result["usage_source"])
	assert.Equal(t, usage, result["usage"], "usage 原样透传")
}

func TestCompactCommandPayloadNilResultKeepsStableKeys(t *testing.T) {
	payload := compactCommandPayload(nil, compactruntime.Status{
		Mode:   "auto",
		Phase:  compactruntime.PhasePreTurn,
		Reason: "history_empty",
	})

	assert.Nil(t, payload["result"], "skipped 必须显式回 null")
	status, ok := payload["status"].(map[string]interface{})
	require.True(t, ok, "status payload must be a map")
	assert.Equal(t, "auto", status["mode"])
	assert.Equal(t, compactruntime.PhasePreTurn, status["phase"])
	assert.Equal(t, "history_empty", status["reason"])
	assert.Equal(t, "", status["provider"])
	assert.Equal(t, "", status["model"])
	assert.Equal(t, 0, status["trigger_token_limit"])
	assert.Equal(t, 0, status["max_context_tokens"])
	assert.Equal(t, 0, status["token_before"])
}

func TestCompactCommandPayloadNormalizesNilCheckpointIDs(t *testing.T) {
	payload := compactCommandPayload(&compactruntime.Result{Mode: "remote"}, compactruntime.Status{Mode: "remote"})

	result, ok := payload["result"].(map[string]interface{})
	require.True(t, ok, "result payload must be a map")
	assert.Equal(t, []string{}, result["checkpoint_ids"], "nil 的 checkpoint_ids 归一成空数组")
	assert.Nil(t, result["usage"], "无 usage 时回 null")
}

func TestNormalizeSessionCompactMode(t *testing.T) {
	cases := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{raw: "", want: "", wantOK: true},
		{raw: "auto", want: "auto", wantOK: true},
		{raw: " AUTO ", want: "auto", wantOK: true},
		{raw: "local", want: "local", wantOK: true},
		{raw: "remote", want: "remote", wantOK: true},
		{raw: "bogus", want: "", wantOK: false},
		{raw: "manual", want: "", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := normalizeSessionCompactMode(tc.raw)
		assert.Equal(t, tc.wantOK, ok, "raw=%q", tc.raw)
		assert.Equal(t, tc.want, got, "raw=%q", tc.raw)
	}
}

// newCompactCommandHandler 构造一个接上真实 SessionActor 的 Handler 及其会话 ID，
// 供 compact 命令的 HTTP 层用例复用（与 rewind/answer 用例同一套装配方式）。
func newCompactCommandHandler(t *testing.T) (*Handler, string) {
	t.Helper()

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionStorage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(sessionStorage, nil)
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-compact-command")
	require.NoError(t, err)

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-runtime-command-compact-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandSequenceProvider{name: "test-runtime-command-compact-model"}
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgent(&agent.Config{
		Name:  "runtime-command-compact-test",
		Model: "test-model",
	}, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   llmRuntime,
		SessionStore: sessionStorage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})
	return handler, session.ID
}

func TestSubmitSessionRuntimeCommand_CompactRejectsUnsupportedMode(t *testing.T) {
	handler, sessionID := newCompactCommandHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/runtime/commands", strings.NewReader(`{
		"type":"compact",
		"mode":"manual"
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": sessionID})
	rec := httptest.NewRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "unsupported compact mode")
}

func TestSubmitSessionRuntimeCommand_CompactSkippedReturnsNullResult(t *testing.T) {
	handler, sessionID := newCompactCommandHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/runtime/commands", strings.NewReader(`{
		"type":"compact",
		"mode":"local"
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": sessionID})
	rec := httptest.NewRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var payload map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	assert.Equal(t, true, payload["ok"])
	assert.Nil(t, payload["result"], "skipped 必须回 result:null")
	status, ok := payload["status"].(map[string]interface{})
	require.True(t, ok, "status payload must be a map")
	assert.Equal(t, "local", status["mode"])
	assert.Equal(t, "history_empty", status["reason"])
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
						// Must not be shell-readonly (e.g. "rg …"); those auto-allow and
						// complete submit_prompt with 200 instead of waiting_approval/202.
						Args: map[string]interface{}{"command": "git commit -m pending"},
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

func TestSubmitSessionRuntimeCommand_AnswerQuestionAllowsEmptyAnswer(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionStorage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(sessionStorage, nil)
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)

	apiAgent := agent.NewAgent(&agent.Config{
		Name:  "runtime-command-empty-answer-test",
		Model: "test-model",
	}, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: sessionStorage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})

	answerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		answer, askErr := actor.AskUserQuestion(context.Background(), toolbroker.UserQuestionRequest{
			ID:         "question_empty",
			Prompt:     "Optional answer?",
			ToolCallID: "tool_question_empty",
		})
		if askErr != nil {
			errCh <- askErr
			return
		}
		answerCh <- answer
	}()

	require.Eventually(t, func() bool {
		state := actor.State()
		return state != nil &&
			state.Status == chat.SessionWaitingInput &&
			state.PendingQuestion != nil &&
			state.PendingQuestion.ID == "question_empty"
	}, 2*time.Second, 20*time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/commands", strings.NewReader(`{
		"type":"answer_question",
		"question_id":"question_empty",
		"answer":""
	}`))
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.SubmitSessionRuntimeCommand(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	select {
	case answer := <-answerCh:
		assert.Equal(t, "", answer)
	case askErr := <-errCh:
		t.Fatalf("ask user question failed: %v", askErr)
	case <-time.After(2 * time.Second):
		t.Fatal("question waiter did not receive empty answer")
	}
	state := actor.State()
	require.NotNil(t, state)
	assert.Nil(t, state.PendingQuestion)
	assert.Nil(t, state.PendingTool)
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

func TestWaitSessionAgentsWithoutTargetUsesParentMailbox(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	parentSession, err := sessionManager.Create(ctx, "user-agent-http-parent-mailbox")
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	waitReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+parentSession.ID+"/agents/wait", strings.NewReader(`{
		"timeout_ms":2000
	}`))
	waitRec := newSynchronizedResponseRecorder()
	waitDone := make(chan struct{})
	go func() {
		router.ServeHTTP(waitRec, waitReq)
		close(waitDone)
	}()

	time.Sleep(100 * time.Millisecond)
	_, err = runtimeStore.AppendEvent(ctx, chat.NewMailboxReceivedEvent(parentSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "http parent mailbox hello",
	}))
	require.NoError(t, err)

	select {
	case <-waitDone:
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait endpoint did not wake from parent mailbox event")
	}

	require.Equal(t, http.StatusOK, waitRec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(waitRec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	event := result["event"].(map[string]interface{})
	assert.Equal(t, chat.EventMailboxReceived, event["type"])
	assert.Equal(t, parentSession.ID, event["session_id"])
	assert.Equal(t, float64(1), result["ready_count"])
	assert.GreaterOrEqual(t, int(result["latest_seq"].(float64)), 1)
	eventPayload := event["payload"].(map[string]interface{})
	assert.Equal(t, "http parent mailbox hello", eventPayload["body"])
}

func TestListSessionAgentEventsWithoutAgentReadsParentMailbox(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	parentSession, err := sessionManager.Create(context.Background(), "user-agent-http-parent-events")
	require.NoError(t, err)
	_, err = runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: parentSession.ID,
		Payload:   map[string]interface{}{"content": "not mailbox"},
	})
	require.NoError(t, err)
	_, err = runtimeStore.AppendEvent(context.Background(), chat.NewMailboxReceivedEvent(parentSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "http parent mailbox events read hello",
	}))
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+parentSession.ID+"/agents/events?after_seq=0&limit=20&wait_ms=0", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, parentSession.ID, result["session_id"])
	assert.Equal(t, float64(1), result["count"])
	events := result["events"].([]interface{})
	require.Len(t, events, 1)
	event := events[0].(map[string]interface{})
	assert.Equal(t, chat.EventMailboxReceived, event["type"])
	eventPayload := event["payload"].(map[string]interface{})
	assert.Equal(t, "http parent mailbox events read hello", eventPayload["body"])
}

func TestListSessionAgentControlMailboxReadsOnlyControlRows(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	parentSession, err := sessionManager.Create(ctx, "user-agent-control-mailbox")
	require.NoError(t, err)
	_, _, err = runtimeStore.AppendMailbox(ctx, parentSession.ID, team.MailMessage{
		FromAgent: "legacy-child",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "legacy mailbox row",
	})
	require.NoError(t, err)
	metadata := agentcontrol.ApplyEnvelope(map[string]interface{}{"target_session_id": parentSession.ID}, agentcontrol.Envelope{
		MessageType:     agentcontrol.MessageTypeAgentMessage,
		ControlAction:   agentcontrol.ActionAgentMessage,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindAgentMessage,
	})
	_, _, err = runtimeStore.AppendAgentControlMailbox(ctx, parentSession.ID, team.MailMessage{
		FromAgent: "control-child",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "control mailbox row",
		Metadata:  metadata,
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+parentSession.ID+"/agent-control/mailbox?after_seq=0&limit=20&wait_ms=0", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	result := payload["result"].(map[string]interface{})
	assert.Equal(t, parentSession.ID, result["session_id"])
	assert.Equal(t, "agent_control_mailbox", result["source"])
	assert.Equal(t, true, result["control_only"])
	assert.Equal(t, float64(1), result["count"])
	assert.Equal(t, float64(1), result["latest_seq"])
	messages := result["messages"].([]interface{})
	require.Len(t, messages, 1)
	message := messages[0].(map[string]interface{})
	assert.Equal(t, "control-child", message["from_agent"])
	assert.Equal(t, "control mailbox row", message["body"])
	assert.Equal(t, float64(1), message["seq"])
	assert.Equal(t, float64(1), message["control_seq"])
	assert.Equal(t, float64(2), message["session_mailbox_seq"])
	msgMetadata := message["metadata"].(map[string]interface{})
	assert.Equal(t, agentcontrol.MessageTypeAgentMessage, msgMetadata["message_type"])
	assert.Equal(t, agentcontrol.ActionAgentMessage, msgMetadata["control_action"])
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
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["latest_seq"])
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

func TestListSessionRuntimeEventsWaitsForNewEvent(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-events-wait/runtime/events?after_seq=0&wait_ms=1500", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-events-wait"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.ListSessionRuntimeEvents(rec, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: "session-runtime-events-wait",
		Payload:   map[string]interface{}{"content": "long poll response"},
	})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll runtime events request did not wake after event append")
	}

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(rec.BodyString()), &payload))
	assert.Equal(t, float64(1), payload["count"])
	assert.Equal(t, float64(1), payload["latest_seq"])
	assert.Equal(t, float64(0), payload["after_seq"])
	assert.Nil(t, payload["timed_out"])
	events := payload["events"].([]interface{})
	require.Len(t, events, 1)
	event := events[0].(map[string]interface{})
	assert.Equal(t, chat.EventAssistantMessage, event["type"])
	eventPayload := event["payload"].(map[string]interface{})
	assert.Equal(t, "long poll response", eventPayload["content"])
}

func TestListSessionRuntimeEventsWaitTimeoutReturnsLatestSeq(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: "session-runtime-events-wait-timeout",
		Payload:   map[string]interface{}{"content": "existing"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-events-wait-timeout/runtime/events?after_seq=1&wait_ms=25", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-events-wait-timeout"})
	rec := httptest.NewRecorder()

	handler.ListSessionRuntimeEvents(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(0), payload["count"])
	assert.Equal(t, float64(1), payload["latest_seq"])
	assert.Equal(t, float64(1), payload["after_seq"])
	assert.Equal(t, true, payload["timed_out"])
	events := payload["events"].([]interface{})
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

func TestStreamSessionRuntimeEventsWakesFromEventWatcher(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-stream-watch/runtime/stream?poll_ms=5000", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-stream-watch"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: "session-runtime-stream-watch",
		Payload:   map[string]interface{}{"content": "watcher stream response"},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return strings.Contains(rec.BodyString(), "watcher stream response")
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}

	assert.Contains(t, rec.BodyString(), "event: runtime_event")
	assert.Contains(t, rec.BodyString(), `"type":"assistant_message"`)
	assert.Contains(t, rec.BodyString(), `"content":"watcher stream response"`)
}

func TestStreamSessionRuntimeEventsLiveProgressFromBus(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	// Arm durable bridge via bus accessor; tool.progress must still stay non-persisted.
	_ = handler.getRuntimeEventBus()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-stream-live/runtime/stream?live=1&poll_ms=5000", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-stream-live"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: "session-runtime-stream-live",
		ToolName:  "shell",
		TraceID:   "trace-live-progress",
		Payload: map[string]interface{}{
			"tool_call_id": "call-live-1",
			"kind":         "progress",
			"message":      "running step 1",
			"percent":      40.0,
		},
	})
	// Other session must not leak into this stream.
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: "session-other",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"tool_call_id": "call-other",
			"message":      "should-not-appear",
		},
	})

	require.Eventually(t, func() bool {
		body := rec.BodyString()
		return strings.Contains(body, `"type":"tool.progress"`) &&
			strings.Contains(body, `"running step 1"`) &&
			strings.Contains(body, `"live":true`)
	}, 2*time.Second, 20*time.Millisecond)

	// Progress remains live-only: not written to the durable session store.
	stored, err := runtimeStore.ListEvents(context.Background(), "session-runtime-stream-live", 0, 0)
	require.NoError(t, err)
	for _, event := range stored {
		assert.NotEqual(t, toolprotocol.EventTypeProgress, event.Type)
	}

	body := rec.BodyString()
	assert.Contains(t, body, "event: runtime_event")
	assert.Contains(t, body, `"tool_call_id":"call-live-1"`)
	assert.NotContains(t, body, "should-not-appear")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}
}

func TestStreamSessionRuntimeEventsWithoutLiveFlagIgnoresBusProgress(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-stream-no-live/runtime/stream?poll_ms=5000", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-stream-no-live"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: "session-runtime-stream-no-live",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"tool_call_id": "call-hidden",
			"message":      "progress-without-live-flag",
		},
	})

	time.Sleep(200 * time.Millisecond)
	assert.NotContains(t, rec.BodyString(), "progress-without-live-flag")
	assert.NotContains(t, rec.BodyString(), `"type":"tool.progress"`)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}
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

// 回归：游标已追平（after == latest_seq）的空闲会话没有任何历史事件可发，
// handler 必须立即提交并 flush 响应头（200 + text/event-stream），否则 Go 的
// net/http 直到第一帧才发响应头，客户端的 fetch 会长期不 resolve：onOpen 不
// 触发、连接状态卡在 connecting、中间 proxy 只能看到「等待响应」。
func TestStreamSessionRuntimeEventsCommitsHeadersWithoutPendingEvents(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// after=3 追平不存在的历史：既无历史回放，也无 live 事件。
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-runtime-stream-idle/runtime/stream?after=3&live=1&poll_ms=500", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": "session-runtime-stream-idle"})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.Flushed
	}, 2*time.Second, 10*time.Millisecond, "stream handler must flush headers before any event frame")

	rec.mu.Lock()
	status := rec.Code
	contentType := rec.Header().Get("Content-Type")
	rec.mu.Unlock()
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "text/event-stream", contentType)
	assert.NotContains(t, rec.BodyString(), "event: runtime_event")
	// 首字节握手帧：只 flush 响应头不足以穿透中间代理（代理要等第一个响应体字节
	// 才转发头部），因此建连必须立刻写出一帧注释。
	assert.Contains(t, rec.BodyString(), ": open")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}
}

// 回归（真 TCP）：SSE 是「字节必须立刻出站」的协议，而 httptest.ResponseRecorder
// 只是把字节同步落进内存 —— flush 有没有真正走到 socket，在 recorder 上永远观察不到。
// 此前 flushSSE 只调 bufio.Flush，字节停在 net/http 自己的 2048B 响应缓冲里
// （server.go 的 bufferBeforeChunkingSize），客户端要等缓冲攒满或 handler 返回才收到
// 任何字节：线上表现为「直连 8101 的空闲会话 25s 零字节」，而单测全绿。
// 本用例走真实 socket 与默认 http.Server 写链路（含 net/http 内部缓冲与 chunked
// 编码），断言空闲会话在建连后立即收到 `: open` 握手帧。
func TestStreamSessionRuntimeEventsFlushesHandshakeFrameOverRealTCP(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime/stream", handler.StreamSessionRuntimeEvents).Methods(http.MethodGet)
	server := httptest.NewServer(router)
	// 注意 defer 顺序：cancel 在 server.Close 之后注册 ⇒ 先取消请求，让 handler
	// 从 select 里退出，server.Close 才不用等这个永不返回的 SSE 流。
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// after=3 追平不存在的历史 + live=1：没有任何业务帧可发，建连后的首批字节只可能
	// 来自握手帧（keepalive 要等 15s，足以把「卡在缓冲里」区分出来）。
	streamURL := server.URL + "/api/runtime/sessions/session-runtime-stream-tcp/runtime/stream?after=3&live=1&poll_ms=500"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	require.NoError(t, err)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	type readOutcome struct {
		text string
		err  error
	}
	outcomes := make(chan readOutcome, 1)
	go func() {
		buf := make([]byte, 128)
		var seen strings.Builder
		for !strings.Contains(seen.String(), ": open") {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
			}
			if readErr != nil {
				outcomes <- readOutcome{text: seen.String(), err: readErr}
				return
			}
		}
		outcomes <- readOutcome{text: seen.String()}
	}()

	select {
	case outcome := <-outcomes:
		require.NoError(t, outcome.err)
		assert.Contains(t, outcome.text, ": open",
			"握手注释帧必须立刻出站：只做 bufio.Flush 时它会滞留在 net/http 的 2048B 内部缓冲里")
	case <-time.After(3 * time.Second):
		t.Fatal("建连 3s 内没有收到任何字节：SSE flush 卡在 net/http 内部缓冲（回归）")
	}
}

// flakyEventStore 让前 N 次 ListEvents 失败，用于验证存储层瞬时错误不会终止 SSE 流。
type flakyEventStore struct {
	*chat.InMemoryRuntimeStore
	mu        sync.Mutex
	failures  int
	listCalls int
}

func (s *flakyEventStore) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, error) {
	s.mu.Lock()
	s.listCalls++
	fail := s.listCalls <= s.failures
	s.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("database is locked (simulated transient failure)")
	}
	return s.InMemoryRuntimeStore.ListEvents(ctx, sessionID, afterSeq, limit)
}

// 回归：存储层瞬时错误不得终止 SSE 流。旧实现一旦 ListEvents 报错就写一个 error 帧
// 并 return —— 客户端立刻重连，重连本身又加剧 SQLite 争用（写事务持有唯一连接），
// 形成「超时 → 断开 → 重连 → 更慢」的正反馈；而 error 帧还会被前端当作致命错误。
// 现在应改为注释帧留痕 + 指数退避重试，连接保持存活并继续投递后续事件。
func TestStreamSessionRuntimeEventsSurvivesTransientStoreErrors(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	const sessionID = "session-runtime-stream-retry"
	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"content": "delivered after retries"},
	})
	require.NoError(t, err)

	flaky := &flakyEventStore{InMemoryRuntimeStore: runtimeStore, failures: 2}
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = flaky

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime/stream", handler.StreamSessionRuntimeEvents).Methods(http.MethodGet)
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamURL := server.URL + "/api/runtime/sessions/" + sessionID + "/runtime/stream?after=0&poll_ms=500"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	type readOutcome struct {
		text string
		err  error
	}
	outcomes := make(chan readOutcome, 1)
	go func() {
		buf := make([]byte, 512)
		var seen strings.Builder
		for !strings.Contains(seen.String(), "delivered after retries") {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
			}
			if readErr != nil {
				outcomes <- readOutcome{text: seen.String(), err: readErr}
				return
			}
		}
		outcomes <- readOutcome{text: seen.String()}
	}()

	select {
	case outcome := <-outcomes:
		require.NoError(t, outcome.err)
		assert.Contains(t, outcome.text, ": stream-retry", "瞬时错误应留下注释帧而不是终止流")
		assert.Contains(t, outcome.text, "delivered after retries", "重试成功后必须继续投递事件")
	case <-time.After(8 * time.Second):
		t.Fatal("瞬时 ListEvents 错误后未能恢复投递：流被过早终止")
	}
}

// P2-1A 契约：runtime 快照端点必须区分「会话不存在」与「会话存在但从未进入
// durable session actor」。只经无状态 /api/agent/chat 的 web 会话属于后者：
// 它的快照是空（state: null），不是资源缺失。旧实现把两种终态都写成 404，
// 调用方无法区分，只能把 404 当成常规空态吞掉——浏览器控制台因此对每个
// web 会话都留下一条误导性 404（shared.ts:247）。
func TestGetSessionRuntimeStateEmptySnapshotForKnownSessionWithoutActorState(t *testing.T) {
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	session, err := sessionManager.Create(ctx, "runtime-state-user")
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	// 钉住运行时存储：端点不得懒加载真实数据库。
	handler.sessionRuntimeStore = chat.NewInMemoryRuntimeStore(64)

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime", handler.GetSessionRuntimeState).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var payload map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	assert.Equal(t, session.ID, payload["session_id"], "空快照必须回显会话身份")
	state, ok := payload["state"]
	assert.True(t, ok, "空快照必须显式带 state 键（null），不得省略")
	assert.Nil(t, state, "无 actor 状态的会话不得伪造 runtime state")
}

// 未知 / 已删除会话是客户端语义错误：404 + SESSION_NOT_FOUND，与会话读取
// 端点（/turns、/backtrack/audit、/history）保持同一约定。
func TestGetSessionRuntimeStateNotFoundForUnknownSession(t *testing.T) {
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionRuntimeStore = chat.NewInMemoryRuntimeStore(64)

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime", handler.GetSessionRuntimeState).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session_20260915073017_missing/runtime", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "SESSION_NOT_FOUND", "body=%s", rec.Body.String())
}

// 有 actor 状态时快照内容原样返回（本次修复只改「空态」分支）。
func TestGetSessionRuntimeStateReturnsActorStateWhenPresent(t *testing.T) {
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	session, err := sessionManager.Create(ctx, "runtime-state-user")
	require.NoError(t, err)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID:    session.ID,
		Status:       chat.SessionIdle,
		HeadOffset:   17,
		ActiveJobIDs: []string{"job-1"},
	}))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionRuntimeStore = runtimeStore

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime", handler.GetSessionRuntimeState).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var payload struct {
		State map[string]interface{} `json:"state"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	require.NotNil(t, payload.State)
	assert.Equal(t, session.ID, payload.State["session_id"])
	assert.Equal(t, string(chat.SessionIdle), payload.State["status"])
	assert.Equal(t, float64(17), payload.State["head_offset"])
	assert.Equal(t, []interface{}{"job-1"}, payload.State["active_job_ids"])
}

// 后台会话轮询可用 view=light 拉取轻量快照：state 经 CloneForInspection 投影，
// 大字段（stable_tool_surface / frozen_turn_tools 及其 set 标记）被丢弃，
// 而 status / pending_approval / head_offset / active_job_ids 与顶层 active_turn
// 必须与完整视图一致；trim + 大小写不敏感，未知取值回退完整视图且不报错。
func TestGetSessionRuntimeStateLightViewProjectsStateForBackgroundPolling(t *testing.T) {
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	session, err := sessionManager.Create(ctx, "runtime-light-user")
	require.NoError(t, err)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: session.ID,
		Status:    chat.SessionWaitingApproval,
		StableToolSurface: []types.ToolDefinition{{
			Name:        "large_tool",
			Description: strings.Repeat("d", 64<<10),
		}},
		StableToolSurfaceSet: true,
		FrozenTurnTools:      []types.ToolDefinition{{Name: "large_tool"}},
		FrozenTurnToolsSet:   true,
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-light",
			SessionID: session.ID,
			ToolName:  "large_tool",
			Reason:    "needs approval",
		},
		HeadOffset:   42,
		ActiveJobIDs: []string{"job-light"},
	}))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionRuntimeStore = runtimeStore

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime", handler.GetSessionRuntimeState).Methods(http.MethodGet)

	// 登记在途回合，让 active_turn 在两种视图下都是非平凡值。
	const turnID = "turn_runtime_light_view"
	release := handler.getActiveTurnRegistry().begin(session.ID, turnID, agentChatActiveTurnSource, true)
	defer release()

	fetch := func(query string) (map[string]interface{}, []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime"+query, nil)
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "query=%q body=%s", query, rec.Body.String())
		body := rec.Body.Bytes()
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &payload))
		return payload, body
	}

	full, fullBody := fetch("")
	light, lightBody := fetch("?view=light")

	fullState, ok := full["state"].(map[string]interface{})
	require.True(t, ok, "默认视图必须返回完整 state，实际 %v", full["state"])
	assert.Contains(t, fullState, "stable_tool_surface", "默认视图必须带大字段")
	assert.Equal(t, true, fullState["stable_tool_surface_set"])
	assert.Contains(t, fullState, "frozen_turn_tools")
	assert.Equal(t, true, fullState["frozen_turn_tools_set"])

	lightState, ok := light["state"].(map[string]interface{})
	require.True(t, ok, "light 视图仍必须返回 state，实际 %v", light["state"])
	assert.NotContains(t, lightState, "stable_tool_surface", "light 视图不得序列化大工具面")
	assert.NotContains(t, lightState, "stable_tool_surface_set")
	assert.NotContains(t, lightState, "frozen_turn_tools")
	assert.NotContains(t, lightState, "frozen_turn_tools_set")
	for _, key := range []string{"session_id", "status", "pending_approval", "head_offset", "active_job_ids"} {
		assert.Equal(t, fullState[key], lightState[key], "light 视图必须保留 state.%s", key)
	}
	assert.Equal(t, fullState["status"], lightState["status"])
	assert.Equal(t, fullState["pending_approval"], lightState["pending_approval"])

	activeTurn, ok := light["active_turn"].(map[string]interface{})
	require.True(t, ok, "active_turn 应为对象，实际 %v", light["active_turn"])
	assert.Equal(t, turnID, activeTurn["turn_id"])
	assert.Equal(t, full["active_turn"], light["active_turn"], "顶层 active_turn 不因 light 视图改变")

	assert.Less(t, len(lightBody), len(fullBody)/2, "light 响应体量必须显著小于完整视图")
	t.Logf("full response=%d bytes, light response=%d bytes", len(fullBody), len(lightBody))

	// trim + 大小写不敏感： " LIGHT " 等价于 light。
	trimmed, trimmedBody := fetch("?view=%20LIGHT%20")
	trimmedState, ok := trimmed["state"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, trimmedState, "stable_tool_surface", "view 值必须 trim 且大小写不敏感")
	assert.Equal(t, len(trimmedBody), len(lightBody))

	// 未知取值保持现状：回退完整视图，不报错。
	unknown, _ := fetch("?view=banana")
	unknownState, ok := unknown["state"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, unknownState, "stable_tool_surface", "未知 view 取值必须回退完整视图")
	assert.Equal(t, fullState["status"], unknownState["status"])
}
