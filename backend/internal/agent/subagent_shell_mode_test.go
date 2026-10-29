package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveChildToolSurfaceShellFalseRemovesShell(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, child, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "no-shell",
		Goal:           "inspect the logs",
		ToolsWhitelist: []string{"view", "shell"},
		ShellDisabled:  true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"view"}, resolved.ToolsWhitelist)
	require.Equal(t, []string{"shell"}, resolved.ShellFilteredTools)
	require.True(t, child.ShellDisabled)
	require.False(t, child.AllowsDefinition("shell"))
}

func TestResolveChildToolSurfaceShellFalseCanLeaveNoTools(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, child, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "only-shell",
		Goal:           "inspect the logs",
		ToolsWhitelist: []string{"shell"},
		ShellDisabled:  true,
	})
	require.NoError(t, err)
	require.Empty(t, resolved.ToolsWhitelist)
	require.Equal(t, []string{"shell"}, resolved.ShellFilteredTools)
	require.True(t, child.ShellDisabled)
}

func TestResolveChildToolSurfaceShellFalseInheritsIntoInheritedSurface(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, child, err := resolveChildToolSurface(parent, SubagentTask{
		ID:            "inherit-no-shell",
		Goal:          "inspect",
		ShellDisabled: true,
	})
	require.NoError(t, err)
	require.True(t, resolved.ShellDisabled)
	require.True(t, child.ShellDisabled)
	require.False(t, child.AllowsDefinition("shell"))
}
