package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestToolCallContextCarriesAgentDepth(t *testing.T) {
	ctx := toolCallContext(context.Background(), nil, "", nil, nil, "session-depth", 2)
	require.Equal(t, 2, toolctx.AgentDepth(ctx))
	require.Equal(t, "session-depth", toolctx.SessionID(ctx))
}

func TestAddRemainingBudgetMetadataDistinguishesUnlimitedFromExhausted(t *testing.T) {
	metadata := map[string]interface{}{"remaining_budget": 0}
	addRemainingBudgetMetadata(metadata, 0)
	require.NotContains(t, metadata, "remaining_budget")
	require.Equal(t, false, metadata["token_budget_configured"])

	addRemainingBudgetMetadata(metadata, 4096)
	require.Equal(t, 4096, metadata["remaining_budget"])
	require.Equal(t, true, metadata["token_budget_configured"])
}

func TestReActLoop_CompletedAnswerRecoversEarlierToolError(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will try the requested operation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call-fails", Name: "write_file",
					Args: map[string]interface{}{"path": "missing.txt"},
				}},
			},
			{
				Content: "I will retry with the corrected path.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call-recovers", Name: "write_file",
					Args: map[string]interface{}{"path": "verified.txt"},
				}},
			},
			{Content: "Completed with an alternative verified result.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "recovery-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 3,
	}, &RecoveringMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 3, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "complete the operation")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "Completed with an alternative verified result.", result.Output)
	require.Empty(t, result.Error)
	require.Equal(t, 1, result.ToolErrorCount)
	require.Equal(t, 1, result.RecoveredToolErrorCount)
	require.Zero(t, result.UnrecoveredToolErrorCount)
}

func TestReActLoop_UnrelatedToolSuccessDoesNotRecoverEarlierError(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will write the requested file.", Model: "test-model",
				ToolCalls: []types.ToolCall{{ID: "call-fails", Name: "write_file"}},
			},
			{
				Content: "I will inspect another file.", Model: "test-model",
				ToolCalls: []types.ToolCall{{ID: "call-unrelated", Name: "read_file"}},
			},
			{Content: "Inspection completed, but the write was not retried.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "recovery-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 3,
	}, &RecoveringMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 3, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "write the file")

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, result.ToolErrorCount)
	require.Zero(t, result.RecoveredToolErrorCount)
	require.Equal(t, 1, result.UnrecoveredToolErrorCount)
	require.Contains(t, result.Error, "file not found")
}

func TestRecoverMatchingToolFailuresRequiresSameTool(t *testing.T) {
	pending := []pendingToolFailure{
		{toolName: "write_file", message: "write failed"},
		{toolName: "read_file", message: "read failed"},
	}

	remaining, recovered := recoverMatchingToolFailures(pending, "read-file")

	require.Equal(t, 1, recovered)
	require.Len(t, remaining, 1)
	require.Equal(t, "write_file", remaining[0].toolName)
	require.Equal(t, []string{"write failed"}, pendingToolFailureMessages(remaining))
}

func TestRecoverMatchingToolFailuresAllowsGenericActRecovery(t *testing.T) {
	pending := []pendingToolFailure{{message: "act failed before a result was available"}}

	remaining, recovered := recoverMatchingToolFailures(pending, "read_file")

	require.Equal(t, 1, recovered)
	require.Empty(t, remaining)
}

func TestSubagentSchedulerRejectsDepthBeforeStartingChildrenWithActionableError(t *testing.T) {
	agent := &Agent{config: &Config{}}
	bus := runtimeevents.NewBus()
	var denied runtimeevents.Event
	bus.Subscribe("subagent.denied", func(event runtimeevents.Event) { denied = event })
	agent.SetEventBus(bus)
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxDepth: 1})

	reports, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 2}, []SubagentTask{{ID: "nested", Goal: "inspect"}})
	require.Error(t, err)
	require.Empty(t, reports)
	require.Contains(t, err.Error(), "before child creation")
	require.Contains(t, err.Error(), "requested_depth=2")
	require.Contains(t, err.Error(), "continue the work in the current agent")
	require.Equal(t, "max_depth", denied.Payload["policy"])
	require.Equal(t, 2, denied.Payload["depth"])
	require.Equal(t, 1, denied.Payload["max_depth"])
}

// MockLLMProvider 模拟 LLM Provider 用于测试
type MockLLMProvider struct {
	name string
}

type SequenceLLMProvider struct {
	name              string
	responses         []*llm.LLMResponse
	callCount         int
	requests          []*llm.LLMRequest
	defaultModel      string
	providerCaps      *llm.ModelCapabilities
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec
}

type BlockingLLMProvider struct {
	name         string
	release      <-chan struct{}
	entered      chan struct{}
	mu           sync.Mutex
	active       int
	maxActive    int
	requestCount int
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

func (s *SequenceLLMProvider) DefaultModelName() string {
	return strings.TrimSpace(s.defaultModel)
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
	s.requests = append(s.requests, cloneLLMRequest(req))
	if s.callCount >= len(s.responses) {
		ch := make(chan llm.StreamChunk, 1)
		ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
		close(ch)
		return ch, nil
	}

	response := s.responses[s.callCount]
	s.callCount++

	ch := make(chan llm.StreamChunk, 3)
	go func() {
		defer close(ch)
		if response == nil {
			ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
			return
		}
		if response.Reasoning != "" {
			ch <- llm.StreamChunk{Type: llm.EventTypeReasoning, Content: response.Reasoning}
		}
		if response.Content != "" {
			ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: response.Content}
		}
		ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	}()
	return ch, nil
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

func (p *BlockingLLMProvider) Name() string {
	return p.name
}

func (p *BlockingLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.mu.Lock()
	p.active++
	p.requestCount++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()

	select {
	case p.entered <- struct{}{}:
	default:
	}

	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()

	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llm.LLMResponse{Content: "Expert child summary.", Model: req.Model}, nil
}

func (p *BlockingLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *BlockingLLMProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *BlockingLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens: 128000,
		MaxOutputTokens:  4096,
		SupportsTools:    true,
	}
}

func (p *BlockingLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (p *BlockingLLMProvider) MaxActive() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

func (p *BlockingLLMProvider) RequestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requestCount
}

type RetrySequenceLLMProvider struct {
	name      string
	errs      []error
	response  *llm.LLMResponse
	callCount int
	requests  []*llm.LLMRequest
}

func (p *RetrySequenceLLMProvider) Name() string {
	return p.name
}

func (p *RetrySequenceLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCount++
	p.requests = append(p.requests, cloneLLMRequest(req))
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
	p.callCount++
	p.requests = append(p.requests, cloneLLMRequest(req))
	if index := p.callCount - 1; index < len(p.errs) && p.errs[index] != nil {
		return nil, p.errs[index]
	}
	response := p.response
	ch := make(chan llm.StreamChunk, 2)
	go func() {
		defer close(ch)
		if response != nil && strings.TrimSpace(response.Content) != "" {
			ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: response.Content}
		}
		ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	}()
	return ch, nil
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
		"MaxSteps":             0,
		"EnableThought":        true,
		"EnableToolCalls":      true,
		"EnableParallelTools":  true,
		"MaxParallelToolCalls": 4,
		"Verbose":              false,
		"Temperature":          0.7,
		"StopOnSuccess":        true,
		"MaxIterations":        10,
	}

	if loop.config.MaxSteps != expectedDefaults["MaxSteps"].(int) {
		t.Errorf("expected default MaxSteps %d, got %d", expectedDefaults["MaxSteps"].(int), loop.config.MaxSteps)
	}
	require.Zero(t, loop.config.MaxToolCalls)
	require.Zero(t, loop.config.MaxRunDuration)
	require.Zero(t, loop.config.MaxExplorationSteps)
	require.Zero(t, loop.config.MaxRepeatedToolCalls)
	require.Equal(t, expectedDefaults["EnableParallelTools"], loop.config.EnableParallelTools)
	require.Equal(t, expectedDefaults["MaxParallelToolCalls"], loop.config.MaxParallelToolCalls)
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

func TestReActLoop_PropagatesParallelToolCapabilityToLLMRequest(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{{
			Content: "inspection complete",
			Model:   "test-model",
		}},
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
		mcpManager: &MockMCPManager{},
	}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:             1,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 4,
	})

	result, err := loop.RunWithSession(context.Background(), "inspect the repository", newTestHistorySession("session-parallel-hint"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, provider.requests, 1)
	require.Equal(t, true, provider.requests[0].Metadata[llm.MetadataKeyParallelToolCalls])
	require.Equal(t, 4, provider.requests[0].Metadata["max_parallel_tool_calls"])
}

func TestReActLoop_PropagatesDisableRetriesOptionToLLMRequest(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{{
			Content: "done",
			Model:   "test-model",
		}},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "test-provider",
			Model:        "test-model",
			SystemPrompt: "You are a helpful assistant.",
			Options:      map[string]interface{}{llm.MetadataKeyDisableRetries: true},
		},
		mcpManager: &MockMCPManager{},
	}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 1})

	result, err := loop.RunWithSession(context.Background(), "finish", newTestHistorySession("session-fail-fast"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, provider.requests, 1)
	require.Equal(t, true, provider.requests[0].Metadata[llm.MetadataKeyDisableRetries])
}

func TestReActLoop_DowngradesUnsupportedParallelToolParameterWithoutAdvancingStep(t *testing.T) {
	provider := &RetrySequenceLLMProvider{
		name: "test-provider",
		errs: []error{fmt.Errorf("HTTP 400: unsupported parameter: parallel_tool_calls")},
		response: &llm.LLMResponse{
			Content: "inspection complete",
			Model:   "test-model",
		},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{MaxRetries: 0})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name: "test-agent", Provider: "test-provider", Model: "test-model",
			SystemPrompt: "You are a helpful assistant.",
		},
		mcpManager: &MockMCPManager{},
	}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps: 1, EnableThought: true, EnableToolCalls: true,
		EnableParallelTools: true, MaxParallelToolCalls: 4,
	})

	result, err := loop.RunWithSession(context.Background(), "inspect the repository", newTestHistorySession("session-parallel-downgrade"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Steps)
	require.Len(t, provider.requests, 2)
	require.Equal(t, true, provider.requests[0].Metadata[llm.MetadataKeyParallelToolCalls])
	_, stillPresent := provider.requests[1].Metadata[llm.MetadataKeyParallelToolCalls]
	require.False(t, stillPresent)
	require.True(t, loop.parallelToolCallsUnsupported.Load())
}

func TestReActLoop_DowngradesUnsupportedOptionalParametersAndRemembersCapability(t *testing.T) {
	provider := &RetrySequenceLLMProvider{
		name: "test-provider",
		errs: []error{
			fmt.Errorf("HTTP 400: unsupported parameter: reasoning_effort"),
			fmt.Errorf("HTTP 400: unsupported parameter: temperature"),
		},
		response: &llm.LLMResponse{Content: "inspection complete", Model: "test-model"},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{MaxRetries: 0})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name: "test-agent", Provider: "test-provider", Model: "test-model",
			SystemPrompt: "You are a helpful assistant.",
		},
		mcpManager: &MockMCPManager{},
	}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps: 1, EnableThought: true, EnableToolCalls: true,
		ReasoningEffort: "high", Temperature: 0.7,
	})

	result, err := loop.RunWithSession(context.Background(), "inspect the repository", newTestHistorySession("session-parameter-downgrade"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Steps)
	require.Len(t, provider.requests, 3)
	require.Equal(t, "high", provider.requests[0].ReasoningEffort)
	require.Empty(t, provider.requests[1].ReasoningEffort)
	require.Equal(t, 0.7, provider.requests[1].Temperature)
	require.Zero(t, provider.requests[2].Temperature)

	_, err = loop.RunWithSession(context.Background(), "inspect another file", newTestHistorySession("session-parameter-memory"))
	require.NoError(t, err)
	require.Len(t, provider.requests, 4)
	require.Empty(t, provider.requests[3].ReasoningEffort)
	require.Zero(t, provider.requests[3].Temperature)
	require.True(t, loop.reasoningEffortUnsupported.Load())
	require.True(t, loop.temperatureUnsupported.Load())
}

func TestReActLoop_DowngradesThinkingAdaptiveEffortFromReasoningEffort(t *testing.T) {
	// Live failure shape: ReasoningEffort-derived adaptive thinking is rejected as
	// thinking.adaptive.effort extra_forbidden. req.Thinking may be nil; still
	// must clear ReasoningEffort so the next attempt does not rebuild adaptive.
	loop := NewReActLoop(&Agent{config: &Config{Name: "test"}}, nil, &LoopReActConfig{
		MaxSteps: 1, ReasoningEffort: "high",
	})
	req := &llm.LLMRequest{ReasoningEffort: "high"}
	err := fmt.Errorf(`HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"thinking.adaptive.effort: Extra inputs are not permitted"}}`)

	got := loop.downgradeUnsupportedProviderRequest(req, err)
	require.Equal(t, "thinking", got)
	require.Empty(t, req.ReasoningEffort)
	require.Nil(t, req.Thinking)
	require.True(t, loop.thinkingUnsupported.Load())
	require.True(t, loop.reasoningEffortUnsupported.Load())

	// Remembered capability must also strip effort on subsequent requests.
	next := &llm.LLMRequest{ReasoningEffort: "xhigh", Thinking: &types.ThinkingConfig{Type: "adaptive", Effort: "xhigh"}}
	loop.applyRememberedProviderRequestDowngrades(next)
	require.Empty(t, next.ReasoningEffort)
	require.Nil(t, next.Thinking)
}

