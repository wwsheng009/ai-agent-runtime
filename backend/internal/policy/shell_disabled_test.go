package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellDisabledHidesAndDeniesShell(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	policy.SetShellDisabled(true)

	require.False(t, policy.AllowsDefinition("shell"))
	require.True(t, policy.AllowsDefinition("view"))
	err := policy.AllowToolCallWithContext(context.Background(), *shellToolInfo(),
		map[string]interface{}{"command": "git status"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "shell is disabled")
}

func TestDeriveChildForTaskWithShellDisabledDropsExecShell(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)
	parent.SetCapabilityScope([]Capability{CapReadOnly, CapExecShell, CapNetwork})

	child := parent.DeriveChildForTaskWithShellDisabled(nil, false, "researcher", nil, true)
	require.True(t, child.ShellDisabled)
	require.True(t, child.CapabilityScopeEnabled)
	require.NotContains(t, child.AllowedCapabilities, CapExecShell)
	require.Contains(t, child.AllowedCapabilities, CapNetwork)
}

func TestDeriveChildForTaskWithShellDisabledWithoutParentScope(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)

	child := parent.DeriveChildForTaskWithShellDisabled([]string{"view", "shell"}, false, "researcher", nil, true)
	require.True(t, child.ShellDisabled)
	require.True(t, child.CapabilityScopeEnabled)
	require.NotContains(t, child.AllowedCapabilities, CapExecShell)
	require.False(t, child.AllowsDefinition("shell"))
}

func TestDeriveChildForTaskCannotReEnableParentDisabledShell(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)
	parent.SetShellDisabled(true)

	child := parent.DeriveChildForTaskWithShellDisabled(nil, false, "researcher", nil, false)
	require.True(t, child.ShellDisabled, "a child must not re-enable a shell its parent disabled")
}
