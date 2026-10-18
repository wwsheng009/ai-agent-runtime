package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// Phase 1 goal driver 测试（2026-10-18 事故修复）：
// docs/plan/aicli-goal-driver-unification-plan-20261018.md §4/§5/§6。

// newGoalDriverTestSession 复用 goal 续跑夹具并开启 Phase 1 driver 开关。
func newGoalDriverTestSession(t *testing.T, status runtimegoal.Status) (*ChatSession, func()) {
	t.Helper()
	session, cleanup := newGoalAutoContinueTestSession(t, status)
	session.goalDriverEnabled = true
	return session, cleanup
}

func goalDriverTerminalEvent(session *ChatSession, turnID string, payload map[string]interface{}) runtimeevents.Event {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if _, ok := payload["turn_id"]; !ok {
		payload["turn_id"] = turnID
	}
	return runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: session.RuntimeSession.ID,
		TraceID:   "trace-" + turnID,
		Payload:   payload,
	}
}

// TestGoalDriverDisabledIsNoOp 锁定应急回退语义：driver 关闭（env 回退或测试
// 模拟）时对终态事件零反应（既不发 kick 请求也不冻结），行为与旧版本一致。
func TestGoalDriverDisabledIsNoOp(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "t1", map[string]interface{}{"success": true}))
	if session.goalKickPending.Load() {
		t.Fatal("disabled driver must not request a kick")
	}
	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "t2", map[string]interface{}{"cancel_source": "user_interrupt"}))
	if session.goalContinuationDisarmed.Load() {
		t.Fatal("disabled driver must not freeze on user interrupts")
	}
}

// TestGoalDriverRequestsKickOnRunTerminal 锁定挂载点主路径：任何来源的 run
// 终态都会重新触发一次驱动评估（kick 请求），且同一 turn 去重。
func TestGoalDriverRequestsKickOnRunTerminal(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	event := goalDriverTerminalEvent(session, "t1", map[string]interface{}{"success": true})
	observeGoalDriverTerminal(session, event)
	if !session.goalKickPending.Load() {
		t.Fatal("run terminal must request a driver kick")
	}

	// 模拟主循环消费第一笔后，同一 turn 的重复事件必须被去重（内联评估与
	// 订阅评估共用 turn 键，避免双触发）。
	session.goalKickPending.Store(false)
	observeGoalDriverTerminal(session, event)
	if session.goalKickPending.Load() {
		t.Fatal("duplicate run terminal for the same turn must be de-duplicated")
	}

	// 新 turn 的终态重新触发。
	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "t2", map[string]interface{}{"success": true}))
	if !session.goalKickPending.Load() {
		t.Fatal("a new turn terminal must request a fresh kick")
	}
}

// TestGoalDriverFreezesOnUserInterrupt 锁定中断语义：用户中断（turn 级动作）
// 触发驱动冻结——作废在途 kick + disarm{user_interrupt}，不再静默保持 armed。
func TestGoalDriverFreezesOnUserInterrupt(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.goalKickPending.Store(true) // 模拟在途 kick
	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "i1", map[string]interface{}{"cancel_source": "user_interrupt"}))

	if session.goalKickPending.Load() {
		t.Fatal("user interrupt must void the pending kick")
	}
	if !session.goalContinuationDisarmed.Load() {
		t.Fatal("user interrupt must freeze the continuation gate")
	}
	if got := session.goalContinuationDisarmReasonText(); got != "user_interrupt" {
		t.Fatalf("disarm reason = %q, want user_interrupt", got)
	}
}

// TestGoalDriverDefersWhileDisarmed 冻结期间（恢复态或用户中断），内部轮终态
// 只做显式让位，不得驱动。
func TestGoalDriverDefersWhileDisarmed(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.setGoalContinuationDisarmed("restore")
	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "t1", map[string]interface{}{"success": true}))
	if session.goalKickPending.Load() {
		t.Fatal("a disarmed goal must not be driven by run terminals")
	}
}

// TestGoalDriverSkipsNonInteractiveSessions 非交互路径保持零变化（续跑由既有
// 脚本/headless 语义负责）。
func TestGoalDriverSkipsNonInteractiveSessions(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.NoInteractive = true
	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "t1", map[string]interface{}{"success": true}))
	if session.goalKickPending.Load() {
		t.Fatal("non-interactive sessions keep the legacy path")
	}
}

