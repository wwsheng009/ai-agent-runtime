package runtimeapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

func (h *Handler) SetMemoryRuntime(runtime *memoryruntime.Runtime) {
	if h == nil {
		return
	}
	h.memoryRuntimeMu.Lock()
	previous := h.memoryRuntime
	var cancel context.CancelFunc
	if previous != nil && previous != runtime {
		h.memorySessionEndClosing = true
		cancel = h.memorySessionEndCancel
		h.memorySessionEndCancel = nil
		h.memorySessionEndCtx = nil
	}
	h.memoryRuntime = runtime
	if runtime != nil && (previous == nil || previous == runtime) {
		h.memorySessionEndClosing = false
	}
	h.memoryRuntimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if previous != nil && previous != runtime {
		h.memorySessionEndWG.Wait()
		if runtime != nil {
			h.memoryRuntimeMu.Lock()
			h.memorySessionEndClosing = false
			h.memoryRuntimeMu.Unlock()
		}
	}
	if previous != nil && previous.Store != nil && previous != runtime {
		_ = previous.Store.Close()
	}
	if h.sessionManager != nil {
		wireSessionMemoryPurgeCoordinator(h.sessionManager, runtime)
	}
}

func wireSessionMemoryPurgeCoordinator(manager *runtimechat.SessionManager, runtime *memoryruntime.Runtime) {
	if manager == nil {
		return
	}
	var coordinator runtimechat.SessionPurgeCoordinatorV2
	if runtime != nil && runtime.Store != nil {
		coordinator = func(
			ctx context.Context,
			sessionID string,
			sessionGeneration, purgeGeneration int64,
		) error {
			_, err := runtime.PurgeSessionWithGenerations(
				ctx,
				sessionID,
				sessionGeneration,
				purgeGeneration,
			)
			return err
		}
	}
	manager.SetSessionPurgeCoordinatorV2(coordinator)
	if coordinator == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := manager.ResumePendingSessionPurges(ctx); err != nil {
		logpkg.Warnf("memory session purge recovery failed: %v", err)
	}
}

// SetMemoryRuntimeLifecycleContext supplies the process/service lifetime for
// asynchronous session-end work. It is deliberately not a request context:
// closing one session must not cancel its durable extraction, while server
// shutdown must still stop new work and let existing work drain before the
// memory store is closed.
func (h *Handler) SetMemoryRuntimeLifecycleContext(ctx context.Context) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lifecycleCtx, cancel := context.WithCancel(ctx)
	h.memoryRuntimeMu.Lock()
	previousCancel := h.memorySessionEndCancel
	h.memorySessionEndCtx = lifecycleCtx
	h.memorySessionEndCancel = cancel
	h.memorySessionEndClosing = false
	h.memoryRuntimeMu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
}

func (h *Handler) MemoryRuntime() *memoryruntime.Runtime {
	if h == nil {
		return nil
	}
	h.memoryRuntimeMu.RLock()
	defer h.memoryRuntimeMu.RUnlock()
	return h.memoryRuntime
}

// SetMemoryRuntimeBootError publishes a bounded diagnostic for a configured
// Memory runtime that failed to initialize. The detail is redacted here and
// the status endpoint still restricts it to admins.
func (h *Handler) SetMemoryRuntimeBootError(err error) {
	if h == nil {
		return
	}
	message := ""
	if err != nil {
		message = sanitizeMemoryDiagnosticText(err.Error())
	}
	h.memoryRuntimeMu.Lock()
	h.memoryRuntimeBootError = message
	h.memoryRuntimeMu.Unlock()
}

func (h *Handler) MemoryRuntimeBootError() string {
	if h == nil {
		return ""
	}
	h.memoryRuntimeMu.RLock()
	defer h.memoryRuntimeMu.RUnlock()
	return h.memoryRuntimeBootError
}

// BindMemoryDigestInvalidation prevents pending turn digests and existing
// session-derived memory records from being reused after compaction/backtrack
// changed the canonical transcript. It returns an unsubscribe function.
func (h *Handler) BindMemoryDigestInvalidation() func() {
	if h == nil || h.MemoryRuntime() == nil {
		return nil
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil
	}
	handler := func(event runtimeevents.Event) {
		if strings.TrimSpace(event.SessionID) == "" {
			return
		}
		runtime := h.MemoryRuntime()
		if runtime == nil {
			return
		}
		_, _ = runtime.InvalidateFromRuntimeEvent(
			context.Background(), event.Type, event.SessionID, event.Payload)
	}
	unsubs := make([]func(), 0, 2)
	for _, eventType := range []string{
		runtimechat.EventSessionCompactCompleted,
		runtimechat.EventBacktrackFinished,
	} {
		if unsubscribe := bus.SubscribeCancelable(eventType, handler); unsubscribe != nil {
			unsubs = append(unsubs, unsubscribe)
		}
	}
	return func() {
		for _, unsubscribe := range unsubs {
			unsubscribe()
		}
	}
}

