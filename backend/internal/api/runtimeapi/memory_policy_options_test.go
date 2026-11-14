package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
)

func TestContextMemoryReadOptionsMatchRuntimePolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		freshness bool
		behavior  string
		redact    bool
	}{
		{"warn", true, "warn", true},
		{"exclude", true, "exclude", true},
		{"freshness_disabled", false, "warn", true},
		{"redaction_opt_out", true, "warn", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := runtimecfg.DefaultRuntimeConfig()
			cfg.Memory.Mode = "on"
			cfg.Memory.Freshness.Enabled = tc.freshness
			cfg.Memory.Freshness.StaleBehavior = tc.behavior
			cfg.Memory.Redaction.Enabled = tc.redact
			options := contextOptionsFromRuntimeConfig(cfg, nil)
			require.Equal(t, memorypolicy.StaleBehavior(cfg.Memory), options["memory_stale_behavior"])
			require.Equal(t, !memorypolicy.DisableSecretFilter(cfg.Memory), options["memory_redaction_enabled"])
		})
	}
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "off"
	require.NotContains(t, contextOptionsFromRuntimeConfig(cfg, nil), "memory_stale_behavior")
}
