package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
)

// remoteNoticeTestTarget builds the session-scoped target the notices describe.
func remoteNoticeTestTarget(warnings ...string) *remoteFlagTarget {
	return &remoteFlagTarget{
		ServerName: "127-0-0-1",
		Config:     mcpconfig.MCPConfig{Type: "streamable"},
		Depth:      remotetargets.DepthOps,
		Warnings:   warnings,
	}
}

// withRemoteNoticeSink registers a real interactive coordinator as the diagnostic
// sink, the same wiring a chat session performs on startup.
func withRemoteNoticeSink(t *testing.T) *chatInteractionCoordinator {
	t.Helper()
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	// Mirror the production startup order: chat switches the console logger to
	// file-only before the TUI takes the terminal. Without this the structured
	// warning log itself would land on stderr in the test and the assertion below
	// would be testing the test harness rather than the notice routing.
	logPath := filepath.Join(t.TempDir(), "aicli.log")
	restoreLogger := suppressChatConsoleLogger(&config.Config{
		Log: logpkg.LogConfig{
			Level:         "info",
			Format:        "json",
			Output:        "file",
			FilePath:      logPath,
			EnableConsole: true,
		},
	}, nil)
	if restoreLogger != nil {
		t.Cleanup(restoreLogger)
	}

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)
	session.Surface = surface
	chatDiagnosticSink.Store(coordinator)
	return coordinator
}

// TestRemoteNoticesDoNotPaintOverTheStatusBar locks the live regression reported
// on 2026-10-22: the startup notice and the `#depth` warning were written straight
// to stderr, so in an interactive session they landed on the fixed bottom surface
// and replaced the status bar with half a line.
func TestRemoteNoticesDoNotPaintOverTheStatusBar(t *testing.T) {
	coordinator := withRemoteNoticeSink(t)

	target := remoteNoticeTestTarget(
		`target "127-0-0-1": profile "ops" is not applied to an http target`)

	_, stderr := captureStdoutStderr(t, func() {
		emitRemoteFlagNotice(target, true, false)
		coordinator.waitUIActorIdle()
	})

	require.Empty(t, strings.TrimSpace(stderr),
		"an interactive session must not write the notice to raw stderr")

	// Latest-wins on the single dynamic row, and the warning is the one that must
	// survive: the routine "added server" line is informational, the profile
	// warning is the actionable one. The row is width-clamped, so assert on the
	// prefix that survives the clamp rather than the full sentence.
	row := chatDynamicStatusRowText(coordinator, 160)
	require.Contains(t, row, "警告: remoteTargets", "row = %q", row)
	require.Contains(t, row, `profile "ops"`, "row = %q", row)
	require.NotContains(t, row, "\r", "the dynamic row must stay single-line")
	require.NotContains(t, row, "\n", "the dynamic row must stay single-line")
}

// TestRemoteNoticeWithoutWarningsStillReachesTheRow covers the routine path: a
// target with nothing to warn about still announces which server was added.
func TestRemoteNoticeWithoutWarningsStillReachesTheRow(t *testing.T) {
	coordinator := withRemoteNoticeSink(t)

	_, stderr := captureStdoutStderr(t, func() {
		emitRemoteFlagNotice(remoteNoticeTestTarget(), true, false)
		coordinator.waitUIActorIdle()
	})

	require.Empty(t, strings.TrimSpace(stderr))
	row := chatDynamicStatusRowText(coordinator, 160)
	require.Contains(t, row, "added server", "row = %q", row)
}

// TestRemoteNoticeFallsBackToStderrWithoutTUI keeps the non-interactive and
// pre-TUI paths working: there is no dynamic row to write to, so the text must
// still reach the user.
func TestRemoteNoticeFallsBackToStderrWithoutTUI(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	target := remoteNoticeTestTarget(
		`target "127-0-0-1": profile "ops" is not applied to an http target`)

	_, stderr := captureStdoutStderr(t, func() {
		emitRemoteFlagNotice(target, true, false)
	})

	require.Contains(t, stderr, "added server")
	require.Contains(t, stderr, "not applied to an http target")
}
