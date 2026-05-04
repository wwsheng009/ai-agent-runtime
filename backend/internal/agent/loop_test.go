package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// MockLLMProvider 模拟 LLM Provider 用于测试
type MockLLMProvider struct {
	name string
}

type SequenceLLMProvider struct {
	name              string
	responses         []*llm.LLMResponse
	callCount         int
	requests          []*llm.LLMRequest
	providerCaps      *llm.ModelCapabilities
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec
}

func (m *MockLLMProvider) Name() string {
	return m.name
}

func (m *MockLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	// 模拟简单的响应
	return &llm.LLMResponse{
		Content: "I'll help you with that.",
		Model:   "test-model",
		Usage: &types.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}, nil
}

func (m *MockLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (m *MockLLMProvider) CountTokens(text string) int {
	return len(text) / 4 // 简单估算
}

func (m *MockLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsVision:    false,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (m *MockLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (s *SequenceLLMProvider) Name() string {
	return s.name
}

func (s *SequenceLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	s.requests = append(s.requests, cloneLLMRequest(req))
	if s.callCount >= len(s.responses) {
		return &llm.LLMResponse{
			Content: "No more responses configured.",
			Model:   "test-model",
		}, nil
	}

	response := s.responses[s.callCount]
	s.callCount++
	return response, nil
}

func (s *SequenceLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (s *SequenceLLMProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (s *SequenceLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	if s.providerCaps != nil {
		return s.providerCaps
	}
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (s *SequenceLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

type RetrySequenceLLMProvider struct {
	name      string
	errs      []error
	response  *llm.LLMResponse
	callCount int
}

func (p *RetrySequenceLLMProvider) Name() string {
	return p.name
}

func (p *RetrySequenceLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCount++
	if index := p.callCount - 1; index < len(p.errs) && p.errs[index] != nil {
		return nil, p.errs[index]
	}
	if p.response != nil {
		return p.response, nil
	}
	return &llm.LLMResponse{
		Content: "retry provider default response",
		Model:   req.Model,
	}, nil
}

func (p *RetrySequenceLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *RetrySequenceLLMProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *RetrySequenceLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *RetrySequenceLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (s *SequenceLLMProvider) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	capability, ok := llm.ResolveModelCapabilitySpec(requestedModel, s.modelCapabilities)
	return requestedModel, capability, ok
}

func TestNewReActLoop(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			MaxSteps:     10,
			SystemPrompt: "You are a helpful assistant.",
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	config := &LoopReActConfig{
		MaxSteps:        5,
		EnableThought:   true,
		EnableToolCalls: true,
	}

	loop := NewReActLoop(agent, llmRuntime, config)

	if loop == nil {
		t.Fatal("expected loop to be created")
	}

	if loop.config.MaxSteps != 5 {
		t.Errorf("expected MaxSteps 5, got %d", loop.config.MaxSteps)
	}
}

func TestNewReActLoop_WithNilConfig(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			MaxSteps: 10,
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	loop := NewReActLoop(agent, llmRuntime, nil)

	if loop == nil {
		t.Fatal("expected loop to be created with default config")
	}

	if loop.config == nil {
		t.Fatal("expected default config to be set")
	}

	// 验证默认配置
	expectedDefaults := map[string]interface{}{
		"MaxSteps":        0,
		"EnableThought":   true,
		"EnableToolCalls": true,
		"Verbose":         false,
		"Temperature":     0.7,
		"StopOnSuccess":   true,
		"MaxIterations":   10,
	}

	if loop.config.MaxSteps != expectedDefaults["MaxSteps"].(int) {
		t.Errorf("expected default MaxSteps %d, got %d", expectedDefaults["MaxSteps"].(int), loop.config.MaxSteps)
	}
}

func TestReActLoop_RunWithSession_DoesNotLimitWhenMaxStepsIsNonPositive(t *testing.T) {
	session := newTestHistorySession("session-unlimited")

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     0,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "先读取目录。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_1",
						Name: "ls",
						Args: map[string]interface{}{"path": "."},
					},
				},
			},
			{
				Content: "已完成分析。",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        0,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.RunWithSession(context.Background(), "请分析当前目录。", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.False(t, result.LimitReached)
	require.Equal(t, "已完成分析。", result.Output)
	require.Len(t, provider.requests, 2)

	messages := session.GetMessages()
	require.Len(t, messages, 4)
	require.Equal(t, "assistant", messages[len(messages)-1].Role)
	require.Equal(t, "已完成分析。", messages[len(messages)-1].Content)
}

func TestReActLoop_RunWithSession_PropagatesStreamOptionToLLMRequest(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "streamed reply",
				Model:   "test-model",
				Usage: &types.TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
			},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.RegisterProvider("test-provider", provider)

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "test-provider",
			Model:        "test-model",
			SystemPrompt: "You are a helpful assistant.",
			Options: map[string]interface{}{
				"stream": true,
			},
		},
		state: AgentState{},
	}
	session := newTestHistorySession("session-stream")
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "hello", session)
	if err != nil {
		t.Fatalf("RunWithSession failed: %v", err)
	}
	if result == nil || result.Output != "streamed reply" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(provider.requests))
	}
	if !provider.requests[0].Stream {
		t.Fatalf("expected stream=true on provider request, got %#v", provider.requests[0])
	}
}

func TestReActLoop_RunWithSession_ForcesStreamForImageGenerationCapability(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "image reply",
				Model:   "test-model",
				Usage: &types.TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
			},
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"test-model": {
				InputModalities: []string{"text", "image"},
				NativeTools: agentconfig.NativeToolCapabilities{
					ImageGeneration: true,
				},
			},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "test-provider",
			Model:        "test-model",
			SystemPrompt: "You are a helpful assistant.",
		},
		state: AgentState{},
	}
	session := newTestHistorySession("session-image-stream")
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "draw a square", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "image reply", result.Output)
	require.Len(t, provider.requests, 1)
	if !provider.requests[0].Stream {
		t.Fatalf("expected stream=true on provider request for image-generation capable model, got %#v", provider.requests[0])
	}
}

func TestReActLoop_RunWithSession_AddsGeneratedImageOutputDirToLLMMetadata(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "saved",
				Model:   "test-model",
				Usage: &types.TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
			},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	artifactStorePath := filepath.Join(t.TempDir(), "runtime", "artifacts.sqlite")
	agent := &Agent{
		config: &Config{
			Name:              "test-agent",
			Provider:          "test-provider",
			Model:             "test-model",
			ArtifactStorePath: artifactStorePath,
		},
		state: AgentState{},
	}
	store := agent.GetArtifactStore()
	require.NotNil(t, store)
	defer func() {
		_ = store.Close()
	}()
	session := newTestHistorySession("session-images")
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "hello", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "saved", result.Output)
	require.Len(t, provider.requests, 1)

	got, ok := provider.requests[0].Metadata[llm.MetadataKeyGeneratedImageOutputDir].(string)
	require.True(t, ok)
	require.Equal(t, filepath.Join(filepath.Dir(artifactStorePath), "generated-images", "session-images"), got)
}

