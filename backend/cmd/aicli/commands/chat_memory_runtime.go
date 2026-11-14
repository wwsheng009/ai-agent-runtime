package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

func (h *localChatRuntimeHost) initializeMemoryRuntime(session *ChatSession) error {
	if h == nil || h.RuntimeConfig == nil || session == nil || session.SessionManager == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(h.RuntimeConfig.Memory.Mode))
	if mode != "shadow" && mode != "on" {
		return nil
	}
	if h.Bootstrap == nil || h.Bootstrap.LLMRuntime() == nil {
		return fmt.Errorf("memoryruntime: LLM runtime is not configured")
	}
	workspace := resolveLocalWorkspacePath(h.RuntimeConfig, session)
	if strings.TrimSpace(workspace) == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	memoryCfg := h.RuntimeConfig.Memory
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
		return err
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
		ConsolidationReasoningEffort:  memoryCfg.Consolidation.ReasoningEffort,
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
		WorkerID:                       fmt.Sprintf("aicli-%d", os.Getpid()),
	}
	cfg = memorypolicy.ApplyRuntime(memoryCfg, cfg)
	cfg = memoryruntime.WithLLMProjectSummarizer(cfg, h.Bootstrap.LLMRuntime())
	source := &memoryruntime.ChatSessionSource{
		Manager: session.SessionManager, UserID: session.SessionUserID,
	}
	extractor := &memoryruntime.LLMExtractor{Runtime: h.Bootstrap.LLMRuntime(), Config: cfg}
	rt := memoryruntime.New(store, source, extractor, store, cfg)
	rt.WindowSummarizer = &memoryruntime.LLMWindowSummarizer{Runtime: h.Bootstrap.LLMRuntime(), Config: cfg}
	h.MemoryRuntime = rt
	h.cleanupFns = append(h.cleanupFns, func() { _ = store.Close() })
	h.bindMemoryDigestInvalidation()
	return nil
}

// bindMemoryDigestInvalidation prevents pending turn digests and existing
// session-derived memory records from being reused after compaction/backtrack
// changed the canonical transcript.
func (h *localChatRuntimeHost) bindMemoryDigestInvalidation() {
	if h == nil || h.EventBus == nil || h.MemoryRuntime == nil {
		return
	}
	handler := func(event runtimeevents.Event) {
		if strings.TrimSpace(event.SessionID) == "" {
			return
		}
		_, _ = h.MemoryRuntime.InvalidateFromRuntimeEvent(
			context.Background(), event.Type, event.SessionID, event.Payload)
	}
	for _, eventType := range []string{
		runtimechat.EventSessionCompactCompleted,
		runtimechat.EventBacktrackFinished,
	} {
		if unsubscribe := h.EventBus.SubscribeCancelable(eventType, handler); unsubscribe != nil {
			h.cleanupFns = append(h.cleanupFns, unsubscribe)
		}
	}
}

