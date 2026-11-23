package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestMemoryConfigFromOptionsCarriesReadPolicy(t *testing.T) {
	cfg := memoryConfigFromOptions(map[string]interface{}{
		"memory_stale_behavior":    "warn",
		"memory_redaction_enabled": false,
	}, memorystore.Config{})
	require.Equal(t, "warn", cfg.StaleBehavior)
	require.True(t, cfg.DisableSecretFilter)

	for _, options := range []map[string]interface{}{
		nil, {}, {"memory_redaction_enabled": "unexpected"},
		{"memory_redaction_enabled": true},
	} {
		cfg := memoryConfigFromOptions(options, memorystore.Config{})
		require.False(t, cfg.DisableSecretFilter, "absent/invalid input must keep the safe default")
	}
}

func TestMemoryConfigFromOptionsCarriesInjectionCategoryBudgets(t *testing.T) {
	cfg := memoryConfigFromOptions(map[string]interface{}{
		"memory_injection_budget_core":      0.20,
		"memory_injection_budget_task":      0.15,
		"memory_injection_budget_retrieved": 0.40,
		"memory_injection_budget_procedure": 0.15,
		"memory_injection_budget_reference": 0.10,
	}, memorystore.Config{})
	require.True(t, cfg.CategoryBudgetsEnabled)
	require.Equal(t, memorystore.InjectionCategoryBudgets{
		Core: 0.20, Task: 0.15, Retrieved: 0.40,
		Procedure: 0.15, Reference: 0.10,
	}, cfg.CategoryBudgets)
}
