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
