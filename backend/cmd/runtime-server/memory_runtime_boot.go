package main

import (
	"context"
	"strings"
	"time"

	runtimeapi "github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// startRuntimeServerMemoryRuntime wires the M2 write path and starts the
// generation/consolidation ticker. mode=off returns (nil,nil) without creating
// memory.db.
func startRuntimeServerMemoryRuntime(ctx context.Context, handler *runtimeapi.Handler, cfg *runtimecfg.RuntimeConfig) (context.CancelFunc, error) {
	if handler == nil || cfg == nil {
		return nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Memory.Mode))
	if mode != "shadow" && mode != "on" {
		return nil, nil
	}
	rt, err := handler.EnableMemoryRuntime()
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, nil
	}
	handler.SetMemoryRuntimeLifecycleContext(ctx)
	trigger := strings.ToLower(strings.TrimSpace(cfg.Memory.Generation.Trigger))
	idleEnabled := cfg.Memory.Generation.Enabled && (trigger == "idle" || trigger == "both")
	consolidationEnabled := cfg.Memory.Consolidation.Enabled
	retentionEnabled := cfg.Memory.Injection.RecallTrace.RetentionDays > 0 ||
		cfg.Memory.Feedback.ExposureRetentionDays > 0
	lifecycleEnabled := cfg.Memory.Retention.Enabled
	freshnessEnabled := cfg.Memory.Freshness.Enabled
	freshnessInterval := time.Duration(cfg.Memory.Freshness.RevalidateIntervalHours) * time.Hour
	if freshnessInterval <= 0 {
		freshnessInterval = 24 * time.Hour
	}
	turnDigestEnabled := cfg.Memory.Generation.Enabled && cfg.Memory.Generation.TurnDigest.Enabled
	startupRecoveryEnabled := turnDigestEnabled && cfg.Memory.Generation.TurnDigest.RecoveryOnStartup
	idleExtractMinutes := rt.Config.TurnDigestIdleExtractMinutes
	if idleExtractMinutes <= 0 {
		idleExtractMinutes = 30
	}
	if !idleEnabled && !consolidationEnabled && !retentionEnabled && !lifecycleEnabled && !turnDigestEnabled && !freshnessEnabled {
		return nil, nil
	}
	sweepCtx, cancel := context.WithCancel(ctx)
	unsubscribeDigestInvalidation := handler.BindMemoryDigestInvalidation()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if startupRecoveryEnabled {
			recoveryCtx, recoveryCancel := context.WithTimeout(sweepCtx, 2*time.Minute)
			_, _ = rt.RunPendingTurnDigests(recoveryCtx, memoryruntime.TriggerStartup)
			recoveryCancel()
		}
		if freshnessEnabled {
			if _, err := rt.SweepFreshness(sweepCtx); err != nil {
				logger.Warn("memory freshness sweep failed", logger.Err(err))
			}
		}
		digestEnabled := idleEnabled || turnDigestEnabled
		maintenanceEnabled := consolidationEnabled || retentionEnabled || lifecycleEnabled || freshnessEnabled
		lastFreshnessSweep := time.Now()
		var digestTicker, maintenanceTicker *time.Ticker
		var digestTick, maintenanceTick <-chan time.Time
		if digestEnabled {
			digestTicker = time.NewTicker(time.Duration(idleExtractMinutes) * time.Minute)
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
			case <-sweepCtx.Done():
				return
			case <-digestTick:
				if idleEnabled {
					if _, err := handler.RunMemoryIdleSweep(sweepCtx); err != nil {
						logger.Warn("memory idle sweep failed", logger.Err(err))
					}
				}
				if turnDigestEnabled {
					if _, err := rt.RunPendingTurnDigests(sweepCtx, memoryruntime.TriggerIdle); err != nil {
						logger.Warn("memory turn digest recovery failed", logger.Err(err))
					}
					if _, err := rt.PruneTurnDigests(sweepCtx); err != nil {
						logger.Warn("memory turn digest retention failed", logger.Err(err))
					}
					if _, err := rt.PruneTurnWindows(sweepCtx); err != nil {
						logger.Warn("memory turn window retention failed", logger.Err(err))
					}
				}
			case <-maintenanceTick:
				if consolidationEnabled {
					if _, err := handler.RunMemoryConsolidation(sweepCtx); err != nil {
						logger.Warn("memory consolidation failed", logger.Err(err))
					}
				}
				if lifecycleEnabled {
					if _, err := rt.Store.SweepLifecycle(sweepCtx, time.Now().UTC()); err != nil {
						logger.Warn("memory lifecycle retention failed", logger.Err(err))
					}
				}
				if freshnessEnabled && time.Since(lastFreshnessSweep) >= freshnessInterval {
					if _, err := rt.SweepFreshness(sweepCtx); err != nil {
						logger.Warn("memory freshness sweep failed", logger.Err(err))
					}
					lastFreshnessSweep = time.Now()
				}
				if retentionEnabled {
					if _, err := rt.Store.PruneDiagnostics(sweepCtx, time.Now().UTC()); err != nil {
						logger.Warn("memory diagnostics retention failed", logger.Err(err))
					}
				}
			}
		}
	}()
	return func() {
		if unsubscribeDigestInvalidation != nil {
			unsubscribeDigestInvalidation()
		}
		cancel()
		<-done
	}, nil
}