func TestResolvePromptPreflightProviderModelUsesLoopConfigOverride(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "default-provider",
		DefaultModel:    "default-model",
	})
	baseProvider := &SequenceLLMProvider{name: "base-provider"}
	routeProvider := &SequenceLLMProvider{name: "route-provider"}
	require.NoError(t, runtime.RegisterProvider(baseProvider.Name(), baseProvider))
	require.NoError(t, runtime.RegisterProvider(routeProvider.Name(), routeProvider))

	agent := &Agent{
		config: &Config{
			Name:     "preflight-route-test",
			Provider: "base-provider",
			Model:    "base-model",
		},
	}
	loopConfig := &LoopReActConfig{
		Provider: "route-provider",
		Model:    "route-model",
	}

	provider, model := resolvePromptPreflightProviderModel(runtime, agent, loopConfig)
	require.Equal(t, "route-provider", provider)
	require.Equal(t, "route-model", model)

	provider, model = resolvePromptPreflightProviderModel(runtime, agent, nil)
	require.Equal(t, "base-provider", provider)
	require.Equal(t, "base-model", model)
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
			BaseDelay:     time.Millisecond,
			Randomization: -1, // 关闭默认 ±10% 抖动，精确断言 retry_delay_ms
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
	assert.Equal(t, "UPSTREAM_RATE_LIMITED", retryEvent.Payload["error_code"])
	assert.NotEmpty(t, retryEvent.Payload["logical_turn_id"])
	assert.NotEmpty(t, retryEvent.Payload["llm_request_id"])
	assert.NotEmpty(t, retryEvent.Payload["retry_attempt_id"])
	assert.NotEmpty(t, retryEvent.Payload["provider_request_id"])
}

func TestReActLoop_EmitsNonRetryableQuotaFailureDiagnostic(t *testing.T) {
	provider := &RetrySequenceLLMProvider{
		name: "test-provider",
		errs: []error{fmt.Errorf("HTTP 403: insufficient_user_quota")},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider", DefaultModel: "test-model", MaxRetries: 3,
	})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{config: &Config{Name: "test-agent", Provider: "test-provider", Model: "test-model"}}
	bus := runtimeevents.NewBus()
	var finished []runtimeevents.Event
	bus.Subscribe("llm.request.finished", func(event runtimeevents.Event) { finished = append(finished, event) })
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 1, EnableThought: true})

	result, err := loop.RunWithSession(context.Background(), "complete the task", newTestHistorySession("session-quota"))
	require.Error(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Failure)
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", result.Failure.ErrorCode)
	assert.False(t, result.Failure.Retryable)
	require.NotNil(t, result.Contract)
	require.NotEmpty(t, result.Contract.Errors)
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", result.Contract.Errors[0].Code)
	assert.False(t, result.Contract.Errors[0].Retryable)
	assert.Contains(t, result.Contract.Errors[0].NextAction, "do not retry unchanged")
	require.Equal(t, 1, provider.callCount, "quota failure must not consume retry attempts")
	require.Len(t, finished, 1)
	assert.Equal(t, false, finished[0].Payload["success"])
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", finished[0].Payload["error_code"])
	assert.Equal(t, false, finished[0].Payload["retryable"])
	assert.Contains(t, finished[0].Payload["next_action"], "do not retry unchanged")
}

func TestReActLoop_ProviderContextErrorCompactsAndRetriesSameStep(t *testing.T) {
	provider := &RetrySequenceLLMProvider{
		name: "test-provider",
		errs: []error{
			fmt.Errorf("HTTP 502: Your input exceeds the context window of this model"),
		},
		response: &llm.LLMResponse{Content: "恢复后的最终回答。", Model: "test-model"},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      3,
	})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		MaxSteps:         0,
		DefaultMaxTokens: 256,
	}, nil, llmRuntime)

	bus := runtimeevents.NewBus()
	var startedEvents []runtimeevents.Event
	bus.Subscribe("session_compact_started", func(event runtimeevents.Event) {
		startedEvents = append(startedEvents, event)
	})
	agent.SetEventBus(bus)

	session := &testContextHistorySession{
		testHistorySession: newTestHistorySession("session-provider-context-recovery"),
		context:            map[string]interface{}{},
	}
	for index := 0; index < 8; index++ {
		session.messages = append(session.messages,
			*types.NewUserMessage(fmt.Sprintf("历史请求 %d %s", index, strings.Repeat("context ", 40))),
			*types.NewAssistantMessage(fmt.Sprintf("历史回答 %d", index)),
		)
	}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 0})
	result, err := loop.RunWithSession(context.Background(), "继续完成任务", session)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "恢复后的最终回答。", result.Output)
	require.Equal(t, 1, result.Steps, "context recovery should retry the same logical step")
	require.Equal(t, 3, provider.callCount, "one failed call, one compact stream, and one recovered call")
	require.Len(t, startedEvents, 1)
	require.Equal(t, "provider_context_window_recovery", startedEvents[0].Payload["reason"])
	require.Equal(t, 1, sessionPromptCacheEpoch(session))
	require.Len(t, provider.requests, 3)
	require.Equal(t, 0, intValue(provider.requests[0].Metadata["prompt_cache_epoch"]))
	require.Equal(t, 1, intValue(provider.requests[2].Metadata["prompt_cache_epoch"]))
	recoveredCacheKey := provider.requests[2].Metadata["prompt_cache_key"]
	require.NotEqual(t, provider.requests[0].Metadata["prompt_cache_key"], recoveredCacheKey)

	// The durable replacement survives this run. A later turn must resume the
	// same rewritten-history generation instead of resetting to the raw session
	// key that was previously associated with the pre-compaction prefix.
	next, err := loop.RunWithSession(context.Background(), "继续下一轮", session)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.True(t, next.Success)
	require.Equal(t, 4, provider.callCount)
	require.Len(t, provider.requests, 4)
	require.Equal(t, 1, intValue(provider.requests[3].Metadata["prompt_cache_epoch"]))
	require.Equal(t, recoveredCacheKey, provider.requests[3].Metadata["prompt_cache_key"])
}

func TestCompactionRecoveryMadeProgressUsesActualHistoryReduction(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	before := []types.Message{
		*types.NewUserMessage(strings.Repeat("task context ", 80)),
		*types.NewAssistantMessage(strings.Repeat("progress ", 80)),
	}
	reducedMessages := []types.Message{*types.NewUserMessage("compact summary")}
	reducedTokens := []types.Message{
		*types.NewUserMessage("short task"),
		*types.NewAssistantMessage("short progress"),
	}

	require.True(t, compactionRecoveryMadeProgress(runtime, before, reducedMessages))
	require.True(t, compactionRecoveryMadeProgress(runtime, before, reducedTokens))
	require.False(t, compactionRecoveryMadeProgress(runtime, before, before))
	require.False(t, compactionRecoveryMadeProgress(runtime, reducedMessages, before))
}

func TestMarkSessionCompactionRecoveryInputRejectsOnlySameHistory(t *testing.T) {
	seen := map[string]struct{}{}
	require.True(t, markSessionCompactionRecoveryInput(seen, "history-a"))
	require.False(t, markSessionCompactionRecoveryInput(seen, "history-a"))
	require.True(t, markSessionCompactionRecoveryInput(seen, "history-b"))
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

func TestReActLoop_ObservesRepeatedSemanticToolCallsWithoutStopping(t *testing.T) {
	manager := &MockSequenceMCPManager{output: "same directory listing"}
	llmRuntime := llm.NewLLMRuntime(nil)
	responses := make([]*llm.LLMResponse, 0, repeatedSemanticToolCallNoticeThreshold+1)
	for index := 1; index <= repeatedSemanticToolCallNoticeThreshold; index++ {
		responses = append(responses, &llm.LLMResponse{
			Content: "检查目录。",
			Model:   "test-model",
			ToolCalls: []types.ToolCall{{
				ID:   fmt.Sprintf("call-%d", index),
				Name: "read_logs",
				Args: map[string]interface{}{"path": "logs/app.log"},
			}},
		})
	}
	responses = append(responses, &llm.LLMResponse{Content: "日志检查完成。", Model: "test-model"})
	provider := &SequenceLLMProvider{name: "test-provider", responses: responses}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "test-provider",
		Model:    "test-model",
		MaxSteps: 0,
	}, manager, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        0,
		EnableToolCalls: true,
	})
	bus := runtimeevents.NewBus()
	var legacyEvents []runtimeevents.Event
	var productWarnings []runtimeevents.Event
	bus.Subscribe(EventRepeatedSemanticCallObserved, func(event runtimeevents.Event) {
		legacyEvents = append(legacyEvents, event)
	})
	bus.Subscribe(EventDoomLoopWarning, func(event runtimeevents.Event) {
		productWarnings = append(productWarnings, event)
	})
	agent.SetEventBus(bus)

	result, err := loop.Run(context.Background(), "持续检查日志")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "日志检查完成。", result.Output)
	require.Equal(t, repeatedSemanticToolCallNoticeThreshold+1, len(provider.requests))
	require.Equal(t, repeatedSemanticToolCallNoticeThreshold, manager.callCount)
	require.Len(t, legacyEvents, 1)
	require.Len(t, productWarnings, 1)
	require.Equal(t, repeatedSemanticToolCallNoticeThreshold, legacyEvents[0].Payload["repeat_count"])
	require.Equal(t, "warning", productWarnings[0].Payload["phase"])
	require.Equal(t, DoomLoopWarningThreshold, productWarnings[0].Payload["warning_threshold"])
	advisoryFound := false
	for _, message := range provider.requests[2].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "Execution was not blocked") {
			advisoryFound = true
			break
		}
	}
	require.True(t, advisoryFound, "repeated calls should receive non-blocking model guidance")
}

func TestReActLoop_StopsRepeatedSemanticToolCallsAtConfiguredLimit(t *testing.T) {
	manager := &MockSequenceMCPManager{output: "unchanged evidence"}
	provider := &SequenceLLMProvider{name: "test-provider", responses: []*llm.LLMResponse{
		{Content: "inspect", ToolCalls: []types.ToolCall{{ID: "call-1", Name: "read_logs", Args: map[string]interface{}{"path": "app.log"}}}},
		{Content: "inspect again", ToolCalls: []types.ToolCall{{ID: "call-2", Name: "read_logs", Args: map[string]interface{}{"path": "app.log"}}}},
		{Content: "inspect again", ToolCalls: []types.ToolCall{{ID: "call-3", Name: "read_logs", Args: map[string]interface{}{"path": "app.log"}}}},
		{Content: "should not be reached"},
	}}
	runtime := llm.NewLLMRuntime(nil)
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{Name: "test-agent", Provider: "test-provider", Model: "test-model"}, manager, runtime)
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{
		MaxSteps:             10,
		MaxRepeatedToolCalls: 3,
		EnableToolCalls:      true,
	})
	bus := runtimeevents.NewBus()
	var terminations []runtimeevents.Event
	bus.Subscribe(EventDoomLoopTerminated, func(event runtimeevents.Event) {
		terminations = append(terminations, event)
	})
	agent.SetEventBus(bus)

	result, err := loop.Run(context.Background(), "inspect logs")
	require.NoError(t, err)
	require.True(t, result.LimitReached)
	require.Equal(t, "repeated_tool_calls", result.LimitReason)
	require.Equal(t, 3, provider.callCount)
	require.Len(t, result.Observations, 2, "the threshold call must not execute again")
	require.Len(t, terminations, 1)
	require.Equal(t, "terminated", terminations[0].Payload["phase"])
	require.Equal(t, 3, terminations[0].Payload["stop_limit"])
	require.Equal(t, "repeated_tool_calls", terminations[0].Payload["limit_reason"])
}

func TestReActLoop_StopsBeforeExceedingToolCallBudget(t *testing.T) {
	provider := &SequenceLLMProvider{name: "test-provider", responses: []*llm.LLMResponse{{
		Content: "read both",
		ToolCalls: []types.ToolCall{
			{ID: "call-1", Name: "read_logs", Args: map[string]interface{}{"path": "a.log"}},
			{ID: "call-2", Name: "read_logs", Args: map[string]interface{}{"path": "b.log"}},
		},
	}}}
	runtime := llm.NewLLMRuntime(nil)
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{Name: "test-agent", Provider: "test-provider", Model: "test-model"}, &MockSequenceMCPManager{}, runtime)
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{MaxSteps: 10, MaxToolCalls: 1, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "read logs")
	require.NoError(t, err)
	require.True(t, result.LimitReached)
	require.Equal(t, "tool_calls", result.LimitReason)
	require.Equal(t, 1, result.ToolCallLimit)
	require.Empty(t, result.Observations)
}

