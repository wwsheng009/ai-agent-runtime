package modelrouting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerhealth"
)

// failingHealth 造一个"某个目标已经熔断"的健康源。阈值设为 1，一次归因失败
// 即可打开，测试里不必堆样本。
func failingHealth(t *testing.T, targets ...string) *providerhealth.Registry {
	t.Helper()
	registry := providerhealth.NewRegistry(providerhealth.Spec{FailureThreshold: 1})
	now := time.Now()
	for _, target := range targets {
		provider, model := splitTarget(t, target)
		observed := registry.Observe(provider, model, llm.FailureCategoryTimeout, now)
		require.True(t, observed.Opened, "expected %s to trip the breaker", target)
	}
	return registry
}

func splitTarget(t *testing.T, target string) (string, string) {
	t.Helper()
	for i := 0; i < len(target); i++ {
		if target[i] == '/' {
			return target[:i], target[i+1:]
		}
	}
	t.Fatalf("target %q must be provider/model", target)
	return "", ""
}

func failoverRoutingConfig() *agentconfig.AICLISubagentRoutingConfig {
	return enabledRoutingConfig(map[string]agentconfig.AICLISubagentRouteProfile{
		DifficultyNormal: {
			Provider:  "opencode",
			Model:     "flash",
			MaxTokens: 4096,
			Candidates: []agentconfig.AICLISubagentRouteCandidate{
				{Provider: "commandgo", Model: "pro"},
			},
		},
	})
}

// 动态健康源的核心用途：主选正在挂时，路由自己改走候选，而不是等人工改配置。
func TestResolver_UnhealthyPrimaryFailsOverToCandidate(t *testing.T) {
	decision, err := (Resolver{
		Config:  failoverRoutingConfig(),
		Catalog: candidateCatalog(),
		Health:  failingHealth(t, "opencode/flash"),
	}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceFailoverCandidate, decision.Source)
	assert.Equal(t, "commandgo", decision.Provider)
	assert.Equal(t, "pro", decision.Model)
	assert.True(t, decision.FallbackUsed)
	assert.Equal(t, "route_primary_gated", decision.FallbackReason)
	assert.Contains(t, decision.Warnings, "route_failover_candidate_used")

	require.Len(t, decision.Candidates, 2)
	assert.Equal(t, providerhealth.StateUnhealthy, decision.Candidates[0].HealthState)
	assert.Equal(t, "candidate_unhealthy", decision.Candidates[0].SkipReason)
	assert.True(t, decision.Candidates[1].Eligible)
}

// 整条链都被健康门禁摘光时，必须退回正在运行的父 Agent，而不是让子 Agent 构造
// 失败：provider 挂了不该升级成"所有委派都不可用"。
func TestResolver_HealthExhaustedDegradesToParent(t *testing.T) {
	decision, err := (Resolver{
		Config:  failoverRoutingConfig(),
		Catalog: candidateCatalog(),
		Health:  failingHealth(t, "opencode/flash", "commandgo/pro"),
	}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceFallback, decision.Source)
	assert.Equal(t, "parent", decision.Provider)
	assert.Equal(t, "parent-model", decision.Model)
	assert.True(t, decision.FallbackUsed)
	assert.Equal(t, "route_health_exhausted_parent", decision.FallbackReason)
	assert.Contains(t, decision.Warnings, "route_health_exhausted_parent")
	// 预算按难度调的，与落在哪个 provider 无关，因此必须保留。
	assert.Equal(t, 4096, decision.MaxTokens)

	require.Len(t, decision.Candidates, 2)
	for _, candidate := range decision.Candidates {
		assert.Equal(t, "candidate_unhealthy", candidate.SkipReason)
	}
}

// 健康门禁必须复用 availability_policy：运维显式写 ignore，等于声明"不要依据
// 观测健康摘除候选"，否则一个开关只关掉了半个行为。
func TestResolver_HealthGateRespectsAvailabilityPolicyIgnore(t *testing.T) {
	cfg := failoverRoutingConfig()
	cfg.AvailabilityPolicy = AvailabilityPolicyIgnore

	decision, err := (Resolver{
		Config:  cfg,
		Catalog: candidateCatalog(),
		Health:  failingHealth(t, "opencode/flash"),
	}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Equal(t, "opencode", decision.Provider)
	// 观测结论仍然写进审计，只是不参与门禁。
	require.Len(t, decision.Candidates, 2)
	assert.Equal(t, providerhealth.StateUnhealthy, decision.Candidates[0].HealthState)
	assert.Empty(t, decision.Candidates[0].SkipReason)
	assert.True(t, decision.Candidates[0].Eligible)
}

// 没有观测记录不等于有罪：新配的 provider 若被当成不可用，就永远拿不到第一次
// 调用，形成死锁。
func TestResolver_UnobservedTargetIsNotGated(t *testing.T) {
	decision, err := (Resolver{
		Config:  failoverRoutingConfig(),
		Catalog: candidateCatalog(),
		Health:  failingHealth(t, "commandgo/pro"),
	}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Equal(t, "opencode", decision.Provider)
	require.Len(t, decision.Candidates, 2)
	assert.Empty(t, decision.Candidates[0].HealthState)
	assert.True(t, decision.Candidates[0].Eligible)
}

// 自身 bug、预算超限、用户取消都不该把 provider 摘掉——否则会在最需要路由的
// 时候把健康的后端误熔断。
func TestResolver_NonProviderFaultDoesNotGate(t *testing.T) {
	registry := providerhealth.NewRegistry(providerhealth.Spec{FailureThreshold: 1})
	now := time.Now()
	for _, category := range []string{
		llm.FailureCategoryToolError,
		llm.FailureCategoryBudgetExceeded,
		llm.FailureCategoryContextOverflow,
		llm.FailureCategoryCancelled,
		llm.FailureCategoryUnknown,
		"",
	} {
		registry.Observe("opencode", "flash", category, now)
	}

	decision, err := (Resolver{
		Config:  failoverRoutingConfig(),
		Catalog: candidateCatalog(),
		Health:  registry,
	}).Resolve(
		ParentDefaults{Provider: "parent", Model: "parent-model"},
		TaskHint{Difficulty: DifficultyNormal},
	)
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Equal(t, "opencode", decision.Provider)
	require.Len(t, decision.Candidates, 2)
	assert.True(t, decision.Candidates[0].Eligible)
}
