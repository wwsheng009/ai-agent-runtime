package modelrouting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func testBoolPtr(value bool) *bool { return &value }

// candidateCatalog mirrors the shape of the real provider catalog: a set of
// resolvable providers and a per-provider/model support table.
func candidateCatalog() testCatalog {
	return testCatalog{
		providers: map[string]string{
			"opencode":  "opencode",
			"commandgo": "commandgo",
			"2chat":     "2chat",
		},
		models: map[string]bool{
			"opencode/flash": true,
			"commandgo/pro":  true,
			"2chat/glm-5.3":  true,
		},
	}
}

func enabledRoutingConfig(levels map[string]agentconfig.AICLISubagentRouteProfile) *agentconfig.AICLISubagentRoutingConfig {
	enabled := true
	return &agentconfig.AICLISubagentRoutingConfig{Enabled: &enabled, Levels: levels}
}

// A profile that carries no annotations and no candidates must behave exactly
// as it did before failover existed: this is the compatibility guarantee for
// every already-deployed routing config.
func TestRouteCandidates_UnannotatedProfileKeepsLegacyBehavior(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {Provider: "opencode", Model: "flash"},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.False(t, decision.FallbackUsed)
	assert.Empty(t, decision.FallbackReason)
	assert.Equal(t, "opencode", decision.Provider)
	assert.Equal(t, "flash", decision.Model)
	require.Len(t, decision.Candidates, 1)
	assert.True(t, decision.Candidates[0].Eligible)
	assert.True(t, decision.Candidates[0].Primary)
}

func TestRouteCandidates_UnavailablePrimaryFailsOver(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:           "2chat",
			Model:              "glm-5.3",
			Availability:       AvailabilityUnavailable,
			AvailabilityReason: "errored in the recorded request timeline",
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "opencode", Model: "flash"},
			},
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceFailoverCandidate, decision.Source)
	assert.True(t, decision.FallbackUsed)
	assert.Equal(t, "route_primary_gated", decision.FallbackReason)
	assert.Contains(t, decision.Warnings, "route_failover_candidate_used")
	assert.Equal(t, "opencode", decision.Provider)
	assert.Equal(t, "flash", decision.Model)

	require.Len(t, decision.Candidates, 2)
	assert.False(t, decision.Candidates[0].Eligible)
	assert.Equal(t, "candidate_unavailable", decision.Candidates[0].SkipReason)
	assert.True(t, decision.Candidates[1].Eligible)
	assert.False(t, decision.Candidates[1].Primary)
}

// Excluding every target is a configuration bug, so it must fail loudly and
// name the annotation that removed each candidate instead of silently routing
// to a provider the operator just marked dead.
func TestRouteCandidates_ExhaustedChainFailsLoud(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyExpert: {
			Provider:     "2chat",
			Model:        "grok-4.5",
			Availability: AvailabilityUnavailable,
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "2chat", Model: "glm-5.3", Availability: AvailabilityUnavailable},
			},
		},
	})
	_, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyExpert},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no eligible candidate")
	assert.Contains(t, err.Error(), "candidate_unavailable")
	assert.Contains(t, err.Error(), "2chat/glm-5.3")
}

func TestRouteCandidates_AvailabilityPolicyIgnoreKeepsPrimary(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:     "2chat",
			Model:        "glm-5.3",
			Availability: AvailabilityUnavailable,
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "opencode", Model: "flash"},
			},
		},
	})
	cfg.AvailabilityPolicy = AvailabilityPolicyIgnore

	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.False(t, decision.FallbackUsed)
	assert.Equal(t, "2chat", decision.Provider)
}

// A backend that does not participate in prompt caching must not be selected
// when the route is configured to require it.
func TestRouteCandidates_PromptCacheGateSkipsNonCachingPrimary(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:    "2chat",
			Model:       "glm-5.3-free",
			PromptCache: testBoolPtr(false),
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "opencode", Model: "flash", PromptCache: testBoolPtr(true)},
			},
		},
	})
	cfg.RequirePromptCache = true

	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceFailoverCandidate, decision.Source)
	assert.Equal(t, "opencode", decision.Provider)
	require.Len(t, decision.Candidates, 2)
	assert.Equal(t, "candidate_prompt_cache_required", decision.Candidates[0].SkipReason)
}

// Without require_prompt_cache the annotation is advisory only.
func TestRouteCandidates_PromptCacheAnnotationIgnoredByDefault(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:    "2chat",
			Model:       "glm-5.3-free",
			PromptCache: testBoolPtr(false),
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, "2chat", decision.Provider)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
}

func TestRouteCandidates_FailoverDisabledIgnoresChain(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider: "opencode",
			Model:    "flash",
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "commandgo", Model: "pro"},
			},
		},
	})
	cfg.Failover = testBoolPtr(false)

	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, "opencode", decision.Provider)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Len(t, decision.Candidates, 1, "disabled failover must not evaluate the chain")
}

func TestRouteCandidates_DegradedCandidateIsEligibleWithWarning(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:           "opencode",
			Model:              "flash",
			Availability:       AvailabilityDegraded,
			AvailabilityReason: "intermittent timeouts observed",
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Contains(t, decision.Warnings, "route_candidate_degraded")
	assert.Equal(t, "opencode", decision.Provider)
}

