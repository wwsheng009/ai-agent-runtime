package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpcatalog "github.com/ai-gateway/ai-agent-runtime/internal/mcp/catalog"
	"github.com/ai-gateway/ai-agent-runtime/internal/artifact"
	"github.com/ai-gateway/ai-agent-runtime/internal/contextmgr"
	runtimeevents "github.com/ai-gateway/ai-agent-runtime/internal/events"
	"github.com/ai-gateway/ai-agent-runtime/internal/llm"
	"github.com/ai-gateway/ai-agent-runtime/internal/output"
	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLLMProvider 模拟 LLM Provider 用于测试
type MockLLMProvider struct {
	name string
}

type SequenceLLMProvider struct {
	name      string
	responses []*llm.LLMResponse
	callCount int
	requests  []*llm.LLMRequest
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
		"MaxSteps":        10,
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
	if !strings.Contains(outputText, "artifact_refs:") {
		t.Fatalf("expected observation output to mention artifact refs, got %q", outputText)
	}
	if strings.Contains(outputText, "frame 4") {
		t.Fatalf("expected inline output to be reduced, got %q", outputText)
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
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 12, result.Usage.TotalTokens)
	assert.Contains(t, result.Error, "token budget exceeded")
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
			BudgetTokens: 64,
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
	assert.Equal(t, 64, provider.requests[0].MaxTokens)
}

func TestReActLoop_GetAvailableTools_UsesCatalogSearch(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		mcpManager: &MockCatalogMCPManager{},
	}
	catalog := mcpcatalog.New()
	catalog.Refresh(agent.mcpManager.ListTools())
	agent.SetToolCatalog(catalog)

	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	tools, err := loop.getAvailableTools("inspect recent logs and errors", nil)
	if err != nil {
		t.Fatalf("get available tools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("expected tools from catalog search")
	}
	if tools[0].Name != "read_logs" {
		t.Fatalf("expected read_logs to rank first, got %s", tools[0].Name)
	}
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

	tools, err := loop.getAvailableTools("write file", nil)
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
}

func (m *MockRichSequenceMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
	}, nil
}

func (m *MockRichSequenceMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if m.output == "" {
		return nil, fmt.Errorf("no output configured for %s", toolName)
	}
	return m.output, nil
}

func (m *MockRichSequenceMCPManager) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
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