func TestReActLoop_MaxRunDurationCancelsBlockedProvider(t *testing.T) {
	provider := &BlockingLLMProvider{name: "test-provider", release: make(chan struct{}), entered: make(chan struct{}, 1)}
	runtime := llm.NewLLMRuntime(nil)
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{Name: "test-agent", Provider: "test-provider", Model: "test-model"}, nil, runtime)
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{MaxSteps: 10, MaxRunDuration: 50 * time.Millisecond})

	result, err := loop.Run(context.Background(), "wait forever")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, result)
	require.True(t, result.LimitReached)
	require.Equal(t, "run_timeout", result.LimitReason)
	require.False(t, agent.IsRunning())
}

func TestReActLoop_ParentDeadlineIsNotReportedAsRunDurationLimit(t *testing.T) {
	provider := &BlockingLLMProvider{name: "test-provider", release: make(chan struct{}), entered: make(chan struct{}, 1)}
	runtime := llm.NewLLMRuntime(nil)
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{Name: "test-agent", Provider: "test-provider", Model: "test-model"}, nil, runtime)
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{MaxSteps: 10, MaxRunDuration: time.Minute})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := loop.Run(ctx, "wait for caller deadline")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, result)
	require.False(t, result.LimitReached)
	require.Equal(t, "canceled", result.LimitReason)
	require.False(t, agent.IsRunning())
}

func TestProjectSimpleGoalToolSurfaceKeepsOnlyRelevantTools(t *testing.T) {
	tools := []types.ToolDefinition{
		{Name: "ls"}, {Name: "glob"}, {Name: "view"}, {Name: "shell"}, {Name: "bash"},
		{Name: "web_search"}, {Name: "spawn_team"}, {Name: "openai_image_generate"},
	}

	projected := projectSimpleGoalToolSurface("ls file", tools)
	require.Equal(t, []string{"ls", "glob"}, toolDefinitionNames(projected))

	projected = projectSimpleGoalToolSurface("git status", tools)
	require.Equal(t, []string{"shell", "bash"}, toolDefinitionNames(projected))

	projected = projectSimpleGoalToolSurface("inspect the repository and fix the failing tests", tools)
	require.Equal(t, toolDefinitionNames(tools), toolDefinitionNames(projected))
}

func TestSemanticToolCallFingerprintExemptsPollingTools(t *testing.T) {
	fingerprint := semanticToolCallFingerprint([]types.ToolCall{{
		ID:   "poll-1",
		Name: "background_task",
		Args: map[string]interface{}{"task_id": "task-1"},
	}})
	require.Empty(t, fingerprint)
}

func TestReActLoop_AdvisesAfterProlongedExplorationWithoutStopping(t *testing.T) {
	manager := &MockSequenceMCPManager{output: "inspection result"}
	llmRuntime := llm.NewLLMRuntime(nil)
	responses := make([]*llm.LLMResponse, 0, explorationStallNoticeThreshold+1)
	for index := 1; index <= explorationStallNoticeThreshold; index++ {
		responses = append(responses, &llm.LLMResponse{
			Content: "继续检查。",
			Model:   "test-model",
			ToolCalls: []types.ToolCall{{
				ID:   fmt.Sprintf("inspect-%d", index),
				Name: "view",
				Args: map[string]interface{}{
					"file_path": fmt.Sprintf("file-%d.go", index),
				},
			}},
		})
	}
	responses = append(responses, &llm.LLMResponse{Content: "已根据现有证据完成分析。", Model: "test-model"})
	provider := &SequenceLLMProvider{name: "test-provider", responses: responses}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "test-provider",
		Model:    "test-model",
		MaxSteps: 0,
	}, manager, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        0,
		EnableToolCalls: true,
	})
	bus := runtimeevents.NewBus()
	var observedEvents []runtimeevents.Event
	bus.Subscribe("tool_loop.exploration_stall_observed", func(event runtimeevents.Event) {
		observedEvents = append(observedEvents, event)
	})
	agent.SetEventBus(bus)

	result, err := loop.Run(context.Background(), "调查问题并在证据充分后处理")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "已根据现有证据完成分析。", result.Output)
	require.Len(t, provider.requests, explorationStallNoticeThreshold+1)
	require.Len(t, observedEvents, 1)
	require.Equal(t, explorationStallNoticeThreshold, observedEvents[0].Payload["consecutive_readonly_steps"])

	advisoryFound := false
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "only inspected or checked state") {
			advisoryFound = true
			break
		}
	}
	require.True(t, advisoryFound, "the next model turn should receive non-blocking progress guidance")
}

func TestNextExplorationStallCountResetsOnMutation(t *testing.T) {
	count := nextExplorationStallCount(4, []types.ToolCall{{Name: "grep"}, {Name: "bash", Args: map[string]interface{}{"mutated_paths": []interface{}{}}}})
	require.Equal(t, 5, count)

	count = nextExplorationStallCount(count, []types.ToolCall{{Name: "edit"}})
	require.Zero(t, count)

	count = nextExplorationStallCount(3, []types.ToolCall{{
		Name: "bash",
		Args: map[string]interface{}{"mutated_paths": []interface{}{"backend/internal/agent/loop.go"}},
	}})
	require.Zero(t, count)
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
			"context_max_prompt_tokens":    1350,
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
	require.Equal(t, 0, intValue(provider.requests[0].Metadata["prompt_cache_epoch"]))
	require.Equal(t, 0, intValue(provider.requests[1].Metadata["prompt_cache_epoch"]))
	require.Equal(t, 1, intValue(provider.requests[2].Metadata["prompt_cache_epoch"]))
	require.NotEqual(t,
		provider.requests[0].Metadata["prompt_cache_key"],
		provider.requests[2].Metadata["prompt_cache_key"],
	)

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
	// Active-turn replay compaction is an intentional cache epoch break: the third
	// request may rewrite earlier tool traffic, so exact-prefix reuse ends here.
	require.Equal(t, true, preflight["prompt_cache_epoch_break"])
	require.Equal(t, "active_turn_replay_compaction", preflight["prompt_cache_epoch_reason"])
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
			"context_max_prompt_tokens":    1350,
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

func TestReActLoop_MidTurnSemanticCompactionContinuesWithoutReplacingDurableHistory(t *testing.T) {
	largeResult := "ROOT_CAUSE_CONFIRMED\n" + strings.Repeat("verified tool evidence line\n", 500)
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		defaultModel: "test-model",
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"test-model": {
				MaxContextTokens:      10000,
				AutoCompactTokenLimit: 2500,
			},
		},
		responses: []*llm.LLMResponse{
			{
				Content: "Inspect the failing log.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call-mid-turn", Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"},
				}},
			},
			{
				Content: "Semantic checkpoint: the root cause is confirmed; apply the targeted fix and run tests.",
				Model:   "test-model",
			},
			{
				Content: "Fix completed after the semantic checkpoint.",
				Model:   "test-model",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		SystemPrompt:     "Current canonical instructions.",
	}, &MockSequenceMCPManager{output: largeResult}, llmRuntime)
	bus := runtimeevents.NewBus()
	var startedEvents []runtimeevents.Event
	var completedEvents []runtimeevents.Event
	bus.Subscribe("context.mid_turn_compact.started", func(event runtimeevents.Event) {
		startedEvents = append(startedEvents, event)
	})
	bus.Subscribe("context.mid_turn_compact.completed", func(event runtimeevents.Event) {
		completedEvents = append(completedEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 0, EnableToolCalls: true})
	session := newTestHistorySession("session-mid-turn-semantic")
	result, err := loop.RunWithSession(context.Background(), "Find and fix the build failure.", session)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "Fix completed after the semantic checkpoint.", result.Output)
	require.Len(t, provider.requests, 3)
	require.NotEmpty(t, provider.requests[1].Tools, "semantic compaction should retain the frozen tools prefix for prompt cache")
	require.Equal(t, false, provider.requests[1].Metadata[llm.MetadataKeyDisableTools])
	require.Equal(t, "none", provider.requests[1].Metadata["tool_choice"])
	require.Contains(t, provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content, "CONTEXT CHECKPOINT COMPACTION")

	continuationPrompt := provider.requests[2].Messages
	require.Contains(t, joinedMessageContents(continuationPrompt), "Find and fix the build failure.")
	require.Contains(t, joinedMessageContents(continuationPrompt), "Semantic checkpoint: the root cause is confirmed")
	require.Len(t, startedEvents, 1)
	require.Len(t, completedEvents, 1)
	require.Equal(t, compactruntime.PhaseMidTurn, completedEvents[0].Payload["phase"])
	require.Equal(t, false, completedEvents[0].Payload["durable_history_replaced"])
	require.Equal(t, "provider", completedEvents[0].Payload["summary_source"])
	require.Equal(t, true, completedEvents[0].Payload["semantic_checkpoint"])

	durable := session.GetMessages()
	require.Contains(t, joinedMessageContents(durable), "ROOT_CAUSE_CONFIRMED")
	require.Contains(t, joinedMessageContents(durable), "Fix completed after the semantic checkpoint.")
	for _, message := range durable {
		require.NotEqual(t, "compaction", message.Metadata.GetString("context_stage", ""))
	}
}

func TestReActLoop_PreTurnSemanticCompactionRunsBeforeFirstModelDecision(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		defaultModel: "test-model",
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"test-model": {MaxContextTokens: 10000, AutoCompactTokenLimit: 1200},
		},
		responses: []*llm.LLMResponse{
			{Content: "Pre-turn semantic checkpoint: preserve the original goal and continue from verified evidence.", Model: "test-model"},
			{Content: "Completed from the pre-turn semantic checkpoint.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "test-agent", Provider: "test-provider", Model: "test-model",
		DefaultMaxTokens: 256, SystemPrompt: "Current canonical instructions.",
	}, nil, llmRuntime)
	bus := runtimeevents.NewBus()
	var completedEvents []runtimeevents.Event
	bus.Subscribe("context.pre_turn_compact.completed", func(event runtimeevents.Event) {
		completedEvents = append(completedEvents, event)
	})
	agent.SetEventBus(bus)

	session := newTestHistorySession("session-pre-turn-semantic")
	session.messages = append(session.messages, *types.NewUserMessage("Finish the original long-running task without losing its constraints."))
	for index := 0; index < 12; index++ {
		callID := fmt.Sprintf("pre-call-%d", index)
		assistant := types.NewAssistantMessage(fmt.Sprintf("verified progress %d", index))
		assistant.ToolCalls = []types.ToolCall{{ID: callID, Name: "inspect", Args: map[string]interface{}{"index": index}}}
		session.messages = append(session.messages, *assistant, *types.NewToolMessage(callID, strings.Repeat(fmt.Sprintf("evidence-%d ", index), 60)))
	}
	durableBefore := len(session.messages)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 0, EnableToolCalls: false})
	result, err := loop.ContinueWithSession(context.Background(), session)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "Completed from the pre-turn semantic checkpoint.", result.Output)
	require.Len(t, provider.requests, 2)
	require.Contains(t, provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content, "CONTEXT CHECKPOINT COMPACTION")
	require.Contains(t, joinedMessageContents(provider.requests[1].Messages), "Finish the original long-running task")
	require.Contains(t, joinedMessageContents(provider.requests[1].Messages), "Pre-turn semantic checkpoint")
	require.Len(t, completedEvents, 1)
	require.Equal(t, compactruntime.PhasePreTurn, completedEvents[0].Payload["phase"])
	require.Len(t, session.GetMessages(), durableBefore+1)
	for _, message := range session.GetMessages() {
		require.NotEqual(t, "compaction", message.Metadata.GetString("context_stage", ""))
	}
}

func joinedMessageContents(messages []types.Message) string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}
	return strings.Join(contents, "\n")
}

func TestReActLoop_ReusesCompactedPromptViewAcrossLongActiveTurn(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "检查最后一个状态。",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call_latest", Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"},
				}},
			},
			{Content: "已完成分析。", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
		Options: map[string]interface{}{
			"context_max_prompt_tokens": 120000,
		},
	}, &MockSequenceMCPManager{output: "latest log"}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 0, EnableToolCalls: true})
	session := newTestHistorySession("session-long-prompt-view")
	session.messages = append(session.messages, *types.NewUserMessage("find the root cause and fix it"))
	for index := 0; index < 500; index++ {
		callID := fmt.Sprintf("call_%d", index)
		assistant := types.NewAssistantMessage("继续检查")
		assistant.ToolCalls = []types.ToolCall{{ID: callID, Name: "read_logs", Args: map[string]interface{}{"path": "logs/app.log"}}}
		session.messages = append(session.messages, *assistant, *types.NewToolMessage(callID, "LOG "+large))
	}

	result, err := loop.ContinueWithSession(context.Background(), session)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 2)

	firstRaw, firstOK := provider.requests[0].Metadata["context_preflight"]
	require.True(t, firstOK, "first request metadata: %#v", provider.requests[0].Metadata)
	firstPreflight, firstOK := firstRaw.(map[string]interface{})
	require.True(t, firstOK, "first preflight metadata: %#v", firstRaw)
	secondRaw, secondOK := provider.requests[1].Metadata["context_preflight"]
	require.True(t, secondOK, "second request metadata: %#v", provider.requests[1].Metadata)
	secondPreflight, secondOK := secondRaw.(map[string]interface{})
	require.True(t, secondOK, "second preflight metadata: %#v", secondRaw)
	firstTokens := intValue(firstPreflight["message_tokens_before"])
	secondTokens := intValue(secondPreflight["message_tokens_before"])
	require.Greater(t, firstTokens, 100000)
	require.Less(t, secondTokens, 10000)
	require.Less(t, secondTokens*10, firstTokens)

	// Prompt compaction is an execution view; durable history remains available
	// for audit, explicit session compaction, and later artifact recovery.
	require.Greater(t, len(session.GetMessages()), 1000)
	providerMessages := provider.requests[1].Messages
	require.Equal(t, "find the root cause and fix it", providerMessages[1].Content)
	require.True(t, providerMessages[2].Metadata.GetBool("active_turn_compaction", false))
}

