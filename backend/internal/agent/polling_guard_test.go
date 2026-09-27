package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// waitAgentResults builds one executed wait_agent batch: the requested window is
// deliberately separate from the waited_ms the host actually reports, mirroring
// the production clamp (a 40m request can complete in 2m).
func waitAgentResults(timeoutMs, waitedMs string) []toolExecutionResult {
	return []toolExecutionResult{{
		Call:   waitAgentCall("call", timeoutMs),
		Output: map[string]interface{}{"ok": true, "waited_ms": waitedMs},
	}}
}

func TestPollingBackoffTracker_NotifiesAfterThreshold(t *testing.T) {
	tracker := NewPollingBackoffTracker(0)
	require.Equal(t, PollingBackoffNoticeThreshold, tracker.Threshold())

	for i := 1; i <= PollingBackoffNoticeThreshold; i++ {
		obs := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
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
		require.Contains(t, obs.Advisory, "not a context/token budget")
		require.True(t, obs.EmitNotice, "crossing the threshold emits the product event once")
	}

	// Past the threshold the advisory keeps guiding the model, but the event
	// must not repeat for the same streak.
	again := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
	require.Equal(t, PollingBackoffNoticeThreshold+1, again.RepeatCount)
	require.NotEmpty(t, again.Advisory)
	require.False(t, again.EmitNotice)

	// Mixed polling tools in one batch still count as a polling streak.
	mixed := tracker.ObserveToolResults([]toolExecutionResult{
		{Call: waitAgentCall("call-a", "1000")},
		{Call: types.ToolCall{ID: "call-b", Name: "read_agent_events", Args: map[string]interface{}{"id": "child-1", "after_seq": 3}}},
	})
	require.Equal(t, 1, mixed.RepeatCount, "a different polling batch restarts the streak")
}

// TestPollingBackoffTracker_SupervisionInspectIsNotPolling pins the split
// between the doom-loop exemption set and the polling soft brake: supervision
// inspection is exempt from both (its tool description promises so), while the
// existing wait/read polling brake is untouched.
func TestPollingBackoffTracker_SupervisionInspectIsNotPolling(t *testing.T) {
	tracker := NewPollingBackoffTracker(0)
	inspect := []toolExecutionResult{{Call: types.ToolCall{
		ID:   "call-1",
		Name: "supervision_descendants",
		Args: map[string]interface{}{"mode": "children"},
	}}}
	fingerprint, tools := pollingBatchFingerprint([]types.ToolCall{inspect[0].Call})
	require.Empty(t, fingerprint)
	require.Empty(t, tools)

	for i := 0; i < PollingBackoffNoticeThreshold+2; i++ {
		obs := tracker.ObserveToolResults(inspect)
		require.Empty(t, obs.Fingerprint)
		require.Zero(t, obs.RepeatCount)
		require.Empty(t, obs.Advisory)
		require.False(t, obs.EmitNotice)
	}

	// The brake itself is unchanged: a wait_agent streak still crosses the
	// threshold after the inspection reset.
	for i := 1; i <= PollingBackoffNoticeThreshold; i++ {
		obs := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
		require.Equal(t, i, obs.RepeatCount)
	}

	// Mixing inspection with a blocking wait resets like real work.
	mixed := tracker.ObserveToolResults([]toolExecutionResult{
		{Call: waitAgentCall("wait-2", "1000")},
		{Call: types.ToolCall{ID: "call-2", Name: "supervision_snapshot", Args: map[string]interface{}{"mode": "descendants"}}},
	})
	require.Zero(t, mixed.RepeatCount)
}

