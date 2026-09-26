package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestExitCodeFromErrorKeepsPlainStatuses(t *testing.T) {
	if code := exitCodeFromError(nil); code != -1 {
		t.Fatalf("nil error must not invent an exit code, got %d", code)
	}
	if code := exitCodeFromError(fmt.Errorf("exit status 3")); code != 3 {
		t.Fatalf("expected exit status 3, got %d", code)
	}
	if termination, signalName := terminationFromError(fmt.Errorf("exit status 3")); termination != "" || signalName != "" {
		t.Fatalf("plain exits must not be classified as signals, got %q/%q", termination, signalName)
	}
}

// TestExitCodeFromErrorReportsSignalDeathAs128PlusN pins the shell convention:
// a process killed by signal N reports 128+N, not Go's -1, and the result is
// classified as termination=signal with the signal label.
func TestExitCodeFromErrorReportsSignalDeathAs128PlusN(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals are a Unix concept; Windows WaitStatus never reports Signaled")
	}
	err := exec.Command("sh", "-c", "kill -9 $$").Run()
	if err == nil {
		t.Fatal("expected the shell to die by SIGKILL")
	}
	if code := exitCodeFromError(err); code != 137 {
		t.Fatalf("expected 128+9=137 for SIGKILL, got %d (err=%v)", code, err)
	}
	termination, signalName := terminationFromError(err)
	if termination != "signal" {
		t.Fatalf("expected termination=signal, got %q (err=%v)", termination, err)
	}
	if strings.TrimSpace(signalName) == "" {
		t.Fatalf("expected a signal label, got %q", signalName)
	}
}

// TestBashToolSignalDeathIsContentSuccessWith137 proves the metadata wiring: a
// signal death is a normal process outcome (content success), reported as
// exit_code=137 + termination=signal.
func TestBashToolSignalDeathIsContentSuccessWith137(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals are a Unix concept; Windows WaitStatus never reports Signaled")
	}
	tool := NewBashTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "kill -9 $$",
		"timeout_ms": 10000,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("signal death must stay a content success, got %+v", result)
	}
	if result.Metadata["exit_code"] != 137 {
		t.Fatalf("expected exit_code=137, got %#v (metadata %#v)", result.Metadata["exit_code"], result.Metadata)
	}
	if result.Metadata["termination"] != "signal" {
		t.Fatalf("expected termination=signal, got %#v", result.Metadata["termination"])
	}
	if !strings.Contains(result.Content, "Exit code: 137") {
		t.Fatalf("expected Exit code: 137 header, got %q", result.Content)
	}
}