// EnableMemoryRuntime constructs the M2 write path from the active runtime
// config. mode=off returns (nil,nil) without creating memory.db.
func (h *Handler) EnableMemoryRuntime() (*memoryruntime.Runtime, error) {
	if h == nil || h.runtimeConfig == nil {
		return nil, nil
	}
	workspace := strings.TrimSpace(h.runtimeConfig.Workspace.Root)
	runtime, err := memoryassembly.BuildFromRuntimeConfig(memoryassembly.BuildOptions{
		RuntimeConfig:  h.runtimeConfig,
		Workspace:      workspace,
		SessionManager: h.sessionManager,
		LLMRuntime:     h.llmRuntime,
		WorkerID:       fmt.Sprintf("runtime-server-%d", os.Getpid()),
	})
	if err != nil || runtime == nil {
		return nil, err
	}
	h.SetMemoryRuntime(runtime)
	return runtime, nil
}

func (h *Handler) CloseMemoryRuntime() error {
	if h == nil {
		return nil
	}
	h.memoryRuntimeMu.Lock()
	runtime := h.memoryRuntime
	h.memoryRuntime = nil
	cancel := h.memorySessionEndCancel
	h.memorySessionEndCancel = nil
	h.memorySessionEndCtx = nil
	h.memorySessionEndClosing = true
	h.memoryRuntimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.memorySessionEndWG.Wait()
	if runtime != nil && runtime.Store != nil {
		return runtime.Store.Close()
	}
	return nil
}

// RunMemoryIdleSweep is the runtime-server idle trigger. It is a no-op when
// generation is disabled or trigger excludes idle.
func (h *Handler) RunMemoryIdleSweep(ctx context.Context) (memoryruntime.RunResult, error) {
	runtime := h.MemoryRuntime()
	if runtime == nil {
		return memoryruntime.RunResult{}, nil
	}
	if h.runtimeConfig == nil {
		return memoryruntime.RunResult{}, nil
	}
	generation := h.runtimeConfig.Memory.Generation
	if !generation.Enabled {
		return memoryruntime.RunResult{}, nil
	}
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	if trigger != "idle" && trigger != "both" {
		return memoryruntime.RunResult{}, nil
	}
	return runtime.Run(ctx, memoryruntime.TriggerIdle)
}

// TriggerMemorySessionEnd handles the session-close trigger. It intentionally
// runs asynchronously at the caller so transcript extraction cannot block
// session teardown, while still being bounded by the service lifetime.
func (h *Handler) TriggerMemorySessionEnd(sessionID string) {
	if h == nil || strings.TrimSpace(sessionID) == "" || h.runtimeConfig == nil {
		return
	}
	generation := h.runtimeConfig.Memory.Generation
	if !generation.Enabled {
		return
	}
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	if trigger != "session_end" && trigger != "both" {
		return
	}
	h.memoryRuntimeMu.Lock()
	runtime := h.memoryRuntime
	if runtime == nil || h.memorySessionEndClosing {
		h.memoryRuntimeMu.Unlock()
		return
	}
	lifecycleCtx := h.memorySessionEndCtx
	if lifecycleCtx == nil {
		lifecycleCtx, h.memorySessionEndCancel = context.WithCancel(context.Background())
		h.memorySessionEndCtx = lifecycleCtx
	}
	if lifecycleCtx.Err() != nil {
		h.memoryRuntimeMu.Unlock()
		return
	}
	h.memorySessionEndWG.Add(1)
	h.memoryRuntimeMu.Unlock()
	go func() {
		defer h.memorySessionEndWG.Done()
		result, err := runtime.RunSessionEndPipeline(lifecycleCtx, sessionID, memoryruntime.SessionEndPipelineOptions{
			Consolidate: !runtime.Config.ConsolidationDisabled,
		})
		if err != nil {
			logpkg.Warnf("memory session-end failed: session=%s stage=%s processed=%d failed=%d rollup_failed=%d",
				sessionID, result.Stage, result.Run.Processed, result.Run.Failed, result.Run.RollupFailed)
		}
	}()
}

