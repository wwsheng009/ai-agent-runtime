package skillsapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	skillshandler "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/embedding"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testMCPManager struct{}

func (m *testMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
}

func (m *testMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("tool call not implemented")
}

func (m *testMCPManager) ListTools() []runtimeskill.ToolInfo {
	return nil
}

func (m *testMCPManager) ListMCPs() []*mcpconfig.MCPStatus {
	return []*mcpconfig.MCPStatus{{
		Name:          "test-mcp",
		Type:          "stdio",
		TrustLevel:    mcpconfig.MCPTrustLevelLocal,
		ExecutionMode: "local_mcp",
		Enabled:       true,
		Connected:     true,
		ToolCount:     0,
	}}
}

type testProvider struct {
	name         string
	content      string
	streamChunks []llm.StreamChunk
	healthErr    error
}

func (p *testProvider) Name() string { return p.name }

func (p *testProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	return &llm.LLMResponse{
		Content: p.content,
		Model:   p.name,
		Usage: &types.TokenUsage{
			PromptTokens:     5,
			CompletionTokens: 7,
			TotalTokens:      12,
		},
	}, nil
}

func (p *testProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, len(p.streamChunks)+1)
	go func() {
		defer close(ch)
		if len(p.streamChunks) == 0 {
			ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: p.content, Done: false}
			ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
			return
		}
		for _, chunk := range p.streamChunks {
			select {
			case <-ctx.Done():
				return
			case ch <- chunk:
			}
		}
	}()
	return ch, nil
}

func (p *testProvider) CountTokens(text string) int {
	return len(text)
}

func (p *testProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		SupportsStreaming: true,
		SupportsTools:     true,
		SupportsJSONMode:  true,
		MaxContextTokens:  16000,
		MaxOutputTokens:   2048,
	}
}

func (p *testProvider) CheckHealth(ctx context.Context) error { return p.healthErr }

type fakeLifecycleMCPManager struct {
	mu          sync.Mutex
	reloadCount int
	startCount  int
	statuses    []*mcpconfig.MCPStatus
}

func (m *fakeLifecycleMCPManager) LoadConfig(configPath string) error { return nil }
func (m *fakeLifecycleMCPManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startCount++
	return nil
}
func (m *fakeLifecycleMCPManager) Stop() error                        { return nil }
func (m *fakeLifecycleMCPManager) ListTools() []*mcpregistry.ToolInfo { return nil }
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadCount++
	return nil
}

func (m *fakeLifecycleMCPManager) Counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadCount, m.startCount
}

var _ mcpmanager.Manager = (*fakeLifecycleMCPManager)(nil)

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

func newTestServer(t *testing.T, remote bool, configure func(*skillshandler.Handler)) *httptest.Server {
	return newManagedTestServer(t, remote, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		if configure != nil {
			configure(handler)
		}
	})
}

func newManagedTestServer(t *testing.T, remote bool, configure func(*skillshandler.Handler, *runtimeskill.Registry, *runtimeskill.Loader, *chat.SessionManager)) *httptest.Server {
	t.Helper()

	mcpManager := &testMCPManager{}
	registry := runtimeskill.NewRegistry(mcpManager)
	loader := runtimeskill.NewLoader(mcpManager)
	require.NoError(t, registry.Register(&runtimeskill.Skill{
		Name:        "echo-skill",
		Description: "echo test skill",
		Triggers: []runtimeskill.Trigger{{
			Type:   "keyword",
			Values: []string{"echo"},
			Weight: 1,
		}},
		Handler: runtimeskill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			result := types.NewResult(true, "ECHO:"+req.Prompt)
			result.Skill = "echo-skill"
			return result, nil
		}),
	}))

	handler := skillshandler.NewHandler(registry, loader, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testProvider{
		name:    "test-model",
		content: "LLM_FALLBACK_OK",
		streamChunks: []llm.StreamChunk{
			{Type: llm.EventTypeReasoning, Content: "thinking", Done: false},
			{Type: llm.EventTypeText, Content: "STREAM_", Done: false},
			{Type: llm.EventTypeText, Content: "OK", Done: false},
			{Type: llm.EventTypeDone, Done: true},
		},
	}))
	handler.SetLLMRuntime(runtime)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	if configure != nil {
		configure(handler, registry, loader, sessionManager)
	}

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	baseHandler := http.Handler(router)
	if remote {
		baseHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Forwarded-For") == "" {
				r.Header.Set("X-Forwarded-For", "10.0.0.5")
			}
			router.ServeHTTP(w, r)
		})
	}

	server := httptest.NewServer(baseHandler)
	t.Cleanup(server.Close)
	return server
}

func boolPtr(v bool) *bool {
	return &v
}

func TestClient_CoreEndpoints(t *testing.T) {
	server := newTestServer(t, false, nil)
	client := NewClient(server.URL)
	ctx := context.Background()

	listResp, err := client.ListSkills(ctx, ListSkillsParams{})
	require.NoError(t, err)
	require.Len(t, listResp.Skills, 1)
	assert.Equal(t, "echo-skill", listResp.Skills[0].Name)

	skillResp, err := client.GetSkill(ctx, "echo-skill")
	require.NoError(t, err)
	assert.Equal(t, "echo-skill", skillResp.Name)

	searchResp, err := client.SearchSkills(ctx, SearchSkillsParams{
		Query: "echo",
		Mode:  "lexical",
	})
	require.NoError(t, err)
	require.Len(t, searchResp.Results, 1)
	assert.Equal(t, "echo-skill", searchResp.Results[0].Name)
	assert.Equal(t, "lexical", searchResp.ResolvedMode)

	execResp, err := client.ExecuteSkill(ctx, "echo-skill", ExecuteSkillRequest{
		Prompt: "hello",
		UserID: "u1",
	})
	require.NoError(t, err)
	assert.Equal(t, "echo-skill", execResp.Skill)
	assert.Equal(t, "completed", execResp.Status)
	assert.Equal(t, "ECHO:hello", execResp.Result["output"])

	chatResp, err := client.AgentChat(ctx, AgentChatRequest{
		Messages: []Message{{Role: "user", Content: "say something"}},
		UserID:   "u2",
	})
	require.NoError(t, err)
	assert.Equal(t, "api-agent", chatResp.AgentID)
	assert.Equal(t, "llm_fallback", chatResp.Source)
	assert.Equal(t, "LLM_FALLBACK_OK", chatResp.Result["output"])

	statsResp, err := client.GetStats(ctx, ListSkillsParams{})
	require.NoError(t, err)
	assert.Equal(t, 1, statsResp.TotalSkills)
	assert.False(t, statsResp.MutationPolicy.ReadOnly)
	assert.False(t, statsResp.UsagePolicy.QuotaEnabled)
	assert.Equal(t, "test-model", statsResp.Runtime.DefaultModel)

	search, err := statsResp.DecodeSearch()
	require.NoError(t, err)
	require.NotNil(t, search)
	assert.True(t, search.HasSearchTraffic())
	assert.GreaterOrEqual(t, search.TotalRequests, 1)

	embedding, err := statsResp.DecodeEmbedding()
	require.NoError(t, err)
	require.NotNil(t, embedding)
	assert.False(t, embedding.Enabled)
}

