package commands

import (
	"os"
	"strings"
	"testing"
)

// unsetEnvForTest 模拟「环境变量未设置」（t.Setenv 只能设值），测试结束恢复原值。
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
			return
		}
		_ = os.Unsetenv(key)
	})
}

// D14：总闸默认启用；只有显式 opt-out 才回退旧行为。
func TestChatBusyCommandPolicyGateDefaultOn(t *testing.T) {
	unsetEnvForTest(t, chatBusyCommandEnv)
	if !chatBusyCommandEnabled() {
		t.Fatalf("%s 未设置时必须默认启用（2026-09-28 决策 D14）", chatBusyCommandEnv)
	}
	for _, value := range []string{"", "1", "true", "on", "yes", "enabled", " TRUE ", "auto"} {
		t.Setenv(chatBusyCommandEnv, value)
		if !chatBusyCommandEnabled() {
			t.Fatalf("%s=%q 应视为启用", chatBusyCommandEnv, value)
		}
	}
	for _, value := range []string{"0", "false", "off", "no", "disabled", "garbage"} {
		t.Setenv(chatBusyCommandEnv, value)
		if chatBusyCommandEnabled() {
			t.Fatalf("%s=%q 应视为显式关闭", chatBusyCommandEnv, value)
		}
	}
}

// D14：默认（无任何环境变量）注册表即接管路由——首批 S/P 档无需配置即可达。
func TestChatBusyCommandPolicyDefaultOnRegistryRouting(t *testing.T) {
	unsetEnvForTest(t, chatBusyCommandEnv)
	unsetEnvForTest(t, runtimeInteractionEnv)

	cases := map[string]chatBusyCommandPolicy{
		"/todos":        chatBusyPolicyScreen,    // 首批 S 档（副屏）
		"/history":      chatBusyPolicyScreen,    // 首批 S 档（副屏）
		"/queue clear":  chatBusyPolicyScreen,    // 首批 prompt 档（确认门）
		"/model status": chatBusyPolicyScreen,    // 批次 3：只读变体迁副屏并纳入忙时白名单
		"/theme":        chatBusyPolicyImmediate, // bare 仍是短内联（inline + read）
		"/model":        chatBusyPolicyDeferred,  // screen 非首批 → 回合后执行
		"/clear":        chatBusyPolicyDeferred,  // queue + session
		"/exit":         chatBusyPolicyReject,    // block 不可放宽
		"/nope":         chatBusyPolicyDeferred,  // 未登记 → queue（INV-6）
	}
	for line, want := range cases {
		if got := chatSlashCommandBusyPolicyFor(line); got != want {
			t.Fatalf("默认启用下 %q 策略为 %s，期望 %s", line, got, want)
		}
	}
}

func TestChatBusyCommandPolicyFirstBatchImmediateWhenEnabled(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	immediate := []string{"/session", "/queue", "/queue status"}
	for _, text := range immediate {
		if got := chatSlashCommandBusyPolicyFor(text); got != chatBusyPolicyImmediate {
			t.Fatalf("%q 应为 immediate，实际 %s", text, got)
		}
	}

	cases := map[string]chatBusyCommandPolicy{
		// 批次 3/4：/help、/status 迁入只读 ScreenDocument 并进入忙时副屏白名单，
		// 忙时由 immediate（主屏内联）改为 screen（副屏通道）。
		"/help":         chatBusyPolicyScreen,
		"/?":            chatBusyPolicyScreen,
		"/status":       chatBusyPolicyScreen,
		"/queue clear":  chatBusyPolicyScreen,    // P2-4b-3：prompt 首批 → 确认门
		"/status extra": chatBusyPolicyDeferred,  // 未登记子命令 → 排队（INV-6）
		"/model":        chatBusyPolicyDeferred,  // screen 非首批 → 回合后执行
		"/theme":        chatBusyPolicyImmediate, // 注册表 inline + read
		"/nope":         chatBusyPolicyDeferred,  // 未登记 → 排队（INV-6）
		"":              chatBusyPolicyInherit,
		"hello":         chatBusyPolicyInherit,
	}
	for text, want := range cases {
		if got := chatSlashCommandBusyPolicyFor(text); got != want {
			t.Fatalf("%q 应为 %s，实际 %s", text, want, got)
		}
	}
}

// T18：总闸显式关闭时，resolver 决策必须与既有 chatSlashCommandQueueSafe 白名单逐条等价。
func TestChatBusyCommandPolicyGateOffMatchesLegacyWhitelist(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "off")

	for _, spec := range chatSlashCommandCatalog() {
		for _, name := range spec.allNames() {
			got := chatSlashCommandBusyPolicyFor(name)
			if got == chatBusyPolicyImmediate || got == chatBusyPolicyScreen {
				t.Fatalf("显式关闭总闸后 %q 不得返回 %s", name, got)
			}
			wantLegacySafe := chatSlashCommandQueueSafe(name)
			if (got == chatBusyPolicyDeferred) != wantLegacySafe {
				t.Fatalf("显式关闭总闸后 %q 决策 %s 与旧白名单(queueSafe=%v)不一致", name, got, wantLegacySafe)
			}
		}
	}
}

func TestChatBusyCommandPolicySubcommandQueueClear(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	if got := chatSlashCommandBusyPolicyFor("/queue clear"); got != chatBusyPolicyScreen {
		t.Fatalf("/queue clear 应走 prompt 确认门（screen 路由），实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/QUEUE CLEAR now"); got != chatBusyPolicyDeferred {
		t.Fatalf("/queue clear 附带额外参数不在首批白名单，应为 deferred，实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/queue status"); got != chatBusyPolicyImmediate {
		t.Fatalf("/queue status 应为 immediate，实际 %s", got)
	}
}

func TestChatBusyCommandPolicyArgsGuardAndAliases(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	// 不接受参数的命令带参数时 fail-closed（不得 immediate）。
	for _, text := range []string{"/help extra", "/status extra", "/session extra"} {
		if got := chatSlashCommandBusyPolicyFor(text); got == chatBusyPolicyImmediate {
			t.Fatalf("%q 带非法参数不得 immediate", text)
		}
	}
	// 别名归一：/h 是 /history（注册表 S 档首批），/? 是 /help（只读副屏，
	// 批次 3 起与 /help 同档）。
	if got := chatSlashCommandBusyPolicyFor("/h"); got != chatBusyPolicyScreen {
		t.Fatalf("/h 别名应归一到 /history 的 S 档，实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/?"); got != chatBusyPolicyScreen {
		t.Fatalf("/? 别名应归一到 /help 的只读副屏档，实际 %s", got)
	}
}

func TestChatBusyCommandPolicyString(t *testing.T) {
	cases := map[chatBusyCommandPolicy]string{
		chatBusyPolicyInherit:   "inherit",
		chatBusyPolicyImmediate: "immediate",
		chatBusyPolicyScreen:    "screen",
		chatBusyPolicyDeferred:  "deferred",
		chatBusyPolicyReject:    "reject",
	}
	for policy, want := range cases {
		if got := policy.String(); !strings.EqualFold(got, want) {
			t.Fatalf("%d String()=%q，期望 %q", policy, got, want)
		}
	}
}
