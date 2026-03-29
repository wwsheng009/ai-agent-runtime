package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type permissionTestHandler struct {
	output string
}

func (h *permissionTestHandler) Execute(ctx interface{}, req *types.Request) (*types.Result, error) {
	return &types.Result{
		Success: true,
		Output:  h.output,
	}, nil
}

type concurrentMCPManager struct {
	mu            sync.Mutex
	concurrent    int
	maxConcurrent int
	delay         time.Duration
}

func (m *concurrentMCPManager) FindTool(toolName string) (ToolInfo, error) {
	return ToolInfo{Name: toolName, Description: toolName, MCPName: "test-mcp", Enabled: true}, nil
}

func (m *concurrentMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.mu.Lock()
	m.concurrent++
	if m.concurrent > m.maxConcurrent {
		m.maxConcurrent = m.concurrent
	}
	m.mu.Unlock()

	time.Sleep(m.delay)

	m.mu.Lock()
	m.concurrent--
	m.mu.Unlock()

	return fmt.Sprintf("%s:%v", toolName, args["prompt"]), nil
}

func (m *concurrentMCPManager) ListTools() []ToolInfo {
	return []ToolInfo{
		{Name: "tool_a", MCPName: "test-mcp", Enabled: true},
		{Name: "tool_b", MCPName: "test-mcp", Enabled: true},
		{Name: "tool_c", MCPName: "test-mcp", Enabled: true},
	}
}

type deniedMCPManager struct{}

func (m *deniedMCPManager) FindTool(toolName string) (ToolInfo, error) {
	return ToolInfo{Name: toolName, Description: toolName, MCPName: "test-mcp", Enabled: true}, nil
}

func (m *deniedMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, runtimeerrors.WrapWithContext(
		runtimeerrors.ErrAgentPermission,
		"sandbox denied workflow tool execution",
		nil,
		map[string]interface{}{
			"policy": "sandbox",
			"tool":   toolName,
		},
	)
}

func (m *deniedMCPManager) ListTools() []ToolInfo {
	return []ToolInfo{
		{Name: "tool_denied", MCPName: "test-mcp", Enabled: true},
	}
}

type governanceAwareMCPManager struct{}

func (m *governanceAwareMCPManager) FindTool(toolName string) (ToolInfo, error) {
	return ToolInfo{
		Name:          toolName,
		Description:   toolName,
		MCPName:       "remote-governed",
		MCPTrustLevel: "trusted_remote",
		ExecutionMode: "remote_mcp",
		Enabled:       true,
	}, nil
}

func (m *governanceAwareMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "GOVERNANCE_OK", nil
}

func (m *governanceAwareMCPManager) ListTools() []ToolInfo {
	return []ToolInfo{
		{
			Name:          "tool_governed",
			MCPName:       "remote-governed",
			MCPTrustLevel: "trusted_remote",
			ExecutionMode: "remote_mcp",
			Enabled:       true,
		},
	}
}

type recordingMCPManager struct {
	lastArgs map[string]interface{}
}

func (m *recordingMCPManager) FindTool(toolName string) (ToolInfo, error) {
	return ToolInfo{Name: toolName, Description: toolName, MCPName: "test-mcp", Enabled: true}, nil
}

func (m *recordingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.lastArgs = args
	return "RECORDED", nil
}

func (m *recordingMCPManager) ListTools() []ToolInfo {
	return []ToolInfo{{Name: "tool_a", MCPName: "test-mcp", Enabled: true}}
}

func TestExecutor_ExecuteWorkflow_UsesParallelExecutor(t *testing.T) {
	mcpManager := &concurrentMCPManager{delay: 50 * time.Millisecond}
	registry := NewRegistry(mcpManager)
	executor := NewExecutor(registry, mcpManager, nil)

	workflowSkill := &Skill{
		Name:        "parallel-workflow",
		Description: "parallel execution test",
		Workflow: &Workflow{Steps: []WorkflowStep{
			{ID: "step_a", Name: "A", Tool: "tool_a"},
			{ID: "step_b", Name: "B", Tool: "tool_b"},
			{ID: "step_c", Name: "C", Tool: "tool_c", DependsOn: []string{"step_a", "step_b"}},
		}},
	}

	req := types.NewRequest("parallel prompt")
	start := time.Now()
	result, err := executor.Execute(context.Background(), workflowSkill, req)
	duration := time.Since(start)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, result.Observations, 3)
	assert.GreaterOrEqual(t, mcpManager.maxConcurrent, 2)
	assert.Less(t, duration, 140*time.Millisecond)
	assert.Contains(t, result.Output, "step_a")
	assert.Contains(t, result.Output, "step_b")
	assert.Contains(t, result.Output, "step_c")
}

