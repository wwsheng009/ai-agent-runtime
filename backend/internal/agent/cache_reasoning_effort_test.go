package agent

// cache_reasoning_effort_test.go 锁定 llm.request.started 载荷里的
// requested/effective reasoning effort：这是 micro web 缓存页展示的数据源。

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRunWithSessionCarriesReasoningEffortInStartedPayload(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name:      "effort-provider",
		responses: []*llm.LLMResponse{{Content: "done", Model: "effort-model"}},
	}
	require.NoError(t, llmRuntime.RegisterProvider("effort-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name:     "cache-effort",
		Provider: "effort-provider",
		Model:    "effort-model",
	}, nil, llmRuntime)

	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	var started []map[string]interface{}
	bus.Subscribe("llm.request.started", func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, event.Payload)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: false,
		ReasoningEffort: "max",
	})
	session := &testContextHistorySession{testHistorySession: newTestHistorySession("session-effort")}
	session.ReplaceHistory([]types.Message{{Role: "user", Content: "show effort"}})

	result, err := loop.RunWithSession(context.Background(), "show effort", session)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 1)
	require.Equal(t, "max", provider.requests[0].ReasoningEffort)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, started, 1)
	require.Equal(t, "max", started[0]["requested_reasoning_effort"])
	require.Equal(t, "max", started[0]["reasoning_effort"])
}
