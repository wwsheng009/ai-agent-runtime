package modelrouting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type testCatalog struct {
	providers     map[string]string
	defaultModels map[string]string
	models        map[string]bool
	efforts       map[string]map[string]bool
}

func (c testCatalog) ResolveProviderName(name string) string {
	if c.providers == nil {
		return name
	}
	return c.providers[name]
}

func (c testCatalog) DefaultModel(provider string) string {
	if c.defaultModels == nil {
		return ""
	}
	return c.defaultModels[provider]
}

func (c testCatalog) SupportsModel(provider, model string) (bool, bool) {
	if c.models == nil {
		return false, false
	}
	supported, ok := c.models[provider+"/"+model]
	return supported, ok
}

func (c testCatalog) SupportsReasoningEffort(provider, model, effort string) (bool, bool) {
	if c.efforts == nil {
		return false, false
	}
	key := provider + "/" + model
	allowed, ok := c.efforts[key]
	if !ok {
		return false, false
	}
	return allowed[effort], true
}

func (c testCatalog) SupportedReasoningEfforts(provider, model string) ([]string, bool) {
	if c.efforts == nil {
		return nil, false
	}
	key := provider + "/" + model
	allowed, ok := c.efforts[key]
	if !ok {
		return nil, false
	}
	efforts := make([]string, 0, len(allowed))
	for effort, supported := range allowed {
		if supported {
			efforts = append(efforts, effort)
		}
	}
	return efforts, true
}

func TestNormalizeDifficultyAliases(t *testing.T) {
	cases := map[string]string{
		"simple":        DifficultyEasy,
		"LOW":           DifficultyEasy,
		"medium":        DifficultyNormal,
		"default":       DifficultyNormal,
		"complex":       DifficultyHard,
		"difficult":     DifficultyHard,
		"critical":      DifficultyExpert,
		"very-hard":     DifficultyExpert,
		"architectural": DifficultyExpert,
	}
	for input, expected := range cases {
		got, ok := NormalizeDifficulty(input)
		require.True(t, ok, "expected %q to normalize", input)
		assert.Equal(t, expected, got)
	}
	_, ok := NormalizeDifficulty("extreme")
	assert.False(t, ok)
}

func TestResolver_DisabledPreservesLegacyModelOverride(t *testing.T) {
	resolver := Resolver{}
	decision, err := resolver.Resolve(ParentDefaults{
		Provider:        "parent-provider",
		Model:           "parent-model",
		ReasoningEffort: "medium",
		MaxTokens:       2048,
	}, TaskHint{
		Model:           "child-model",
		Provider:        "ignored-provider",
		ReasoningEffort: "high",
		BudgetTokens:    1024,
	})
	require.NoError(t, err)
	assert.Equal(t, SourceDisabled, decision.Source)
	assert.Equal(t, "parent-provider", decision.Provider)
	assert.Equal(t, "child-model", decision.Model)
	assert.Equal(t, "medium", decision.ReasoningEffort)
	assert.Equal(t, 1024, decision.MaxTokens)
}

func TestResolver_PreservesTaskWarnings(t *testing.T) {
	resolver := Resolver{}
	decision, err := resolver.Resolve(ParentDefaults{
		Provider: "parent-provider",
		Model:    "parent-model",
	}, TaskHint{
		Warnings: []string{"thinking_effort_alias_used"},
	})
	require.NoError(t, err)
	assert.Contains(t, decision.Warnings, "thinking_effort_alias_used")
}

func TestResolver_DisabledIgnoresInvalidRoutingConfig(t *testing.T) {
	disabled := false
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &disabled,
		DefaultDifficulty: "not-a-difficulty",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"impossible": {},
		},
	}
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{
		Provider: "parent-provider",
		Model:    "parent-model",
	}, TaskHint{Model: "child-model"})
	require.NoError(t, err)
	assert.Equal(t, SourceDisabled, decision.Source)
	assert.Equal(t, "child-model", decision.Model)
}

func TestResolver_EnabledUsesDifficultyRouteAndRoleOverride(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: "normal",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {
				Provider:        "remote",
				Model:           "strong",
				ReasoningEffort: "high",
				MaxTokens:       12000,
			},
		},
		Roles: map[string]map[string]agentconfig.AICLISubagentRouteProfile{
			"verifier": {
				"hard": {
					Provider:        "verify",
					Model:           "verify-model",
					ReasoningEffort: "medium",
				},
			},
		},
	}
	resolver := Resolver{Config: cfg}
	decision, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Role:       "verifier",
		Difficulty: "hard",
	})
	require.NoError(t, err)
	assert.Equal(t, SourceRoleOverride, decision.Source)
	assert.Equal(t, "hard", decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.Equal(t, "verify", decision.Provider)
	assert.Equal(t, "verify-model", decision.Model)
	assert.Equal(t, "medium", decision.ReasoningEffort)
}