func TestClient_AgentChat_UsesCanonicalEndpoint(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"agent_id": "api-agent",
			"source":   "llm_fallback",
			"status":   "completed",
			"result": map[string]interface{}{
				"success": true,
				"output":  "canonical ok",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.AgentChat(context.Background(), AgentChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, agentChatEndpoint, requestedPath)
	assert.Equal(t, "canonical ok", resp.Result["output"])
}

func TestClient_AgentChatStream_UsesCanonicalEndpoint(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("event: done\ndata: {\"status\":\"completed\"}\n\n"))
			flusher.Flush()
			return
		}
		_, _ = w.Write([]byte("event: done\ndata: {\"status\":\"completed\"}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	stream, err := client.AgentChatStream(context.Background(), AgentChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	defer stream.Close()

	assert.Equal(t, agentChatEndpoint, requestedPath)
}

func TestClient_GetRuntimeStatus(t *testing.T) {
	server := newTestServer(t, true, func(handler *skillshandler.Handler) {
		handler.SetAdminToken("secret-token")
	})
	client := NewClient(server.URL, WithAdminToken("secret-token"))

	resp, err := client.GetRuntimeStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-model", resp.Runtime.DefaultModel)
	assert.Len(t, resp.Runtime.Providers, 1)
	assert.Len(t, resp.Runtime.MCPs, 1)
	assert.Equal(t, "test-mcp", resp.Runtime.MCPs[0].Name)
	assert.Equal(t, "local", resp.Runtime.MCPs[0].TrustLevel)
	assert.Equal(t, "local_mcp", resp.Runtime.MCPs[0].ExecutionMode)
	assert.True(t, resp.Runtime.MCPs[0].IsLocalMCP())
	assert.False(t, resp.Runtime.MCPs[0].IsRemoteMCP())
	assert.False(t, resp.Runtime.MCPs[0].IsTrustedRemote())
	summary := resp.Runtime.MCPSummary()
	assert.Equal(t, []string{"test-mcp"}, summary.Names)
	assert.Equal(t, 1, summary.LocalCount)
	assert.Equal(t, 0, summary.RemoteCount)
}

func TestExecuteSkillResponse_DecodeResult(t *testing.T) {
	resp := &ExecuteSkillResponse{
		Skill:  "permissioned-echo",
		Status: "failed",
		Result: map[string]interface{}{
			"success":    false,
			"output":     "",
			"skillName":  "permissioned-echo",
			"error":      "permission denied",
			"error_code": "AGENT_PERMISSION",
			"error_context": map[string]interface{}{
				"policy": "sandbox",
			},
			"observations": []map[string]interface{}{
				{
					"step":    "run_command",
					"tool":    "bash",
					"success": false,
					"error":   "sandbox denied",
					"metrics": map[string]interface{}{
						"mcp_name":        "remote-governed",
						"mcp_trust_level": "trusted_remote",
						"execution_mode":  "remote_mcp",
					},
				},
			},
		},
	}

	decoded, err := resp.DecodeResult()
	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.False(t, decoded.Success)
	assert.Equal(t, "AGENT_PERMISSION", decoded.ErrorCode)
	assert.Equal(t, "sandbox", decoded.ErrorContext["policy"])
	require.Len(t, decoded.Observations, 1)
	assert.Equal(t, "remote-governed", decoded.Observations[0].Metrics["mcp_name"])
	assert.Equal(t, "trusted_remote", decoded.Observations[0].Metrics["mcp_trust_level"])
	assert.Equal(t, "remote_mcp", decoded.Observations[0].Metrics["execution_mode"])
	governance := decoded.Observations[0].Governance()
	assert.True(t, governance.Present())
	assert.True(t, governance.IsRemoteMCP())
	assert.True(t, governance.IsTrustedRemote())
	assert.False(t, governance.IsLocalMCP())

	summary := decoded.GovernanceSummary()
	assert.True(t, summary.HasMCP())
	assert.True(t, summary.UsesRemoteMCP())
	assert.True(t, summary.UsesTrustedRemoteMCP())
	assert.False(t, summary.UsesUntrustedRemoteMCP())
	assert.Equal(t, []string{"remote-governed"}, summary.MCPNames)
	assert.Equal(t, 1, summary.RemoteMCPCount)
	assert.Equal(t, 1, summary.TrustedRemoteCount)

	decodedAgain, err := resp.DecodeResult()
	require.NoError(t, err)
	assert.Same(t, decoded, decodedAgain)
}

func TestAgentChatResponse_DecodeResult(t *testing.T) {
	resp := &AgentChatResponse{
		SessionID: "session_123",
		AgentID:   "api-agent",
		Source:    "agent_route",
		Status:    "completed",
		Result: map[string]interface{}{
			"kind":    "agent",
			"source":  "agent_route",
			"success": true,
			"output":  "workflow ok",
			"skill":   "governed_workflow",
			"steps":   1,
			"state": map[string]interface{}{
				"currentStep": 1,
				"running":     false,
				"errors":      []string{"warn: review pending"},
				"context": map[string]interface{}{
					"workspace_path": "workspace",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     11,
				"completion_tokens": 7,
				"total_tokens":      18,
			},
			"metadata": map[string]interface{}{
				"finish_reason": "stop",
				"cached":        true,
				"attempt":       2,
				"provider": map[string]interface{}{
					"name": "test-model",
				},
			},
			"duration": map[string]interface{}{
				"start": "2026-03-09T09:20:25Z",
				"end":   "2026-03-09T09:20:27Z",
			},
			"observations": []map[string]interface{}{
				{
					"step":    "step_1",
					"tool":    "tool_governed",
					"success": true,
					"metrics": map[string]interface{}{
						"mcp_name":        "remote-governed",
						"mcp_trust_level": "trusted_remote",
						"execution_mode":  "remote_mcp",
					},
				},
			},
			"orchestration": map[string]interface{}{
				"source":          "agent_route",
				"route_attempted": true,
				"route_matched":   true,
				"skill":           "governed_workflow",
				"route_candidates": []map[string]interface{}{
					{
						"skill":            "governed_workflow",
						"score":            1.2,
						"matched_by":       "keyword:governed",
						"details":          "keyword match",
						"chosen":           true,
						"selection_reason": "selected",
					},
				},
				"observation_summary": map[string]interface{}{
					"count":               1,
					"successful":          1,
					"failed":              0,
					"tools":               []string{"tool_governed"},
					"failed_tools":        []string{},
					"failed_details":      []map[string]interface{}{},
					"step_durations_ms":   map[string]interface{}{"step_1": 12},
					"total_duration_ms":   12,
					"max_duration_ms":     12,
					"average_duration_ms": 12,
				},
			},
			"planning": map[string]interface{}{
				"mode":                          "planner_preferred",
				"attempted":                     true,
				"planning_source":               "workflow",
				"subagent_execution_requested":  true,
				"subagent_execution_eligible":   true,
				"subagent_execution_attempted":  true,
				"subagent_result_count":         2,
				"subagent_patch_count":          1,
				"subagent_applied_patch_count":  1,
				"subagent_verified_patch_count": 1,
				"step_count":                    1,
				"subagent_task_count":           2,
				"goal":                          "finish workflow",
				"steps": []map[string]interface{}{
					{
						"id":          "plan_1",
						"description": "Run governed workflow",
						"tool":        "tool_governed",
						"depends_on":  []string{},
						"priority":    1,
					},
				},
				"subagent_tasks": []map[string]interface{}{
					{
						"id":              "step_write",
						"role":            "writer",
						"goal":            "Write the implementation",
						"tools_whitelist": []string{"write_file"},
						"depends_on":      []string{},
						"read_only":       false,
					},
					{
						"id":              "step_verify",
						"role":            "verifier",
						"goal":            "Verify the implementation",
						"tools_whitelist": []string{"run_tests"},
						"depends_on":      []string{"step_write"},
						"read_only":       true,
					},
				},
			},
			"subagent_summary": map[string]interface{}{
				"batches":             1,
				"count":               2,
				"successful":          2,
				"failed":              0,
				"roles":               []string{"verifier", "writer"},
				"patch_count":         1,
				"applied_patch_count": 1,
				"patch_paths":         []string{"workspace/out.txt"},
			},
			"subagent_results": []map[string]interface{}{
				{
					"id":      "writer_1",
					"role":    "writer",
					"success": true,
					"summary": "Writer completed changes.",
					"patches": []map[string]interface{}{
						{
							"path":                "workspace/out.txt",
							"summary":             "created file",
							"diff":                "--- /dev/null\n+++ b/workspace/out.txt\n@@ -0,0 +1 @@\n+hello\n",
							"apply_status":        "applied",
							"applied_by":          []string{"writer_1"},
							"artifact_refs":       []string{"art-1"},
							"verification_status": "verified",
							"verified_by":         []string{"verifier_1"},
						},
					},
				},
				{
					"id":       "verifier_1",
					"role":     "verifier",
					"success":  true,
					"summary":  "Verifier passed.",
					"findings": []string{"tests passed"},
				},
			},
		},
	}

	decoded, err := resp.DecodeResult()
	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.Equal(t, "agent", decoded.Kind)
	assert.Equal(t, "agent_route", decoded.Source)
	assert.True(t, decoded.Success)
	assert.Equal(t, "governed_workflow", decoded.Skill)
	require.Len(t, decoded.Observations, 1)
	assert.Equal(t, "trusted_remote", decoded.Observations[0].Metrics["mcp_trust_level"])
	assert.Equal(t, "remote_mcp", decoded.Observations[0].Metrics["execution_mode"])
	assert.Equal(t, true, decoded.Orchestration["route_attempted"])
	require.Len(t, decoded.SubagentResults, 2)
	assert.Equal(t, "writer", decoded.SubagentResults[0].Role)
	require.Len(t, decoded.SubagentResults[0].Patches, 1)
	assert.Equal(t, "workspace/out.txt", decoded.SubagentResults[0].Patches[0].Path)
	assert.Equal(t, "applied", decoded.SubagentResults[0].Patches[0].ApplyStatus)
	assert.Equal(t, []string{"writer_1"}, decoded.SubagentResults[0].Patches[0].AppliedBy)
	assert.Equal(t, "verified", decoded.SubagentResults[0].Patches[0].VerificationStatus)
	require.NotNil(t, decoded.SubagentSummary)

	summary := decoded.GovernanceSummary()
	assert.True(t, summary.HasMCP())
	assert.True(t, summary.UsesRemoteMCP())
	assert.True(t, summary.UsesTrustedRemoteMCP())
	assert.Equal(t, []string{"remote-governed"}, summary.MCPNames)

	orchestration, err := decoded.DecodeOrchestration()
	require.NoError(t, err)
	require.NotNil(t, orchestration)
	assert.Equal(t, "agent_route", orchestration.Source)
	assert.Equal(t, "governed_workflow", orchestration.Skill)
	assert.Equal(t, true, orchestration.RouteAttempted)
	require.Len(t, orchestration.RouteCandidates, 1)
	assert.Equal(t, "governed_workflow", orchestration.RouteCandidates[0].Skill)
	require.NotNil(t, orchestration.SelectedRoute())
	assert.Equal(t, "governed_workflow", orchestration.SelectedRoute().Skill)
	require.NotNil(t, orchestration.ObservationSummary)
	assert.Equal(t, int64(12), orchestration.ObservationSummary.TotalDurationMS)
	assert.Equal(t, int64(12), orchestration.ObservationSummary.StepDurationsMS["step_1"])

	planning, err := decoded.DecodePlanning()
	require.NoError(t, err)
	require.NotNil(t, planning)
	assert.Equal(t, "planner_preferred", planning.Mode)
	assert.True(t, planning.Attempted)
	assert.Equal(t, "workflow", planning.PlanningSource)
	assert.True(t, planning.SubagentExecutionRequested)
	assert.True(t, planning.SubagentExecutionEligible)
	assert.True(t, planning.SubagentExecutionAttempted)
	assert.Equal(t, 2, planning.SubagentResultCount)
	assert.Equal(t, 1, planning.SubagentPatchCount)
	assert.Equal(t, 1, planning.SubagentAppliedPatchCount)
	assert.Equal(t, 1, planning.SubagentVerifiedPatchCount)
	assert.Equal(t, 2, planning.SubagentTaskCount)
	require.Len(t, planning.Steps, 1)
	assert.Equal(t, "plan_1", planning.Steps[0].ID)
	assert.Equal(t, "tool_governed", planning.Steps[0].Tool)
	require.Len(t, planning.SubagentTasks, 2)
	assert.Equal(t, "writer", planning.SubagentTasks[0].Role)
	assert.Equal(t, "verifier", planning.SubagentTasks[1].Role)
	assert.False(t, planning.HasError())

	subagentSummary, err := decoded.DecodeSubagentSummary()
	require.NoError(t, err)
	require.NotNil(t, subagentSummary)
	assert.Equal(t, 1, subagentSummary.Batches)
	assert.Equal(t, 2, subagentSummary.Count)
	assert.Equal(t, 1, subagentSummary.PatchCount)
	assert.Equal(t, 1, subagentSummary.AppliedPatchCount)
	assert.Equal(t, []string{"workspace/out.txt"}, subagentSummary.PatchPaths)

	usage, err := decoded.DecodeUsage()
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 7, usage.CompletionTokens)
	assert.Equal(t, 18, usage.TotalTokens)

	duration, err := decoded.DecodeDuration()
	require.NoError(t, err)
	require.NotNil(t, duration)
	assert.Equal(t, 2*time.Second, duration.Elapsed())

	state, err := decoded.DecodeState()
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 1, state.CurrentStep)
	assert.False(t, state.Running)
	assert.True(t, state.HasErrors())
	contextMap, ok := state.ContextMap()
	require.True(t, ok)
	assert.Equal(t, "workspace", contextMap["workspace_path"])

	assert.Equal(t, "stop", decoded.MetadataString("finish_reason"))
	cached, ok := decoded.MetadataBool("cached")
	require.True(t, ok)
	assert.True(t, cached)
	attempt, ok := decoded.MetadataInt("attempt")
	require.True(t, ok)
	assert.Equal(t, 2, attempt)
	providerMeta, ok := decoded.MetadataMap("provider")
	require.True(t, ok)
	assert.Equal(t, "test-model", providerMeta["name"])
	value, ok := decoded.MetadataValue("finish_reason")
	require.True(t, ok)
	assert.Equal(t, "stop", value)

	decodedAgain, err := resp.DecodeResult()
	require.NoError(t, err)
	assert.Same(t, decoded, decodedAgain)

	orchestrationAgain, err := decoded.DecodeOrchestration()
	require.NoError(t, err)
	assert.Same(t, orchestration, orchestrationAgain)

	planningAgain, err := decoded.DecodePlanning()
	require.NoError(t, err)
	assert.Same(t, planning, planningAgain)

	subagentSummaryAgain, err := decoded.DecodeSubagentSummary()
	require.NoError(t, err)
	assert.Same(t, subagentSummary, subagentSummaryAgain)

	usageAgain, err := decoded.DecodeUsage()
	require.NoError(t, err)
	assert.Same(t, usage, usageAgain)

	durationAgain, err := decoded.DecodeDuration()
	require.NoError(t, err)
	assert.Same(t, duration, durationAgain)

	stateAgain, err := decoded.DecodeState()
	require.NoError(t, err)
	assert.Same(t, state, stateAgain)
}

func TestAgentChatResult_DecodeToolCalls(t *testing.T) {
	resp := &AgentChatResponse{
		SessionID: "session_456",
		AgentID:   "api-agent",
		Source:    "llm_fallback",
		Status:    "completed",
		Result: map[string]interface{}{
			"kind":    "llm",
			"source":  "llm_fallback",
			"success": true,
			"output":  "done",
			"tool_calls": []map[string]interface{}{
				{
					"id":        "call_1",
					"name":      "search",
					"arguments": map[string]interface{}{"query": "weather"},
				},
				{
					"id":        "call_2",
					"name":      "fetch",
					"arguments": map[string]interface{}{"url": "https://example.com"},
				},
			},
		},
	}

	decoded, err := resp.DecodeResult()
	require.NoError(t, err)
	require.NotNil(t, decoded)

	toolCalls, err := decoded.DecodeToolCalls()
	require.NoError(t, err)
	require.Len(t, toolCalls, 2)
	assert.Equal(t, "call_1", toolCalls[0].ID)
	assert.Equal(t, "search", toolCalls[0].Name)
	assert.Equal(t, "weather", toolCalls[0].Arguments["query"])
	assert.Equal(t, "fetch", toolCalls[1].Name)

	toolCallsAgain, err := decoded.DecodeToolCalls()
	require.NoError(t, err)
	require.Len(t, toolCallsAgain, 2)
	assert.Equal(t, toolCalls, toolCallsAgain)
}

func TestAPIErrorHelpers(t *testing.T) {
	apiErr := &APIError{
		StatusCode: 403,
		Message:    "forbidden",
		Code:       "AGENT_PERMISSION",
		Context: map[string]interface{}{
			"policy": "sandbox",
		},
	}

	assert.True(t, apiErr.HasCode("AGENT_PERMISSION"))
	assert.True(t, apiErr.HasCode("agent_permission"))

	value, ok := apiErr.ContextValue("policy")
	require.True(t, ok)
	assert.Equal(t, "sandbox", value)

	_, ok = apiErr.ContextValue("missing")
	assert.False(t, ok)

	governance := apiErr.Governance()
	assert.True(t, governance.Present())
	assert.True(t, governance.IsSandboxPolicy())
	assert.False(t, governance.IsMCPGovernance())
}

