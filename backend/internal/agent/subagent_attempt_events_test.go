package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestSchedulerEmitsAttemptLifecycleEvents 锁定 §6.1-4（行动文档 P0）：任务级
// attempt 的成对 started/finished 事件。归属父会话、带 attempt 序号与
// 时序/预算/usage/partial 字段，供 request/attempt/task 三层对账；started 不得
// 携带终态字段，finished 必须携带终态与用量口径。
func TestSchedulerEmitsAttemptLifecycleEvents(t *testing.T) {
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
	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	payloads := map[string]map[string]interface{}{}
	var order []string
	bus.Subscribe("", func(event runtimeevents.Event) {
		mu.Lock()
		order = append(order, event.Type)
		payloads[event.Type] = event.Payload
		mu.Unlock()
	})
	agent.SetEventBus(bus)

	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{})
	task := SubagentTask{ID: "t-attempt", Role: "explore", ReadOnly: true}
	options := SubagentRunOptions{
		ParentSessionID:  "session-parent",
		ParentToolCallID: "call-1",
		BatchID:          "batch-1",
		TraceID:          "trace-1",
	}
	spec := ChildAgentSpec{}

	startedAt := time.Now().Add(-1500 * time.Millisecond)
	deadline := time.Now().Add(30 * time.Second)
	batchDeadline := time.Now().Add(2 * time.Minute)
	timing := subagentAttemptTiming{
		StartedAt:     &startedAt,
		Deadline:      &deadline,
		BatchDeadline: &batchDeadline,
		RetryBudgetMS: 12345,
		RemainingMS:   20000,
	}

	scheduler.emitSubagentAttemptStarted(options, task, "child-1", "explore", spec, 1, 2, timing)
	scheduler.emitSubagentAttemptFinished(options, task, "child-1", "explore", spec, 1, 2, timing, subagentAttemptOutcome{
		Success:              false,
		Status:               "failed",
		CompletionReason:     "timeout",
		FailureCategory:      "timeout",
		ErrorCode:            "UPSTREAM_TIMEOUT",
		ErrorText:            "context deadline exceeded",
		Retryable:            true,
		RetryReason:          "timeout",
		RetryAdvice:          "retry_with_changed_inputs",
		Intermediate:         true,
		Usage:                &types.TokenUsage{PromptTokens: 40, CompletionTokens: 2, TotalTokens: 42},
		PartialOutput:        true,
		PartialFindingsCount: 1,
		RemainingMS:          20000,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{
		runtimeevents.EventSubagentAttemptStarted,
		runtimeevents.EventSubagentAttemptFinished,
	}, order, "started 必须先于 finished 发射")

	startedPayload := payloads[runtimeevents.EventSubagentAttemptStarted]
	require.Equal(t, "t-attempt", startedPayload["subagent_id"])
	require.Equal(t, "explore", startedPayload["role"])
	require.Equal(t, true, startedPayload["read_only"])
	require.Equal(t, 1, startedPayload["attempt"])
	require.Equal(t, 2, startedPayload["max_attempts"])
	require.Equal(t, "scheduler", startedPayload["source"])
	require.Equal(t, "session-parent", startedPayload["parent_session_id"])
	require.Equal(t, "child-1", startedPayload["child_session_id"])
	require.Equal(t, "batch-1", startedPayload["batch_id"])
	require.Equal(t, "trace-1", startedPayload["trace_id"])
	require.Equal(t, int64(12345), startedPayload["retry_budget_ms"])
	require.Equal(t, int64(20000), startedPayload["remaining_ms"])
	require.NotEmpty(t, startedPayload["attempt_started_at"])
	require.NotEmpty(t, startedPayload["attempt_deadline"])
	_, hasOutcome := startedPayload["success"]
	require.False(t, hasOutcome, "started 事件不得携带终态字段")

	finishedPayload := payloads[runtimeevents.EventSubagentAttemptFinished]
	require.Equal(t, false, finishedPayload["success"])
	require.Equal(t, "failed", finishedPayload["status"])
	require.Equal(t, "timeout", finishedPayload["completion_reason"])
	require.Equal(t, "UPSTREAM_TIMEOUT", finishedPayload["error_code"])
	require.Equal(t, true, finishedPayload["retryable"])
	require.Equal(t, true, finishedPayload["intermediate_attempt"])
	require.Equal(t, true, finishedPayload["partial_output"])
	require.Equal(t, 1, finishedPayload["partial_findings_count"])
	require.Equal(t, 42, finishedPayload["usage_total_tokens"])
	require.Equal(t, 40, finishedPayload["usage_prompt_tokens"])
	require.Equal(t, int64(20000), finishedPayload["remaining_ms"])
	require.NotEmpty(t, finishedPayload["error"])
	duration, ok := finishedPayload["duration_ms"].(int64)
	require.True(t, ok, "duration_ms 必须是 int64")
	require.Greater(t, duration, int64(0), "duration_ms 应可从 attempt started_at 推导")
}
