package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAgentsMaxConcurrentDefaultsAndPassthrough pins the P1-4/H12 config
// contract: agents.maxConcurrent is the per-batch subagent ceiling, 0 means
// "unset → 4" (the historical scheduler default), and a partial `agents:` block
// that only sets maxThreads must keep it at the default. The backpressure knobs
// default to 0 = disabled, i.e. today's unbounded waiting.
func TestAgentsMaxConcurrentDefaultsAndPassthrough(t *testing.T) {
	defaults := DefaultRuntimeConfig().Agents
	require.Equal(t, 4, defaults.MaxConcurrent, "absent config must keep the historical per-batch default")
	require.Zero(t, defaults.MaxConcurrentQueueDepth, "backpressure must stay opt-in (default off)")
	require.Zero(t, defaults.MaxConcurrentQueueTimeoutMs, "queue timeout must stay opt-in (default off)")

	require.Equal(t, 4, NormalizeAgentsConfig(AgentsConfig{}).MaxConcurrent)

	// Partial config tolerance: writing only maxThreads must not drop the
	// per-batch ceiling to 0 (which would read as "unlimited" downstream).
	partial := NormalizeAgentsConfig(AgentsConfig{MaxThreads: 3})
	require.Equal(t, 3, partial.MaxThreads)
	require.Equal(t, 4, partial.MaxConcurrent)
	require.Zero(t, partial.MaxConcurrentQueueDepth)
	require.Zero(t, partial.MaxConcurrentQueueTimeoutMs)

	// Explicit values pass through untouched.
	explicit := NormalizeAgentsConfig(AgentsConfig{
		MaxConcurrent:               2,
		MaxConcurrentQueueDepth:     1,
		MaxConcurrentQueueTimeoutMs: 250,
	})
	require.Equal(t, 2, explicit.MaxConcurrent)
	require.Equal(t, 1, explicit.MaxConcurrentQueueDepth)
	require.Equal(t, 250, explicit.MaxConcurrentQueueTimeoutMs)
}

// TestValidateAgentsConfigMaxConcurrent pins validation: negatives are rejected
// with the knob name, while 0 (default) and positive values are valid.
func TestValidateAgentsConfigMaxConcurrent(t *testing.T) {
	require.NoError(t, ValidateAgentsConfig(&AgentsConfig{}))
	require.NoError(t, ValidateAgentsConfig(&AgentsConfig{MaxConcurrent: 2}))
	require.NoError(t, ValidateAgentsConfig(&AgentsConfig{
		MaxConcurrentQueueDepth:     1,
		MaxConcurrentQueueTimeoutMs: 100,
	}))

	err := ValidateAgentsConfig(&AgentsConfig{MaxConcurrent: -1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents.maxConcurrent")

	err = ValidateAgentsConfig(&AgentsConfig{MaxConcurrentQueueDepth: -1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents.maxConcurrentQueueDepth")

	err = ValidateAgentsConfig(&AgentsConfig{MaxConcurrentQueueTimeoutMs: -1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents.maxConcurrentQueueTimeoutMs")
}