func TestMergeConfiguredSystemPromptReplacesExtendedPrefixWithoutDuplication(t *testing.T) {
	base := "Base instructions.\n\nEnvironment context."
	extended := base + "\n\nCurrent workspace root: E:\\workspace"
	original := types.NewSystemMessage(base)
	original.Metadata["prompt_layer"] = "base"
	history := []types.Message{*original, *types.NewUserMessage("fix it")}

	merged := mergeConfiguredSystemPrompt(history, extended)
	require.Len(t, merged, 2)
	require.Equal(t, "system", merged[0].Role)
	require.Equal(t, base, merged[0].Content)
	require.Equal(t, "base", merged[0].Metadata.GetString("prompt_layer", ""))
	require.Equal(t, base, history[0].Content, "source history must not be mutated")
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

func TestReActLoop_EnforcePromptPreflight_CountsToolSchemas(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{name: "test-provider"}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 128,
		Options: map[string]interface{}{
			"context_max_prompt_tokens": 300,
		},
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{})
	tools := []types.ToolDefinition{{
		Name:        "large_schema_tool",
		Description: strings.Repeat("schema description ", 100),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"input": map[string]interface{}{"type": "string"},
			},
		},
	}}

	_, metadata, err := loop.enforcePromptPreflightWithTools(
		"trace-tools",
		"session-tools",
		1,
		[]types.Message{*types.NewUserMessage("run the tool")},
		tools,
		0,
	)
	require.Error(t, err)
	preflightErr, ok := AsPromptPreflightError(err)
	require.True(t, ok)
	require.Equal(t, "tool_schema_exceeds_budget", preflightErr.Code)
	require.Greater(t, metadata["tool_schema_tokens"].(int), 300)
	require.Equal(t, 1, metadata["tool_count"])
}

func TestEstimatePromptMessageTokensUsesCompleteRuntimeCount(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	messages := []types.Message{{
		Role: "assistant",
		ContentParts: []types.ContentPart{{
			Type: types.ContentPartText,
			Text: strings.Repeat("structured content ", 40),
		}},
		ToolCalls: []types.ToolCall{{
			ID:   "call-large",
			Name: "write_file",
			Args: map[string]interface{}{"content": strings.Repeat("payload ", 80)},
		}},
		Metadata: types.NewMetadata(),
	}}

	flat := runtime.CountMessagesTokens([]types.Message{{Role: "assistant", Metadata: types.NewMetadata()}})
	counted := runtime.CountMessagesTokens(messages)
	estimated := estimatePromptMessageTokens(runtime, messages)
	require.Equal(t, counted, estimated)
	require.Greater(t, counted-flat, 100)
}

func TestCompactRecoveryMessageTokenLimitReservesToolSchema(t *testing.T) {
	require.Equal(t, 800, compactRecoveryMessageTokenLimit(map[string]interface{}{
		"prompt_budget":      1000,
		"tool_schema_tokens": 200,
	}))
	require.Equal(t, 1, compactRecoveryMessageTokenLimit(map[string]interface{}{
		"prompt_budget":      100,
		"tool_schema_tokens": 120,
	}))
	require.Zero(t, compactRecoveryMessageTokenLimit(nil))
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
			"context_max_prompt_tokens":    700,
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
	require.Equal(t, 700, preflightErr.PromptBudget)
	require.Equal(t, false, preflightErr.ActiveTurnCompacted)
	// Recovery may compact more than once while each replacement still shrinks
	// the history, then returns once compaction can no longer make progress.
	require.GreaterOrEqual(t, len(provider.requests), 2)
	require.NotEmpty(t, failedEvents)
	payload := failedEvents[len(failedEvents)-1].Payload
	require.Equal(t, "active-turn replay cannot be compacted further", payload["failure_reason"])
	require.Equal(t, "active_turn_not_compactable", payload["failure_reason_code"])
	require.Equal(t, false, payload["can_retry_after_compaction"])
	require.NotNil(t, payload["active_turn_message_count"])
	require.NotNil(t, payload["latest_replay_block_message_count"])
}

func TestReActLoop_RunWithSession_AutoCompactionRecoveryContinuesAfterPromptPreflightFailure(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 400)
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
			"context_max_prompt_tokens":    1500,
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
			Name:             "test-agent",
			Provider:         "test-provider",
			Model:            "test-model",
			DefaultMaxTokens: 4096,
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.Budget{
				MaxPromptTokens: 12000,
			},
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, 7500, budget.PromptBudget)
	require.Equal(t, "model_capability_context_ratio", budget.BudgetSource)
	require.Equal(t, 10000, budget.ModelCapabilityMaxContextTokens)
	require.InDelta(t, 0.75, budget.ModelCapabilityAutoCompactRatio, 0.001)
	require.Equal(t, 200000, budget.ProviderContextLimit)
	require.Equal(t, 8192, budget.ProviderOutputLimit)
	require.Equal(t, 4096, budget.ReservedOutputTokens)
	require.Equal(t, 5904, budget.EffectiveInputBudget)
}

func TestEstimatePromptTokenBreakdownSeparatesToolResults(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{})
	historyTokens, toolResultTokens := estimatePromptTokenBreakdown(llmRuntime, []types.Message{
		*types.NewSystemMessage("system instructions"),
		*types.NewUserMessage("user request"),
		*types.NewToolMessage("call-1", strings.Repeat("tool output ", 40)),
	})
	require.Positive(t, historyTokens)
	require.Positive(t, toolResultTokens)
	require.Equal(t, historyTokens+toolResultTokens, estimatePromptMessageTokens(llmRuntime, []types.Message{
		*types.NewSystemMessage("system instructions"),
		*types.NewUserMessage("user request"),
		*types.NewToolMessage("call-1", strings.Repeat("tool output ", 40)),
	}))
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

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, 200000, budget.PromptBudget)
	require.Equal(t, "model_capability_auto_compact_token_limit", budget.BudgetSource)
	require.Equal(t, 200000, budget.ModelCapabilityAutoCompactTokenLimit)
	require.NotContains(t, budget.BudgetCandidates, "default_context_max_prompt_tokens")
}

func TestResolvePromptPreflightBudget_ContextProfileDoesNotConstrainKnownCapability(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "mimo_anthropic",
		DefaultModel:    "mimo-v2.5-pro",
	})
	provider := &SequenceLLMProvider{
		name: "mimo_anthropic",
		providerCaps: &llm.ModelCapabilities{
			MaxContextTokens: 128000,
			MaxOutputTokens:  8192,
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"mimo-v2.5-pro": {
				MaxContextTokens: 1000000,
				AutoCompactRatio: 0.9,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("mimo_anthropic", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("mimo-v2.5-pro", "mimo_anthropic"))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "mimo_anthropic",
			Model:    "mimo-v2.5-pro",
			Options: map[string]interface{}{
				"context_profile": contextmgr.BudgetProfileBalanced,
			},
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.DefaultBudget(),
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, 900000, budget.PromptBudget)
	require.Equal(t, "model_capability_context_ratio", budget.BudgetSource)
	require.Equal(t, 1000000, budget.ModelCapabilityMaxContextTokens)
	require.InDelta(t, 0.9, budget.ModelCapabilityAutoCompactRatio, 0.001)
	require.NotContains(t, budget.BudgetCandidates, "context_max_prompt_tokens")
	require.NotContains(t, budget.BudgetCandidates, "default_context_max_prompt_tokens")
}

func TestResolveContextBuildPromptBudget_ContextProfileDoesNotConstrainKnownCapability(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "mimo_anthropic",
		DefaultModel:    "mimo-v2.5-pro",
	})
	provider := &SequenceLLMProvider{
		name: "mimo_anthropic",
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"mimo-v2.5-pro": {
				MaxContextTokens: 1000000,
				AutoCompactRatio: 0.9,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("mimo_anthropic", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("mimo-v2.5-pro", "mimo_anthropic"))

	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "mimo_anthropic",
		Model:    "mimo-v2.5-pro",
		Options: map[string]interface{}{
			"context_profile": contextmgr.BudgetProfileBalanced,
		},
	}, &MockMCPManager{}, llmRuntime)

	contextBudget := resolveContextBuildPromptBudget(llmRuntime, agent, nil)
	require.Equal(t, 900000, contextBudget.PromptBudget)
	require.Equal(t, "model_capability_context_ratio", contextBudget.BudgetSource)

	manager := agent.GetContextManager()
	require.NotNil(t, manager)
	result := manager.Build(context.Background(), contextmgr.BuildInput{
		History:                  []types.Message{*types.NewUserMessage(strings.Repeat("abcd", 15000))},
		CountTokens:              llmRuntime.CountMessagesTokens,
		PromptBudget:             contextBudget.PromptBudget,
		PromptBudgetSource:       contextBudget.BudgetSource,
		PromptBudgetSourceDetail: contextBudget.BudgetSourceDetail,
	})

	require.Len(t, result.Messages, 1)
	require.Equal(t, 900000, result.Metadata["budget_max_prompt_tokens"])
	require.Equal(t, "model_capability_context_ratio", result.Metadata["budget_max_prompt_tokens_source"])
	require.Equal(t, 12000, result.Metadata["budget_profile_max_prompt_tokens"])
}

func TestReActLoop_ThinkPreservesOlderHistoryWithCapabilityBuildBudget(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "mimo_anthropic",
		DefaultModel:    "mimo-v2.5-pro",
	})
	provider := &SequenceLLMProvider{
		name: "mimo_anthropic",
		responses: []*llm.LLMResponse{
			{Content: "继续处理。", Model: "mimo-v2.5-pro"},
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"mimo-v2.5-pro": {
				MaxContextTokens: 1000000,
				AutoCompactRatio: 0.9,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("mimo_anthropic", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("mimo-v2.5-pro", "mimo_anthropic"))

	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "mimo_anthropic",
		Model:    "mimo-v2.5-pro",
		Options: map[string]interface{}{
			"context_profile": contextmgr.BudgetProfileBalanced,
		},
	}, &MockMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 1})
	objective := "original objective: inspect docs and fix stale content " + strings.Repeat("abcd", 15000)
	history := []types.Message{
		*types.NewUserMessage(objective),
		*types.NewAssistantMessage("ack"),
		*types.NewUserMessage("继续"),
	}

	_, _, _, err := loop.think(context.Background(), "trace-capability-build", "session-capability-build", 1, "继续", history, nil, nil, 0, 0)
	require.NoError(t, err)
	require.Len(t, provider.requests, 1)
	foundObjective := false
	for _, message := range provider.requests[0].Messages {
		if strings.Contains(message.Content, "original objective: inspect docs") {
			foundObjective = true
			break
		}
	}
	require.True(t, foundObjective, "expected context manager to preserve older objective under capability budget")
}

func TestResolvePromptPreflightBudget_UsesConfigurableFallbackForUnknownCapability(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		providerCaps: &llm.ModelCapabilities{},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "test-provider",
		Model:    "test-model",
		Options: map[string]interface{}{
			"context_fallback_max_prompt_tokens": 32000,
		},
	}, &MockMCPManager{}, llmRuntime)

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, 32000, budget.PromptBudget)
	require.Equal(t, "context_fallback_max_prompt_tokens", budget.BudgetSource)
	require.Equal(t, 12000, agent.GetContextManager().Budget.MaxPromptTokens)
	require.NotContains(t, budget.BudgetCandidates, "default_context_max_prompt_tokens")
}

func TestResolvePromptPreflightBudget_UsesDefaultFallbackForUnknownCapability(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		providerCaps: &llm.ModelCapabilities{},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "test-provider",
		Model:    "test-model",
	}, &MockMCPManager{}, llmRuntime)

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, contextmgr.DefaultFallbackMaxPromptTokens, budget.PromptBudget)
	require.Equal(t, "default_context_fallback_max_prompt_tokens", budget.BudgetSource)
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
			Budget: contextmgr.Budget{MaxPromptTokens: 50000},
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
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

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, 6800, budget.PromptBudget)
	require.Equal(t, "provider_context_limit_default_ratio", budget.BudgetSource)
	require.Equal(t, 8000, budget.ProviderContextLimit)
	require.Equal(t, 2048, budget.ProviderOutputLimit)
}