// TestGoalDisarmPreservesFirstReason 首次 disarmed 原因被保留（restore 早于
// 用户中断时，用户中断不覆盖更早的原因）。
func TestGoalDisarmPreservesFirstReason(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.setGoalContinuationDisarmed("restore")
	session.setGoalContinuationDisarmed("user_interrupt")
	if got := session.goalContinuationDisarmReasonText(); got != "restore" {
		t.Fatalf("disarm reason = %q, want the first reason restore", got)
	}
}

// TestGoalDriverEnabledByDefaultWithEnvKillSwitch 锁定发布形态（D8 裁决）：
// driver 默认启用（正确性修复不以 opt-in 发布）；仅显式 disable 值关闭。
func TestGoalDriverEnabledByDefaultWithEnvKillSwitch(t *testing.T) {
	t.Setenv(goalDriverDisabledEnv, "")
	require.True(t, goalDriverEnabledFromEnv(), "driver must default to enabled")

	t.Setenv(goalDriverDisabledEnv, "1")
	require.False(t, goalDriverEnabledFromEnv(), "1 must disable the driver")

	t.Setenv(goalDriverDisabledEnv, "TRUE")
	require.False(t, goalDriverEnabledFromEnv(), "case-insensitive true must disable the driver")

	t.Setenv(goalDriverDisabledEnv, "0")
	require.True(t, goalDriverEnabledFromEnv(), "non-disable values keep the driver enabled")
} // TestConsumeDropsKickWhileDisarmed 消费门兜底：disarmed 期间 kick 被作废，
// 而不是启动续跑（冻结与消费的竞态窗口保护）。
func TestConsumeDropsKickWhileDisarmed(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.goalKickPending.Store(true)
	session.setGoalContinuationDisarmed("user_interrupt")
	maybeConsumeDeferredGoalKick(session)
	if session.goalKickPending.Load() {
		t.Fatal("a kick must be dropped while the goal is disarmed")
	}
}

// TestRearmRequestsDriverKickWhenEnabled rearm 接力修复：驱动许可重新武装即
// 请求一次驱动检查（不再依赖"后续某轮正常收尾"这一巧合）。
func TestRearmRequestsDriverKickWhenEnabled(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.setGoalContinuationDisarmed("user_interrupt")
	rearmGoalContinuation(session, "user_continue_intent")

	if session.goalContinuationDisarmed.Load() {
		t.Fatal("rearm must clear the gate")
	}
	if !session.goalKickPending.Load() {
		t.Fatal("rearm must request the driver immediately")
	}
	if got := session.goalContinuationDisarmReasonText(); got != "" {
		t.Fatalf("rearm must clear the disarm reason, got %q", got)
	}
}

// TestRearmKeepsLegacyBehaviorWhenDriverDisabled 开关关闭时 rearm 维持旧行为
// （只清闸、不排驱动）——零变化保证。
func TestRearmKeepsLegacyBehaviorWhenDriverDisabled(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	session.setGoalContinuationDisarmed("restore")
	rearmGoalContinuation(session, "user_continue_intent")
	if session.goalContinuationDisarmed.Load() {
		t.Fatal("rearm must clear the gate")
	}
	if session.goalKickPending.Load() {
		t.Fatal("legacy path must not request a kick on rearm")
	}
}

