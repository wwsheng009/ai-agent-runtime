package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellExecutorCodexCommandAndContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'hook context'\n"), 0o755))

	manager := NewManager([]HookConfig{{
		ID:    "ctx",
		Event: EventSessionStart,
		Exec:  ExecConfig{Type: "shell", Command: script},
	}})
	decision, err := manager.Dispatch(context.Background(), EventSessionStart, map[string]interface{}{"session_id": "s1"})
	require.NoError(t, err)
	require.Equal(t, DecisionEnrich, decision.Action)
	require.Equal(t, "hook context", decision.ExtraContext["additional_context"])
}