func TestReActLoop_RunWithSession_DoesNotEmitDuplicateReasoningAfterStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先确认问题。"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hello!"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := llm.NewProvider(&llm.ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "gpt-4o-mini",
			Options: map[string]interface{}{
				"stream": true,
			},
		},
		state: AgentState{},
	}

	bus := runtimeevents.NewBus()
	var reasoningEvents []runtimeevents.Event
	bus.Subscribe("assistant.reasoning", func(event runtimeevents.Event) {
		reasoningEvents = append(reasoningEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "hello", newTestHistorySession("session-stream-reasoning"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "Hello!", result.Output)
	require.Len(t, reasoningEvents, 1)

	block := types.ReasoningBlockFromMap(reasoningEvents[0].Payload["reasoning"])
	require.NotNil(t, block)
	require.Equal(t, "stream_delta", block.Format)
	require.Equal(t, "先确认问题。", block.DisplayText())
}

func TestReActLoop_RunWithSession_EmitsLLMRetryRuntimeEvent(t *testing.T) {
	provider := &RetrySequenceLLMProvider{
		name: "test-provider",
		errs: []error{
			fmt.Errorf("HTTP 429: {\"error\":{\"message\":\"rate limit reached\"}}"),
		},
		response: &llm.LLMResponse{
			Content: "已恢复。",
			Model:   "test-model",
			Usage: &types.TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      1,
		RetryTuning: llm.RetryTuning{
			BaseDelay: time.Millisecond,
		},
	})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "test-model",
		},
		state: AgentState{},
	}

	bus := runtimeevents.NewBus()
	var retryEvents []runtimeevents.Event
	bus.Subscribe("llm.retry", func(event runtimeevents.Event) {
		retryEvents = append(retryEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "hello", newTestHistorySession("session-retry"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "已恢复。", result.Output)
	require.Len(t, retryEvents, 1)
	require.Equal(t, 2, provider.callCount)

	retryEvent := retryEvents[0]
	require.NotEmpty(t, retryEvent.TraceID)
	assert.Equal(t, "test-agent", retryEvent.AgentName)
	assert.Equal(t, "session-retry", retryEvent.SessionID)
	assert.Equal(t, retryEvent.TraceID, retryEvent.Payload["trace_id"])
	assert.EqualValues(t, 1, retryEvent.Payload["step"])
	assert.Equal(t, "llm_runtime", retryEvent.Payload["source"])
	assert.Equal(t, "test-provider", retryEvent.Payload["provider"])
	assert.Equal(t, "test-model", retryEvent.Payload["model"])
	assert.EqualValues(t, 1, retryEvent.Payload["attempt"])
	assert.EqualValues(t, 2, retryEvent.Payload["max_attempts"])
	assert.Equal(t, "rate_limit", retryEvent.Payload["retry_reason"])
	assert.EqualValues(t, 1, retryEvent.Payload["retry_delay_ms"])
	assert.Contains(t, retryEvent.Payload["error"], "HTTP 429")
}

func TestReActLoop_RunWithSession_PreservesExplicitEmptyReasoningContentMetadata(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "当前只有一个配置文件发生了修改。",
				Model:   "test-model",
				Metadata: map[string]interface{}{
					"reasoning_content": "",
				},
			},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "test-model",
			Options: map[string]interface{}{
				"stream": true,
			},
		},
		state: AgentState{},
	}

	session := newTestHistorySession("session-empty-reasoning")
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "git status", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "当前只有一个配置文件发生了修改。", result.Output)

	messages := session.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "assistant", messages[1].Role)
	got, exists := messages[1].Metadata["reasoning_content"]
	require.True(t, exists, "expected explicit empty reasoning_content metadata to survive")
	require.Equal(t, "", got)
}

func TestReActLoop_Run_WithoutAgent(t *testing.T) {
	config := &LoopReActConfig{
		MaxSteps:        5,
		EnableThought:   true,
		EnableToolCalls: true,
	}

	loop := ReActLoop{
		agent:      nil,
		llmRuntime: llm.NewLLMRuntime(nil),
		config:     config,
	}

	ctx := context.Background()
	result, err := loop.Run(ctx, "test prompt")

	if err == nil {
		t.Error("expected error for nil agent, got nil")
	}

	if result != nil {
		t.Error("expected nil result for error case")
	}
}

func TestReActLoop_Run_WithoutLLMRuntime(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			MaxSteps: 10,
		},
	}

	config := &LoopReActConfig{
		MaxSteps:        5,
		EnableThought:   true,
		EnableToolCalls: true,
	}

	loop := ReActLoop{
		agent:      agent,
		llmRuntime: nil,
		config:     config,
	}

	ctx := context.Background()
	result, err := loop.Run(ctx, "test prompt")

	if err == nil {
		t.Error("expected error for nil LLM runtime, got nil")
	}

	if result != nil {
		t.Error("expected nil result for error case")
	}
}

func TestReActLoop_Run_BasicExecution(t *testing.T) {
	// 创建 Agent
	agent := &Agent{
		config: &Config{
			Name:           "test-agent",
			MaxSteps:       5,
			SystemPrompt:   "You are a helpful assistant.",
			EnablePlanning: false,
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	// 创建 LLM Runtime
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	// 创建 ReAct Loop
	config := &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: false, // 禁用工具调用简化测试
		MaxIterations:   3,
	}

	loop := NewReActLoop(agent, llmRuntime, config)

	ctx := context.Background()
	prompt := "What is the capital of France?"

	result, err := loop.Run(ctx, prompt)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// 验证结果
	if result.State.CurrentStep < 0 {
		t.Errorf("expected non-negative step count, got %d", result.State.CurrentStep)
	}

	// Agent should not be running after execution
	if agent.IsRunning() {
		t.Error("expected agent to not be running after execution")
	}
}

func TestReActLoop_Run_WithMaxSteps(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			MaxSteps:     10,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	config := &LoopReActConfig{
		MaxSteps:        2,
		EnableToolCalls: false,
		MaxIterations:   2,
	}

	loop := NewReActLoop(agent, llmRuntime, config)

	ctx := context.Background()
	result, err := loop.Run(ctx, "test prompt")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// 应该在 MaxSteps 步内停止
	if result.State.CurrentStep > config.MaxSteps {
		t.Errorf("expected at most %d steps, got %d", config.MaxSteps, result.State.CurrentStep)
	}
}

func TestReActLoop_RunWithSession_EmitsLimitNoticeAndPersistsAssistantMessage(t *testing.T) {
	session := newTestHistorySession("session-step-limit")

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     1,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "先读取目录。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_limit_1",
						Name: "ls",
						Args: map[string]interface{}{"path": "."},
					},
				},
			},
			{
				Content: "这条回复不应出现。",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.RunWithSession(context.Background(), "请分析当前目录。", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.True(t, result.LimitReached)
	require.Equal(t, 1, result.StepLimit)
	require.Contains(t, result.Output, "maxSteps=1")
	require.Len(t, provider.requests, 1)

	messages := session.GetMessages()
	require.Len(t, messages, 4)
	require.Equal(t, "assistant", messages[len(messages)-1].Role)
	require.Equal(t, result.Output, messages[len(messages)-1].Content)
}

func TestReActLoop_Run_WithTimeout(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			MaxSteps:     100,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	config := &LoopReActConfig{
		MaxSteps:        10,
		EnableToolCalls: false,
		MaxIterations:   10,
	}

	loop := NewReActLoop(agent, llmRuntime, config)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := loop.Run(ctx, "test prompt")

	// 可能因为超时而失败或完成
	if err == context.DeadlineExceeded || err == context.Canceled {
		// 预期的超时
		t.Logf("Execution timed out as expected: %v", err)
		return
	}

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != nil {
		t.Logf("Completed %d steps before timeout", result.State.CurrentStep)
	}
}

func TestAgent_IsRunning_AfterLoop(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			MaxSteps:     5,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	// 手动设置 running 状态
	agent.SetRunning(true)

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	config := &LoopReActConfig{
		MaxSteps:        2,
		EnableToolCalls: false,
		MaxIterations:   2,
	}

	loop := NewReActLoop(agent, llmRuntime, config)

	ctx := context.Background()
	_, err := loop.Run(ctx, "test prompt")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// 执行后 Agent 应该停止运行
	if agent.IsRunning() {
		t.Error("expected agent to not be running after loop execution")
	}
}

func TestReActLoop_Run_UsesOutputGatewayForToolResults(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockSequenceMCPManager{
			output: strings.Join([]string{
				"header",
				"unique-stack-trace",
				"frame 1",
				"frame 2",
				"frame 3",
				"frame 4",
			}, "\n"),
		},
		artifacts:  store,
		contextMgr: contextmgr.NewManager(contextmgr.DefaultBudget(), store),
		outputGate: output.NewGateway(store, output.NewTextReducer(60, 2)),
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will inspect the logs first.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "read_logs",
						Args: map[string]interface{}{"path": "logs/app.log"},
					},
				},
			},
			{
				Content: "The stack trace points to the parser failure.",
				Model:   "test-model",
			},
		},
	}
	llmRuntime.RegisterProvider("test-provider", provider)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "Find the failing stack trace.")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if result.TraceID == "" {
		t.Fatal("expected trace id on react result")
	}
	if provider.callCount != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", provider.callCount)
	}
	if len(result.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(result.Observations))
	}

	refs, ok := result.Observations[0].GetMetric("artifact_refs")
	if !ok {
		t.Fatal("expected artifact refs on observation")
	}
	artifactRefs, ok := refs.([]string)
	if !ok || len(artifactRefs) != 1 {
		t.Fatalf("expected one artifact ref, got %#v", refs)
	}

	record, err := store.Get(context.Background(), artifactRefs[0])
	if err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if record == nil {
		t.Fatal("expected stored artifact record")
	}
	if !strings.Contains(record.Content, "unique-stack-trace") {
		t.Fatalf("expected full raw output to be stored, got %q", record.Content)
	}
	if traceID, ok := record.Metadata["trace_id"].(string); !ok || traceID == "" {
		t.Fatalf("expected trace_id in artifact metadata, got %#v", record.Metadata["trace_id"])
	}

	outputText, ok := result.Observations[0].Output.(string)
	if !ok {
		t.Fatalf("expected observation output to be string, got %T", result.Observations[0].Output)
	}
	if strings.Contains(outputText, "frame 4") {
		t.Fatalf("expected inline output to be reduced, got %q", outputText)
	}
	if strings.Contains(outputText, "artifact_refs:") {
		t.Fatalf("expected observation output to omit artifact refs, got %q", outputText)
	}
}

