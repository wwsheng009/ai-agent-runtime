package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// TestReadFamilyStillShortCircuitsLocally pins the behaviour that must NOT change.
//
// The fix narrowed the local short-circuit to the read family. A future edit that
// widens or removes it must fail here, because "the local ls/grep/view still win"
// is a deliberate product decision: those four tools are the ones a model uses to
// orient itself, and letting an arbitrary MCP server hijack them was rejected.
func TestReadFamilyStillShortCircuitsLocally(t *testing.T) {
	for _, name := range []string{"view", "ls", "glob", "grep"} {
		t.Run(name, func(t *testing.T) {
			// A LOCAL-scoped server publishing the same name: allowed by the
			// collision guard, so this isolates the dispatch decision.
			probe := newScopedProbeManager("filesystem", mcpconfig.ExecutionScopeLocal, name)
			m := NewDefaultManager(probe)

			_, _, _ = m.ExecuteWithMeta(context.Background(), name, map[string]interface{}{
				"file_path": "/definitely/not/a/local/file",
				"path":      "/definitely/not/a/local/file",
				"pattern":   "*",
			})

			require.Empty(t, probe.executed,
				"%q must still be served by the LOCAL toolkit, not the MCP server", name)
		})
	}
}

// TestNonReadMcpOwnedNameDispatchesToMCP is the mirror assertion for every other
// name: if the MCP server owns it and the local toolkit does not win the
// short-circuit, the call must reach the server.
func TestNonReadMcpOwnedNameDispatchesToMCP(t *testing.T) {
	for _, name := range []string{"write", "edit", "apply_patch", "append_write", "shell"} {
		t.Run(name, func(t *testing.T) {
			probe := newScopedProbeManager("filesystem", mcpconfig.ExecutionScopeLocal, name)
			m := NewDefaultManager(probe)

			_, _, _ = m.ExecuteWithMeta(context.Background(), name, map[string]interface{}{
				"file_path": "x.txt", "content": "c", "old_string": "a",
				"new_string": "b", "patch": "p", "command": "echo x",
			})

			require.Equal(t, []string{name}, probe.executed,
				"%q is listed as MCP-owned and must be dispatched there", name)
		})
	}
}

// TestResolveToolSourceMatchesDispatch keeps the audit label honest: whatever
// executeWithMeta routes to, resolveToolSource must report the same origin.
func TestResolveToolSourceMatchesDispatch(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		wantSrc  string
		executed bool
	}{
		// read family -> local
		{name: "read stays local", tool: "view", wantSrc: "toolkit", executed: false},
		// non-read -> MCP
		{name: "write goes to mcp", tool: "write", wantSrc: "mcp", executed: true},
		{name: "shell goes to mcp", tool: "shell", wantSrc: "mcp", executed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := newScopedProbeManager("filesystem", mcpconfig.ExecutionScopeLocal, tc.tool)
			m := NewDefaultManager(probe)

			require.Equal(t, tc.wantSrc, m.resolveToolSource(tc.tool),
				"source label must match where the call is routed")

			_, _, _ = m.ExecuteWithMeta(context.Background(), tc.tool, map[string]interface{}{
				"file_path": "/definitely/not/a/local/file", "content": "c",
				"old_string": "a", "new_string": "b", "patch": "p", "command": "echo x",
			})
			if tc.executed {
				require.Len(t, probe.executed, 1, "the MCP server must have been called")
			} else {
				require.Empty(t, probe.executed, "the MCP server must not have been called")
			}
		})
	}
}

