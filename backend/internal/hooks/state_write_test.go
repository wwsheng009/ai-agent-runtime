package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetHookEnabledPersistsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo ok"}]}]}
}`), 0o600))

	require.NoError(t, SetHookEnabled(path, "handler-1", false))
	states, err := LoadHookStatesFile(path)
	require.NoError(t, err)
	require.NotNil(t, states["handler-1"].Enabled)
	require.False(t, *states["handler-1"].Enabled)

	// The hooks declaration must survive the state rewrite.
	hooks, _, err := LoadDeclarativeFile(path, ScopeProject, "")
	require.NoError(t, err)
	require.Len(t, hooks, 1)
}

func TestSetHookEnabledWritesYAMLAndPreservesComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`# keep this comment
hooks:
  Stop:
    - hooks:
        - type: command
          command: echo ok
`), 0o600))
	require.NoError(t, SetHookEnabled(path, "handler-1", false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# keep this comment")

	states, err := LoadHookStatesFile(path)
	require.NoError(t, err)
	require.NotNil(t, states["handler-1"].Enabled)
	require.False(t, *states["handler-1"].Enabled)
}
