package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestReActLoop_StopHookBlockForcesContinuation(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name:         "test-provider",
		defaultModel: "test-model",
		responses: []*llm.LLMResponse{
			{Content: "first final answer without tools", Model: "test-model"},
			{Content: "recovered final answer after stop block", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		SystemPrompt:     "You are a helpful assistant.",
	}, nil, llmRuntime)

	var stopHits atomic.Int32
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := stopHits.Add(1)
		if hit == 1 {
			_, _ = w.Write([]byte(`{"action":"block","message":"tests are not green yet"}`))
			return
		}
		_, _ = w.Write([]byte(`{"action":"continue"}`))
	}))
	defer hookServer.Close()
	agent.SetHookManager(runtimehooks.NewManager([]runtimehooks.HookConfig{{
		ID:    "stop-gate",
		Event: runtimehooks.EventStop,
		Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL, Method: http.MethodPost},
	}}))

	bus := runtimeevents.NewBus()
	var stopBlocked []runtimeevents.Event
	bus.Subscribe("hooks.stop_blocked", func(event runtimeevents.Event) {
		stopBlocked = append(stopBlocked, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableToolCalls: false,
	})
	session := newTestHistorySession("session-stop-block")

	result, err := loop.RunWithSession(context.Background(), "finish the task", session)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "recovered final answer after stop block", result.Output)
	require.GreaterOrEqual(t, len(provider.requests), 2)
	require.Equal(t, int32(2), stopHits.Load())
	require.Len(t, stopBlocked, 1)
	require.Equal(t, "tests are not green yet", stopBlocked[0].Payload["message"])

	joined := joinedMessageContents(session.GetMessages())
	require.Contains(t, joined, "A stop hook blocked finishing this turn")
	require.Contains(t, joined, "tests are not green yet")
}

func TestReActLoop_PreCompactHookBlockSkipsCompaction(t *testing.T) {
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
			{Content: "Completed without compaction checkpoint.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Provider:         "test-provider",
		Model:            "test-model",
		DefaultMaxTokens: 256,
		SystemPrompt:     "Current canonical instructions.",
	}, nil, llmRuntime)

	var preCompactHits atomic.Int32
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preCompactHits.Add(1)
		payload := map[string]interface{}{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"action":"block","message":"keep full history"}`))
	}))
	defer hookServer.Close()
	agent.SetHookManager(runtimehooks.NewManager([]runtimehooks.HookConfig{{
		ID:    "pre-compact-block",
		Event: runtimehooks.EventPreCompact,
		Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL, Method: http.MethodPost},
	}}))

	bus := runtimeevents.NewBus()
	var skippedEvents []runtimeevents.Event
	var completedEvents []runtimeevents.Event
	bus.Subscribe("context.pre_turn_compact.skipped", func(event runtimeevents.Event) {
		skippedEvents = append(skippedEvents, event)
	})
	bus.Subscribe("context.pre_turn_compact.completed", func(event runtimeevents.Event) {
		completedEvents = append(completedEvents, event)
	})
	agent.SetEventBus(bus)

	session := newTestHistorySession("session-pre-compact-block")
	session.messages = append(session.messages, *types.NewUserMessage("Finish the original long-running task without losing its constraints."))
	for index := 0; index < 12; index++ {
		callID := fmt.Sprintf("pre-call-%d", index)
		assistant := types.NewAssistantMessage(fmt.Sprintf("verified progress %d", index))
		assistant.ToolCalls = []types.ToolCall{{ID: callID, Name: "inspect", Args: map[string]interface{}{"index": index}}}
		session.messages = append(session.messages, *assistant, *types.NewToolMessage(callID, strings.Repeat(fmt.Sprintf("evidence-%d ", index), 60)))
	}

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 0, EnableToolCalls: false})
	result, err := loop.ContinueWithSession(context.Background(), session)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "Completed without compaction checkpoint.", result.Output)
	require.Len(t, provider.requests, 1)
	require.GreaterOrEqual(t, preCompactHits.Load(), int32(1))
	require.NotEmpty(t, skippedEvents)
	require.Equal(t, "pre_compact_hook_blocked", skippedEvents[0].Payload["reason"])
	require.Equal(t, "keep full history", skippedEvents[0].Payload["hook_message"])
	require.Empty(t, completedEvents)
	require.NotContains(t, joinedMessageContents(provider.requests[0].Messages), "CONTEXT CHECKPOINT COMPACTION")
}
