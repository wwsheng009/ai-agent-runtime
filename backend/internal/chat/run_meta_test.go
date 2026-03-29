package chat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runMetaSequenceProvider struct {
	name      string
	responses []*llm.LLMResponse
	callCount int
}

func (p *runMetaSequenceProvider) Name() string {
	return p.name
}

func (p *runMetaSequenceProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
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

func (p *runMetaSequenceProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *runMetaSequenceProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *runMetaSequenceProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *runMetaSequenceProvider) CheckHealth(ctx context.Context) error {
	return nil
}

type runMetaCapturingMCPManager struct {
	lastMeta  *team.RunMeta
	callCount int
}

func (m *runMetaCapturingMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if toolName != "team_echo" {
		return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return skill.ToolInfo{
		Name:          toolName,
		Description:   "Echo tool for run meta tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *runMetaCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	runCtx, ok := ctx.(context.Context)
	if !ok {
		return nil, fmt.Errorf("unexpected context type %T", ctx)
	}
	meta, ok := team.GetRunMeta(runCtx)
	if !ok || meta == nil {
		return nil, fmt.Errorf("run meta missing")
	}
	m.lastMeta = meta.Clone()
	m.callCount++
	return "ok", nil
}

func (m *runMetaCapturingMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("team_echo")
	return []skill.ToolInfo{info}
}

func TestSessionActorSubmitPromptPropagatesRunMetaToToolContext(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-run-meta-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-run-meta-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Using a tool.",
				Model:   "test-run-meta-model",
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
				Model:   "test-run-meta-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &runMetaCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-run-meta-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	runMeta := &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        "team-1",
			AgentID:       "mate-1",
			CurrentTaskID: "task-1",
		},
	}
	result, err := actor.SubmitPrompt(ctx, "Use the tool.", runMeta)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, mcpManager.lastMeta)

	assert.Equal(t, "team-1", mcpManager.lastMeta.Team.TeamID)
	assert.Equal(t, "mate-1", mcpManager.lastMeta.Team.AgentID)
	assert.Equal(t, "task-1", mcpManager.lastMeta.Team.CurrentTaskID)

	state := actor.State()
	require.NotNil(t, state)
	assert.Nil(t, state.CurrentRunMeta)
}

func TestSessionActorSubmitPromptPreservesRunMetaAcrossQuestionResume(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-run-meta-question-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-run-meta-question-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I need more input.",
				Model:   "test-run-meta-question-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_question",
						Name: toolbroker.ToolAskUserQuestion,
						Args: map[string]interface{}{
							"prompt":   "Need confirmation",
							"required": true,
						},
					},
				},
			},
			{
				Content: "Now using the team tool.",
				Model:   "test-run-meta-question-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_echo",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello"},
					},
				},
			},
			{
				Content: "Finished.",
				Model:   "test-run-meta-question-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &runMetaCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-run-meta-question-test",
		Model:        provider.Name(),
		MaxSteps:     4,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	runMeta := &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        "team-1",
			AgentID:       "mate-1",
			CurrentTaskID: "task-1",
		},
	}

	resultCh := make(chan *agent.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, submitErr := actor.SubmitPrompt(ctx, "Start the flow.", runMeta)
		resultCh <- result
		errCh <- submitErr
	}()

	var questionID string
	require.Eventually(t, func() bool {
		state := actor.State()
		if state == nil || state.PendingQuestion == nil {
			return false
		}
		if state.Status != SessionWaitingInput {
			return false
		}
		questionID = state.PendingQuestion.ID
		return questionID != ""
	}, 5*time.Second, 20*time.Millisecond)

	require.NoError(t, actor.AnswerQuestion(context.Background(), questionID, "yes"))

	result := <-resultCh
	err = <-errCh
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, mcpManager.lastMeta)

	assert.Equal(t, "team-1", mcpManager.lastMeta.Team.TeamID)
	assert.Equal(t, "mate-1", mcpManager.lastMeta.Team.AgentID)
	assert.Equal(t, "task-1", mcpManager.lastMeta.Team.CurrentTaskID)

	state := actor.State()
	require.NotNil(t, state)
	assert.Nil(t, state.CurrentRunMeta)
	assert.Nil(t, state.PendingQuestion)
}

type shellLikeCapturingMCPManager struct {
	callCount int
}

func (m *shellLikeCapturingMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if toolName != "run_shell_command" {
		return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return skill.ToolInfo{
		Name:          toolName,
		Description:   "Shell-like tool for permission mode tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *shellLikeCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.callCount++
	return "shell ok", nil
}

func (m *shellLikeCapturingMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("run_shell_command")
	return []skill.ToolInfo{info}
}

func TestSessionActorSubmitPromptBypassesInteractiveApprovalForTeamRunMeta(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	session, err := manager.CreateSession(ctx, "actor-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-run-meta-bypass-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "test-run-meta-bypass-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Using a shell-like tool.",
				Model:   "test-run-meta-bypass-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_shell_1",
						Name: "run_shell_command",
						Args: map[string]interface{}{"command": "rg run_meta"},
					},
				},
			},
			{
				Content: "Finished without waiting for approval.",
				Model:   "test-run-meta-bypass-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	mcpManager := &shellLikeCapturingMCPManager{}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-run-meta-bypass-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, mcpManager, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	result, err := actor.SubmitPrompt(ctx, "Inspect the codebase.", &team.RunMeta{
		PermissionMode: "bypass_permissions",
		Team: &team.TeamRunMeta{
			TeamID:        "team-1",
			AgentID:       "mate-1",
			CurrentTaskID: "task-1",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	assert.Equal(t, "Finished without waiting for approval.", result.Output)
	assert.Equal(t, 1, mcpManager.callCount)

	state := actor.State()
	require.NotNil(t, state)
	assert.Equal(t, SessionIdle, state.Status)
	assert.Nil(t, state.PendingApproval)
}