// A failover must never land on a provider the catalog cannot resolve.
func TestRouteCandidates_UnresolvableCandidateIsSkipped(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:     "2chat",
			Model:        "glm-5.3",
			Availability: AvailabilityUnavailable,
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "ghost", Model: "nowhere"},
				{Provider: "opencode", Model: "flash"},
			},
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, "opencode", decision.Provider)
	require.Len(t, decision.Candidates, 3)
	assert.Equal(t, "candidate_provider_unresolved", decision.Candidates[1].SkipReason)
	assert.True(t, decision.Candidates[2].Eligible)
}

// The fallback shares the difficulty level's reasoning/token/timeout policy:
// only the target varies between candidates.
func TestRouteCandidates_FallbackKeepsProfileSettings(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyExpert: {
			Provider:        "2chat",
			Model:           "grok-4.5",
			Availability:    AvailabilityUnavailable,
			ReasoningEffort: "high",
			MaxTokens:       12000,
			Timeout:         90 * time.Second,
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "commandgo", Model: "pro"},
			},
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyExpert},
	)
	require.NoError(t, err)
	assert.Equal(t, "commandgo", decision.Provider)
	assert.Equal(t, "pro", decision.Model)
	assert.Equal(t, "high", decision.ReasoningEffort)
	assert.Equal(t, 12000, decision.MaxTokens)
	assert.Equal(t, 90*time.Second, decision.Timeout)
}

// A broken fallback must never take down a route whose primary is healthy; it
// is recorded and warned about instead, so the operator can repair it before
// the primary actually fails.
func TestRouteCandidates_BrokenFallbackDoesNotBreakHealthyPrimary(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider: "opencode",
			Model:    "flash",
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "ghost", Model: "nowhere"},
			},
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.False(t, decision.FallbackUsed)
	assert.Equal(t, "opencode", decision.Provider)
	assert.Contains(t, decision.Warnings, "route_fallback_chain_degraded")

	require.Len(t, decision.Candidates, 2)
	assert.True(t, decision.Candidates[0].Eligible)
	assert.False(t, decision.Candidates[1].Eligible)
	assert.Equal(t, "candidate_provider_unresolved", decision.Candidates[1].SkipReason)
}

// The full chain is audited even when the primary wins, so doctor can show the
// configured fallback capacity instead of only the selected target.
func TestRouteCandidates_ChainFullyAuditedWhenPrimaryIsHealthy(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider: "opencode",
			Model:    "flash",
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "commandgo", Model: "pro"},
				{Provider: "2chat", Model: "glm-5.3"},
			},
		},
	})
	decision, err := (Resolver{Config: cfg, Catalog: candidateCatalog()}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.NotContains(t, decision.Warnings, "route_fallback_chain_degraded")
	require.Len(t, decision.Candidates, 3)
	for _, candidate := range decision.Candidates {
		assert.True(t, candidate.Eligible, "expected %s/%s to be eligible", candidate.Provider, candidate.Model)
	}
	assert.True(t, decision.Candidates[0].Primary)
	assert.False(t, decision.Candidates[1].Primary)
	assert.False(t, decision.Candidates[2].Primary)
}

// Every already-deployed routing config carries none of the new keys, so their
// zero values must stay valid and must resolve to the documented defaults.
// Without this, adding the feature would break existing configs at load time.
func TestValidateConfig_AcceptsConfigWithoutNewKeys(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {Provider: "opencode", Model: "flash"},
	})
	require.NoError(t, ValidateConfig(cfg))
	assert.Equal(t, AvailabilityPolicySkip, AvailabilityPolicy(cfg))
	assert.True(t, RoutingFailoverEnabled(cfg), "an unset failover key must keep the chain enabled")
	assert.False(t, RequirePromptCache(cfg))
}

func TestValidateConfig_RejectsInvalidAvailabilityAnnotations(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {Provider: "opencode", Model: "flash", Availability: "sometimes"},
	})
	err := ValidateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid availability")

	cfg = enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider: "opencode",
			Model:    "flash",
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "commandgo", Model: "pro", Availability: "sometimes"},
			},
		},
	})
	err = ValidateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidates[0]")
}

func TestValidateConfig_RejectsIncompleteCandidate(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider: "opencode",
			Model:    "flash",
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "commandgo"},
			},
		},
	})
	err := ValidateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must set model")
}

func TestValidateConfig_RejectsInvalidAvailabilityPolicy(t *testing.T) {
	cfg := enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {Provider: "opencode", Model: "flash"},
	})
	cfg.AvailabilityPolicy = "maybe"
	err := ValidateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid subagent availability policy")
}

func TestNormalizeAvailability(t *testing.T) {
	cases := map[string]string{
		"":            AvailabilityAvailable,
		"ok":          AvailabilityAvailable,
		"healthy":     AvailabilityAvailable,
		"degraded":    AvailabilityDegraded,
		"flaky":       AvailabilityDegraded,
		"unavailable": AvailabilityUnavailable,
		"errored":     AvailabilityUnavailable,
		"disabled":    AvailabilityUnavailable,
	}
	for input, expected := range cases {
		got, ok := NormalizeAvailability(input)
		require.True(t, ok, "expected %q to normalize", input)
		assert.Equal(t, expected, got)
	}
	_, ok := NormalizeAvailability("sometimes")
	assert.False(t, ok)
}
