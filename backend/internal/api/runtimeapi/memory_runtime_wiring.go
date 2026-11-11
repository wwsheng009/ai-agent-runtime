package runtimeapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func (h *Handler) SetMemoryRuntime(runtime *memoryruntime.Runtime) {
	if h == nil {
		return
	}
	h.memoryRuntimeMu.Lock()
	previous := h.memoryRuntime
	h.memoryRuntime = runtime
	h.memoryRuntimeMu.Unlock()
	if previous != nil && previous.Store != nil && previous != runtime {
		_ = previous.Store.Close()
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

// EnableMemoryRuntime constructs the M2 write path from the active runtime
// config. mode=off returns (nil,nil) without creating memory.db.
func (h *Handler) EnableMemoryRuntime() (*memoryruntime.Runtime, error) {
	if h == nil || h.runtimeConfig == nil {
		return nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(h.runtimeConfig.Memory.Mode))
	if mode != "shadow" && mode != "on" {
		return nil, nil
	}
	if h.sessionManager == nil {
		return nil, fmt.Errorf("memoryruntime: session manager is not configured")
	}
	if h.llmRuntime == nil {
		return nil, fmt.Errorf("memoryruntime: LLM runtime is not configured")
	}
	workspace := strings.TrimSpace(h.runtimeConfig.Workspace.Root)
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	memoryCfg := h.runtimeConfig.Memory
	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: workspace, Mode: mode, StorePath: memoryCfg.StorePath,
		InjectionMode:         memoryCfg.Injection.Mode,
		PinnedBudgetRatio:     memoryCfg.Injection.PinnedBudgetRatio,
		DisableStageSplit:     !memoryCfg.Injection.StageSplit,
		DisableInjection:      !memoryCfg.Injection.Enabled,
		DisableManual:         !memoryCfg.Manual.Enabled,
		RecallTraceEnabled:    memoryCfg.Injection.RecallTrace.Enabled,
		RecallTraceSampleRate: memoryCfg.Injection.RecallTrace.SampleRate,
	})
	if err != nil {
		return nil, err
	}
	generation := memoryCfg.Generation
	cfg := memoryruntime.Config{
		Mode: mode, GenerationDisabled: !generation.Enabled,
		Workspace: workspace, ProjectKey: store.ProjectKey(),
		IncludeSubagents: generation.IncludeSubagents, RequireWorkspaceMatch: true,
		MinIdle:                    time.Duration(generation.MinIdleHours) * time.Hour,
		MaxSessionsPerRun:          generation.MaxSessionsPerRun,
		MaxRolloutAge:              time.Duration(generation.MaxRolloutAgeDays) * 24 * time.Hour,
		MaxCandidatesPerSession:    generation.MaxRecordsPerSession,
		MaxAttempts:                3,
		ConsolidationDisabled:      !memoryCfg.Consolidation.Enabled,
		ConsolidationInterval:      time.Duration(memoryCfg.Consolidation.IntervalHours) * time.Hour,
		MaxConsolidationRecords:    memoryCfg.Consolidation.MaxRecords,
		MaxConsolidationUnusedDays: memoryCfg.Consolidation.MaxUnusedDays,
		SummaryTokenBudget:         memoryCfg.Consolidation.SummaryTokenBudget,
		CitationEnabled:            memoryCfg.Feedback.CitationEnabled,
		ExposureSampling:           memoryCfg.Feedback.ExposureSampling,
		ExtractionModel:            generation.Model,
		ExtractionReasoningEffort:  generation.ReasoningEffort,
		WorkerID:                   fmt.Sprintf("runtime-server-%d", os.Getpid()),
	}
	source := &memoryruntime.ChatSessionSource{Manager: h.sessionManager}
	extractor := &memoryruntime.LLMExtractor{Runtime: h.llmRuntime, Config: cfg}
	runtime := memoryruntime.New(store, source, extractor, store, cfg)
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
	h.memoryRuntimeMu.Unlock()
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
// session teardown.
func (h *Handler) TriggerMemorySessionEnd(sessionID string) {
	runtime := h.MemoryRuntime()
	if runtime == nil || h.runtimeConfig == nil || strings.TrimSpace(sessionID) == "" {
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = runtime.RunSession(ctx, memoryruntime.TriggerSessionEnd, sessionID)
		if runtime.Config.ConsolidationDisabled {
			return
		}
		consolidator := memoryruntime.NewConsolidator(runtime.Store, runtime.Config)
		if _, err := consolidator.Run(ctx); err != nil {
			return
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
		WorkerID:                   fmt.Sprintf("runtime-server-%d", os.Getpid()),
	})
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
		WorkerID:                   fmt.Sprintf("runtime-server-%d", os.Getpid()),
	})
	return consolidator.RebuildStaleSummaries(ctx)
}
