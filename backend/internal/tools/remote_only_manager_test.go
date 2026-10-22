package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRemoteOnlyManagerExposesOnlyRemoteTools is the core guarantee of
// `aicli chat --remote`: the local toolkit contributes nothing, so the model can
// only touch the remote host.
func TestRemoteOnlyManagerExposesOnlyRemoteTools(t *testing.T) {
	m := NewRemoteOnlyManager(newShadowManager("wsl", "remote_wsl__view"))

	names := listingNames(m)
	require.Equal(t, []string{"remote_wsl__view"}, names,
		"only the remote tool may appear; no local view/write/shell/ls")
}

// TestRemoteOnlyManagerHasNoLocalFallback proves the local-file hazard is gone by
// construction: even an unprefixed remote `write` cannot shadow anything, because
// there is no local `write` to shadow.
func TestRemoteOnlyManagerHasNoLocalFallback(t *testing.T) {
	m := NewRemoteOnlyManager(newShadowManager("wsl", "write"))

	names := listingNames(m)
	require.Equal(t, []string{"write"}, names)

	desc := m.ListTools()[0]
	require.Equal(t, "wsl", desc.Metadata["mcp_name"],
		"the only write available is the remote one")
}

// TestRemoteOnlyManagerRejectsLocalOnlyTools locks that tools the remote host does
// not serve simply do not exist here. A model asking for `shell` must get a clear
// absence rather than a silent local shell.
func TestRemoteOnlyManagerRejectsLocalOnlyTools(t *testing.T) {
	m := NewRemoteOnlyManager(newShadowManager("wsl", "remote_wsl__view"))

	names := listingNames(m)
	require.NotContains(t, names, "shell")
	require.NotContains(t, names, "write")
	require.NotContains(t, names, "apply_patch")

	// Executing a local-only tool must fail: no local toolkit exists to serve it,
	// and the remote host does not advertise it either.
	_, err := m.Execute(context.Background(), "shell", map[string]interface{}{"command": "id"})
	require.Error(t, err, "a local-only tool must not execute in remote-only mode")
}

// TestRemoteOnlyManagerWithoutMCPIsEmpty makes the degenerate case explicit: no
// MCP runtime means no tools at all, never a fallback to the local surface.
func TestRemoteOnlyManagerWithoutMCPIsEmpty(t *testing.T) {
	m := NewRemoteOnlyManager(nil)
	require.Empty(t, listingNames(m))
	require.False(t, m.MCPAvailable())
}
