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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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

// TestExecutor_BuildMainLoopPrompt_RendersInstructionsWithoutLLMCall 固化方案 A：
// 说明型技能的指令渲染为可直接注入主循环的文本，且不触发任何 LLM 调用（不
// 需要 LLM Runtime）。
func TestExecutor_BuildMainLoopPrompt_RendersInstructionsWithoutLLMCall(t *testing.T) {
	executor := NewExecutor(NewRegistry(nil), nil, nil)
	skillItem := &Skill{
		Name:         "skill-installer",
		Description:  "Helps install skills",
		SystemPrompt: "# Skill Installer\n\nFollow the install document.",
	}

	text, err := executor.BuildMainLoopPrompt(skillItem, types.NewRequest("在 ./aicli 安装 skill"))
	require.NoError(t, err)
	require.Contains(t, text, "skill-installer")
	require.Contains(t, text, "Helps install skills")
	require.Contains(t, text, "# Skill Installer")
	require.Contains(t, text, "在 ./aicli 安装 skill")
	// 主循环已自带 environment / shell / file 指引，注入文本不应重复这些锅炉板。
	require.NotContains(t, text, "Environment context")
	require.NotContains(t, text, "Shell guidance")

	_, err = executor.BuildMainLoopPrompt(nil, types.NewRequest("x"))
	require.Error(t, err)
}

