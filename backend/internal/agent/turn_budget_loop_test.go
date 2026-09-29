package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newTurnBudgetTestLoop(t *testing.T, maxSteps int, responses []*llm.LLMResponse) (*ReActLoop, *SequenceLLMProvider) {
	t.Helper()
	testAgent := &Agent{
		config: &Config{
			Name:         "turn-budget-agent",
			Model:        "test-provider",
			MaxSteps:     maxSteps,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{name: "test-provider", responses: responses}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	return NewReActLoop(testAgent, llmRuntime, &LoopReActConfig{
		MaxSteps:             maxSteps,
		EnableThought:        true,
		EnableToolCalls:      true,
		MaxParallelToolCalls: 1,
	}), provider
}

func turnBudgetToolCallResponse(content string, totalTokens int) *llm.LLMResponse {
	return &llm.LLMResponse{
		Content: content,
		Model:   "test-model",
		Usage:   &types.TokenUsage{PromptTokens: totalTokens - 20, CompletionTokens: 20, TotalTokens: totalTokens},
		ToolCalls: []types.ToolCall{
			{ID: "turn_budget_tool", Name: "ls", Args: map[string]interface{}{"path": "."}},
		},
	}
}

func turnBudgetRequestText(req *llm.LLMRequest) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
	for _, msg := range req.Messages {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func TestReActLoop_Run_InjectsTurnBudgetSoftLandingOnce(t *testing.T) {
	loop, provider := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		// 20000 的预算下，预检水位是 3200 token（足以容纳工具 schema），
		// 16800 恰好是 84% —— 高于 80% 收尾水位、低于硬边界。
		turnBudgetToolCallResponse("先看目录。", 16800),
		{Content: "收尾完成。", Model: "test-model", Usage: &types.TokenUsage{TotalTokens: 60}},
	})

	result, err := loop.run(context.Background(), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_soft",
		IncludePrompt: true,
		BudgetTokens:  20000,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 2, provider.callCount)

	// 80% 水位提示真实出现在第二次模型调用里，且只出现一次。
	require.True(t, result.TurnBudgetSoftCueInjected)
	second := turnBudgetRequestText(provider.requests[1])
	require.Contains(t, second, `<system-reminder kind="turn_budget">`)
	require.Contains(t, second, "tokens 84%")
	require.Contains(t, second, "Wrap up now")
	require.Equal(t, 1, strings.Count(second, "Wrap up now"), "cue must be injected once per turn")

	// 结果字段是同一份水位口径：收尾判决 + 最终水位行（含水印本身）。
	require.Equal(t, TurnBudgetLevelSoft, result.TurnBudgetLevel)
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", result.TurnBudgetLine)
}

func TestReActLoop_Run_TokenBudgetHardStopIsGraceful(t *testing.T) {
	loop, provider := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("继续探索。", 20000),
		{Content: "这条回复不应出现。", Model: "test-model"},
	})

	var persisted []types.Message
	result, err := loop.run(context.Background(), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_hard",
		IncludePrompt: true,
		BudgetTokens:  20000,
		PersistHistory: func(messages []types.Message) error {
			persisted = messages
			return nil
		},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.True(t, result.LimitReached)
	require.Equal(t, "turn_budget", result.LimitReason)
	require.Contains(t, result.Output, "token 预算上限")
	require.Contains(t, result.Output, "tokens 100%")
	require.Equal(t, TurnBudgetLevelHard, result.TurnBudgetLevel)
	require.False(t, result.TurnBudgetSoftCueInjected, "hard stop must not inject a cue the model never reads")

	// 硬边界不是静默截断：不再调用模型，且把收尾文案写回可持久化历史。
	require.Equal(t, 1, provider.callCount)
	require.NotEmpty(t, persisted)
	require.Equal(t, "assistant", persisted[len(persisted)-1].Role)
	require.Equal(t, result.Output, persisted[len(persisted)-1].Content)
}

func TestReActLoop_Run_StepLimitReportsTurnBudgetReason(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 1, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 20),
		{Content: "这条回复不应出现。", Model: "test-model"},
	})

	result, err := loop.run(context.Background(), "查看目录。", loopRunOptions{
		TraceID:       "trace_turn_budget_steps",
		IncludePrompt: true,
	})
	require.NoError(t, err)
	require.True(t, result.LimitReached)
	require.Equal(t, "step_limit", result.LimitReason)
	require.Equal(t, 1, result.StepLimit)
	require.Equal(t, TurnBudgetLevelHard, result.TurnBudgetLevel)
	require.Equal(t, "turn budget: step 1/1", result.TurnBudgetLine)
}

