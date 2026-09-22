package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func longRunningTestCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 60"
	}
	return "sleep 60"
}

// TestShellToolTerminatesTreeOnTimeout proves a shell command that exceeds its
// timeout no longer leaves the tool call pending: the tree is terminated and
// the result carries structured termination diagnostics.
func TestShellToolTerminatesTreeOnTimeout(t *testing.T) {
	tool := NewShellTool()
	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": longRunningTestCommand(),
		"timeout": "1s",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("timeout did not bound the call: %s", elapsed)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a hard timeout failure, got %+v", result)
	}
	if result.Metadata == nil {
		t.Fatal("missing metadata")
	}
	if got := strings.TrimSpace(fmt.Sprint(result.Metadata["termination"])); got != "timeout" {
		t.Fatalf("termination = %q, want timeout (metadata=%v)", got, result.Metadata)
	}
	if treeKill, _ := result.Metadata["process_tree_kill"].(bool); !treeKill {
		t.Fatalf("expected process_tree_kill=true, metadata=%v", result.Metadata)
	}
}

func TestResolveShellTimeoutBudgetCeiling(t *testing.T) {
	t.Setenv("AICLI_SHELL_MAX_COMMAND_TIMEOUT", "200ms")
	budget := resolveShellTimeoutBudget(context.Background(), time.Hour)
	if budget.Effective > 200*time.Millisecond {
		t.Fatalf("ceiling not applied: %+v", budget)
	}
	if budget.Source != "runtime_ceiling" {
		t.Fatalf("budget source = %q, want runtime_ceiling", budget.Source)
	}
}

// TestResolveShellTimeoutBudgetCeilingDisabledByDefault 回归：运行时上限默认
// 关闭（0 = 不限制）。自动化场景必须能把命令跑到自然结束，唯一的时间上界
// 来自模型显式传入的 timeout/timeout_ms；只有显式配置
// AICLI_SHELL_MAX_COMMAND_TIMEOUT 时才恢复硬上限。
func TestResolveShellTimeoutBudgetCeilingDisabledByDefault(t *testing.T) {
	t.Setenv("AICLI_SHELL_MAX_COMMAND_TIMEOUT", "")
	if got := resolveMaxShellCommandTimeout(); got != 0 {
		t.Fatalf("default ceiling = %s, want 0 (disabled)", got)
	}
	budget := resolveShellTimeoutBudget(context.Background(), 2*time.Hour)
	if budget.Effective != 2*time.Hour || budget.Requested != 2*time.Hour {
		t.Fatalf("explicit timeout must not be capped by default: %+v", budget)
	}
	if budget.Source == "runtime_ceiling" {
		t.Fatalf("budget source = %q, want the requested timeout", budget.Source)
	}

	t.Setenv("AICLI_SHELL_MAX_COMMAND_TIMEOUT", "off")
	if got := resolveMaxShellCommandTimeout(); got != 0 {
		t.Fatalf("off ceiling = %s, want 0 (disabled)", got)
	}

	t.Setenv("AICLI_SHELL_MAX_COMMAND_TIMEOUT", "15") // 漏写单位
	if got := resolveMaxShellCommandTimeout(); got != 0 {
		t.Fatalf("unparsable ceiling = %s, want 0 (disabled, no silent cap)", got)
	}

	t.Setenv("AICLI_SHELL_MAX_COMMAND_TIMEOUT", "20m")
	if got := resolveMaxShellCommandTimeout(); got != 20*time.Minute {
		t.Fatalf("configured ceiling = %s, want 20m", got)
	}
}

// TestDefaultCommandExecuterUnlimitedTimeoutRunsToCompletion 回归：上限关闭且
// 调用方未指定超时时 budget.Effective == 0 表示"不设超时"，绝不能被实现成
// context.WithTimeout(ctx, 0)——那会让命令立即以 DeadlineExceeded 失败。
func TestDefaultCommandExecuterUnlimitedTimeoutRunsToCompletion(t *testing.T) {
	t.Setenv("AICLI_SHELL_MAX_COMMAND_TIMEOUT", "")
	executer := &DefaultCommandExecuter{}
	result, err := executer.Execute(context.Background(), "echo unlimited-timeout-ok", 0)
	if err != nil {
		t.Fatalf("unlimited timeout must not fail the command: %v", err)
	}
	if !strings.Contains(result.Output, "unlimited-timeout-ok") {
		t.Fatalf("output=%q, want the echo marker", result.Output)
	}
	if result.TimeoutEffectiveMs != 0 || result.TimeoutSource != "tool_default" {
		t.Fatalf("timeout budget = (%d ms, %s), want (0, tool_default) = unlimited",
			result.TimeoutEffectiveMs, result.TimeoutSource)
	}
}

func TestLongRunningShellCommandHint(t *testing.T) {
	if hint := longRunningShellCommandHint("bsk daemon status"); hint == "" {
		t.Fatal("expected daemon lifecycle hint")
	}
	if hint := longRunningShellCommandHint("npm run dev"); hint == "" {
		t.Fatal("expected dev-server hint")
	}
	if hint := longRunningShellCommandHint("git status"); hint != "" {
		t.Fatalf("unexpected hint for plain command: %q", hint)
	}
}
