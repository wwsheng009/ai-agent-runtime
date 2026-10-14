package commands

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// TestBuildLocalChatBackgroundManagerForwardsInteractiveCap pins the
// 2026-10-14 wiring: background.maxInteractiveSessions set in the runtime
// config must reach the background manager (it used to be dropped, leaving the
// cap hard-coded at the default 64). A cap of 1 makes the forwarding observable
// through the manager's public "too many interactive sessions" contract.
func TestBuildLocalChatBackgroundManagerForwardsInteractiveCap(t *testing.T) {
	if runtime.GOOS == "windows" && runtimeexecutor.DefaultUserShell().Type == runtimeexecutor.ShellTypeCmd {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	command := `$input | ForEach-Object { Write-Output ("echo:" + $_) }`
	if runtime.GOOS != "windows" {
		command = `while IFS= read -r line; do printf 'echo:%s\n' "$line"; done`
	}

	dir := t.TempDir()
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.Background.StorePath = filepath.Join(dir, "jobs.db")
	cfg.Background.MaxInteractiveSessions = 1

	manager := buildLocalChatBackgroundManager(cfg, nil)
	require.NotNil(t, manager)
	t.Cleanup(func() { _ = manager.Close() })

	first, err := manager.OpenTerminal(context.Background(), "session-cap-a", background.TerminalOpenArgs{Command: command})
	require.NoError(t, err)
	require.NotNil(t, first)

	_, err = manager.OpenTerminal(context.Background(), "session-cap-b", background.TerminalOpenArgs{Command: command})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too many interactive sessions (1)",
		"the configured cap must be forwarded to the background manager")
}
