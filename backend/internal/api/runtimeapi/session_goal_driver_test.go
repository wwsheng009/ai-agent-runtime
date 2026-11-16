package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// session_goal_driver_test.go 锁定 goal 驱动器的门控与容忍语义（gap 3）：
// 开关缺省关闭（行为零变化）、CLI 冷却窗让位（且先于 strike 记账，避免 CLI
// 轮污染服务端计数）、strike 阈值与 CLI 共享层同源。

func newGoalDriverTestHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{}
}

func TestGoalDriverDisabledByDefault(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	// A zero-value Handler has no runtime config; only DefaultRuntimeConfig
	// enables the driver. This pins the nil-config degradation.
	if handler.goalDriverEnabled() {
		t.Fatal("zero-value handler without runtime config must not enable the driver")
	}
}

func TestGoalDriverUserInterruptDisarmsUntilExplicitRearm(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	handler.runtimeConfig = runtimecfg.DefaultRuntimeConfig()
	state := handler.getGoalDriverState()
	sessionID := "sess-goal-disarm"

	handler.sessionGoalDriverEvaluate(goalDriverTurnOutcome{
		SessionID:    sessionID,
		Success:      false,
		CancelSource: "user_interrupt",
	})
	require.Equal(t, "user_interrupt", state.disarmedReason(sessionID))

	// A later terminal event must not re-enter the driver while disarmed.
	handler.sessionGoalDriverEvaluate(goalDriverTurnOutcome{
		SessionID: sessionID,
		Success:   true,
	})
	require.Equal(t, "user_interrupt", state.disarmedReason(sessionID))

	handler.goalDriverRearm(sessionID, "test_rearm")
	require.Empty(t, state.disarmedReason(sessionID))
}

func TestGoalDriverCLICooldownBeforeStrikes(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	state := handler.getGoalDriverState()
	sessionID := "sess-driver-cooldown"

	// CLI 在场（冷却窗内）→ 失败 turn 不得污染服务端 strike 计数。
	state.markCLISeen(sessionID, time.Now())
	handler.sessionGoalDriverEvaluate(goalDriverTurnOutcome{
		SessionID:  sessionID,
		Success:    false,
		ErrMessage: "boom",
	})
	state.mu.Lock()
	strikes := state.errorStrikes[sessionID]
	state.mu.Unlock()
	if strikes != 0 {
		t.Fatalf("CLI-visible turn must not pollute server strikes, got %d", strikes)
	}

	// CLI 离场（窗口过期）→ 同样的失败轮开始计数。
	state.mu.Lock()
	state.cliLastSeen[sessionID] = time.Now().Add(-2 * goalDriverCooldown)
	state.mu.Unlock()
	// 开关关闭时 evaluate 直接返回；直接驱动 strike 断言阈值路径。
	if got := state.noteError(sessionID); got != 1 {
		t.Fatalf("expected first strike after CLI leaves, got %d", got)
	}
}

func TestGoalDriverStrikeThresholdsMatchSharedLimits(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	state := handler.getGoalDriverState()
	sessionID := "sess-driver-strikes"

	// 第三次失败达到阈值（与 CLI 3 连同源）。
	for i := 1; i <= goal.ContinuationErrorStrikeLimit; i++ {
		if strikes := state.noteError(sessionID); strikes != i {
			t.Fatalf("strike %d recorded as %d", i, strikes)
		}
	}
	state.resetStrikes(sessionID)
	state.mu.Lock()
	errLeft := state.errorStrikes[sessionID]
	noProgLeft := state.noProgStrikes[sessionID]
	state.mu.Unlock()
	if errLeft != 0 || noProgLeft != 0 {
		t.Fatalf("resetStrikes must clear both counters, got err=%d noProg=%d", errLeft, noProgLeft)
	}

	for i := 1; i <= goal.ContinuationNoProgressStrikeLimit; i++ {
		if strikes := state.noteNoProgress(sessionID); strikes != i {
			t.Fatalf("no-progress strike %d recorded as %d", i, strikes)
		}
	}
}

func TestGoalDriverNoteCLIRefreshesWindow(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	state := handler.getGoalDriverState()
	sessionID := "sess-driver-note"

	// 陈旧在场标记 + noteCLI → 窗口刷新，cliActive 恢复 true。
	state.mu.Lock()
	state.cliLastSeen[sessionID] = time.Now().Add(-2 * goalDriverCooldown)
	state.mu.Unlock()
	if state.cliActive(sessionID, time.Now()) {
		t.Fatal("stale CLI mark must be outside the cooldown window")
	}
	handler.goalDriverNoteCLI(sessionID, "", false)
	if !state.cliActive(sessionID, time.Now()) {
		t.Fatal("goalDriverNoteCLI must refresh the CLI presence window")
	}
}

