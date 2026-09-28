package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestReActLoop_OnProgressReportsRunTicks pins the P0-1 progress contract: a
// loop with OnProgress set reports llm_response / tool_call_start /
// tool_call_end / iteration_end ticks, in that order within one iteration.
func TestReActLoop_OnProgressReportsRunTicks(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Reporting completion.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-outcome",
					Name: toolbroker.ToolReportTaskOutcome,
					Args: map[string]interface{}{"task_status": "done", "summary": "done"},
				}},
			},
			{Content: "All set.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "worker", Provider: "test-provider", Model: "test-model", MaxSteps: 4,
	}, &completionOutcomeMCPManager{}, llmRuntime)

	var mu sync.Mutex
	var kinds []string
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableToolCalls: true,
		OnProgress: func(_ context.Context, kind string) {
			mu.Lock()
			kinds = append(kinds, kind)
			mu.Unlock()
		},
	})
	result, err := loop.Run(context.Background(), "finish")
	require.NoError(t, err)
	require.NotNil(t, result)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, kinds, "OnProgress must receive run ticks")
	assert.Equal(t, "llm_response", kinds[0], "第一个 tick 应为 LLM 响应完成")
	for _, want := range []string{"llm_response", "tool_call_start", "tool_call_end", "iteration_end"} {
		assert.Contains(t, kinds, want)
	}
	// 第一轮迭代的顺序契约：llm_response → tool_call_start → tool_call_end → iteration_end。
	firstIteration := kinds[:4]
	assert.Equal(t,
		[]string{"llm_response", "tool_call_start", "tool_call_end", "iteration_end"},
		firstIteration,
	)
}

// TestReActLoop_NoteProgressNilSafe guards the best-effort contract: nil loop /
// nil config / nil hook must never panic, so the progress channel can never
// fail the run it observes.
func TestReActLoop_NoteProgressNilSafe(t *testing.T) {
	var nilLoop *ReActLoop
	nilLoop.noteProgress(context.Background(), "llm_response")

	loop := &ReActLoop{config: &LoopReActConfig{}}
	loop.noteProgress(context.Background(), "llm_response")

	withHook := &ReActLoop{config: &LoopReActConfig{OnProgress: func(context.Context, string) {}}}
	withHook.noteProgress(context.Background(), "iteration_end")
}
