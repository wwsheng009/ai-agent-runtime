package commands

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSendMessage_AutoContinuationUnlimitedByDefault(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	// 默认 0 = 不限制（对齐 Codex 的 continue_if_idle）：由模型在若干轮后完成
	// goal 才停止，而不是被固定次数截断。
	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			if fake.continuations < 8 {
				return "working", nil
			}
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "completed after eight unlimited continuations",
			}); err != nil {
				return "", err
			}
			return "done", nil
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if len(executor.prompts) != 1 || executor.prompts[0] != "start" {
		t.Fatalf("expected only the initial prompt as user prompt, got %#v", executor.prompts)
	}
	if executor.continuations != 8 {
		t.Fatalf("expected 8 hidden continuations until completion, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusComplete)
}

func TestSendMessage_AutoContinuationRespectsConfiguredLimit(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 8

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != 8 {
		t.Fatalf("expected configured safety cap of 8 continuations, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusActive)
}

func TestSendMessage_AutoContinuationStopsAfterModelMarksGoalComplete(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "all requested work is complete",
			}); err != nil {
				return "", err
			}
			return "ok", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != 1 {
		t.Fatalf("expected continuation to run once, got %d", executor.continuations)
	}
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected goal metadata, ok=%v err=%v", ok, err)
	}
	if goal.Status != runtimegoal.StatusComplete || goal.CompletedBy != "model" {
		t.Fatalf("expected model-completed goal, got %#v", goal)
	}
}

