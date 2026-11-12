package memorypolicy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
)

func TestStoreAndRuntimePolicyMapping(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig().Memory
	cfg.Retention.Enabled = true
	cfg.Retention.CandidateDays = 11
	cfg.Retention.EphemeralDays = 3
	cfg.Retention.DeletedDays = 22
	cfg.Retention.AuditDays = 99
	cfg.Retention.MaxRecords = 77
	cfg.Consolidation.MaxUnusedDays = 44
	cfg.Freshness.StaleBehavior = "warn"
	cfg.Freshness.DefaultTTLDays = 12
	cfg.Freshness.PerKind = map[string]int{"fact": 5}
	cfg.Redaction.Enabled = false

	policy := StoreRetention(cfg)
	require.True(t, policy.Enabled)
	require.Equal(t, 11, policy.CandidateDays)
	require.Equal(t, 3, policy.EphemeralDays)
	require.Equal(t, 22, policy.DeletedDays)
	require.Equal(t, 99, policy.AuditDays)
	require.Equal(t, 77, policy.MaxRecords)
	require.Equal(t, 44, policy.ActiveUnusedDays)
	require.Equal(t, "permanent", policy.Classes.ClassForKind("decision"))

	require.Equal(t, "warn", StaleBehavior(cfg))
	require.True(t, DisableSecretFilter(cfg))

	runtimePolicy := ApplyRuntime(cfg, memoryruntime.Config{})
	require.False(t, runtimePolicy.FreshnessDisabled)
	require.Equal(t, 12*24*time.Hour, runtimePolicy.FreshnessDefaultTTL)
	require.Equal(t, 5*24*time.Hour, runtimePolicy.FreshnessPerKind["fact"])
	require.Equal(t, "warn", runtimePolicy.FreshnessStaleBehavior)
	require.True(t, runtimePolicy.RedactionConfigured)
	require.False(t, runtimePolicy.RedactionEnabled)
}

func TestStaleBehaviorDisabledFreshnessStillExcludes(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig().Memory
	cfg.Freshness.Enabled = false
	cfg.Freshness.StaleBehavior = "warn"
	require.Equal(t, "exclude", StaleBehavior(cfg))
}
