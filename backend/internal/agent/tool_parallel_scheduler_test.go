package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestParallelToolBatchPlan_RejectsUndeclaredMCPParallelism(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-model"},
		mcpManager: &parallelSchedulerMCPManager{
			maxParallelCalls: 1,
		},
	}
	loop := NewReActLoop(agent, nil, &LoopReActConfig{
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 2,
	})

	plan := loop.buildParallelToolBatchPlan([]types.ToolCall{
		{ID: "call-1", Name: "read_a", Args: map[string]interface{}{}},
		{ID: "call-2", Name: "read_b", Args: map[string]interface{}{}},
	}, nil)
	require.Nil(t, plan)
}

func TestParallelToolBatchPlan_RejectsExplicitlyNonParallelTool(t *testing.T) {
	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-model"},
		mcpManager: &parallelSchedulerMCPManager{
			maxParallelCalls: 2,
			metadataByTool: map[string]map[string]interface{}{
				"read_a": {
					"supports_parallel": false,
				},
			},
		},
	}
	loop := NewReActLoop(agent, nil, &LoopReActConfig{
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 2,
	})

	plan := loop.buildParallelToolBatchPlan([]types.ToolCall{
		{ID: "call-1", Name: "read_a", Args: map[string]interface{}{}},
		{ID: "call-2", Name: "read_b", Args: map[string]interface{}{}},
	}, nil)
	require.Nil(t, plan)
}

func TestParallelToolBatchPlan_RespectsEnvDisable(t *testing.T) {
	t.Setenv("AICLI_DISABLE_PARALLEL_TOOLS", "1")

	agent := &Agent{
		config: &Config{Name: "test-agent", Model: "test-model"},
		mcpManager: &parallelSchedulerMCPManager{
			maxParallelCalls: 2,
		},
	}
	loop := NewReActLoop(agent, nil, &LoopReActConfig{
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 2,
	})

	plan := loop.buildParallelToolBatchPlan([]types.ToolCall{
		{ID: "call-1", Name: "read_a", Args: map[string]interface{}{}},
		{ID: "call-2", Name: "read_b", Args: map[string]interface{}{}},
	}, nil)
	require.Nil(t, plan)
}

func TestParallelToolBatch_UsesConcurrentExecution(t *testing.T) {
	releaseCh := make(chan struct{})
	startCh := make(chan string, 2)
	mgr := &parallelSchedulerMCPManager{
		maxParallelCalls: 2,
		startCh:          startCh,
		releaseCh:        releaseCh,
	}
	agent := NewAgentWithLLM(&Config{Name: "test-agent", Model: "test-model"}, mgr, nil)
	agent.SetPermissionEngine(NewPermissionEngine())
	agent.SetToolBroker(&toolbroker.Broker{})
	loop := NewReActLoop(agent, nil, &LoopReActConfig{
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 2,
	})

	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "read_a", Args: map[string]interface{}{"path": "a.txt"}},
		{ID: "call-2", Name: "read_b", Args: map[string]interface{}{"path": "b.txt"}},
	}
	plan := loop.buildParallelToolBatchPlan(toolCalls, nil)
	require.NotNil(t, plan)

	completedCh := make(chan runtimeevents.Event, 2)
	agent.GetEventBus().Subscribe("tool.completed", func(event runtimeevents.Event) {
		completedCh <- event
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseCh)
		})
	}
	defer release()

	resultCh := make(chan struct {
		results []toolExecutionResult
		err     error
	}, 1)
	go func() {
		results, err := loop.act(ctx, "trace_parallel", "session_parallel", 1, 0, nil, toolCalls, nil)
		resultCh <- struct {
			results []toolExecutionResult
			err     error
		}{results: results, err: err}
	}()

	first := waitParallelToolStart(t, startCh, time.Second)
	second := waitParallelToolStart(t, startCh, time.Second)
	require.ElementsMatch(t, []string{"read_a", "read_b"}, []string{first, second})

	release()

	select {
	case res := <-resultCh:
		require.NoError(t, res.err)
		require.Len(t, res.results, 2)
		require.Equal(t, "call-1", res.results[0].Call.ID)
		require.Equal(t, "call-2", res.results[1].Call.ID)
		require.Equal(t, "read_a-result", res.results[0].Output)
		require.Equal(t, "read_b-result", res.results[1].Output)
		require.GreaterOrEqual(t, mgr.maxConcurrent, 2)
	case <-ctx.Done():
		t.Fatal("parallel batch did not complete before context timeout")
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case event := <-completedCh:
			callID, _ := event.Payload["tool_call_id"].(string)
			require.NotEmpty(t, callID)
			require.Equal(t, false, event.Payload["awaiting_model"], "parallel tool completed event must not claim awaiting_model")
			seen[callID] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for parallel completion events")
		}
	}
	require.True(t, seen["call-1"])
	require.True(t, seen["call-2"])
}

