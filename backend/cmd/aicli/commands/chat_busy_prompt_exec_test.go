package commands

import (
	"strings"
	"testing"
)

// ============================================================================
// P2-4b-3：prompt 档忙时确认门
// ============================================================================

func withChatBusyPromptTestHook(t *testing.T, hook func(*ChatSession, string, []string) (bool, bool)) {
	t.Helper()
	prev, prevAvailable := chatBusyPromptConfirmOverride, chatBusyPromptChannelAvailable
	chatBusyPromptConfirmOverride = hook
	chatBusyPromptChannelAvailable = func(session *ChatSession) bool { return session != nil && session.Interaction != nil }
	t.Cleanup(func() {
		chatBusyPromptConfirmOverride = prev
		chatBusyPromptChannelAvailable = prevAvailable
	})
}

func TestChatBusyPolicyPromptFirstBatch(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")

	cases := map[string]chatBusyCommandPolicy{
		"/queue clear":   chatBusyPolicyScreen,    // 首批 prompt → 宿主确认门
		"/queue   clear": chatBusyPolicyScreen,    // 空白归一后同一条
		"/queue":         chatBusyPolicyImmediate, // P1 catalog 首批（状态查询）
		"/normal":        chatBusyPolicyDeferred,
	}
	for line, want := range cases {
		if got := chatSlashCommandBusyPolicyFor(line); got != want {
			t.Fatalf("%q 策略为 %s，期望 %s", line, got, want)
		}
	}
}

func TestRuntimeCommandHostPromptConfirmExecutes(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	var asked string
	withChatBusyPromptTestHook(t, func(_ *ChatSession, question string, _ []string) (bool, bool) {
		asked = question
		return true, true
	})
	dispatched := false
	withChatBusyScreenTestHooks(t, true, func(_ *ChatSession, line string) bool {
		dispatched = true
		if line != "/queue clear" {
			t.Errorf("dispatch 收到 %q", line)
		}
		return true
	})

	if !runtimeCommandHostFor(session).SubmitBusy("/queue clear") {
		t.Fatal("/queue clear（首批 prompt）应在确认后被消费")
	}
	if !dispatched {
		t.Fatal("确认后必须进入执行入口")
	}
	if !strings.Contains(asked, "/queue clear") {
		t.Fatalf("确认提问缺少命令：%q", asked)
	}
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("确认门退出后 modal 登记必须已释放")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("确认门退出后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "prompt" {
		t.Fatalf("审计内容不符：%+v", events)
	}
}

func TestRuntimeCommandHostPromptDeclineConsumesWithoutExecuting(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	withChatBusyPromptTestHook(t, func(*ChatSession, string, []string) (bool, bool) { return false, true })
	dispatched := false
	withChatBusyScreenTestHooks(t, true, func(*ChatSession, string) bool {
		dispatched = true
		return true
	})

	if !runtimeCommandHostFor(session).SubmitBusy("/queue clear") {
		t.Fatal("显式拒绝也必须消费该行（不得回退入队反复弹确认）")
	}
	if dispatched {
		t.Fatal("拒绝后不得执行命令")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "rejected" || events[0].Payload["mode"] != "prompt" {
		t.Fatalf("拒绝审计内容不符：%+v", events)
	}
	// 拒绝提示必须到达渲染面（产物含取消文案即可，具体通道由交互宿主决定）。
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("拒绝路径也必须释放 modal 登记")
	}
}

func TestRuntimeCommandHostPromptUnavailableDegrades(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)

	// answered=false 表示通道不可用/被中断：必须降级入队，不消费。
	withChatBusyPromptTestHook(t, func(*ChatSession, string, []string) (bool, bool) { return false, false })
	withChatBusyScreenTestHooks(t, true, func(*ChatSession, string) bool { return true })

	if runtimeCommandHostFor(session).SubmitBusy("/queue clear") {
		t.Fatal("确认通道不可用时必须降级（回退入队）")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "degraded" {
		t.Fatalf("降级审计内容不符：%+v", events)
	}
}

func TestRuntimeCommandHostDegradesNonWhitelistedPrompt(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)

	withChatBusyPromptTestHook(t, func(*ChatSession, string, []string) (bool, bool) {
		t.Fatal("非首批 prompt 命令不得进入确认门")
		return false, false
	})
	if runtimeCommandHostFor(session).SubmitBusy("/normal") {
		t.Fatal("非首批 prompt 命令必须降级")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "degraded" {
		t.Fatalf("降级审计内容不符：%+v", events)
	}
}
