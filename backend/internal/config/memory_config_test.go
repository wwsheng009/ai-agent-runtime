package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMemoryConfigDefaultsOff(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.Equal(t, "off", cfg.Memory.Mode)
	require.Equal(t, "project", cfg.Memory.Scope)
	require.True(t, cfg.Memory.Manual.Enabled)
	require.True(t, cfg.Memory.Generation.Enabled)
	require.Equal(t, "both", cfg.Memory.Generation.Trigger)
	require.Equal(t, 6, cfg.Memory.Generation.MinIdleHours)
	require.Equal(t, 2, cfg.Memory.Generation.MaxSessionsPerRun)
	require.Equal(t, 20, cfg.Memory.Generation.MaxRecordsPerSession)
	require.True(t, cfg.Memory.Generation.TurnDigest.Enabled)
	require.True(t, cfg.Memory.Maintenance.CaptureExtraction.Enabled)
	require.Equal(t, 50, cfg.Memory.Maintenance.CaptureExtraction.BatchSize)
	require.Equal(t, 5*time.Minute, cfg.Memory.Maintenance.CaptureExtraction.Interval)
	require.False(t, cfg.Memory.Maintenance.JobDeadlineRepair.Enabled)
	require.Equal(t, 100, cfg.Memory.Maintenance.JobDeadlineRepair.BatchSize)
	require.Equal(t, 30*time.Minute, cfg.Memory.Maintenance.JobDeadlineRepair.Interval)
	require.True(t, cfg.Memory.Generation.TurnDigest.StoreText)
	require.True(t, cfg.Memory.Generation.TurnDigest.RecoveryOnStartup)
	require.Equal(t, 7, cfg.Memory.Generation.TurnDigest.RetentionDays)
	require.Equal(t, 20, cfg.Memory.Generation.TurnDigest.MaxTurnsPerBatch)
	require.Equal(t, 128*1024, cfg.Memory.Generation.TurnDigest.MaxBytesPerBatch)
	require.Equal(t, 50, cfg.Memory.Generation.TurnDigest.ExtractEveryNTurns)
	require.Equal(t, 30, cfg.Memory.Generation.TurnDigest.IdleExtractMinutes)
	require.Equal(t, 100, cfg.Memory.Generation.TurnDigest.WindowTurns)
	require.Equal(t, 30, cfg.Memory.Generation.TurnDigest.WindowRetentionDays)
	require.True(t, cfg.Memory.Generation.TurnDigest.WindowSummaryEnabled)
	require.Empty(t, cfg.Memory.Generation.TurnDigest.ReasoningEffort, "inherit generation effort by default")
	require.Empty(t, cfg.Memory.Consolidation.ReasoningEffort, "inherit generation effort by default")
	require.True(t, cfg.Memory.Consolidation.Enabled)
	require.Equal(t, 256, cfg.Memory.Consolidation.MaxRecords)
	require.Equal(t, 800, cfg.Memory.Consolidation.SummaryTokenBudget)
	require.Equal(t, 24, cfg.Memory.Consolidation.IntervalHours)
	require.False(t, cfg.Memory.Core.Rebuild.Enabled)
	require.Equal(t, 24*time.Hour, cfg.Memory.Core.Rebuild.Interval)
	require.Equal(t, 32, cfg.Memory.Core.Rebuild.MaxRecords)
	require.Equal(t, 200, cfg.Memory.Core.Rebuild.MaxUsageRows)
	require.Equal(t, 1200, cfg.Memory.Core.Rebuild.TokenBudget)
	require.True(t, cfg.Memory.Injection.Enabled)
	require.Equal(t, "notes", cfg.Memory.Injection.Mode)
	require.Equal(t, 600, cfg.Memory.Injection.EffectiveTokenBudget())
	require.False(t, cfg.Memory.Injection.ExtendedPreset)
	require.Equal(t, MemoryInjectionBudgets{
		Core: 0.20, Task: 0.15, Retrieved: 0.40, Procedure: 0.15, Reference: 0.10,
	}, cfg.Memory.Injection.Budgets)
	require.Equal(t, 0.20, cfg.Memory.Injection.PinnedBudgetRatio)
	require.True(t, cfg.Memory.Injection.StageSplit)
	require.False(t, cfg.Memory.Injection.RecallTrace.Enabled)
	require.Equal(t, 0.01, cfg.Memory.Injection.RecallTrace.SampleRate)
	require.Equal(t, 14, cfg.Memory.Injection.RecallTrace.RetentionDays)
	require.True(t, cfg.Memory.Feedback.CitationEnabled)
	require.Equal(t, 1.0, cfg.Memory.Feedback.ExposureSampling)
	require.Equal(t, 30, cfg.Memory.Feedback.ExposureRetentionDays)
	require.False(t, cfg.Memory.Retention.Enabled)
	require.Equal(t, 30, cfg.Memory.Retention.CandidateDays)
	require.Equal(t, 14, cfg.Memory.Retention.EphemeralDays)
	require.Equal(t, 30, cfg.Memory.Retention.DeletedDays)
	require.Equal(t, 180, cfg.Memory.Retention.AuditDays)
	require.Equal(t, 4096, cfg.Memory.Retention.MaxRecords)
	require.Equal(t, []string{"manual", "pattern", "decision", "correction", "preference"}, cfg.Memory.Retention.Classes.Permanent)
	require.True(t, cfg.Memory.Freshness.Enabled)
	require.Equal(t, "exclude", cfg.Memory.Freshness.StaleBehavior)
	require.Equal(t, 90, cfg.Memory.Freshness.DefaultTTLDays)
	require.Equal(t, 50, cfg.Memory.Freshness.RevalidateBatchSize)
	require.Equal(t, 24, cfg.Memory.Freshness.RevalidateIntervalHours)
	require.Equal(t, 30, cfg.Memory.Freshness.PerKind["fact"])
	require.True(t, cfg.Memory.Redaction.Enabled)
	require.True(t, cfg.Memory.Redaction.DropOnSecret)
	require.False(t, cfg.Memory.Tools.Enabled)
	require.False(t, cfg.Memory.Tools.AllowForget)
	require.False(t, cfg.Memory.Tools.AllowRollback)
	require.NoError(t, ValidateRuntimeConfig(cfg))
}

