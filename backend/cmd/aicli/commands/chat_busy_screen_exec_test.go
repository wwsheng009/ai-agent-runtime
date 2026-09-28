package commands

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// P2-4b：S 档首批白名单 + 副屏执行原语（modal 登记 / 租约预算 / 恢复）。

// withChatBusyScreenTestHooks 注入副屏能力与执行替身，避免单测依赖真实 TTY。
func withChatBusyScreenTestHooks(t *testing.T, capable bool, dispatch func(*ChatSession, string) bool) {
	t.Helper()
	prevCapability, prevDispatch := chatBusyScreenCapability, chatBusyScreenDispatchOverride
	chatBusyScreenCapability = func(*ChatSession) bool { return capable }
	chatBusyScreenDispatchOverride = dispatch
	t.Cleanup(func() {
		chatBusyScreenCapability, chatBusyScreenDispatchOverride = prevCapability, prevDispatch
	})
}

func TestChatBusyPolicyScreenFirstBatch(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")

	cases := map[string]chatBusyCommandPolicy{
		"/todos":         chatBusyPolicyScreen,
		"/todos active":  chatBusyPolicyScreen,
		"/history":       chatBusyPolicyScreen,
		"/usage":         chatBusyPolicyScreen,
		"/debug display": chatBusyPolicyScreen,
		"/web endpoints": chatBusyPolicyScreen,
		"/debug status":  chatBusyPolicyImmediate,
		"/skills":        chatBusyPolicyDeferred, // screen+read 但非首批
		"/theme on":      chatBusyPolicyDeferred, // screen+live（写入类，非首批）
		"/export":        chatBusyPolicyDeferred,
	}
	for line, want := range cases {
		if got := chatSlashCommandBusyPolicyFor(line); got != want {
			t.Fatalf("%q 策略为 %s，期望 %s", line, got, want)
		}
	}

	// 未显式启用 P1 通道时，S 档必须回退 D（与既有灰度契约一致）。
	t.Setenv(chatBusyCommandEnv, "off")
	if got := chatSlashCommandBusyPolicyFor("/todos"); got != chatBusyPolicyDeferred {
		t.Fatalf("关闭忙时通道后 S 档应回退 D，实际 %s", got)
	}
}

func TestRuntimeCommandHostRunsWhitelistedScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	session.Surface = surface
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	var (
		dispatched   bool
		insideBudget time.Duration
	)
	withChatBusyScreenTestHooks(t, true, func(s *ChatSession, line string) bool {
		dispatched = true
		if line != "/todos" {
			t.Errorf("dispatch 收到 %q，期望 /todos", line)
		}
		if snap, ok := chatInputArbitrationSnapshotOf(s); !ok || snap.Owner != chatInputOwnerModal {
			t.Errorf("副屏执行期间仲裁属主应为 modal，实际 ok=%v snap=%#v", ok, snap)
		}
		if s.Surface != nil {
			insideBudget = s.Surface.AlternateScreenWaitBudget()
		}
		return true
	})

	if !runtimeCommandHostFor(session).SubmitBusy("/todos") {
		t.Fatal("/todos（首批 S 档）应在宿主内执行")
	}
	if !dispatched {
		t.Fatal("副屏执行入口未被调用")
	}
	if insideBudget != chatBusyScreenWaitBudget {
		t.Fatalf("执行期间租约预算 = %s，期望 %s", insideBudget, chatBusyScreenWaitBudget)
	}
	if got := surface.AlternateScreenWaitBudget(); got != 0 {
		t.Fatalf("执行后租约预算未复位：%s", got)
	}
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("执行后 modal 登记必须已释放")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("执行后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()

	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	if events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("审计内容不符：%+v", events[0].Payload)
	}
}

func TestRuntimeCommandHostDegradesNonWhitelistedScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)

	dispatched := false
	withChatBusyScreenTestHooks(t, true, func(*ChatSession, string) bool {
		dispatched = true
		return true
	})
	if runtimeCommandHostFor(session).SubmitBusy("/skills") {
		t.Fatal("非首批 screen 命令（/skills）必须降级入队")
	}
	if dispatched {
		t.Fatal("非首批 screen 命令不得进入副屏执行入口")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "degraded" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("降级审计内容不符：%+v", events)
	}
}

// 能力门 fail-closed：未注入替身时（无 surface/无 TTY）必须降级，
// 既有 T6 行为不变。
func TestRuntimeCommandHostScreenCapabilityFailClosed(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, _ := newRuntimeHostTestSession(t)

	if runtimeCommandHostFor(session).SubmitBusy("/todos") {
		t.Fatal("无副屏能力时必须降级入队")
	}
	if chatBusyScreenCapability(session) {
		t.Fatal("无 surface 的会话不应通过副屏能力门")
	}
}