func TestResolvePromptPreflightBudget_UsesProviderContextLimitWhenWildcardCapabilityHasNoLimit(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "gpt-5.5",
	})
	provider := &SequenceLLMProvider{
		name: "test-provider",
		providerCaps: &llm.ModelCapabilities{
			MaxContextTokens: 128000,
			MaxOutputTokens:  4096,
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"*": {
				ReasoningModel: true,
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("gpt-5.5", "test-provider"))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "gpt-5.5",
			Options: map[string]interface{}{
				"context_fallback_max_prompt_tokens": 32000,
			},
		},
		contextMgr: &contextmgr.Manager{
			Budget: contextmgr.DefaultBudget(),
		},
	}

	budget := resolvePromptPreflightBudget(llmRuntime, agent, nil, 0)
	require.Equal(t, 108800, budget.PromptBudget)
	require.Equal(t, "provider_context_limit_default_ratio", budget.BudgetSource)
	require.Equal(t, 128000, budget.ProviderContextLimit)
	require.Equal(t, 4096, budget.ProviderOutputLimit)
	require.Equal(t, 0, budget.ModelCapabilityMaxContextTokens)
	require.Contains(t, budget.BudgetCandidates, "provider_context_limit_default_ratio")
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

	assert.Nil(t, checkpoint.Metadata["files"])
	files, err := store.GetCheckpointFiles(context.Background(), checkpoint.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, filepath.Clean(targetPath), filepath.Clean(files[0].Path))
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

	assert.Nil(t, checkpoint.Metadata["files"])
	files, err := store.GetCheckpointFiles(context.Background(), checkpoint.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, filepath.Clean(targetPath), filepath.Clean(files[0].Path))
	assert.NotEmpty(t, files[0].BeforeBlobID)
	assert.Empty(t, files[0].AfterBlobID)
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
	projected := make(chan BatchTerminalLifecycle, 1)
	agent.SetBatchLifecycleProjector(func(_ context.Context, event BatchTerminalLifecycle) error {
		projected <- event
		return nil
	})

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
	select {
	case event := <-projected:
		require.Equal(t, subagentbatch.ExecutionModeWait, event.ExecutionMode)
		require.Equal(t, subagentbatch.BatchCompleted, event.Status)
		require.NotEmpty(t, event.ParentSessionID)
		require.Equal(t, event.ParentSessionID, event.RootScopeID)
		require.Equal(t, 1, event.CompletedCount)
	case <-time.After(5 * time.Second):
		t.Fatal("expected synchronous subagent terminal lifecycle projection")
	}
}

func TestDecodeSubagentTasksReadsRoutingFields(t *testing.T) {
	tasks, err := decodeSubagentTasks(map[string]interface{}{
		"agents": []interface{}{
			map[string]interface{}{
				"id":                     "child-1",
				"role":                   "verifier",
				"goal":                   "Verify the implementation.",
				"difficulty":             "hard",
				"difficulty_rationale":   "Touches provider routing.",
				"provider":               "local-strong",
				"model":                  "strong-model",
				"thinking_effort":        "high",
				"read_only":              true,
				"completion_requirement": "complete_task",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "hard", tasks[0].Difficulty)
	assert.Equal(t, "Touches provider routing.", tasks[0].DifficultyRationale)
	assert.Equal(t, "local-strong", tasks[0].Provider)
	assert.Equal(t, "strong-model", tasks[0].Model)
	assert.Equal(t, "high", tasks[0].ReasoningEffort)
	assert.Equal(t, "complete_task", tasks[0].CompletionRequirement)
	assert.Contains(t, tasks[0].RouteWarnings, "thinking_effort_alias_used")
}

func TestDecodeSubagentTasksReadsCompletionRequirementCamelCase(t *testing.T) {
	tasks, err := decodeSubagentTasks(map[string]interface{}{
		"agents": []interface{}{
			map[string]interface{}{
				"id":                    "child-2",
				"goal":                  "Finish with outcome.",
				"completionRequirement": "complete_task",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "complete_task", tasks[0].CompletionRequirement)
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
			ID:       "child-1",
			Role:     "researcher",
			Goal:     "Inspect the logs.",
			ReadOnly: true,
			// Keep budget above subagent prompt size so preflight compact does not
			// consume the only SequenceLLMProvider response before the model turn.
			BudgetTokens: 8192,
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
	req := firstNonCompactLLMRequest(provider.requests)
	require.NotNil(t, req, "expected a non-compact child LLM request, got %d request(s)", len(provider.requests))
	assert.Equal(t, 8192, req.MaxTokens)
}

func TestSubagentScheduler_RunChildren_RoutesByDifficulty(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Provider:         "parent-provider",
			Model:            "parent-model",
			MaxSteps:         3,
			DefaultMaxTokens: 512,
			SystemPrompt:     "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	parentProvider := &SequenceLLMProvider{name: "parent-provider"}
	hardProvider := &SequenceLLMProvider{
		name: "hard-provider",
		responses: []*llm.LLMResponse{
			{Content: "Hard child summary.", Model: "hard-model", Usage: &types.TokenUsage{TotalTokens: 17}},
		},
		modelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"hard-model": {ReasoningModel: true, ReasoningEfforts: []string{"high"}},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", parentProvider))
	require.NoError(t, llmRuntime.RegisterProvider("hard-provider", hardProvider))
	agent.llmRuntime = llmRuntime

	enabled := true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"hard": {
					Provider:        "hard-provider",
					Model:           "hard-model",
					ReasoningEffort: "high",
					MaxTokens:       4096,
				},
			},
		},
	})

	bus := runtimeevents.NewBus()
	var startPayload map[string]interface{}
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startPayload = event.Payload
	})
	agent.SetEventBus(bus)
	hookPayloads := make(chan map[string]interface{}, 4)
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]interface{}{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			payload["_decode_error"] = err.Error()
		}
		payload["_hook_path"] = strings.TrimPrefix(r.URL.Path, "/")
		hookPayloads <- payload
		_, _ = w.Write([]byte(`{"action":"continue"}`))
	}))
	defer hookServer.Close()
	agent.SetHookManager(runtimehooks.NewManager([]runtimehooks.HookConfig{
		{
			ID:    "subagent-start-route-audit",
			Event: runtimehooks.EventSubagentStart,
			Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL + "/start", Method: http.MethodPost},
		},
		{
			ID:    "subagent-stop-route-audit",
			Event: runtimehooks.EventSubagentStop,
			Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL + "/stop", Method: http.MethodPost},
		},
	}))

	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{
		TraceID:         "trace_route",
		ParentSessionID: "parent-session",
		Depth:           1,
	}, []SubagentTask{
		{
			ID:                  "hard-child",
			Role:                "researcher",
			Goal:                "Analyze the provider migration.",
			Difficulty:          "hard",
			DifficultyRationale: "Cross-provider behavior.",
			BudgetTokens:        8192,
			ReadOnly:            true,
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Len(t, hardProvider.requests, 1)
	assert.Equal(t, "hard-provider", hardProvider.requests[0].Provider)
	assert.Equal(t, "hard-model", hardProvider.requests[0].Model)
	assert.Equal(t, "high", hardProvider.requests[0].ReasoningEffort)
	assert.Equal(t, 4096, hardProvider.requests[0].MaxTokens)
	assert.Contains(t, hardProvider.requests[0].Messages[0].Content, "Subtask difficulty: hard.")
	assert.Contains(t, hardProvider.requests[0].Messages[0].Content, "Runtime routing: provider=hard-provider, model=hard-model, reasoning_effort=high, source=difficulty_level.")
	require.NotNil(t, startPayload)
	assert.Equal(t, "hard", startPayload["difficulty"])
	assert.Equal(t, "explicit", startPayload["difficulty_source"])
	assert.Equal(t, "hard-provider", startPayload["route_provider"])
	assert.Equal(t, "hard-model", startPayload["route_model"])
	assert.Equal(t, "high", startPayload["route_reasoning_effort"])
	warnings, ok := startPayload["route_warnings"].([]string)
	require.True(t, ok)
	assert.Contains(t, warnings, "budget_tokens_capped_by_route")
	hookStartPayload, hookStopPayload := waitForSubagentHookPayloads(t, hookPayloads)
	assert.Equal(t, "hard", hookStartPayload["difficulty"])
	assert.Equal(t, "hard-provider", hookStartPayload["route_provider"])
	assert.Equal(t, "hard-model", hookStartPayload["route_model"])
	assert.Equal(t, "difficulty_level", hookStartPayload["route_source"])
	assert.Equal(t, "hard", hookStopPayload["difficulty"])
	assert.Equal(t, "hard-provider", hookStopPayload["route_provider"])
	assert.Equal(t, "hard-model", hookStopPayload["route_model"])
	assert.Equal(t, float64(17), hookStopPayload["usage_total_tokens"])
}

func waitForSubagentHookPayloads(t *testing.T, payloads <-chan map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	var startPayload map[string]interface{}
	var stopPayload map[string]interface{}
	deadline := time.After(2 * time.Second)
	for startPayload == nil || stopPayload == nil {
		select {
		case payload := <-payloads:
			require.Empty(t, payload["_decode_error"])
			switch payload["_hook_path"] {
			case "start":
				startPayload = payload
			case "stop":
				stopPayload = payload
			}
		case <-deadline:
			t.Fatalf("timed out waiting for subagent hook payloads: start=%#v stop=%#v", startPayload, stopPayload)
		}
	}
	return startPayload, stopPayload
}

func TestSubagentScheduler_RunChildren_ProviderOnlyRouteUsesProviderDefaultModel(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Provider:         "parent-provider",
			Model:            "parent-model",
			MaxSteps:         3,
			DefaultMaxTokens: 512,
			SystemPrompt:     "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	parentProvider := &SequenceLLMProvider{name: "parent-provider"}
	hardProvider := &SequenceLLMProvider{
		name:         "hard-provider",
		defaultModel: "hard-default-model",
		responses: []*llm.LLMResponse{
			{Content: "Hard child summary.", Model: "hard-default-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", parentProvider))
	require.NoError(t, llmRuntime.RegisterProvider("hard-provider", hardProvider))
	agent.llmRuntime = llmRuntime

	enabled := true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"hard": {Provider: "hard-provider"},
			},
		},
	})

	bus := runtimeevents.NewBus()
	var startPayload map[string]interface{}
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startPayload = event.Payload
	})
	agent.SetEventBus(bus)

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{ID: "hard-child", Goal: "Analyze provider migration.", Difficulty: "hard", ReadOnly: true},
	})
	require.NoError(t, err)
	require.Len(t, hardProvider.requests, 1)
	assert.Equal(t, "hard-provider", hardProvider.requests[0].Provider)
	assert.Equal(t, "hard-default-model", hardProvider.requests[0].Model)
	require.NotNil(t, startPayload)
	assert.Equal(t, "hard-default-model", startPayload["route_model"])
	warnings, ok := startPayload["route_warnings"].([]string)
	require.True(t, ok)
	assert.Contains(t, warnings, "model_default_provider")
}

func TestSubagentScheduler_RunChildren_RoutingDisabledPreservesLegacyModelOnlyOverride(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Provider:         "parent-provider",
			Model:            "parent-model",
			MaxSteps:         3,
			DefaultMaxTokens: 512,
			SystemPrompt:     "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  nil,
	}

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	parentProvider := &SequenceLLMProvider{
		name: "parent-provider",
		responses: []*llm.LLMResponse{
			{Content: "Legacy child summary.", Model: "legacy-child-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", parentProvider))
	agent.llmRuntime = llmRuntime

	disabled := false
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled:           &disabled,
			DefaultDifficulty: "not-a-real-difficulty",
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"hard": {Provider: "hard-provider", Model: "hard-model", ReasoningEffort: "high"},
			},
		},
	})

	bus := runtimeevents.NewBus()
	var startPayload map[string]interface{}
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startPayload = event.Payload
	})
	agent.SetEventBus(bus)

	// Budget is large enough that prompt preflight does not spend the first
	// SequenceLLMProvider response on active-turn compaction. This test is about
	// routing inheritance, not compact budgets.
	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{
			ID:              "legacy-child",
			Goal:            "Inspect legacy route behavior.",
			Provider:        "ignored-provider",
			Model:           "legacy-child-model",
			ReasoningEffort: "high",
			BudgetTokens:    8192,
			ReadOnly:        true,
		},
	})
	require.NoError(t, err)
	req := firstNonCompactLLMRequest(parentProvider.requests)
	require.NotNil(t, req, "results=%+v requests=%d", results, len(parentProvider.requests))
	assert.Equal(t, "parent-provider", req.Provider)
	assert.Equal(t, "legacy-child-model", req.Model)
	assert.Equal(t, "", req.ReasoningEffort)
	assert.Equal(t, 8192, req.MaxTokens)
	require.NotNil(t, startPayload)
	assert.Equal(t, "disabled", startPayload["route_source"])
}

func TestSubagentScheduler_RunChildren_EmitsInvalidDifficultyWarning(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "parent-provider",
			Model:        "parent-model",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name: "parent-provider",
		responses: []*llm.LLMResponse{
			{Content: "Child summary.", Model: "parent-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", provider))
	agent.llmRuntime = llmRuntime

	enabled := true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled:           &enabled,
			DefaultDifficulty: "normal",
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"normal": {Provider: "parent-provider", Model: "parent-model"},
			},
		},
	})

	bus := runtimeevents.NewBus()
	var startPayload map[string]interface{}
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startPayload = event.Payload
	})
	agent.SetEventBus(bus)

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{ID: "child", Goal: "Inspect behavior.", Difficulty: "复杂", ReadOnly: true},
	})
	require.NoError(t, err)
	require.NotNil(t, startPayload)
	warnings, ok := startPayload["route_warnings"].([]string)
	require.True(t, ok)
	assert.Contains(t, warnings, "difficulty_invalid_defaulted")
}

