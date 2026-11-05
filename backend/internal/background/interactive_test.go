package background

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/pty"
)

// interactiveEchoCommand returns a shell command that reads stdin line by line
// and echoes each line back. It never exits until stdin closes.
func interactiveEchoCommand() string {
	switch runtimeexecutor.DefaultUserShell().Type {
	case runtimeexecutor.ShellTypePowerShell, runtimeexecutor.ShellTypePwsh:
		return `while ($true) { $line = [Console]::ReadLine(); if ($null -eq $line) { break }; Write-Output ("echo:" + $line) }`
	case runtimeexecutor.ShellTypeCmd:
		return "more"
	default:
		return `while IFS= read -r line; do printf 'echo:%s\n' "$line"; done`
	}
}

// interactiveEchoSupported reports whether the detected shell can host the echo
// fixture. cmd.exe cannot read stdin line-by-line portably.
func interactiveEchoSupported() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	return runtimeexecutor.DefaultUserShell().Type != runtimeexecutor.ShellTypeCmd
}

func TestOpenTerminalWriteStdinRoundTrip(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir(), MonitorInterval: 50 * time.Millisecond})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.OpenTerminal(ctx, "session-interactive", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.True(t, job.Interactive)
	require.Equal(t, "terminal", job.Kind)
	require.Equal(t, StatusRunning, job.Status)

	first, err := manager.WriteStdin(ctx, job.ID, "hello\n", 5000)
	require.NoError(t, err)
	require.Contains(t, first.Output, "echo:hello")
	require.Equal(t, "output", first.WaitCondition)
	require.Greater(t, first.NextOffset, int64(0))

	second, err := manager.WriteStdin(ctx, job.ID, "world\n", 5000)
	require.NoError(t, err)
	require.Contains(t, second.Output, "echo:world")
	require.GreaterOrEqual(t, second.Offset, first.NextOffset)

	closed, err := manager.CloseTerminal(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, closed)
	require.True(t, isTerminalStatus(closed.Status))
}

func TestOpenTerminalLongPowerShellScriptKeepsStdinInteractive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell command-line limit regression")
	}
	hosts := []struct {
		name string
		typ  runtimeexecutor.ShellType
	}{
		{name: "pwsh", typ: runtimeexecutor.ShellTypePwsh},
		{name: "powershell", typ: runtimeexecutor.ShellTypePowerShell},
	}
	transports := []struct {
		name string
		tty  bool
	}{
		{name: "pipe"},
		{name: "pty", tty: true},
	}
	for _, host := range hosts {
		for _, transport := range transports {
			t.Run(host.name+"/"+transport.name, func(t *testing.T) {
				if transport.tty && !pty.Supported() {
					t.Skip("ConPTY/pty not supported on this platform")
				}
				path, err := exec.LookPath(host.name)
				if err != nil {
					t.Skipf("%s is not installed", host.name)
				}
				manager := NewManager(Config{
					LogDir: t.TempDir(),
					Shell: runtimeexecutor.Shell{
						Path: path,
						Type: host.typ,
					},
					MonitorInterval: 50 * time.Millisecond,
				})
				t.Cleanup(func() { require.NoError(t, manager.Close()) })

				command := strings.Repeat("# padding\n", 6000) +
					`while ($true) { $line = [Console]::ReadLine(); if ($null -eq $line) { break }; Write-Output ("echo:" + $line) }`
				require.Greater(t, len(command), runtimeexecutor.ShellStdinScriptThreshold)
				job, err := manager.OpenTerminal(context.Background(),
					"session-long-powershell-"+host.name+"-"+transport.name,
					TerminalOpenArgs{Command: command, TTY: transport.tty, Cols: 80, Rows: 30})
				require.NoError(t, err)
				require.NotNil(t, job)
				require.Equal(t, StatusRunning, job.Status)

				if transport.tty {
					// Drain the ConPTY console-host init sequence before sending
					// the line; PowerShell needs a short startup window here.
					for i := 0; i < 5; i++ {
						res, drainErr := manager.WriteStdin(context.Background(), job.ID, "", 1000)
						require.NoError(t, drainErr)
						if res.WaitCondition == "timeout" {
							break
						}
					}
					output := writeStdinUntilContains(t, manager, job.ID, "hello-long\r", "echo:hello-long", 10*time.Second)
					require.Contains(t, output, "echo:hello-long")
				} else {
					output, writeErr := manager.WriteStdin(context.Background(), job.ID, "hello-long\n", 10_000)
					require.NoError(t, writeErr)
					require.Contains(t, output.Output, "echo:hello-long")
				}

				closed, err := manager.CloseTerminal(context.Background(), job.ID)
				require.NoError(t, err)
				require.NotNil(t, closed)
				if !transport.tty {
					require.True(t, isTerminalStatus(closed.Status))
				}
			})
		}
	}
}