func TestMemoryRollbackToolConfigExplicitOptIn(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte(`
memory:
  mode: on
  tools:
    enabled: true
    allowRollback: true
`), cfg))
	require.True(t, cfg.Memory.Tools.Enabled)
	require.True(t, cfg.Memory.Tools.AllowRollback)
}

func TestMemoryStageReasoningConfigRoundTrip(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte(`
memory:
  generation:
    provider: extract-provider
    model: extract-model
    reasoningEffort: max
    turnDigest:
      reasoningEffort: low
  consolidation:
    provider: summary-provider
    model: summary-model
    reasoningEffort: medium
  core:
    rebuild:
      enabled: true
      interval: 6h
      maxRecords: 48
      maxUsageRows: 250
      tokenBudget: 1600
  injection:
    extendedPreset: true
    budgets:
      core: 0.25
      task: 0.15
      retrieved: 0.35
      procedure: 0.15
      reference: 0.10
`), cfg))
	require.NoError(t, ValidateMemoryConfig(&cfg.Memory))
	require.Equal(t, "extract-provider", cfg.Memory.Generation.Provider)
	require.Equal(t, "extract-model", cfg.Memory.Generation.Model)
	require.Equal(t, "summary-provider", cfg.Memory.Consolidation.Provider)
	require.Equal(t, "summary-model", cfg.Memory.Consolidation.Model)
	require.Equal(t, "max", cfg.Memory.Generation.ReasoningEffort)
	require.Equal(t, "low", cfg.Memory.Generation.TurnDigest.ReasoningEffort)
	require.Equal(t, "medium", cfg.Memory.Consolidation.ReasoningEffort)
	require.True(t, cfg.Memory.Core.Rebuild.Enabled)
	require.Equal(t, 6*time.Hour, cfg.Memory.Core.Rebuild.Interval)
	require.Equal(t, 48, cfg.Memory.Core.Rebuild.MaxRecords)
	require.Equal(t, 250, cfg.Memory.Core.Rebuild.MaxUsageRows)
	require.Equal(t, 1600, cfg.Memory.Core.Rebuild.TokenBudget)
	require.True(t, cfg.Memory.Injection.ExtendedPreset)
	require.Equal(t, 5000, cfg.Memory.Injection.EffectiveTokenBudget())
	require.Equal(t, MemoryInjectionBudgets{
		Core: 0.25, Task: 0.15, Retrieved: 0.35, Procedure: 0.15, Reference: 0.10,
	}, cfg.Memory.Injection.Budgets)
	raw, err := json.Marshal(cfg.Memory)
	require.NoError(t, err)
	var roundTrip MemoryConfig
	require.NoError(t, json.Unmarshal(raw, &roundTrip))
	require.Equal(t, cfg.Memory.Generation.Provider, roundTrip.Generation.Provider)
	require.Equal(t, cfg.Memory.Generation.Model, roundTrip.Generation.Model)
	require.Equal(t, cfg.Memory.Consolidation.Provider, roundTrip.Consolidation.Provider)
	require.Equal(t, cfg.Memory.Consolidation.Model, roundTrip.Consolidation.Model)
	require.Equal(t, cfg.Memory.Generation.TurnDigest.ReasoningEffort, roundTrip.Generation.TurnDigest.ReasoningEffort)
	require.Equal(t, cfg.Memory.Consolidation.ReasoningEffort, roundTrip.Consolidation.ReasoningEffort)
	require.Equal(t, cfg.Memory.Core.Rebuild, roundTrip.Core.Rebuild)
	require.Equal(t, cfg.Memory.Injection.Budgets, roundTrip.Injection.Budgets)
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
		{"valid reserved trigger", func(c *MemoryConfig) { c.Generation.Trigger = "turn_threshold" }, true},
		{"negative extraction batch", func(c *MemoryConfig) { c.Maintenance.CaptureExtraction.BatchSize = -1 }, false},
		{"oversized extraction batch", func(c *MemoryConfig) { c.Maintenance.CaptureExtraction.BatchSize = 1001 }, false},
		{"negative extraction interval", func(c *MemoryConfig) { c.Maintenance.CaptureExtraction.Interval = -time.Second }, false},
		{"negative job deadline repair batch", func(c *MemoryConfig) { c.Maintenance.JobDeadlineRepair.BatchSize = -1 }, false},
		{"oversized job deadline repair batch", func(c *MemoryConfig) { c.Maintenance.JobDeadlineRepair.BatchSize = 501 }, false},
		{"negative job deadline repair interval", func(c *MemoryConfig) { c.Maintenance.JobDeadlineRepair.Interval = -time.Second }, false},
		{"negative min idle", func(c *MemoryConfig) { c.Generation.MinIdleHours = -1 }, false},
		{"negative max sessions", func(c *MemoryConfig) { c.Generation.MaxSessionsPerRun = -1 }, false},
		{"negative max rollout", func(c *MemoryConfig) { c.Generation.MaxRolloutAgeDays = -1 }, false},
		{"negative max records", func(c *MemoryConfig) { c.Generation.MaxRecordsPerSession = -1 }, false},
		{"negative turn digest retention", func(c *MemoryConfig) { c.Generation.TurnDigest.RetentionDays = -1 }, false},
		{"negative turn digest batch", func(c *MemoryConfig) { c.Generation.TurnDigest.MaxTurnsPerBatch = -1 }, false},
		{"negative turn window", func(c *MemoryConfig) { c.Generation.TurnDigest.WindowTurns = -1 }, false},
		{"negative window retention", func(c *MemoryConfig) { c.Generation.TurnDigest.WindowRetentionDays = -1 }, false},
		{"negative consolidation records", func(c *MemoryConfig) { c.Consolidation.MaxRecords = -1 }, false},
		{"negative consolidation unused days", func(c *MemoryConfig) { c.Consolidation.MaxUnusedDays = -1 }, false},
		{"negative summary budget", func(c *MemoryConfig) { c.Consolidation.SummaryTokenBudget = -1 }, false},
		{"negative consolidation interval", func(c *MemoryConfig) { c.Consolidation.IntervalHours = -1 }, false},
		{"negative core rebuild interval", func(c *MemoryConfig) { c.Core.Rebuild.Interval = -time.Second }, false},
		{"negative core rebuild records", func(c *MemoryConfig) { c.Core.Rebuild.MaxRecords = -1 }, false},
		{"oversized core rebuild records", func(c *MemoryConfig) { c.Core.Rebuild.MaxRecords = 65 }, false},
		{"oversized core usage rows", func(c *MemoryConfig) { c.Core.Rebuild.MaxUsageRows = 501 }, false},
		{"oversized core token budget", func(c *MemoryConfig) { c.Core.Rebuild.TokenBudget = 4001 }, false},
		{"negative max items", func(c *MemoryConfig) { c.Injection.MaxItems = -1 }, false},
		{"negative token budget", func(c *MemoryConfig) { c.Injection.TokenBudget = -1 }, false},
		{"oversized token budget", func(c *MemoryConfig) { c.Injection.TokenBudget = MaxMemoryInjectionTokenBudget + 1 }, false},
		{"negative category budget", func(c *MemoryConfig) { c.Injection.Budgets.Core = -0.1 }, false},
		{"category budget above one", func(c *MemoryConfig) { c.Injection.Budgets.Core = 1.1 }, false},
		{"category budgets sum above one", func(c *MemoryConfig) {
			c.Injection.Budgets = MemoryInjectionBudgets{
				Core: 0.3, Task: 0.2, Retrieved: 0.3, Procedure: 0.2, Reference: 0.1,
			}
		}, false},
		{"ratio too high", func(c *MemoryConfig) { c.Injection.PinnedBudgetRatio = 1.1 }, false},
		{"ratio negative", func(c *MemoryConfig) { c.Injection.PinnedBudgetRatio = -0.1 }, false},
		{"recall trace sampling too high", func(c *MemoryConfig) { c.Injection.RecallTrace.SampleRate = 1.1 }, false},
		{"recall trace sampling negative", func(c *MemoryConfig) { c.Injection.RecallTrace.SampleRate = -0.1 }, false},
		{"negative recall trace retention", func(c *MemoryConfig) { c.Injection.RecallTrace.RetentionDays = -1 }, false},
		{"sampling too high", func(c *MemoryConfig) { c.Feedback.ExposureSampling = 1.1 }, false},
		{"sampling negative", func(c *MemoryConfig) { c.Feedback.ExposureSampling = -0.1 }, false},
		{"negative exposure retention", func(c *MemoryConfig) { c.Feedback.ExposureRetentionDays = -1 }, false},
		{"negative candidate days", func(c *MemoryConfig) { c.Retention.CandidateDays = -1 }, false},
		{"negative ephemeral days", func(c *MemoryConfig) { c.Retention.EphemeralDays = -1 }, false},
		{"negative deleted days", func(c *MemoryConfig) { c.Retention.DeletedDays = -1 }, false},
		{"negative audit days", func(c *MemoryConfig) { c.Retention.AuditDays = -1 }, false},
		{"negative max records", func(c *MemoryConfig) { c.Retention.MaxRecords = -1 }, false},
		{"invalid stale behavior", func(c *MemoryConfig) { c.Freshness.StaleBehavior = "inject" }, false},
		{"negative freshness ttl", func(c *MemoryConfig) { c.Freshness.DefaultTTLDays = -1 }, false},
		{"negative per-kind ttl", func(c *MemoryConfig) { c.Freshness.PerKind["fact"] = -1 }, false},
		{"duplicate retention class", func(c *MemoryConfig) {
			c.Retention.Classes.Ephemeral = append(c.Retention.Classes.Ephemeral, "decision")
		}, false},
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
