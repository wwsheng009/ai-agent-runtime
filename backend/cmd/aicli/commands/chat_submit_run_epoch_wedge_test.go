package commands

// P0-1/P0-3 回归：提交-运行 epoch 撕裂（"幽灵 Analyzing"，见
// docs/plan/aicli-chat-submit-run-epoch-wedge-hardening.md）的防线。
//
// 事故形态：提交路径把 UI 置为等待态（Analyzing），但 run 协议从未 BeginRun
// （epoch 恒为 0），epoch 围栏随后静默丢弃一切事件 → 状态永久假忙、输入丢失。
// 本文件的断言把三条防线钉死：
//  1. 只有进程内 actor executor 允许把等待态推迟到 BeginRun 之后；
//  2. 预跑阶段失败不得留下被置位的等待态（sendMessage 级回归）；
//  3. 围栏拒绝必须进入可观测计数，/debug/chat/status 必须报告撕裂。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestChatExecutorArmsWaitingAfterBeginRunClassification(t *testing.T) {
	if !chatExecutorArmsWaitingAfterBeginRun(newAICLIActorChatExecutor()) {
		t.Fatal("in-process actor executor must own waiting arming after BeginRun")
	}
	if chatExecutorArmsWaitingAfterBeginRun(newAICLISharedChatExecutor()) {
		t.Fatal("shared executor must keep submit-side waiting arming")
	}
	if chatExecutorArmsWaitingAfterBeginRun(&fakeChatExecutor{}) {
		t.Fatal("actor-like test double must keep submit-side waiting arming")
	}
	if chatSubmitArmsWaitingAtEntry(nil, newAICLIActorChatExecutor()) {
		t.Fatal("nil session must never arm waiting at submit entry")
	}
}

// TestSendMessageActorPathDoesNotArmWaitingBeforeBeginRun 是事故形态的直接回归：
// actor 路径在 run 真正开启前失败（此处为未配置 local runtime host），
// sendMessage 返回错误后 UI 不得残留等待态。
func TestSendMessageActorPathDoesNotArmWaitingBeforeBeginRun(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		ChatExecutor:   newAICLIActorChatExecutor(),
		cancelCtx:      context.Background(),
		RuntimeSession: &runtimechat.Session{ID: "session-submit-gate"},
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	session.Surface = surface

	captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.SetSurface(surface)
		coord.PrintPrompt()
		coord.SetPromptInput("hello")
		coord.ResetPromptState()
		renderSubmittedUserInputEcho(session, "hello")

		if _, err := sendMessage(session, "hello"); err == nil {
			t.Fatal("expected pre-BeginRun failure without a local runtime host")
		}
	})
	if coord.WaitingArmed() {
		t.Fatal("a pre-BeginRun failure must not leave the waiting/Analyzing state armed")
	}
	if bridge := session.RuntimeEventBridge; bridge != nil && bridge.RunActive() {
		t.Fatal("no run may be reported active after a pre-BeginRun failure")
	}
}

// TestActorExecutorPreRunFailureKeepsBridgeIdle 锁定 executor 侧的契约：
// 在 BeginRun 之前失败时不置位等待态、也不制造 run epoch。
func TestActorExecutorPreRunFailureKeepsBridgeIdle(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-pre-run-failure"}}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	if _, err := newAICLIActorChatExecutor().Execute(context.Background(), session, "hello"); err == nil {
		t.Fatal("expected pre-run failure without a local runtime host")
	}
	if coord.WaitingArmed() {
		t.Fatal("pre-BeginRun failure must not arm the waiting/Analyzing state")
	}
	if bridge := session.RuntimeEventBridge; bridge != nil {
		if bridge.RunEpoch() != 0 || bridge.RunActive() {
			t.Fatalf("run state = (epoch=%d active=%v), want (0, false)", bridge.RunEpoch(), bridge.RunActive())
		}
	}
}

// TestBridgeLateRuntimeDropStatsRecorded 锁定 P0-3：围栏拒绝进入计数。
func TestBridgeLateRuntimeDropStatsRecorded(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "late-drop"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.logLateRuntimeEvent(runtimeevents.Event{
		Type:      chatWebDynamicStatusBusEvent,
		SessionID: "late-drop",
	}, chatRuntimeLateReasonClosedRunEpoch)

	total, closed, lastType, lastReason, lastAt := bridge.LateRuntimeDropStats()
	if total != 1 || closed != 1 {
		t.Fatalf("late drop counters = (total=%d closed=%d), want (1, 1)", total, closed)
	}
	if lastType != chatWebDynamicStatusBusEvent || lastReason != chatRuntimeLateReasonClosedRunEpoch {
		t.Fatalf("last late drop = (%q, %q)", lastType, lastReason)
	}
	if lastAt.IsZero() {
		t.Fatal("last late drop timestamp must be recorded")
	}
	if idleNoRun, closedAfterRun, activeMismatch := bridge.LateRuntimeDropBreakdown(); idleNoRun != 1 || closedAfterRun != 0 || activeMismatch != 0 {
		t.Fatalf("fresh-bridge drop breakdown = (idle=%d closed=%d active-mismatch=%d), want (1, 0, 0)",
			idleNoRun, closedAfterRun, activeMismatch)
	}
	if state := bridge.RunState(); state != chatRunStateIdle {
		t.Fatalf("fresh bridge run state = %q, want %q", state, chatRunStateIdle)
	}
	if bridge.RunEpoch() != 0 || bridge.RunActive() {
		t.Fatalf("fresh bridge run state = (epoch=%d active=%v), want (0, false)", bridge.RunEpoch(), bridge.RunActive())
	}
}

