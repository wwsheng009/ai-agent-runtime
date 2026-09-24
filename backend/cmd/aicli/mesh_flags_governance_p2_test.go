package main

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
)

// P2 两个治理开关的默认值与接线（架构 §9.4 / §9.5 / §10）：
//   - --mesh-allow-nonloopback 默认**关闭**（非回环一律拒绝写路径）；
//   - --mesh-journal 默认**开启**（审计默认开，关闭后只降级 watch/gc）。

// TestMeshNonLoopbackFlagDefaults 验证跨机开关已注册且默认关闭。默认开等于把
// 「非回环一律拒绝」变成空话，故这里把默认值钉死。
func TestMeshNonLoopbackFlagDefaults(t *testing.T) {
	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)

	flag := fs.Lookup(meshAllowNonLoopbackFlag)
	if flag == nil {
		t.Fatalf("未注册 --%s", meshAllowNonLoopbackFlag)
	}
	if flag.DefValue != "false" {
		t.Fatalf("--%s 默认值 = %q, want false（§9.4：非回环默认拒绝）", meshAllowNonLoopbackFlag, flag.DefValue)
	}
}

// TestApplyMeshNonLoopbackFlag 验证 --mesh-allow-nonloopback → 进程级开关的接线：
// 默认不改行为，显式 =true 才放宽。
func TestApplyMeshNonLoopbackFlag(t *testing.T) {
	prev := commands.ChatWebMeshAllowNonLoopback()
	t.Cleanup(func() { commands.SetChatWebMeshAllowNonLoopback(prev) })
	commands.SetChatWebMeshAllowNonLoopback(false)

	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	applyMeshGovernanceFlags(fs)
	if commands.ChatWebMeshAllowNonLoopback() {
		t.Fatal("默认不得开启跨机开关（§9.4：非回环默认拒绝）")
	}

	fs = pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	if err := fs.Parse([]string{"--" + meshAllowNonLoopbackFlag + "=true"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyMeshGovernanceFlags(fs)
	if !commands.ChatWebMeshAllowNonLoopback() {
		t.Fatal("显式 --mesh-allow-nonloopback=true 必须开启跨机开关")
	}
}

// TestMeshJournalFlagDefaults 验证审计开关已注册且默认开启（§9.5 / Q9：
// 审计价值高、成本低）。
func TestMeshJournalFlagDefaults(t *testing.T) {
	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)

	flag := fs.Lookup(meshJournalFlag)
	if flag == nil {
		t.Fatalf("未注册 --%s", meshJournalFlag)
	}
	if flag.DefValue != "true" {
		t.Fatalf("--%s 默认值 = %q, want true（§9.5：审计默认开启）", meshJournalFlag, flag.DefValue)
	}
}

// TestApplyMeshJournalFlag 验证 --mesh-journal → 进程级开关的接线：默认保持
// 开启，显式 =false 才关闭（关闭后的降级语义由 internal/mesh 的
// journal_disabled_test.go 与 mesh_bootstrap 接线共同保证）。
func TestApplyMeshJournalFlag(t *testing.T) {
	prev := commands.ChatWebMeshJournalEnabled()
	t.Cleanup(func() { commands.SetChatWebMeshJournalEnabled(prev) })
	commands.SetChatWebMeshJournalEnabled(true)

	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	applyMeshGovernanceFlags(fs)
	if !commands.ChatWebMeshJournalEnabled() {
		t.Fatal("默认必须保持审计开启（§9.5）")
	}

	fs = pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	if err := fs.Parse([]string{"--" + meshJournalFlag + "=false"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyMeshGovernanceFlags(fs)
	if commands.ChatWebMeshJournalEnabled() {
		t.Fatal("显式 --mesh-journal=false 必须关闭审计")
	}
}
