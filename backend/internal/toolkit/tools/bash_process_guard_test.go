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
