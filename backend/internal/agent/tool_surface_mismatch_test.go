package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func frozenSurfaceContext(t *testing.T, names ...string) context.Context {
	t.Helper()
	snapshot := &inMemoryTurnToolSurfaceSnapshot{}
	ctx := WithTurnToolSurfaceSnapshot(context.Background(), snapshot)
	defs := make([]types.ToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, types.ToolDefinition{Name: name})
	}
	require.NoError(t, snapshot.SaveTurnToolSurface(ctx, defs))
	return ctx
}

func TestToolAdvertisedInTurnMatchesFrozenSurface(t *testing.T) {
	ctx := frozenSurfaceContext(t, "view", "shell", "grep")

	require.True(t, toolSurfaceKnown(ctx))
	require.True(t, toolAdvertisedInTurn(ctx, "view"))
	require.True(t, toolAdvertisedInTurn(ctx, " shell "))
	require.False(t, toolAdvertisedInTurn(ctx, "todos"))

	require.False(t, toolSurfaceKnown(context.Background()))
	require.False(t, toolAdvertisedInTurn(context.Background(), "view"))
}

func TestIsUnadvertisedToolDenialRequiresKnownSurface(t *testing.T) {
	ctx := frozenSurfaceContext(t, "view", "shell")

	require.True(t, isUnadvertisedToolDenial(ctx, toolExecutionResult{
		Error: "tool not allowed for this agent: todos",
	}))
	require.False(t, isUnadvertisedToolDenial(ctx, toolExecutionResult{
		Error: "tool not allowed for this agent: view",
	}), "a denial for an advertised tool is a real boundary violation")
	require.False(t, isUnadvertisedToolDenial(ctx, toolExecutionResult{
		Error: "permission denied by policy",
	}), "non-whitelist errors keep the legacy fatal path")
	require.False(t, isUnadvertisedToolDenial(ctx, toolExecutionResult{
		Error: "tool not allowed for this agent: write_file",
	}), "write-like surface mismatches stay fatal (real boundary violation)")
	require.False(t, isUnadvertisedToolDenial(ctx, toolExecutionResult{
		Error: "tool not allowed for this agent: subagent_spawn",
	}), "delegation surface mismatches stay fatal")

	// Unknown surface must not be treated as empty: keep the legacy fatal path.
	require.False(t, isUnadvertisedToolDenial(context.Background(), toolExecutionResult{
		Error: "tool not allowed for this agent: todos",
	}))
}

func TestDeniedToolNameParsesJoinedDenialText(t *testing.T) {
	name, ok := deniedToolName("tool not allowed for this agent: todos; tool not allowed for this agent: artifact_read")
	require.True(t, ok)
	require.Equal(t, "todos", name)

	_, ok = deniedToolName("tool denied by execution policy: view")
	require.False(t, ok)
}

// The gate (streak/count) and the ledger (pendingFailures) must classify with
// the same predicate. Write/shell/delegation/orchestration calls stay fatal.
func TestNonFatalSurfaceMismatchName(t *testing.T) {
	require.True(t, nonFatalSurfaceMismatchName("todos"))
	require.True(t, nonFatalSurfaceMismatchName("artifact_read"))
	require.False(t, nonFatalSurfaceMismatchName("write_file"))
	require.False(t, nonFatalSurfaceMismatchName("shell"))
	require.False(t, nonFatalSurfaceMismatchName("subagent_spawn"))
	require.False(t, nonFatalSurfaceMismatchName("job_start"))
}

// T1: a read-only tool that was never advertised is recorded but must not flip
// the run to failed. The frozen surface contains write_file only, so todos is
// a genuine surface mismatch.
func TestReActLoop_UnadvertisedReadOnlyToolIsNonFatal(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Trying the task list.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call-todos", Name: "todos",
					Args: map[string]interface{}{"todos": []interface{}{}},
				}},
			},
			{Content: "Finished with the available tools.", Model: "test-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "surface-mismatch", Provider: "test-provider", Model: "test-model", MaxSteps: 3,
	}, &RecoveringMCPManager{}, runtime)
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"write_file"}, false))
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{MaxSteps: 3, EnableToolCalls: true})

	result, err := loop.run(context.Background(), "do the thing", loopRunOptions{
		TraceID:       "trace_surface_mismatch_nonfatal",
		IncludePrompt: true,
		SessionID:     "surface-mismatch",
		ToolWhitelist: []string{"write_file"},
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, 1, result.SurfaceMismatchCount)
	require.Equal(t, 1, result.ToolErrorCount)
	require.Zero(t, result.UnrecoveredToolErrorCount)
}

// T2: a write-like tool that is in the surface but denied by the read-only
// policy is a real boundary violation and must keep the run failed.
func TestReActLoop_InSurfaceWriteDeniedByReadOnlyStaysFatal(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Trying to write.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call-write", Name: "write_file",
					Args: map[string]interface{}{"path": "out.txt", "content": "hello"},
				}},
			},
			{Content: "Finished.", Model: "test-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "read-only-write", Provider: "test-provider", Model: "test-model", MaxSteps: 3,
	}, &RecoveringMCPManager{}, runtime)
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"write_file"}, true))
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{MaxSteps: 3, EnableToolCalls: true})

	result, err := loop.run(context.Background(), "write a file", loopRunOptions{
		TraceID:       "trace_surface_mismatch_fatal",
		IncludePrompt: true,
		SessionID:     "read-only-write",
		ToolWhitelist: []string{"write_file"},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Zero(t, result.SurfaceMismatchCount)
	require.Equal(t, 1, result.UnrecoveredToolErrorCount)
}

// A write-like call denied by the whitelist is fatal and must NOT be counted as
// a non-fatal surface mismatch (the gate and the ledger must agree).
func TestReActLoop_UnadvertisedWriteToolStaysFatalAndUncounted(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Trying to write.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID: "call-write-hidden", Name: "write",
					Args: map[string]interface{}{"path": "out.txt", "content": "hello"},
				}},
			},
			{Content: "Finished.", Model: "test-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "hidden-write", Provider: "test-provider", Model: "test-model", MaxSteps: 3,
	}, &MockCatalogMCPManager{}, runtime)
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"read_file"}, false))
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{MaxSteps: 3, EnableToolCalls: true})

	result, err := loop.run(context.Background(), "write a file", loopRunOptions{
		TraceID:       "trace_surface_mismatch_hidden_write",
		IncludePrompt: true,
		SessionID:     "hidden-write",
		ToolWhitelist: []string{"read_file"},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Zero(t, result.SurfaceMismatchCount)
	require.Equal(t, 1, result.UnrecoveredToolErrorCount)
}