func TestExecutor_Execute_HydratesDiscoveryOnlySkillBeforeWorkflowRun(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: hydrated_workflow
description: hydrated workflow
triggers:
  - type: keyword
    values: ["hydrate"]
    weight: 1
workflow:
  steps:
    - id: step_a
      name: A
      tool: tool_a
      args:
        mode: "HYDRATED_OK"
`), 0o644))

	loader := NewLoader(nil)
	summary, err := loader.DiscoverFile(manifestPath)
	require.NoError(t, err)
	require.NotNil(t, summary)
	stub := summary.ToSkillStub()
	require.NotNil(t, stub)
	require.NotNil(t, stub.Source)
	require.True(t, stub.Source.DiscoveryOnly)
	require.Len(t, stub.Workflow.Steps, 1)
	require.NotNil(t, stub.Workflow.Steps[0].Args)
	assert.Equal(t, "HYDRATED_OK", stub.Workflow.Steps[0].Args["mode"])

	mcpManager := &recordingMCPManager{}
	executor := NewExecutor(NewRegistry(mcpManager), mcpManager, nil)
	result, err := executor.Execute(context.Background(), stub, types.NewRequest("run"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Len(t, result.Observations, 1)
	require.Equal(t, "RECORDED", result.Output)
	require.NotNil(t, mcpManager.lastArgs)
	require.Equal(t, "HYDRATED_OK", mcpManager.lastArgs["mode"])
}

type systemRejectingProvider struct {
	callCount int
}

func (p *systemRejectingProvider) Name() string { return "system-rejecting" }

func (p *systemRejectingProvider) Call(_ context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCount++
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			return nil, fmt.Errorf("HTTP 400: messages[0].role: unknown variant system, expected user or assistant")
		}
	}
	return &llm.LLMResponse{
		Content: "SKILL_RUNTIME_OK",
		Usage: &types.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
		},
		Model: "system-rejecting",
	}, nil
}

func (p *systemRejectingProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	resp, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: resp.Content}
	close(ch)
	return ch, nil
}

func (p *systemRejectingProvider) CountTokens(text string) int { return len(text) }

func (p *systemRejectingProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true, SupportsJSONMode: true}
}

func (p *systemRejectingProvider) CheckHealth(context.Context) error { return nil }

type systemRejectingUserFirstProvider struct {
	callCount    int
	lastMessages []types.Message
}

func (p *systemRejectingUserFirstProvider) Name() string { return "system-rejecting-user-first" }

func (p *systemRejectingUserFirstProvider) Call(_ context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCount++
	p.lastMessages = append([]types.Message(nil), req.Messages...)

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			return nil, fmt.Errorf("HTTP 400: system role is not supported for this model")
		}
	}
	if len(req.Messages) == 0 || req.Messages[0].Role != "user" {
		return nil, fmt.Errorf("HTTP 400: messages.0.role must be user")
	}

	return &llm.LLMResponse{
		Content: "USER_FIRST_OK",
		Usage: &types.TokenUsage{
			PromptTokens:     12,
			CompletionTokens: 2,
			TotalTokens:      14,
		},
		Model: "system-rejecting-user-first",
	}, nil
}

func (p *systemRejectingUserFirstProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	resp, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: resp.Content}
	close(ch)
	return ch, nil
}

func (p *systemRejectingUserFirstProvider) CountTokens(text string) int { return len(text) }

func (p *systemRejectingUserFirstProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true, SupportsJSONMode: true}
}

func (p *systemRejectingUserFirstProvider) CheckHealth(context.Context) error { return nil }

type recordingLLMProvider struct {
	lastRequest *llm.LLMRequest
}

func (p *recordingLLMProvider) Name() string { return "recording-llm" }

func (p *recordingLLMProvider) Call(_ context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if req != nil {
		cloned := *req
		cloned.Thinking = types.CloneThinkingConfig(req.Thinking)
		cloned.Messages = append([]types.Message(nil), req.Messages...)
		cloned.Tools = append([]types.ToolDefinition(nil), req.Tools...)
		p.lastRequest = &cloned
	}
	return &llm.LLMResponse{
		Content: "RECORDED_LLM_OK",
		Model:   "recording-llm",
	}, nil
}

func (p *recordingLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	resp, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: resp.Content}
	close(ch)
	return ch, nil
}

func (p *recordingLLMProvider) CountTokens(text string) int { return len(text) }

func (p *recordingLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true}
}

func (p *recordingLLMProvider) CheckHealth(context.Context) error { return nil }

func TestExecutor_ExecuteDefault_RetriesWithoutSystemRole(t *testing.T) {
	provider := &systemRejectingProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "system-rejecting",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("system-rejecting", provider))

	executor := NewExecutor(NewRegistry(nil), nil, runtime)
	skillItem := &Skill{
		Name:         "skill_runtime_smoke",
		Description:  "smoke skill",
		SystemPrompt: "Return exactly SKILL_RUNTIME_OK",
		UserPrompt:   "Return exactly SKILL_RUNTIME_OK",
		Triggers: []Trigger{
			{Type: "keyword", Values: []string{"smoke"}, Weight: 1},
		},
	}

	result, err := executor.Execute(context.Background(), skillItem, types.NewRequest("run smoke test"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	assert.Equal(t, "SKILL_RUNTIME_OK", result.Output)
	assert.Equal(t, 2, provider.callCount)
}

func TestExecutor_ExecuteDefault_PropagatesThinkingAndReasoningToLLMRuntime(t *testing.T) {
	provider := &recordingLLMProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "recording-llm",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("recording-llm", provider))

	executor := NewExecutor(NewRegistry(nil), nil, runtime)
	skillItem := &Skill{
		Name:         "skill_runtime_reasoning",
		Description:  "reasoning propagation skill",
		SystemPrompt: "Follow the reasoning policy.",
		UserPrompt:   "Answer the request.",
	}

	req := types.NewRequest("run reasoning test")
	budget := 8192
	req.ReasoningEffort = "high"
	req.Thinking = &types.ThinkingConfig{
		Type:         "enabled",
		BudgetTokens: &budget,
	}

	result, err := executor.Execute(context.Background(), skillItem, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	assert.Equal(t, "RECORDED_LLM_OK", result.Output)
	require.NotNil(t, provider.lastRequest)
	assert.Equal(t, "high", provider.lastRequest.ReasoningEffort)
	if assert.NotNil(t, provider.lastRequest.Thinking) {
		assert.Equal(t, "enabled", provider.lastRequest.Thinking.Type)
		if assert.NotNil(t, provider.lastRequest.Thinking.BudgetTokens) {
			assert.Equal(t, 8192, *provider.lastRequest.Thinking.BudgetTokens)
		}
	}
}

func TestBuildContextSummary_IncludesProfileLayer(t *testing.T) {
	req := types.NewRequest("review prior notes")
	req.Context["profile_memory_path"] = "E:/profiles/dev/agents/coder/memory/memory.json"
	req.Context["context_pack"] = map[string]interface{}{
		"profile": map[string]interface{}{
			"name": "dev",
			"resources": map[string]interface{}{
				"memory": map[string]interface{}{
					"path":    "E:/profiles/dev/agents/coder/memory/memory.json",
					"format":  "json",
					"content": `{"summary":"cached profile memory"}`,
				},
				"notes": map[string]interface{}{
					"path":    "E:/profiles/dev/agents/coder/context/notes.md",
					"format":  "markdown",
					"content": "Profile investigation notes.",
				},
			},
		},
	}

	summary := buildContextSummary(req)
	require.NotEmpty(t, summary)
	assert.Contains(t, summary, `"context_pack"`)
	assert.Contains(t, summary, `"profile"`)
	assert.Contains(t, summary, `cached profile memory`)
	assert.Contains(t, summary, `Profile investigation notes.`)
	assert.False(t, strings.Contains(summary, `"profile_memory_path"`), "expected top-level profile scalar to collapse into context_pack.profile")
}

func TestBuildSystemRoleFallbackMessages_MergesIntoLeadingUser(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("Follow repo conventions."),
		*types.NewUserMessage("Summarize the diff."),
		*types.NewAssistantMessage("Previous answer"),
	}

	fallback, ok := buildSystemRoleFallbackMessages(messages, fmt.Errorf("HTTP 400: unsupported role: system"))
	require.True(t, ok)
	require.Len(t, fallback, 2)
	assert.Equal(t, "user", fallback[0].Role)
	assert.Contains(t, fallback[0].Content, "Follow repo conventions.")
	assert.Contains(t, fallback[0].Content, "Summarize the diff.")
	assert.Equal(t, "assistant", fallback[1].Role)
}

func TestBuildSystemRoleFallbackMessages_PrependsUserWhenHistoryStartsWithAssistant(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("Return concise answers."),
		*types.NewAssistantMessage("Previous answer"),
		*types.NewUserMessage("New request"),
	}

	fallback, ok := buildSystemRoleFallbackMessages(messages, fmt.Errorf("HTTP 400: messages.0.role must be user"))
	require.True(t, ok)
	require.Len(t, fallback, 3)
	assert.Equal(t, "user", fallback[0].Role)
	assert.Equal(t, "System instructions:\nReturn concise answers.", fallback[0].Content)
	assert.Equal(t, "assistant", fallback[1].Role)
	assert.Equal(t, "Previous answer", fallback[1].Content)
	assert.Equal(t, "user", fallback[2].Role)
	assert.Equal(t, "New request", fallback[2].Content)
}

func TestShouldRetryWithoutSystemRole_MatchesCommonErrors(t *testing.T) {
	testCases := []struct {
		name string
		err  string
		want bool
	}{
		{
			name: "legacy expected user message",
			err:  "HTTP 400: messages[0].role: unknown variant system, expected user or assistant",
			want: true,
		},
		{
			name: "dot notation first message",
			err:  "HTTP 400: messages.0.role must be user",
			want: true,
		},
		{
			name: "system role unsupported",
			err:  "HTTP 400: system role is not supported for this model",
			want: true,
		},
		{
			name: "invalid system value",
			err:  "HTTP 400: invalid value: 'system'",
			want: true,
		},
		{
			name: "unrelated validation error",
			err:  "HTTP 400: temperature must be between 0 and 2",
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldRetryWithoutSystemRole(fmt.Errorf("%s", tc.err)))
		})
	}
}

func TestExecutor_ExecuteDefault_RetriesWithoutSystemRoleWhenHistoryStartsWithAssistant(t *testing.T) {
	provider := &systemRejectingUserFirstProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "system-rejecting-user-first",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("system-rejecting-user-first", provider))

	executor := NewExecutor(NewRegistry(nil), nil, runtime)
	skillItem := &Skill{
		Name:         "assistant_history_fallback",
		Description:  "fallback when history starts with assistant",
		SystemPrompt: "Always answer with USER_FIRST_OK",
		UserPrompt:   "Return the marker",
	}

	req := types.NewRequest("ignored prompt")
	req.History = []types.Message{
		*types.NewAssistantMessage("Previous answer"),
	}

	result, err := executor.Execute(context.Background(), skillItem, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	assert.Equal(t, "USER_FIRST_OK", result.Output)
	assert.Equal(t, 2, provider.callCount)
	require.Len(t, provider.lastMessages, 3)
	assert.Equal(t, "user", provider.lastMessages[0].Role)
	assert.Equal(t, "assistant", provider.lastMessages[1].Role)
	assert.Equal(t, "user", provider.lastMessages[2].Role)
	assert.Equal(t, "System instructions:\nAlways answer with USER_FIRST_OK", provider.lastMessages[0].Content)
}

func TestExecutor_PrepareArgs_RendersWorkflowTemplates(t *testing.T) {
	executor := NewExecutor(nil, nil, nil)
	req := types.NewRequest("echo SKILL_SHELL_OK")
	req.Context["file_path"] = "README.md"
	req.Options["limit"] = 50
	req.Metadata.Set("source", "test")

	rendered := executor.prepareArgs(map[string]interface{}{
		"command": "{{prompt}}",
		"path":    "{{context.file_path}}",
		"summary": "Run {{prompt}} against {{context.file_path}}",
		"limit":   "{{options.limit}}",
		"meta": map[string]interface{}{
			"origin": "{{metadata.source}}",
		},
	}, map[string]interface{}{
		"step_fetch": "FETCH_OK",
	}, req)

	assert.Equal(t, "echo SKILL_SHELL_OK", rendered["command"])
	assert.Equal(t, "README.md", rendered["path"])
	assert.Equal(t, "Run echo SKILL_SHELL_OK against README.md", rendered["summary"])
	assert.Equal(t, 50, rendered["limit"])

	meta, ok := rendered["meta"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "test", meta["origin"])
}

func TestExecutor_PrepareArgs_RendersWorkflowResultTemplates(t *testing.T) {
	executor := NewExecutor(nil, nil, nil)
	req := types.NewRequest("ignored")

	rendered := executor.prepareArgs(map[string]interface{}{
		"content": "{{results.step_fetch}}",
	}, map[string]interface{}{
		"step_fetch": "FETCH_OK",
	}, req)

	assert.Equal(t, "FETCH_OK", rendered["content"])
}

func TestExecutor_FormatOutput_ReturnsSingleResultDirectly(t *testing.T) {
	executor := NewExecutor(nil, nil, nil)
	output := executor.formatOutput(map[string]interface{}{
		"run_command": "SKILL_SHELL_OK",
	})
	assert.Equal(t, "SKILL_SHELL_OK", output)
}

func TestExecutor_Execute_DeniesMissingPermissions(t *testing.T) {
	executor := NewExecutor(nil, nil, nil)
	skillItem := &Skill{
		Name:        "permissioned_skill",
		Permissions: []string{"shell"},
		Handler:     &permissionTestHandler{output: "should not run"},
		Triggers:    []Trigger{{Type: "keyword", Values: []string{"shell"}, Weight: 1}},
		Description: "permission test",
	}

	result, err := executor.Execute(context.Background(), skillItem, types.NewRequest("run"))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "requires permissions")
	assert.Equal(t, "", result.Output)
}

func TestExecutor_Execute_AllowsGrantedPermissions(t *testing.T) {
	executor := NewExecutor(nil, nil, nil)
	skillItem := &Skill{
		Name:        "permissioned_skill",
		Permissions: []string{"shell"},
		Handler:     &permissionTestHandler{output: "PERMISSION_OK"},
		Triggers:    []Trigger{{Type: "keyword", Values: []string{"shell"}, Weight: 1}},
		Description: "permission test",
	}

	req := types.NewRequest("run")
	req.Metadata.Set("permissions", []string{"shell"})

	result, err := executor.Execute(context.Background(), skillItem, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Equal(t, "PERMISSION_OK", result.Output)
}

func TestExecutor_Execute_ExposesStructuredRuntimeErrorForWorkflowFailure(t *testing.T) {
	mcpManager := &deniedMCPManager{}
	executor := NewExecutor(NewRegistry(mcpManager), mcpManager, nil)
	skillItem := &Skill{
		Name:        "sandboxed_workflow",
		Description: "sandbox failure propagation",
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "step_denied",
			Name: "denied",
			Tool: "tool_denied",
		}}},
	}

	result, err := executor.Execute(context.Background(), skillItem, types.NewRequest("run"))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Equal(t, string(runtimeerrors.ErrAgentPermission), result.ErrorCode)
	assert.Equal(t, "sandbox", result.ErrorContext["policy"])
	require.Len(t, result.Observations, 1)
	assert.False(t, result.Observations[0].Success)
	assert.Contains(t, result.Observations[0].Error, "sandbox denied")
}

func TestExecutor_ExecuteWorkflow_ObservationIncludesMCPGovernanceMetrics(t *testing.T) {
	mcpManager := &governanceAwareMCPManager{}
	executor := NewExecutor(NewRegistry(mcpManager), mcpManager, nil)
	skillItem := &Skill{
		Name:        "governed_workflow",
		Description: "governance metrics",
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "step_governed",
			Name: "governed",
			Tool: "tool_governed",
		}}},
	}

	result, err := executor.Execute(context.Background(), skillItem, types.NewRequest("run"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Len(t, result.Observations, 1)

	observation := result.Observations[0]
	require.Equal(t, "remote-governed", observation.Metrics["mcp_name"])
	require.Equal(t, "trusted_remote", observation.Metrics["mcp_trust_level"])
	require.Equal(t, "remote_mcp", observation.Metrics["execution_mode"])
}