func (h *Handler) RunMemoryConsolidation(ctx context.Context) (memoryruntime.ConsolidationResult, error) {
	runtime := h.MemoryRuntime()
	if runtime == nil {
		var err error
		runtime, err = h.EnableMemoryRuntime()
		if err != nil {
			return memoryruntime.ConsolidationResult{}, err
		}
	}
	if runtime == nil || runtime.Store == nil {
		return memoryruntime.ConsolidationResult{}, nil
	}
	consolidator := memoryruntime.NewConsolidator(runtime.Store, runtime.Config)
	return consolidator.Run(ctx)
}

func (h *Handler) RunMemoryConsolidationForWorkspace(ctx context.Context, workspace string) (memoryruntime.ConsolidationResult, error) {
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		return memoryruntime.ConsolidationResult{}, err
	}
	defer store.Close()
	mode := "off"
	consolidation := runtimecfg.MemoryConsolidationConfig{}
	if h.runtimeConfig != nil {
		mode = strings.ToLower(strings.TrimSpace(h.runtimeConfig.Memory.Mode))
		consolidation = h.runtimeConfig.Memory.Consolidation
	}
	consolidator := memoryruntime.NewConsolidator(store, memoryruntime.Config{
		Mode: mode, ProjectKey: store.ProjectKey(),
		ConsolidationDisabled:      !consolidation.Enabled,
		ConsolidationInterval:      time.Duration(consolidation.IntervalHours) * time.Hour,
		MaxConsolidationRecords:    consolidation.MaxRecords,
		MaxConsolidationUnusedDays: consolidation.MaxUnusedDays,
		SummaryTokenBudget:         consolidation.SummaryTokenBudget,
		ConsolidationModel:         consolidation.Model,
		WorkerID:                   fmt.Sprintf("runtime-server-%d", os.Getpid()),
	})
	if h.llmRuntime != nil {
		consolidator.Config = memoryruntime.WithLLMProjectSummarizer(consolidator.Config, h.llmRuntime)
	}
	return consolidator.Run(ctx)
}

func (h *Handler) RebuildMemorySummariesForWorkspace(ctx context.Context, workspace string) (memoryruntime.ConsolidationResult, error) {
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		return memoryruntime.ConsolidationResult{}, err
	}
	defer store.Close()
	mode := "off"
	consolidation := runtimecfg.MemoryConsolidationConfig{}
	if h.runtimeConfig != nil {
		mode = strings.ToLower(strings.TrimSpace(h.runtimeConfig.Memory.Mode))
		consolidation = h.runtimeConfig.Memory.Consolidation
	}
	consolidator := memoryruntime.NewConsolidator(store, memoryruntime.Config{
		Mode: mode, ProjectKey: store.ProjectKey(),
		ConsolidationDisabled:      !consolidation.Enabled,
		ConsolidationInterval:      time.Duration(consolidation.IntervalHours) * time.Hour,
		MaxConsolidationRecords:    consolidation.MaxRecords,
		MaxConsolidationUnusedDays: consolidation.MaxUnusedDays,
		SummaryTokenBudget:         consolidation.SummaryTokenBudget,
		ConsolidationModel:         consolidation.Model,
		WorkerID:                   fmt.Sprintf("runtime-server-%d", os.Getpid()),
	})
	if h.llmRuntime != nil {
		consolidator.Config = memoryruntime.WithLLMProjectSummarizer(consolidator.Config, h.llmRuntime)
	}
	return consolidator.RebuildStaleSummaries(ctx)
}

// RebuildMemoryCoreForWorkspace runs one bounded, operator-triggered Core
// rebuild using the active runtime configuration. The store method only
// creates a reviewable candidate; it never activates or replaces the active
// Core.
func (h *Handler) RebuildMemoryCoreForWorkspace(ctx context.Context, workspace string) (memoryruntime.CoreRebuildResult, error) {
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		return memoryruntime.CoreRebuildResult{}, err
	}
	defer store.Close()

	mode := "off"
	core := runtimecfg.MemoryCoreConfig{}
	if h.runtimeConfig != nil {
		mode = strings.ToLower(strings.TrimSpace(h.runtimeConfig.Memory.Mode))
		core = h.runtimeConfig.Memory.Core
	}
	cfg := memoryruntime.Config{
		Mode:                    mode,
		ProjectKey:              store.ProjectKey(),
		CoreRebuildMaxRecords:   core.Rebuild.MaxRecords,
		CoreRebuildMaxUsageRows: core.Rebuild.MaxUsageRows,
		CoreRebuildTokenBudget:  core.Rebuild.TokenBudget,
		MaxAttempts:             3,
		WorkerID:                fmt.Sprintf("runtime-server-core-%d", os.Getpid()),
	}
	return memoryruntime.NewCoreRebuilder(store, cfg).Run(ctx)
}