// TestDebugReportsWedgeSuspected 锁定 /debug/chat/status#turn 的撕裂指纹：
// 等待态在无 run epoch 时被置位必须显式报告 Wedge Suspected。
func TestDebugReportsWedgeSuspected(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "wedge-debug"}}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	coord.StartWaiting()
	plain := renderDocPlainText(buildChatDebugDisplayDocument(session))
	for _, marker := range []string{
		"Run Epoch:",
		"Run State:",
		"Run Active:",
		"Waiting Armed:",
		"Waiting For:",
		"Late Action Drops:",
		"Wedge Suspected:",
	} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("debug turn 区块缺少撕裂检测锚点 %q\n---\n%s", marker, plain)
		}
	}
}

// TestWaitingArmedSinceTracksLifecycle 锁定 P1-2 的等待态时钟：
// 重复置位不得刷新起始时间，清态必须复位。
func TestWaitingArmedSinceTracksLifecycle(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "waiting-since"}}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	if armed, _ := coord.WaitingArmedSince(); armed {
		t.Fatal("fresh coordinator must not report an armed waiting state")
	}
	coord.StartWaiting()
	armed, since := coord.WaitingArmedSince()
	if !armed || since.IsZero() {
		t.Fatalf("StartWaiting must record the waiting clock (armed=%v since=%v)", armed, since)
	}
	coord.StartWaiting()
	if _, again := coord.WaitingArmedSince(); !again.Equal(since) {
		t.Fatalf("re-arming must keep the original waiting clock: %v -> %v", since, again)
	}
	coord.ClearWaiting()
	if armed, since := coord.WaitingArmedSince(); armed || !since.IsZero() {
		t.Fatalf("ClearWaiting must reset the waiting clock (armed=%v since=%v)", armed, since)
	}
}