// 落点 B 的桥接契约：TUI 状态行只认「带 turn 身份 + kind=turn_budget」的
// system_reminder.injected。这里走真实 EventBus 事件流，钉住宿主消费的三个前置条件：
// 事件类型、payload kind、payload turn_id（由 emitRuntimeEvent 按 run ctx 统一盖章）。
func TestReActLoop_Run_TurnBudgetReminderEventCarriesTurnIdentity(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 16800),
		{Content: "收尾完成。", Model: "test-model", Usage: &types.TokenUsage{TotalTokens: 60}},
	})
	bus := runtimeevents.NewBus()
	var reminders []runtimeevents.Event
	bus.Subscribe(EventSystemReminderInjected, func(event runtimeevents.Event) {
		reminders = append(reminders, event)
	})
	loop.agent.SetEventBus(bus)

	result, err := loop.run(WithTurnID(context.Background(), "turn-budget-bridge"), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_bridge",
		IncludePrompt: true,
		BudgetTokens:  20000,
	})
	require.NoError(t, err)
	require.True(t, result.TurnBudgetSoftCueInjected)

	var budgetEvent *runtimeevents.Event
	for i := range reminders {
		if reminders[i].Payload["kind"] == ReminderKindTurnBudget {
			budgetEvent = &reminders[i]
		}
	}
	require.NotNil(t, budgetEvent, "soft landing must emit %s with kind=%s", EventSystemReminderInjected, ReminderKindTurnBudget)
	require.Equal(t, "turn-budget-bridge", budgetEvent.Payload["turn_id"], "host keys the status line on the durable turn identity")
	require.Equal(t, TurnBudgetLevelSoft, budgetEvent.Payload["turn_budget_level"])
	require.Equal(t, true, budgetEvent.Payload["durable"], "wrap-up cue must survive into the next turn")
	// 事件里的行是**通告时刻**的水位；result.TurnBudgetLine 是退出时刻的终局水位。
	// 二者口径同源但取样点不同（step 1/10 通告，step 2/10 收尾），宿主不应把事件行
	// 当作终值使用。
	require.Equal(t, "turn budget: step 1/10 · tokens 84%", budgetEvent.Payload["turn_budget_line"])
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", result.TurnBudgetLine)
}

// TestReActLoop_Run_EmitsTurnLifecycleEvents 是 PR-4 落点 C 的回归：每轮 run 必须
// 成对发出 agent.turn.started/finished（observe 的 RunningTurns 与 /debug 的 turn
// 水位区块都依赖它们），且 finished 携带的终局水位与 Result 同源。
func TestReActLoop_Run_EmitsTurnLifecycleEvents(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 16800),
		{Content: "收尾完成。", Model: "test-model", Usage: &types.TokenUsage{TotalTokens: 60}},
	})
	bus := runtimeevents.NewBus()
	var started, finished []runtimeevents.Event
	bus.Subscribe("agent.turn.started", func(event runtimeevents.Event) { started = append(started, event) })
	bus.Subscribe("agent.turn.finished", func(event runtimeevents.Event) { finished = append(finished, event) })
	loop.agent.SetEventBus(bus)

	result, err := loop.run(WithTurnID(context.Background(), "turn-lifecycle"), "查看目录并总结。", loopRunOptions{
		SessionID:     "session-turn-lifecycle",
		TraceID:       "trace_turn_lifecycle",
		IncludePrompt: true,
		BudgetTokens:  20000,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	require.Len(t, started, 1, "started must be emitted exactly once per run")
	require.Len(t, finished, 1, "finished must be emitted exactly once per run")
	require.Equal(t, "session-turn-lifecycle", started[0].SessionID)
	require.Equal(t, "session-turn-lifecycle", finished[0].SessionID)
	require.Equal(t, "trace_turn_lifecycle", started[0].TraceID)
	require.Equal(t, "trace_turn_lifecycle", finished[0].TraceID)
	require.Equal(t, "turn-lifecycle", finished[0].Payload["turn_id"], "turn 水位必须能按 durable turn id 归属")
	require.Equal(t, NormalizeMaxSteps(10), started[0].Payload["max_steps"])
	require.Equal(t, 0, started[0].Payload["step"])
	require.Equal(t, TurnBudgetLevelOK, started[0].Payload["budget_level"])

	require.Equal(t, result.Steps, finished[0].Payload["step"])
	require.Equal(t, result.TurnBudgetLevel, finished[0].Payload["budget_level"])
	ratio, ok := finished[0].Payload["budget_ratio"].(float64)
	require.True(t, ok, "budget_ratio must be a float64, got %T", finished[0].Payload["budget_ratio"])
	require.InDelta(t, 0.84, ratio, 1e-9)
	elapsed, ok := finished[0].Payload["elapsed_ms"].(int64)
	require.True(t, ok, "elapsed_ms must be int64, got %T", finished[0].Payload["elapsed_ms"])
	require.GreaterOrEqual(t, elapsed, int64(0))
}

// TestReActLoop_Run_TurnBudgetDefaultsFromLoopConfig 覆盖主 chat 路径接线：
// CLI --budget-tokens → ChatSession → LoopReActConfig.TurnBudgetTokens。runtimechat
// actor 只携带 LoopConfig、不显式传 loopRunOptions.BudgetTokens，因此缺省预算必须
// 由循环配置生效（PR-4 §6.4 待落地第 2 条）。
func TestReActLoop_Run_TurnBudgetDefaultsFromLoopConfig(t *testing.T) {
	loop, provider := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 16800),
		{Content: "收尾完成。", Model: "test-model", Usage: &types.TokenUsage{TotalTokens: 60}},
	})
	loop.config.TurnBudgetTokens = 20000

	result, err := loop.run(context.Background(), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_from_config",
		IncludePrompt: true,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, result.TurnBudgetSoftCueInjected)
	require.Equal(t, TurnBudgetLevelSoft, result.TurnBudgetLevel)
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", result.TurnBudgetLine)
	require.Contains(t, turnBudgetRequestText(provider.requests[1]), "tokens 84%")
}

