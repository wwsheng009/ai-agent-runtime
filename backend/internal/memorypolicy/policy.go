// Package memorypolicy translates runtime configuration into the store and
// runtime policy types. Keeping the mapping in one place prevents the CLI and
// runtime-server hosts from drifting on destructive lifecycle defaults.
package memorypolicy

import (
	"strings"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// StoreRetention builds the store-side lifecycle policy.
func StoreRetention(cfg runtimecfg.MemoryConfig) memorystore.RetentionPolicy {
	return memorystore.RetentionPolicy{
		Enabled:          cfg.Retention.Enabled,
		CandidateDays:    cfg.Retention.CandidateDays,
		EphemeralDays:    cfg.Retention.EphemeralDays,
		DeletedDays:      cfg.Retention.DeletedDays,
		AuditDays:        cfg.Retention.AuditDays,
		MaxRecords:       cfg.Retention.MaxRecords,
		ActiveUnusedDays: cfg.Consolidation.MaxUnusedDays,
		Classes: memorystore.RetentionClasses{
			Permanent: append([]string(nil), cfg.Retention.Classes.Permanent...),
			Standard:  append([]string(nil), cfg.Retention.Classes.Standard...),
			Ephemeral: append([]string(nil), cfg.Retention.Classes.Ephemeral...),
		},
	}
}

// StaleBehavior returns the normalized retrieval freshness policy.
func StaleBehavior(cfg runtimecfg.MemoryConfig) string {
	if !cfg.Freshness.Enabled {
		return "exclude"
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Freshness.StaleBehavior)) {
	case "warn":
		return "warn"
	default:
		return "exclude"
	}
}

// DisableSecretFilter reports the explicit redaction.enabled=false opt-out.
func DisableSecretFilter(cfg runtimecfg.MemoryConfig) bool {
	return !cfg.Redaction.Enabled
}

// ApplyRuntime copies lifecycle/freshness/redaction settings onto a runtime
// Config built by a host. Boolean flags are expressed as negative/default-on
// fields so a zero-value Config keeps the historical safe behavior.
func ApplyRuntime(cfg runtimecfg.MemoryConfig, runtimeCfg memoryruntime.Config) memoryruntime.Config {
	runtimeCfg.RetentionClasses = memorystore.RetentionClasses{
		Permanent: append([]string(nil), cfg.Retention.Classes.Permanent...),
		Standard:  append([]string(nil), cfg.Retention.Classes.Standard...),
		Ephemeral: append([]string(nil), cfg.Retention.Classes.Ephemeral...),
	}
	runtimeCfg.FreshnessDisabled = !cfg.Freshness.Enabled
	runtimeCfg.FreshnessDefaultTTL = days(cfg.Freshness.DefaultTTLDays)
	runtimeCfg.FreshnessStaleBehavior = StaleBehavior(cfg)
	runtimeCfg.FreshnessRevalidateBatchSize = cfg.Freshness.RevalidateBatchSize
	if len(cfg.Freshness.PerKind) > 0 {
		runtimeCfg.FreshnessPerKind = make(map[string]time.Duration, len(cfg.Freshness.PerKind))
		for kind, days := range cfg.Freshness.PerKind {
			runtimeCfg.FreshnessPerKind[strings.ToLower(strings.TrimSpace(kind))] = daysDuration(days)
		}
	}
	runtimeCfg.RedactionConfigured = true
	runtimeCfg.RedactionEnabled = cfg.Redaction.Enabled
	runtimeCfg.RedactionDropOnSecret = cfg.Redaction.DropOnSecret
	return runtimeCfg
}

func days(days int) time.Duration {
	return daysDuration(days)
}

func daysDuration(days int) time.Duration {
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}