func TestSubagentScheduler_RunChildren_UnavailableRouteProviderFallsBackToParentRequest(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "parent-provider",
			Model:        "parent-model",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	parentProvider := &SequenceLLMProvider{
		name: "parent-provider",
		responses: []*llm.LLMResponse{
			{Content: "Fallback child summary.", Model: "parent-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", parentProvider))
	agent.llmRuntime = llmRuntime

	enabled := true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"hard": {Provider: "missing-provider", Model: "missing-model"},
			},
		},
	})

	bus := runtimeevents.NewBus()
	var startPayload map[string]interface{}
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startPayload = event.Payload
	})
	agent.SetEventBus(bus)

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{ID: "child", Goal: "Inspect fallback.", Difficulty: "hard", ReadOnly: true},
	})
	require.NoError(t, err)
	require.Len(t, parentProvider.requests, 1)
	assert.Equal(t, "parent-provider", parentProvider.requests[0].Provider)
	assert.Equal(t, "parent-model", parentProvider.requests[0].Model)
	require.NotNil(t, startPayload)
	assert.Equal(t, "fallback", startPayload["route_source"])
	assert.Equal(t, true, startPayload["fallback_used"])
	assert.Equal(t, "provider_unresolved_parent", startPayload["fallback_reason"])
	warnings, ok := startPayload["route_warnings"].([]string)
	require.True(t, ok)
	assert.Contains(t, warnings, "provider_unresolved")
	assert.Contains(t, warnings, "provider_fallback_parent")
	assert.Contains(t, warnings, "model_fallback_parent")
}

func TestSubagentScheduler_RunChildren_BlocksExplicitHardWriterWithoutVerifier(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "parent-provider",
			Model:        "parent-model",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1})

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{
			ID:             "writer",
			Role:           "writer",
			Goal:           "Write provider migration.",
			Difficulty:     "hard",
			ReadOnly:       false,
			ToolsWhitelist: []string{"write_file"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a read-only verifier dependency")
}

func TestSubagentScheduler_RunChildren_LimitsExpertConcurrency(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "expert-provider",
			Model:        "expert-model",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	release := make(chan struct{})
	provider := &BlockingLLMProvider{
		name:    "expert-provider",
		release: release,
		entered: make(chan struct{}, 2),
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "expert-provider",
		DefaultModel:    "expert-model",
		MaxRetries:      0,
	})
	require.NoError(t, llmRuntime.RegisterProvider("expert-provider", provider))
	agent.llmRuntime = llmRuntime

	enabled := true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 2,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled:              &enabled,
			MaxExpertConcurrency: 1,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"expert": {Provider: "expert-provider", Model: "expert-model"},
			},
		},
	})

	done := make(chan error, 1)
	go func() {
		_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
			{ID: "expert-1", Goal: "Inspect expert path one.", Difficulty: "expert", ReadOnly: true},
			{ID: "expert-2", Goal: "Inspect expert path two.", Difficulty: "expert", ReadOnly: true},
		})
		done <- err
	}()

	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("expected first expert request to start")
	}
	select {
	case <-provider.entered:
		t.Fatal("expected second expert request to wait for expert concurrency slot")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for expert subagents")
	}
	assert.Equal(t, 2, provider.RequestCount())
	assert.Equal(t, 1, provider.MaxActive())
}

func TestSubagentScheduler_RunChildren_EmitsRoutingWarnings(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:             "test-agent",
			Provider:         "parent-provider",
			Model:            "parent-model",
			MaxSteps:         3,
			DefaultMaxTokens: 512,
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}

	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name: "parent-provider",
		responses: []*llm.LLMResponse{
			{Content: "Child summary.", Model: "parent-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", provider))
	agent.llmRuntime = llmRuntime

	enabled := true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"normal": {
					Provider:        "parent-provider",
					Model:           "parent-model",
					ReasoningEffort: "high",
				},
			},
		},
	})

	bus := runtimeevents.NewBus()
	var startPayload map[string]interface{}
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		startPayload = event.Payload
	})
	agent.SetEventBus(bus)

	_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{ID: "child", Goal: "Inspect behavior.", ReadOnly: true},
	})
	require.NoError(t, err)
	require.NotNil(t, startPayload)
	warnings, ok := startPayload["route_warnings"].([]string)
	require.True(t, ok)
	assert.Contains(t, warnings, "difficulty_missing_defaulted")
	assert.Contains(t, warnings, "reasoning_effort_capability_unknown")
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

func TestOptimizeModelToolSurfacePrefersCanonicalShellAndCompactsGrep(t *testing.T) {
	raw := []types.ToolDefinition{
		{Name: "shell", Description: "canonical shell", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "bash", Description: "compat shell", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "execute_shell_command", Description: strings.Repeat("legacy shell guidance ", 100), Parameters: map[string]interface{}{"type": "object"}},
		{
			Name:        "grep",
			Description: strings.Repeat("verbose grep guidance ", 100),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{"type": "string"},
					"paths":   map[string]interface{}{"type": "array"},
					"rg_args": map[string]interface{}{"type": "array"},
					"pretty":  map[string]interface{}{"type": "boolean", "description": strings.Repeat("verbose ", 100)},
				},
				"additionalProperties": false,
			},
		},
	}
	rawJSON, err := json.Marshal(raw)
	require.NoError(t, err)

	optimized := optimizeModelToolSurface(raw)
	require.Equal(t, []string{"shell", "grep"}, toolDefinitionNames(optimized))
	optimizedJSON, err := json.Marshal(optimized)
	require.NoError(t, err)
	require.Less(t, len(optimizedJSON), len(rawJSON)/2)

	grepDefinition := optimized[1]
	require.Contains(t, grepDefinition.Description, "patterns + paths")
	properties := grepDefinition.Parameters["properties"].(map[string]interface{})
	require.Contains(t, properties, "pattern")
	require.Contains(t, properties, "paths")
	require.Contains(t, properties, "rg_args")
	require.NotContains(t, properties, "pretty")
}

func TestOptimizeModelToolSurfaceKeepsLegacyShellWhenCanonicalIsUnavailable(t *testing.T) {
	tools := optimizeModelToolSurface([]types.ToolDefinition{{Name: "execute_shell_command"}})
	require.Len(t, tools, 1)
	require.Equal(t, "execute_shell_command", tools[0].Name)

	tools = optimizeModelToolSurface([]types.ToolDefinition{
		{Name: "bash"},
		{Name: "execute_shell_command"},
	})
	require.Equal(t, []string{"bash"}, toolDefinitionNames(tools))
}

func TestOptimizeModelToolSurfaceReducesDefaultToolkitSchema(t *testing.T) {
	manager := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil))
	raw := make([]types.ToolDefinition, 0)
	for _, info := range manager.ListTools() {
		raw = append(raw, types.ToolDefinition{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  normalizeToolParameters(info.InputSchema),
		})
	}
	rawJSON, err := json.Marshal(raw)
	require.NoError(t, err)
	optimized := optimizeModelToolSurface(raw)
	optimizedJSON, err := json.Marshal(optimized)
	require.NoError(t, err)

	names := toolDefinitionNames(optimized)
	require.Contains(t, names, "shell")
	require.NotContains(t, names, "bash")
	require.NotContains(t, names, "execute_shell_command")
	require.Greater(t, len(rawJSON)-len(optimizedJSON), 8*1024)
}

func TestCompactToolSurfaceToBudgetStripsAnnotationsWithoutRemovingParameters(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	agent := &Agent{config: &Config{
		Name:  "test-agent",
		Model: "test-model",
		Options: map[string]interface{}{
			"context_max_prompt_tokens": 300,
		},
	}}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{})
	tools := []types.ToolDefinition{{
		Name:        "inspect",
		Description: strings.Repeat("verbose tool description ", 100),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": strings.Repeat("verbose path guidance ", 100),
					"default":     ".",
				},
				"mode": map[string]interface{}{
					"type": "string",
					"enum": []interface{}{"fast", "full"},
				},
			},
			"required": []string{"path"},
		},
	}}

	compacted := loop.compactToolSurfaceToBudget(tools)
	require.Len(t, compacted, 1)
	require.Less(t, len([]rune(compacted[0].Description)), len([]rune(tools[0].Description)))
	properties := compacted[0].Parameters["properties"].(map[string]interface{})
	pathSchema := properties["path"].(map[string]interface{})
	require.NotContains(t, pathSchema, "description")
	require.NotContains(t, pathSchema, "default")
	require.Equal(t, "string", pathSchema["type"])
	require.Equal(t, []string{"path"}, compacted[0].Parameters["required"])
	require.Equal(t, []interface{}{"fast", "full"}, properties["mode"].(map[string]interface{})["enum"])
}

func TestCompactToolSurfaceForPromptUsesCombinedMessageAndSchemaPressure(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	agent := &Agent{config: &Config{
		Name:    "test-agent",
		Model:   "test-model",
		Options: map[string]interface{}{},
	}}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{})
	tools := []types.ToolDefinition{{
		Name:        "inspect",
		Description: strings.Repeat("tool guidance ", 80),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": strings.Repeat("path guidance ", 80),
				},
			},
		},
	}}
	toolTokens := estimateToolDefinitionTokens(llmRuntime, tools)
	require.Positive(t, toolTokens)
	agent.config.Options["context_max_prompt_tokens"] = toolTokens + 1

	compacted := loop.compactToolSurfaceForPrompt(
		[]types.Message{*types.NewUserMessage(strings.Repeat("active task context ", 200))},
		tools,
		0,
	)

	require.Len(t, compacted, 1)
	require.Equal(t, "inspect", compacted[0].Name)
	require.Less(t, estimateToolDefinitionTokens(llmRuntime, compacted), toolTokens)
	properties := compacted[0].Parameters["properties"].(map[string]interface{})
	require.NotContains(t, properties["path"].(map[string]interface{}), "description")
	require.Equal(t, "string", properties["path"].(map[string]interface{})["type"])
}

func TestFreezeToolSurfaceForTurnUsesFixedPromptBudgetShare(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	tools := []types.ToolDefinition{{
		Name:        "inspect",
		Description: strings.Repeat("verbose tool description ", 100),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": strings.Repeat("verbose path guidance ", 100),
					"default":     ".",
				},
			},
			"required": []string{"path"},
		},
	}}
	toolTokens := estimateToolDefinitionTokens(llmRuntime, tools)
	require.Positive(t, toolTokens)

	// Tight turn budget must not rewrite cache-relevant descriptions or schemas.
	tightAgent := &Agent{config: &Config{
		Name:  "test-agent",
		Model: "test-model",
		Options: map[string]interface{}{
			"context_max_prompt_tokens": toolTokens * 2,
		},
	}}
	tightLoop := NewReActLoop(tightAgent, llmRuntime, &LoopReActConfig{})
	frozenTight := tightLoop.freezeToolSurfaceForTurn(tools)
	require.Equal(t, tools, frozenTight)

	// A larger budget produces the same immutable surface.
	wideAgent := &Agent{config: &Config{
		Name:  "test-agent",
		Model: "test-model",
		Options: map[string]interface{}{
			"context_max_prompt_tokens": toolTokens * 8,
		},
	}}
	wideLoop := NewReActLoop(wideAgent, llmRuntime, &LoopReActConfig{})
	frozenWide := wideLoop.freezeToolSurfaceForTurn(tools)
	require.Equal(t, toolTokens, estimateToolDefinitionTokens(llmRuntime, frozenWide))
}

func TestReActLoop_Run_FreezesToolSurfaceForEntireTurn(t *testing.T) {
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
			"context_max_prompt_tokens":    2000,
			"context_max_messages":         16,
			"context_keep_recent_messages": 8,
		},
	}, &MockSequenceMCPManager{output: "LOG " + large}, llmRuntime)
	bus := runtimeevents.NewBus()
	var freezeEvents []runtimeevents.Event
	bus.Subscribe("context.tool_schema.compacted", func(event runtimeevents.Event) {
		freezeEvents = append(freezeEvents, event)
	})
	bus.Subscribe("context.tool_schema.frozen", func(event runtimeevents.Event) {
		freezeEvents = append(freezeEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "继续处理")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 3)

	firstTools := estimateToolDefinitionTokens(llmRuntime, provider.requests[0].Tools)
	require.Positive(t, firstTools)
	for index, req := range provider.requests {
		require.Equal(t, firstTools, estimateToolDefinitionTokens(llmRuntime, req.Tools), "request %d rewrote tool surface", index)
		require.Equal(t, toolDefinitionNames(provider.requests[0].Tools), toolDefinitionNames(req.Tools), "request %d changed tool names", index)
	}
	require.Len(t, freezeEvents, 1)
	require.Equal(t, ToolDefinitionsFingerprint(provider.requests[0].Tools), freezeEvents[0].Payload["tool_surface_fingerprint"])
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

func TestReActLoop_EmptyAllowlistPreservesSurfaceAndRejectsExecution(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     2,
			SystemPrompt: "No tools are available.",
		},
		mcpManager: &MockPolicyMCPManager{},
		toolBroker: &toolbroker.Broker{},
	}
	agent.SetSubagentScheduler(NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1}))
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{}, false))

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})
	tools, err := loop.getAvailableTools(context.Background(), "try every tool", nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools, "execution policy must not remove request definitions")
	assert.Contains(t, toolDefinitionNames(tools), "write_file")

	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Attempting a forged tool call.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					Name: "write_file",
					Args: map[string]interface{}{"path": "forbidden.txt"},
				}},
			},
			{Content: "The tool call was rejected.", Model: "test-model"},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	loop = NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	result, err := loop.Run(context.Background(), "forge a tool call")
	require.NoError(t, err)
	require.NotEmpty(t, result.Observations)
	assert.Contains(t, result.Observations[0].Error, "tool not allowed by execution policy")
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
	fingerprint := ToolDefinitionsFingerprint(request.Tools)
	require.NotEmpty(t, fingerprint)
	assert.Equal(t, fingerprint, surface["fingerprint"])
	assert.Equal(t, fingerprint, request.Metadata["tool_surface_fingerprint"])
}