func TestGoalDriverCLIContinuationRearmsServerDisarm(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	state := handler.getGoalDriverState()
	sessionID := "sess-driver-cli-rearm"
	state.disarm(sessionID, "user_interrupt")

	handler.goalDriverNoteCLI(sessionID, goal.ContinuationPrompt, true)

	require.Empty(t, state.disarmedReason(sessionID))
}

func TestGoalDriverCLIContinueIntentRearmsServerDisarm(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	active, err := goal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	persistGoalForGuidanceTest(t, sessionManager, session, active)

	state := handler.getGoalDriverState()
	state.disarm(session.ID, "user_interrupt")

	handler.goalDriverNoteCLI(session.ID, "继续", false)

	require.Empty(t, state.disarmedReason(session.ID))
}

func TestGoalDriverSystemCancellationReevaluates(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	handler.runtimeConfig = runtimecfg.DefaultRuntimeConfig()
	active, err := goal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	persistGoalForGuidanceTest(t, sessionManager, session, active)

	handler.sessionGoalDriverEvaluate(goalDriverTurnOutcome{
		SessionID:    session.ID,
		RunSource:    goalDriverRunSource,
		CancelSource: "execution_context",
	})

	// A system cancellation is not a strike, but it must re-enter the decision
	// path. The admitted round is the observable proof that
	// sessionGoalDriverDecide ran (the submit fails later because this test
	// handler has no session hub).
	require.Eventually(t, func() bool {
		loaded, err := sessionManager.Get(context.Background(), session.ID)
		if err != nil || loaded == nil {
			return false
		}
		current, ok, err := goal.NewMetadataStore().Get(loaded)
		return err == nil && ok && current != nil && current.RoundsStarted == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.Empty(t, handler.getGoalDriverState().disarmedReason(session.ID))
	require.Zero(t, handler.getGoalDriverState().noProgressStrikes(session.ID))
}

// TestGoalDriverEscalationStateLifecycle 锁定升级标记的生命周期（P2-2' 入口 2）：
// 标记注入一次；有进展（resetStrikes）时一并清除，允许后续再次升级。
func TestGoalDriverEscalationStateLifecycle(t *testing.T) {
	state := newGoalDriverState()
	sessionID := "sess-driver-escalation"

	if state.escalationTried(sessionID) {
		t.Fatal("fresh state must not report an attempted escalation")
	}
	state.markEscalationAttempted(sessionID)
	if !state.escalationTried(sessionID) {
		t.Fatal("markEscalationAttempted must be observable via escalationTried")
	}
	if strikes := state.noProgressStrikes(sessionID); strikes != 0 {
		t.Fatalf("escalation bookkeeping must not touch the strike counter, got %d", strikes)
	}

	// 有进展 / 状态重置 → 升级标记与 strike 一并清除。
	state.noteNoProgress(sessionID)
	state.noteNoProgress(sessionID)
	if strikes := state.noProgressStrikes(sessionID); strikes != 2 {
		t.Fatalf("expected 2 no-progress strikes, got %d", strikes)
	}
	state.resetStrikes(sessionID)
	if state.escalationTried(sessionID) {
		t.Fatal("resetStrikes must clear the escalation-attempted mark")
	}
	if strikes := state.noProgressStrikes(sessionID); strikes != 0 {
		t.Fatalf("resetStrikes must clear no-progress strikes, got %d", strikes)
	}

	// doom_loop 上浮标记（OR 条件）：写入 → 可读；升级注入时消费；resetStrikes
	// 不自动清除（告警多发生在「有输出但重复工具批」的成功轮，否则信号会在
	// 评估阶段被误清）。
	state.noteDoomLoopWarning(sessionID)
	if !state.doomLoopAdvisedFor(sessionID) {
		t.Fatal("noteDoomLoopWarning must be observable via doomLoopAdvisedFor")
	}
	state.markEscalationAttempted(sessionID)
	if state.doomLoopAdvisedFor(sessionID) {
		t.Fatal("markEscalationAttempted must consume the doom-loop advisory")
	}
	state.noteDoomLoopWarning(sessionID)
	state.resetStrikes(sessionID)
	if !state.doomLoopAdvisedFor(sessionID) {
		t.Fatal("resetStrikes must preserve the doom-loop advisory until it is consumed")
	}
	// 空 sessionID 写入是无操作。
	state.noteDoomLoopWarning("  ")
}

// TestGoalDriverEscalationAvailabilityDefaultsOff 锁定服务端可用性口径：
// 未配置 runtimeConfig 的宿主（含默认测试 Handler）必须回落 iterate_goal
// 未启用（默认关闭），驱动器不得注入升级提示。
func TestGoalDriverEscalationAvailabilityDefaultsOff(t *testing.T) {
	handler := newGoalDriverTestHandler(t)
	if handler.goalDriverIterationEscalationAvailable() {
		t.Fatal("iterate_goal escalation must default off without configured agents.iterateGoalEnabled")
	}
	// 显式启用 → 可用（runtimeConfig 注入路径）。
	enabled := &Handler{runtimeConfig: &runtimecfg.RuntimeConfig{Agents: runtimecfg.AgentsConfig{IterateGoalEnabled: true}}}
	if !enabled.goalDriverIterationEscalationAvailable() {
		t.Fatal("configured agents.iterateGoalEnabled must enable escalation")
	}
}

func TestGoalDriverSessionScopedConfigResolution(t *testing.T) {
	handler, _, session := newGoalGuidanceTestHandler(t)
	base := runtimecfg.DefaultRuntimeConfig()
	base.Goal.MaxConsecutiveContinuations = 0
	base.Agents.IterateGoalEnabled = false
	scoped := runtimecfg.DefaultRuntimeConfig()
	scoped.Goal.MaxConsecutiveContinuations = 3
	scoped.Agents.IterateGoalEnabled = true
	handler.runtimeConfig = base
	handler.SetRuntimeConfigResolver(func(scope UsageScope) *runtimecfg.RuntimeConfig {
		if scope.TenantID == "tenant-goal" && scope.ProjectID == "project-goal" {
			return scoped
		}
		return base
	})
	handler.stampSessionUsageScope(context.Background(), session, UsageScope{
		TenantID:  "tenant-goal",
		ProjectID: "project-goal",
	})

	require.Equal(t, 3, handler.goalDriverContinuationLimitForSession(session))
	require.True(t, handler.goalDriverIterationEscalationAvailableForSession(session))
	require.Zero(t, handler.goalDriverContinuationLimit())
	require.False(t, handler.goalDriverIterationEscalationAvailable())
}

func TestGoalDriverDecideUsesSessionScopedContinuationLimit(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	base := runtimecfg.DefaultRuntimeConfig()
	base.Goal.MaxConsecutiveContinuations = 0
	scoped := runtimecfg.DefaultRuntimeConfig()
	scoped.Goal.MaxConsecutiveContinuations = 1
	handler.runtimeConfig = base
	handler.SetRuntimeConfigResolver(func(scope UsageScope) *runtimecfg.RuntimeConfig {
		if scope.TenantID == "tenant-goal" {
			return scoped
		}
		return base
	})
	handler.stampSessionUsageScope(context.Background(), session, UsageScope{
		TenantID: "tenant-goal",
	})
	active, err := goal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	persistGoalForGuidanceTest(t, sessionManager, session, active)
	handler.getGoalDriverState().noteContinuation(session.ID)

	handler.sessionGoalDriverDecide(goalDriverTurnOutcome{SessionID: session.ID})

	loaded, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	current, ok, err := goal.NewMetadataStore().Get(loaded)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, current.RoundsStarted,
		"session-scoped continuation limit must stop before admitting another round")
}

func TestUsageTotalTokensFallback(t *testing.T) {
	if got := usageTotalTokens(nil); got != 0 {
		t.Fatalf("nil usage must account 0, got %d", got)
	}
	if got := usageTotalTokens(&types.TokenUsage{PromptTokens: 7, CompletionTokens: 3}); got != 10 {
		t.Fatalf("missing total must fall back to prompt+completion, got %d", got)
	}
	if got := usageTotalTokens(&types.TokenUsage{TotalTokens: 42, PromptTokens: 7, CompletionTokens: 3}); got != 42 {
		t.Fatalf("explicit total must win, got %d", got)
	}
}

func TestGoalDriverContinuationRunOnlyOwnSource(t *testing.T) {
	require.True(t, goalDriverContinuationRun(goalDriverTurnOutcome{RunSource: goalDriverRunSource}))
	require.True(t, goalDriverContinuationRun(goalDriverTurnOutcome{RunSource: " " + goalDriverRunSource + " "}))
	require.False(t, goalDriverContinuationRun(goalDriverTurnOutcome{}))
	require.False(t, goalDriverContinuationRun(goalDriverTurnOutcome{RunSource: "cli"}))
	require.False(t, goalDriverContinuationRun(goalDriverTurnOutcome{RunSource: "agent_chat"}))
}

func TestGoalDriverHasPendingApprovalReadsDurableStateWithoutActor(t *testing.T) {
	handler := &Handler{}
	store := chat.NewInMemoryRuntimeStore(8)
	handler.sessionRuntimeStore = store

	sessionID := "sess-driver-durable-approval"
	require.False(t, handler.goalDriverHasPendingApproval(context.Background(), sessionID))

	require.NoError(t, store.SaveState(context.Background(), &chat.RuntimeState{
		SessionID: sessionID,
		Status:    chat.SessionWaitingApproval,
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-driver-1",
			SessionID: sessionID,
			ToolName:  "shell",
		},
		UpdatedAt: time.Now().UTC(),
	}))
	require.True(t, handler.goalDriverHasPendingApproval(context.Background(), sessionID),
		"a durable pending approval must block goal continuation even when no actor is materialized")
}