func TestReActLoop_Run_ContextManagerRecallsArtifacts(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockSequenceMCPManager{
			output: "header\nunique-stack-trace\nframe 1\nframe 2\nframe 3\nframe 4",
		},
		artifacts: store,
		contextMgr: contextmgr.NewManager(contextmgr.Budget{
			MaxPromptTokens:     8000,
			MaxMessages:         12,
			KeepRecentMessages:  6,
			MaxRecallResults:    2,
			MaxObservationItems: 3,
		}, store),
		outputGate: output.NewGateway(store, output.NewTextReducer(60, 2)),
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will inspect the logs first.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "read_logs",
						Args: map[string]interface{}{"path": "logs/app.log"},
					},
				},
			},
			{
				Content: "The recalled artifact confirms the failing stack trace.",
				Model:   "test-model",
			},
		},
	}
	llmRuntime.RegisterProvider("test-provider", provider)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	_, err = loop.Run(context.Background(), "Find the error stack trace.")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected at least 2 LLM requests, got %d", len(provider.requests))
	}

	var foundRecall bool
	for _, message := range provider.requests[1].Messages {
		if strings.Contains(message.Content, "Relevant recalled artifacts:") &&
			strings.Contains(message.Content, "unique-stack-trace") {
			foundRecall = true
			break
		}
	}
	if !foundRecall {
		t.Fatal("expected second LLM request to include recalled artifact preview")
	}
}

func TestReActLoop_Run_PromptBudgetCompactsActiveTurnReplayBeforeThirdRequest(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "先查看一次日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
			{
				Content: "继续查看最新日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
			{
				Content: "已完成分析。",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		MaxSteps:         3,
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
		Options: map[string]interface{}{
			"context_max_prompt_tokens":    680,
			"context_max_messages":         16,
			"context_keep_recent_messages": 8,
		},
	}, &MockSequenceMCPManager{output: "LOG " + large}, llmRuntime)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "继续处理")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 3)

	foundCompaction := false
	for _, message := range provider.requests[2].Messages {
		if message.Metadata.GetBool("active_turn_compaction", false) {
			foundCompaction = true
			break
		}
	}
	require.True(t, foundCompaction, "expected third request to include active-turn compaction")
	rawPreflight, ok := provider.requests[2].Metadata["context_preflight"]
	require.True(t, ok, "expected prompt preflight metadata on prompt-only compaction")
	preflight, ok := rawPreflight.(map[string]interface{})
	require.True(t, ok, "expected context_preflight metadata map, got %T", rawPreflight)
	require.Equal(t, true, preflight["active_turn_prompt_only"])
	require.Equal(t, true, preflight["active_turn_compacted"])
}

func TestReActLoop_RunWithSession_PromptOnlyActiveTurnCompactionDoesNotPersist(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "先查看一次日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
			{
				Content: "继续查看最新日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
			{
				Content: "已完成分析。",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		MaxSteps:         3,
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
		Options: map[string]interface{}{
			"context_max_prompt_tokens":    680,
			"context_max_messages":         16,
			"context_keep_recent_messages": 8,
		},
	}, &MockSequenceMCPManager{output: "LOG " + large}, llmRuntime)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	session := newTestHistorySession("session-prompt-only-compaction")

	result, err := loop.RunWithSession(context.Background(), "继续处理", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 3)

	foundPromptOnlyCompaction := false
	for _, message := range provider.requests[2].Messages {
		if message.Metadata.GetBool("active_turn_compaction", false) {
			foundPromptOnlyCompaction = true
			break
		}
	}
	require.True(t, foundPromptOnlyCompaction, "expected provider prompt view to include active-turn compaction")

	messages := session.GetMessages()
	require.Len(t, messages, 6)
	for _, message := range messages {
		require.False(t, message.Metadata.GetBool("active_turn_compaction", false), "did not expect prompt-only compaction in persisted history: %#v", messages)
	}
	require.Equal(t, "tool", messages[2].Role)
	require.Equal(t, "tool", messages[4].Role)
	require.Contains(t, messages[2].Content, "LOG ")
	require.Contains(t, messages[4].Content, "LOG ")
	require.Equal(t, "已完成分析。", messages[5].Content)
}

func TestReActLoop_EnforcePromptPreflight_CompactsActiveTurnReplayByTokenBudget(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{name: "test-provider"}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		Options: map[string]interface{}{
			"context_max_prompt_tokens":    680,
			"context_max_messages":         16,
			"context_keep_recent_messages": 8,
		},
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{})

	messages := []types.Message{
		*types.NewUserMessage("继续处理"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", "LOG "+large),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_2", Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "LOG "+large),
	}

	compacted, metadata, err := loop.enforcePromptPreflight("trace-1", "session-1", 2, messages, 0)
	require.NoError(t, err)
	require.Len(t, compacted, 4)
	require.NotNil(t, metadata)
	require.Equal(t, true, metadata["active_turn_compacted"])
	require.Equal(t, "context_max_prompt_tokens", metadata["budget_source"])
	require.Equal(t, "test-provider", metadata["resolved_provider"])
	require.Equal(t, "test-model", metadata["resolved_model"])
}

