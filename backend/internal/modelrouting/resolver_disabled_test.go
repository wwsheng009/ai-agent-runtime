package modelrouting

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// Routing-disabled resolution used to drop task.Provider silently while
// honoring task.Model. It must now either honor the provider under the same
// opt-in gate as the enabled path or report the denial as a warning.
func TestResolveDisabledReportsDroppedProvider(t *testing.T) {
	resolver := Resolver{Config: &agentconfig.AICLISubagentRoutingConfig{}}
	decision, err := resolver.Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Provider: "other"},
	)
	require.NoError(t, err)
	require.Equal(t, "parent", decision.Provider)
	require.Contains(t, decision.Warnings, "explicit_provider_override_denied")
}

func TestResolveDisabledHonorsAllowedProviderOverride(t *testing.T) {
	resolver := Resolver{Config: &agentconfig.AICLISubagentRoutingConfig{
		AllowExplicitProviderOverride: true,
	}}
	decision, err := resolver.Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Provider: "other"},
	)
	require.NoError(t, err)
	require.Equal(t, "other", decision.Provider)
	require.Equal(t, SourceExplicitOverride, decision.Source)
}