func TestWriteStdinRejectsNonInteractiveJob(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.SubmitShell(ctx, "session-one-shot", BackgroundTaskArgs{Command: shellEchoCommand("plain")})
	require.NoError(t, err)
	require.NotNil(t, job)

	_, err = manager.WriteStdin(ctx, job.ID, "nope\n", 1000)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not interactive")
}

func TestOpenTerminalEnforcesSessionCap(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir(), MaxInteractiveSessions: 1})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	first, err := manager.OpenTerminal(ctx, "session-cap", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.NotNil(t, first)

	_, err = manager.OpenTerminal(ctx, "session-cap-2", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too many interactive sessions")

	_, err = manager.CloseTerminal(ctx, first.ID)
	require.NoError(t, err)
}

func TestRecoverPersistedInteractiveJobBecomesOrphaned(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "background.sqlite")

	store, err := NewSQLiteStore(&StoreConfig{Path: storePath})
	require.NoError(t, err)
	require.NoError(t, store.SaveJob(ctx, Job{
		ID:          "job_interactive_crash",
		SessionID:   "session-crash",
		Kind:        "terminal",
		Status:      StatusRunning,
		Interactive: true,
		Command:     "echo hi",
		CreatedAt:   time.Now().UTC(),
	}))
	require.NoError(t, store.Close())

	manager := NewManager(Config{StorePath: storePath, LogDir: filepath.Join(dir, "logs")})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.GetJob(ctx, "job_interactive_crash")
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusOrphaned, job.Status)
	require.True(t, job.Interactive)
}

