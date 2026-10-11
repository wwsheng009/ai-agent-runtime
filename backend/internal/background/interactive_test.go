package background

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/pty"
)

// interactiveEchoCommand returns a shell command that reads stdin line by line
// and echoes each line back. It never exits until stdin closes.
func interactiveEchoCommand() string {
	if runtime.GOOS == "windows" {
		return `$input | ForEach-Object { Write-Output ("echo:" + $_) }`
	}
	return `while IFS= read -r line; do printf 'echo:%s\n' "$line"; done`
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
