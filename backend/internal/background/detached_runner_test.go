package background

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// These tests cover the P2 slice of
// docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md: the
// detached runner must follow the *shell dialect*, not the OS, so a Windows box
// running Git Bash gets a sh runner hosted by bash instead of a .ps1 runner
// handed to PowerShell.

// stubDetachedRunnerShell pins the shell the runner is built for and restores
// the previous one on cleanup. Background tests share process state.
func stubDetachedRunnerShell(t *testing.T, shell runtimeexecutor.Shell) {
	t.Helper()
	previous := detachedShellForRunner
	detachedShellForRunner = func() runtimeexecutor.Shell { return shell }
	t.Cleanup(func() { detachedShellForRunner = previous })
}

func TestDetachedRunnerExtensionFollowsHostNotOS(t *testing.T) {
	require.Equal(t, ".sh", detachedRunnerExtension(true))
	require.Equal(t, ".ps1", detachedRunnerExtension(false))
}

// TestPosixRunnerHostArgvKeepsShellPrefix pins the busybox-w32 case: sh is an
// applet, so `busybox.exe runner.sh` is not a valid invocation. The prefix that
// DeriveExecArgs applies to the inner command must also be applied to the
// process that hosts the runner.
func TestPosixRunnerHostArgvKeepsShellPrefix(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.sh")

	bash := runtimeexecutor.Shell{
		Path:    filepath.Join(dir, "bash.exe"),
		Type:    runtimeexecutor.ShellTypeBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	}
	require.Equal(t, []string{filepath.ToSlash(runner)}, posixRunnerHostArgv(bash, runner))

	busybox := runtimeexecutor.Shell{
		Path:    filepath.Join(dir, "busybox.exe"),
		Prefix:  []string{"sh"},
		Type:    runtimeexecutor.ShellTypeSh,
		Dialect: runtimeexecutor.ShellDialectPosixNarrow,
	}
	require.Equal(t, []string{"sh", filepath.ToSlash(runner)}, posixRunnerHostArgv(busybox, runner))
}

// TestDetachedRunnerHostPrefersPosixShell checks that a resolvable POSIX shell
// becomes the runner host, i.e. no PowerShell is involved on the Git Bash path.
func TestDetachedRunnerHostPrefersPosixShell(t *testing.T) {
	shell := runtimeexecutor.Shell{
		Path:    writeFakeShellBinary(t),
		Type:    runtimeexecutor.ShellTypeBash,
		ID:      runtimeexecutor.ShellIDGitBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	}
	stubDetachedRunnerShell(t, shell)

	got, posixHost := detachedRunnerHost()
	require.True(t, posixHost, "a present POSIX shell must host the runner")
	require.Equal(t, shell.Path, got.Path)
}

// TestDetachedRunnerHostFallsBackWhenPosixPathIsGone covers the forced-path
// trap: config points at a bash that has since been uninstalled. Selecting the
// sh runner there would produce a script nothing can start, so the host must
// fall back to PowerShell (and the .ps1 runner).
func TestDetachedRunnerHostFallsBackWhenPosixPathIsGone(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "uninstalled", "bash.exe")
	stubDetachedRunnerShell(t, runtimeexecutor.Shell{
		Path:    missing,
		Type:    runtimeexecutor.ShellTypeBash,
		ID:      runtimeexecutor.ShellIDGitBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	})

	got, posixHost := detachedRunnerHost()
	require.False(t, posixHost, "a missing bash must not be trusted as the runner host")
	if runtime.GOOS == "windows" {
		require.False(t, got.SpeaksPosix(), "fallback host must be PowerShell/cmd, got %q", got.Path)
	}
}

// TestDetachedRunnerPosixHostRejectsDirectory makes sure a directory (a very
// plausible typo for a shell directory such as /usr/bin) is not accepted.
func TestDetachedRunnerPosixHostRejectsDirectory(t *testing.T) {
	stubDetachedRunnerShell(t, runtimeexecutor.Shell{
		Path:    t.TempDir(),
		Type:    runtimeexecutor.ShellTypeBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	})

	_, ok := detachedRunnerPosixHost()
	require.False(t, ok)
}

