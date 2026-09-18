package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func detachProbeCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 60"
	}
	return "sleep 60"
}

func TestShellToolExposesDetachParameter(t *testing.T) {
	params := NewShellTool().Parameters()
	properties, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected parameters shape: %#v", params)
	}
	schema, ok := properties["detach"].(map[string]interface{})
	if !ok {
		t.Fatalf("shell tool must expose the detach parameter, got %#v", properties)
	}
	if schema["type"] != "boolean" {
		t.Fatalf("detach must be a boolean, got %#v", schema)
	}
}

func detachedProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
		if err != nil {
			return true
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		return true
	} else if errors.Is(err, syscall.EPERM) {
		// Alive but owned by another user.
		return true
	}
	return false
}

func killDetachedProcess(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run()
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}

func TestBashToolDetachLaunchesIndependentProcess(t *testing.T) {
	tool := NewShellTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": detachProbeCommand(),
		"detach":  true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if !result.Success {
		t.Fatalf("expected success, error=%v content=%q", result.Error, result.Content)
	}
	if result.Metadata["detached"] != true {
		t.Fatalf("expected detached=true metadata, got %#v", result.Metadata)
	}
	pid, _ := result.Metadata["detached_pid"].(int)
	if pid <= 0 {
		t.Fatalf("invalid detached_pid: %#v", result.Metadata["detached_pid"])
	}
	t.Cleanup(func() { killDetachedProcess(pid) })

	if !strings.Contains(result.Content, strconv.Itoa(pid)) {
		t.Fatalf("content should mention pid %d: %q", pid, result.Content)
	}
	if !detachedProcessAlive(pid) {
		t.Fatalf("detached process %d is not alive", pid)
	}
}

func TestBashToolDetachRefusedWhenDisabled(t *testing.T) {
	t.Setenv(runtimeexecutor.DetachAllowedEnv, "0")
	tool := NewShellTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": detachProbeCommand(),
		"detach":  true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatalf("expected refusal when %s=0, got content=%q", runtimeexecutor.DetachAllowedEnv, result.Content)
	}
	if result.Metadata["detach_refused"] != true {
		t.Fatalf("expected detach_refused metadata, got %#v", result.Metadata)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), runtimeexecutor.DetachAllowedEnv) {
		t.Fatalf("error should name the gate env var, got %v", result.Error)
	}
}

func TestBashToolDetachRejectedInBatch(t *testing.T) {
	tool := NewShellTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"detach": true,
		"commands": []interface{}{
			map[string]interface{}{"command": "echo first"},
			map[string]interface{}{"command": "echo second"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatalf("expected batch refusal, got content=%q", result.Content)
	}
	if result.Metadata["detach_batch_refused"] != true {
		t.Fatalf("expected detach_batch_refused metadata, got %#v", result.Metadata)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "commands") {
		t.Fatalf("error should explain the batch limitation, got %v", result.Error)
	}
}

// TestBashToolDetachSurvivesGuardTerminate proves the detached process is not
// part of the per-command job: a timed-out sibling command must not kill it.
func TestBashToolDetachSurvivesGuardTerminate(t *testing.T) {
	tool := NewShellTool()
	launch, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": detachProbeCommand(),
		"detach":  true,
	})
	if err != nil || !launch.Success {
		t.Fatalf("detach launch failed: err=%v result=%+v", err, launch)
	}
	pid, _ := launch.Metadata["detached_pid"].(int)
	if pid <= 0 {
		t.Fatalf("invalid detached_pid: %#v", launch.Metadata)
	}
	t.Cleanup(func() { killDetachedProcess(pid) })

	timeoutCommand := "Start-Sleep -Seconds 30"
	if runtime.GOOS != "windows" {
		timeoutCommand = "sleep 30"
	}
	timeoutResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": timeoutCommand,
		"timeout": "2s",
	})
	if err != nil {
		t.Fatalf("timeout command Execute: %v", err)
	}
	if timeoutResult.Success {
		t.Fatalf("expected the 2s command to be terminated, got content=%q", timeoutResult.Content)
	}
	if !detachedProcessAlive(pid) {
		t.Fatalf("detached process %d was killed by a sibling command's guard terminate", pid)
	}
}