func TestReActLoop_Run_EmitsProviderCacheHitRatio(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{{
			Content: "Done.",
			Model:   "test-model",
			Usage: &types.TokenUsage{
				PromptTokens:     1000,
				CompletionTokens: 100,
				TotalTokens:      1100,
				CachedTokens:     250,
			},
		}},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{config: &Config{
		Name:         "test-agent",
		Provider:     "test-provider",
		Model:        "test-model",
		MaxSteps:     1,
		SystemPrompt: "You are a helpful assistant.",
	}}
	bus := runtimeevents.NewBus()
	var finishedEvents []runtimeevents.Event
	bus.Subscribe("llm.request.finished", func(event runtimeevents.Event) {
		finishedEvents = append(finishedEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: false,
	})
	result, err := loop.Run(context.Background(), "finish the task")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, finishedEvents, 1)
	require.Equal(t, 250, finishedEvents[0].Payload["usage_cached_tokens"])
	require.Equal(t, 250, finishedEvents[0].Payload["usage_cache_read_tokens"])
	require.Equal(t, true, finishedEvents[0].Payload["usage_cache_read_reported"])
	require.Equal(t, "hit", finishedEvents[0].Payload["usage_cache_status"])
	require.InDelta(t, 0.25, finishedEvents[0].Payload["usage_cache_hit_ratio"], 0.000001)
}

func TestReActLoop_Run_DistinguishesZeroAndUnreportedProviderCacheUsage(t *testing.T) {
	tests := []struct {
		name         string
		usage        *types.TokenUsage
		wantStatus   string
		wantRatio    bool
		wantReported bool
	}{
		{
			name: "reported zero",
			usage: &types.TokenUsage{
				PromptTokens:      1000,
				CompletionTokens:  100,
				TotalTokens:       1100,
				CacheReadReported: true,
			},
			wantStatus:   "reported_zero",
			wantRatio:    true,
			wantReported: true,
		},
		{
			name: "not reported",
			usage: &types.TokenUsage{
				PromptTokens:     1000,
				CompletionTokens: 100,
				TotalTokens:      1100,
			},
			wantStatus: "not_reported_by_provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llmRuntime := llm.NewLLMRuntime(nil)
			provider := &SequenceLLMProvider{
				name: "test-provider",
				responses: []*llm.LLMResponse{{
					Content: "Done.",
					Model:   "test-model",
					Usage:   tt.usage,
				}},
			}
			require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

			agent := &Agent{config: &Config{
				Name:     "test-agent",
				Provider: "test-provider",
				Model:    "test-model",
				MaxSteps: 1,
			}}
			bus := runtimeevents.NewBus()
			var finishedEvents []runtimeevents.Event
			bus.Subscribe("llm.request.finished", func(event runtimeevents.Event) {
				finishedEvents = append(finishedEvents, event)
			})
			agent.SetEventBus(bus)

			loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
				MaxSteps:        1,
				EnableThought:   true,
				EnableToolCalls: false,
			})
			result, err := loop.Run(context.Background(), "finish the task")
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Len(t, finishedEvents, 1)
			payload := finishedEvents[0].Payload
			require.Equal(t, tt.wantStatus, payload["usage_cache_status"])
			require.Equal(t, tt.wantReported, payload["usage_cache_read_reported"])
			_, ratioPresent := payload["usage_cache_hit_ratio"]
			require.Equal(t, tt.wantRatio, ratioPresent)
		})
	}
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
			Options: map[string]interface{}{
				"read_only_source": "spawn_subagents.read_only",
			},
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockPolicyMCPManager{},
	}
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy(nil, true))
	bus := runtimeevents.NewBus()
	var deniedPolicies []string
	var deniedCodes []string
	var deniedSources []string
	var deniedOverridable []bool
	bus.Subscribe("tool.denied", func(event runtimeevents.Event) {
		if policy, ok := event.Payload["policy"].(string); ok {
			deniedPolicies = append(deniedPolicies, policy)
		}
		if code, ok := event.Payload["error_code"].(string); ok {
			deniedCodes = append(deniedCodes, code)
		}
		if source, ok := event.Payload["policy_source"].(string); ok {
			deniedSources = append(deniedSources, source)
		}
		if overridable, ok := event.Payload["overridable"].(bool); ok {
			deniedOverridable = append(deniedOverridable, overridable)
		}
	})
	var completedOutcomes []string
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		if outcome, ok := event.Payload["outcome"].(string); ok {
			completedOutcomes = append(completedOutcomes, outcome)
		} else if outcome := fmt.Sprint(event.Payload["outcome"]); outcome != "" && outcome != "<nil>" {
			completedOutcomes = append(completedOutcomes, outcome)
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
	assert.Contains(t, deniedCodes, string(runtimeerrors.ErrAgentReadOnly))
	assert.Contains(t, deniedSources, "spawn_subagents.read_only")
	assert.Contains(t, deniedOverridable, false)
	require.NotEmpty(t, completedOutcomes)
	assert.Contains(t, completedOutcomes, "failed")
	require.NotEmpty(t, provider.requests)
	effectiveSurface, ok := provider.requests[0].Metadata["effective_tool_surface"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, effectiveSurface["read_only"])
	assert.Equal(t, "spawn_subagents.read_only", effectiveSurface["policy_source"])
	assert.Contains(t, effectiveSurface["blocked"], "write_file")

	tools, err := loop.getAvailableTools(context.Background(), "write file", nil)
	require.NoError(t, err)
	assert.Contains(t, toolDefinitionNames(tools), "write_file",
		"execution policy must not dynamically remove tools from the frozen request surface")
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

func TestSubagentScheduler_ReadOnlyFiltersWriteLikeTools(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 2},
	}
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent:       2,
		MaxDepth:            1,
		EnforceSingleWriter: true,
	})

	prepared, err := scheduler.prepareTasks([]SubagentTask{
		{
			ID:             "reader-1",
			Goal:           "Inspect files",
			ReadOnly:       true,
			ToolsWhitelist: []string{"write_file", "read_file", "apply_patch", "git_log"},
		},
	})
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	assert.True(t, prepared[0].ReadOnly)
	assert.Equal(t, []string{"read_file", "git_log"}, prepared[0].ToolsWhitelist)
	assert.Equal(t, []string{"write_file", "apply_patch"}, prepared[0].ReadOnlyFilteredTools)
	assert.Equal(t, "spawn_subagents.read_only", prepared[0].ReadOnlySource)

	readers, writers, err := scheduler.partitionTasks(prepared)
	require.NoError(t, err)
	assert.Len(t, readers, 1)
	assert.Empty(t, writers)
}