func TestAPIErrorGovernance_ForRemoteMCP(t *testing.T) {
	apiErr := &APIError{
		StatusCode: 500,
		Message:    "mcp tool failed",
		Code:       "TOOL_EXECUTION",
		Context: map[string]interface{}{
			"governance_scope": "mcp",
			"mcp_name":         "remote-governed",
			"mcp_trust_level":  "trusted_remote",
			"execution_mode":   "remote_mcp",
		},
	}

	governance := apiErr.Governance()
	assert.True(t, governance.Present())
	assert.True(t, governance.IsMCPGovernance())
	assert.True(t, governance.IsRemoteMCP())
	assert.True(t, governance.IsTrustedRemote())
	assert.False(t, governance.IsUntrustedRemote())
	assert.Equal(t, "remote-governed", governance.MCPName)
}

func TestStreamDonePayload_DecodeResult_CachesDecodedValue(t *testing.T) {
	done := &StreamDonePayload{
		Status: "completed",
		Result: map[string]interface{}{
			"kind":    "llm",
			"source":  "llm_stream",
			"success": true,
			"output":  "ok",
		},
	}

	decoded, err := done.DecodeResult()
	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.Equal(t, "llm_stream", decoded.Source)

	decodedAgain, err := done.DecodeResult()
	require.NoError(t, err)
	assert.Same(t, decoded, decodedAgain)
}

func TestExecuteSkillResponse_DecodeResult_IsConcurrentSafe(t *testing.T) {
	resp := &ExecuteSkillResponse{
		Result: map[string]interface{}{
			"success": true,
			"output":  "ok",
		},
	}

	var wg sync.WaitGroup
	results := make([]*ExecuteSkillResult, 8)
	errors := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errors[idx] = resp.DecodeResult()
		}(i)
	}
	wg.Wait()

	for i := range results {
		require.NoError(t, errors[i])
		require.NotNil(t, results[i])
		assert.Same(t, results[0], results[i])
	}
}

func TestRuntimeStatus_MCPSummaryAndFilters(t *testing.T) {
	status := RuntimeStatus{
		MCPs: []RuntimeMCPStatus{
			{Name: "local-a", TrustLevel: "local", ExecutionMode: "local_mcp", Connected: true},
			{Name: "remote-b", TrustLevel: "trusted_remote", ExecutionMode: "remote_mcp", Connected: true},
			{Name: "remote-c", TrustLevel: "untrusted_remote", ExecutionMode: "remote_mcp", Connected: false},
		},
	}

	summary := status.MCPSummary()
	assert.Equal(t, []string{"local-a", "remote-b", "remote-c"}, summary.Names)
	assert.Equal(t, 1, summary.LocalCount)
	assert.Equal(t, 2, summary.RemoteCount)
	assert.Equal(t, 1, summary.TrustedRemoteCount)
	assert.Equal(t, 1, summary.UntrustedRemoteCount)
	assert.Equal(t, 2, summary.ConnectedCount)
	assert.Equal(t, 1, summary.DisconnectedCount)

	require.Len(t, status.LocalMCPs(), 1)
	require.Len(t, status.RemoteMCPs(), 2)
	assert.Equal(t, "local-a", status.LocalMCPs()[0].Name)
	assert.Equal(t, "remote-b", status.RemoteMCPs()[0].Name)
}

func TestSearchStatsAndEmbeddingDecodeHelpers(t *testing.T) {
	searchResp := &SearchStatsResponse{
		Search: map[string]interface{}{
			"total_requests":      3,
			"total_results":       9,
			"average_results":     3.0,
			"embedding_requests":  2,
			"last_used_embedding": true,
			"reindex_count":       1,
		},
		Embedding: map[string]interface{}{
			"enabled": true,
			"stats": map[string]interface{}{
				"indexSize": 5,
				"threshold": 0.5,
				"topK":      5,
			},
		},
	}

	search, err := searchResp.DecodeSearch()
	require.NoError(t, err)
	require.NotNil(t, search)
	assert.True(t, search.HasSearchTraffic())
	assert.True(t, search.HasReindexHistory())

	embedding, err := searchResp.DecodeEmbedding()
	require.NoError(t, err)
	require.NotNil(t, embedding)
	assert.True(t, embedding.Indexed())

	reindexResp := &ReindexSearchIndexResponse{
		Reindexed: true,
		Search:    searchResp.Search,
		Embedding: searchResp.Embedding,
	}

	reindexSearch, err := reindexResp.DecodeSearch()
	require.NoError(t, err)
	require.NotNil(t, reindexSearch)
	assert.Equal(t, 3, reindexSearch.TotalRequests)

	reindexEmbedding, err := reindexResp.DecodeEmbedding()
	require.NoError(t, err)
	require.NotNil(t, reindexEmbedding)
	assert.Equal(t, 5, reindexEmbedding.Stats.IndexSize)
}

func TestClient_GetRuntimeHealth(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "good-model", MaxRetries: 0})
		require.NoError(t, runtime.RegisterProvider("good-model", &testProvider{name: "good-model", content: "ok"}))
		require.NoError(t, runtime.RegisterProvider("bad-model", &testProvider{name: "bad-model", content: "bad", healthErr: fmt.Errorf("provider offline")}))
		handler.SetLLMRuntime(runtime)
	})
	client := NewClient(server.URL, WithAdminToken("secret-token"))

	resp, err := client.GetRuntimeHealth(context.Background())
	require.NoError(t, err)
	assert.False(t, resp.Health.Healthy)
	assert.Equal(t, 1, resp.Health.UnhealthyProviders)
	assert.NotEmpty(t, resp.Health.Issues)
}

func TestClient_ReloadRuntimeMCPs(t *testing.T) {
	lifecycleManager := &fakeLifecycleMCPManager{
		statuses: []*mcpconfig.MCPStatus{{
			Name:      "test-mcp",
			Type:      "stdio",
			Enabled:   true,
			Connected: true,
			ToolCount: 0,
		}},
	}

	mcpAdapter := runtimeskill.NewMCPAdapter(lifecycleManager)
	registry := runtimeskill.NewRegistry(mcpAdapter)
	loader := runtimeskill.NewLoader(mcpAdapter)
	handler := skillshandler.NewHandler(registry, loader, mcpAdapter)
	handler.SetAdminToken("secret-token")

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	resp, err := client.ReloadRuntimeMCPs(context.Background())
	require.NoError(t, err)
	assert.True(t, resp.Reloaded)
	assert.Equal(t, 1, resp.Runtime.MCPCount)
	assert.True(t, resp.Health.Healthy)
}

func TestClient_ValidateRuntime(t *testing.T) {
	server := newTestServer(t, true, func(handler *skillshandler.Handler) {
		handler.SetAdminToken("secret-token")
	})
	client := NewClient(server.URL, WithAdminToken("secret-token"))

	resp, err := client.ValidateRuntime(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.GreaterOrEqual(t, resp.Validation.WarningCount, 0)
	assert.GreaterOrEqual(t, resp.Validation.SkillCount, 1)
}

func TestClient_GetCapabilities(t *testing.T) {
	server := newTestServer(t, true, nil)
	client := NewClient(server.URL)

	resp, err := client.GetCapabilities(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, resp.Count, 1)
}

func TestClient_AgentChatStream(t *testing.T) {
	server := newTestServer(t, false, nil)
	client := NewClient(server.URL)

	stream, err := client.AgentChatStream(context.Background(), AgentChatRequest{
		Messages: []Message{{Role: "user", Content: "stream this"}},
		UserID:   "stream-user",
	})
	require.NoError(t, err)
	defer stream.Close()

	events := make([]string, 0)
	metaDecoded := false
	resultDecoded := false
	doneDecoded := false
	for {
		event, nextErr := stream.Next()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		events = append(events, event.Event)
		envelope, err := event.DecodeEnvelopeMeta()
		require.NoError(t, err)
		assert.Equal(t, event.Event, envelope.Name)

		switch event.Event {
		case "meta":
			meta, err := event.DecodeMetaPayload()
			require.NoError(t, err)
			assert.Equal(t, "llm_stream", meta.Source)
			assert.Equal(t, "llm", meta.Kind)
			metaDecoded = true
		case "result":
			result, err := event.DecodeResultPayload()
			require.NoError(t, err)
			assert.Equal(t, "llm", result.Kind)
			assert.Equal(t, "llm_stream", result.Source)
			assert.True(t, result.Success)
			resultDecoded = true
		case "done":
			done, err := event.DecodeDonePayload()
			require.NoError(t, err)
			assert.Equal(t, "completed", done.Status)
			decodedResult, err := done.DecodeResult()
			require.NoError(t, err)
			require.NotNil(t, decodedResult)
			assert.Equal(t, "llm_stream", decodedResult.Source)
			doneDecoded = true
		}
		if event.Event == "done" {
			break
		}
	}

	assert.Contains(t, events, "meta")
	assert.Contains(t, events, "reasoning")
	assert.Contains(t, events, "chunk")
	assert.Contains(t, events, "result")
	assert.Contains(t, events, "done")
	assert.True(t, metaDecoded)
	assert.True(t, resultDecoded)
	assert.True(t, doneDecoded)
}

func TestClient_AgentChat_WithPlanningAndWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(`package demo
func SearchDocs() {}
`), 0o644))

	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, registry *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		require.NoError(t, registry.Register(&runtimeskill.Skill{
			Name:        "planned-skill",
			Description: "planned workflow skill",
			Triggers: []runtimeskill.Trigger{{
				Type:   "keyword",
				Values: []string{"plan", "workflow"},
				Weight: 1,
			}},
			Workflow: &runtimeskill.Workflow{Steps: []runtimeskill.WorkflowStep{{
				ID:   "step_1",
				Name: "echo",
				Tool: "echo_tool",
			}}},
		}))
		_ = handler
	})

	client := NewClient(server.URL)
	resp, err := client.AgentChat(context.Background(), AgentChatRequest{
		Messages:      []Message{{Role: "user", Content: "plan workflow for me"}},
		EnableRouting: true,
		PlanningMode:  "planner_preferred",
		WorkspacePath: tmpDir,
	})
	require.NoError(t, err)
	planning, ok := resp.Result["planning"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "planner_preferred", planning["mode"])
	assert.Equal(t, "workflow", planning["planning_source"])
	assert.Equal(t, float64(1), planning["step_count"])
	assert.Equal(t, float64(1), planning["subagent_task_count"])
}

func TestClient_AgentChat_WithWorkspaceAndPlanning(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(`package demo
func SearchDocs() {}
`), 0o644))

	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, registry *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		require.NoError(t, registry.Register(&runtimeskill.Skill{
			Name:        "planned-skill",
			Description: "planned workflow skill",
			Triggers: []runtimeskill.Trigger{{
				Type:   "keyword",
				Values: []string{"plan", "workflow"},
				Weight: 1,
			}},
			Workflow: &runtimeskill.Workflow{Steps: []runtimeskill.WorkflowStep{{
				ID:   "step_1",
				Name: "echo",
				Tool: "echo_tool",
			}}},
		}))
		_ = handler
	})

	client := NewClient(server.URL)
	resp, err := client.AgentChat(context.Background(), AgentChatRequest{
		Messages:      []Message{{Role: "user", Content: "plan workflow for me"}},
		EnableRouting: true,
		PlanningMode:  "planner_preferred",
		WorkspacePath: tmpDir,
	})
	require.NoError(t, err)
	planning, ok := resp.Result["planning"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "planner_preferred", planning["mode"])
	assert.Equal(t, true, planning["attempted"])
	assert.Equal(t, "workflow", planning["planning_source"])
	assert.Equal(t, float64(1), planning["step_count"])
	assert.Equal(t, float64(1), planning["subagent_task_count"])
}