func TestParallelToolBatchPlan_RejectsBrokerTools(t *testing.T) {
	agent := NewAgentWithLLM(&Config{Name: "test-agent", Model: "test-model"}, &parallelSchedulerMCPManager{
		maxParallelCalls: 2,
	}, nil)
	agent.SetPermissionEngine(NewPermissionEngine())
	agent.SetToolBroker(&toolbroker.Broker{})

	loop := NewReActLoop(agent, nil, &LoopReActConfig{
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 2,
	})

	plan := loop.buildParallelToolBatchPlan([]types.ToolCall{
		{ID: "call-1", Name: toolbroker.ToolSpawnTeam, Args: map[string]interface{}{}},
		{ID: "call-2", Name: "read_a", Args: map[string]interface{}{}},
	}, nil)
	require.Nil(t, plan)
}

func TestParallelToolBatchPlan_RejectsCustomPermissionEngineBehavior(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*PermissionEngine)
	}{
		{
			name: "callback",
			configure: func(engine *PermissionEngine) {
				engine.Callback = func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
				}
			},
		},
		{
			name: "rules",
			configure: func(engine *PermissionEngine) {
				engine.Rules = []runtimepolicy.Rule{
					{
						Name:     "deny-read-a",
						Tools:    []string{"read_a"},
						Decision: runtimepolicy.DecisionDeny,
						Reason:   "blocked by rule",
					},
				}
			},
		},
		{
			name: "capability_resolver",
			configure: func(engine *PermissionEngine) {
				engine.CapabilityResolver = parallelSchedulerCapabilityResolver{}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := NewAgentWithLLM(&Config{Name: "test-agent", Model: "test-model"}, &parallelSchedulerMCPManager{
				maxParallelCalls: 2,
			}, nil)
			engine := NewPermissionEngine()
			tc.configure(engine)
			agent.SetPermissionEngine(engine)

			loop := NewReActLoop(agent, nil, &LoopReActConfig{
				EnableToolCalls:      true,
				EnableParallelTools:  true,
				MaxParallelToolCalls: 2,
			})

			plan := loop.buildParallelToolBatchPlan([]types.ToolCall{
				{ID: "call-1", Name: "read_a", Args: map[string]interface{}{}},
				{ID: "call-2", Name: "read_b", Args: map[string]interface{}{}},
			}, nil)
			require.Nil(t, plan)
		})
	}
}

type parallelSchedulerMCPManager struct {
	maxParallelCalls int
	startCh          chan string
	releaseCh        chan struct{}
	metadataByTool   map[string]map[string]interface{}

	mu            sync.Mutex
	concurrent    int
	maxConcurrent int
}

func (m *parallelSchedulerMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	return runtimeskill.ToolInfo{
		Name:             toolName,
		Description:      toolName,
		Metadata:         cloneSchedulerMetadata(m.metadataByTool, toolName),
		MCPName:          "parallel-mcp",
		MaxParallelCalls: m.maxParallelCalls,
		Enabled:          true,
	}, nil
}

func (m *parallelSchedulerMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	callCtx, _ := ctx.(context.Context)
	if callCtx == nil {
		callCtx = context.Background()
	}

	m.mu.Lock()
	m.concurrent++
	if m.concurrent > m.maxConcurrent {
		m.maxConcurrent = m.concurrent
	}
	m.mu.Unlock()

	if m.startCh != nil {
		m.startCh <- toolName
	}

	select {
	case <-m.releaseCh:
	case <-callCtx.Done():
		m.mu.Lock()
		m.concurrent--
		m.mu.Unlock()
		return nil, callCtx.Err()
	}

	m.mu.Lock()
	m.concurrent--
	m.mu.Unlock()
	return fmt.Sprintf("%s-result", toolName), nil
}

func (m *parallelSchedulerMCPManager) ListTools() []runtimeskill.ToolInfo {
	return []runtimeskill.ToolInfo{
		{
			Name:             "read_a",
			Description:      "read_a",
			Metadata:         cloneSchedulerMetadata(m.metadataByTool, "read_a"),
			MCPName:          "parallel-mcp",
			MaxParallelCalls: m.maxParallelCalls,
			Enabled:          true,
		},
		{
			Name:             "read_b",
			Description:      "read_b",
			Metadata:         cloneSchedulerMetadata(m.metadataByTool, "read_b"),
			MCPName:          "parallel-mcp",
			MaxParallelCalls: m.maxParallelCalls,
			Enabled:          true,
		},
	}
}

func cloneSchedulerMetadata(source map[string]map[string]interface{}, toolName string) map[string]interface{} {
	if len(source) == 0 {
		return nil
	}
	metadata, ok := source[toolName]
	if !ok || len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func waitParallelToolStart(t *testing.T, startCh <-chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case toolName := <-startCh:
		return toolName
	case <-time.After(timeout):
		t.Fatal("timed out waiting for parallel tool start")
		return ""
	}
}

type parallelSchedulerCapabilityResolver struct{}

func (parallelSchedulerCapabilityResolver) Resolve(req runtimepolicy.EvalRequest) []runtimepolicy.Capability {
	return []runtimepolicy.Capability{runtimepolicy.CapNetwork}
}
