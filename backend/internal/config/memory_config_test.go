package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryConfigDefaultsOff(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.Equal(t, "off", cfg.Memory.Mode)
	require.Equal(t, "project", cfg.Memory.Scope)
	require.True(t, cfg.Memory.Manual.Enabled)
	require.True(t, cfg.Memory.Generation.Enabled)
	require.Equal(t, "idle", cfg.Memory.Generation.Trigger)
	require.Equal(t, 6, cfg.Memory.Generation.MinIdleHours)
	require.Equal(t, 2, cfg.Memory.Generation.MaxSessionsPerRun)
	require.Equal(t, 20, cfg.Memory.Generation.MaxRecordsPerSession)
	require.True(t, cfg.Memory.Injection.Enabled)
	require.Equal(t, "notes", cfg.Memory.Injection.Mode)
	require.Equal(t, 0.20, cfg.Memory.Injection.PinnedBudgetRatio)
	require.True(t, cfg.Memory.Injection.StageSplit)
	require.NoError(t, ValidateRuntimeConfig(cfg))
}

func TestMemoryConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*MemoryConfig)
		ok   bool
	}{
		{"valid on", func(c *MemoryConfig) { c.Mode = "on" }, true},
		{"valid shadow", func(c *MemoryConfig) { c.Mode = "shadow" }, true},
		{"invalid mode", func(c *MemoryConfig) { c.Mode = "enabled" }, false},
		{"invalid scope", func(c *MemoryConfig) { c.Scope = "global" }, false},
		{"invalid injection mode", func(c *MemoryConfig) { c.Injection.Mode = "semantic" }, false},
		{"invalid generation trigger", func(c *MemoryConfig) { c.Generation.Trigger = "always" }, false},
		{"negative min idle", func(c *MemoryConfig) { c.Generation.MinIdleHours = -1 }, false},
		{"negative max sessions", func(c *MemoryConfig) { c.Generation.MaxSessionsPerRun = -1 }, false},
		{"negative max rollout", func(c *MemoryConfig) { c.Generation.MaxRolloutAgeDays = -1 }, false},
		{"negative max records", func(c *MemoryConfig) { c.Generation.MaxRecordsPerSession = -1 }, false},
		{"negative max items", func(c *MemoryConfig) { c.Injection.MaxItems = -1 }, false},
		{"negative token budget", func(c *MemoryConfig) { c.Injection.TokenBudget = -1 }, false},
		{"ratio too high", func(c *MemoryConfig) { c.Injection.PinnedBudgetRatio = 1.1 }, false},
		{"ratio negative", func(c *MemoryConfig) { c.Injection.PinnedBudgetRatio = -0.1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultRuntimeConfig()
			tc.mut(&cfg.Memory)
			err := ValidateMemoryConfig(&cfg.Memory)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