// TestExecutor_ExecuteDefault_UsesConfiguredModelOverRuntimeDefault 回归
// “技能桥沿用 runtime 默认模型而不是会话模型”：共享 bootstrap 场景下运行
// 时默认模型可能是 claude-3-5-sonnet 之类的无关模型，请求会被路由到非会话
// provider 并返回 HTTP 500。设置 default model 后必须显式携带会话模型。
func TestExecutor_ExecuteDefault_UsesConfiguredModelOverRuntimeDefault(t *testing.T) {
	provider := &recordingLLMProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "recording-llm",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("recording-llm", provider))
	require.NoError(t, runtime.RegisterProviderAlias("session-model", "recording-llm"))

	executor := NewExecutor(NewRegistry(nil), nil, runtime)
	executor.SetDefaultModel("session-model")
	require.Equal(t, "session-model", executor.DefaultModel())

	skillItem := &Skill{
		Name:         "skill_runtime_model",
		Description:  "model binding skill",
		SystemPrompt: "Answer the request.",
		UserPrompt:   "Answer the request.",
	}
	result, err := executor.Execute(context.Background(), skillItem, types.NewRequest("run model test"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.NotNil(t, provider.lastRequest)
	assert.Equal(t, "session-model", provider.lastRequest.Model)

	// 清空后回到“沿用运行时默认模型”的旧行为，避免影响其它宿主。
	executor.SetDefaultModel("")
	assert.Equal(t, "", executor.DefaultModel())
}

func TestExecutor_ExecuteDefault_IncludesEnvironmentContextAndShellGuidance(t *testing.T) {
	provider := &recordingLLMProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "recording-llm",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("recording-llm", provider))

	executor := NewExecutor(NewRegistry(nil), nil, runtime)
	skillItem := &Skill{
		Name:         "skill_runtime_environment",
		Description:  "environment context skill",
		SystemPrompt: "Use the provided environment context.",
		UserPrompt:   "Answer the request.",
	}

	workspacePath := `E:\projects\ai\ai-agent-runtime`
	req := types.NewRequest("inspect environment")
	req.Context["workspace_path"] = workspacePath

	result, err := executor.Execute(context.Background(), skillItem, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.NotNil(t, provider.lastRequest)

	var sawEnvironmentContext bool
	var sawShellGuidance bool
	var sawRuntimeSummary bool
	for _, message := range provider.lastRequest.Messages {
		if message.Role != "system" {
			continue
		}
		if strings.Contains(message.Content, "Environment context:") &&
			strings.Contains(message.Content, "<environment_context>") &&
			strings.Contains(message.Content, "<cwd>"+workspacePath+"</cwd>") &&
			strings.Contains(message.Content, "<shell>") &&
			strings.Contains(message.Content, "<current_date>") &&
			strings.Contains(message.Content, "<timezone>") {
			sawEnvironmentContext = true
		}
		if strings.Contains(message.Content, "Shell guidance:") &&
			strings.Contains(message.Content, "Detected user shell:") {
			sawShellGuidance = true
		}
		if strings.Contains(message.Content, "Runtime context summary:") &&
			strings.Contains(message.Content, `"workspace_path":"E:\\projects\\ai\\ai-agent-runtime"`) {
			sawRuntimeSummary = true
		}
	}

	assert.True(t, sawEnvironmentContext, "expected environment context block in default executor request")
	assert.True(t, sawShellGuidance, "expected shell guidance in default executor request")
	assert.True(t, sawRuntimeSummary, "expected runtime context summary in default executor request")
}

func TestExecutor_ExecuteDefault_ReusesFrozenEnvironmentSnapshot(t *testing.T) {
	provider := &recordingLLMProvider{}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "recording-llm",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("recording-llm", provider))

	executor := NewExecutor(NewRegistry(nil), nil, runtime)
	skillItem := &Skill{
		Name:         "skill_frozen_environment",
		Description:  "frozen environment skill",
		SystemPrompt: "Use the frozen environment context.",
		UserPrompt:   "Answer the request.",
	}

	frozenBlock := "<environment_context>\n  <cwd>frozen-session-cwd</cwd>\n  <current_date>2099-01-01</current_date>\n</environment_context>"
	frozenCapability := "Frozen capability guidance marker."
	req := types.NewRequest("inspect frozen environment")
	req.Context["workspace_path"] = `E:\projects\live-path`
	req.Context[skillEnvironmentContextBlock] = frozenBlock
	req.Context[skillEnvironmentCapabilityGuidance] = frozenCapability
	req.Context[skillEnvironmentProbedAt] = "2099-01-01T00:00:00Z"
	req.Context["current_date"] = "2099-01-01"
	req.Context["timezone"] = "FROZEN"
	req.Context["os"] = "frozen-os"
	req.Context["shell"] = "frozen-shell"

	result, err := executor.Execute(context.Background(), skillItem, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.NotNil(t, provider.lastRequest)

	var sawFrozenEnvironment bool
	var sawFrozenCapability bool
	var sawRuntimeSummary bool
	for _, message := range provider.lastRequest.Messages {
		if message.Role != "system" {
			continue
		}
		if strings.Contains(message.Content, "Environment context:") {
			assert.Contains(t, message.Content, frozenBlock)
			assert.NotContains(t, message.Content, `E:\projects\live-path`)
			assert.Contains(t, message.Content, "2099-01-01")
			sawFrozenEnvironment = true
		}
		if strings.Contains(message.Content, "Shell guidance:") {
			assert.Contains(t, message.Content, frozenCapability)
			sawFrozenCapability = true
		}
		if strings.Contains(message.Content, "Runtime context summary:") {
			assert.Contains(t, message.Content, `"workspace_path":"E:\\projects\\live-path"`)
			assert.NotContains(t, message.Content, skillEnvironmentContextBlock)
			assert.NotContains(t, message.Content, `"current_date"`)
			assert.NotContains(t, message.Content, `"timezone"`)
			assert.NotContains(t, message.Content, `"os"`)
			assert.NotContains(t, message.Content, `"shell"`)
			sawRuntimeSummary = true
		}
	}

	assert.True(t, sawFrozenEnvironment, "expected frozen environment context block")
	assert.True(t, sawFrozenCapability, "expected frozen capability guidance in shell guidance")
	assert.True(t, sawRuntimeSummary, "expected runtime context summary without environment fact keys")
}

func TestBuildEnvironmentContextMessage_PrefersFrozenBlock(t *testing.T) {
	req := types.NewRequest("probe")
	req.Context["workspace_path"] = `E:\projects\live`
	req.Context[skillEnvironmentContextBlock] = "frozen-env-block"
	assert.Equal(t, "frozen-env-block", buildEnvironmentContextMessage(req))
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
	assert.Contains(t, provider.lastMessages[0].Content, "System instructions:\nAlways answer with USER_FIRST_OK")
	assert.Contains(t, provider.lastMessages[0].Content, "Environment context:")
	assert.Contains(t, provider.lastMessages[0].Content, "Shell guidance:")
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

// --- tool loop coverage -----------------------------------------------------

type toolLoopCall struct {
	MCPName  string
	ToolName string
	Args     map[string]interface{}
}

type toolLoopMCPManager struct {
	mu       sync.Mutex
	calls    []toolLoopCall
	outputs  map[string]string
	failures map[string]error
}

func (m *toolLoopMCPManager) FindTool(toolName string) (ToolInfo, error) {
	if err, failed := m.failures[toolName]; failed {
		return ToolInfo{}, err
	}
	if _, ok := m.outputs[toolName]; !ok {
		return ToolInfo{}, fmt.Errorf("tool '%s' not found", toolName)
	}
	return ToolInfo{Name: toolName, Description: toolName, MCPName: "loop-mcp", Enabled: true}, nil
}

func (m *toolLoopMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.mu.Lock()
	cloned := make(map[string]interface{}, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	m.calls = append(m.calls, toolLoopCall{MCPName: mcpName, ToolName: toolName, Args: cloned})
	m.mu.Unlock()

	if err, failed := m.failures[toolName]; failed {
		return nil, err
	}
	if output, ok := m.outputs[toolName]; ok {
		return output, nil
	}
	return "ok", nil
}

func (m *toolLoopMCPManager) ListTools() []ToolInfo {
	infos := make([]ToolInfo, 0, len(m.outputs))
	for name := range m.outputs {
		infos = append(infos, ToolInfo{Name: name, Description: name, MCPName: "loop-mcp", Enabled: true})
	}
	return infos
}

func (m *toolLoopMCPManager) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *toolLoopMCPManager) recordedCalls() []toolLoopCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]toolLoopCall(nil), m.calls...)
}