func TestReActLoop_Run_PromptPreflightFailsWhenReplayCannotBeCompactedFurther(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "先查看日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
			{
				Content: "这条响应不应被请求到。",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		MaxSteps:         3,
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
		Options: map[string]interface{}{
			"context_max_prompt_tokens":    250,
			"context_max_messages":         16,
			"context_keep_recent_messages": 8,
		},
	}, &MockSequenceMCPManager{output: "LOG " + large}, llmRuntime)
	bus := runtimeevents.NewBus()
	var failedEvents []runtimeevents.Event
	bus.Subscribe("context.preflight.failed", func(event runtimeevents.Event) {
		failedEvents = append(failedEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "继续处理")
	require.Error(t, err)
	require.NotNil(t, result)
	require.Contains(t, err.Error(), "prompt preflight budget exceeded")
	preflightErr, ok := AsPromptPreflightError(err)
	require.True(t, ok, "expected prompt preflight error type")
	require.Equal(t, "active_turn_not_compactable", preflightErr.Code)
	require.Equal(t, 250, preflightErr.PromptBudget)
	require.Equal(t, false, preflightErr.ActiveTurnCompacted)
	require.Len(t, provider.requests, 1)
	require.NotEmpty(t, failedEvents)
	payload := failedEvents[len(failedEvents)-1].Payload
	require.Equal(t, "active-turn replay cannot be compacted further", payload["failure_reason"])
	require.Equal(t, "active_turn_not_compactable", payload["failure_reason_code"])
	require.Equal(t, false, payload["can_retry_after_compaction"])
	require.NotNil(t, payload["active_turn_message_count"])
	require.NotNil(t, payload["latest_replay_block_message_count"])
}

func TestReActLoop_RunWithSession_AutoCompactionRecoveryContinuesAfterPromptPreflightFailure(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "先查看第一段日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app-1.log"}},
				},
			},
			{
				Content: "再查看第二段日志。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app-2.log"}},
				},
			},
			{
				Content: "压缩后整理上下文。",
				Model:   "test-model",
			},
			{
				Content: "恢复后的最终回答。",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		MaxSteps:         4,
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
		Options: map[string]interface{}{
			"context_max_prompt_tokens":    550,
			"context_max_messages":         16,
			"context_keep_recent_messages": 8,
		},
	}, &MockSequenceMCPManager{output: "LOG " + large}, llmRuntime)

	bus := runtimeevents.NewBus()
	var compactionEvents []runtimeevents.Event
	bus.Subscribe("session_compact_started", func(event runtimeevents.Event) {
		compactionEvents = append(compactionEvents, event)
	})
	bus.Subscribe("session_compact_completed", func(event runtimeevents.Event) {
		compactionEvents = append(compactionEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	session := newTestHistorySession("session-preflight-recovery")

	result, err := loop.RunWithSession(context.Background(), "继续处理", session)
	require.NotNil(t, result)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "恢复后的最终回答。", result.Output)
	require.Len(t, provider.requests, 4)
	require.Len(t, compactionEvents, 2)
	require.Equal(t, "session_compact_started", compactionEvents[0].Type)
	require.Equal(t, "session_compact_completed", compactionEvents[1].Type)
	require.Equal(t, "session-preflight-recovery", compactionEvents[0].SessionID)
	require.Equal(t, "session-preflight-recovery", compactionEvents[1].SessionID)
	require.NotNil(t, compactionEvents[1].Payload["message_count_after"])

	messages := session.GetMessages()
	require.NotEmpty(t, messages)

	foundCompaction := false
	for _, message := range messages {
		if message.Metadata.GetString("context_stage", "") == "compaction" {
			foundCompaction = true
			require.Contains(t, message.Content, "Compacted context from earlier turns:")
			break
		}
	}
	require.True(t, foundCompaction, "expected session-level compacted summary to be persisted to session history")
}

func TestResolvePromptPreflightBudget_UsesModelCapabilityThresholdWhenMoreConservative(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &SequenceLLMProvider{
		name: "test-provider",
		providerCaps: &llm.ModelCapabilities{
			MaxContextTokens: 200000,
			MaxOutputTokens:  8192,
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"test-model": {
				MaxContextTokens: 10000,
				AutoCompactRatio: 0.75,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("test-model", "test-provider"))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "test-model",
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.Budget{
				MaxPromptTokens: 12000,
			},
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, 0)
	require.Equal(t, 7500, budget.PromptBudget)
	require.Equal(t, "model_capability_context_ratio", budget.BudgetSource)
	require.Equal(t, 10000, budget.ModelCapabilityMaxContextTokens)
	require.InDelta(t, 0.75, budget.ModelCapabilityAutoCompactRatio, 0.001)
	require.Equal(t, 200000, budget.ProviderContextLimit)
	require.Equal(t, 8192, budget.ProviderOutputLimit)
}

func TestResolvePromptPreflightBudget_DoesNotLetDefaultBudgetOverrideKnownCapability(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "modelscope",
		DefaultModel:    "deepseek-ai/DeepSeek-V4-Flash",
	})
	provider := &SequenceLLMProvider{
		name: "modelscope",
		providerCaps: &llm.ModelCapabilities{
			MaxContextTokens: 270000,
			MaxOutputTokens:  8192,
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"*": {
				MaxContextTokens:      270000,
				AutoCompactTokenLimit: 200000,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("modelscope", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("deepseek-ai/DeepSeek-V4-Flash", "modelscope"))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "modelscope",
			Model:    "deepseek-ai/DeepSeek-V4-Flash",
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.DefaultBudget(),
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, 0)
	require.Equal(t, 200000, budget.PromptBudget)
	require.Equal(t, "model_capability_auto_compact_token_limit", budget.BudgetSource)
	require.Equal(t, 200000, budget.ModelCapabilityAutoCompactTokenLimit)
	require.NotContains(t, budget.BudgetCandidates, "default_context_max_prompt_tokens")
}

func TestResolvePromptPreflightBudget_ExplicitContextBudgetStillConstrainsCapability(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &SequenceLLMProvider{
		name: "test-provider",
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"test-model": {
				MaxContextTokens:      270000,
				AutoCompactTokenLimit: 200000,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("test-model", "test-provider"))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "test-model",
			Options: map[string]interface{}{
				"context_max_prompt_tokens": 12000,
			},
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.DefaultBudget(),
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, 0)
	require.Equal(t, 12000, budget.PromptBudget)
	require.Equal(t, "context_max_prompt_tokens", budget.BudgetSource)
	require.Equal(t, 200000, budget.ModelCapabilityAutoCompactTokenLimit)
}

func TestResolvePromptPreflightBudget_FallsBackToProviderContextLimitWhenCapabilityMissing(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &SequenceLLMProvider{
		name: "test-provider",
		providerCaps: &llm.ModelCapabilities{
			MaxContextTokens: 8000,
			MaxOutputTokens:  2048,
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("test-model", "test-provider"))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "test-model",
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.Budget{
				MaxPromptTokens: 12000,
			},
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, 0)
	require.Equal(t, 7200, budget.PromptBudget)
	require.Equal(t, "provider_context_limit_default_ratio", budget.BudgetSource)
	require.Equal(t, 8000, budget.ProviderContextLimit)
	require.Equal(t, 2048, budget.ProviderOutputLimit)
}

func TestReActLoop_Run_EmptyTerminalAssistantResponseFails(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     2,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "Say something.")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Equal(t, emptyTerminalAssistantResponseError, result.Error)
}

func TestReActLoop_Run_MutationHintsTriggerCheckpoint(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "note.txt")
	require.NoError(t, os.WriteFile(targetPath, []byte("before"), 0o644))

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     2,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMutatingMCPManager{path: targetPath, output: "ok"},
		artifacts:   store,
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will run a command.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "execute_shell_command",
						Args: map[string]interface{}{
							"command":       "echo updated",
							"mutated_paths": []string{targetPath},
						},
					},
				},
			},
			{
				Content: "Done.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	sessionID := "session_mutation_hint"
	result, err := loop.run(context.Background(), "Update the file via shell.", loopRunOptions{
		TraceID:       "trace_mutation_hint",
		SessionID:     sessionID,
		IncludePrompt: true,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	checkpoint, err := store.LatestCheckpoint(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	assert.Equal(t, "tool:execute_shell_command", checkpoint.Reason)

	rawFiles, ok := checkpoint.Metadata["files"].([]interface{})
	require.True(t, ok)
	found := false
	for _, item := range rawFiles {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if entry["path"] == targetPath {
			found = true
			break
		}
	}
	assert.True(t, found, "expected checkpoint metadata to include mutated path")
}

func TestReActLoop_Run_ShellLikeToolTriggersCheckpointWithoutMutationHints(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "sample.txt")
	require.NoError(t, os.WriteFile(targetPath, []byte("before"), 0o644))

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     2,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMutatingMCPManager{path: targetPath, output: "ok"},
		artifacts:   store,
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will run a command.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "execute_shell_command",
						Args: map[string]interface{}{
							"command": "echo updated",
							"cwd":     tempDir,
						},
					},
				},
			},
			{
				Content: "Done.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	sessionID := "session_shell_fallback"
	result, err := loop.run(context.Background(), "Update the file via shell.", loopRunOptions{
		TraceID:       "trace_shell_fallback",
		SessionID:     sessionID,
		IncludePrompt: true,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	checkpoint, err := store.LatestCheckpoint(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	assert.Equal(t, "tool:execute_shell_command", checkpoint.Reason)

	rawFiles, ok := checkpoint.Metadata["files"].([]interface{})
	require.True(t, ok)
	found := false
	for _, item := range rawFiles {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if entry["path"] == filepath.Clean(targetPath) {
			found = true
			assert.Equal(t, "before", entry["before"])
			assert.Equal(t, "after", entry["after"])
			break
		}
	}
	assert.True(t, found, "expected checkpoint metadata to include shell-mutated file from cwd fallback")
}

func TestReActLoop_Run_AggregatesUsageAcrossSteps(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Model:            "test-provider",
			MaxSteps:         3,
			DefaultMaxTokens: 256,
			SystemPrompt:     "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockSequenceMCPManager{
			output: "ok",
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will inspect the logs.",
				Model:   "test-model",
				Usage:   &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
			{
				Content: "The logs show the failure.",
				Model:   "test-model",
				Usage:   &types.TokenUsage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12},
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "Find the issue in the logs.")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 18, result.Usage.PromptTokens)
	assert.Equal(t, 9, result.Usage.CompletionTokens)
	assert.Equal(t, 27, result.Usage.TotalTokens)
}

func TestReActLoop_Run_StopsWhenBudgetExceeded(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Model:            "test-provider",
			MaxSteps:         3,
			DefaultMaxTokens: 256,
			SystemPrompt:     "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockSequenceMCPManager{
			output: "ok",
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will inspect the logs.",
				Model:   "test-model",
				Usage:   &types.TokenUsage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12},
				ToolCalls: []types.ToolCall{
					{Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}},
				},
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.run(context.Background(), "Find the issue in the logs.", loopRunOptions{
		TraceID:       "trace_budget",
		IncludePrompt: true,
		BudgetTokens:  10,
	})
	require.Error(t, err)
	require.False(t, result.Success)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 0, result.Usage.TotalTokens)
	assert.Contains(t, result.Error, "prompt preflight budget exceeded")
}