// TestChatActorBuildContextBoundsBootstrap 锁定 P0-2：actor 构建预算必须真的
// 生效（超时返回 DeadlineExceeded），否则提交仍可能静默卡在构建阶段。
func TestChatActorBuildContextBoundsBootstrap(t *testing.T) {
	old := chatActorBuildBudget
	chatActorBuildBudget = 25 * time.Millisecond
	t.Cleanup(func() { chatActorBuildBudget = old })

	ctx, cancel := chatActorBuildContext(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("build ctx err = %v, want DeadlineExceeded", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("actor build context was not bounded by chatActorBuildBudget")
	}
}

// TestChatActorBuildContextDisabledReturnsParent 锁定关闭语义：预算为 0 时
// 必须原样返回父 ctx（不引入额外取消源）。
func TestChatActorBuildContextDisabledReturnsParent(t *testing.T) {
	old := chatActorBuildBudget
	chatActorBuildBudget = 0
	t.Cleanup(func() { chatActorBuildBudget = old })

	parent := context.Background()
	ctx, cancel := chatActorBuildContext(parent)
	defer cancel()
	if ctx != parent {
		t.Fatal("disabled budget must return the parent context unchanged")
	}
}

// TestChatActorForSessionBoundedPassesThroughNonTimeoutError 保证预算层不吞掉
// 可归因的构建错误（只把超时翻译成可读文案）。
func TestChatActorForSessionBoundedPassesThroughNonTimeoutError(t *testing.T) {
	old := chatActorBuildBudget
	chatActorBuildBudget = time.Second
	t.Cleanup(func() { chatActorBuildBudget = old })

	_, err := chatActorForSessionBounded(context.Background(), &ChatSession{})
	if err == nil || !strings.Contains(err.Error(), "local runtime host is not configured") {
		t.Fatalf("expected the original build error to pass through, got %v", err)
	}
}

// TestWaitingDetachedFromRunPredicate 直接锁定看门狗判据：只有 actor 协议 +
// 已存在事件桥 + run epoch 为 0 且无活动 run 才算"等待态脱离 run"。
func TestWaitingDetachedFromRunPredicate(t *testing.T) {
	if chatWaitingDetachedFromRun(nil) {
		t.Fatal("nil session must not be reported as detached")
	}
	legacy := &ChatSession{ChatExecutor: newAICLISharedChatExecutor()}
	legacy.RuntimeEventBridge = newChatRuntimeEventBridge(legacy)
	if chatWaitingDetachedFromRun(legacy) {
		t.Fatal("non-actor executor must never be reported as detached")
	}
	actor := &ChatSession{ChatExecutor: newAICLIActorChatExecutor()}
	if chatWaitingDetachedFromRun(actor) {
		t.Fatal("actor session without a bridge must not be reported as detached")
	}
	actor.RuntimeEventBridge = newChatRuntimeEventBridge(actor)
	if !chatWaitingDetachedFromRun(actor) {
		t.Fatal("actor session with a fresh (epoch 0) bridge is detached")
	}
	actor.RuntimeEventBridge.BeginRun()
	defer actor.RuntimeEventBridge.EndRun()
	if chatWaitingDetachedFromRun(actor) {
		t.Fatal("actor session with an open run epoch must not be reported as detached")
	}
}

// TestWaitingWatchdogClearsDetachedWaiting 锁定 P1-2 自愈：actor 协议下等待态
// 超过预算仍未开启任何 run epoch（事故形态）时必须自动清态并留下可见提示。
func TestWaitingWatchdogClearsDetachedWaiting(t *testing.T) {
	old := chatWaitingWithoutRunWatchdogBudget
	chatWaitingWithoutRunWatchdogBudget = 20 * time.Millisecond
	t.Cleanup(func() { chatWaitingWithoutRunWatchdogBudget = old })

	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "watchdog-detached"},
		ChatExecutor:   newAICLIActorChatExecutor(),
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	coord.StartWaiting()
	if armed, _ := coord.WaitingArmedSince(); !armed {
		t.Fatal("sanity: StartWaiting must arm the waiting state")
	}
	deadline := time.Now().Add(3 * time.Second)
	cleared := false
	for time.Now().Before(deadline) {
		if armed, _ := coord.WaitingArmedSince(); !armed {
			cleared = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cleared {
		t.Fatal("watchdog must clear a waiting state that never opened a run epoch")
	}
	// 清态与提示发布在同一回调里先后完成：这里按有界窗口等待提示落地，避免
	// 把"清态先可见"误判成"没有可见提示"。
	noticeDeadline := time.Now().Add(time.Second)
	for time.Now().Before(noticeDeadline) {
		coord.mu.Lock()
		notice := coord.diagnosticNotice
		coord.mu.Unlock()
		if notice != "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("watchdog must publish a visible notice when it clears a wedged waiting state")
}

// TestWaitingWatchdogKeepsWaitingWithOpenRun 保证看门狗不误伤：run epoch 已开启
// 的等待态（正常 in-flight 回合）不得被清理。
func TestWaitingWatchdogKeepsWaitingWithOpenRun(t *testing.T) {
	old := chatWaitingWithoutRunWatchdogBudget
	chatWaitingWithoutRunWatchdogBudget = 20 * time.Millisecond
	t.Cleanup(func() { chatWaitingWithoutRunWatchdogBudget = old })

	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "watchdog-running"},
		ChatExecutor:   newAICLIActorChatExecutor(),
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.BeginRun()
	t.Cleanup(bridge.EndRun)
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	coord.StartWaiting()
	time.Sleep(5 * chatWaitingWithoutRunWatchdogBudget)
	if armed, _ := coord.WaitingArmedSince(); !armed {
		t.Fatal("watchdog must not clear a waiting state backed by an open run epoch")
	}
}

// TestWaitingWatchdogSkipsNonActorExecutor 保证 legacy/shared 路径不被误清：
// 它们的等待态本就不依赖 run epoch。
func TestWaitingWatchdogSkipsNonActorExecutor(t *testing.T) {
	old := chatWaitingWithoutRunWatchdogBudget
	chatWaitingWithoutRunWatchdogBudget = 20 * time.Millisecond
	t.Cleanup(func() { chatWaitingWithoutRunWatchdogBudget = old })

	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "watchdog-legacy"},
		ChatExecutor:   newAICLISharedChatExecutor(),
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	coord.StartWaiting()
	time.Sleep(5 * chatWaitingWithoutRunWatchdogBudget)
	if armed, _ := coord.WaitingArmedSince(); !armed {
		t.Fatal("watchdog must not clear waiting states on non-actor executors")
	}
}

// TestChatRuntimeEventBridgeRunStateLifecycle 锁定 P1-1：显式 run 状态必须与
// BeginRun/EndRun 一一对应，且"从未 BeginRun 的防御性 EndRun"不得把 idle 误标
// 成 closed（这正是 epoch 0 双关需要被取代的前提）。
func TestChatRuntimeEventBridgeRunStateLifecycle(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "run-state"}})
	if state := bridge.RunState(); state != chatRunStateIdle {
		t.Fatalf("fresh bridge run state = %q, want idle", state)
	}
	bridge.EndRun() // 防御性调用：从未 BeginRun
	if state := bridge.RunState(); state != chatRunStateIdle {
		t.Fatalf("defensive EndRun must keep idle, got %q", state)
	}
	bridge.BeginRun()
	if state := bridge.RunState(); state != chatRunStateRunning {
		t.Fatalf("state after BeginRun = %q, want running", state)
	}
	bridge.EndRun()
	if state := bridge.RunState(); state != chatRunStateClosed {
		t.Fatalf("state after EndRun = %q, want closed", state)
	}
	bridge.BeginRun()
	if state := bridge.RunState(); state != chatRunStateRunning {
		t.Fatalf("state after re-BeginRun = %q, want running", state)
	}
	bridge.EndRun()
}