func TestClient_AgentChatStream_WithPlanning(t *testing.T) {
	server := newTestServer(t, false, nil)
	client := NewClient(server.URL)

	stream, err := client.AgentChatStream(context.Background(), AgentChatRequest{
		Messages:     []Message{{Role: "user", Content: "no route but plan this"}},
		UserID:       "stream-user",
		PlanningMode: "planner_preferred",
		Stream:       true,
	})
	require.NoError(t, err)
	defer stream.Close()

	events := make([]string, 0)
	planningSeen := false
	metaDecoded := false
	resultDecoded := false
	doneDecoded := false
	for {
		event, nextErr := stream.Next()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		events = append(events, event.Event)
		switch event.Event {
		case "meta":
			meta, err := event.DecodeMetaPayload()
			require.NoError(t, err)
			assert.Equal(t, "llm_stream", meta.Source)
			orchestration, err := meta.DecodeOrchestration()
			require.NoError(t, err)
			require.NotNil(t, orchestration)
			assert.Equal(t, "llm_stream", orchestration.Source)
			assert.True(t, orchestration.PlanningAttempted)
			planning, err := meta.DecodePlanning()
			require.NoError(t, err)
			require.NotNil(t, planning)
			assert.Equal(t, "planner_preferred", planning.Mode)
			assert.True(t, planning.Attempted)
			metaDecoded = true
		case "planning":
			planningPayload, err := event.DecodePlanningPayload()
			require.NoError(t, err)
			assert.Equal(t, "planner_preferred", planningPayload.Mode)
			assert.True(t, planningPayload.Attempted)
			assert.GreaterOrEqual(t, planningPayload.SubagentTaskCount, 0)
			assert.False(t, planningPayload.SubagentExecutionRequested)
			assert.False(t, planningPayload.SubagentExecutionAttempted)
			if len(planningPayload.Steps) > 0 {
				assert.NotEmpty(t, planningPayload.Steps[0].ID)
			}
			if len(planningPayload.SubagentTasks) > 0 {
				assert.NotEmpty(t, planningPayload.SubagentTasks[0].Role)
			}
			planningSeen = true
		case "result":
			result, err := event.DecodeResultPayload()
			require.NoError(t, err)
			planning, err := result.DecodePlanning()
			require.NoError(t, err)
			require.NotNil(t, planning)
			assert.Equal(t, "planner_preferred", planning.Mode)
			orchestration, err := result.DecodeOrchestration()
			require.NoError(t, err)
			require.NotNil(t, orchestration)
			assert.Equal(t, "llm_stream", orchestration.Source)
			resultDecoded = true
		case "done":
			done, err := event.DecodeDonePayload()
			require.NoError(t, err)
			assert.Equal(t, "completed", done.Status)
			decodedResult, err := done.DecodeResult()
			require.NoError(t, err)
			require.NotNil(t, decodedResult)
			planning, err := decodedResult.DecodePlanning()
			require.NoError(t, err)
			require.NotNil(t, planning)
			assert.Equal(t, "planner_preferred", planning.Mode)
			doneDecoded = true
		}
		if event.Event == "done" {
			break
		}
	}

	assert.Contains(t, events, "meta")
	assert.Contains(t, events, "planning")
	assert.Contains(t, events, "result")
	assert.Contains(t, events, "done")
	assert.True(t, planningSeen)
	assert.True(t, metaDecoded)
	assert.True(t, resultDecoded)
	assert.True(t, doneDecoded)
}

func TestClient_AgentChatStream_NextDecoded(t *testing.T) {
	server := newTestServer(t, false, nil)
	client := NewClient(server.URL)

	stream, err := client.AgentChatStream(context.Background(), AgentChatRequest{
		Messages:     []Message{{Role: "user", Content: "no route but plan this"}},
		UserID:       "stream-user",
		PlanningMode: "planner_preferred",
		Stream:       true,
	})
	require.NoError(t, err)
	defer stream.Close()

	metaSeen := false
	planningSeen := false
	reasoningSeen := false
	chunkSeen := false
	orchestrationSeen := false
	resultSeen := false
	doneSeen := false

	for {
		decoded, nextErr := stream.NextDecoded()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		require.NotNil(t, decoded)
		require.NotNil(t, decoded.Raw)
		assert.Equal(t, decoded.Event, decoded.Raw.Event)

		switch {
		case decoded.Meta != nil:
			metaSeen = true
			assert.Equal(t, "llm_stream", decoded.Meta.Source)
			orchestration, err := decoded.Meta.DecodeOrchestration()
			require.NoError(t, err)
			require.NotNil(t, orchestration)
			assert.Equal(t, "llm_stream", orchestration.Source)
			assert.True(t, orchestration.PlanningAttempted)
			planning, err := decoded.Meta.DecodePlanning()
			require.NoError(t, err)
			require.NotNil(t, planning)
			assert.Equal(t, "planner_preferred", planning.Mode)
		case decoded.Planning != nil:
			planningSeen = true
			assert.Equal(t, "planner_preferred", decoded.Planning.Mode)
			assert.True(t, decoded.Planning.Attempted)
		case decoded.Orchestration != nil:
			orchestrationSeen = true
			assert.Equal(t, "llm_stream", decoded.Orchestration.Source)
			assert.True(t, decoded.Orchestration.PlanningAttempted)
		case decoded.Result != nil:
			resultSeen = true
			assert.Equal(t, "llm", decoded.Result.Kind)
			assert.Equal(t, "llm_stream", decoded.Result.Source)
			planning, err := decoded.Result.DecodePlanning()
			require.NoError(t, err)
			require.NotNil(t, planning)
			assert.Equal(t, "planner_preferred", planning.Mode)
		case decoded.Done != nil:
			doneSeen = true
			assert.Equal(t, "completed", decoded.Done.Status)
			finalResult, err := decoded.Done.DecodeResult()
			require.NoError(t, err)
			require.NotNil(t, finalResult)
			planning, err := finalResult.DecodePlanning()
			require.NoError(t, err)
			require.NotNil(t, planning)
			assert.Equal(t, "planner_preferred", planning.Mode)
		case decoded.Chunk != nil && decoded.Event == "reasoning":
			reasoningSeen = true
			assert.Equal(t, "reasoning", decoded.Chunk.Type)
			reasoning, err := decoded.Chunk.DecodeReasoning()
			require.NoError(t, err)
			require.NotNil(t, reasoning)
			assert.Equal(t, "thinking", reasoning.Content)
			assert.Equal(t, "thinking", reasoning.Delta)
		case decoded.Chunk != nil && decoded.Event == "chunk":
			chunkSeen = true
			assert.NotEmpty(t, decoded.Chunk.Content)
			if decoded.Chunk.Type == "text" {
				text, err := decoded.Chunk.DecodeText()
				require.NoError(t, err)
				require.NotNil(t, text)
				assert.NotEmpty(t, text.Content)
				assert.Greater(t, text.TotalChars, 0)
			}
		}

		if decoded.Event == "done" {
			break
		}
	}

	assert.True(t, metaSeen)
	assert.True(t, planningSeen)
	assert.True(t, reasoningSeen)
	assert.True(t, chunkSeen)
	assert.True(t, orchestrationSeen)
	assert.True(t, resultSeen)
	assert.True(t, doneSeen)
}

func TestClient_AgentChatStream_DecodeToolChunks(t *testing.T) {
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
		require.NoError(t, runtime.RegisterProvider("test-model", &testProvider{
			name:    "test-model",
			content: "hello world",
			streamChunks: []llm.StreamChunk{
				{Type: llm.EventTypeToolStart, Content: "search", ToolCall: &types.ToolCall{ID: "tool-1", Name: "search", Args: map[string]interface{}{"query": "weather"}}, Metadata: map[string]interface{}{"phase": "start", "attempt": 1}, Done: false},
				{Type: llm.EventTypeToolCall, Delta: &types.ToolCall{ID: "tool-1", Name: "search", Args: map[string]interface{}{"query": "weather"}}, Metadata: map[string]interface{}{"phase": "call", "streaming": true}, Done: false},
				{Type: llm.EventTypeToolEnd, Content: "search complete", ToolCall: &types.ToolCall{ID: "tool-1", Name: "search", Args: map[string]interface{}{"query": "weather"}}, Metadata: map[string]interface{}{"phase": "end", "result": map[string]interface{}{"status": "ok"}}, Done: false},
				{Type: llm.EventTypeDone, Done: true},
			},
		}))
		handler.SetLLMRuntime(runtime)
	})
	client := NewClient(server.URL)

	stream, err := client.AgentChatStream(context.Background(), AgentChatRequest{
		Messages: []Message{{Role: "user", Content: "stream tools"}},
		UserID:   "stream-user",
		Stream:   true,
	})
	require.NoError(t, err)
	defer stream.Close()

	sawToolStart := false
	sawToolCall := false
	sawToolEnd := false

	for {
		decoded, nextErr := stream.NextDecoded()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)

		if decoded.Chunk == nil {
			if decoded.Event == "done" {
				break
			}
			continue
		}

		switch decoded.Event {
		case "tool_start":
			sawToolStart = true
			tool, err := decoded.Chunk.DecodeTool()
			require.NoError(t, err)
			require.NotNil(t, tool)
			assert.Equal(t, "tool_start", tool.Status)
			assert.Equal(t, "search", tool.Name)
			assert.Equal(t, "weather", tool.Args["query"])

			toolCall, err := decoded.Chunk.DecodeToolCall()
			require.NoError(t, err)
			require.NotNil(t, toolCall)
			assert.Equal(t, "tool-1", toolCall.ID)
			assert.Equal(t, "search", toolCall.Name)
			assert.Equal(t, "weather", toolCall.Arguments["query"])

			delta, err := decoded.Chunk.DecodeDelta()
			require.NoError(t, err)
			assert.Nil(t, delta)
			assert.Equal(t, "start", decoded.Chunk.MetadataString("phase"))
			attempt, ok := decoded.Chunk.MetadataInt("attempt")
			require.True(t, ok)
			assert.Equal(t, 1, attempt)
		case "tool_call":
			sawToolCall = true
			tool, err := decoded.Chunk.DecodeTool()
			require.NoError(t, err)
			require.NotNil(t, tool)
			assert.Equal(t, "tool_call", tool.Status)
			assert.Equal(t, "search", tool.Name)

			toolCall, err := decoded.Chunk.DecodeToolCall()
			require.NoError(t, err)
			assert.Nil(t, toolCall)

			delta, err := decoded.Chunk.DecodeDelta()
			require.NoError(t, err)
			require.NotNil(t, delta)
			assert.Equal(t, "tool-1", delta.ID)
			assert.Equal(t, "search", delta.Name)
			assert.Equal(t, "weather", delta.Arguments["query"])
			streaming, ok := decoded.Chunk.MetadataBool("streaming")
			require.True(t, ok)
			assert.True(t, streaming)
		case "tool_end":
			sawToolEnd = true
			tool, err := decoded.Chunk.DecodeTool()
			require.NoError(t, err)
			require.NotNil(t, tool)
			assert.Equal(t, "tool_end", tool.Status)
			assert.Equal(t, "search complete", tool.Content)

			toolCall, err := decoded.Chunk.DecodeToolCall()
			require.NoError(t, err)
			require.NotNil(t, toolCall)
			assert.Equal(t, "tool-1", toolCall.ID)
			resultMeta, ok := decoded.Chunk.MetadataMap("result")
			require.True(t, ok)
			assert.Equal(t, "ok", resultMeta["status"])
		}
	}

	assert.True(t, sawToolStart)
	assert.True(t, sawToolCall)
	assert.True(t, sawToolEnd)
}

