//go:build windows

package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMSYSBashDescendantsAreInsideWindowsJobObject measures appendix B item 7
// of docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md.
//
// Cygwin/MSYS fork emulation has historically interacted badly with Windows
// Job Objects: fork children may be spawned with CREATE_BREAKAWAY_FROM_JOB and
// escape the per-command job, in which case TerminateJobObject kills only the
// bash parent and leaves orphaned helpers behind. This test does not assume the
// outcome — it queries the job's own process-id list, cancels, and then checks
// a liveness file that a forked child keeps appending to.
func TestMSYSBashDescendantsAreInsideWindowsJobObject(t *testing.T) {
	resolution, err := ResolveShell(ShellConfig{
		Prefer: []string{ShellIDGitBash, ShellIDMSYSBash},
	})
	if err != nil || resolution == nil || !resolution.Shell.SpeaksPosix() || resolution.Shell.Path == "" {
		t.Skipf("no MSYS/Cygwin bash available on this machine: %v", err)
	}
	shell := resolution.Shell

	dir := t.TempDir()
	// Forward slashes: the script is interpreted by MSYS, where a backslash is
	// an escape character inside a shell word.
	livenessPath := filepath.ToSlash(filepath.Join(dir, "alive.txt"))
	script := fmt.Sprintf(
		"(while :; do printf x >> '%s'; sleep 0.2; done) & wait",
		livenessPath,
	)
	argv := shell.DeriveExecArgs(script, false)
	cmd := exec.Command(argv[0], argv[1:]...)

	guard := NewProcessGuard()
	require.NoError(t, guard.Bind(cmd))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runCommandCapture(ctx, cmd, guard, &output, nil, "msys-job-tree", 0, guard.PID)
	}()

	// Wait for the child to be alive *and* for the job object to enumerate more
	// than the bash root. A single PID here would mean MSYS children broke away.
	deadline := time.Now().Add(20 * time.Second)
	var jobPIDs []int
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(livenessPath)
		guard.mu.Lock()
		jobPIDs = append([]int(nil), guard.platform.livePIDs(guard.pid)...)
		guard.mu.Unlock()
		if readErr == nil && len(data) > 0 && len(jobPIDs) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(jobPIDs) < 2 {
		cancel()
		<-done
		t.Fatalf("MSYS child did not join the Windows Job Object: job PIDs=%v output=%q", jobPIDs, output.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("guarded MSYS run did not return after cancel")
	}

	report := guard.Report()
	require.True(t, report.TreeKill, "expected a tree kill, got %+v", report)
	require.Equal(t, "job_object", report.Mode, "TerminateJobObject must be the killing mechanism, got %+v", report)
	require.GreaterOrEqual(t, len(report.Killed), 2,
		"the job must have contained the bash root and its forked child, got %+v", report)

	// Independent of the API report: the child must actually stop writing.
	before, statErr := os.Stat(livenessPath)
	require.NoError(t, statErr)
	time.Sleep(800 * time.Millisecond)
	after, statErr := os.Stat(livenessPath)
	require.NoError(t, statErr)
	require.Equal(t, before.Size(), after.Size(),
		"MSYS descendant survived TerminateJobObject (orphan leak)")
}
