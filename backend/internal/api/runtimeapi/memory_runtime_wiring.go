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
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
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

// deleteMemoryTurnDigestsForSession is best-effort cleanup for an explicitly
// deleted session. The memory DB and session DB are separate stores.
func (h *Handler) deleteMemoryTurnDigestsForSession(sessionID string) {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if runtime := h.MemoryRuntime(); runtime != nil {
		_, _ = runtime.DeleteSessionDigests(context.Background(), sessionID)
	}
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
		InjectionMode:            memoryCfg.Injection.Mode,
		PinnedBudgetRatio:        memoryCfg.Injection.PinnedBudgetRatio,
		DisableStageSplit:        !memoryCfg.Injection.StageSplit,
		DisableInjection:         !memoryCfg.Injection.Enabled,
		DisableManual:            !memoryCfg.Manual.Enabled,
		RecallTraceEnabled:       memoryCfg.Injection.RecallTrace.Enabled,
		RecallTraceSampleRate:    memoryCfg.Injection.RecallTrace.SampleRate,
		RecallTraceRetentionDays: memoryCfg.Injection.RecallTrace.RetentionDays,
		ExposureRetentionDays:    memoryCfg.Feedback.ExposureRetentionDays,
		Retention:                memorypolicy.StoreRetention(memoryCfg),
		StaleBehavior:            memorypolicy.StaleBehavior(memoryCfg),
		DisableSecretFilter:      memorypolicy.DisableSecretFilter(memoryCfg),
		TurnDigestBatchSize:      memoryCfg.Generation.TurnDigest.MaxTurnsPerBatch,
	})
	if err != nil {
		return nil, err
	}
	generation := memoryCfg.Generation
	cfg := memoryruntime.Config{
		Mode: mode, GenerationDisabled: !generation.Enabled,
		Workspace: workspace, ProjectKey: store.ProjectKey(),
		IncludeSubagents: generation.IncludeSubagents, RequireWorkspaceMatch: true,
		MinIdle:                        time.Duration(generation.MinIdleHours) * time.Hour,
		MaxSessionsPerRun:              generation.MaxSessionsPerRun,
		MaxRolloutAge:                  time.Duration(generation.MaxRolloutAgeDays) * 24 * time.Hour,
		MaxCandidatesPerSession:        generation.MaxRecordsPerSession,
		MaxAttempts:                    3,
		ConsolidationDisabled:          !memoryCfg.Consolidation.Enabled,
		ConsolidationInterval:          time.Duration(memoryCfg.Consolidation.IntervalHours) * time.Hour,
		MaxConsolidationRecords:        memoryCfg.Consolidation.MaxRecords,
		MaxConsolidationUnusedDays:     memoryCfg.Consolidation.MaxUnusedDays,
		SummaryTokenBudget:             memoryCfg.Consolidation.SummaryTokenBudget,
		ConsolidationModel:             memoryCfg.Consolidation.Model,
		ConsolidationReasoningEffort:   memoryCfg.Consolidation.ReasoningEffort,
		CitationEnabled:                memoryCfg.Feedback.CitationEnabled,
		ExposureSampling:               memoryCfg.Feedback.ExposureSampling,
		ExtractionModel:                generation.Model,
		ExtractionReasoningEffort:      generation.ReasoningEffort,
		WindowSummaryReasoningEffort:   generation.TurnDigest.ReasoningEffort,
		TurnDigestEnabled:              generation.TurnDigest.Enabled,
		TurnDigestStoreText:            generation.TurnDigest.StoreText,
		TurnDigestMaxUserChars:         generation.TurnDigest.MaxUserChars,
		TurnDigestMaxAssistantChars:    generation.TurnDigest.MaxAssistantChars,
		TurnDigestRetentionDays:        generation.TurnDigest.RetentionDays,
		TurnDigestMaxPendingPerSession: generation.TurnDigest.MaxPendingPerSession,
		TurnDigestMaxTurnsPerBatch:     generation.TurnDigest.MaxTurnsPerBatch,
		TurnDigestMaxBytesPerBatch:     generation.TurnDigest.MaxBytesPerBatch,
		TurnDigestMaxDigestsPerRun:     generation.TurnDigest.MaxDigestsPerRun,
		TurnDigestExtractEveryNTurns:   generation.TurnDigest.ExtractEveryNTurns,
		TurnDigestIdleExtractMinutes:   generation.TurnDigest.IdleExtractMinutes,
		TurnDigestRecoveryOnStartup:    generation.TurnDigest.RecoveryOnStartup,
		TurnDigestWindowSummaryEnabled: generation.TurnDigest.WindowSummaryEnabled,
		TurnDigestWindowTurns:          generation.TurnDigest.WindowTurns,
		TurnDigestWindowRetentionDays:  generation.TurnDigest.WindowRetentionDays,
		WorkerID:                       fmt.Sprintf("runtime-server-%d", os.Getpid()),
	}
	cfg = memorypolicy.ApplyRuntime(memoryCfg, cfg)
	cfg = memoryruntime.WithLLMProjectSummarizer(cfg, h.llmRuntime)
	source := &memoryruntime.ChatSessionSource{Manager: h.sessionManager}
	extractor := &memoryruntime.LLMExtractor{Runtime: h.llmRuntime, Config: cfg}
	runtime := memoryruntime.New(store, source, extractor, store, cfg)
	runtime.WindowSummarizer = &memoryruntime.LLMWindowSummarizer{Runtime: h.llmRuntime, Config: cfg}
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