func TestResolver_ExplicitModelDeniedWhenRoutingEnabled(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "routed"},
		},
	}
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty: "normal",
		Model:      "expensive",
	})
	require.NoError(t, err)
	assert.Equal(t, "local", decision.Provider)
	assert.Equal(t, "routed", decision.Model)
	assert.Contains(t, decision.Warnings, "explicit_model_override_denied")
}

func TestResolver_ExplicitOverridesAllowedByAllowlist(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                       &enabled,
		AllowExplicitProviderOverride: true,
		AllowExplicitModelOverride:    true,
		AllowedProviderOverrides:      []string{"resolved-remote"},
		AllowedModelOverrides:         []string{"allowed-model"},
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "routed"},
		},
	}
	decision, err := (Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{
				"remote-alias":    "resolved-remote",
				"resolved-remote": "resolved-remote",
				"local":           "local",
			},
		},
	}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty: "normal",
		Provider:   "remote-alias",
		Model:      "allowed-model",
	})
	require.NoError(t, err)
	assert.Equal(t, SourceExplicitOverride, decision.Source)
	assert.Equal(t, "resolved-remote", decision.Provider)
	assert.Equal(t, "allowed-model", decision.Model)
}

func TestResolver_ExplicitOverridesRejectedByAllowlist(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                       &enabled,
		AllowExplicitProviderOverride: true,
		AllowExplicitModelOverride:    true,
		AllowedProviderOverrides:      []string{"safe-provider"},
		AllowedModelOverrides:         []string{"safe-model"},
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "routed"},
		},
	}
	decision, err := (Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{
				"local":           "local",
				"unsafe-provider": "unsafe-provider",
			},
		},
	}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty: "normal",
		Provider:   "unsafe-provider",
		Model:      "unsafe-model",
	})
	require.NoError(t, err)
	assert.Equal(t, "local", decision.Provider)
	assert.Equal(t, "routed", decision.Model)
	assert.Contains(t, decision.Warnings, "explicit_provider_override_not_allowed")
	assert.Contains(t, decision.Warnings, "explicit_model_override_not_allowed")
}

func TestResolver_ExplicitReasoningDeniedUnlessAllowed(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "routed", ReasoningEffort: "low"},
		},
	}
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty:      "normal",
		ReasoningEffort: "high",
	})
	require.NoError(t, err)
	assert.Equal(t, "low", decision.ReasoningEffort)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Contains(t, decision.Warnings, "explicit_reasoning_override_denied")
}

func TestResolver_ExplicitReasoningAllowedByPolicy(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                        &enabled,
		AllowExplicitReasoningOverride: true,
		ValidateModelCapabilities:      boolPtr(false),
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "routed", ReasoningEffort: "low"},
		},
	}
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty:      "normal",
		ReasoningEffort: "high",
	})
	require.NoError(t, err)
	assert.Equal(t, "high", decision.ReasoningEffort)
	assert.Equal(t, SourceExplicitOverride, decision.Source)
}

func TestResolver_MissingDifficultyDefaultsAndPromotesRiskKeywords(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: "easy",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote", Model: "strong"},
		},
	}
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Goal: "Analyze provider protocol migration risks.",
	})
	require.NoError(t, err)
	assert.Equal(t, "hard", decision.Difficulty)
	assert.Equal(t, "inferred", decision.DifficultySource)
	assert.Equal(t, "remote", decision.Provider)
	assert.Equal(t, "strong", decision.Model)
	assert.Contains(t, decision.Warnings, "difficulty_missing_defaulted")
	assert.Contains(t, decision.Warnings, "difficulty_promoted_by_heuristic")
}

func TestResolver_InvalidDifficultyPermissiveFallsBackToDefault(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: "normal",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "standard"},
		},
	}
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty: "复杂",
	})
	require.NoError(t, err)
	assert.Equal(t, "normal", decision.Difficulty)
	assert.Equal(t, "default", decision.DifficultySource)
	assert.Equal(t, "local", decision.Provider)
	assert.Equal(t, "standard", decision.Model)
	assert.Contains(t, decision.Warnings, "difficulty_invalid_defaulted")
}

