package memoryassembly

import (
	"context"
	"strings"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
)

// MaintenanceHost is the host-owned half of the memory maintenance loop.
// Runtime methods stay on the shared object; only host-specific logging and
// consolidation construction are callbacks.
type MaintenanceHost interface {
	RunIdle(context.Context) error
	RunConsolidation(context.Context) error
}

type MaintenanceOptions struct {
	Runtime *memoryruntime.Runtime
	Config  *runtimecfg.RuntimeConfig
	Host    MaintenanceHost
	OnError func(stage string, err error)
}

// MaintenanceEnabled reports whether any ticker/maintenance feature is on.
func MaintenanceEnabled(opts MaintenanceOptions) bool {
	cfg := deriveMaintenanceSchedule(opts)
	return cfg.idleEnabled || cfg.consolidationEnabled || cfg.retentionEnabled ||
		cfg.lifecycleEnabled || cfg.turnDigestEnabled || cfg.freshnessEnabled
}

// StartMaintenance starts the shared ticker loop and returns a stop function
// that cancels the loop and waits for its goroutine. The returned stop is
// always non-nil.
func StartMaintenance(ctx context.Context, opts MaintenanceOptions) func() {
	if !MaintenanceEnabled(opts) {
		return func() {}
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMaintenance(loopCtx, opts)
	}()
	return func() {
		cancel()
		<-done
	}
}

type maintenanceSchedule struct {
	idleEnabled          bool
	consolidationEnabled bool
	retentionEnabled     bool
	lifecycleEnabled     bool
	freshnessEnabled     bool
	turnDigestEnabled    bool
	startupRecovery      bool
	idleExtractMinutes   int
	freshnessInterval    time.Duration
}

func deriveMaintenanceSchedule(opts MaintenanceOptions) maintenanceSchedule {
	cfg := opts.Config
	rt := opts.Runtime
	result := maintenanceSchedule{}
	if cfg == nil || rt == nil {
		return result
	}
	generation := cfg.Memory.Generation
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	result.idleEnabled = generation.Enabled && (trigger == "idle" || trigger == "both")
	result.consolidationEnabled = cfg.Memory.Consolidation.Enabled
	result.retentionEnabled = cfg.Memory.Injection.RecallTrace.RetentionDays > 0 ||
		cfg.Memory.Feedback.ExposureRetentionDays > 0
	result.lifecycleEnabled = cfg.Memory.Retention.Enabled
	result.freshnessEnabled = cfg.Memory.Freshness.Enabled
	result.freshnessInterval = time.Duration(cfg.Memory.Freshness.RevalidateIntervalHours) * time.Hour
	if result.freshnessInterval <= 0 {
		result.freshnessInterval = 24 * time.Hour
	}
	result.turnDigestEnabled = generation.Enabled && generation.TurnDigest.Enabled
	result.startupRecovery = result.turnDigestEnabled && generation.TurnDigest.RecoveryOnStartup
	result.idleExtractMinutes = rt.Config.TurnDigestIdleExtractMinutes
	if result.idleExtractMinutes <= 0 {
		result.idleExtractMinutes = 30
	}
	return result
}

func runMaintenance(ctx context.Context, opts MaintenanceOptions) {
	cfg := deriveMaintenanceSchedule(opts)
	rt := opts.Runtime
	if rt == nil {
		return
	}
	report := func(stage string, err error) {
		if err != nil && opts.OnError != nil {
			opts.OnError(stage, err)
		}
	}
	if cfg.startupRecovery {
		recoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err := rt.RunPendingTurnDigests(recoveryCtx, memoryruntime.TriggerStartup)
		cancel()
		report("startup_recovery", err)
	}
	if cfg.freshnessEnabled {
		_, err := rt.SweepFreshness(ctx)
		report("freshness", err)
	}
	digestEnabled := cfg.idleEnabled || cfg.turnDigestEnabled
	maintenanceEnabled := cfg.consolidationEnabled || cfg.retentionEnabled || cfg.lifecycleEnabled || cfg.freshnessEnabled
	lastFreshnessSweep := time.Now()
	var digestTicker, maintenanceTicker *time.Ticker
	var digestTick, maintenanceTick <-chan time.Time
	if digestEnabled {
		digestTicker = time.NewTicker(time.Duration(cfg.idleExtractMinutes) * time.Minute)
		digestTick = digestTicker.C
		defer digestTicker.Stop()
	}
	if maintenanceEnabled {
		maintenanceTicker = time.NewTicker(30 * time.Minute)
		maintenanceTick = maintenanceTicker.C
		defer maintenanceTicker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-digestTick:
			if cfg.idleEnabled && opts.Host != nil {
				report("idle", opts.Host.RunIdle(ctx))
			}
			if cfg.turnDigestEnabled {
				_, err := rt.RunPendingTurnDigests(ctx, memoryruntime.TriggerIdle)
				report("turn_digest", err)
				_, err = rt.PruneTurnDigests(ctx)
				report("turn_digest_retention", err)
				_, err = rt.PruneTurnWindows(ctx)
				report("turn_window_retention", err)
			}
		case <-maintenanceTick:
			if cfg.consolidationEnabled && opts.Host != nil {
				report("consolidation", opts.Host.RunConsolidation(ctx))
			}
			if cfg.lifecycleEnabled {
				_, err := rt.Store.SweepLifecycle(ctx, time.Now().UTC())
				report("lifecycle", err)
			}
			if cfg.freshnessEnabled && time.Since(lastFreshnessSweep) >= cfg.freshnessInterval {
				_, err := rt.SweepFreshness(ctx)
				report("freshness", err)
				lastFreshnessSweep = time.Now()
			}
			if cfg.retentionEnabled {
				_, err := rt.Store.PruneDiagnostics(ctx, time.Now().UTC())
				report("diagnostics_retention", err)
			}
		}
	}
}