func TestOpenTerminalReusesLiveSession(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	first, err := manager.OpenTerminal(ctx, "session-reuse", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := manager.OpenTerminal(ctx, "session-reuse", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, first.ID, second.ID, "a live session for the same session id must be reused")
	require.Equal(t, true, second.Metadata["already_open"])

	_, err = manager.CloseTerminal(ctx, first.ID)
	require.NoError(t, err)
}

func TestOpenTerminalDifferentCommandReturnsBusy(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	first, err := manager.OpenTerminal(ctx, "session-busy", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.NotNil(t, first)

	// 同槽位、不同命令：必须显式 busy，而不是静默返回正在运行的会话
	// （2026-10-14：静默复用让调用方以为新会话已启动）。
	_, err = manager.OpenTerminal(ctx, "session-busy", TerminalOpenArgs{Command: "another-command --flag"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInteractiveBusy)

	var busy *InteractiveBusyError
	require.ErrorAs(t, err, &busy)
	require.Equal(t, first.ID, busy.ExistingJobID)
	require.Equal(t, strings.TrimSpace(interactiveEchoCommand()), busy.ExistingCommand)
	require.Equal(t, "another-command --flag", busy.RequestedCommand)
	require.Contains(t, err.Error(), first.ID)
}

func TestWriteStdinSentinelErrors(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.SubmitShell(ctx, "session-sentinel", BackgroundTaskArgs{Command: shellEchoCommand("x")})
	require.NoError(t, err)
	_, err = manager.WriteStdin(ctx, job.ID, "", 1000)
	require.ErrorIs(t, err, ErrNotInteractive)

	_, err = manager.WriteStdin(ctx, "job_missing", "", 1000)
	require.ErrorIs(t, err, ErrSessionNotAvailable)
}

func TestResizeWithoutPTYReportsUnsupported(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.OpenTerminal(ctx, "session-resize", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	// Without a PTY the manager reports the capability gap instead of silently
	// accepting a size it cannot apply.
	require.ErrorIs(t, manager.Resize(ctx, job.ID, 100, 40), ErrTTYUnsupported)
}

func TestOpenTerminalTTYFallsBackToPipeWhenUnsupported(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	if pty.Supported() {
		t.Skip("PTY is supported here; the pipe fallback is not exercised")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.OpenTerminal(ctx, "session-tty-fallback", TerminalOpenArgs{Command: interactiveEchoCommand(), TTY: true})
	require.NoError(t, err)
	require.False(t, job.TTY, "a pipe-backed session must not claim a TTY")
	require.Equal(t, "pipe", job.Metadata["tty_fallback"])

	res, err := manager.WriteStdin(ctx, job.ID, "hi\n", 5000)
	require.NoError(t, err)
	require.Contains(t, res.Output, "echo:hi")

	_, err = manager.CloseTerminal(ctx, job.ID)
	require.NoError(t, err)
}

func TestWriteStdinReportsProcessMetrics(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.OpenTerminal(ctx, "session-metrics", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)

	first, err := manager.WriteStdin(ctx, job.ID, "hi\n", 5000)
	require.NoError(t, err)
	require.NotNil(t, first.OSPID)
	require.Greater(t, *first.OSPID, 0)
	require.NotNil(t, first.RSSKB)

	// The second exchange has a previous CPU sample, so a percentage is derived.
	second, err := manager.WriteStdin(ctx, job.ID, "again\n", 5000)
	require.NoError(t, err)
	require.NotNil(t, second.CPUPercent)
	require.GreaterOrEqual(t, *second.CPUPercent, 0.0)

	_, err = manager.CloseTerminal(ctx, job.ID)
	require.NoError(t, err)
}

func TestCleanTerminals(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir()})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	_, err := manager.OpenTerminal(ctx, "session-clean-a", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	_, err = manager.OpenTerminal(ctx, "session-clean-b", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.Equal(t, 2, manager.CleanTerminals(ctx, ""))

	_, err = manager.OpenTerminal(ctx, "session-clean-c", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.Equal(t, 0, manager.CleanTerminals(ctx, "session-other"))
	require.Equal(t, 1, manager.CleanTerminals(ctx, "session-clean-c"))
}

func TestOpenTerminalPTYSessionDrivesStdin(t *testing.T) {
	if !pty.Supported() {
		t.Skip("ConPTY/pty not supported on this platform")
	}
	if runtime.GOOS != "windows" {
		t.Skip("windows ConPTY fixture")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir(), MonitorInterval: 50 * time.Millisecond})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	// `more` echoes every stdin line through the console host, independent of
	// PSReadLine's win32-input-mode (which expects encoded key events instead
	// of raw characters).
	job, err := manager.OpenTerminal(ctx, "session-pty", TerminalOpenArgs{
		Command: "more",
		TTY:     true,
		Cols:    80,
		Rows:    30,
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.True(t, job.TTY, "a PTY-backed session must report TTY")
	require.Equal(t, StatusRunning, job.Status)

	// Drain until the console host finishes its init sequence (VT mode setup,
	// title, cursor) so the typed line is not racing conhost startup.
	for i := 0; i < 5; i++ {
		res, err := manager.WriteStdin(ctx, job.ID, "", 1000)
		require.NoError(t, err)
		if res.WaitCondition == "timeout" {
			break
		}
	}

	res, err := manager.WriteStdin(ctx, job.ID, "hello\r", 5000)
	require.NoError(t, err)
	// conhost echoes the typed line before `more` re-prints it.
	require.Contains(t, res.Output, "hello")

	require.NoError(t, manager.Resize(ctx, job.ID, 100, 40))

	_, err = manager.CloseTerminal(ctx, job.ID)
	require.NoError(t, err)
}

// TestOpenTerminalJobIsNeverDispatched guards the live-session bug found by the
// E2E session run (2026-10-11): OpenTerminal registered the job as pending, so
// the scheduler dispatched the SAME job through the detached/in-process path a
// second time, clobbering the session with cancel_source=parent_context.
func TestOpenTerminalJobIsNeverDispatched(t *testing.T) {
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := NewManager(Config{LogDir: t.TempDir(), MonitorInterval: 50 * time.Millisecond})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	events := make([]string, 0, 16)
	var eventsMu sync.Mutex
	manager.eventHandler = func(event JobEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event.Type)
	}

	job, err := manager.OpenTerminal(ctx, "session-no-dispatch", TerminalOpenArgs{Command: interactiveEchoCommand()})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusRunning, job.Status)

	// Give the dispatcher ample time to (wrongly) pick the pending job up.
	time.Sleep(500 * time.Millisecond)

	// The session must still be alive and driveable: no second execution may
	// have converged it to a terminal state.
	res, err := manager.WriteStdin(ctx, job.ID, "still-alive\n", 5000)
	require.NoError(t, err)
	require.Contains(t, res.Output, "echo:still-alive")

	// No dispatch-side lifecycle events for this job (queued/launch_retry/
	// process_created from a second run would indicate a double execution).
	eventsMu.Lock()
	recorded := append([]string(nil), events...)
	eventsMu.Unlock()
	for _, eventType := range recorded {
		switch eventType {
		case "queued", "launch_retry", "recovered_queued":
			t.Fatalf("interactive job %s must not be dispatched, got event %q", job.ID, eventType)
		}
	}

	_, err = manager.CloseTerminal(ctx, job.ID)
	require.NoError(t, err)
}

// TestOpenTerminalPTYSurvivesCallerContextCancel guards the E2E canary bug
// (session_20261011162136_52gX0Jys): the PTY wait goroutine hung off the
// CALLER's ctx, so end-of-turn cancelled the session (cancel_source=
// parent_context). The session must live on jobCtx and survive a caller cancel.
func TestOpenTerminalPTYSurvivesCallerContextCancel(t *testing.T) {
	if !pty.Supported() {
		t.Skip("ConPTY/pty not supported on this platform")
	}
	if runtime.GOOS != "windows" {
		t.Skip("windows ConPTY fixture")
	}
	if !interactiveEchoSupported() {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := NewManager(Config{LogDir: t.TempDir(), MonitorInterval: 50 * time.Millisecond})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.OpenTerminal(ctx, "session-pty-survive", TerminalOpenArgs{
		Command: interactiveEchoCommand(),
		TTY:     true,
		Cols:    80,
		Rows:    30,
	})
	require.NoError(t, err)
	require.Equal(t, StatusRunning, job.Status)

	// Simulate end-of-turn: cancel the CALLER's context. The session must NOT
	// be cancelled with it.
	cancel()
	time.Sleep(300 * time.Millisecond)

	// The caller context is already cancelled. The PTY must still accept input
	// and echo it. A bounded poll absorbs ConPTY/PowerShell startup jitter
	// without turning a missing echo into a silent skip.
	output := writeStdinUntilContains(t, manager, job.ID, "after-cancel\r", "echo:after-cancel", backgroundTestTimeout(10*time.Second))
	require.Contains(t, output, "echo:after-cancel", "session must survive caller ctx cancel and still echo input")

	current, err := manager.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, current.Status, "session must not converge when the caller ctx is cancelled")

	_, err = manager.CloseTerminal(context.Background(), job.ID)
	require.NoError(t, err)
}

func writeStdinUntilContains(t *testing.T, manager *Manager, jobID, input, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var seen strings.Builder
	sent := false
	var lastErr error
	for time.Now().Before(deadline) {
		payload := ""
		waitMs := 500
		if !sent {
			payload = input
			waitMs = 2000
			sent = true
		}
		res, err := manager.WriteStdin(context.Background(), jobID, payload, waitMs)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		seen.WriteString(res.Output)
		if strings.Contains(seen.String(), want) {
			return seen.String()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("PTY did not echo %q within %s (last_err=%v, seen=%q)", want, timeout, lastErr, seen.String())
	return seen.String()
}
