package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// attemptEventRig 是 attempt 事件端到端用例的共享装置：agent/调度器/批次协调器
// （detached worker 里真实跑子任务）+ 事件采集 + 批次终态通道。
type attemptEventRig struct {
	agent    *Agent
	terminal chan BatchTerminalLifecycle
	mu       sync.Mutex
	events   []recordedLifecycleEvent
}

type recordedLifecycleEvent struct {
	eventType string
	payload   map[string]interface{}
}

func newAttemptEventRig(t *testing.T) *attemptEventRig {
	t.Helper()
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
	rig := &attemptEventRig{agent: agent, terminal: make(chan BatchTerminalLifecycle, 1)}
	bus := runtimeevents.NewBus()
	bus.Subscribe("", func(event runtimeevents.Event) {
		switch event.Type {
		case runtimeevents.EventSubagentAttemptStarted, runtimeevents.EventSubagentAttemptFinished,
			runtimeevents.EventSubagentRouteResolved, "subagent.completed":
			rig.mu.Lock()
			rig.events = append(rig.events, recordedLifecycleEvent{eventType: event.Type, payload: event.Payload})
			rig.mu.Unlock()
		}
	})
	agent.SetEventBus(bus)
	agent.SetSubagentScheduler(NewSubagentScheduler(agent, SubagentSchedulerConfig{MaxConcurrent: 2, MaxDepth: 1}))
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	agent.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store:     batchStore,
		Scheduler: agent.GetSubagentScheduler(),
	}))
	agent.SetBatchLifecycleProjector(func(_ context.Context, event BatchTerminalLifecycle) error {
		rig.terminal <- event
		return nil
	})
	return rig
}

// waitTerminal 等待 detached 批次投影终态，确保事件已全部写入。
func (r *attemptEventRig) waitTerminal(t *testing.T) {
	t.Helper()
	select {
	case event := <-r.terminal:
		require.Equal(t, subagentbatch.ExecutionModeBackground, event.ExecutionMode)
	case <-time.After(10 * time.Second):
		t.Fatal("expected the detached batch to reach a terminal projection")
	}
}

// snapshot 返回当前已采集的事件副本（带锁）。
func (r *attemptEventRig) snapshot() []recordedLifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedLifecycleEvent(nil), r.events...)
}

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

// firstChildFailureProvider 让第一次子请求在 provider 层直接失败（注入瞬时
// 错误），之后放行到脚本化成功响应。配合 MaxRetries=0 的 runtime（每次任务
// attempt 恰好一次 provider 调用），任务级 attempt 2 必然发生。
type firstChildFailureProvider struct {
	*roleRoutingLLMProvider
	mu         sync.Mutex
	childCalls int
	attempts   []int
	errText    string
}

func (p *firstChildFailureProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if !isSubagentChildRequest(req) {
		return p.roleRoutingLLMProvider.Call(ctx, req)
	}
	p.mu.Lock()
	p.childCalls++
	if attempt, ok := req.Metadata["task_attempt"].(int); ok {
		p.attempts = append(p.attempts, attempt)
	}
	fail := p.childCalls == 1
	p.mu.Unlock()
	if fail {
		return nil, errors.New(p.errText)
	}
	return p.roleRoutingLLMProvider.Call(ctx, req)
}

func (p *firstChildFailureProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.childCalls
}

// observedAttempts 返回每次子请求携带的调度 attempt 序号（含失败调用）。
func (p *firstChildFailureProvider) observedAttempts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.attempts...)
}

