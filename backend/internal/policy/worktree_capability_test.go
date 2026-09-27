package policy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 2026-09-27 incident: a child landed its own worktree mid-turn and wiped
// unrelated uncommitted main-tree changes. Besides the broker-side parent-only
// guard, a capability-scoped policy must require write_fs + agent_management
// explicitly: inheriting the requirement from the tool name let a scope without
// agent_management (or a read-only scope that already holds agent_management)
// reach the worktree tools.
func TestWorktreeToolsRequireWriteFSAndAgentManagement(t *testing.T) {
	for _, name := range []string{"apply_agent_worktree", "discard_agent_worktree"} {
		caps, ok := controlPlaneToolCapabilities(normalizeToolName(name))
		require.True(t, ok, "%s must have a control-plane capability row", name)
		require.ElementsMatch(t, []Capability{CapWriteFS, CapAgentManagement}, caps)

		// A read-only child scope holds agent_management (for spawn/collab) but
		// never write_fs: it must not be able to land or drop a worktree.
		readOnlyChild := NewCapabilityScopedToolExecutionPolicy(nil, ReadOnlyChildCapabilities())
		if err := readOnlyChild.AllowTool(name); err == nil {
			t.Errorf("expected %s to stay out of a read-only child scope", name)
		}

		// The taxonomy-first resolution path must agree with the name table.
		require.ElementsMatch(t, caps,
			capabilitiesFromTaxonomy(ToolTaxonomy{Name: name, Kind: types.ToolKindControl}),
			"%s taxonomy-first resolution disagrees with the control-plane table", name)
	}
}