func TestStreamEvent_DecodeTyped_StructuredEvents(t *testing.T) {
	routeEvent := &StreamEvent{
		Event: "route",
		Data:  json.RawMessage(`{"_event":{"name":"route","schema_version":"skills-agent-sse-v1","sequence":2},"source":"agent_route","skill":"echo-skill","route_attempted":true,"route_matched":true,"candidate_count":1,"route_candidates":[{"skill":"echo-skill","score":1.0,"chosen":true}]}`),
	}
	decodedRoute, err := routeEvent.DecodeTyped()
	require.NoError(t, err)
	require.NotNil(t, decodedRoute)
	require.NotNil(t, decodedRoute.Route)
	assert.Equal(t, "agent_route", decodedRoute.Route.Source)
	assert.Len(t, decodedRoute.Route.RouteCandidates, 1)
	require.NotNil(t, decodedRoute.Route.SelectedRoute())
	assert.Equal(t, "echo-skill", decodedRoute.Route.SelectedRoute().Skill)

	observationEvent := &StreamEvent{
		Event: "observation",
		Data:  json.RawMessage(`{"_event":{"name":"observation","schema_version":"skills-agent-sse-v1","sequence":3},"index":1,"step":"step_1","tool":"echo_tool","success":true,"duration_ms":12}`),
	}
	decodedObservation, err := observationEvent.DecodeTyped()
	require.NoError(t, err)
	require.NotNil(t, decodedObservation)
	require.NotNil(t, decodedObservation.Observation)
	assert.Equal(t, "step_1", decodedObservation.Observation.Step)
	assert.Equal(t, "echo_tool", decodedObservation.Observation.Tool)

	errorEvent := &StreamEvent{
		Event: "error",
		Data:  json.RawMessage(`{"_event":{"name":"error","schema_version":"skills-agent-sse-v1","sequence":4},"index":5,"message":"boom","source":"llm_stream"}`),
	}
	decodedError, err := errorEvent.DecodeTyped()
	require.NoError(t, err)
	require.NotNil(t, decodedError)
	require.NotNil(t, decodedError.Error)
	assert.Equal(t, "boom", decodedError.Error.Message)

	unknownEvent := &StreamEvent{
		Event: "custom",
		Data:  json.RawMessage(`{"_event":{"name":"custom","schema_version":"skills-agent-sse-v1","sequence":6},"ok":true}`),
	}
	decodedUnknown, err := unknownEvent.DecodeTyped()
	require.NoError(t, err)
	require.NotNil(t, decodedUnknown)
	assert.Equal(t, "custom", decodedUnknown.Event)
	assert.NotNil(t, decodedUnknown.Raw)
	assert.Nil(t, decodedUnknown.Meta)
	assert.Nil(t, decodedUnknown.Result)
}

func TestClient_AgentChatStream_Consume(t *testing.T) {
	server := newTestServer(t, false, nil)
	client := NewClient(server.URL)

	stream, err := client.AgentChatStream(context.Background(), AgentChatRequest{
		Messages:     []Message{{Role: "user", Content: "no route but plan this"}},
		UserID:       "stream-user",
		PlanningMode: "planner_preferred",
		Stream:       true,
	})
	require.NoError(t, err)

	metaSeen := false
	planningSeen := false
	orchestrationSeen := false
	resultSeen := false
	doneSeen := false
	chunkKinds := make([]string, 0)

	err = stream.Consume(StreamHandlers{
		OnMeta: func(meta *StreamMetaPayload) error {
			metaSeen = true
			assert.Equal(t, "llm_stream", meta.Source)
			return nil
		},
		OnPlanning: func(planning *StreamPlanningPayload) error {
			planningSeen = true
			assert.Equal(t, "planner_preferred", planning.Mode)
			return nil
		},
		OnOrchestration: func(orchestration *StreamOrchestrationPayload) error {
			orchestrationSeen = true
			assert.Equal(t, "llm_stream", orchestration.Source)
			return nil
		},
		OnChunk: func(chunk *StreamChunkPayload) error {
			chunkKinds = append(chunkKinds, chunk.Type)
			return nil
		},
		OnResult: func(result *StreamResultPayload) error {
			resultSeen = true
			assert.Equal(t, "llm_stream", result.Source)
			return nil
		},
		OnDone: func(done *StreamDonePayload) error {
			doneSeen = true
			assert.Equal(t, "completed", done.Status)
			finalResult, err := done.DecodeResult()
			require.NoError(t, err)
			require.NotNil(t, finalResult)
			return nil
		},
	})
	require.NoError(t, err)
	assert.True(t, metaSeen)
	assert.True(t, planningSeen)
	assert.True(t, orchestrationSeen)
	assert.True(t, resultSeen)
	assert.True(t, doneSeen)
	assert.Contains(t, chunkKinds, "reasoning")
	assert.Contains(t, chunkKinds, "text")
}

func TestStream_Consume_CallbackError(t *testing.T) {
	body := io.NopCloser(strings.NewReader("event: meta\ndata: {\"_event\":{\"name\":\"meta\"},\"source\":\"llm_stream\"}\n\n"))
	stream := &Stream{
		body:    body,
		scanner: bufio.NewScanner(body),
	}

	err := stream.Consume(StreamHandlers{
		OnMeta: func(meta *StreamMetaPayload) error {
			assert.Equal(t, "llm_stream", meta.Source)
			return errors.New("stop here")
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop here")
	assert.True(t, stream.closed)
}

func TestClient_AdminMutationUsesToken(t *testing.T) {
	server := newTestServer(t, true, func(handler *skillshandler.Handler) {
		handler.SetAdminToken("secret-token")
	})

	ctx := context.Background()
	createReq := Skill{
		Name:        "created-skill",
		Description: "created via client",
		Triggers: []Trigger{{
			Type:   "keyword",
			Values: []string{"created"},
			Weight: 1,
		}},
	}

	noTokenClient := NewClient(server.URL)
	_, err := noTokenClient.CreateSkill(ctx, createReq, CreateSkillOptions{})
	require.Error(t, err)
	apiErr, ok := err.(*APIError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)

	adminClient := NewClient(server.URL, WithAdminToken("secret-token"))
	createResp, err := adminClient.CreateSkill(ctx, createReq, CreateSkillOptions{})
	require.NoError(t, err)
	assert.Equal(t, "created-skill", createResp.Skill)

	deleteResp, err := adminClient.DeleteSkill(ctx, "created-skill", DeleteSkillOptions{})
	require.NoError(t, err)
	assert.Equal(t, "created-skill", deleteResp.Skill)
}

func TestClient_SessionEndpoints(t *testing.T) {
	server := newTestServer(t, false, nil)
	client := NewClient(server.URL)
	ctx := context.Background()

	createResp, err := client.CreateSession(ctx, CreateSessionRequest{
		UserID: "session-user",
		Title:  "demo-session",
	})
	require.NoError(t, err)
	sessionID := createResp.Session.ID
	require.NotEmpty(t, sessionID)
	assert.Equal(t, "demo-session", createResp.Session.Metadata.Title)

	_, err = client.AgentChat(ctx, AgentChatRequest{
		Messages:  []Message{{Role: "user", Content: "hello session"}},
		UserID:    "session-user",
		SessionID: sessionID,
	})
	require.NoError(t, err)

	listResp, err := client.ListSessions(ctx, "session-user")
	require.NoError(t, err)
	require.Len(t, listResp.Sessions, 1)
	assert.Equal(t, sessionID, listResp.Sessions[0].ID)

	updateResp, err := client.UpdateSession(ctx, sessionID, UpdateSessionRequest{
		Title:   strPtr("updated-session"),
		State:   strPtr("idle"),
		TagsAdd: []string{"support", "priority"},
		Context: map[string]interface{}{"ticket": "INC-42"},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated-session", updateResp.Session.Metadata.Title)
	assert.Equal(t, "idle", updateResp.Session.State)

	searchResp, err := client.SearchSessions(ctx, SearchSessionsRequest{
		UserID: "session-user",
		Tags:   []string{"support"},
		State:  "idle",
	})
	require.NoError(t, err)
	require.Len(t, searchResp.Sessions, 1)
	assert.Equal(t, sessionID, searchResp.Sessions[0].ID)

	historyResp, err := client.GetSessionHistory(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, sessionID, historyResp.SessionID)
	assert.GreaterOrEqual(t, historyResp.Count, 2)

	statsResp, err := client.GetSessionStats(ctx, "session-user")
	require.NoError(t, err)
	assert.Equal(t, "session-user", statsResp.UserID)
	assert.NotEmpty(t, statsResp.Stats)

	archiveResp, err := client.ArchiveSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, "archived", archiveResp.State)

	activateResp, err := client.ActivateSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, "active", activateResp.State)

	closeResp, err := client.CloseSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, "closed", closeResp.State)

	clearResp, err := client.ClearSessionHistory(ctx, sessionID)
	require.NoError(t, err)
	assert.True(t, clearResp.Cleared)

	secondResp, err := client.CreateSession(ctx, CreateSessionRequest{
		UserID: "session-user",
		Title:  "second-session",
	})
	require.NoError(t, err)

	batchArchiveResp, err := client.BatchArchiveSessions(ctx, []string{sessionID, secondResp.Session.ID})
	require.NoError(t, err)
	assert.Equal(t, "archived", batchArchiveResp.Action)
	assert.Len(t, batchArchiveResp.Processed, 2)

	batchDeleteResp, err := client.BatchDeleteSessions(ctx, []string{sessionID, secondResp.Session.ID})
	require.NoError(t, err)
	assert.Equal(t, "deleted", batchDeleteResp.Action)
	assert.Len(t, batchDeleteResp.Processed, 2)

	_, err = client.GetSession(ctx, sessionID)
	require.Error(t, err)
}

func TestClient_AdminAndOpsEndpoints(t *testing.T) {
	skillDir := t.TempDir()
	externalDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "semantic.yaml"), []byte(`name: semantic-skill
description: search customer orders in sap
version: 1.0.0
triggers:
  - type: embedding
    weight: 1
userPrompt: "search customer orders in sap"
`), 0o644))

	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, registry *runtimeskill.Registry, loader *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		handler.SetUsagePolicy(skillshandler.UsagePolicy{
			TrackingEnabled:    true,
			QuotaEnabled:       true,
			DefaultMaxRequests: 10,
			DefaultMaxTokens:   1000,
		})
		loader.SetSkillDirs([]string{skillDir, externalDir})
		embeddingIndex, err := embedding.NewVectorIndex(nil)
		require.NoError(t, err)
		embeddingRouter, err := runtimeskill.NewSemanticEmbeddingRouter(embeddingIndex, registry)
		require.NoError(t, err)
		require.NoError(t, embeddingRouter.IndexSkills())
		handler.SetEmbeddingRouter(embeddingRouter)
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	exportResp, err := client.ExportSkills(ctx, ListSkillsParams{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, exportResp.Count, 1)

	importResp, err := client.ImportSkills(ctx, []Skill{{
		Name:        "imported-skill",
		Description: "imported by client",
		Triggers: []Trigger{{
			Type:   "keyword",
			Values: []string{"imported"},
			Weight: 1,
		}},
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, importResp.Imported)

	importPersistResp, err := client.ImportSkillsWithOptions(ctx, []Skill{{
		Name:         "imported-persisted-skill",
		Description:  "imported by client with persistence",
		SystemPrompt: "You are imported.",
		UserPrompt:   "Return imported.",
		Triggers: []Trigger{{
			Type:   "keyword",
			Values: []string{"persisted-import"},
			Weight: 1,
		}},
	}}, ImportSkillsOptions{Persist: true, TargetDir: externalDir})
	require.NoError(t, err)
	assert.Equal(t, 1, importPersistResp.Imported)
	assert.Equal(t, 1, importPersistResp.Persisted)

	searchStatsResp, err := client.GetSearchStats(ctx)
	require.NoError(t, err)
	assert.NotNil(t, searchStatsResp.Search)
	assert.NotNil(t, searchStatsResp.Embedding)

	usageStatsResp, err := client.GetUsageStats(ctx, "")
	require.NoError(t, err)
	assert.True(t, usageStatsResp.TrackingEnabled)
	assert.True(t, usageStatsResp.Policy.QuotaEnabled)

	reindexResp, err := client.ReindexSearchIndex(ctx, true)
	require.NoError(t, err)
	assert.True(t, reindexResp.Reindexed)

	reloadResp, err := client.ReloadSkills(ctx, ReloadSkillsRequest{
		Dirs: []string{skillDir},
	})
	require.NoError(t, err)
	assert.Equal(t, "success", reloadResp.Status)
	assert.Equal(t, 1, reloadResp.TotalSkills)

	startResp, err := client.StartHotReload(ctx, HotReloadRequest{
		Dirs: []string{skillDir},
	})
	require.NoError(t, err)
	assert.True(t, startResp.Started)

	hotStatsResp, err := client.GetHotReloadStats(ctx)
	require.NoError(t, err)
	assert.NotNil(t, hotStatsResp.Stats)

	reloadHotResp, err := client.ReloadHotReload(ctx)
	require.NoError(t, err)
	assert.True(t, reloadHotResp.Reloaded)

	stopResp, err := client.StopHotReload(ctx)
	require.NoError(t, err)
	assert.True(t, stopResp.Stopped)

	_, err = client.UpdateSkill(ctx, "semantic-skill", Skill{
		Name:        "semantic-skill",
		Description: "updated description",
		Triggers: []Trigger{{
			Type:   "embedding",
			Weight: 1,
		}},
	}, UpdateSkillOptions{Persist: boolPtr(false)})
	require.NoError(t, err)

	resetUsageResp, err := client.ResetUsageStats(ctx, ResetUsageStatsRequest{})
	require.NoError(t, err)
	assert.True(t, resetUsageResp.Reset)
}

func TestClient_UsageScopeSeparatesQuota(t *testing.T) {
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetUsagePolicy(skillshandler.UsagePolicy{
			TrackingEnabled:    true,
			QuotaEnabled:       true,
			DefaultMaxRequests: 1,
		})
	})

	client := NewClient(server.URL)
	ctx := context.Background()

	_, err := client.ExecuteSkill(ctx, "echo-skill", ExecuteSkillRequest{
		Prompt:    "one",
		UserID:    "scope-user",
		TenantID:  "tenant-a",
		ProjectID: "project-a",
	})
	require.NoError(t, err)

	_, err = client.ExecuteSkill(ctx, "echo-skill", ExecuteSkillRequest{
		Prompt:    "two",
		UserID:    "scope-user",
		TenantID:  "tenant-a",
		ProjectID: "project-a",
	})
	require.Error(t, err)

	_, err = client.ExecuteSkill(ctx, "echo-skill", ExecuteSkillRequest{
		Prompt:    "three",
		UserID:    "scope-user",
		TenantID:  "tenant-a",
		ProjectID: "project-b",
	})
	require.NoError(t, err)

	usageResp, err := client.GetUsageStatsWithScope(ctx, UsageScope{
		TenantID:  "tenant-a",
		ProjectID: "project-a",
		UserID:    "scope-user",
	})
	require.NoError(t, err)
	require.NotNil(t, usageResp.Scope)
	assert.Equal(t, "tenant-a/project-a/scope-user", usageResp.Scope.ScopeKey)
	assert.Equal(t, float64(1), usageResp.Usage["request_count"])
}

