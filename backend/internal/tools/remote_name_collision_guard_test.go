package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// TestRemoteToolNameCollisionIsRefused is the core guarantee.
//
// A remote-scoped server that publishes `write` (no prefix) must not appear in the
// tool list at all. Before this guard, the name was listed as remote (because
// ListTools visits MCP first) while execution silently used the local builtin —
// the model then wrote to the wrong machine with no error.
func TestRemoteToolNameCollisionIsRefused(t *testing.T) {
	probe := newScopedProbeManager("wsl", mcpconfig.ExecutionScopeRemote, "write")
	m := NewDefaultManager(probe)

	// Diagnostic: confirm the guard can see what it needs to see.
	status, statusErr := probe.GetMCPStatus("wsl")
	require.NoError(t, statusErr)
	require.Equal(t, mcpconfig.ExecutionScopeRemote, status.ExecutionScope,
		"precondition: the probe reports a remote scope")
	require.True(t, m.isRemoteScopedServer("wsl"),
		"precondition: the manager resolves the server as remote-scoped")
	_, hasLocalWrite := m.toolkit.Get("write")
	require.True(t, hasLocalWrite, "precondition: the local toolkit owns write")
	require.Error(t, m.validateRemoteToolNameCollision(probe.ListTools()[0]),
		"precondition: the guard itself refuses this tool")

	// `write` still appears, but it must be the LOCAL one. The point is that the
	// remote entry was refused, so there is exactly one entry and it carries no
	// MCP identity.
	var writes []ToolDescriptor
	for _, d := range m.ListTools() {
		if d.Name == "write" {
			writes = append(writes, d)
		}
	}
	require.Len(t, writes, 1, "exactly one write may be listed")
	require.NotContains(t, writes[0].Metadata, "mcp_name",
		"the surviving write must be the LOCAL builtin; the remote one is refused")

	collisions := m.RemoteNameCollisions()
	require.Len(t, collisions, 1)
	require.Equal(t, "wsl", collisions[0].MCPName)
	require.Equal(t, "write", collisions[0].ToolName)
	require.Contains(t, collisions[0].Reason, "collides with the local builtin")
	require.Contains(t, collisions[0].Reason, "toolPrefix",
		"the error must tell the operator how to fix it")
}

// TestLocalMCPMayShadowLocalTool keeps the guard scoped.
//
// A server that is NOT executionScope=remote has legitimate reasons to mirror a
// builtin name (the filesystem MCP is the canonical example). Refusing it would
// break working setups.
func TestLocalMCPMayShadowLocalTool(t *testing.T) {
	probe := newScopedProbeManager("filesystem", mcpconfig.ExecutionScopeLocal, "write")
	m := NewDefaultManager(probe)

	names := listingNames(m)
	require.Contains(t, names, "write", "a local MCP may still shadow a builtin")
	require.Empty(t, m.RemoteNameCollisions())
}

// TestRemoteToolWithPrefixIsAccepted proves the fix is not a blanket ban: the
// documented deployment (prefixed remote tools) keeps working.
func TestRemoteToolWithPrefixIsAccepted(t *testing.T) {
	probe := newScopedProbeManager("wsl", mcpconfig.ExecutionScopeRemote,
		"remote_wsl__write", "remote_wsl__view")
	m := NewDefaultManager(probe)

	names := listingNames(m)
	require.Contains(t, names, "remote_wsl__write")
	require.Contains(t, names, "remote_wsl__view")
	require.Contains(t, names, "write", "the local builtin is still there")
	require.Empty(t, m.RemoteNameCollisions())
}

// TestRemoteToolNameWithoutLocalPeerIsAccepted makes sure the guard only fires on
// an actual collision: a remote-only name is fine, prefixed or not.
func TestRemoteToolNameWithoutLocalPeerIsAccepted(t *testing.T) {
	probe := newScopedProbeManager("wsl", mcpconfig.ExecutionScopeRemote, "deploy_app")
	m := NewDefaultManager(probe)

	require.Contains(t, listingNames(m), "deploy_app")
	require.Empty(t, m.RemoteNameCollisions())
}

