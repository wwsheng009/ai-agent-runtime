package main

import (
	"context"
	"strings"

	runtimeapi "github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly"
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
	sweepCtx, cancel := context.WithCancel(ctx)
	unsubscribeDigestInvalidation := handler.BindMemoryDigestInvalidation()
	stopMaintenance := memoryassembly.StartMaintenance(sweepCtx, memoryassembly.MaintenanceOptions{
		Runtime: rt,
		Config:  cfg,
		Host:    runtimeServerMemoryMaintenanceHost{handler: handler},
		OnError: func(stage string, err error) {
			logger.Warn("memory maintenance failed: "+stage, logger.Err(err))
		},
	})
	return func() {
		if unsubscribeDigestInvalidation != nil {
			unsubscribeDigestInvalidation()
		}
		cancel()
		stopMaintenance()
	}, nil
}

type runtimeServerMemoryMaintenanceHost struct {
	handler *runtimeapi.Handler
}

func (h runtimeServerMemoryMaintenanceHost) RunIdle(ctx context.Context) error {
	if h.handler == nil {
		return nil
	}
	_, err := h.handler.RunMemoryIdleSweep(ctx)
	return err
}

func (h runtimeServerMemoryMaintenanceHost) RunConsolidation(ctx context.Context) error {
	if h.handler == nil {
		return nil
	}
	_, err := h.handler.RunMemoryConsolidation(ctx)
	return err
}