func TestClient_UsagePolicyEndpoints(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	getResp, err := client.GetUsagePolicy(ctx)
	require.NoError(t, err)
	assert.False(t, getResp.Policy.QuotaEnabled)

	maxRequests := 4
	updateResp, err := client.UpdateUsagePolicy(ctx, UsagePolicyUpdateRequest{
		TrackingEnabled:    boolPtr(true),
		QuotaEnabled:       boolPtr(true),
		DefaultMaxRequests: &maxRequests,
		Users: map[string]UsageQuotaLimitConfig{
			"tenant-a/project-a/alice": {MaxRequests: &maxRequests},
		},
	})
	require.NoError(t, err)
	assert.True(t, updateResp.Policy.QuotaEnabled)
	assert.Equal(t, 4, updateResp.Policy.DefaultMaxRequests)
	require.Contains(t, updateResp.Policy.Users, "tenant-a/project-a/alice")

	deleteResp, err := client.DeleteUsagePolicyEntry(ctx, DeleteUsagePolicyEntryRequest{
		Level: "user",
		Key:   "tenant-a/project-a/alice",
	})
	require.NoError(t, err)
	assert.True(t, deleteResp.Deleted)
	_, ok := deleteResp.Policy.Users["tenant-a/project-a/alice"]
	assert.False(t, ok)
}

func TestClient_GetAuthPolicy(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		handler.SetScopeResolverConfig(skillshandler.ScopeResolverConfig{
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
		})
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	resp, err := client.GetAuthPolicy(ctx)
	require.NoError(t, err)
	assert.True(t, resp.Policy.Enabled)
	assert.True(t, resp.Policy.JWTClaimsEnabled)
	assert.True(t, resp.Policy.JWTSecretConfigured)
	assert.Equal(t, []string{"skills-admin"}, resp.Policy.AdminRoles)
	assert.Equal(t, []string{"X-Role"}, resp.Policy.RoleHeaders)
}

func TestClient_AuthPolicyEndpoints(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		handler.SetScopeResolverConfig(skillshandler.ScopeResolverConfig{
			Enabled: true,
			APIKeyScopes: map[string]skillshandler.UsageScope{
				"scope-key-a": {TenantID: "tenant-a", ProjectID: "project-a", UserID: "alice"},
			},
			AdminRoles: []string{"skills-admin"},
		})
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	updateResp, err := client.UpdateAuthPolicy(ctx, AuthPolicyUpdateRequest{
		RoleHeaders: []string{"X-Role"},
		RoleClaims:  []string{"roles"},
		AdminRoles:  []string{"platform-admin"},
		APIKeyScopes: map[string]UsageScope{
			"scope-key-b": {TenantID: "tenant-b", ProjectID: "project-b", UserID: "bob"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"skills-admin", "platform-admin"}, updateResp.Policy.AdminRoles)
	assert.Equal(t, 2, updateResp.Policy.APIKeyScopeCount)

	deleteResp, err := client.DeleteAuthPolicyEntry(ctx, DeleteAuthPolicyEntryRequest{
		Field: "api_key_scope",
		Key:   "scope-key-a",
	})
	require.NoError(t, err)
	assert.True(t, deleteResp.Deleted)
	assert.Equal(t, 1, deleteResp.Policy.APIKeyScopeCount)
}

func TestClient_MutationPolicyEndpoints(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		handler.SetMutationPolicy(skillshandler.MutationPolicy{
			ReadOnly: false,
		})
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	getResp, err := client.GetMutationPolicy(ctx)
	require.NoError(t, err)
	assert.False(t, getResp.Policy.ReadOnly)

	updateResp, err := client.UpdateMutationPolicy(ctx, MutationPolicyUpdateRequest{
		ReadOnly:       boolPtr(true),
		DisableImport:  boolPtr(true),
		DisablePersist: boolPtr(true),
	})
	require.NoError(t, err)
	assert.True(t, updateResp.Policy.ReadOnly)
	assert.True(t, updateResp.Policy.DisableImport)
	assert.True(t, updateResp.Policy.DisablePersist)
}

func TestClient_GetGovernancePolicy(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		handler.SetMutationPolicy(skillshandler.MutationPolicy{ReadOnly: true})
		handler.SetUsagePolicy(skillshandler.UsagePolicy{
			TrackingEnabled:    true,
			QuotaEnabled:       true,
			DefaultMaxRequests: 7,
		})
		handler.SetScopeResolverConfig(skillshandler.ScopeResolverConfig{
			Enabled: true,
		})
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	resp, err := client.GetGovernancePolicy(ctx)
	require.NoError(t, err)
	assert.True(t, resp.MutationPolicy.ReadOnly)
	assert.True(t, resp.UsagePolicy.QuotaEnabled)
	assert.Equal(t, 7, resp.UsagePolicy.DefaultMaxRequests)
	assert.True(t, resp.AuthPolicy.Enabled)
}

func TestClient_GetUsageLedger(t *testing.T) {
	server := newManagedTestServer(t, true, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetAdminToken("secret-token")
		handler.SetUsagePolicy(skillshandler.UsagePolicy{TrackingEnabled: true})
		handler.SetUsageLedgerStore(&testUsageLedgerStore{})
	})

	client := NewClient(server.URL, WithAdminToken("secret-token"))
	ctx := context.Background()

	_, err := client.ExecuteSkill(ctx, "echo-skill", ExecuteSkillRequest{
		Prompt:    "record ledger",
		UserID:    "alice",
		TenantID:  "tenant-a",
		ProjectID: "project-a",
	})
	require.NoError(t, err)

	ledgerResp, err := client.GetUsageLedger(ctx, GetUsageLedgerParams{
		Scope: UsageScope{
			TenantID:  "tenant-a",
			ProjectID: "project-a",
			UserID:    "alice",
		},
		Entrypoint: "execute",
		Skill:      "echo-skill",
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, ledgerResp.Records, 1)
	assert.Equal(t, true, ledgerResp.Records[0].Success)
	assert.Equal(t, "execute", ledgerResp.Records[0].Metadata["entrypoint"])
}

func TestSessionRuntimeCommandRequest_MarshalIncludesRunMeta(t *testing.T) {
	payload, err := json.Marshal(SessionRuntimeCommandRequest{
		Type:   "submit_prompt",
		Prompt: "hello",
		RunMeta: &SessionRunMeta{
			Team: &SessionTeamRunMeta{
				TeamID:        "team-1",
				AgentID:       "mate-1",
				CurrentTaskID: "task-1",
			},
		},
	})
	require.NoError(t, err)

	body := string(payload)
	assert.Contains(t, body, `"run_meta"`)
	assert.Contains(t, body, `"team_id":"team-1"`)
	assert.Contains(t, body, `"agent_id":"mate-1"`)
	assert.Contains(t, body, `"current_task_id":"task-1"`)
}

func TestSessionRuntimeState_UnmarshalIncludesCurrentRunMeta(t *testing.T) {
	var response SessionRuntimeStateResponse
	err := json.Unmarshal([]byte(`{
		"state": {
			"session_id": "sess-1",
			"status": "running",
			"current_run_meta": {
				"team": {
					"team_id": "team-1",
					"agent_id": "mate-1",
					"current_task_id": "task-1"
				}
			},
			"updated_at": "2026-03-15T00:00:00Z"
		}
	}`), &response)
	require.NoError(t, err)
	require.NotNil(t, response.State.CurrentRunMeta)
	require.NotNil(t, response.State.CurrentRunMeta.Team)

	assert.Equal(t, "team-1", response.State.CurrentRunMeta.Team.TeamID)
	assert.Equal(t, "mate-1", response.State.CurrentRunMeta.Team.AgentID)
	assert.Equal(t, "task-1", response.State.CurrentRunMeta.Team.CurrentTaskID)
}

func TestClient_SessionAgentLifecycle(t *testing.T) {
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	})
	client := NewClient(server.URL)
	ctx := context.Background()

	parentResp, err := client.CreateSession(ctx, CreateSessionRequest{
		UserID: "agent-http-user",
		Title:  "agent-parent",
	})
	require.NoError(t, err)

	spawnResp, err := client.SpawnSessionAgent(ctx, parentResp.Session.ID, SpawnSessionAgentRequest{
		AgentType:   "explorer",
		ForkContext: boolPtr(true),
	})
	require.NoError(t, err)
	require.NotEmpty(t, spawnResp.Agent.SessionID)
	assert.Equal(t, parentResp.Session.ID, spawnResp.Agent.ParentSessionID)
	assert.Equal(t, "explorer", spawnResp.Agent.AgentType)
	assert.True(t, spawnResp.Agent.Created)

	statusResp, err := client.GetSessionAgentStatus(ctx, parentResp.Session.ID, spawnResp.Agent.SessionID)
	require.NoError(t, err)
	assert.Equal(t, spawnResp.Agent.SessionID, statusResp.Agent.SessionID)
	assert.Equal(t, parentResp.Session.ID, statusResp.Agent.ParentSessionID)
	assert.Equal(t, "explorer", statusResp.Agent.AgentType)

	inputResp, err := client.SendSessionAgentInput(ctx, parentResp.Session.ID, spawnResp.Agent.SessionID, SendSessionAgentInputRequest{
		Message: "Reply with exactly child done",
	})
	require.NoError(t, err)
	assert.Equal(t, spawnResp.Agent.SessionID, inputResp.Agent.SessionID)
	assert.True(t, inputResp.Agent.Queued)

	waitResp, err := client.WaitSessionAgents(ctx, parentResp.Session.ID, WaitSessionAgentsRequest{
		IDs:       []string{spawnResp.Agent.SessionID},
		TimeoutMs: 2000,
	})
	require.NoError(t, err)
	require.NotNil(t, waitResp.Result.Agent)
	assert.Equal(t, spawnResp.Agent.SessionID, waitResp.Result.Agent.SessionID)
	assert.NotEmpty(t, waitResp.Result.Agent.Status)
	assert.False(t, waitResp.Result.TimedOut)
	assert.GreaterOrEqual(t, waitResp.Result.ReadyCount, 1)

	eventsResp, err := client.ListSessionAgentEvents(ctx, parentResp.Session.ID, spawnResp.Agent.SessionID, ListSessionAgentEventsParams{
		AfterSeq: 0,
		Limit:    20,
		WaitMs:   0,
	})
	require.NoError(t, err)
	assert.Equal(t, spawnResp.Agent.SessionID, eventsResp.Result.SessionID)
	assert.GreaterOrEqual(t, eventsResp.Result.Count, 0)
	assert.GreaterOrEqual(t, eventsResp.Result.LatestSeq, int64(0))

	closeResp, err := client.CloseSessionAgent(ctx, parentResp.Session.ID, spawnResp.Agent.SessionID)
	require.NoError(t, err)
	assert.Equal(t, spawnResp.Agent.SessionID, closeResp.Agent.SessionID)
	assert.Equal(t, "stopped", closeResp.Agent.Status)

	resumeResp, err := client.ResumeSessionAgent(ctx, parentResp.Session.ID, spawnResp.Agent.SessionID)
	require.NoError(t, err)
	assert.Equal(t, spawnResp.Agent.SessionID, resumeResp.Agent.SessionID)
	assert.True(t, resumeResp.Agent.Exists)
}