// TestSubagentAttemptEventsPairOnTransientRetry 锁定 §6.1（行动文档 P0）验收：
// 注入第一次子请求 UPSTREAM_UNAVAILABLE 后，事件流必须可见 attempt 1（中间
// 失败、retryable）与 attempt 2（终态成功）的成对 started/finished，以及**唯一
// 一条**任务终态 subagent.completed（成功，attempt=2）；中间尝试不再发
// subagent.completed（§6.1-4 过渡期已闭合）。
func TestSubagentAttemptEventsPairOnTransientRetry(t *testing.T) {
	rig := newAttemptEventRig(t)

	llmRuntime := llm.NewLLMRuntime(nil)
	// 关掉 runtime 层重试：每次任务 attempt 恰好一次 provider 调用；否则
	// provider 层重试会吃掉 attempt 2 的成功响应，任务级重试无法被观测。
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	baseProvider := &SequenceLLMProvider{
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
			{Content: "Final answer from parent.", Model: "test-model"},
		},
	}
	provider := &firstChildFailureProvider{
		roleRoutingLLMProvider: &roleRoutingLLMProvider{
			SequenceLLMProvider: baseProvider,
			childResponses: []*llm.LLMResponse{
				{
					Content: "Recovered child answer.",
					Model:   "test-model",
					Usage:   &types.TokenUsage{PromptTokens: 30, CompletionTokens: 5, TotalTokens: 35},
				},
			},
		},
		errText: "UPSTREAM_UNAVAILABLE: injected transient child failure (attempt-1 retry test)",
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(rig.agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	result, err := loop.Run(context.Background(), "Find the root cause.")
	require.NoError(t, err)
	require.True(t, result.Success)

	rig.waitTerminal(t)

	events := rig.snapshot()

	var startedAttempts []int
	finishedByAttempt := map[int]map[string]interface{}{}
	var completedEvents []map[string]interface{}
	for _, event := range events {
		switch event.eventType {
		case runtimeevents.EventSubagentAttemptStarted:
			if attempt, ok := event.payload["attempt"].(int); ok {
				startedAttempts = append(startedAttempts, attempt)
			}
		case runtimeevents.EventSubagentAttemptFinished:
			if attempt, ok := event.payload["attempt"].(int); ok {
				finishedByAttempt[attempt] = event.payload
			}
		case "subagent.completed":
			completedEvents = append(completedEvents, event.payload)
		}
	}
	require.ElementsMatch(t, []int{1, 2}, startedAttempts,
		"attempt 1 与 attempt 2 都应有 started 事件；实际 %v（provider 调用 %d 次）",
		startedAttempts, provider.callCount())

	first := finishedByAttempt[1]
	require.NotNil(t, first, "attempt 1 必须有 finished 事件")
	require.Equal(t, false, first["success"])
	require.Equal(t, true, first["intermediate_attempt"], "attempt 1 是中间尝试（后面还有 attempt 2）")
	require.Equal(t, "failed", first["status"])
	require.Equal(t, "provider_error", first["failure_category"])
	require.Equal(t, true, first["retryable"])
	require.Equal(t, "", first["retry_skipped"], "transient 失败且预算充足时不应有跳过原因")
	require.NotEmpty(t, first["attempt_started_at"])
	require.Equal(t, 2, first["max_attempts"])

	second := finishedByAttempt[2]
	require.NotNil(t, second, "attempt 2 必须有 finished 事件")
	require.Equal(t, true, second["success"])
	require.Equal(t, false, second["intermediate_attempt"], "attempt 2 是终态尝试")
	require.Equal(t, "completed", second["status"])
	require.Equal(t, 35, second["usage_total_tokens"], "attempt 2 的用量应随 finished 事件携带")

	require.Len(t, completedEvents, 1,
		"§6.1-4 过渡期已闭合：中间尝试不再发 subagent.completed，任务只发一条终态")
	finalCompleted := completedEvents[0]
	require.Equal(t, true, finalCompleted["success"])
	require.Equal(t, 2, finalCompleted["attempt"])
	if intermediate, ok := finalCompleted["intermediate_attempt"].(bool); ok {
		require.False(t, intermediate, "终态 subagent.completed 不得带 intermediate 标记")
	}

	var routeReceipt map[string]interface{}
	for _, event := range events {
		if event.eventType == runtimeevents.EventSubagentRouteResolved {
			routeReceipt = event.payload
			break
		}
	}
	require.NotNil(t, routeReceipt, "route.resolved 必须携带 context receipt（§6.5）")
	require.Equal(t, "fresh", routeReceipt["requested_context"])
	require.Equal(t, "fresh", routeReceipt["effective_context"])
	require.Equal(t, false, routeReceipt["parent_history_injected"])
	// provider 层在每次子请求（含被注入失败的那次）看到的调度 attempt 序号（§8.2）。
	require.Equal(t, []int{1, 2}, provider.observedAttempts(),
		"每次子请求必须携带调度 attempt 序号（task_attempt），request↔attempt 可对账")

	require.Equal(t, 2, provider.callCount(),
		"每次任务 attempt 恰一次 provider 调用（runtime 重试已关闭）：1 次失败 + 1 次成功")
}

// alwaysFailingChildProvider 让所有子请求在 provider 层失败（注入含 deadline 的
// provider 超时文本），用于锁定终态 cancel_source=provider_timeout 与
// retry_skipped 的可见性。
type alwaysFailingChildProvider struct {
	*roleRoutingLLMProvider
	mu      sync.Mutex
	calls   int
	errText string
}

func (p *alwaysFailingChildProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if !isSubagentChildRequest(req) {
		return p.roleRoutingLLMProvider.Call(ctx, req)
	}
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil, errors.New(p.errText)
}

func (p *alwaysFailingChildProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestSubagentAttemptCancelSourceAndRetrySkipOnProviderTimeout 锁定 §8.1 验收：
// provider 超时（文本含 deadline）连续失败时，attempt 1/2 成对可见，且终态事件
// 给出可区分的 cancel_source=provider_timeout 与
// retry_skipped=max_attempts_reached（不是只留下 Retryable=true）。
func TestSubagentAttemptCancelSourceAndRetrySkipOnProviderTimeout(t *testing.T) {
	rig := newAttemptEventRig(t)

	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	baseProvider := &SequenceLLMProvider{
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
			{Content: "Final answer from parent.", Model: "test-model"},
		},
	}
	provider := &alwaysFailingChildProvider{
		roleRoutingLLMProvider: &roleRoutingLLMProvider{SequenceLLMProvider: baseProvider},
		errText:                "UPSTREAM_UNAVAILABLE: provider call failed: context deadline exceeded (attempt-timeout test)",
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(rig.agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	_, err := loop.Run(context.Background(), "Find the root cause.")
	require.NoError(t, err)
	rig.waitTerminal(t)

	events := rig.snapshot()
	var startedAttempts []int
	finishedByAttempt := map[int]map[string]interface{}{}
	var completedEvents []map[string]interface{}
	for _, event := range events {
		switch event.eventType {
		case runtimeevents.EventSubagentAttemptStarted:
			if attempt, ok := event.payload["attempt"].(int); ok {
				startedAttempts = append(startedAttempts, attempt)
			}
		case runtimeevents.EventSubagentAttemptFinished:
			if attempt, ok := event.payload["attempt"].(int); ok {
				finishedByAttempt[attempt] = event.payload
			}
		case "subagent.completed":
			completedEvents = append(completedEvents, event.payload)
		}
	}

	require.ElementsMatch(t, []int{1, 2}, startedAttempts, "两次失败 attempt 都应有 started 事件")
	first := finishedByAttempt[1]
	require.NotNil(t, first, "attempt 1 必须有 finished 事件")
	require.Equal(t, false, first["success"])
	require.Equal(t, true, first["intermediate_attempt"])
	second := finishedByAttempt[2]
	require.NotNil(t, second, "attempt 2 必须有 finished 事件")
	require.Equal(t, false, second["success"])
	require.Equal(t, false, second["intermediate_attempt"])

	require.Len(t, completedEvents, 1, "任务只发一条终态 subagent.completed")
	final := completedEvents[0]
	require.Equal(t, false, final["success"])
	require.Equal(t, 2, final["attempt"])
	require.Equal(t, "provider_timeout", final["cancel_source"], "provider 文本超时必须与 task/batch deadline 区分")
	require.Equal(t, "max_attempts_reached", final["retry_skipped"], "达到上限的跳过原因必须可读")
	require.Equal(t, "timeout", final["failure_category"])
	require.Equal(t, 2, provider.callCount(), "每次 attempt 恰一次 provider 调用")
}