func TestGoalDriverDefersBudgetWrapUpWhileApprovalPending(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	handler.runtimeConfig = runtimecfg.DefaultRuntimeConfig()
	runtimeStore := chat.NewInMemoryRuntimeStore(8)
	handler.sessionRuntimeStore = runtimeStore
	require.NoError(t, runtimeStore.SaveState(context.Background(), &chat.RuntimeState{
		SessionID: session.ID,
		Status:    chat.SessionWaitingApproval,
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-budget-wrapup",
			SessionID: session.ID,
			ToolName:  "shell",
		},
		UpdatedAt: time.Now().UTC(),
	}))

	active, err := goal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	active.TokenBudget = 1
	persistGoalForGuidanceTest(t, sessionManager, session, active)

	handler.sessionGoalDriverDecide(goalDriverTurnOutcome{
		SessionID:   session.ID,
		UsageTokens: 2,
	})

	loaded, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	current, ok, err := goal.NewMetadataStore().Get(loaded)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, goal.StatusActive, current.Status,
		"pending approval must defer budget_limited transition until the decision resolves")
	require.Equal(t, 2, current.TokensUsed)
}

func TestGoalDriverKickRechecksGoalStateAndRoundLimit(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	handler.runtimeConfig = runtimecfg.DefaultRuntimeConfig()
	active, err := goal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	active.MaxRounds = 1
	active.RoundsStarted = 1
	persistGoalForGuidanceTest(t, sessionManager, session, active)

	handler.goalDriverKick(session.ID)

	require.Eventually(t, func() bool {
		loaded, loadErr := sessionManager.Get(context.Background(), session.ID)
		if loadErr != nil || loaded == nil {
			return false
		}
		current, ok, getErr := goal.NewMetadataStore().Get(loaded)
		return getErr == nil && ok && current != nil &&
			current.Status == goal.StatusBlocked &&
			current.BlockedCode == goal.BlockedCodeRoundLimit
	}, 2*time.Second, 10*time.Millisecond,
		"a stale kick must re-evaluate goal state instead of blindly submitting a continuation")
}

