package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// loopInjectedToolNames are exposed by the ReAct loop itself rather than by the
// built-in tool registry, so they are legitimately absent from ListTools().
var loopInjectedToolNames = map[string]bool{
	"search_tool": true,
}

// DefaultToolsForRole must only emit tool names the runtime registry actually
// serves. A stale name is invisible to the child and, under a parent allowlist,
// intersects the child allowlist down to nothing.
func TestRoleDefaultsUseRegisteredToolNames(t *testing.T) {
	registered := make(map[string]bool)
	for _, descriptor := range runtimetools.NewDefaultManager(nil).ListTools() {
		registered[descriptor.Name] = true
	}
	require.NotEmpty(t, registered, "runtime tool registry must expose built-in tools")

	var unknown []string
	for _, role := range []string{"researcher", "tester", "verifier", "writer", "implementer", "explorer"} {
		names := DefaultToolsForRole(role)
		require.NotEmpty(t, names, "role %s must expose default tools", role)
		for _, name := range names {
			if !registered[name] && !loopInjectedToolNames[name] {
				unknown = append(unknown, role+":"+name)
			}
		}
	}
	sort.Strings(unknown)
	require.Empty(t, unknown,
		"role defaults must use registered runtime tool names, unregistered: %s", strings.Join(unknown, ", "))
}

// Guards against reintroducing the draft vocabulary that caused the blackout.
func TestRoleDefaultsAvoidLegacyToolVocabulary(t *testing.T) {
	legacy := []string{
		"read_file", "read_files", "read_logs", "grep_repo", "search_repo",
		"run_tests", "git_log", "git_status", "git_diff", "write_file",
		"create_file", "edit_file",
	}
	for _, role := range []string{"researcher", "tester", "verifier", "writer"} {
		for _, name := range DefaultToolsForRole(role) {
			require.NotContains(t, legacy, name, "role %s must not advertise the legacy tool %q", role, name)
		}
	}
}

func TestNormalizeToolWhitelistContract(t *testing.T) {
	require.Nil(t, normalizeToolWhitelist(nil))

	empty := normalizeToolWhitelist([]string{})
	require.NotNil(t, empty)
	require.Empty(t, empty)

	require.Equal(t, []string{"view", "grep"}, normalizeToolWhitelist([]string{" view ", "grep", "view", "  "}))

	// Names are preserved verbatim: a broker/MCP tool name must never be
	// silently rewritten into a built-in tool name.
	require.Equal(t, []string{"read_file"}, normalizeToolWhitelist([]string{"read_file"}))
}

func TestValidateChildToolAllowlistRejectsBlackout(t *testing.T) {
	blackout := NewToolExecutionPolicy([]string{}, false)
	require.True(t, blackout.AllowlistEnabled)

	err := validateChildToolAllowlist("task-1", "tools_whitelist", []string{"write_file"}, blackout)
	require.Error(t, err)
	require.Contains(t, err.Error(), "task-1")
	require.Contains(t, err.Error(), "write_file")

	// Explicit empty request and an inheriting (nil) request stay allowed.
	require.NoError(t, validateChildToolAllowlist("task-2", "tools_whitelist", []string{}, blackout))
	require.NoError(t, validateChildToolAllowlist("task-3", "role defaults", nil, blackout))

	// A child that keeps at least one tool, or that has no allowlist at all, is fine.
	require.NoError(t, validateChildToolAllowlist("task-4", "tools_whitelist", []string{"view"}, NewToolExecutionPolicy([]string{"view"}, false)))
	require.NoError(t, validateChildToolAllowlist("task-5", "tools_whitelist", []string{"write_file"}, NewToolExecutionPolicy(nil, false)))
}