type scriptedLLMProvider struct {
	mu        sync.Mutex
	responses []llm.LLMResponse
	requests  []llm.LLMRequest
}

func (p *scriptedLLMProvider) Name() string { return "scripted-loop" }

func (p *scriptedLLMProvider) Call(_ context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req != nil {
		cloned := *req
		cloned.Messages = append([]types.Message(nil), req.Messages...)
		cloned.Tools = append([]types.ToolDefinition(nil), req.Tools...)
		p.requests = append(p.requests, cloned)
	}
	if len(p.responses) == 0 {
		return &llm.LLMResponse{Content: "SCRIPT_EXHAUSTED", Model: "scripted-loop"}, nil
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return &response, nil
}

func (p *scriptedLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	response, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: response.Content}
	close(ch)
	return ch, nil
}

func (p *scriptedLLMProvider) CountTokens(text string) int { return len(text) }

func (p *scriptedLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true}
}

func (p *scriptedLLMProvider) CheckHealth(context.Context) error { return nil }

func (p *scriptedLLMProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *scriptedLLMProvider) requestAt(index int) llm.LLMRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[index]
}

func newToolLoopExecutor(t *testing.T, mcp MCPManager, provider *scriptedLLMProvider) *Executor {
	t.Helper()
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel:   "scripted-loop",
		DefaultTimeout: 10 * time.Second,
		MaxRetries:     0,
	})
	require.NoError(t, runtime.RegisterProvider("scripted-loop", provider))
	return NewExecutor(NewRegistry(mcp), mcp, runtime)
}

func TestExecutor_ExecuteDefault_ToolLoopExecutesToolCalls(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"shell": "hi from shell"}}
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{
		{
			ToolCalls: []types.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Name: "shell",
				Args: map[string]interface{}{"command": "echo hi"},
			}},
			Model: "scripted-loop",
		},
		{Content: "INSTALL_DONE", Model: "scripted-loop"},
	}}
	executor := newToolLoopExecutor(t, mcp, provider)

	req := types.NewRequest("install it")
	req.Options = map[string]interface{}{"tool_loop": true}
	result, err := executor.Execute(context.Background(), &Skill{
		Name:         "skill-installer",
		SystemPrompt: "install skills",
		UserPrompt:   "install it",
	}, req)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "INSTALL_DONE", result.Output)
	require.Equal(t, 1, mcp.callCount())
	recorded := mcp.recordedCalls()[0]
	require.Equal(t, "shell", recorded.ToolName)
	require.Equal(t, "loop-mcp", recorded.MCPName)
	require.Equal(t, "echo hi", recorded.Args["command"])
	require.Len(t, result.Observations, 1)
	require.True(t, result.Observations[0].Success)

	require.Equal(t, 2, provider.requestCount())
	require.NotEmpty(t, provider.requestAt(0).Tools)
	secondMessages := provider.requestAt(1).Messages
	require.NotEmpty(t, secondMessages)
	lastMessage := secondMessages[len(secondMessages)-1]
	require.Equal(t, "tool", lastMessage.Role)
	require.Equal(t, "call_1", lastMessage.ToolCallID)
	require.Equal(t, "hi from shell", lastMessage.Content)
}

