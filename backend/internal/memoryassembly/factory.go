package memoryassembly

import (
	"fmt"
	"os"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// BuildOptions are the host inputs for the memory v2 runtime. Workspace and
// WorkerID are host-resolved; everything else comes from runtime.yaml.
type BuildOptions struct {
	RuntimeConfig  *runtimecfg.RuntimeConfig
	Workspace      string
	SessionManager *runtimechat.SessionManager
	LLMRuntime     *llm.LLMRuntime
	UserID         string
	WorkerID       string
}

// BuildFromRuntimeConfig constructs the memory write/read runtime shared by
// runtime-server and aicli. mode=off returns (nil, nil) without creating the
// memory store.
func BuildFromRuntimeConfig(opts BuildOptions) (*memoryruntime.Runtime, error) {
	if opts.RuntimeConfig == nil {
		return nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(opts.RuntimeConfig.Memory.Mode))
	if mode != "shadow" && mode != "on" {
		return nil, nil
	}
	if opts.SessionManager == nil {
		return nil, fmt.Errorf("memoryruntime: session manager is not configured")
	}
	if opts.LLMRuntime == nil {
		return nil, fmt.Errorf("memoryruntime: LLM runtime is not configured")
	}
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	memoryCfg := opts.RuntimeConfig.Memory
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
		WorkerID:                       strings.TrimSpace(opts.WorkerID),
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = fmt.Sprintf("memoryruntime-%d", os.Getpid())
	}
	cfg = memorypolicy.ApplyRuntime(memoryCfg, cfg)
	cfg = memoryruntime.WithLLMProjectSummarizer(cfg, opts.LLMRuntime)
	source := &memoryruntime.ChatSessionSource{Manager: opts.SessionManager, UserID: opts.UserID}
	extractor := &memoryruntime.LLMExtractor{Runtime: opts.LLMRuntime, Config: cfg}
	runtime := memoryruntime.New(store, source, extractor, store, cfg)
	runtime.WindowSummarizer = &memoryruntime.LLMWindowSummarizer{Runtime: opts.LLMRuntime, Config: cfg}
	return runtime, nil
}