func TestReActLoop_RunWithSession_PersistsHistoryAcrossRuns(t *testing.T) {
	session := newTestHistorySession("session-user-1")

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     2,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{Content: "First answer.", Model: "test-model"},
			{Content: "Second answer.", Model: "test-model"},
		},
	}
	llmRuntime.RegisterProvider("test-provider", provider)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	if _, err := loop.RunWithSession(context.Background(), "First question?", session); err != nil {
		t.Fatalf("first session-backed run failed: %v", err)
	}
	if got := len(session.GetMessages()); got != 2 {
		t.Fatalf("expected 2 persisted messages after first run, got %d", got)
	}
	if session.GetMessages()[1].Content != "First answer." {
		t.Fatalf("expected persisted assistant answer, got %q", session.GetMessages()[1].Content)
	}

	if _, err := loop.RunWithSession(context.Background(), "Second question?", session); err != nil {
		t.Fatalf("second session-backed run failed: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected 2 provider requests, got %d", len(provider.requests))
	}

	secondRequest := provider.requests[1]
	var sawPreviousAssistant bool
	for _, message := range secondRequest.Messages {
		if message.Role == "assistant" && message.Content == "First answer." {
			sawPreviousAssistant = true
			break
		}
	}
	if !sawPreviousAssistant {
		t.Fatal("expected second run to include persisted assistant history")
	}
	if got := len(session.GetMessages()); got != 4 {
		t.Fatalf("expected 4 persisted messages after second run, got %d", got)
	}
}

func TestReActLoop_Run_SpawnSubagentsUsesStructuredReports(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     4,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	agent.SetSubagentScheduler(NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 2,
		MaxDepth:      1,
	}))

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will delegate the log analysis.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "spawn_subagents",
						Args: map[string]interface{}{
							"agents": []interface{}{
								map[string]interface{}{
									"id":              "child-1",
									"goal":            "Inspect the latest logs and report the root cause.",
									"tools_whitelist": []interface{}{},
									"read_only":       true,
								},
							},
						},
					},
				},
			},
			{
				Content: "The logs point to a parser panic in the request path.",
				Model:   "test-model",
			},
			{
				Content: "I combined the child report into the final answer.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "Find the root cause from the logs.")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected 3 model requests (parent, child, parent), got %d", len(provider.requests))
	}
	if result.Output != "I combined the child report into the final answer." {
		for index, request := range provider.requests {
			t.Logf("request[%d] messages=%d", index, len(request.Messages))
			for msgIndex, message := range request.Messages {
				t.Logf("request[%d].messages[%d]=role=%s content=%q", index, msgIndex, message.Role, message.Content)
			}
		}
		t.Fatalf("unexpected final output: %q", result.Output)
	}

	parentAfterChild := provider.requests[2]
	var sawStructuredReport bool
	for _, message := range parentAfterChild.Messages {
		if message.Role == "tool" &&
			strings.Contains(message.Content, "Subagent reports:") &&
			strings.Contains(message.Content, "parser panic") {
			sawStructuredReport = true
			break
		}
	}
	if !sawStructuredReport {
		t.Fatal("expected parent to receive structured subagent report in tool_result history")
	}
}

func TestReActLoop_Run_SpawnSubagentsChildUsesPromptBuilder(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     4,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	agent.SetSubagentScheduler(NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 2,
		MaxDepth:      1,
	}))

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
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
									"id":              "child-1",
									"role":            "researcher",
									"goal":            "Inspect the logs and summarize the error.",
									"tools_whitelist": []interface{}{"read_logs"},
									"read_only":       true,
									"timeout":         15.0,
								},
							},
						},
					},
				},
			},
			{Content: "Child summary.", Model: "test-model"},
			{Content: "Parent final.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	_, err := loop.Run(context.Background(), "Find the root cause.")
	require.NoError(t, err)
	require.Len(t, provider.requests, 3)

	childRequest := provider.requests[1]
	require.NotEmpty(t, childRequest.Messages)
	assert.Equal(t, "system", childRequest.Messages[0].Role)
	assert.Contains(t, childRequest.Messages[0].Content, "read-only subagent")
	assert.Contains(t, childRequest.Messages[0].Content, "Subagent role: researcher")
	assert.Contains(t, childRequest.Messages[0].Content, "Allowed tools: read_logs.")
	assert.Contains(t, childRequest.Messages[0].Content, "Assigned goal: Inspect the logs and summarize the error.")
}

func TestSubagentScheduler_RunChildren_AppliesBudgetAndSessionIsolation(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Model:            "test-provider",
			MaxSteps:         3,
			DefaultMaxTokens: 512,
			SystemPrompt:     "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	bus := runtimeevents.NewBus()
	var startedSessionID string
	var completedUsageTotal float64
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startedSessionID = event.SessionID
	})
	bus.Subscribe("subagent.completed", func(event runtimeevents.Event) {
		if value, ok := event.Payload["usage_total_tokens"].(int); ok {
			completedUsageTotal = float64(value)
		}
	})
	agent.SetEventBus(bus)

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Child summary.",
				Model:   "test-model",
				Usage:   &types.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent.llmRuntime = llmRuntime

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1})
	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID:         "trace_subagent_budget",
		ParentSessionID: "parent-session",
		Depth:           1,
	}, []SubagentTask{
		{
			ID:           "child-1",
			Role:         "researcher",
			Goal:         "Inspect the logs.",
			ReadOnly:     true,
			BudgetTokens: 1024,
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Usage)
	assert.Equal(t, "researcher", results[0].Role)
	assert.NotEmpty(t, results[0].SessionID)
	assert.Equal(t, 10, results[0].Usage.TotalTokens)
	assert.Equal(t, results[0].SessionID, startedSessionID)
	assert.Equal(t, 10.0, completedUsageTotal)
	require.Len(t, provider.requests, 1)
	assert.Equal(t, 1024, provider.requests[0].MaxTokens)
}

func TestReActLoop_GetAvailableTools_ExposesStableManagerToolsAcrossGoals(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		mcpManager: &MockCatalogMCPManager{},
	}

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	tools, err := loop.getAvailableTools(context.Background(), "inspect recent logs and errors", nil)
	if err != nil {
		t.Fatalf("get available tools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("expected tools from manager")
	}
	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "read_file")
	assert.Contains(t, names, "read_logs")
	assert.Contains(t, names, "run_tests")

	otherTools, err := loop.getAvailableTools(context.Background(), "run the test suite", nil)
	require.NoError(t, err)
	assert.Equal(t, names, toolDefinitionNames(otherTools), "tool surface should not vary by goal text")

	for _, tool := range tools {
		if tool.Name == "read_logs" {
			if got := tool.Metadata[toolresult.SourceKey]; got != toolresult.SourceToolkit {
				t.Fatalf("expected read_logs %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceToolkit, got)
			}
			return
		}
	}
	t.Fatal("expected read_logs to be exposed")
}

func TestReActLoop_GetAvailableTools_AlwaysExposesCoreFileTools(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		mcpManager: runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil)),
	}

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	tools, err := loop.getAvailableTools(context.Background(), "分析 项目 e:/projects/ai/codex-server 中 botspage.jsx 是否需要进行优化", nil)
	require.NoError(t, err)

	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "glob")
	assert.Contains(t, names, "grep")
	assert.Contains(t, names, "ls")
	assert.Contains(t, names, "view")

	otherTools, err := loop.getAvailableTools(context.Background(), "summarize the current implementation", nil)
	require.NoError(t, err)
	assert.Equal(t, names, toolDefinitionNames(otherTools), "core local tool surface should remain stable across goals")
}

