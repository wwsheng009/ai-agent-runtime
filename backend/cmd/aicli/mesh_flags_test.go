package main

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
)

// TestMeshRestrictWorkspaceFlagDefaults 验证治理开关已注册且默认关闭
// （架构 §10）。误把默认值改成 true 会让跨工作区写调用在用户没显式开启
// 时被拒——§9.3 明确要求默认放行（工作区不是权限边界）。
func TestMeshRestrictWorkspaceFlagDefaults(t *testing.T) {
	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)

	flag := fs.Lookup(meshRestrictWorkspaceFlag)
	if flag == nil {
		t.Fatalf("未注册 --%s", meshRestrictWorkspaceFlag)
	}
	if flag.DefValue != "false" {
		t.Fatalf("--%s 默认值 = %q, want false", meshRestrictWorkspaceFlag, flag.DefValue)
	}
}

// TestApplyMeshGovernanceFlags 验证 flag → 进程级开关的接线：默认不改行为，
// 显式开启后 ChatWebMeshRestrictWorkspace() 为 true。
func TestApplyMeshGovernanceFlags(t *testing.T) {
	prev := commands.ChatWebMeshRestrictWorkspace()
	t.Cleanup(func() { commands.SetChatWebMeshRestrictWorkspace(prev) })
	commands.SetChatWebMeshRestrictWorkspace(false)

	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	applyMeshGovernanceFlags(fs)
	if commands.ChatWebMeshRestrictWorkspace() {
		t.Fatal("默认不得开启收敛开关（§9.3：跨工作区默认放行）")
	}

	fs = pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	if err := fs.Parse([]string{"--" + meshRestrictWorkspaceFlag}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyMeshGovernanceFlags(fs)
	if !commands.ChatWebMeshRestrictWorkspace() {
		t.Fatal("显式 --mesh-restrict-workspace 必须开启收敛开关")
	}
}

// TestApplyMeshGovernanceFlagsNilSafe 验证 nil flag set 不 panic（内嵌调用
// 路径）。
func TestApplyMeshGovernanceFlagsNilSafe(t *testing.T) {
	registerMeshGovernanceFlags(nil)
	applyMeshGovernanceFlags(nil)
}

// TestMeshAllowStopFlagDefaults 验证停止开关已注册且默认**关闭**（架构 §5.7 /
// §9.2）。默认开等于把「谁都能杀谁」变成常态，与拉起开关的默认值刻意相反。
func TestMeshAllowStopFlagDefaults(t *testing.T) {
	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)

	flag := fs.Lookup(meshAllowStopFlag)
	if flag == nil {
		t.Fatalf("未注册 --%s", meshAllowStopFlag)
	}
	if flag.DefValue != "false" {
		t.Fatalf("--%s 默认值 = %q, want false（停止是治理动作，默认拒绝）", meshAllowStopFlag, flag.DefValue)
	}
}

// TestApplyMeshAllowStopFlag 验证 --mesh-allow-stop → 进程级开关的接线：
// 默认不改行为，显式 =true 才开启。
func TestApplyMeshAllowStopFlag(t *testing.T) {
	prev := commands.ChatWebMeshAllowStop()
	t.Cleanup(func() { commands.SetChatWebMeshAllowStop(prev) })
	commands.SetChatWebMeshAllowStop(false)

	fs := pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	applyMeshGovernanceFlags(fs)
	if commands.ChatWebMeshAllowStop() {
		t.Fatal("默认不得开启停止开关（§9.2：默认拒绝）")
	}

	fs = pflag.NewFlagSet("mesh", pflag.ContinueOnError)
	registerMeshGovernanceFlags(fs)
	if err := fs.Parse([]string{"--" + meshAllowStopFlag + "=true"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyMeshGovernanceFlags(fs)
	if !commands.ChatWebMeshAllowStop() {
		t.Fatal("显式 --mesh-allow-stop=true 必须开启停止开关")
	}
}