// TestGoalDriverSurvivesInterruptAndSupervisionHandoff 复现 2026-10-18 事故
// 组合场景并锁定修复后的行为：
// 恢复(disarm{restore}) → 显式继续(rearm+请求驱动) → 首轮被用户中断
// (冻结+作废 kick) → supervision 内部轮接管(终态只让位、不驱动) → 显式继续
// 恢复驱动（消费 kick 后真实启动续跑）。全程不存在"armed 但无驱动者"的悬置态。
func TestGoalDriverSurvivesInterruptAndSupervisionHandoff(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 1

	// 1) 恢复态：默认 disarmed{restore}。
	disarmGoalContinuationAfterRestore(session)
	if !session.goalContinuationDisarmed.Load() || session.goalContinuationDisarmReasonText() != "restore" {
		t.Fatal("restore must disarm with reason=restore")
	}

	// 2) 用户显式继续 → rearm 并立即请求驱动（Phase 1 接力修复）。
	rearmGoalContinuation(session, "user_continue_intent")
	if !session.goalKickPending.Load() {
		t.Fatal("rearm must request the driver immediately")
	}

	// 3) "继续"轮被用户中断 → 冻结：在途 kick 作废 + disarm{user_interrupt}。
	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "continue-turn", map[string]interface{}{"cancel_source": "user_interrupt"}))
	if session.goalKickPending.Load() {
		t.Fatal("interrupt must void the pending kick")
	}
	if !session.goalContinuationDisarmed.Load() || session.goalContinuationDisarmReasonText() != "user_interrupt" {
		t.Fatal("interrupt must freeze the gate with reason=user_interrupt")
	}

	// 4) supervision 内部轮连续终态：冻结期间只让位、不驱动——修复前这些轮
	//    不经过任何评估点，goal 静默悬置。
	for _, turn := range []string{"preflight-1", "preflight-2"} {
		observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, turn, map[string]interface{}{"success": true}))
	}
	if session.goalKickPending.Load() {
		t.Fatal("frozen goal must not be driven by internal run terminals")
	}

	// 5) 用户再次显式继续 → 驱动恢复：kick 请求 + 消费后真实启动续跑。
	executor := &fakeChatExecutor{output: "resumed after explicit continue"}
	session.ChatExecutor = executor
	rearmGoalContinuation(session, "user_continue_intent")
	if !session.goalKickPending.Load() {
		t.Fatal("second explicit continue must re-request the driver")
	}
	maybeConsumeDeferredGoalKick(session)
	if session.goalKickPending.Load() {
		t.Fatal("kick must be consumed")
	}
	if executor.continuations != 1 {
		t.Fatalf("expected one continuation after recovery, got %d", executor.continuations)
	}
}

// TestGoalDriverTerminalDrivesNonForegroundContinuation 挂载点的核心价值：非
// 前台轮（supervision wake / actor 内部提交）的终态也能请求并消费驱动。
func TestGoalDriverTerminalDrivesNonForegroundContinuation(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 1

	executor := &fakeChatExecutor{output: "driven by run terminal"}
	session.ChatExecutor = executor

	observeGoalDriverTerminal(session, goalDriverTerminalEvent(session, "wake-turn-1", map[string]interface{}{"success": true}))
	if !session.goalKickPending.Load() {
		t.Fatal("run terminal must request a driver kick")
	}
	maybeConsumeDeferredGoalKick(session)
	if executor.continuations != 1 {
		t.Fatalf("expected the driver to run the continuation, got %d", executor.continuations)
	}
}

// TestWireLocalGoalDriverSubscribesRunTerminals 订阅端到端：同会话总线事件到达
// observe 入口并触发驱动请求；foreign session 的事件被过滤。
func TestWireLocalGoalDriverSubscribesRunTerminals(t *testing.T) {
	session, cleanup := newGoalDriverTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	bus := runtimeevents.NewBus()
	host := &localChatRuntimeHost{EventBus: bus, BaseSession: session}
	session.LocalRuntimeHost = host
	host.wireLocalGoalDriver()

	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "session-somewhere-else",
		TraceID:   "trace-foreign",
		Payload:   map[string]interface{}{"turn_id": "foreign-turn", "success": true},
	})
	require.False(t, session.goalKickPending.Load(), "foreign session events must be ignored")

	bus.Publish(goalDriverTerminalEvent(session, "sess-terminal-1", map[string]interface{}{"success": true}))
	require.Eventually(t, func() bool { return session.goalKickPending.Load() },
		time.Second, 10*time.Millisecond, "same-session run terminal must request a kick via the subscription")
}

// TestWireLocalGoalDriverNoopWhenDisabled 订阅侧零变化：开关关闭时不订阅。
func TestWireLocalGoalDriverNoopWhenDisabled(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	bus := runtimeevents.NewBus()
	host := &localChatRuntimeHost{EventBus: bus, BaseSession: session}
	session.LocalRuntimeHost = host
	host.wireLocalGoalDriver()

	bus.Publish(goalDriverTerminalEvent(session, "sess-terminal-1", map[string]interface{}{"success": true}))
	time.Sleep(50 * time.Millisecond)
	require.False(t, session.goalKickPending.Load(), "disabled driver must not subscribe")
}
