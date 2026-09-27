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