// TestDefaultDetachedRunnerHostAvailableWithPosixOnly models the §7 Win7 box
// with no PowerShell: PATH is emptied so LookPath cannot find pwsh or
// powershell, and a POSIX shell is the only host left.
func TestDefaultDetachedRunnerHostAvailableWithPosixOnly(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the PowerShell fallback only exists on Windows")
	}
	t.Setenv("PATH", "")
	stubDetachedRunnerShell(t, runtimeexecutor.Shell{
		Path:    writeFakeShellBinary(t),
		Type:    runtimeexecutor.ShellTypeBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	})

	require.True(t, defaultDetachedRunnerHostAvailable(),
		"a POSIX shell must satisfy the runner host requirement without PowerShell")
}

// TestDefaultDetachedRunnerHostUnavailableWithNoHost is the negative case: no
// PowerShell and no POSIX shell means jobs must stay on the in-process path.
func TestDefaultDetachedRunnerHostUnavailableWithNoHost(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the PowerShell fallback only exists on Windows")
	}
	t.Setenv("PATH", "")
	stubDetachedRunnerShell(t, runtimeexecutor.Shell{
		Path:    filepath.Join(t.TempDir(), "nope", "bash.exe"),
		Type:    runtimeexecutor.ShellTypeBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	})

	require.False(t, defaultDetachedRunnerHostAvailable())
}

