package runtimeapi

import (
	"testing"
	"time"

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
	// 无配置（resolveRuntimeConfig 零值路径）时开关必须为 false。
	if handler.goalDriverEnabled() {
		t.Fatal("server auto-continuation must default to disabled (zero behavior change)")
	}
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
	handler.goalDriverNoteCLI(sessionID)
	if !state.cliActive(sessionID, time.Now()) {
		t.Fatal("goalDriverNoteCLI must refresh the CLI presence window")
	}
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