func TestClient_SessionAgentEndpointsEncodePathsAndQuery(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotQuery  string
		gotBody   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/agents/wait"):
			_, _ = w.Write([]byte(`{"result":{"matched_id":"child-1","matched_session_id":"child-1","ready_count":1}}`))
		case strings.HasSuffix(r.URL.Path, "/events"):
			_, _ = w.Write([]byte(`{"result":{"session_id":"child-1","count":0}}`))
		default:
			_, _ = w.Write([]byte(`{"agent":{"session_id":"child-1","status":"idle","exists":true}}`))
		}
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL)

	_, err := client.WaitSessionAgents(context.Background(), "parent/1", WaitSessionAgentsRequest{
		IDs:       []string{"child-1", "child-2"},
		TimeoutMs: 1500,
	})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/skills/sessions/parent%2F1/agents/wait", gotPath)
	assert.Contains(t, gotBody, `"ids":["child-1","child-2"]`)
	assert.Contains(t, gotBody, `"timeout_ms":1500`)

	_, err = client.ListSessionAgentEvents(context.Background(), "parent/1", "child/1", ListSessionAgentEventsParams{
		AfterSeq: 12,
		Limit:    5,
		WaitMs:   900,
	})
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, "/api/skills/sessions/parent%2F1/agents/child%2F1/events", gotPath)
	assert.Contains(t, gotQuery, "after_seq=12")
	assert.Contains(t, gotQuery, "limit=5")
	assert.Contains(t, gotQuery, "wait_ms=900")
}

func TestClient_ReportTaskOutcomeUsesCanonicalEndpoint(t *testing.T) {
	var (
		gotPath string
		gotReq  ReportTaskOutcomeRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotReq))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":{"id":"task-1","team_id":"team-1","status":"done","summary":"finished","result_ref":"artifact://1"}}`))
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL)
	resp, err := client.ReportTaskOutcome(context.Background(), "team-1", "task-1", ReportTaskOutcomeRequest{
		TaskStatus: "done",
		Summary:    "finished",
		ResultRef:  "artifact://1",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "/api/skills/teams/team-1/tasks/task-1/outcome", gotPath)
	assert.Equal(t, "done", gotReq.TaskStatus)
	assert.Equal(t, "finished", gotReq.Summary)
	assert.Equal(t, "artifact://1", gotReq.ResultRef)
	assert.Equal(t, "done", resp.Task.Status)
	require.NotNil(t, resp.Task.ResultRef)
	assert.Equal(t, "artifact://1", *resp.Task.ResultRef)
}

func TestClient_LegacyTaskOutcomeHelpersUseCompatibilityEndpoints(t *testing.T) {
	cases := []struct {
		name        string
		call        func(*Client) (*ReportTaskOutcomeResponse, error)
		expectedURL string
	}{
		{
			name: "complete",
			call: func(c *Client) (*ReportTaskOutcomeResponse, error) {
				return c.CompleteTask(context.Background(), "team-1", "task-1", ReportTaskOutcomeRequest{
					Summary: "done summary",
				})
			},
			expectedURL: "/api/skills/teams/team-1/tasks/task-1/complete",
		},
		{
			name: "fail",
			call: func(c *Client) (*ReportTaskOutcomeResponse, error) {
				return c.FailTask(context.Background(), "team-1", "task-1", ReportTaskOutcomeRequest{
					Summary: "failed summary",
				})
			},
			expectedURL: "/api/skills/teams/team-1/tasks/task-1/fail",
		},
		{
			name: "block",
			call: func(c *Client) (*ReportTaskOutcomeResponse, error) {
				return c.BlockTask(context.Background(), "team-1", "task-1", ReportTaskOutcomeRequest{
					Summary: "blocked summary",
				})
			},
			expectedURL: "/api/skills/teams/team-1/tasks/task-1/block",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"task":{"id":"task-1","team_id":"team-1","status":"ok","summary":"ok"}}`))
			}))
			t.Cleanup(server.Close)

			client := NewClient(server.URL)
			_, err := tc.call(client)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedURL, gotPath)
		})
	}
}

func TestClient_GetTeamTask(t *testing.T) {
	var (
		teamID     string
		depID      string
		taskID     string
		followupID string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-task?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		depID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "dep",
			Status: team.TaskStatusDone,
		})
		require.NoError(t, err)
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "task",
			Status: team.TaskStatusRunning,
		})
		require.NoError(t, err)
		followupID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "followup",
			Status: team.TaskStatusPending,
		})
		require.NoError(t, err)
		require.NoError(t, store.AddTaskDependency(ctx, taskID, depID))
		require.NoError(t, store.AddTaskDependency(ctx, followupID, taskID))
	})

	client := NewClient(server.URL)
	resp, err := client.GetTeamTask(context.Background(), teamID, taskID, GetTeamTaskOptions{
		IncludeDependencies: true,
		IncludeDependents:   true,
	})
	require.NoError(t, err)
	assert.Equal(t, taskID, resp.Task.ID)
	assert.Equal(t, "task", resp.Task.Title)
	assert.Equal(t, []string{depID}, resp.Dependencies)
	assert.Equal(t, []string{followupID}, resp.Dependents)
}

func TestClient_ListTeamTasks(t *testing.T) {
	var (
		teamID     string
		taskID     string
		depID      string
		followupID string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-list-tasks?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		assignee := "mate-1"
		depID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "dep",
			Status: team.TaskStatusDone,
		})
		require.NoError(t, err)
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID:   teamID,
			Title:    "task",
			Status:   team.TaskStatusRunning,
			Assignee: &assignee,
		})
		require.NoError(t, err)
		followupID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "followup",
			Status: team.TaskStatusPending,
		})
		require.NoError(t, err)
		require.NoError(t, store.AddTaskDependency(ctx, taskID, depID))
		require.NoError(t, store.AddTaskDependency(ctx, followupID, taskID))
	})

	client := NewClient(server.URL)
	resp, err := client.ListTeamTasks(context.Background(), teamID, ListTeamTasksParams{
		Status:              []string{string(team.TaskStatusRunning)},
		Assignee:            "mate-1",
		TaskIDs:             []string{taskID},
		IncludeDependencies: true,
		IncludeDependents:   true,
	})
	require.NoError(t, err)
	require.Len(t, resp.Tasks, 1)
	assert.Equal(t, taskID, resp.Tasks[0].ID)
	assert.Equal(t, "task", resp.Tasks[0].Title)
	assert.Equal(t, []string{string(team.TaskStatusRunning)}, resp.Status)
	require.NotNil(t, resp.Assignee)
	assert.Equal(t, "mate-1", *resp.Assignee)
	assert.Equal(t, []string{taskID}, resp.TaskIDs)
	assert.Equal(t, []string{depID}, resp.Dependencies[taskID])
	assert.Equal(t, []string{followupID}, resp.Dependents[taskID])
}

func TestClient_ListTaskDependenciesAndDependents(t *testing.T) {
	var (
		teamID     string
		taskID     string
		depID      string
		followupID string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-deps?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		depID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "dep",
			Status: team.TaskStatusDone,
		})
		require.NoError(t, err)
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "task",
			Status: team.TaskStatusRunning,
		})
		require.NoError(t, err)
		followupID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "followup",
			Status: team.TaskStatusPending,
		})
		require.NoError(t, err)
		require.NoError(t, store.AddTaskDependency(ctx, taskID, depID))
		require.NoError(t, store.AddTaskDependency(ctx, followupID, taskID))
	})

	client := NewClient(server.URL)

	depsResp, err := client.ListTaskDependencies(context.Background(), teamID, taskID)
	require.NoError(t, err)
	assert.Equal(t, taskID, depsResp.TaskID)
	assert.Equal(t, []string{depID}, depsResp.Dependencies)
	assert.Equal(t, 1, depsResp.Count)

	dependentsResp, err := client.ListTaskDependents(context.Background(), teamID, taskID)
	require.NoError(t, err)
	assert.Equal(t, taskID, dependentsResp.TaskID)
	assert.Equal(t, []string{followupID}, dependentsResp.Dependents)
	assert.Equal(t, 1, dependentsResp.Count)
}

func TestClient_CreateAndListTeams(t *testing.T) {
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-teams?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)
	})

	client := NewClient(server.URL)
	createResp, err := client.CreateTeam(context.Background(), CreateTeamRequest{
		WorkspaceID:   "workspace-a",
		LeadSessionID: "lead-session",
		Status:        "active",
		Strategy:      "parallel",
		MaxTeammates:  4,
		MaxWriters:    2,
	})
	require.NoError(t, err)
	require.NotEmpty(t, createResp.Team.ID)
	assert.Equal(t, "workspace-a", createResp.Team.WorkspaceID)
	assert.Equal(t, "lead-session", createResp.Team.LeadSessionID)

	listResp, err := client.ListTeams(context.Background(), ListTeamsParams{
		Status:      "active",
		WorkspaceID: "workspace-a",
		TeamIDs:     []string{createResp.Team.ID},
	})
	require.NoError(t, err)
	require.Len(t, listResp.Teams, 1)
	assert.Equal(t, createResp.Team.ID, listResp.Teams[0].ID)
	assert.Equal(t, "workspace-a", listResp.Teams[0].WorkspaceID)
}

func TestClient_ListTeammates(t *testing.T) {
	var teamID string
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-teammates?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		_, err = store.UpsertTeammate(ctx, team.Teammate{
			ID:     "mate-1",
			TeamID: teamID,
			State:  team.TeammateStateIdle,
		})
		require.NoError(t, err)
		_, err = store.UpsertTeammate(ctx, team.Teammate{
			ID:     "mate-2",
			TeamID: teamID,
			State:  team.TeammateStateBusy,
		})
		require.NoError(t, err)
	})

	client := NewClient(server.URL)
	resp, err := client.ListTeammates(context.Background(), teamID, ListTeammatesParams{
		State: "idle",
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, resp.Teammates, 1)
	assert.Equal(t, "mate-1", resp.Teammates[0].ID)
	require.NotNil(t, resp.State)
	assert.Equal(t, "idle", *resp.State)
}

func TestClient_PlanTeamTasksUsesEndpoint(t *testing.T) {
	var (
		gotPath string
		gotReq  PlanTeamTasksRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotReq))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"team_id":"team-1",
			"goal":"ship feature",
			"auto_persist":true,
			"tasks":[{"id":"task-1","team_id":"team-1","title":"draft spec","status":"pending","read_paths":["docs/spec.md"],"write_paths":["docs/plan.md"],"deliverables":["plan"],"priority":2}],
			"dependencies":[{"task_id":"task-1","depends_on_id":"task-0"}],
			"task_count":1,
			"dependency_count":1,
			"summary":"initial plan"
		}`))
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL)
	resp, err := client.PlanTeamTasks(context.Background(), "team-1", PlanTeamTasksRequest{
		Goal:        "ship feature",
		AutoPersist: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "/api/skills/teams/team-1/plan", gotPath)
	assert.Equal(t, "ship feature", gotReq.Goal)
	assert.True(t, gotReq.AutoPersist)
	assert.Equal(t, "team-1", resp.TeamID)
	assert.Equal(t, "ship feature", resp.Goal)
	assert.True(t, resp.AutoPersist)
	require.Len(t, resp.Tasks, 1)
	assert.Equal(t, "draft spec", resp.Tasks[0].Title)
	assert.Equal(t, []string{"docs/spec.md"}, resp.Tasks[0].ReadPaths)
	assert.Equal(t, []string{"docs/plan.md"}, resp.Tasks[0].WritePaths)
	assert.Equal(t, []string{"plan"}, resp.Tasks[0].Deliverables)
	require.Len(t, resp.Dependencies, 1)
	assert.Equal(t, "task-0", resp.Dependencies[0].DependsOnID)
	assert.Equal(t, "initial plan", resp.Summary)
}

