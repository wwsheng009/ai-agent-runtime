package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

func TestRenderAgentDefsListIncludesBuiltins(t *testing.T) {
	out := renderAgentDefsList(nil)
	require.Contains(t, out, "BUILTIN")
	require.Contains(t, out, "explore")
	require.Contains(t, out, "plan")
}

func TestRenderAgentDefsShowBuiltin(t *testing.T) {
	out := renderAgentDefsShow(nil, "explore")
	require.Contains(t, out, "Agent definition: explore")
	require.Contains(t, out, "read_only=true")
	require.Contains(t, out, "builtin:explore")
}

func TestRenderAgentDefsShowUnknownListsCandidates(t *testing.T) {
	out := renderAgentDefsShow(nil, "definitely-not-a-role")
	require.Contains(t, out, "not found")
	require.Contains(t, out, "explore")
}

func TestRenderAgentDefsLintReportsBuiltinToolsHint(t *testing.T) {
	out := renderAgentDefsLint(nil)
	require.Contains(t, out, "lint")
	// general omits tools -> at least the fail-open info line must be present.
	require.Contains(t, out, "tools 未声明")
}

func TestApplyAgentdefSkillsToResolved(t *testing.T) {
	resolved := &profilesys.ResolvedAgent{}
	applyAgentdefSkillsToResolved(resolved, &agentdef.Binding{SkillAllowlist: []string{"docs"}})
	require.Equal(t, []string{"docs"}, resolved.Skills.Allowlist)

	untouched := &profilesys.ResolvedAgent{}
	applyAgentdefSkillsToResolved(untouched, &agentdef.Binding{})
	require.Empty(t, untouched.Skills.Allowlist)
	applyAgentdefSkillsToResolved(nil, &agentdef.Binding{SkillAllowlist: []string{"docs"}})
	require.Empty(t, untouched.Skills.Allowlist)
}
