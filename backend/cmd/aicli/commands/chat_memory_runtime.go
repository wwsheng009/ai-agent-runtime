package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
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
	})
	if err != nil {
		return err
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
		WorkerID:                   fmt.Sprintf("aicli-%d", os.Getpid()),
	}
	source := &memoryruntime.ChatSessionSource{
		Manager: session.SessionManager, UserID: session.SessionUserID,
	}
	extractor := &memoryruntime.LLMExtractor{Runtime: h.Bootstrap.LLMRuntime(), Config: cfg}
	rt := memoryruntime.New(store, source, extractor, store, cfg)
	h.MemoryRuntime = rt
	h.cleanupFns = append(h.cleanupFns, func() { _ = store.Close() })
	return nil
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
	if !idleEnabled && !consolidationEnabled && !retentionEnabled {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-h.lifecycleCtx.Done():
				return
			case <-ticker.C:
				if idleEnabled {
					_, _ = h.MemoryRuntime.Run(h.lifecycleCtx, memoryruntime.TriggerIdle)
				}
				if consolidationEnabled {
					consolidator := memoryruntime.NewConsolidator(h.MemoryRuntime.Store, h.MemoryRuntime.Config)
					_, _ = consolidator.Run(h.lifecycleCtx)
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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = h.MemoryRuntime.RunSession(ctx, memoryruntime.TriggerSessionEnd, sessionID)
		if !h.RuntimeConfig.Memory.Consolidation.Enabled {
			return
		}
		consolidator := memoryruntime.NewConsolidator(h.MemoryRuntime.Store, h.MemoryRuntime.Config)
		_, _ = consolidator.Run(ctx)
	}()
}