func TestExecutor_ModelMode_LetsModelChooseWorkflowProgram(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"bash": "E:/repo"}}
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{
		{
			ToolCalls: []types.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Name: "bash",
				Args: map[string]interface{}{"command": "pwd"},
			}},
			Model: "scripted-loop",
		},
		{Content: "当前目录是 E:/repo", Model: "scripted-loop"},
	}}
	executor := newToolLoopExecutor(t, mcp, provider)

	req := types.NewRequest("pwd")
	req.Options = map[string]interface{}{"execution_mode": "model"}
	result, err := executor.Execute(context.Background(), &Skill{
		Name:        "run_shell_command",
		Description: "Run a shell command through the managed bash MCP tool.",
		Tools:       []string{"bash"},
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "run_command",
			Name: "Run shell command",
			Tool: "bash",
			Args: map[string]interface{}{"command": "{{prompt}}"},
		}}},
	}, req)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "当前目录是 E:/repo", result.Output)

	// 模型驱动：workflow 不再被直执行，唯一一次程序调用来自模型发起的 tool_call。
	require.Equal(t, 1, mcp.callCount())
	require.Equal(t, "bash", mcp.recordedCalls()[0].ToolName)
	require.Equal(t, "pwd", mcp.recordedCalls()[0].Args["command"])

	// 模型读到了说明与程序清单，并且拿到了可调用工具面。
	firstRequest := provider.requestAt(0)
	require.NotEmpty(t, firstRequest.Tools)
	require.NotEmpty(t, firstRequest.Messages)
	firstMessage := firstRequest.Messages[0]
	require.Equal(t, "system", firstMessage.Role)
	require.Contains(t, firstMessage.Content, "Skill program guide")
	require.Contains(t, firstMessage.Content, "run_shell_command")
	require.Contains(t, firstMessage.Content, "bash")
	require.Contains(t, firstMessage.Content, "run_command")
}

func TestExecutor_AutoMode_WorkflowStillDirectExecutesWithoutLLM(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"bash": "PWD_OK"}}
	provider := &scriptedLLMProvider{}
	executor := newToolLoopExecutor(t, mcp, provider)

	result, err := executor.Execute(context.Background(), &Skill{
		Name:        "run_shell_command",
		Description: "Run a shell command through the managed bash MCP tool.",
		Tools:       []string{"bash"},
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "run_command",
			Name: "Run shell command",
			Tool: "bash",
			Args: map[string]interface{}{"command": "{{prompt}}"},
		}}},
	}, types.NewRequest("pwd"))

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "PWD_OK", result.Output)
	require.Equal(t, 1, mcp.callCount())
	require.Equal(t, "pwd", mcp.recordedCalls()[0].Args["command"])
	// auto 模式保持确定性直执行语义：零次 LLM 调用。
	require.Equal(t, 0, provider.requestCount())
}

