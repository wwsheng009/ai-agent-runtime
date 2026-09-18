package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestProcessGuardHelperProcess is a re-exec helper: the test binary launches
// itself to simulate a shell that spawns a long-lived grandchild.
func TestProcessGuardHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PROCESS_GUARD_HELPER") != "1" {
		return
	}
	mode := os.Getenv("PROCESS_GUARD_HELPER_MODE")
	switch mode {
	case "spawner":
		spawnStubbornGrandchild()
		os.Exit(0)
	case "spawner-pidfile":
		spawnPIDFileGrandchild()
		os.Exit(0)
	case "spawner-and-wait":
		spawnStubbornGrandchild()
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "grandchild-pidfile":
		if pidFile := strings.TrimSpace(os.Getenv("PROCESS_GUARD_PID_FILE")); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
		}
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "stubborn":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(0)
}

func spawnPIDFileGrandchild() {
	child := exec.Command(os.Args[0], "-test.run=TestProcessGuardHelperProcess", "--")
	child.Env = append(os.Environ(),
		"GO_WANT_PROCESS_GUARD_HELPER=1",
		"PROCESS_GUARD_HELPER_MODE=grandchild-pidfile",
	)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	_ = child.Start()
}

func spawnStubbornGrandchild() {
	child := exec.Command(os.Args[0], "-test.run=TestProcessGuardHelperProcess", "--")
	child.Env = append(os.Environ(),
		"GO_WANT_PROCESS_GUARD_HELPER=1",
		"PROCESS_GUARD_HELPER_MODE=stubborn",
	)
	// Inherit stdout/stderr so the grandchild keeps the parent's output pipe
	// open - the exact shape of a daemon started by a shell command.
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	_ = child.Start()
}

func helperCommand(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessGuardHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_PROCESS_GUARD_HELPER=1",
		"PROCESS_GUARD_HELPER_MODE="+mode,
	)
	return cmd
}

// TestRunCommandCaptureTerminatesTreeOnCancel proves the guarded path kills the
// whole tree on context cancellation instead of only the direct child.
func TestRunCommandCaptureTerminatesTreeOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := helperCommand("spawner-and-wait")
	guard := NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		t.Fatalf("bind: %v", err)
	}

	var buf bytes.Buffer
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- runCommandCapture(ctx, cmd, guard, &buf, nil, "helper", 0, guard.PID)
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("guarded run did not return after context cancel")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("guarded cancel took %s, want bounded", elapsed)
	}

	report := guard.Report()
	if !report.TreeKill {
		t.Fatalf("expected tree kill on cancel, got %+v", report)
	}
	if len(report.Leftovers) != 0 {
		t.Fatalf("expected no leftover descendants after terminate, got %v", report.Leftovers)
	}
}

// TestRunCommandCaptureBoundsWaitDelayWhenGrandchildHoldsPipe proves that a
// shell that exits while its grandchild still holds the output pipe can no
// longer keep the tool call pending forever.
func TestRunCommandCaptureBoundsWaitDelayWhenGrandchildHoldsPipe(t *testing.T) {
	t.Setenv("AICLI_SHELL_WAIT_DELAY", "1s")
	t.Setenv("AICLI_SHELL_KILL_ORPHANS_ON_EXIT", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := helperCommand("spawner")
	guard := NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		t.Fatalf("bind: %v", err)
	}

	var buf bytes.Buffer
	start := time.Now()
	runErr := runCommandCapture(ctx, cmd, guard, &buf, nil, "helper", 0, guard.PID)
	elapsed := time.Since(start)

	if elapsed > 15*time.Second {
		t.Fatalf("WaitDelay did not bound post-exit wait: %s", elapsed)
	}
	if runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay) {
		t.Logf("run returned %v after %s (grandchild pipe inheritance may differ per platform)", runErr, elapsed)
	}
	report := guard.Report()
	if len(report.Leftovers) == 0 {
		t.Fatalf("expected the grandchild to be tracked as a leftover descendant, report=%+v", report)
	}
}

