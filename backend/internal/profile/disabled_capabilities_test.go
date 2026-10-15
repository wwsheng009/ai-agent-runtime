package profile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeToolPoliciesUnionsDisabledCapabilitiesAcrossLayers(t *testing.T) {
	resolved := MergeToolPolicies(
		ToolPolicySpec{DisabledCapabilities: []string{"Network"}},
		ToolPolicySpec{DisabledCapabilities: []string{"network", "background_task"}},
	)
	require.Equal(t, []string{"background_task", "network"}, resolved.DisabledCapabilities)
}

func TestMergeToolPoliciesWithoutDisabledCapabilitiesStaysNil(t *testing.T) {
	resolved := MergeToolPolicies(
		ToolPolicySpec{Allowlist: []string{"view"}},
		ToolPolicySpec{Denylist: []string{"shell"}},
	)
	require.Empty(t, resolved.DisabledCapabilities)
}

func TestValidateProfileSpecDisabledCapabilities(t *testing.T) {
	unknown := ProfileSpec{
		Tools: ToolPolicySpec{DisabledCapabilities: []string{"nonsense"}},
	}
	issues := ValidateProfileSpec(&unknown)
	require.NotEmpty(t, issues)
	found := false
	for _, issue := range issues {
		if issue.Path == "tools.disabled_capabilities" && issue.Severity == ProfileSpecIssueError {
			require.Contains(t, issue.Message, "unknown capability domain")
			found = true
		}
	}
	require.True(t, found)

	valid := ProfileSpec{
		// Case-normalized duplicates are legal but produce a warning; the
		// merged result still collapses to one domain.
		Tools: ToolPolicySpec{DisabledCapabilities: []string{"network", "NETWORK"}},
	}
	validIssues := ValidateProfileSpec(&valid)
	for _, issue := range validIssues {
		require.NotEqual(t, ProfileSpecIssueError, issue.Severity,
			"case variants of a valid domain must not error: %v", issue)
	}

	agentScoped := ProfileSpec{
		Agents: map[string]AgentSpec{
			"explore": {Tools: ToolPolicySpec{DisabledCapabilities: []string{"bad-domain"}}},
		},
	}
	issues = ValidateProfileSpec(&agentScoped)
	found = false
	for _, issue := range issues {
		if issue.Path == "agents.explore.tools.disabled_capabilities" {
			found = true
		}
	}
	require.True(t, found)
}