func TestReActLoop_GetAvailableTools_PreservesMetaToolkitAndBrokerSourceMetadata(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		mcpManager: runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil)),
		toolBroker: &toolbroker.Broker{},
	}

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	tools, err := loop.getAvailableTools(context.Background(), "", []string{"list_mcp_resources", "view", toolbroker.ToolBackgroundTask})
	require.NoError(t, err)

	var metaSource interface{}
	var toolkitSource interface{}
	var brokerSource interface{}
	for _, tool := range tools {
		switch tool.Name {
		case "list_mcp_resources":
			metaSource = tool.Metadata[toolresult.SourceKey]
		case "view":
			toolkitSource = tool.Metadata[toolresult.SourceKey]
		case toolbroker.ToolBackgroundTask:
			brokerSource = tool.Metadata[toolresult.SourceKey]
		}
	}

	assert.Equal(t, toolresult.SourceMeta, metaSource)
	assert.Equal(t, toolresult.SourceToolkit, toolkitSource)
	assert.Equal(t, toolresult.SourceBroker, brokerSource)
}

func TestReActLoop_Run_AttachesToolSurfaceMetadataToLLMRequest(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     1,
			SystemPrompt: "You are a helpful assistant.",
		},
		mcpManager: &MockCatalogMCPManager{},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Done.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "inspect recent logs and errors")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 1)

	request := provider.requests[0]
	require.NotEmpty(t, request.Tools)

	surface, ok := request.Metadata["tool_surface"].(map[string]interface{})
	require.True(t, ok, "expected tool_surface metadata to be attached")
	assert.Equal(t, len(request.Tools), surface["count"])

	names, ok := surface["names"].([]string)
	require.True(t, ok, "expected tool_surface.names to be a []string")
	assert.Equal(t, toolDefinitionNames(request.Tools), names)
}

func TestResolveToolSourceForRequest_PrefersResolvedRuntimeSource(t *testing.T) {
	agent := &Agent{
		mcpManager: runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil)),
	}

	assert.Equal(t, toolresult.SourceMeta, resolveToolSourceForRequest(agent, "list_mcp_resources"))
	assert.Equal(t, toolresult.SourceToolkit, resolveToolSourceForRequest(agent, "view"))
	agent.toolBroker = &toolbroker.Broker{}
	assert.Equal(t, toolresult.SourceBroker, resolveToolSourceForRequest(agent, toolbroker.ToolBackgroundTask))
}

func TestReActLoop_GetAvailableTools_HidesTeamOnlyBrokerToolsUntilRunMetaActive(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		toolBroker: &toolbroker.Broker{TeamStore: store},
	}

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})

	tools, err := loop.getAvailableTools(context.Background(), "coordinate team tasks", nil)
	require.NoError(t, err)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, toolbroker.ToolSpawnTeam)
	assert.NotContains(t, names, toolbroker.ToolReadTaskSpec)
	assert.NotContains(t, names, toolbroker.ToolReadTaskContext)
	assert.NotContains(t, names, toolbroker.ToolSendTeamMessage)

	runCtx := team.WithRunMeta(context.Background(), &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:  "team-1",
			AgentID: "lead",
		},
	})
	tools, err = loop.getAvailableTools(runCtx, "coordinate team tasks", nil)
	require.NoError(t, err)
	names = names[:0]
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, toolbroker.ToolSpawnTeam)
	assert.Contains(t, names, toolbroker.ToolReadTaskSpec)
	assert.Contains(t, names, toolbroker.ToolReadTaskContext)
	assert.Contains(t, names, toolbroker.ToolSendTeamMessage)
}

func TestReActLoop_Run_FreezesToolSurfaceWithinActiveTurnAfterSpawnTeam(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "You are a helpful assistant.",
		},
		toolBroker: &toolbroker.Broker{TeamStore: store},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will create a team.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_spawn_team",
						Name: toolbroker.ToolSpawnTeam,
						Args: map[string]interface{}{
							"team_id":    "team-cache-freeze",
							"auto_start": false,
						},
					},
				},
			},
			{
				Content: "Team created.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "Coordinate team tasks.")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 2)

	firstNames := toolDefinitionNames(provider.requests[0].Tools)
	secondNames := toolDefinitionNames(provider.requests[1].Tools)
	assert.Contains(t, firstNames, toolbroker.ToolSpawnTeam)
	assert.NotContains(t, firstNames, toolbroker.ToolReadTaskSpec)
	assert.Equal(t, firstNames, secondNames)
	assert.NotContains(t, secondNames, toolbroker.ToolReadTaskSpec)
	assert.NotContains(t, secondNames, toolbroker.ToolReadTaskContext)
	assert.NotContains(t, secondNames, toolbroker.ToolSendTeamMessage)
}

func TestReActLoop_GetAvailableTools_RecomputesForIndependentTurnSnapshots(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		toolBroker: &toolbroker.Broker{TeamStore: store},
	}

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})

	firstTurnCtx := ensureTurnToolSurfaceSnapshot(context.Background())
	firstTurnTools, err := loop.getAvailableTools(firstTurnCtx, "coordinate team tasks", nil)
	require.NoError(t, err)
	firstTurnNames := toolDefinitionNames(firstTurnTools)
	assert.Contains(t, firstTurnNames, toolbroker.ToolSpawnTeam)
	assert.NotContains(t, firstTurnNames, toolbroker.ToolReadTaskSpec)

	secondTurnCtx := ensureTurnToolSurfaceSnapshot(context.Background())
	secondTurnCtx = team.WithRunMeta(secondTurnCtx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:  "team-1",
			AgentID: "lead",
		},
	})
	secondTurnTools, err := loop.getAvailableTools(secondTurnCtx, "coordinate team tasks", nil)
	require.NoError(t, err)
	secondTurnNames := toolDefinitionNames(secondTurnTools)
	assert.Contains(t, secondTurnNames, toolbroker.ToolSpawnTeam)
	assert.Contains(t, secondTurnNames, toolbroker.ToolReadTaskSpec)
	assert.Contains(t, secondTurnNames, toolbroker.ToolReadTaskContext)
	assert.Contains(t, secondTurnNames, toolbroker.ToolSendTeamMessage)
}

func TestReActLoop_Run_EmitsRuntimeEvents(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     4,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	bus := runtimeevents.NewBus()
	var eventTypes []string
	var traceIDs []string
	bus.Subscribe("", func(event runtimeevents.Event) {
		eventTypes = append(eventTypes, event.Type)
		traceIDs = append(traceIDs, event.TraceID)
	})
	agent.SetEventBus(bus)
	agent.SetSubagentScheduler(NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 2, MaxDepth: 1}))

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will delegate.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "spawn_subagents",
						Args: map[string]interface{}{
							"agents": []interface{}{
								map[string]interface{}{
									"id":        "child-1",
									"goal":      "Inspect logs",
									"read_only": true,
								},
							},
						},
					},
				},
			},
			{Content: "The logs show a parser panic.", Model: "test-model"},
			{Content: "Final answer from parent.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	_, err := loop.Run(context.Background(), "Find the root cause.")
	require.NoError(t, err)

	assert.Contains(t, eventTypes, "tool.requested")
	assert.Contains(t, eventTypes, "subagent.batch.started")
	assert.Contains(t, eventTypes, "subagent.started")
	assert.Contains(t, eventTypes, "subagent.completed")
	assert.Contains(t, eventTypes, "tool.reduced")
	for _, traceID := range traceIDs {
		assert.NotEmpty(t, traceID)
	}
	assert.True(t, allEqualStrings(traceIDs))
}

func TestReActLoop_Run_ReadOnlyPolicyBlocksWriteLikeTools(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockPolicyMCPManager{},
	}
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy(nil, true))
	bus := runtimeevents.NewBus()
	var deniedPolicies []string
	bus.Subscribe("tool.denied", func(event runtimeevents.Event) {
		if policy, ok := event.Payload["policy"].(string); ok {
			deniedPolicies = append(deniedPolicies, policy)
		}
	})
	agent.SetEventBus(bus)

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will write a file.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "write_file",
						Args: map[string]interface{}{"path": "tmp.txt"},
					},
				},
			},
			{
				Content: "I cannot write in read-only mode.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	result, err := loop.Run(context.Background(), "Attempt to update a file.")
	require.NoError(t, err)
	require.NotEmpty(t, result.Observations)
	assert.Contains(t, result.Observations[0].Error, "read-only policy blocks write-like tool")
	assert.Contains(t, deniedPolicies, "read_only")

	tools, err := loop.getAvailableTools(context.Background(), "write file", nil)
	require.NoError(t, err)
	for _, tool := range tools {
		assert.NotEqual(t, "write_file", tool.Name)
	}
}