func TestPollingBackoffTracker_ResetsOnRealWork(t *testing.T) {
	tracker := NewPollingBackoffTracker(2)
	require.Equal(t, 2, tracker.Threshold())

	first := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
	require.Equal(t, 1, first.RepeatCount)
	second := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
	require.Equal(t, 2, second.RepeatCount)
	require.NotEmpty(t, second.Advisory)

	// Real work in the batch resets the streak (polling guard must not nag
	// while the model makes progress).
	work := tracker.ObserveToolResults([]toolExecutionResult{{
		Call:   types.ToolCall{ID: "call-3", Name: "view", Args: map[string]interface{}{"file_path": "a.go"}},
		Output: map[string]interface{}{"ok": true},
	}})
	require.Zero(t, work.RepeatCount)
	require.Empty(t, work.Advisory)
	require.Zero(t, tracker.RepeatCount())

	// A mixed batch (polling + real work) also resets instead of counting.
	mixed := tracker.ObserveToolResults([]toolExecutionResult{
		{Call: waitAgentCall("call-4", "1000")},
		{Call: types.ToolCall{ID: "call-5", Name: "view", Args: map[string]interface{}{"file_path": "a.go"}}},
	})
	require.Zero(t, mixed.RepeatCount)

	// After the reset, a fresh streak starts from 1 and only crosses at the
	// threshold again.
	restart := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
	require.Equal(t, 1, restart.RepeatCount)
	require.Empty(t, restart.Advisory)
}

func TestPollingBackoffTracker_TimingArgsDoNotBreakStreak(t *testing.T) {
	tracker := NewPollingBackoffTracker(2)
	require.Equal(t, 1, tracker.ObserveToolResults(waitAgentResults("1000", "1000")).RepeatCount)
	changed := tracker.ObserveToolResults(waitAgentResults("60000", "1000"))
	require.Equal(t, 2, changed.RepeatCount, "a larger timeout_ms is the same wait intent, not a new request")
	require.NotEmpty(t, changed.Advisory)

	// The observation target stays semantic: a different child id is a new request.
	otherTarget := tracker.ObserveToolResults([]toolExecutionResult{{
		Call: types.ToolCall{
			ID:   "call-2",
			Name: "wait_agent",
			Args: map[string]interface{}{"ids": []string{"child-2"}, "timeout_ms": "60000"},
		},
		Output: map[string]interface{}{"ok": true, "waited_ms": "1000"},
	}})
	require.Equal(t, 1, otherTarget.RepeatCount, "waiting on a different target starts a new streak")
	require.Empty(t, otherTarget.Advisory)
}

// TestPollingBackoffTracker_WaitBudgetUsesActualWaitedMs is the incident
// contract: the guard must never bill the requested timeout_ms. In the observed
// session a 40m request was clamped to a 120s window, yet the pre-execution
// accounting reported "40m of blocking wait" before the window had even opened.
func TestPollingBackoffTracker_WaitBudgetUsesActualWaitedMs(t *testing.T) {
	tracker := NewPollingBackoffTracker(5) // threshold high: only the wait budget can fire

	first := tracker.ObserveToolResults(waitAgentResults("2400000", "120000"))
	require.Equal(t, 2*time.Minute, first.CumulativeWait,
		"requested timeout_ms must never be counted as elapsed waiting")
	require.False(t, first.WaitBudgetExceeded)
	require.Empty(t, first.Advisory, "a 40m request completed in 2m must not trip the 5m notice")
	require.False(t, first.EmitNotice)

	second := tracker.ObserveToolResults(waitAgentResults("2400000", "120000"))
	require.Equal(t, 4*time.Minute, second.CumulativeWait)
	require.False(t, second.WaitBudgetExceeded)

	third := tracker.ObserveToolResults(waitAgentResults("2400000", "60000"))
	require.Equal(t, 5*time.Minute, third.CumulativeWait)
	require.True(t, third.WaitBudgetExceeded)
	require.True(t, third.EmitNotice, "crossing the actual-wait threshold notifies once")
	require.Contains(t, third.Advisory, "actual waiting")
	require.Contains(t, third.Advisory, "not a context/token budget")
	require.Contains(t, third.Advisory, "does not authorize finalizing")

	fourth := tracker.ObserveToolResults(waitAgentResults("2400000", "60000"))
	require.False(t, fourth.EmitNotice, "the budget notice fires once per streak")

	// Real work resets the accumulated window along with the streak.
	work := tracker.ObserveToolResults([]toolExecutionResult{{
		Call:   types.ToolCall{ID: "call-3", Name: "view", Args: map[string]interface{}{"file_path": "a.go"}},
		Output: map[string]interface{}{"ok": true},
	}})
	require.False(t, work.WaitBudgetExceeded)
	require.Zero(t, tracker.CumulativeWait())
}