func TestResolveProcessWaitDelay(t *testing.T) {
	t.Setenv("AICLI_SHELL_WAIT_DELAY", "1500ms")
	if got := ResolveProcessWaitDelay(); got != 1500*time.Millisecond {
		t.Fatalf("wait delay = %s, want 1.5s", got)
	}
	t.Setenv("AICLI_SHELL_WAIT_DELAY", "0s")
	if got := ResolveProcessWaitDelay(); got != 0 {
		t.Fatalf("disabled wait delay = %s, want 0", got)
	}
	t.Setenv("AICLI_SHELL_WAIT_DELAY", "")
	if got := ResolveProcessWaitDelay(); got != DefaultProcessWaitDelay {
		t.Fatalf("default wait delay = %s, want %s", got, DefaultProcessWaitDelay)
	}
}

func TestResolveQuietNoticeTimeout(t *testing.T) {
	t.Setenv("AICLI_SHELL_QUIET_NOTICE_TIMEOUT", "30s")
	if got := ResolveQuietNoticeTimeout(); got != 30*time.Second {
		t.Fatalf("quiet notice = %s, want 30s", got)
	}
	t.Setenv("AICLI_SHELL_QUIET_NOTICE_TIMEOUT", "0")
	if got := ResolveQuietNoticeTimeout(); got != 0 {
		t.Fatalf("quiet notice disabled = %s, want 0", got)
	}
}

// TestProcessGuardConcurrentControl exercises the concurrent Terminate/Close/
// Leftovers paths (race detector target) that the context watchdog and the
// capture teardown can hit simultaneously.
func TestProcessGuardConcurrentControl(t *testing.T) {
	t.Setenv("AICLI_SHELL_KILL_ORPHANS_ON_EXIT", "1")

	cmd := helperCommand("spawner-and-wait")
	guard := NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := guard.Attach(cmd.Process); err != nil {
		t.Logf("attach degraded: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = guard.Terminate()
			_ = guard.Leftovers()
			_ = guard.Report()
			_ = guard.PID()
			_ = guard.WaitDelay()
			guard.NoteAttachError(nil)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		guard.Close()
	}()
	wg.Wait()

	_ = cmd.Wait()
	if report := guard.Report(); len(report.Leftovers) > 0 {
		t.Fatalf("expected orphan cleanup to leave no tracked leftovers, got %v", report.Leftovers)
	}
}

// TestLeftoverDescendantsPreservedByDefault proves the documented default: a
// descendant that keeps the output pipe open after the shell exits is left
// running (and reported via leftovers) rather than killed at close time.
func TestLeftoverDescendantsPreservedByDefault(t *testing.T) {
	t.Setenv("AICLI_SHELL_WAIT_DELAY", "1s")
	t.Setenv("AICLI_SHELL_KILL_ORPHANS_ON_EXIT", "")

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessGuardHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_PROCESS_GUARD_HELPER=1",
		"PROCESS_GUARD_HELPER_MODE=spawner-pidfile",
		"PROCESS_GUARD_PID_FILE="+pidFile,
	)

	guard := NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		t.Fatalf("bind: %v", err)
	}
	var buf bytes.Buffer
	start := time.Now()
	runErr := runCommandCapture(context.Background(), cmd, guard, &buf, nil, "helper", 0, guard.PID)
	elapsed := time.Since(start)
	report := guard.Report()
	t.Logf("runErr=%v elapsed=%s report=%+v", runErr, elapsed.Round(time.Millisecond), report)

	pid := waitForPIDFile(t, pidFile, 3*time.Second)
	if pid <= 0 {
		t.Fatalf("grandchild pid file not written; report=%+v", report)
	}
	alive := processAlive(pid)
	t.Logf("grandchild pid=%d alive_after_close=%v leftovers=%v", pid, alive, report.Leftovers)
	if !containsPID(report.Leftovers, pid) {
		t.Errorf("expected grandchild %d in leftover descendants, got %v", pid, report.Leftovers)
	}
	if !alive {
		t.Errorf("expected preserved grandchild %d to stay alive after close", pid)
	}
	if err := killPID(pid); err != nil {
		t.Logf("cleanup kill %d: %v", pid, err)
	}
}

func waitForPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

func containsPID(pids []int, pid int) bool {
	for _, candidate := range pids {
		if candidate == pid {
			return true
		}
	}
	return false
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
		if err != nil {
			return true
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func killPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