func TestReActLoop_Run_HooksCanBlockAndObserveTools(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockPolicyMCPManager{},
	}

	var postCalled bool
	agent.AddPreToolUse(func(ctx context.Context, sessionID string, call types.ToolCall) error {
		if call.Name == "write_file" {
			return fmt.Errorf("hook blocked tool")
		}
		return nil
	})
	agent.AddPostToolUse(func(ctx context.Context, sessionID string, result toolExecutionResult) {
		postCalled = true
	})

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will write a file.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "write_file",
						Args: map[string]interface{}{"path": "tmp.txt"},
					},
				},
			},
			{
				Content: "The hook prevented the write.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	result, err := loop.Run(context.Background(), "Attempt write.")
	require.NoError(t, err)
	assert.True(t, postCalled)
	require.NotEmpty(t, result.Observations)
	assert.Contains(t, result.Observations[0].Error, "hook blocked tool")
}

func TestSubagentScheduler_RunChildren_InheritsParentHooks(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockSequenceMCPManager{
			output: "log output",
		},
	}

	var preCalls []string
	var postCalls []string
	agent.AddPreToolUse(func(ctx context.Context, sessionID string, call types.ToolCall) error {
		preCalls = append(preCalls, call.Name)
		return nil
	})
	agent.AddPostToolUse(func(ctx context.Context, sessionID string, result toolExecutionResult) {
		postCalls = append(postCalls, result.Call.Name)
	})

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Inspecting logs.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "read_logs",
						Args: map[string]interface{}{"path": "logs/app.log"},
					},
				},
			},
			{
				Content: "Found the parser panic.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent.llmRuntime = llmRuntime

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1})
	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID:         "trace_hooks",
		ParentSessionID: "parent-session",
		Depth:           1,
	}, []SubagentTask{
		{
			ID:             "child-1",
			Role:           "researcher",
			Goal:           "Inspect the logs for failures.",
			ReadOnly:       true,
			ToolsWhitelist: []string{"read_logs"},
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, []string{"read_logs"}, preCalls)
	assert.Equal(t, []string{"read_logs"}, postCalls)
}

func TestSubagentScheduler_RunChildren_WriterReportsPatches(t *testing.T) {
	bus := runtimeevents.NewBus()
	var patchAppliedEvents []runtimeevents.Event
	bus.Subscribe("patch.applied", func(event runtimeevents.Event) {
		patchAppliedEvents = append(patchAppliedEvents, event)
	})

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockRichSequenceMCPManager{
			output: "Successfully wrote file.",
			meta: map[string]interface{}{
				"file_path": "workspace/result.txt",
				"action":    "created",
				"old_size":  0,
				"new_size":  24,
				"patch":     "--- /dev/null\n+++ b/workspace/result.txt\n@@ -0,0 +1 @@\n+patched output\n",
			},
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Writing result file.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "write_file",
						Args: map[string]interface{}{
							"path":    "workspace/result.txt",
							"content": "patched output",
						},
					},
				},
			},
			{
				Content: "Created the output file.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent.llmRuntime = llmRuntime
	agent.SetEventBus(bus)

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1})
	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID:         "trace_writer",
		ParentSessionID: "parent-session",
		Depth:           1,
	}, []SubagentTask{
		{
			ID:             "writer-1",
			Role:           "writer",
			Goal:           "Create the output file.",
			ReadOnly:       false,
			ToolsWhitelist: []string{"write_file"},
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Len(t, results[0].Patches, 1)
	assert.Equal(t, "workspace/result.txt", results[0].Patches[0].Path)
	assert.Contains(t, results[0].Patches[0].Summary, "created file")
	assert.Contains(t, results[0].Patches[0].Diff, "---")
	assert.Contains(t, results[0].Patches[0].Diff, "+++")
	assert.Equal(t, "applied", results[0].Patches[0].ApplyStatus)
	assert.Contains(t, results[0].Patches[0].AppliedBy, "writer-1")
	assert.Equal(t, "unverified", results[0].Patches[0].VerificationStatus)
	require.Len(t, patchAppliedEvents, 1)
	assert.Equal(t, "applied", patchAppliedEvents[0].Payload["apply_status"])
	assert.Equal(t, "unverified", patchAppliedEvents[0].Payload["verification_status"])

	reportText := renderSubagentResults(results)
	assert.Contains(t, reportText, "patch: workspace/result.txt")
	assert.Contains(t, reportText, "created file")
}

func TestSubagentScheduler_RunChildren_WriterExtractsDiffFromOutput(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockRichSequenceMCPManager{
			output: "Write completed.\n--- /dev/null\n+++ b/workspace/from-output.txt\n@@ -0,0 +1 @@\n+hello from output\n",
			meta:   nil,
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Writing result file.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "write_file",
						Args: map[string]interface{}{
							"content": "hello from output",
						},
					},
				},
			},
			{
				Content: "Created the output file.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent.llmRuntime = llmRuntime

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1})
	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID:         "trace_writer_output_diff",
		ParentSessionID: "parent-session",
		Depth:           1,
	}, []SubagentTask{
		{
			ID:             "writer-1",
			Role:           "writer",
			Goal:           "Create the output file.",
			ReadOnly:       false,
			ToolsWhitelist: []string{"write_file"},
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Len(t, results[0].Patches, 1)
	assert.Equal(t, "workspace/from-output.txt", results[0].Patches[0].Path)
	assert.Contains(t, results[0].Patches[0].Diff, "--- /dev/null")
	assert.Contains(t, results[0].Patches[0].Diff, "+++ b/workspace/from-output.txt")
	assert.Equal(t, "applied", results[0].Patches[0].ApplyStatus)
	assert.Contains(t, results[0].Patches[0].AppliedBy, "writer-1")
	assert.Equal(t, "unverified", results[0].Patches[0].VerificationStatus)
}

func TestSubagentScheduler_RunChildren_DependencyInjectsWriterPatchesIntoVerifier(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager: &MockRichSequenceMCPManager{
			output: "Successfully wrote file.",
			meta: map[string]interface{}{
				"file_path": "workspace/result.txt",
				"action":    "created",
				"old_size":  0,
				"new_size":  24,
				"patch":     "--- /dev/null\n+++ b/workspace/result.txt\n@@ -0,0 +1 @@\n+patched output\n",
			},
		},
	}

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Writing result file.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "write_file",
						Args: map[string]interface{}{
							"path":    "workspace/result.txt",
							"content": "patched output",
						},
					},
				},
			},
			{
				Content: "Created the output file.",
				Model:   "test-model",
			},
			{
				Content: "Verified the patch via review.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent.llmRuntime = llmRuntime

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 2, MaxDepth: 1})
	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID:         "trace_verifier",
		ParentSessionID: "parent-session",
		Depth:           1,
	}, []SubagentTask{
		{
			ID:             "writer-1",
			Role:           "writer",
			Goal:           "Create the output file.",
			ReadOnly:       false,
			ToolsWhitelist: []string{"write_file"},
		},
		{
			ID:             "verifier-1",
			Role:           "verifier",
			Goal:           "Review the writer output and verify the change.",
			ReadOnly:       true,
			ToolsWhitelist: []string{"read_file", "git_log"},
			DependsOn:      []string{"writer-1"},
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Len(t, results[0].Patches, 1)
	assert.Equal(t, "applied", results[0].Patches[0].ApplyStatus)
	assert.Contains(t, results[0].Patches[0].AppliedBy, "writer-1")
	assert.Equal(t, "verified", results[0].Patches[0].VerificationStatus)
	assert.Contains(t, results[0].Patches[0].VerifiedBy, "verifier-1")

	require.Len(t, provider.requests, 3)
	verifierRequest := provider.requests[2]
	require.NotEmpty(t, verifierRequest.Messages)
	assert.Contains(t, verifierRequest.Messages[0].Content, "Depends on completed subagents: writer-1.")
	assert.Contains(t, verifierRequest.Messages[0].Content, "Patch context:")
	assert.Contains(t, verifierRequest.Messages[0].Content, "workspace/result.txt")
	assert.Contains(t, verifierRequest.Messages[0].Content, "Patch diff excerpt:")
}

func TestDefaultToolsForRole(t *testing.T) {
	assert.Contains(t, DefaultToolsForRole("researcher"), "read_logs")
	assert.Contains(t, DefaultToolsForRole("tester"), "run_tests")
	assert.Contains(t, DefaultToolsForRole("writer"), "write_file")
	assert.Contains(t, DefaultToolsForRole("verifier"), "git_log")
}

func TestSubagentScheduler_EnforcesSingleWriterPolicy(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 2},
	}
	bus := runtimeevents.NewBus()
	var deniedReasons []string
	bus.Subscribe("subagent.denied", func(event runtimeevents.Event) {
		if reason, ok := event.Payload["reason"].(string); ok {
			deniedReasons = append(deniedReasons, reason)
		}
	})
	agent.SetEventBus(bus)
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent:       2,
		MaxDepth:            1,
		EnforceSingleWriter: true,
	})

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID: "trace_test",
		Depth:   1,
	}, []SubagentTask{
		{ID: "writer-1", Goal: "Modify config", ReadOnly: false},
		{ID: "writer-2", Goal: "Modify code", ReadOnly: false},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single-writer policy violation")
	assert.Contains(t, deniedReasons[0], "single-writer policy violation")
}