// TestLateRuntimeDropBreakdownClassifiesRunState 锁定 P1-1 的归因细分：同样一条
// "closed run epoch" 拒绝，在 idle / closed / running 三种状态下落进不同桶——
// 修复前它们都只记作 closed-epoch。
func TestLateRuntimeDropBreakdownClassifiesRunState(t *testing.T) {
	event := runtimeevents.Event{Type: chatWebDynamicStatusBusEvent, SessionID: "late-drop-class"}

	idleBridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "late-drop-class"}})
	idleBridge.logLateRuntimeEvent(event, chatRuntimeLateReasonClosedRunEpoch)
	if idle, closed, active := idleBridge.LateRuntimeDropBreakdown(); idle != 1 || closed != 0 || active != 0 {
		t.Fatalf("idle breakdown = (idle=%d closed=%d active-mismatch=%d), want (1, 0, 0)", idle, closed, active)
	}

	closedBridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "late-drop-class"}})
	closedBridge.BeginRun()
	closedBridge.EndRun()
	closedBridge.logLateRuntimeEvent(event, chatRuntimeLateReasonClosedRunEpoch)
	if idle, closed, active := closedBridge.LateRuntimeDropBreakdown(); idle != 0 || closed != 1 || active != 0 {
		t.Fatalf("closed breakdown = (idle=%d closed=%d active-mismatch=%d), want (0, 1, 0)", idle, closed, active)
	}

	activeBridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "late-drop-class"}})
	activeBridge.BeginRun()
	activeBridge.logLateRuntimeEvent(event, chatRuntimeLateReasonClosedRunEpoch)
	if idle, closed, active := activeBridge.LateRuntimeDropBreakdown(); idle != 0 || closed != 0 || active != 1 {
		t.Fatalf("active breakdown = (idle=%d closed=%d active-mismatch=%d), want (0, 0, 1)", idle, closed, active)
	}
	activeBridge.EndRun()
}

// TestBridgePublishesRenderFenceDropsAtEndRun 锁定 P1-1b 生产侧：EndRun 把本轮
// 新增的 late action 拒绝计数（增量）上报到 EventBus，供 usageanalytics 落进
// usage_render_fence_drops；无增量时不得重复上报。
func TestBridgePublishesRenderFenceDropsAtEndRun(t *testing.T) {
	bus := runtimeevents.NewBus()
	var published []runtimeevents.Event
	unsubscribe := bus.SubscribeCancelable(runtimeevents.EventRenderFenceDropped, func(event runtimeevents.Event) {
		published = append(published, event)
	})
	defer unsubscribe()

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "fence-publish"},
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
	}
	bridge := newChatRuntimeEventBridge(session)

	drop := runtimeevents.Event{Type: chatWebDynamicStatusBusEvent, SessionID: "fence-publish"}
	bridge.logLateRuntimeEvent(drop, chatRuntimeLateReasonClosedRunEpoch)
	bridge.logLateRuntimeEvent(drop, chatRuntimeLateReasonClosedRunEpoch)
	bridge.BeginRun()
	bridge.EndRun()

	if len(published) != 1 {
		t.Fatalf("published fence-drop events = %d, want 1", len(published))
	}
	event := published[0]
	if event.SessionID != "fence-publish" {
		t.Fatalf("event session = %q, want fence-publish", event.SessionID)
	}
	if idle, _ := event.Payload["idle"].(int); idle != 2 {
		t.Fatalf("published idle = %v, want 2", event.Payload["idle"])
	}
	if closed, _ := event.Payload["closed"].(int); closed != 0 {
		t.Fatalf("published closed = %v, want 0", event.Payload["closed"])
	}

	bridge.BeginRun()
	bridge.EndRun()
	if len(published) != 1 {
		t.Fatalf("increment-free run republished counts: %d events", len(published))
	}
}
