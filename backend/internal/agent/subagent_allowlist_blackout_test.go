package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// Regression for the silent allowlist blackout: a normal chat session
// synthesizes a policy whose allowlist holds the real runtime tool names
// (view/grep/ls/...). A read-only researcher subagent without an explicit
// tools_whitelist used to fall back to role defaults built from names the
// registry no longer serves (read_file, grep_repo, ...), so
// DeriveChildForTask intersected both sets down to nothing and the child was
// left with an empty allowlist that denied every tool, including runtime
// essentials such as search_tool.
func TestSubagentRoleDefaultsSurviveParentAllowlist(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))
	parent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{
		"append_write", "apply_patch", "background_task", "edit", "fetch",
		"glob", "grep", "ls", "search_tool", "shell", "spawn_subagents",
		"todos", "view", "write",
	}, false))

	prepared, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
		{ID: "target-features", Role: "researcher", Goal: "inspect", ReadOnly: true},
	})
	require.NoError(t, err)
	require.Len(t, prepared, 1)

	task := prepared[0]
	require.NotEmpty(t, task.ToolsWhitelist, "child must not end up with an empty allowlist")
	for _, want := range []string{"view", "grep", "ls", "glob", "shell"} {
		require.Contains(t, task.ToolsWhitelist, want)
	}

	childPolicy := runtimepolicy.NewToolExecutionPolicy(task.ToolsWhitelist, true)
	require.True(t, childPolicy.AllowlistEnabled)
	// Every defaulted tool must also be grantable under the read-only policy
	// the researcher role implies; a name the policy always rejects (for
	// example background_task) is dead weight in an allowlist.
	for _, name := range task.ToolsWhitelist {
		require.NoError(t, childPolicy.AllowTool(name), "child must be allowed to call %s", name)
	}
}

// A whitelist that the parent policy cannot grant must fail fast with the
// offending task and names instead of handing the child a 0-tool policy.
func TestSubagentRequestedToolsFailFastInsteadOfBlackout(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))
	parent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false))

	_, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
		{ID: "writer-draft", Role: "writer", Goal: "write", ToolsWhitelist: []string{"write_file", "edit_file"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "writer-draft")
	require.Contains(t, err.Error(), "write_file")
	require.Contains(t, err.Error(), "empty tool allowlist")
}

// The documented "disable every tool for this child" contract survives: an
// explicit empty whitelist is not treated as a blackout.
func TestSubagentExplicitEmptyWhitelistIsNotABlackout(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))
	parent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false))

	prepared, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
		{ID: "no-tools", Role: "researcher", Goal: "inspect", ToolsWhitelist: []string{}},
	})
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	require.NotNil(t, prepared[0].ToolsWhitelist)
	require.Empty(t, prepared[0].ToolsWhitelist)

	// The child prompt must say that it has no tools instead of staying silent,
	// otherwise the child keeps probing a surface it will never get.
	prompt := NewPromptBuilder().BuildSubagentPrompt(&Config{Name: "parent"}, prepared[0])
	require.Contains(t, prompt, "No tools are available for this task")
	require.NotContains(t, prompt, "Allowed tools:")
}

// The policy-level guard cannot see this case: when the parent session has no
// allowlist, the requested names are copied into the child policy verbatim, so a
// retired name such as read_file yields an allowlist that matches no executable
// tool instead of an empty one. The surface check must catch it and name the
// replacement.
func TestSubagentStaleToolVocabularyFailsFastWithoutParentAllowlist(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil)))
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))
	require.Nil(t, parent.GetToolExecutionPolicy(), "test must cover the unrestricted parent case")

	_, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
		{
			ID:             "reader-1",
			Role:           "researcher",
			Goal:           "inspect",
			ReadOnly:       true,
			ToolsWhitelist: []string{"read_file", "list_directory"},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reader-1")
	require.Contains(t, err.Error(), `"read_file" -> "view"`)
	require.Contains(t, err.Error(), `"list_directory" -> "ls"`)
	require.Equal(t, "tool_vocabulary", classifySubagentDeniedPolicy(err.Error()))

	// Registered names stay accepted for the same unrestricted parent, and the
	// role defaults are unaffected.
	prepared, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
		{ID: "reader-2", Role: "researcher", Goal: "inspect", ReadOnly: true, ToolsWhitelist: []string{"view"}},
		{ID: "reader-3", Role: "researcher", Goal: "inspect", ReadOnly: true},
	})
	require.NoError(t, err)
	require.Len(t, prepared, 2)
	require.Equal(t, []string{"view"}, prepared[0].ToolsWhitelist)
	require.NotEmpty(t, prepared[1].ToolsWhitelist)
}

// A broker or MCP server may legitimately expose tools named read_file or
// read_logs: the surface check must never hijack those names.
func TestSubagentMCPToolsSharingRetiredNamesSurvive(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, &MockCatalogMCPManager{})
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))

	prepared, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
		{
			ID:             "reader-1",
			Role:           "researcher",
			Goal:           "inspect",
			ReadOnly:       true,
			ToolsWhitelist: []string{"read_file", "read_logs"},
		},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"read_file", "read_logs"}, prepared[0].ToolsWhitelist)
}

// Unknown is not the same as "does not exist": when the live surface cannot be
// inspected (no manager, or a manager that has not been populated yet) the
// request must pass through untouched.
func TestSubagentUnknownToolSurfaceKeepsRequestedNames(t *testing.T) {
	for name, parent := range map[string]*Agent{
		"unpopulated manager": NewAgent(&Config{Name: "parent"}, &MockMCPManager{}),
		"no manager":          NewAgent(&Config{Name: "parent"}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))
			prepared, err := parent.GetSubagentScheduler().prepareTasks([]SubagentTask{
				{
					ID:             "reader-1",
					Role:           "researcher",
					Goal:           "inspect",
					ReadOnly:       true,
					ToolsWhitelist: []string{"read_file"},
				},
			})
			require.NoError(t, err)
			require.Equal(t, []string{"read_file"}, prepared[0].ToolsWhitelist)
		})
	}
}

// Both entry points must agree on the effective allowlist: the child factory
// used to advertise the raw request even when the parent policy narrowed it.
func TestResolveChildToolSurfaceNarrowsToParentAllowlist(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	parent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep", "write"}, false))

	task := SubagentTask{
		ID:             "writer-1",
		Role:           "writer",
		Goal:           "edit",
		ToolsWhitelist: []string{"write", "fetch"},
	}
	resolved, factoryPolicy, err := resolveChildToolSurface(parent, task)
	require.NoError(t, err)
	require.True(t, factoryPolicy.AllowlistEnabled)
	require.Equal(t, []string{"write"}, resolved.ToolsWhitelist)

	schedulerPolicy := parent.GetSubagentScheduler().childPolicy(task)
	require.Equal(t, schedulerPolicy.AllowedToolNames(), factoryPolicy.AllowedToolNames())
}