func TestSubagentScheduler_ReadOnlyRejectsWriteLikeTools(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 2},
	}
	bus := runtimeevents.NewBus()
	var deniedReasons []string
	bus.Subscribe("subagent.denied", func(event runtimeevents.Event) {
		if reason, ok := event.Payload["reason"].(string); ok {
			deniedReasons = append(deniedReasons, reason)
		}
	})
	agent.SetEventBus(bus)
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent:       2,
		MaxDepth:            1,
		EnforceSingleWriter: true,
	})

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID: "trace_test",
		Depth:   1,
	}, []SubagentTask{
		{ID: "reader-1", Goal: "Inspect files", ReadOnly: true, ToolsWhitelist: []string{"write_file"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requested write-like tools")
	assert.Contains(t, deniedReasons[0], "requested write-like tools")
}

func TestSubagentScheduler_ReadOnlyParentRejectsWritableSubagent(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 2},
	}
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy(nil, true))

	bus := runtimeevents.NewBus()
	var deniedPolicies []string
	bus.Subscribe("subagent.denied", func(event runtimeevents.Event) {
		if policy, ok := event.Payload["policy"].(string); ok {
			deniedPolicies = append(deniedPolicies, policy)
		}
	})
	agent.SetEventBus(bus)

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent:       2,
		MaxDepth:            1,
		EnforceSingleWriter: true,
	})

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID: "trace_test",
		Depth:   1,
	}, []SubagentTask{
		{ID: "writer-1", Goal: "Modify config", ReadOnly: false, ToolsWhitelist: []string{"write_file"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only parent policy blocks writable subagent")
	assert.Contains(t, deniedPolicies, "read_only")
}

// MockMCPManager 实现 skill.MCPManager 接口
type MockMCPManager struct{}

func (m *MockMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
	}, nil
}

func (m *MockMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{
		"result": "mock result",
	}, nil
}

func (m *MockMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "mock_tool", Description: "A mock tool", Enabled: true},
	}
}

type MockSequenceMCPManager struct {
	output string
}

func (m *MockSequenceMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
	}, nil
}

func (m *MockSequenceMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if m.output == "" {
		return nil, fmt.Errorf("no output configured for %s", toolName)
	}
	return m.output, nil
}

func (m *MockSequenceMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "read_logs", Description: "Read logs", Enabled: true, MCPName: "mock-mcp"},
	}
}

type MockMutatingMCPManager struct {
	path   string
	output string
}

func (m *MockMutatingMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
	}, nil
}

func (m *MockMutatingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if strings.TrimSpace(m.path) != "" {
		if err := os.WriteFile(m.path, []byte("after"), 0o644); err != nil {
			return nil, err
		}
	}
	if m.output == "" {
		return "ok", nil
	}
	return m.output, nil
}

func (m *MockMutatingMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "execute_shell_command", Description: "Execute a shell command", Enabled: true, MCPName: "mock-mcp"},
	}
}

type MockRichSequenceMCPManager struct {
	output string
	meta   map[string]interface{}
	err    error
}

func (m *MockRichSequenceMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
	}, nil
}

func (m *MockRichSequenceMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if m.err != nil {
		return m.output, m.err
	}
	if m.output == "" {
		return nil, fmt.Errorf("no output configured for %s", toolName)
	}
	return m.output, nil
}

func (m *MockRichSequenceMCPManager) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if m.err != nil {
		return m.output, m.meta, m.err
	}
	if m.output == "" {
		return nil, nil, fmt.Errorf("no output configured for %s", toolName)
	}
	return m.output, m.meta, nil
}

func (m *MockRichSequenceMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "write_file", Description: "Write file", Enabled: true, MCPName: "mock-mcp"},
	}
}

type MockCatalogMCPManager struct{}

func (m *MockCatalogMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true}, nil
}

func (m *MockCatalogMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *MockCatalogMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "read_logs", Description: "Read and inspect logs", Enabled: true},
		{Name: "read_file", Description: "Read a file from workspace", Enabled: true},
		{Name: "run_tests", Description: "Run tests and inspect failures", Enabled: true},
	}
}

type MockPolicyMCPManager struct{}

func (m *MockPolicyMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true}, nil
}

func (m *MockPolicyMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "unexpected call", nil
}

func (m *MockPolicyMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "write_file", Description: "Write a file", Enabled: true},
		{Name: "read_file", Description: "Read a file", Enabled: true},
	}
}

func cloneLLMRequest(req *llm.LLMRequest) *llm.LLMRequest {
	if req == nil {
		return nil
	}

	cloned := &llm.LLMRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
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

func allEqualStrings(values []string) bool {
	if len(values) <= 1 {
		return true
	}
	first := values[0]
	for _, value := range values[1:] {
		if value != first {
			return false
		}
	}
	return true
}

func toolDefinitionNames(defs []types.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

type testHistorySession struct {
	id       string
	messages []types.Message
}

func newTestHistorySession(id string) *testHistorySession {
	return &testHistorySession{
		id:       id,
		messages: make([]types.Message, 0),
	}
}

func (s *testHistorySession) SessionID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *testHistorySession) GetMessages() []types.Message {
	if s == nil {
		return nil
	}
	return cloneTestMessages(s.messages)
}

func (s *testHistorySession) LastMessage() *types.Message {
	if s == nil || len(s.messages) == 0 {
		return nil
	}
	return s.messages[len(s.messages)-1].Clone()
}

func (s *testHistorySession) ReplaceHistory(messages []types.Message) {
	if s == nil {
		return
	}
	s.messages = cloneTestMessages(messages)
}

func cloneTestMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func TestToolExecutionResultMessage_UsesFullRawOutputInsteadOfReducedEnvelope(t *testing.T) {
	result := toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-1",
			Name: "execute_shell_command",
		},
		Output: "line 1\nline 2\nline 3",
		Envelope: &output.Envelope{
			ToolName:   "execute_shell_command",
			ToolCallID: "call-1",
			Summary:    "line 1",
		},
	}

	message := toolExecutionResultMessage(result)
	if message == nil {
		t.Fatal("expected tool message")
	}
	if message.Content != "line 1\nline 2\nline 3" {
		t.Fatalf("expected full raw output, got %q", message.Content)
	}
}

func TestToolResultsToPayloads_UsesFullRawOutputInsteadOfReducedEnvelope(t *testing.T) {
	payloads := toolResultsToPayloads([]toolExecutionResult{
		{
			Call: types.ToolCall{
				ID:   "call-1",
				Name: "execute_shell_command",
			},
			Output: "line 1\nline 2\nline 3",
			Envelope: &output.Envelope{
				ToolName:   "execute_shell_command",
				ToolCallID: "call-1",
				Summary:    "line 1",
				Metadata: map[string]interface{}{
					"reducer": "text_truncation",
				},
			},
		},
	})

	if len(payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(payloads))
	}
	if payloads[0].Content != "line 1\nline 2\nline 3" {
		t.Fatalf("expected full raw output, got %q", payloads[0].Content)
	}
	if payloads[0].Metadata["reducer"] != "text_truncation" {
		t.Fatalf("expected reducer metadata to stay attached, got %v", payloads[0].Metadata["reducer"])
	}
}
