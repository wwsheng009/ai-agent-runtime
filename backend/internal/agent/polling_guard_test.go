package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func waitAgentCall(id, timeoutMs string) types.ToolCall {
	return types.ToolCall{
		ID:   id,
		Name: "wait_agent",
		Args: map[string]interface{}{"ids": []string{"child-1"}, "timeout_ms": timeoutMs},
	}
}

func TestPollingBackoffTracker_NotifiesAfterThreshold(t *testing.T) {
	tracker := NewPollingBackoffTracker(0)
	require.Equal(t, PollingBackoffNoticeThreshold, tracker.Threshold())

	for i := 1; i <= PollingBackoffNoticeThreshold; i++ {
		obs := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")})
		require.Equal(t, i, obs.RepeatCount)
		require.Equal(t, []string{"wait_agent"}, obs.Tools)
		if i < PollingBackoffNoticeThreshold {
			require.Empty(t, obs.Advisory, "advisory must stay silent below the threshold")
			require.False(t, obs.EmitNotice)
			continue
		}
		require.Contains(t, obs.Advisory, "polling/control request")
		require.Contains(t, obs.Advisory, "wait_agent")
		require.Contains(t, obs.Advisory, "Execution was not blocked")
		require.True(t, obs.EmitNotice, "crossing the threshold emits the product event once")
	}

	// Past the threshold the advisory keeps guiding the model, but the event
	// must not repeat for the same streak.
	again := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")})
	require.Equal(t, PollingBackoffNoticeThreshold+1, again.RepeatCount)
	require.NotEmpty(t, again.Advisory)
	require.False(t, again.EmitNotice)

	// Mixed polling tools in one batch still count as a polling streak.
	mixed := tracker.ObserveToolBatch([]types.ToolCall{
		waitAgentCall("call-a", "1000"),
		{ID: "call-b", Name: "read_agent_events", Args: map[string]interface{}{"id": "child-1", "after_seq": 3}},
	})
	require.Equal(t, 1, mixed.RepeatCount, "a different polling batch restarts the streak")
}

func TestPollingBackoffTracker_ResetsOnRealWork(t *testing.T) {
	tracker := NewPollingBackoffTracker(2)
	require.Equal(t, 2, tracker.Threshold())

	first := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")})
	require.Equal(t, 1, first.RepeatCount)
	second := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")})
	require.Equal(t, 2, second.RepeatCount)
	require.NotEmpty(t, second.Advisory)

	// Real work in the batch resets the streak (polling guard must not nag
	// while the model makes progress).
	work := tracker.ObserveToolBatch([]types.ToolCall{{ID: "call-3", Name: "view", Args: map[string]interface{}{"file_path": "a.go"}}})
	require.Zero(t, work.RepeatCount)
	require.Empty(t, work.Advisory)
	require.Zero(t, tracker.RepeatCount())

	// A mixed batch (polling + real work) also resets instead of counting.
	mixed := tracker.ObserveToolBatch([]types.ToolCall{
		waitAgentCall("call-4", "1000"),
		{ID: "call-5", Name: "view", Args: map[string]interface{}{"file_path": "a.go"}},
	})
	require.Zero(t, mixed.RepeatCount)

	// After the reset, a fresh streak starts from 1 and only crosses at the
	// threshold again.
	restart := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")})
	require.Equal(t, 1, restart.RepeatCount)
	require.Empty(t, restart.Advisory)
}

func TestPollingBackoffTracker_DifferentArgsBreakStreak(t *testing.T) {
	tracker := NewPollingBackoffTracker(2)
	require.Equal(t, 1, tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")}).RepeatCount)
	changed := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "60000")})
	require.Equal(t, 1, changed.RepeatCount, "a larger timeout_ms is a different polling request")
	require.Empty(t, changed.Advisory)
}

func TestPollingBackoffTracker_DisabledWithNegativeThreshold(t *testing.T) {
	tracker := NewPollingBackoffTracker(-1)
	require.Zero(t, tracker.Threshold())
	for i := 0; i < 5; i++ {
		obs := tracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")})
		require.Empty(t, obs.Advisory)
		require.False(t, obs.EmitNotice)
		require.Zero(t, obs.RepeatCount)
	}

	var nilTracker *PollingBackoffTracker
	require.Zero(t, nilTracker.Threshold())
	require.Zero(t, nilTracker.ObserveToolBatch([]types.ToolCall{waitAgentCall("call", "1000")}).RepeatCount)
}

func TestPollingBackoffAdvisory_ReminderKindWiring(t *testing.T) {
	advisory := pollingBackoffAdvisory([]string{"wait_agent", "wait_agent"}, 3)
	require.NotEmpty(t, advisory)
	require.Equal(t, ReminderKindPollingBackoff, inferAdvisoryReminderKind(advisory))
	require.Equal(t, ReminderKindPollingBackoff, NormalizeReminderKind(ReminderKindPollingBackoff))
	require.True(t, strings.Contains(FormatSystemReminder(ReminderKindPollingBackoff, advisory), `kind="polling_backoff"`))
}

// TestReActLoop_InjectsPollingBackoffAdvisoryWithoutStopping is the loop-level
// contract for P1-7: identical repeated wait_agent calls stay exempt from the
// doom loop, receive non-blocking backoff guidance, and emit the product event
// once per streak.
func TestReActLoop_InjectsPollingBackoffAdvisoryWithoutStopping(t *testing.T) {
	manager := &MockSequenceMCPManager{output: "child still running"}
	llmRuntime := llm.NewLLMRuntime(nil)
	responses := make([]*llm.LLMResponse, 0, PollingBackoffNoticeThreshold+1)
	for index := 1; index <= PollingBackoffNoticeThreshold; index++ {
		responses = append(responses, &llm.LLMResponse{
			Content: "继续等待子任务。",
			Model:   "test-model",
			ToolCalls: []types.ToolCall{{
				ID:   fmt.Sprintf("call-%d", index),
				Name: "wait_agent",
				Args: map[string]interface{}{
					"ids":        []string{"child-1"},
					"timeout_ms": 30000,
				},
			}},
		})
	}
	responses = append(responses, &llm.LLMResponse{Content: "等待完成。", Model: "test-model"})
	provider := &SequenceLLMProvider{name: "test-provider", responses: responses}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name:     "test-agent",
		Provider: "test-provider",
		Model:    "test-model",
	}, manager, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{EnableToolCalls: true})
	bus := runtimeevents.NewBus()
	var backoffEvents []runtimeevents.Event
	var doomWarnings []runtimeevents.Event
	bus.Subscribe(EventPollingBackoffObserved, func(event runtimeevents.Event) {
		backoffEvents = append(backoffEvents, event)
	})
	bus.Subscribe(EventDoomLoopWarning, func(event runtimeevents.Event) {
		doomWarnings = append(doomWarnings, event)
	})
	agent.SetEventBus(bus)

	result, err := loop.Run(context.Background(), "等待子任务完成")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "等待完成。", result.Output)
	require.Equal(t, PollingBackoffNoticeThreshold, manager.callCount, "polling must not be blocked")
	require.Len(t, backoffEvents, 1)
	require.Equal(t, PollingBackoffNoticeThreshold, backoffEvents[0].Payload["repeat_count"])
	require.Equal(t, []string{"wait_agent"}, backoffEvents[0].Payload["tools"])
	require.Empty(t, doomWarnings, "polling/control tools stay doom-loop exempt")

	advisoryFound := false
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "polling/control request") {
			advisoryFound = true
			break
		}
	}
	require.True(t, advisoryFound, "repeated polling should receive non-blocking backoff guidance")
}
