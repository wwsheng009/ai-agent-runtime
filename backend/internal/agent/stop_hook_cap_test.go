package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
)

// A Stop hook that always blocks must not loop forever. Codex leaves this to the
// hook via stop_hook_active, which is easy to get wrong: an unconditional
// `decision:block` would otherwise keep forcing steps until maxSteps runs out,
// and with maxSteps=0 that is effectively forever.
func TestStopHookContinuationIsCapped(t *testing.T) {
	require.Equal(t, 5, maxStopHookContinues)

	agent := &Agent{}
	// Use a real script file: string commands run through cmd.exe on Windows,
	// which eats the JSON double quotes when they are inlined.
	command := writeAlwaysBlockScript(t)
	manager := runtimehooks.NewManager([]runtimehooks.HookConfig{{
		ID:    "always-block",
		Event: runtimehooks.EventStop,
		Exec: runtimehooks.ExecConfig{
			Type:    "shell",
			Command: command,
		},
	}})
	// SetHookManager also installs status/async observers that need a live event
	// bus, so assign the field directly: this test only exercises the stop gate.
	agent.hookManager = manager

	loop := &ReActLoop{agent: agent, config: &LoopReActConfig{MaxSteps: 0}}
	result := &Result{Success: true}

	// Under the cap each block continues the run.
	for i := 0; i < maxStopHookContinues; i++ {
		continueRun, _, stopNow := loop.dispatchStopHook(
			context.Background(), "s1", "t1", i+1, result, "no_tool_calls",
		)
		require.Truef(t, continueRun, "call %d should continue", i+1)
		require.False(t, stopNow)
		require.Equal(t, i+1, loop.stopHookContinues)
	}

	// At the cap the hook must no longer be allowed to extend the run.
	continueRun, message, stopNow := loop.dispatchStopHook(
		context.Background(), "s1", "t1", maxStopHookContinues+1, result, "no_tool_calls",
	)
	require.False(t, continueRun, "the cap must stop further continuations")
	require.False(t, stopNow, "hitting the cap is a soft stop, not a hook stop")
	require.Contains(t, message, "continuation limit reached")
	require.Equal(t, maxStopHookContinues, loop.stopHookContinues,
		"the counter must not advance past the cap")
}

// writeAlwaysBlockScript creates a hook command that always emits a block
// decision, using a real interpreter per platform.
func writeAlwaysBlockScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "always-block.cmd")
		script := "@echo off\r\necho {\"decision\":\"block\",\"reason\":\"keep going\"}\r\n"
		require.NoError(t, os.WriteFile(path, []byte(script), 0o644))
		return path
	}
	path := filepath.Join(dir, "always-block.sh")
	script := "#!/bin/sh\nprintf '{\"decision\":\"block\",\"reason\":\"keep going\"}'\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}
