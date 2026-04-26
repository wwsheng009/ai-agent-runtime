package executor

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCaptureCombinedOutput_ReturnsOriginalWhenWithinBudget(t *testing.T) {
	cmd := exec.Command("go", "env", "GOOS")

	capture, err := CaptureCombinedOutput(cmd, 4096)
	if err != nil {
		t.Fatalf("CaptureCombinedOutput failed: %v", err)
	}
	if capture.Truncated {
		t.Fatalf("did not expect truncation, got %+v", capture)
	}
	if strings.TrimSpace(capture.Output) == "" {
		t.Fatalf("expected non-empty output, got %+v", capture)
	}
	if strings.Contains(capture.Output, "exec output truncated at capture limit") {
		t.Fatalf("did not expect truncation marker, got %q", capture.Output)
	}
}

func TestCaptureCombinedOutput_TruncatesLargeOutputAndKeepsHeadTail(t *testing.T) {
	var cmd *exec.Cmd
	if IsWindows() {
		shell := DefaultUserShell()
		command := "$i=0; while($i -lt 600){ Write-Output (\"line-\" + $i + \"-abcdefghijklmnopqrstuvwxyz0123456789\"); $i++ }"
		args := shell.DeriveExecArgs(command, false)
		cmd = exec.Command(args[0], args[1:]...)
	} else {
		cmd = exec.Command("sh", "-c", "i=0; while [ $i -lt 600 ]; do printf 'line-%s-abcdefghijklmnopqrstuvwxyz0123456789\\n' \"$i\"; i=$((i+1)); done")
	}

	capture, err := CaptureCombinedOutput(cmd, 4096)
	if err != nil {
		t.Fatalf("CaptureCombinedOutput failed: %v", err)
	}
	if !capture.Truncated {
		t.Fatalf("expected truncation, got %+v", capture)
	}
	if !strings.Contains(capture.Output, "Total output lines: 600") {
		t.Fatalf("expected line count header, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "exec output truncated at capture limit") {
		t.Fatalf("expected truncation marker, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "line-0-") {
		t.Fatalf("expected head to be preserved, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "line-599-") {
		t.Fatalf("expected tail to be preserved, got %q", capture.Output)
	}
}