func TestClient_GetTaskGraph(t *testing.T) {
	var (
		store  *team.SQLiteStore
		teamID string
		taskID string
		depID  string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		var err error
		store, err = team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-task-graph?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)

		depID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "dependency",
			Status: team.TaskStatusDone,
		})
		require.NoError(t, err)

		assignee := "mate-1"
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID:     teamID,
			Title:      "ready task",
			Status:     team.TaskStatusReady,
			Assignee:   &assignee,
			ReadPaths:  []string{"docs/spec.md"},
			WritePaths: []string{"docs/plan.md"},
		})
		require.NoError(t, err)
		require.NoError(t, store.AddTaskDependency(ctx, taskID, depID))
	})

	client := NewClient(server.URL)
	resp, err := client.GetTaskGraph(context.Background(), teamID, GetTaskGraphParams{
		Status:          []string{"ready", " "},
		Assignee:        "mate-1",
		TaskIDs:         []string{taskID, " "},
		IncludeExternal: true,
		Limit:           5,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Count)
	require.Len(t, resp.Tasks, 1)
	assert.Equal(t, taskID, resp.Tasks[0].ID)
	assert.Equal(t, []string{"docs/spec.md"}, resp.Tasks[0].ReadPaths)
	assert.Equal(t, []string{"docs/plan.md"}, resp.Tasks[0].WritePaths)
	assert.Equal(t, 1, resp.EdgeCount)
	require.Len(t, resp.Edges, 1)
	assert.Equal(t, taskID, resp.Edges[0].TaskID)
	assert.Equal(t, depID, resp.Edges[0].DependsOnID)
	assert.Equal(t, []string{depID}, resp.MissingDependencies)
	assert.Equal(t, []string{taskID}, resp.TaskIDs)
	assert.Equal(t, []string{"ready"}, resp.Status)
	require.NotNil(t, resp.Assignee)
	assert.Equal(t, "mate-1", *resp.Assignee)
	assert.True(t, resp.IncludeExternal)
	assert.Equal(t, 5, resp.Limit)
}

func TestClient_CreateAndUpdateTeamTask(t *testing.T) {
	var teamID string
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-write-task?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
	})

	client := NewClient(server.URL)
	createResp, err := client.CreateTeamTask(context.Background(), teamID, CreateTeamTaskRequest{
		Title:     "new task",
		Goal:      "ship feature",
		Status:    "pending",
		Priority:  2,
		ResultRef: "artifact://draft",
	})
	require.NoError(t, err)
	require.NotEmpty(t, createResp.Task.ID)
	assert.Equal(t, "new task", createResp.Task.Title)
	assert.Equal(t, "pending", createResp.Task.Status)
	require.NotNil(t, createResp.Task.ResultRef)
	assert.Equal(t, "artifact://draft", *createResp.Task.ResultRef)

	newTitle := "updated task"
	newStatus := "ready"
	newSummary := "ready to assign"
	newResultRef := "artifact://final"
	updateResp, err := client.UpdateTeamTask(context.Background(), teamID, createResp.Task.ID, UpdateTeamTaskRequest{
		Title:     &newTitle,
		Status:    &newStatus,
		Summary:   &newSummary,
		ResultRef: &newResultRef,
	})
	require.NoError(t, err)
	assert.Equal(t, createResp.Task.ID, updateResp.Task.ID)
	assert.Equal(t, "updated task", updateResp.Task.Title)
	assert.Equal(t, "ready", updateResp.Task.Status)
	assert.Equal(t, "ready to assign", updateResp.Task.Summary)
	require.NotNil(t, updateResp.Task.ResultRef)
	assert.Equal(t, "artifact://final", *updateResp.Task.ResultRef)
}

func TestClient_AddTaskDependency(t *testing.T) {
	var (
		teamID string
		taskID string
		depID  string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-add-dep?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "task",
			Status: team.TaskStatusPending,
		})
		require.NoError(t, err)
		depID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "dep",
			Status: team.TaskStatusDone,
		})
		require.NoError(t, err)
	})

	client := NewClient(server.URL)
	resp, err := client.AddTaskDependency(context.Background(), teamID, taskID, depID)
	require.NoError(t, err)
	assert.Equal(t, taskID, resp.TaskID)
	assert.Equal(t, depID, resp.DependsOnID)
}

func TestClient_ClaimReadyTasks(t *testing.T) {
	var teamID string
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-claim-ready?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		_, err = store.UpsertTeammate(ctx, team.Teammate{
			ID:     "mate-1",
			TeamID: teamID,
			State:  team.TeammateStateIdle,
		})
		require.NoError(t, err)
		_, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "ready task",
			Status: team.TaskStatusReady,
		})
		require.NoError(t, err)
	})

	client := NewClient(server.URL)
	resp, err := client.ClaimReadyTasks(context.Background(), teamID, ClaimReadyTasksRequest{Limit: 1})
	require.NoError(t, err)
	require.Len(t, resp.Assignments, 1)
	assert.Equal(t, "mate-1", resp.Assignments[0].Teammate.ID)
	assert.Equal(t, "ready task", resp.Assignments[0].Task.Title)
}

func TestClient_ReclaimExpiredTasks(t *testing.T) {
	var (
		store  *team.SQLiteStore
		teamID string
		taskID string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		var err error
		store, err = team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-reclaim-expired?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		_, err = store.UpsertTeammate(ctx, team.Teammate{
			ID:     "mate-1",
			TeamID: teamID,
			State:  team.TeammateStateBusy,
		})
		require.NoError(t, err)

		assignee := "mate-1"
		leaseUntil := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID:     teamID,
			Title:      "running task",
			Status:     team.TaskStatusRunning,
			Assignee:   &assignee,
			LeaseUntil: &leaseUntil,
		})
		require.NoError(t, err)
	})

	asOf := time.Date(2026, 3, 15, 9, 5, 0, 0, time.UTC)
	client := NewClient(server.URL)
	resp, err := client.ReclaimExpiredTasks(context.Background(), teamID, ReclaimExpiredTasksRequest{
		Limit: 1,
		AsOf:  &asOf,
	})
	require.NoError(t, err)
	assert.Equal(t, teamID, resp.TeamID)
	assert.WithinDuration(t, asOf, resp.AsOf, time.Second)
	assert.False(t, resp.DryRun)
	assert.Equal(t, 1, resp.Count)
	require.Len(t, resp.Reclaimed, 1)
	assert.Equal(t, taskID, resp.Reclaimed[0].Task.ID)
	assert.Equal(t, "mate-1", resp.Reclaimed[0].PreviousAssignee)
	require.NotNil(t, resp.Reclaimed[0].PreviousLeaseUntil)
	assert.WithinDuration(t, time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC), *resp.Reclaimed[0].PreviousLeaseUntil, time.Second)

	updated, err := store.GetTask(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, team.TaskStatusReady, updated.Status)
	assert.Equal(t, 1, updated.RetryCount)
	assert.Nil(t, updated.Assignee)
}

func TestClient_MarkReadyTasks(t *testing.T) {
	var (
		store  *team.SQLiteStore
		teamID string
		taskID string
		depID  string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		var err error
		store, err = team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-mark-ready?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		depID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "done dependency",
			Status: team.TaskStatusDone,
		})
		require.NoError(t, err)
		taskID, err = store.CreateTask(ctx, team.Task{
			TeamID: teamID,
			Title:  "blocked task",
			Status: team.TaskStatusPending,
		})
		require.NoError(t, err)
		require.NoError(t, store.AddTaskDependency(ctx, taskID, depID))
	})

	client := NewClient(server.URL)
	resp, err := client.MarkReadyTasks(context.Background(), teamID)
	require.NoError(t, err)
	assert.Equal(t, teamID, resp.TeamID)
	assert.EqualValues(t, 1, resp.Count)

	updated, err := store.GetTask(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, team.TaskStatusReady, updated.Status)
}

func TestClient_SendTeamMailboxMessage(t *testing.T) {
	var teamID string
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-send-mail?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
	})

	client := NewClient(server.URL)
	resp, err := client.SendTeamMailboxMessage(context.Background(), teamID, SendTeamMailboxMessageRequest{
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "question",
		Body:      "confirm the task boundary",
		Metadata: map[string]interface{}{
			"priority": "high",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, teamID, resp.Message.TeamID)
	assert.Equal(t, "lead", resp.Message.FromAgent)
	assert.Equal(t, "mate-1", resp.Message.ToAgent)
	assert.Equal(t, "question", resp.Message.Kind)
	assert.Equal(t, "confirm the task boundary", resp.Message.Body)
	assert.Empty(t, resp.DispatchError)
}

func TestClient_ReplanTaskUsesEndpoint(t *testing.T) {
	var (
		gotPath string
		gotReq  ReplanTaskRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotReq))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"team_id":"team-1",
			"failed_task":"task-1",
			"auto_persist":true,
			"tasks":[{"id":"task-2","team_id":"team-1","title":"follow up","status":"pending"}],
			"dependencies":[{"task_id":"task-2","depends_on_id":"task-1"}],
			"task_count":1,
			"dependency_count":1,
			"summary":"replanned"
		}`))
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL)
	resp, err := client.ReplanTask(context.Background(), "team-1", "task-1", ReplanTaskRequest{
		AutoPersist: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "/api/skills/teams/team-1/tasks/task-1/replan", gotPath)
	assert.True(t, gotReq.AutoPersist)
	assert.Equal(t, "team-1", resp.TeamID)
	assert.Equal(t, "task-1", resp.FailedTask)
	assert.Len(t, resp.Tasks, 1)
	assert.Equal(t, "follow up", resp.Tasks[0].Title)
	assert.Len(t, resp.Dependencies, 1)
	assert.Equal(t, "replanned", resp.Summary)
}

func TestClient_ListAndAckTeamMailbox(t *testing.T) {
	var (
		teamID    string
		messageID string
	)
	server := newManagedTestServer(t, false, func(handler *skillshandler.Handler, _ *runtimeskill.Registry, _ *runtimeskill.Loader, _ *chat.SessionManager) {
		store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:skillsapi-team-mailbox?mode=memory&cache=shared"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		handler.SetTeamStore(store)

		ctx := context.Background()
		teamID, err = store.CreateTeam(ctx, team.Team{})
		require.NoError(t, err)
		messageID, err = store.InsertMail(ctx, team.MailMessage{
			TeamID:    teamID,
			FromAgent: "lead",
			ToAgent:   "mate-1",
			Kind:      "question",
			Body:      "confirm the task boundary",
		})
		require.NoError(t, err)
	})

	client := NewClient(server.URL)
	mailboxResp, err := client.ListTeamMailbox(context.Background(), teamID, ListTeamMailboxParams{
		ToAgent:    "mate-1",
		UnreadOnly: true,
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, mailboxResp.Messages, 1)
	assert.Equal(t, messageID, mailboxResp.Messages[0].ID)
	assert.Equal(t, "confirm the task boundary", mailboxResp.Messages[0].Body)

	ackResp, err := client.AckTeamMailboxMessage(context.Background(), teamID, messageID, "mate-1")
	require.NoError(t, err)
	assert.Equal(t, teamID, ackResp.TeamID)
	assert.Equal(t, messageID, ackResp.MessageID)
	assert.Equal(t, "mate-1", ackResp.AgentID)
}

func strPtr(v string) *string {
	return &v
}