// TestWriteDetachedRunnerShContentForPosixHost asserts the generated script is a
// sh script, and that Windows paths were slash-normalized where a shell builtin
// (cd) or MSYS path converter would otherwise have to guess.
func TestWriteDetachedRunnerShContentForPosixHost(t *testing.T) {
	dir := t.TempDir()
	runnerPath := filepath.Join(dir, "runner.sh")
	logPath := filepath.Join(dir, "job.log")
	statusPath := filepath.Join(dir, "job.status")

	shell := runtimeexecutor.Shell{
		Path:    filepath.Join(dir, "sqlite-unused.exe"),
		Type:    runtimeexecutor.ShellTypeBash,
		ID:      runtimeexecutor.ShellIDGitBash,
		Dialect: runtimeexecutor.ShellDialectPosixFull,
	}
	require.NoError(t, writeDetachedRunner(runnerPath, shell, true, "echo hi", dir, logPath, statusPath))

	data, err := os.ReadFile(runnerPath)
	require.NoError(t, err)
	content := string(data)

	require.True(t, strings.HasPrefix(content, "#!/bin/sh"), "runner must be a sh script")
	require.Contains(t, content, "heartbeat")
	// Every embedded path is slash-normalized on Windows; ToSlash is identity
	// elsewhere, so one expectation covers both.
	require.Contains(t, content, shellQuote(filepath.ToSlash(statusPath)),
		"exit code must be written to the status file")
	require.Contains(t, content, ">> "+shellQuote(filepath.ToSlash(logPath)))
	// The shell is invoked with the plain -c flag: the detached runner already
	// owns the script file, so no stdin/-s delivery is involved.
	require.Contains(t, content, "'-c' 'echo hi'")
	if runtime.GOOS == "windows" {
		// A drive-letter path with a backslash would mean a path escaped
		// normalization. The runner legitimately contains a `\n` inside a printf
		// format, so check for `:\` specifically.
		require.NotContains(t, content, `:\`, "no Windows-style path should survive into the sh runner")
	}
}

// TestWriteDetachedRunnerKeepsPowerShellRunner is the mirror case: a non-POSIX
// host still gets the .ps1 runner.
func TestWriteDetachedRunnerKeepsPowerShellRunner(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the .ps1 runner is Windows-only")
	}
	dir := t.TempDir()
	runnerPath := filepath.Join(dir, "runner.ps1")

	shell := runtimeexecutor.Shell{Path: "powershell", Type: runtimeexecutor.ShellTypePowerShell}
	require.NoError(t, writeDetachedRunner(
		runnerPath, shell, false, "echo hi", dir,
		filepath.Join(dir, "job.log"), filepath.Join(dir, "job.status"),
	))

	data, err := os.ReadFile(runnerPath)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.False(t, strings.HasPrefix(string(data), "#!/bin/sh"))
}

// TestLaunchDetachedShellWithPosixHost is the end-to-end proof: build a runner
// for a real POSIX shell, start it detached, and read the exit code back from
// the status file — exactly what the manager does after a timeout promotion.
func TestLaunchDetachedShellWithPosixHost(t *testing.T) {
	resolution, err := runtimeexecutor.ResolveShell(runtimeexecutor.ShellConfig{
		Prefer: []string{runtimeexecutor.ShellIDGitBash, runtimeexecutor.ShellIDMSYSBash},
	})
	if err != nil || resolution == nil || !resolution.Shell.SpeaksPosix() || strings.TrimSpace(resolution.Shell.Path) == "" {
		t.Skipf("no POSIX shell on this machine: %v", err)
	}
	stubDetachedRunnerShell(t, resolution.Shell)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "job.log")
	launch, err := launchDetachedShell("exit 7", dir, logPath)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(launch.RunnerPath, ".sh"), "posix host must produce a .sh runner, got %q", launch.RunnerPath)
	require.Positive(t, launch.PID)

	code, ok := waitDetachedStatus(launch.StatusPath, 30*time.Second)
	require.True(t, ok, "status file never appeared; log: %s", readFileOrEmpty(logPath))
	require.Equal(t, 7, code)

	// Appendix B item 5: the sh runner's `kill -0 $$` heartbeat loop must
	// actually produce a numeric timestamp on Windows, not just on paper.
	heartbeat, err := os.ReadFile(launch.HeartbeatPath)
	require.NoError(t, err, "sh runner never wrote a heartbeat")
	_, err = strconv.Atoi(strings.TrimSpace(string(heartbeat)))
	require.NoError(t, err, "heartbeat must contain a unix timestamp, got %q", heartbeat)
}

// TestDetachedPosixRunnerTerminateKillsWholeTree measures appendix B item 8's
// second half: after switching the detached host from PowerShell to bash, the
// cancellation path (taskkill /T /F on Windows) must still reap the forked
// grandchildren, not just the bash runner.
func TestDetachedPosixRunnerTerminateKillsWholeTree(t *testing.T) {
	resolution, err := runtimeexecutor.ResolveShell(runtimeexecutor.ShellConfig{
		Prefer: []string{runtimeexecutor.ShellIDGitBash, runtimeexecutor.ShellIDMSYSBash},
	})
	if err != nil || resolution == nil || !resolution.Shell.SpeaksPosix() || strings.TrimSpace(resolution.Shell.Path) == "" {
		t.Skipf("no POSIX shell on this machine: %v", err)
	}
	stubDetachedRunnerShell(t, resolution.Shell)

	dir := t.TempDir()
	livenessPath := filepath.ToSlash(filepath.Join(dir, "alive.txt"))
	command := fmt.Sprintf("(while :; do printf x >> '%s'; sleep 0.2; done) & wait", livenessPath)
	launch, err := launchDetachedShell(command, dir, filepath.Join(dir, "job.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = terminateProcess(launch.PID) })

	require.True(t, waitForLivenessFile(livenessPath, 20*time.Second),
		"the detached POSIX runner never forked a live child; log: %s", readFileOrEmpty(filepath.Join(dir, "job.log")))

	// taskkill /T /F may return 255 on an MSYS tree even when it killed every
	// process (a retry then reports "process not found"). The production path
	// likewise treats waitProcessGone as authoritative, not the exit code, so
	// this test asserts observable liveness rather than the return value.
	if err := terminateProcess(launch.PID); err != nil {
		t.Logf("terminateProcess returned %v; verifying tree death by liveness file", err)
	}
	before, statErr := os.Stat(livenessPath)
	require.NoError(t, statErr)
	time.Sleep(900 * time.Millisecond)
	after, statErr := os.Stat(livenessPath)
	require.NoError(t, statErr)
	require.Equal(t, before.Size(), after.Size(),
		"a descendant of the detached bash runner survived taskkill /T (orphan leak)")
}

func waitForLivenessFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func waitDetachedStatus(path string, timeout time.Duration) (int, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if code, ok := readDetachedExitCode(path); ok {
			return code, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, false
}

func readFileOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "<unreadable: " + err.Error() + ">"
	}
	return string(data)
}

// writeFakeShellBinary produces an existing regular file to stand in for a
// shell binary. os.Stat is the whole contract in detachedRunnerPosixHost, so a
// placeholder is enough and no real interpreter is required.
func writeFakeShellBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bash")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}
