package main

import (
	"context"
	"strings"
	"time"

	runtimeapi "github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
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
	trigger := strings.ToLower(strings.TrimSpace(cfg.Memory.Generation.Trigger))
	idleEnabled := cfg.Memory.Generation.Enabled && (trigger == "idle" || trigger == "both")
	consolidationEnabled := cfg.Memory.Consolidation.Enabled
	retentionEnabled := cfg.Memory.Injection.RecallTrace.RetentionDays > 0 ||
		cfg.Memory.Feedback.ExposureRetentionDays > 0
	if !idleEnabled && !consolidationEnabled && !retentionEnabled {
		return nil, nil
	}
	sweepCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-sweepCtx.Done():
				return
			case <-ticker.C:
				if idleEnabled {
					if _, err := handler.RunMemoryIdleSweep(sweepCtx); err != nil {
						logger.Warn("memory idle sweep failed", logger.Err(err))
					}
				}
				if consolidationEnabled {
					if _, err := handler.RunMemoryConsolidation(sweepCtx); err != nil {
						logger.Warn("memory consolidation failed", logger.Err(err))
					}
				}
				if retentionEnabled {
					if _, err := rt.Store.PruneDiagnostics(sweepCtx, time.Now().UTC()); err != nil {
						logger.Warn("memory diagnostics retention failed", logger.Err(err))
					}
				}
			}
		}
	}()
	return cancel, nil
}