func (h *localChatRuntimeHost) startMemoryIdleSweep() {
	if h == nil || h.MemoryRuntime == nil || h.RuntimeConfig == nil || h.lifecycleCtx == nil {
		return
	}
	generation := h.RuntimeConfig.Memory.Generation
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	idleEnabled := generation.Enabled && (trigger == "idle" || trigger == "both")
	consolidationEnabled := h.RuntimeConfig.Memory.Consolidation.Enabled
	retentionEnabled := h.RuntimeConfig.Memory.Injection.RecallTrace.RetentionDays > 0 ||
		h.RuntimeConfig.Memory.Feedback.ExposureRetentionDays > 0
	lifecycleEnabled := h.RuntimeConfig.Memory.Retention.Enabled
	freshnessEnabled := h.RuntimeConfig.Memory.Freshness.Enabled
	freshnessInterval := time.Duration(h.RuntimeConfig.Memory.Freshness.RevalidateIntervalHours) * time.Hour
	if freshnessInterval <= 0 {
		freshnessInterval = 24 * time.Hour
	}
	turnDigestEnabled := generation.Enabled && generation.TurnDigest.Enabled
	startupRecoveryEnabled := turnDigestEnabled && generation.TurnDigest.RecoveryOnStartup
	idleExtractMinutes := h.MemoryRuntime.Config.TurnDigestIdleExtractMinutes
	if idleExtractMinutes <= 0 {
		idleExtractMinutes = 30
	}
	if !idleEnabled && !consolidationEnabled && !retentionEnabled && !lifecycleEnabled && !turnDigestEnabled && !freshnessEnabled {
		return
	}
	go func() {
		if startupRecoveryEnabled {
			recoveryCtx, recoveryCancel := context.WithTimeout(h.lifecycleCtx, 2*time.Minute)
			_, _ = h.MemoryRuntime.RunPendingTurnDigests(recoveryCtx, memoryruntime.TriggerStartup)
			recoveryCancel()
		}
		if freshnessEnabled {
			_, _ = h.MemoryRuntime.SweepFreshness(h.lifecycleCtx)
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
			case <-h.lifecycleCtx.Done():
				return
			case <-digestTick:
				if idleEnabled {
					_, _ = h.MemoryRuntime.Run(h.lifecycleCtx, memoryruntime.TriggerIdle)
				}
				if turnDigestEnabled {
					_, _ = h.MemoryRuntime.RunPendingTurnDigests(h.lifecycleCtx, memoryruntime.TriggerIdle)
					_, _ = h.MemoryRuntime.PruneTurnDigests(h.lifecycleCtx)
					_, _ = h.MemoryRuntime.PruneTurnWindows(h.lifecycleCtx)
				}
			case <-maintenanceTick:
				if consolidationEnabled {
					consolidator := memoryruntime.NewConsolidator(h.MemoryRuntime.Store, h.MemoryRuntime.Config)
					_, _ = consolidator.Run(h.lifecycleCtx)
				}
				if lifecycleEnabled {
					_, _ = h.MemoryRuntime.Store.SweepLifecycle(h.lifecycleCtx, time.Now().UTC())
				}
				if freshnessEnabled && time.Since(lastFreshnessSweep) >= freshnessInterval {
					_, _ = h.MemoryRuntime.SweepFreshness(h.lifecycleCtx)
					lastFreshnessSweep = time.Now()
				}
				if retentionEnabled {
					_, _ = h.MemoryRuntime.Store.PruneDiagnostics(h.lifecycleCtx, time.Now().UTC())
				}
			}
		}
	}()
}

func (h *localChatRuntimeHost) triggerMemorySessionEnd(sessionID string) {
	if h == nil || h.MemoryRuntime == nil || h.RuntimeConfig == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	generation := h.RuntimeConfig.Memory.Generation
	if !generation.Enabled {
		return
	}
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	if trigger != "session_end" && trigger != "both" {
		return
	}
	h.asyncWG.Add(1)
	go func() {
		defer h.asyncWG.Done()
		ctx := h.lifecycleCtx
		if ctx == nil {
			ctx = context.Background()
		}
		result, err := h.MemoryRuntime.RunSessionEndPipeline(ctx, sessionID, memoryruntime.SessionEndPipelineOptions{
			Consolidate: h.RuntimeConfig.Memory.Consolidation.Enabled,
		})
		if err != nil {
			logpkg.Warnf("memory session-end failed: session=%s stage=%s processed=%d failed=%d rollup_failed=%d",
				sessionID, result.Stage, result.Run.Processed, result.Run.Failed, result.Run.RollupFailed)
		}
	}()
}

// deleteMemoryTurnDigestsForSession is best-effort cleanup for an explicitly
// deleted session. The memory DB and session DB are separate stores.
func deleteMemoryTurnDigestsForSession(session *ChatSession, sessionID string) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.MemoryRuntime == nil {
		return
	}
	_, _ = session.LocalRuntimeHost.MemoryRuntime.DeleteSessionDigests(
		context.Background(), strings.TrimSpace(sessionID))
}