// TestReActLoop_Run_ExplicitBudgetOverridesLoopConfig 钉住优先级：显式
// loopRunOptions.BudgetTokens（子代理/团队的任务预算）必须压过宿主配置缺省。
func TestReActLoop_Run_ExplicitBudgetOverridesLoopConfig(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 16800),
		{Content: "收尾完成。", Model: "test-model", Usage: &types.TokenUsage{TotalTokens: 60}},
	})
	// 缺省故意放大：若被误当作用量口径，16800 token 不会触发软着陆。
	loop.config.TurnBudgetTokens = 1000000

	result, err := loop.run(context.Background(), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_explicit",
		IncludePrompt: true,
		BudgetTokens:  20000,
	})
	require.NoError(t, err)
	require.True(t, result.TurnBudgetSoftCueInjected)
	require.Equal(t, TurnBudgetLevelSoft, result.TurnBudgetLevel)
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", result.TurnBudgetLine)
}

// TestReActLoop_Run_EmitsLiveBudgetEvents 钉住建议稿 §4.2 的"运行中实时超支告警"
// 事件面：软着陆跨线发 budget_warning（一次性、独立可订阅事件名 + 水位载荷），
// token 硬停发 budget_exhausted（此前该路径事件面完全静默，只能等终局 result）。
func TestReActLoop_Run_EmitsLiveBudgetEvents(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 16800),
		turnBudgetToolCallResponse("继续推进。", 20000),
	})
	bus := runtimeevents.NewBus()
	var warnings, exhausted []runtimeevents.Event
	bus.Subscribe(EventTurnBudgetWarning, func(event runtimeevents.Event) { warnings = append(warnings, event) })
	bus.Subscribe(EventTurnBudgetExhausted, func(event runtimeevents.Event) { exhausted = append(exhausted, event) })
	loop.agent.SetEventBus(bus)

	result, err := loop.run(WithTurnID(context.Background(), "turn-budget-live"), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_live",
		IncludePrompt: true,
		BudgetTokens:  20000,
	})
	require.NoError(t, err)
	require.True(t, result.LimitReached)
	require.Equal(t, "turn_budget", result.LimitReason)
	require.Equal(t, TurnBudgetLevelHard, result.TurnBudgetLevel)

	require.Len(t, warnings, 1, "soft crossing must emit exactly one live warning")
	require.Equal(t, "turn-budget-live", warnings[0].Payload["turn_id"])
	require.Equal(t, TurnBudgetLevelSoft, warnings[0].Payload["turn_budget_level"])
	require.Equal(t, 20000, warnings[0].Payload["budget_tokens"])
	require.Equal(t, 16800, warnings[0].Payload["tokens_spent"])
	require.NotEmpty(t, warnings[0].Payload["turn_budget_line"])

	require.Len(t, exhausted, 1, "hard token stop must emit exactly one exhausted event")
	require.Equal(t, "turn-budget-live", exhausted[0].Payload["turn_id"])
	require.Equal(t, TurnBudgetLevelHard, exhausted[0].Payload["turn_budget_level"])
	require.Equal(t, 20000, exhausted[0].Payload["budget_tokens"])
}

// TestReActLoop_Run_ReportsBudgetWatermarkToHost 钉住 §4.2 的宿主回写面：水位变化
// 时循环必须把同一份判决（等级/行/比例）交给 OnBudgetProgress，宿主才能把它写进
// 监督账本；nil 回调保持完全 no-op（未接线宿主行为不变）。
func TestReActLoop_Run_ReportsBudgetWatermarkToHost(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 10, []*llm.LLMResponse{
		turnBudgetToolCallResponse("先看目录。", 16800),
		{Content: "收尾完成。", Model: "test-model", Usage: &types.TokenUsage{TotalTokens: 60}},
	})
	var mu sync.Mutex
	var states []TurnBudgetState
	loop.config.OnBudgetProgress = func(_ context.Context, state TurnBudgetState) {
		mu.Lock()
		states = append(states, state)
		mu.Unlock()
	}

	result, err := loop.run(context.Background(), "查看目录并总结。", loopRunOptions{
		TraceID:       "trace_turn_budget_report",
		IncludePrompt: true,
		BudgetTokens:  20000,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, states, "水位变化必须上报宿主")
	last := states[len(states)-1]
	require.Equal(t, TurnBudgetLevelSoft, last.Level)
	require.Contains(t, last.Line, "tokens 84%")
	require.Greater(t, last.Ratio, 0.8)
	require.LessOrEqual(t, len(states), 4, "同一水位不得每步重复上报")
}