func TestResolver_InvalidDifficultyStrictReturnsError(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		CompatibilityMode: CompatibilityStrict,
		DefaultDifficulty: "normal",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"normal": {Provider: "local", Model: "standard"},
		},
	}
	_, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{
		Difficulty: "复杂",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid subagent difficulty "复杂"`)
}

func TestResolver_UnresolvedProviderFallsBackToParent(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "missing-provider", Model: "strong"},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{
				"parent": "parent",
			},
		},
	}
	decision, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.NoError(t, err)
	assert.Equal(t, SourceFallback, decision.Source)
	assert.Equal(t, "parent", decision.Provider)
	assert.Equal(t, "parent-model", decision.Model)
	assert.True(t, decision.FallbackUsed)
	assert.Equal(t, "provider_unresolved_parent", decision.FallbackReason)
	assert.Contains(t, decision.Warnings, "provider_unresolved")
	assert.Contains(t, decision.Warnings, "provider_fallback_parent")
	assert.Contains(t, decision.Warnings, "model_fallback_parent")
}

func TestResolver_ProviderOnlyRouteUsesProviderDefaultModel(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote"},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers:     map[string]string{"remote": "remote"},
			defaultModels: map[string]string{"remote": "remote-default"},
		},
	}
	decision, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.NoError(t, err)
	assert.Equal(t, "remote", decision.Provider)
	assert.Equal(t, "remote-default", decision.Model)
	assert.Contains(t, decision.Warnings, "model_default_provider")
}

func TestResolver_UnresolvedProviderFailsWhenFallbackDisabled(t *testing.T) {
	enabled := true
	inherit := false
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                  &enabled,
		InheritParentWhenMissing: &inherit,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "missing-provider", Model: "strong"},
		},
	}
	resolver := Resolver{
		Config:  cfg,
		Catalog: testCatalog{providers: map[string]string{"parent": "parent"}},
	}
	_, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagent route provider unavailable")
}

func TestResolver_UnsupportedRouteModelFallsBackToParent(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote", Model: "unknown-model"},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{"remote": "remote", "parent": "parent"},
			models: map[string]bool{
				"remote/unknown-model": false,
			},
		},
	}
	decision, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.NoError(t, err)
	assert.Equal(t, SourceFallback, decision.Source)
	assert.Equal(t, "parent", decision.Provider)
	assert.Equal(t, "parent-model", decision.Model)
	assert.True(t, decision.FallbackUsed)
	assert.Equal(t, "model_unsupported_parent", decision.FallbackReason)
	assert.Contains(t, decision.Warnings, "model_unsupported")
	assert.Contains(t, decision.Warnings, "model_fallback_parent")
}

func TestResolver_UnsupportedRouteModelFailsWhenFallbackDisabled(t *testing.T) {
	enabled := true
	inherit := false
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                  &enabled,
		InheritParentWhenMissing: &inherit,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote", Model: "unknown-model"},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{"remote": "remote"},
			models:    map[string]bool{"remote/unknown-model": false},
		},
	}
	_, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagent route model unavailable")
}

func TestResolver_UnsupportedReasoningEffortIsIgnored(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote", Model: "strong", ReasoningEffort: "high", Timeout: 5 * time.Second},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{"remote": "remote"},
			efforts: map[string]map[string]bool{
				"remote/strong": {"low": true},
			},
		},
	}
	decision, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.NoError(t, err)
	assert.Equal(t, "", decision.ReasoningEffort)
	assert.Equal(t, 5*time.Second, decision.Timeout)
	assert.Contains(t, decision.Warnings, "reasoning_effort_unsupported_ignored")
}

func TestResolver_UnsupportedReasoningEffortFailsByPolicy(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                    &enabled,
		UnsupportedReasoningPolicy: "fail",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote", Model: "strong", ReasoningEffort: "high"},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{"remote": "remote"},
			efforts: map[string]map[string]bool{
				"remote/strong": {"low": true},
			},
		},
	}
	_, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reasoning_effort unsupported")
}

func TestResolver_UnsupportedReasoningEffortDowngradesByPolicy(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                    &enabled,
		UnsupportedReasoningPolicy: "downgrade",
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			"hard": {Provider: "remote", Model: "strong", ReasoningEffort: "high"},
		},
	}
	resolver := Resolver{
		Config: cfg,
		Catalog: testCatalog{
			providers: map[string]string{"remote": "remote"},
			efforts: map[string]map[string]bool{
				"remote/strong": {"low": true, "medium": true},
			},
		},
	}
	decision, err := resolver.Resolve(ParentDefaults{Provider: "parent", Model: "parent-model"}, TaskHint{Difficulty: "hard"})
	require.NoError(t, err)
	assert.Equal(t, "medium", decision.ReasoningEffort)
	assert.Contains(t, decision.Warnings, "reasoning_effort_unsupported_downgraded")
}

func boolPtr(value bool) *bool {
	return &value
}