func TestExecutor_ExecuteDefault_ToolLoopParsesDSMLFallback(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"shell": "doc content"}}
	dsmlContent := "先读取这份安装说明：\n\n" +
		"<" + dsmlMarker + " calls>\n" +
		"<" + dsmlMarker + " invoke name=\"shell\">\n" +
		"<" + dsmlMarker + " parameter name=\"command\" string=\"true\">curl -sSL https://example.com/AGENT_INSTALL.md</" + dsmlMarker + " parameter>\n" +
		"<" + dsmlMarker + " parameter name=\"timeout_sec\" string=\"false\">60</" + dsmlMarker + " parameter>\n" +
		"</" + dsmlMarker + " invoke>\n" +
		"</" + dsmlMarker + " calls>"
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{
		{Content: dsmlContent, Model: "scripted-loop"},
		{Content: "已按说明完成安装", Model: "scripted-loop"},
	}}
	executor := newToolLoopExecutor(t, mcp, provider)

	req := types.NewRequest("install it")
	req.Options = map[string]interface{}{"tool_loop": true}
	result, err := executor.Execute(context.Background(), &Skill{
		Name:         "skill-installer",
		SystemPrompt: "install skills",
		UserPrompt:   "install it",
	}, req)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "已按说明完成安装", result.Output)
	require.Equal(t, 1, mcp.callCount())
	recorded := mcp.recordedCalls()[0]
	require.Equal(t, "shell", recorded.ToolName)
	require.Equal(t, "curl -sSL https://example.com/AGENT_INSTALL.md", recorded.Args["command"])
	require.Equal(t, float64(60), recorded.Args["timeout_sec"])
	require.Len(t, result.Observations, 1)
	require.True(t, result.Observations[0].Success)

	require.Equal(t, 2, provider.requestCount())
	secondMessages := provider.requestAt(1).Messages
	require.NotEmpty(t, secondMessages)
	assistantMessage := secondMessages[len(secondMessages)-2]
	require.Equal(t, "assistant", assistantMessage.Role)
	require.NotContains(t, assistantMessage.Content, dsmlMarker)
	require.Len(t, assistantMessage.ToolCalls, 1)
	require.Equal(t, "shell", assistantMessage.ToolCalls[0].Name)
}

func TestExecutor_ExecuteDefault_WithoutToolLoopKeepsSingleShot(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"shell": "unused"}}
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{{Content: "PLAIN_TEXT", Model: "scripted-loop"}}}
	executor := newToolLoopExecutor(t, mcp, provider)

	result, err := executor.Execute(context.Background(), &Skill{
		Name:         "plain",
		SystemPrompt: "plain",
		UserPrompt:   "plain",
	}, types.NewRequest("plain"))

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "PLAIN_TEXT", result.Output)
	require.Equal(t, 1, provider.requestCount())
	require.Empty(t, provider.requestAt(0).Tools)
	require.Equal(t, 0, mcp.callCount())
}

func TestExecutor_ExecuteDefault_ToolMarkupWithoutToolsReportsError(t *testing.T) {
	dsmlContent := "<" + dsmlMarker + " calls>\n" +
		"<" + dsmlMarker + " invoke name=\"shell\">\n" +
		"<" + dsmlMarker + " parameter name=\"command\" string=\"true\">echo hi</" + dsmlMarker + " parameter>\n" +
		"</" + dsmlMarker + " invoke>\n" +
		"</" + dsmlMarker + " calls>"
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{{Content: dsmlContent, Model: "scripted-loop"}}}
	executor := newToolLoopExecutor(t, &toolLoopMCPManager{outputs: map[string]string{}}, provider)

	result, err := executor.Execute(context.Background(), &Skill{
		Name:         "no-tools",
		SystemPrompt: "no tools",
		UserPrompt:   "no tools",
	}, types.NewRequest("no tools"))

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "工具调用")
	require.Contains(t, result.Error, "shell")
	require.NotContains(t, result.Output, dsmlMarker)
}

func TestExecutor_ExecuteDefault_ToolLoopStopsAtStepLimit(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"shell": "still running"}}
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{
		{ToolCalls: []types.ToolCall{{ID: "call_1", Type: "function", Name: "shell", Args: map[string]interface{}{"command": "step 1"}}}},
		{ToolCalls: []types.ToolCall{{ID: "call_2", Type: "function", Name: "shell", Args: map[string]interface{}{"command": "step 2"}}}},
		{ToolCalls: []types.ToolCall{{ID: "call_3", Type: "function", Name: "shell", Args: map[string]interface{}{"command": "step 3"}}}},
	}}
	executor := newToolLoopExecutor(t, mcp, provider)

	req := types.NewRequest("loop forever")
	req.Options = map[string]interface{}{"tool_loop": true, "tool_loop_max_steps": 2}
	result, err := executor.Execute(context.Background(), &Skill{
		Name:         "looping",
		SystemPrompt: "loop",
		UserPrompt:   "loop",
	}, req)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 2, mcp.callCount())
	require.Equal(t, 2, provider.requestCount())
	require.Contains(t, result.Output, "步数上限")
}