// TestPollingBackoffTracker_ReadyAndTerminalResetStreak keeps the guard from
// nagging a parent whose child actually reached a terminal state or produced a
// ready output — those are progress, not polling.
func TestPollingBackoffTracker_ReadyAndTerminalResetStreak(t *testing.T) {
	tracker := NewPollingBackoffTracker(0)
	require.Equal(t, 1, tracker.ObserveToolResults(waitAgentResults("600000", "120000")).RepeatCount)

	ready := tracker.ObserveToolResults([]toolExecutionResult{{
		Call: waitAgentCall("call", "600000"),
		Output: map[string]interface{}{
			"ok": true, "waited_ms": "120000", "ready_count": 1,
			"terminal_delta": []string{"child-1"},
		},
	}})
	require.Zero(t, ready.RepeatCount, "a ready/terminal result ends the wait streak")
	require.Zero(t, tracker.CumulativeWait())

	// The next wait is a fresh streak, not a continuation.
	restart := tracker.ObserveToolResults(waitAgentResults("600000", "120000"))
	require.Equal(t, 1, restart.RepeatCount)
	require.Equal(t, 2*time.Minute, restart.CumulativeWait)
}

func TestPollingBackoffTracker_ErrorsAndInterruptsResetStreak(t *testing.T) {
	tracker := NewPollingBackoffTracker(0)
	require.Equal(t, 1, tracker.ObserveToolResults(waitAgentResults("600000", "120000")).RepeatCount)

	failed := tracker.ObserveToolResults([]toolExecutionResult{{
		Call:  waitAgentCall("call", "600000"),
		Error: "context canceled",
	}})
	require.Zero(t, failed.RepeatCount, "a failed/interrupted wait is not a polling repeat")
	require.Zero(t, tracker.CumulativeWait())

	interrupted := tracker.ObserveToolResults([]toolExecutionResult{{
		Call:   waitAgentCall("call", "600000"),
		Output: map[string]interface{}{"ok": true, "interrupted": true, "waited_ms": "120000"},
	}})
	require.Zero(t, interrupted.RepeatCount)
	require.Zero(t, tracker.CumulativeWait())
}

// TestPollingBackoffTracker_MissingWaitedMsContributesZero locks the evidence
// discipline: without an executed waited_ms value the guard contributes zero
// instead of guessing from the request.
func TestPollingBackoffTracker_MissingWaitedMsContributesZero(t *testing.T) {
	tracker := NewPollingBackoffTracker(5)
	obs := tracker.ObserveToolResults([]toolExecutionResult{{
		Call:   waitAgentCall("call", "2400000"),
		Output: "child still running",
	}})
	require.Equal(t, 1, obs.RepeatCount)
	require.Zero(t, obs.CumulativeWait)
	require.False(t, obs.WaitBudgetExceeded)
	require.Empty(t, obs.Advisory)
}

func TestPollingBackoffTracker_DisabledWithNegativeThreshold(t *testing.T) {
	tracker := NewPollingBackoffTracker(-1)
	require.Zero(t, tracker.Threshold())
	for i := 0; i < 5; i++ {
		obs := tracker.ObserveToolResults(waitAgentResults("600000", "1000"))
		require.Empty(t, obs.Advisory)
		require.False(t, obs.EmitNotice)
		require.Zero(t, obs.RepeatCount)
	}

	var nilTracker *PollingBackoffTracker
	require.Zero(t, nilTracker.Threshold())
	require.Zero(t, nilTracker.ObserveToolResults(waitAgentResults("600000", "1000")).RepeatCount)
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
// once per streak. The event is measured from executed results, not requested
// windows.
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
	require.Equal(t, "tool_result.waited_ms", backoffEvents[0].Payload["wait_measurement"])
	require.Equal(t, "current_run_polling_streak", backoffEvents[0].Payload["scope"])
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