// TestCollisionCoversEveryLocalName is a table test over the local toolkit.
//
// Two distinct outcomes are correct, and conflating them would hide a real gap:
//
//   - Read family (ls/glob/grep/view and aliases): the listing path drops the
//     remote entry via shouldPreferLocalToolkit BEFORE the collision guard runs.
//     There is no hazard to report because the remote tool never becomes
//     reachable; it was outranked, not refused.
//   - Everything else: the remote entry would have claimed the name (the dangerous
//     case), so the guard must refuse it and record why.
func TestCollisionCoversEveryLocalName(t *testing.T) {
	// Only names the local TOOLKIT owns participate: `list_mcp_resources` is a
	// runtime meta tool appended by the manager, not a toolkit entry, so a remote
	// server using that name does not shadow anything the guard must protect.
	probeLocal := NewDefaultManager(nil)
	localNames := make([]string, 0)
	for _, d := range probeLocal.ListTools() {
		if _, owned := probeLocal.toolkit.Get(d.Name); !owned {
			continue
		}
		localNames = append(localNames, d.Name)
	}
	require.NotEmpty(t, localNames)

	for _, name := range localNames {
		t.Run(name, func(t *testing.T) {
			probe := newScopedProbeManager("wsl", mcpconfig.ExecutionScopeRemote, name)
			m := NewDefaultManager(probe)

			require.True(t, m.isRemoteScopedServer("wsl"),
				"precondition: %q server must resolve as remote-scoped", name)
			_, localHas := m.toolkit.Get(name)
			require.True(t, localHas,
				"precondition: the local toolkit must own %q for this case to be meaningful", name)

			// The guard runs inside ListTools, so the listing must happen first.
			listed := m.ListTools()

			if _, locallyPreferred := localToolkitPriorityTools[name]; locallyPreferred {
				// Outranked before the guard: nothing to refuse, nothing recorded.
				require.Empty(t, m.RemoteNameCollisions(),
					"%q is locally preferred, so no collision record is expected", name)
			} else {
				collisions := m.RemoteNameCollisions()
				require.Len(t, collisions, 1,
					"remote %q must be refused because the local toolkit owns that name", name)
				require.Equal(t, name, collisions[0].ToolName)
			}

			// Either way, the surviving entry with that name must be the local one.
			for _, d := range listed {
				if d.Name != name {
					continue
				}
				require.NotContains(t, d.Metadata, "mcp_name",
					"the listed %q must be local after the remote one was refused", name)
			}
		})
	}
}

// TestRemoteOnlyManagerSkipsCollisionGuard documents the interaction with
// exclusive mode: with no local toolkit there is nothing to collide with, so the
// guard is a no-op and remote names are used verbatim.
func TestRemoteOnlyManagerSkipsCollisionGuard(t *testing.T) {
	probe := newScopedProbeManager("wsl", mcpconfig.ExecutionScopeRemote, "write")
	m := NewRemoteOnlyManager(probe)

	require.Equal(t, []string{"write"}, listingNames(m),
		"in exclusive mode the remote write is the only write")
	require.Empty(t, m.RemoteNameCollisions())
}

// scopedProbeManager publishes remote tools with an explicit execution scope.
type scopedProbeManager struct {
	*shadowProbeManager
	scope mcpconfig.ExecutionScope
}

func newScopedProbeManager(mcpName string, scope mcpconfig.ExecutionScope, tools ...string) *scopedProbeManager {
	base := newShadowProbeManager(tools...)
	base.mcpName = mcpName
	return &scopedProbeManager{shadowProbeManager: base, scope: scope}
}

// GetMCPStatus must have a POINTER receiver.
//
// shadowProbeManager.GetMCPStatus is a pointer method; a value receiver here would
// declare a sibling method on scopedProbeManager that the embedded *pointer* does
// not shadow for interface satisfaction, and the manager would silently see the
// base (scope-less) implementation instead of this one.
func (m *scopedProbeManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{
		Name:           name,
		ExecutionScope: m.scope,
	}, nil
}

func (m *scopedProbeManager) Start(context.Context) error { return nil }
