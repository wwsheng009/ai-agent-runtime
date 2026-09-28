package commands

import (
	"strings"
	"testing"
)

func TestChatBusyCommandPolicyGateDefaultOff(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "")
	if chatBusyCommandEnabled() {
		t.Fatalf("%s 默认必须关闭（P1 灰度）", chatBusyCommandEnv)
	}
	for _, value := range []string{"0", "false", "off", "no", "disabled", "garbage"} {
		t.Setenv(chatBusyCommandEnv, value)
		if chatBusyCommandEnabled() {
			t.Fatalf("%s=%q 不应视为开启", chatBusyCommandEnv, value)
		}
	}
	for _, value := range []string{"1", "true", "on", "yes", "enabled", " TRUE "} {
		t.Setenv(chatBusyCommandEnv, value)
		if !chatBusyCommandEnabled() {
			t.Fatalf("%s=%q 应视为开启", chatBusyCommandEnv, value)
		}
	}
}

func TestChatBusyCommandPolicyFirstBatchImmediateWhenEnabled(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	immediate := []string{"/help", "/?", "/status", "/session", "/queue", "/queue status"}
	for _, text := range immediate {
		if got := chatSlashCommandBusyPolicyFor(text); got != chatBusyPolicyImmediate {
			t.Fatalf("%q 应为 immediate，实际 %s", text, got)
		}
	}

	notImmediate := map[string]chatBusyCommandPolicy{
		"/queue clear":  chatBusyPolicyReject,
		"/status extra": chatBusyPolicyDeferred, // 自由参数 → 回退旧白名单（queue-safe→D）
		"/model":        chatBusyPolicyDeferred, // 旧白名单 queue-safe（§4.3 基线保持 deferred）
		"/theme":        chatBusyPolicyReject,   // 未标记且不在旧白名单
		"/nope":         chatBusyPolicyReject,
		"":              chatBusyPolicyInherit,
		"hello":         chatBusyPolicyInherit,
	}
	for text, want := range notImmediate {
		if got := chatSlashCommandBusyPolicyFor(text); got != want {
			t.Fatalf("%q 应为 %s，实际 %s", text, want, got)
		}
	}
}

// T18：灰度关闭时，resolver 决策必须与既有 chatSlashCommandQueueSafe 白名单逐条等价。
func TestChatBusyCommandPolicyGateOffMatchesLegacyWhitelist(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "off")

	for _, spec := range chatSlashCommandCatalog() {
		for _, name := range spec.allNames() {
			got := chatSlashCommandBusyPolicyFor(name)
			if got == chatBusyPolicyImmediate || got == chatBusyPolicyScreen {
				t.Fatalf("关闭灰度后 %q 不得返回 %s", name, got)
			}
			wantLegacySafe := chatSlashCommandQueueSafe(name)
			if (got == chatBusyPolicyDeferred) != wantLegacySafe {
				t.Fatalf("关闭灰度后 %q 决策 %s 与旧白名单(queueSafe=%v)不一致", name, got, wantLegacySafe)
			}
		}
	}
}

func TestChatBusyCommandPolicySubcommandQueueClear(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	if got := chatSlashCommandBusyPolicyFor("/queue clear"); got != chatBusyPolicyReject {
		t.Fatalf("/queue clear 必须保持拒绝（状态变更），实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/QUEUE CLEAR now"); got != chatBusyPolicyReject {
		t.Fatalf("/queue clear 大小写/附加参数形式必须保持拒绝，实际 %s", got)
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
	// 别名归一：/h 是 /history（旧白名单 queue-safe → D），/? 是 /help（I）。
	if got := chatSlashCommandBusyPolicyFor("/h"); got != chatBusyPolicyDeferred {
		t.Fatalf("/h 别名应归一到 /history 的旧策略 D，实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/?"); got != chatBusyPolicyImmediate {
		t.Fatalf("/? 别名应归一到 /help 的 I，实际 %s", got)
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
