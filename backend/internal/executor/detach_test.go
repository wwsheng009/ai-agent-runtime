package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestResolveDetachAllowed(t *testing.T) {
	t.Setenv(DetachAllowedEnv, "")
	if !ResolveDetachAllowed() {
		t.Fatal("expected detached launches to be enabled by default")
	}
	for _, raw := range []string{"0", "false", "OFF", "no", "disabled"} {
		t.Setenv(DetachAllowedEnv, raw)
		if ResolveDetachAllowed() {
			t.Fatalf("%s=%q: expected detached launches to be disabled", DetachAllowedEnv, raw)
		}
	}
	t.Setenv(DetachAllowedEnv, "1")
	if !ResolveDetachAllowed() {
		t.Fatalf("%s=1: expected detached launches to stay enabled", DetachAllowedEnv)
	}
}

// TestStartDetachedLaunchesIndependentProcess proves the launcher returns the
// real PID of a live process started outside the runtime's capture plumbing.
func TestStartDetachedLaunchesIndependentProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "detached.pid")
	env := append(os.Environ(),
		"GO_WANT_PROCESS_GUARD_HELPER=1",
		"PROCESS_GUARD_HELPER_MODE=grandchild-pidfile",
		"PROCESS_GUARD_PID_FILE="+pidFile,
	)
	proc, err := StartDetached(DetachedLaunch{
		Path: os.Args[0],
		Args: []string{"-test.run=TestProcessGuardHelperProcess", "--"},
		Env:  env,
	})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}
	t.Cleanup(func() {
		if err := killPID(proc.PID); err != nil {
			t.Logf("cleanup kill %d: %v", proc.PID, err)
		}
	})

	wantMode := DetachModeSetsid
	if runtime.GOOS == "windows" {
		wantMode = DetachModeNewConsole
	}
	if proc.Mode != wantMode {
		t.Fatalf("mode = %q, want %q", proc.Mode, wantMode)
	}

	pid := waitForPIDFile(t, pidFile, 5*time.Second)
	if pid != proc.PID {
		t.Fatalf("helper reported pid %d, launcher reported %d", pid, proc.PID)
	}
	if !processAlive(proc.PID) {
		t.Fatalf("detached process %d is not alive", proc.PID)
	}
}

func TestStartDetachedRejectsMissingExecutable(t *testing.T) {
	if _, err := StartDetached(DetachedLaunch{Path: "definitely-not-a-real-binary-xyz"}); err == nil {
		t.Fatal("expected error for a missing executable")
	}
	if _, err := StartDetached(DetachedLaunch{Path: "   "}); err == nil {
		t.Fatal("expected error for an empty executable path")
	}
}