func TestGoalDriverDecisionAndBudgetGuards(t *testing.T) {
	state := newGoalDriverState()
	sessionID := "sess-driver-guards"

	require.True(t, state.beginDecision(sessionID))
	require.False(t, state.beginDecision(sessionID), "a second concurrent decision must be rejected")
	state.endDecision(sessionID)
	require.True(t, state.beginDecision(sessionID))
	state.endDecision(sessionID)

	require.Equal(t, 1, state.noteContinuation(sessionID))
	require.Equal(t, 2, state.noteContinuation(sessionID))
	require.Equal(t, 2, state.continuationCount(sessionID))
	state.resetContinuations(sessionID)
	require.Zero(t, state.continuationCount(sessionID))

	require.False(t, state.wrapUpSubmitted("goal|complete|t1"))
	state.markWrapUpSubmitted("goal|complete|t1")
	require.True(t, state.wrapUpSubmitted("goal|complete|t1"))
}

func TestGoalDriverOutputPresentFallback(t *testing.T) {
	require.True(t, goalDriverOutputPresent(map[string]interface{}{"output_present": true}))
	require.False(t, goalDriverOutputPresent(map[string]interface{}{"output_present": false}))
	require.True(t, goalDriverOutputPresent(map[string]interface{}{"partial_summary": "partial"}),
		"older failure events without output_present still carry their partial summary")
	require.False(t, goalDriverOutputPresent(map[string]interface{}{"partial_summary": "  "}))
}