func TestSendMessage_AutoContinuesActiveGoalAfterInitialError(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		err: context.DeadlineExceeded,
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "goal completed after retry",
			}); err != nil {
				return "", err
			}
			return "ok", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("expected active goal continuation to recover initial error, got %v", err)
	}
	if executor.continuations != 1 {
		t.Fatalf("expected one continuation after initial error, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusComplete)
}

func TestSendMessage_AutoContinuationRetriesErrorsUntilGoalComplete(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			if ctx == nil {
				t.Fatal("expected continuation context")
			}
			if session.ChatExecutor.(*fakeChatExecutor).continuations < 3 {
				return "", errors.New("transient continuation failure")
			}
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "goal completed after retrying continuation errors",
			}); err != nil {
				return "", err
			}
			return "ok", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != 3 {
		t.Fatalf("expected continuation to retry until third attempt, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusComplete)
}

func TestSendMessage_DoesNotAutoContinueInactiveGoal(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	defer cleanup()

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if len(executor.prompts) != 1 {
		t.Fatalf("expected no continuation for paused goal, got %#v", executor.prompts)
	}
	if executor.continued {
		t.Fatal("expected no hidden continuation for paused goal")
	}
}

func TestSendMessage_MissingGoalDoesNotMaskInitialTurnError(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	updated, err := runtimegoal.NewMetadataStore().ClearPersistent(context.Background(), session.SessionManager.GetStorage(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("ClearPersistent: %v", err)
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		t.Fatalf("restoreChatStateFromRuntimeSession: %v", err)
	}

	executor := &fakeChatExecutor{err: context.DeadlineExceeded}
	session.ChatExecutor = executor
	_, err = sendMessage(session, "start")
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("expected original turn error to remain visible, got %v", err)
	}
	if executor.continuations != 0 {
		t.Fatalf("expected missing goal to suppress continuation, got %d", executor.continuations)
	}
}

func TestSendMessage_DoesNotAutoContinueWhenInputQueued(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.InputQueue = newChatInputQueue(nil)
	session.InputQueue.routeLine(chatQueuedInput{Text: "new user input"})

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if len(executor.prompts) != 1 {
		t.Fatalf("expected queued input to suppress continuation, got %#v", executor.prompts)
	}
	if executor.continued {
		t.Fatal("expected queued input to suppress hidden continuation")
	}
}

func TestSendMessage_DoesNotAutoContinueNonInteractive(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.NoInteractive = true

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continued {
		t.Fatal("expected non-interactive mode to suppress hidden continuation")
	}
}

func TestShouldAutoContinueActiveGoalAllowsActorWhenGoalToolAvailable(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.ChatExecutor = &aicliActorChatExecutor{}
	session.LocalRuntimeHost = &localChatRuntimeHost{
		ToolSurface: wrapGoalToolSurface(session, nil),
	}

	shouldContinue, err := shouldAutoContinueActiveGoal(session)
	if err != nil {
		t.Fatalf("shouldAutoContinueActiveGoal failed: %v", err)
	}
	if !shouldContinue {
		t.Fatal("expected actor-first session to auto-continue when update_goal is exposed")
	}
}

func TestSendMessage_AutoContinuationErrorDoesNotFailInitialResponse(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			return "", context.Canceled
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("expected initial response to remain successful, got error: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	// 真实取消（context.Canceled）立即停止续跑，不计入错误容忍。
	if executor.continuations != 1 {
		t.Fatalf("expected canceled continuation to stop after one attempt, got %d", executor.continuations)
	}
}

func TestSendMessage_AutoContinuationStopsAfterThreeConsecutiveErrors(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			return "", errors.New("persistent continuation failure")
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("expected initial response to remain successful, got error: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if executor.continuations != goalAutoContinuationErrorStrikeLimit {
		t.Fatalf("expected continuation to stop at %d consecutive errors, got %d",
			goalAutoContinuationErrorStrikeLimit, executor.continuations)
	}
	// 对齐 Codex：连续失败停止并把目标置为 blocked（/goal resume 解除）。
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

func TestSendMessage_AutoContinuationBlocksAfterThreeNoProgressRounds(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			return "", nil
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("expected initial response to remain successful, got error: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if executor.continuations != goalAutoContinuationNoProgressStrikeLimit {
		t.Fatalf("expected continuation to stop at %d no-progress rounds, got %d",
			goalAutoContinuationNoProgressStrikeLimit, executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

func TestSendMessage_NoProgressStrikesResetOnToolActivity(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 5

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			if fake.continuations == 3 {
				// 第 3 轮有工具执行（计数增长）→ 空输出也不算空转，计数清零。
				recordChatTurnToolExecution(session)
			}
			return "", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	// 轮次：1/2 空转各记 1 strike；3 有工具执行清零；4/5 再各记 1；到 cap=5 停下。
	if executor.continuations != 5 {
		t.Fatalf("expected tool activity to reset no-progress strikes (cap 5), got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusActive)
}

// TestSendMessage_NoProgressEscalatesToFreshContextIteration 覆盖升级路径
// （P2-2' 入口 2）：连续无进展达到共享阈值（2）且部署启用 iterate_goal 时，
// 下一次续跑改用 IterateEscalationPrompt（恰好一次），随后仍无进展则按原
// 停止线置 blocked。未完成升级注入的轮次仍是默认续跑提示。
func TestSendMessage_NoProgressEscalatesToFreshContextIteration(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	// 部署启用 iterate_goal（本地宿主的 agents 配置是驱动器的可用性判定来源）。
	session.LocalRuntimeHost = &localChatRuntimeHost{
		RuntimeConfig: &runtimecfg.RuntimeConfig{
			Agents: runtimecfg.AgentsConfig{IterateGoalEnabled: true},
		},
	}

	var continuedPrompts []string
	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			continuedPrompts = append(continuedPrompts, fake.lastContinuationPrompt)
			return "", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != goalAutoContinuationNoProgressStrikeLimit {
		t.Fatalf("expected %d continuations before blocked, got %d",
			goalAutoContinuationNoProgressStrikeLimit, executor.continuations)
	}
	if len(continuedPrompts) != 3 {
		t.Fatalf("expected 3 continuation prompts, got %d", len(continuedPrompts))
	}
	if continuedPrompts[0] != goalAutoContinuationPrompt || continuedPrompts[1] != goalAutoContinuationPrompt {
		t.Fatalf("pre-threshold continuations must use the default prompt, got %q / %q",
			continuedPrompts[0], continuedPrompts[1])
	}
	if continuedPrompts[2] != runtimegoal.IterateEscalationPrompt {
		t.Fatalf("threshold continuation must use the escalation prompt, got %q", continuedPrompts[2])
	}
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

// TestSendMessage_NoProgressEscalationUnavailableWithoutTool 锁定回退口径：
// 部署未启用 iterate_goal 时，驱动器不得注入升级提示（避免指示不存在的工具），
// 空转全部走默认续跑提示后按停止线置 blocked。
func TestSendMessage_NoProgressEscalationUnavailableWithoutTool(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	// 未挂载宿主（等价 iterate_goal 不可用）。

	var continuedPrompts []string
	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			continuedPrompts = append(continuedPrompts, fake.lastContinuationPrompt)
			return "", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	for i, prompt := range continuedPrompts {
		if prompt != goalAutoContinuationPrompt {
			t.Fatalf("continuation %d must stay on the default prompt when the tool is unavailable, got %q", i+1, prompt)
		}
	}
	if executor.continuations != goalAutoContinuationNoProgressStrikeLimit {
		t.Fatalf("expected stop at the no-progress limit, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

// TestSendMessage_NoProgressEscalationResetsAfterProgress 锁定升级状态的重置：
// 升级轮产生实质进展后，strike 与升级标记一并清零；后续再次连续空转时允许
// 再来一次升级（而不是永久失去该路径）。
func TestSendMessage_NoProgressEscalationResetsAfterProgress(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 6
	session.LocalRuntimeHost = &localChatRuntimeHost{
		RuntimeConfig: &runtimecfg.RuntimeConfig{
			Agents: runtimecfg.AgentsConfig{IterateGoalEnabled: true},
		},
	}

	var continuedPrompts []string
	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			continuedPrompts = append(continuedPrompts, fake.lastContinuationPrompt)
			if fake.continuations == 3 {
				// 升级轮（第 3 次续跑）产生非空输出 → 有进展，重置 strike 与升级标记。
				return "escalated progress", nil
			}
			return "", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if len(continuedPrompts) != 6 {
		t.Fatalf("expected 6 continuations, got %d", len(continuedPrompts))
	}
	// 第一段：默认 / 默认 / 升级；第二段（重置后）：默认 / 默认 / 升级后触发停止线。
	wantPrompts := []string{
		goalAutoContinuationPrompt,
		goalAutoContinuationPrompt,
		runtimegoal.IterateEscalationPrompt,
		goalAutoContinuationPrompt,
		goalAutoContinuationPrompt,
		runtimegoal.IterateEscalationPrompt,
	}
	for i, want := range wantPrompts {
		if continuedPrompts[i] != want {
			t.Fatalf("continuation %d prompt = %q, want %q", i+1, continuedPrompts[i], want)
		}
	}
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

// TestSendMessage_DoomLoopWarningEscalatesToIteration 覆盖入口 2 的 doom_loop
// OR 条件：续跑轮内收到会话级 tool_loop.doom_loop_warning 且部署启用
// iterate_goal 时，下一次续跑立即升级（无需等无进展 strike 达阈值）；该标记
// 被升级消费一次，后续空转回到默认提示并按原停止线置 blocked。
func TestSendMessage_DoomLoopWarningEscalatesToIteration(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	bus := runtimeevents.NewBus()
	session.LocalRuntimeHost = &localChatRuntimeHost{
		EventBus: bus,
		RuntimeConfig: &runtimecfg.RuntimeConfig{
			Agents: runtimecfg.AgentsConfig{IterateGoalEnabled: true},
		},
	}

	var continuedPrompts []string
	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			continuedPrompts = append(continuedPrompts, fake.lastContinuationPrompt)
			if fake.continuations == 1 {
				// 首轮续跑内 agent 循环回放同一语义工具批 → 发 doom_loop 告警。
				bus.Publish(runtimeevents.Event{Type: agent.EventDoomLoopWarning, SessionID: session.RuntimeSession.ID})
				return "progress", nil
			}
			return "", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	// 轮次序列：1）有进展 + 告警 → 2）升级轮（消费告警）；3/4）空转 strike 累计，
	// 第 4 轮达停止线置 blocked。
	if executor.continuations != 4 {
		t.Fatalf("expected 4 continuations, got %d", executor.continuations)
	}
	wantPrompts := []string{
		goalAutoContinuationPrompt,
		runtimegoal.IterateEscalationPrompt,
		goalAutoContinuationPrompt,
		goalAutoContinuationPrompt,
	}
	for i, want := range wantPrompts {
		if continuedPrompts[i] != want {
			t.Fatalf("continuation %d prompt = %q, want %q", i+1, continuedPrompts[i], want)
		}
	}
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

func TestSendMessage_AccountsGoalUsageAndStopsAtBudget(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	// 预算 250：初始 turn 计 100，续跑每轮计 100 → 第 2 轮后 300 触发收尾。
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected goal metadata, ok=%v err=%v", ok, err)
	}
	goal.TokenBudget = 250
	if err := runtimegoal.NewMetadataStore().Put(session.RuntimeSession, *goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	resetGoalAccountingBaseline(session)

	executor := &fakeChatExecutor{
		output: "initial response",
		onCall: func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
			session.TokenCount += 100
			return "initial response", nil
		},
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			session.TokenCount += 100
			return "working", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}

	updated, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || updated == nil {
		t.Fatalf("expected updated goal, ok=%v err=%v", ok, err)
	}
	if updated.Status != runtimegoal.StatusBudgetLimited {
		t.Fatalf("expected budget_limited, got %s (used=%d budget=%d)", updated.Status, updated.TokensUsed, updated.TokenBudget)
	}
	if updated.TokensUsed < 250 {
		t.Fatalf("expected accounted usage >= budget, got %d", updated.TokensUsed)
	}
	if executor.lastContinuationPrompt != goalBudgetWrapUpPrompt {
		t.Fatalf("expected the wrap-up prompt as the last continuation, got %q", executor.lastContinuationPrompt)
	}
	// 2 轮正常续跑 + 1 轮预算收尾。
	if executor.continuations != 3 {
		t.Fatalf("expected 3 continuation calls (2 + wrap-up), got %d", executor.continuations)
	}
}

func TestGoalAutoContinuationLimitEnvFallback(t *testing.T) {
	t.Setenv(goalMaxContinuationsEnv, "2")
	if got := goalAutoContinuationLimit(nil); got != 2 {
		t.Fatalf("expected env fallback 2, got %d", got)
	}
	if got := goalAutoContinuationLimit(&ChatSession{}); got != 2 {
		t.Fatalf("expected env fallback for unset session, got %d", got)
	}
	if got := goalAutoContinuationLimit(&ChatSession{goalMaxContinuations: 5}); got != 5 {
		t.Fatalf("explicit session config must win over env, got %d", got)
	}
	t.Setenv(goalMaxContinuationsEnv, "-3")
	if got := goalAutoContinuationLimit(&ChatSession{}); got != 0 {
		t.Fatalf("invalid env must fall back to unlimited, got %d", got)
	}
}

func TestRequestAndConsumeGoalKickRunsContinuation(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 2

	executor := &fakeChatExecutor{output: "kick response"}
	session.ChatExecutor = executor

	if !requestGoalContinuationKick(session) {
		t.Fatal("expected kick request to be accepted for interactive active goal")
	}
	if !session.goalKickPending.Load() {
		t.Fatal("expected kick flag to be set")
	}
	maybeConsumeDeferredGoalKick(session)
	if session.goalKickPending.Load() {
		t.Fatal("expected kick flag to be consumed")
	}
	if executor.continuations != 2 {
		t.Fatalf("expected kick to run the continuation loop (2 capped rounds), got %d", executor.continuations)
	}
}

func TestRequestGoalKickRejectedForPausedOrNonInteractive(t *testing.T) {
	paused, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	defer cleanup()
	if requestGoalContinuationKick(paused) {
		t.Fatal("paused goal must not accept a kick")
	}

	active, cleanupActive := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanupActive()
	active.NoInteractive = true
	if requestGoalContinuationKick(active) {
		t.Fatal("non-interactive session must not accept a kick")
	}
}

// TestQueuedInputDrainCompletionReevaluatesHeldGoalKick 锁定 2026-10-18 真机
// 复现：kick 在「queued=0 但 draining=true」的瞬时判定里被保留后，主循环会阻塞
// 在读取上，没有任何事件再唤醒它——/goal resume 后续跑永不启动。排空状态落地
// （prepareInteractiveRead 的清零路径）必须立即重估并消费该 kick。
func TestQueuedInputDrainCompletionReevaluatesHeldGoalKick(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 1
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))

	executor := &fakeChatExecutor{output: "held kick resumed"}
	session.ChatExecutor = executor

	if !requestGoalContinuationKick(session) {
		t.Fatal("expected kick request to be accepted for interactive active goal")
	}
	// 真机时序：kick 已请求，队列处于"排空中（draining=true）且无排队行"。
	session.setQueuedInputDrainActive(true)
	if _, _, err := prepareInteractiveRead(session); err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if session.goalKickPending.Load() {
		t.Fatal("drain completion must re-evaluate and consume the held kick")
	}
	if executor.continuations != 1 {
		t.Fatalf("held kick must resume the continuation loop (1 capped round), got %d", executor.continuations)
	}
}

// TestQueuedInputSkipReArmsGoalKickForLaterConsumption 锁定 2026-10-18 真机第二处
// 同类缺陷：用户「继续」rearm 后，收尾续跑决策撞上 draining 瞬时态返回
// queued_input——若按终局跳过，主循环随后阻塞在读取上，没有任何事件再触发
// 续跑（真机 07:49 复现：rearm 成功但 attempt=0）。让位必须转成延迟 kick，并在
// 排空落地后被消费启动续跑。
func TestQueuedInputSkipReArmsGoalKickForLaterConsumption(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 1
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	executor := &fakeChatExecutor{output: "queued skip then resumed"}
	session.ChatExecutor = executor

	// 排空中（queued=0, draining=true）：续跑决策应让位并重排 kick。
	session.setQueuedInputDrainActive(true)
	if err := maybeAutoContinueActiveGoal(context.Background(), session, executor); err != nil {
		t.Fatalf("maybeAutoContinueActiveGoal: %v", err)
	}
	if !session.goalKickPending.Load() {
		t.Fatal("queued_input skip must re-arm the kick for later re-evaluation")
	}
	if executor.continuations != 0 {
		t.Fatalf("skipped turn must not run continuations, got %d", executor.continuations)
	}

	// 排空落地：prepareInteractiveRead 的清零路径消费 kick 并启动续跑。
	if _, _, err := prepareInteractiveRead(session); err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if session.goalKickPending.Load() {
		t.Fatal("drain completion must consume the re-armed kick")
	}
	if executor.continuations != 1 {
		t.Fatalf("expected 1 capped continuation after drain, got %d", executor.continuations)
	}
}

func TestAICLISharedChatExecutor_GoalContinuationDoesNotPersistInstructionPrompt(t *testing.T) {
	originalExecute := executeToolLoop
	originalCompact := autoCompactSharedChatHistory
	defer func() {
		executeToolLoop = originalExecute
		autoCompactSharedChatHistory = originalCompact
	}()

	autoCompactSharedChatHistory = func(ctx context.Context, session *ChatSession, history []types.Message) ([]types.Message, *sharedChatAutoCompactReport, error) {
		t.Fatal("goal continuation should not run pre-turn auto compaction")
		return history, nil, nil
	}

	var captured runtimechatcore.ToolLoopRequest
	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		captured = req
		return &runtimechatcore.ToolLoopResult{
			Response: &runtimechatcore.ChatResult{Output: "audit done"},
			History: append(append([]types.Message(nil), req.History...),
				*types.NewAssistantMessage("audit done"),
			),
		}, nil
	}

	session := &ChatSession{
		Messages: []types.Message{
			*types.NewSystemMessage("system"),
			*types.NewUserMessage("start"),
			*types.NewAssistantMessage("initial response"),
		},
	}
	output, err := (&aicliSharedChatExecutor{}).ContinueGoal(context.Background(), session)
	if err != nil {
		t.Fatalf("ContinueGoal failed: %v", err)
	}
	if output != "audit done" {
		t.Fatalf("unexpected output: %q", output)
	}
	if captured.Prompt != "" {
		t.Fatalf("expected empty visible prompt for hidden continuation, got %q", captured.Prompt)
	}
	if len(captured.History) == 0 || !strings.Contains(captured.History[len(captured.History)-1].Content, "Continue working toward") {
		t.Fatalf("expected provider history to include hidden continuation instruction, got %#v", captured.History)
	}
	for _, message := range session.Messages {
		if strings.Contains(message.Content, "Continue working toward") {
			t.Fatalf("did not expect continuation instruction to persist in session history: %#v", session.Messages)
		}
		if message.Metadata != nil {
			if value, ok := message.Metadata.Get(goalContinuationMetadataKey); ok {
				t.Fatalf("did not expect hidden continuation metadata to persist, got %v in %#v", value, session.Messages)
			}
		}
	}
	if got := session.Messages[len(session.Messages)-1].Content; got != "audit done" {
		t.Fatalf("expected continuation assistant response to persist, got %q", got)
	}
}

func newGoalAutoContinueTestSession(t *testing.T, status runtimegoal.Status) (*ChatSession, func()) {
	t.Helper()
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	runtimeSession, err := manager.Create(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	goal, err := runtimegoal.NewSessionGoal(runtimeSession.ID, "finish the requested implementation", runtimeSession.CreatedAt)
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = status
	if err := runtimegoal.NewMetadataStore().Put(runtimeSession, goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	if err := manager.Update(context.Background(), runtimeSession); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}
	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "test-user",
		cancelCtx:      context.Background(),
	}
	registerGoalFunctions(session)
	return session, manager.Stop
}