func TestSubagentScheduler_RejectsDuplicateTaskID(t *testing.T) {
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
		TraceID: "trace_duplicate_id",
		Depth:   1,
	}, []SubagentTask{
		{ID: "reader-1", Goal: "Inspect files", ReadOnly: true},
		{ID: "reader-1", Goal: "Inspect logs", ReadOnly: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate subagent task id "reader-1"`)
	assert.Contains(t, deniedReasons[0], `duplicate subagent task id "reader-1"`)
}

func TestSubagentScheduler_RejectsUnknownDependency(t *testing.T) {
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
		TraceID: "trace_unknown_dependency",
		Depth:   1,
	}, []SubagentTask{
		{ID: "reader-1", Goal: "Inspect files", ReadOnly: true, DependsOn: []string{"missing-task"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `depends on unknown task "missing-task"`)
	assert.Contains(t, deniedReasons[0], `depends on unknown task "missing-task"`)
}

func TestSubagentScheduler_RejectsDependencyCycle(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 2},
	}
	bus := runtimeevents.NewBus()
	var deniedPolicies []string
	var deniedReasons []string
	bus.Subscribe("subagent.denied", func(event runtimeevents.Event) {
		if policy, ok := event.Payload["policy"].(string); ok {
			deniedPolicies = append(deniedPolicies, policy)
		}
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
		TraceID: "trace_dependency_cycle",
		Depth:   1,
	}, []SubagentTask{
		{ID: "reader-1", Goal: "Inspect files", ReadOnly: true, DependsOn: []string{"reader-2"}},
		{ID: "reader-2", Goal: "Inspect logs", ReadOnly: true, DependsOn: []string{"reader-1"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagent dependency deadlock detected")
	assert.Contains(t, deniedPolicies, "dependency")
	assert.Contains(t, deniedReasons, "subagent dependency deadlock detected")
}

func TestSubagentScheduler_ReadOnlyParentNarrowsWritableSubagent(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 2},
	}
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy(nil, true))

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent:       2,
		MaxDepth:            1,
		EnforceSingleWriter: true,
	})

	prepared, err := scheduler.prepareTasks([]SubagentTask{
		{ID: "writer-1", Goal: "Modify config", ReadOnly: false, ToolsWhitelist: []string{"write_file"}},
	})
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	assert.True(t, prepared[0].ReadOnly)
	assert.Empty(t, prepared[0].ToolsWhitelist)
	assert.Equal(t, []string{"write_file"}, prepared[0].ReadOnlyFilteredTools)
	assert.Equal(t, "parent_tool_execution_policy", prepared[0].ReadOnlySource)
}

func TestWhitelistSetPreservesExplicitEmptyAllowlist(t *testing.T) {
	assert.Nil(t, whitelistSet(nil))
	empty := whitelistSet([]string{})
	require.NotNil(t, empty)
	assert.Empty(t, empty)
}

func TestComputeAvailableToolsRespectsExplicitEmptyAllowlist(t *testing.T) {
	agent := &Agent{
		config:     &Config{Name: "empty-whitelist", Model: "test-provider"},
		mcpManager: &MockMCPManager{},
	}
	loop := NewReActLoop(agent, nil, &LoopReActConfig{})

	tools, err := loop.computeAvailableTools(context.Background(), "Inspect files", []string{}, false)
	require.NoError(t, err)
	assert.Empty(t, tools)
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
	output    string
	callCount int
}

func (m *MockSequenceMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
	}, nil
}

func (m *MockSequenceMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.callCount++
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

type RecoveringMCPManager struct{ calls int }

func (m *RecoveringMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true, MCPName: "recovering-mcp"}, nil
}

func (m *RecoveringMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.calls++
	if m.calls == 1 {
		return "first attempt failed", fmt.Errorf("file not found")
	}
	return "verified write result", nil
}

func (m *RecoveringMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{{Name: "write_file", Description: "Write a file", Enabled: true, MCPName: "recovering-mcp"}}
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

// firstNonCompactLLMRequest returns the first recorded request that is not an
// internal active-turn compaction call. Subagent routing/budget tests should
// assert against the model turn, not a preflight compact that may consume the
// SequenceLLMProvider response queue when BudgetTokens is tight.
func firstNonCompactLLMRequest(requests []*llm.LLMRequest) *llm.LLMRequest {
	for _, req := range requests {
		if req == nil {
			continue
		}
		if req.Metadata != nil {
			if op, _ := req.Metadata["internal_operation"].(string); strings.EqualFold(strings.TrimSpace(op), "compact") {
				continue
			}
			if phase, _ := req.Metadata["compact_phase"].(string); strings.TrimSpace(phase) != "" {
				continue
			}
		}
		return req
	}
	return nil
}

func cloneLLMRequest(req *llm.LLMRequest) *llm.LLMRequest {
	if req == nil {
		return nil
	}

	cloned := &llm.LLMRequest{
		Provider:        req.Provider,
		Model:           req.Model,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.ReasoningEffort,
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

type testContextHistorySession struct {
	*testHistorySession
	context map[string]interface{}
}

func (s *testContextHistorySession) GetContext(key string) (interface{}, bool) {
	value, ok := s.context[key]
	return value, ok
}

func (s *testContextHistorySession) SetContext(key string, value interface{}) {
	if s.context == nil {
		s.context = make(map[string]interface{})
	}
	s.context[key] = value
}

func TestSessionGoalIDReadsOnlyCurrentSessionMetadata(t *testing.T) {
	session := &testContextHistorySession{
		testHistorySession: newTestHistorySession("session-a"),
		context: map[string]interface{}{
			"aicli.goal": map[string]interface{}{"goal_id": "goal-a"},
		},
	}
	if got := sessionGoalID(session); got != "goal-a" {
		t.Fatalf("expected goal-a, got %q", got)
	}
	delete(session.context, "aicli.goal")
	if got := sessionGoalID(session); got != "" {
		t.Fatalf("expected empty goal after metadata removal, got %q", got)
	}
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

func TestToolCompletedEventPayloadPropagatesShellArtifactMetadata(t *testing.T) {
	result := toolExecutionResult{
		Call: types.ToolCall{ID: "call-shell", Name: "bash"},
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{
				"raw_output_artifact_path":   `C:\logs\local-shell\toolkit\git_123.txt`,
				"output_capture_complete":    false,
				"capture_limit_reached":      true,
				"retained_output_bytes":      4096,
				"omitted_output_bytes":       2048,
				"output_capture_limit_bytes": 4096,
			},
		}},
	}

	payload := toolCompletedEventPayload(result, 1, "trace-shell", nil)
	require.Equal(t, `C:\logs\local-shell\toolkit\git_123.txt`, payload["raw_output_artifact_path"])
	require.Equal(t, false, payload["output_capture_complete"])
	require.Equal(t, true, payload["capture_limit_reached"])
	require.Equal(t, 4096, payload["retained_output_bytes"])
	require.Equal(t, 2048, payload["omitted_output_bytes"])
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
	}, "")

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

func TestToolResultsToPayloads_AppendsSemanticRepeatAdvisoryOnce(t *testing.T) {
	payloads := toolResultsToPayloads([]toolExecutionResult{
		{Call: types.ToolCall{ID: "call-1", Name: "view"}, Output: "first"},
		{Call: types.ToolCall{ID: "call-2", Name: "grep"}, Output: "second"},
	}, repeatedSemanticToolCallAdvisory(2))
	require.Len(t, payloads, 2)
	require.NotContains(t, payloads[0].Content, "Runtime advisory")
	require.Contains(t, payloads[1].Content, "Execution was not blocked")
	require.Contains(t, payloads[1].Content, `<system-reminder kind="doom_loop">`)
	require.Equal(t, true, payloads[1].Metadata["semantic_repeat_advisory"])
	require.Equal(t, true, payloads[1].Metadata[MetaSystemReminder])
	require.Equal(t, ReminderKindDoomLoop, payloads[1].Metadata[MetaSystemReminderKind])
	require.Equal(t, false, payloads[1].Metadata[MetaSystemReminderDurable])
}

func TestDispositionReplayAdvisory(t *testing.T) {
	partialFirst := dispositionReplayAdvisory(toolresult.OutcomePartial, 1)
	require.Contains(t, partialFirst, "outcome=partial")
	require.Contains(t, partialFirst, "Do not re-run the entire batch unchanged")

	partialRepeat := dispositionReplayAdvisory(toolresult.OutcomePartial, 3)
	require.Contains(t, partialRepeat, "replayed 3 times")
	require.Contains(t, partialRepeat, "Stop full-batch unchanged retries")

	emptyFirst := dispositionReplayAdvisory(toolresult.OutcomeEmpty, 1)
	require.Contains(t, emptyFirst, "successful empty result")
	require.Contains(t, emptyFirst, "broaden/change inputs")

	require.Empty(t, dispositionReplayAdvisory(toolresult.OutcomeSuccess, 2))

	failedFirst := dispositionReplayAdvisory(toolresult.OutcomeFailed, 1)
	require.Contains(t, failedFirst, "outcome=failed")
	require.Contains(t, failedFirst, "Do not replay the same args unchanged")

	staleFirst := dispositionReplayAdvisory(toolresult.OutcomeFailed, 1, "STALE_CONTEXT")
	require.Contains(t, staleFirst, "STALE_CONTEXT")
	require.Contains(t, staleFirst, "current_snippet")
	require.Contains(t, staleFirst, "suggested_view_offset")
	require.Contains(t, staleFirst, "do not retry the same stale")

	staleRepeat := dispositionReplayAdvisory(toolresult.OutcomeFailed, 4, "STALE_CONTEXT")
	require.Contains(t, staleRepeat, "replayed 4 times")
	require.Contains(t, staleRepeat, "Stop unchanged edit/patch retries")

	pathFirst := dispositionReplayAdvisory(toolresult.OutcomeFailed, 1, "TOOL_PATH_NOT_FOUND")
	require.Contains(t, pathFirst, "TOOL_PATH_NOT_FOUND")
	require.Contains(t, pathFirst, "path_candidates")
	require.Contains(t, pathFirst, "do not retry the same missing path")

	pathRepeat := dispositionReplayAdvisory(toolresult.OutcomeFailed, 3, "TOOL_PATH_NOT_FOUND")
	require.Contains(t, pathRepeat, "replayed 3 times")
	require.Contains(t, pathRepeat, "Stop unchanged path retries")
}

func TestDominantToolResultDisposition(t *testing.T) {
	partial := dominantToolResultDisposition([]toolExecutionResult{
		{
			Error: "batch completed with 1 failure(s)",
			Envelope: &output.Envelope{
				Metadata: map[string]interface{}{
					toolresult.MetadataOutcomeKey: toolresult.OutcomePartial,
				},
			},
		},
		{Output: "ok"},
	})
	require.Equal(t, toolresult.OutcomePartial, partial)

	empty := dominantToolResultDisposition([]toolExecutionResult{
		{
			Envelope: &output.Envelope{
				Metadata: map[string]interface{}{
					toolresult.MetadataEmptyResultKey: true,
					toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
				},
			},
		},
	})
	require.Equal(t, toolresult.OutcomeEmpty, empty)

	mixed := dominantToolResultDisposition([]toolExecutionResult{
		{
			Envelope: &output.Envelope{Metadata: map[string]interface{}{
				toolresult.MetadataOutcomeKey: toolresult.OutcomeEmpty,
			}},
		},
		{
			Error: "stale edit",
			Envelope: &output.Envelope{Metadata: map[string]interface{}{
				toolresult.MetadataOutcomeKey: toolresult.OutcomeFailed,
			}},
		},
	})
	require.Equal(t, toolresult.OutcomeFailed, mixed)
}

func TestDominantToolResultErrorCodePrefersStaleContext(t *testing.T) {
	code := dominantToolResultErrorCode([]toolExecutionResult{
		{
			Error: "path missing",
			Envelope: &output.Envelope{
				Metadata: map[string]interface{}{
					"error_code": "TOOL_PATH_NOT_FOUND",
				},
			},
		},
		{
			Error: "old_string miss",
			Envelope: &output.Envelope{
				Metadata: map[string]interface{}{
					"error_code": "STALE_CONTEXT",
					"tool_metadata": map[string]interface{}{
						"error_code": "STALE_CONTEXT",
					},
				},
			},
		},
	})
	require.Equal(t, "STALE_CONTEXT", code)

	nestedOnly := dominantToolResultErrorCode([]toolExecutionResult{
		{
			Error: "stale",
			Envelope: &output.Envelope{
				Metadata: map[string]interface{}{
					"tool_metadata": map[string]interface{}{
						"error_code": "STALE_CONTEXT",
					},
				},
			},
		},
	})
	require.Equal(t, "STALE_CONTEXT", nestedOnly)
}

func TestReActLoop_PromptCacheExactPrefixAcrossToolSteps(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will inspect logs.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-logs",
					Name: "read_logs",
					Args: map[string]interface{}{"path": "app.log"},
				}},
			},
			{
				Content: "I will re-read logs for confirmation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-logs-2",
					Name: "read_logs",
					Args: map[string]interface{}{"path": "app.log"},
				}},
			},
			{Content: "Logs look healthy.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	apiAgent := NewAgentWithLLM(&Config{
		Name:         "prefix-agent",
		Provider:     "test-provider",
		Model:        "test-model",
		MaxSteps:     4,
		SystemPrompt: "You are a helpful assistant.",
		Options: map[string]interface{}{
			"active_goal_guidance": "Persistent goal.\n\nkeep the prefix stable",
			// Keep Build free of budget-driven rewrites for this sequence test.
			"context_max_prompt_tokens": 200000,
			"context_max_messages":      200,
		},
	}, &MockSequenceMCPManager{output: "log line ok"}, llmRuntime)

	// Inject a product-managed todo context message before the first model call
	// by seeding history with only system/user; Build will append goal + freeze.
	loop := NewReActLoop(apiAgent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "check application logs")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.GreaterOrEqual(t, len(provider.requests), 3)

	requests := make([]*llm.LLMRequest, 0, len(provider.requests))
	for _, req := range provider.requests {
		if req == nil {
			continue
		}
		if req.Metadata != nil {
			if op, _ := req.Metadata["internal_operation"].(string); strings.EqualFold(strings.TrimSpace(op), "compact") {
				continue
			}
		}
		requests = append(requests, req)
	}
	require.GreaterOrEqual(t, len(requests), 3)

	for step := 1; step < len(requests); step++ {
		prev := requests[step-1]
		curr := requests[step]
		require.NotNil(t, prev)
		require.NotNil(t, curr)
		require.GreaterOrEqual(t, len(curr.Messages), len(prev.Messages), "step %d grew shorter", step)
		require.Equal(t, prev.Messages, curr.Messages[:len(prev.Messages)], "request %d must keep previous messages as exact prefix", step)

		// Tools surface must remain frozen across ordinary ReAct steps.
		require.Equal(t, ToolDefinitionsFingerprint(prev.Tools), ToolDefinitionsFingerprint(curr.Tools))
	}

	// Snapshot must sit after the user message and before later tool traffic.
	first := requests[0]
	userIdx := -1
	goalIdx := -1
	for index, message := range first.Messages {
		if message.Role == "user" && strings.Contains(message.Content, "check application logs") {
			userIdx = index
		}
		if message.Metadata.GetString("context_stage", "") == "active_goal" {
			goalIdx = index
			require.True(t, message.Metadata.GetBool("context_snapshot", false))
			require.Contains(t, message.Content, "keep the prefix stable")
		}
	}
	require.GreaterOrEqual(t, userIdx, 0)
	require.Greater(t, goalIdx, userIdx)

	// Later requests must still keep the same frozen goal text in the same place.
	for _, req := range requests[1:] {
		require.Equal(t, first.Messages[goalIdx], req.Messages[goalIdx])
	}

	// Wire-level Codex assertion: instructions stay byte-stable and input grows
	// by suffix only across ReAct steps (exact body prefix for prompt cache).
	codexBodies := make([]map[string]interface{}, 0, len(requests))
	codex := &llmadapter.CodexAdapter{}
	for _, req := range requests {
		protocolMessages := llm.RuntimeMessagesToProtocolMessages(req.Messages, "codex")
		functions := make([]map[string]interface{}, 0, len(req.Tools))
		for _, tool := range req.Tools {
			functions = append(functions, map[string]interface{}{
				"type":        "function",
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			})
		}
		body := codex.BuildRequest(llmadapter.RequestConfig{
			Model:     "gpt-5.4",
			Messages:  protocolMessages,
			Stream:    false,
			Functions: functions,
			Metadata: map[string]interface{}{
				"prompt_cache_key": "prefix-session",
			},
		})
		codexBodies = append(codexBodies, body)
	}
	require.GreaterOrEqual(t, len(codexBodies), 3)

	firstInstructions, _ := codexBodies[0]["instructions"].(string)
	require.Equal(t, "You are a helpful assistant.", firstInstructions)
	// Active goal is a frozen turn-context developer message and must stay in
	// input (after the user message), never rewrite top-level instructions.
	require.NotContains(t, firstInstructions, "keep the prefix stable")
	require.Equal(t, "prefix-session", codexBodies[0]["prompt_cache_key"])

	firstInput, okFirstInput := codexBodies[0]["input"].([]map[string]interface{})
	require.True(t, okFirstInput)
	foundGoalInInput := false
	for _, item := range firstInput {
		if item["role"] == "developer" {
			contentJSON, _ := json.Marshal(item["content"])
			if strings.Contains(string(contentJSON), "keep the prefix stable") {
				foundGoalInInput = true
				break
			}
		}
	}
	require.True(t, foundGoalInInput, "expected frozen goal developer item in codex input")

	for step := 1; step < len(codexBodies); step++ {
		prev := codexBodies[step-1]
		curr := codexBodies[step]
		require.Equal(t, prev["instructions"], curr["instructions"], "codex instructions must stay stable across step %d", step)
		require.Equal(t, prev["prompt_cache_key"], curr["prompt_cache_key"])
		require.Equal(t, prev["tools"], curr["tools"], "codex tools must stay frozen across step %d", step)

		prevInput, okPrev := prev["input"].([]map[string]interface{})
		currInput, okCurr := curr["input"].([]map[string]interface{})
		require.True(t, okPrev && okCurr, "expected codex input arrays at step %d", step)
		require.GreaterOrEqual(t, len(currInput), len(prevInput), "codex input shortened at step %d", step)
		require.Equal(t, prevInput, currInput[:len(prevInput)], "codex input of request %d must be exact prefix of request %d", step, step+1)
	}
}
