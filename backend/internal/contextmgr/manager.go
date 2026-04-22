package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/memory"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

const (
	BudgetProfileCompact  = "compact"
	BudgetProfileBalanced = "balanced"
	BudgetProfileExtended = "extended"

	BudgetProfileHot  = "hot"
	BudgetProfileWarm = "warm"
	BudgetProfileCold = "cold"
)

const (
	CompactionModeSummary         = "summary"
	CompactionModeLedgerPreferred = "ledger_preferred"

	RecallModeDisabled = "disabled"
	RecallModeSignals  = "signals"
	RecallModeBroad    = "broad"

	ObservationModeAll      = "all"
	ObservationModeFailures = "failures"

	WorkspaceModeDisabled = "disabled"
	WorkspaceModeSignals  = "signals"
	WorkspaceModeBroad    = "broad"
)

const (
	ledgerCheckpointReason         = "history_window"
	ledgerCheckpointSegmentReason  = "history_window_segment"
	summaryCheckpointSegmentReason = "history_window_summary_segment"

	ledgerCheckpointSegmentStartKey = "segment_start"
	ledgerCheckpointSegmentEndKey   = "segment_end"
	compactionSummaryTextKey        = "summary_text"
)

// TokenCounter 允许 context manager 复用不同 tokenizer。
type TokenCounter func([]types.Message) int

// ArtifactSearcher 约束 recall 所需的最小 artifact 能力。
type ArtifactSearcher interface {
	Search(ctx context.Context, sessionID, query string, limit int) ([]artifact.SearchResult, error)
}

// TeamContextBuilder builds shared team context for prompts.
type TeamContextBuilder interface {
	Build(ctx context.Context, teamID, taskID string, budget int) (*team.ContextDigest, error)
}

// WorkspaceContextBuilder builds workspace recall context.
type WorkspaceContextBuilder interface {
	Build(query string) *workspace.WorkspaceContext
}

// LedgerStore 约束持久 ledger/checkpoint 所需的最小能力。
type LedgerStore interface {
	InsertMemoryEntry(ctx context.Context, entry artifact.MemoryEntry) (string, error)
	LoadMemoryEntries(ctx context.Context, sessionID string, kinds []string, limit int) ([]artifact.MemoryEntry, error)
	SaveCheckpoint(ctx context.Context, checkpoint artifact.Checkpoint) (string, error)
	LatestCheckpoint(ctx context.Context, sessionID string) (*artifact.Checkpoint, error)
}

type ledgerCheckpointLister interface {
	ListCheckpoints(ctx context.Context, sessionID string, limit, offset int) ([]artifact.Checkpoint, error)
}

type matchedLedgerCheckpoint struct {
	Checkpoint artifact.Checkpoint
	Start      int
	End        int
}

// Budget 描述一次请求允许消耗的上下文预算。
type Budget struct {
	MaxPromptTokens     int
	MaxMessages         int
	KeepRecentMessages  int
	MaxRecallResults    int
	MaxObservationItems int
}

// BuildInput 描述一次上下文装配请求。
type BuildInput struct {
	TraceID      string
	SessionID    string
	TaskID       string
	TeamID       string
	Profile      map[string]interface{}
	Goal         string
	History      []types.Message
	Memory       *memory.Memory
	Observations []types.Observation
	CountTokens  TokenCounter
}

// BuildResult 返回上下文装配结果和元信息。
type BuildResult struct {
	Messages []types.Message
	Metadata map[string]interface{}
}

type Strategy struct {
	Profile                 string
	CompactionMode          string
	RecallMode              string
	ObservationMode         string
	WorkspaceMode           string
	MinCompactionMessages   int
	MinRecallQueryLength    int
	MinWorkspaceQueryLength int
	LedgerLoadLimit         int
}

type LayerSpec struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	MaxMessages int      `json:"max_messages,omitempty"`
	MaxItems    int      `json:"max_items,omitempty"`
	Mode        string   `json:"mode,omitempty"`
}

type LayerPlan struct {
	Profile        string    `json:"profile"`
	ProfileContext LayerSpec `json:"profile_context"`
	Hot            LayerSpec `json:"hot"`
	Warm           LayerSpec `json:"warm"`
	Cold           LayerSpec `json:"cold"`
}

// Manager 实现 P1 级别的 admission/compaction/recall。
type Manager struct {
	Budget      Budget
	Strategy    Strategy
	Artifact    ArtifactSearcher
	Ledger      LedgerStore
	TeamContext TeamContextBuilder
	Workspace   WorkspaceContextBuilder
	Events      runtimeevents.Publisher
	Agent       string
}

// DefaultBudget 返回保守的默认预算。
func DefaultBudget() Budget {
	return Budget{
		MaxPromptTokens:     12000,
		MaxMessages:         24,
		KeepRecentMessages:  8,
		MaxRecallResults:    3,
		MaxObservationItems: 6,
	}
}

func BudgetForProfile(profile string) Budget {
	switch normalizeBudgetProfile(profile) {
	case BudgetProfileCompact:
		return Budget{
			MaxPromptTokens:     8000,
			MaxMessages:         16,
			KeepRecentMessages:  5,
			MaxRecallResults:    2,
			MaxObservationItems: 4,
		}
	case BudgetProfileExtended:
		return Budget{
			MaxPromptTokens:     20000,
			MaxMessages:         40,
			KeepRecentMessages:  12,
			MaxRecallResults:    5,
			MaxObservationItems: 8,
		}
	default:
		return DefaultBudget()
	}
}

func ResolveBudget(profile string, overrides Budget) Budget {
	budget := BudgetForProfile(profile)
	if overrides.MaxPromptTokens > 0 {
		budget.MaxPromptTokens = overrides.MaxPromptTokens
	}
	if overrides.MaxMessages > 0 {
		budget.MaxMessages = overrides.MaxMessages
	}
	if overrides.KeepRecentMessages > 0 {
		budget.KeepRecentMessages = overrides.KeepRecentMessages
	}
	if overrides.MaxRecallResults > 0 {
		budget.MaxRecallResults = overrides.MaxRecallResults
	}
	if overrides.MaxObservationItems > 0 {
		budget.MaxObservationItems = overrides.MaxObservationItems
	}
	return budget
}

func StrategyForProfile(profile string) Strategy {
	switch normalizeBudgetProfile(profile) {
	case BudgetProfileCompact:
		return Strategy{
			Profile:                 BudgetProfileCompact,
			CompactionMode:          CompactionModeSummary,
			RecallMode:              RecallModeDisabled,
			ObservationMode:         ObservationModeFailures,
			WorkspaceMode:           WorkspaceModeBroad,
			MinCompactionMessages:   2,
			MinRecallQueryLength:    12,
			MinWorkspaceQueryLength: 4,
			LedgerLoadLimit:         6,
		}
	case BudgetProfileExtended:
		return Strategy{
			Profile:                 BudgetProfileExtended,
			CompactionMode:          CompactionModeLedgerPreferred,
			RecallMode:              RecallModeBroad,
			ObservationMode:         ObservationModeAll,
			WorkspaceMode:           WorkspaceModeBroad,
			MinCompactionMessages:   1,
			MinRecallQueryLength:    4,
			MinWorkspaceQueryLength: 4,
			LedgerLoadLimit:         20,
		}
	default:
		return Strategy{
			Profile:                 BudgetProfileBalanced,
			CompactionMode:          CompactionModeLedgerPreferred,
			RecallMode:              RecallModeSignals,
			ObservationMode:         ObservationModeAll,
			WorkspaceMode:           WorkspaceModeBroad,
			MinCompactionMessages:   1,
			MinRecallQueryLength:    8,
			MinWorkspaceQueryLength: 4,
			LedgerLoadLimit:         12,
		}
	}
}

func ResolveStrategy(profile string, overrides Strategy) Strategy {
	strategy := StrategyForProfile(profile)
	if overrides.CompactionMode != "" {
		strategy.CompactionMode = overrides.CompactionMode
	}
	if overrides.RecallMode != "" {
		strategy.RecallMode = overrides.RecallMode
	}
	if overrides.ObservationMode != "" {
		strategy.ObservationMode = overrides.ObservationMode
	}
	if overrides.WorkspaceMode != "" {
		strategy.WorkspaceMode = overrides.WorkspaceMode
	}
	if overrides.MinCompactionMessages > 0 {
		strategy.MinCompactionMessages = overrides.MinCompactionMessages
	}
	if overrides.MinRecallQueryLength > 0 {
		strategy.MinRecallQueryLength = overrides.MinRecallQueryLength
	}
	if overrides.MinWorkspaceQueryLength > 0 {
		strategy.MinWorkspaceQueryLength = overrides.MinWorkspaceQueryLength
	}
	if overrides.LedgerLoadLimit > 0 {
		strategy.LedgerLoadLimit = overrides.LedgerLoadLimit
	}
	return strategy
}

func ResolvedLayerPlan(profile string, budget Budget, strategy Strategy) LayerPlan {
	resolvedBudget := ResolveBudget(profile, budget)
	resolvedStrategy := ResolveStrategy(profile, strategy)
	return LayerPlan{
		Profile: resolvedStrategy.Profile,
		ProfileContext: LayerSpec{
			Name:        "profile",
			Description: "Read-only profile memory and notes resolved from the active agent profile.",
			Sources:     []string{"profile_memory", "profile_notes"},
			MaxItems:    2,
			Mode:        "static_readonly",
		},
		Hot: LayerSpec{
			Name:        "hot",
			Description: "Messages admitted into the next model request.",
			Sources:     []string{"system_prompt", "profile_context", "recent_turns"},
			MaxTokens:   resolvedBudget.MaxPromptTokens,
			MaxMessages: resolvedBudget.KeepRecentMessages,
			Mode:        "recent_first",
		},
		Warm: LayerSpec{
			Name:        "warm",
			Description: "Compressed observations and short-lived execution memory.",
			Sources:     []string{"observations", "memory"},
			MaxItems:    resolvedBudget.MaxObservationItems,
			Mode:        resolvedStrategy.ObservationMode,
		},
		Cold: LayerSpec{
			Name:        "cold",
			Description: "Compacted ledger and recalled artifacts referenced by the hot layer.",
			Sources:     []string{"decision_ledger", "artifact_recall"},
			MaxItems:    resolvedBudget.MaxRecallResults,
			Mode:        resolvedStrategy.CompactionMode + "+" + resolvedStrategy.RecallMode,
		},
	}
}

// NewManager 创建 context manager。
func NewManager(budget Budget, artifactSearcher ArtifactSearcher) *Manager {
	if budget.MaxPromptTokens <= 0 || budget.MaxMessages <= 0 || budget.KeepRecentMessages <= 0 {
		budget = DefaultBudget()
	}
	return &Manager{
		Budget:   budget,
		Strategy: StrategyForProfile(BudgetProfileBalanced),
		Artifact: artifactSearcher,
		Ledger:   ledgerStoreFromSearcher(artifactSearcher),
	}
}

func NewManagerWithProfile(profile string, budget Budget, artifactSearcher ArtifactSearcher) *Manager {
	manager := NewManager(budget, artifactSearcher)
	if manager != nil {
		manager.Strategy = ResolveStrategy(profile, Strategy{})
	}
	return manager
}

// Build 组装可发送给模型的受控上下文。
func (m *Manager) Build(ctx context.Context, input BuildInput) BuildResult {
	if m == nil {
		return BuildResult{Messages: cloneMessages(input.History)}
	}

	budget := m.Budget
	if budget.MaxPromptTokens <= 0 || budget.MaxMessages <= 0 || budget.KeepRecentMessages <= 0 {
		budget = DefaultBudget()
	}

	systemMessages, nonSystemMessages := splitMessages(input.History)
	result := BuildResult{
		Metadata: map[string]interface{}{
			"budget_max_prompt_tokens": budget.MaxPromptTokens,
			"budget_max_messages":      budget.MaxMessages,
			"context_profile":          m.Strategy.Profile,
			"context_strategy": map[string]interface{}{
				"compaction_mode":            m.Strategy.CompactionMode,
				"recall_mode":                m.Strategy.RecallMode,
				"observation_mode":           m.Strategy.ObservationMode,
				"workspace_mode":             m.Strategy.WorkspaceMode,
				"min_compaction_messages":    m.Strategy.MinCompactionMessages,
				"min_recall_query_length":    m.Strategy.MinRecallQueryLength,
				"min_workspace_query_length": m.Strategy.MinWorkspaceQueryLength,
				"ledger_load_limit":          m.Strategy.LedgerLoadLimit,
			},
			"context_layers": ResolvedLayerPlan(m.Strategy.Profile, budget, m.Strategy),
		},
	}

	recent := keepRecent(nonSystemMessages, budget.KeepRecentMessages)
	older := dropRecent(nonSystemMessages, budget.KeepRecentMessages)

	layerMetrics := map[string]interface{}{
		"hot": map[string]interface{}{
			"system_messages":  len(systemMessages),
			"recent_messages":  len(recent),
			"trimmed_messages": len(older),
			"max_messages":     budget.KeepRecentMessages,
			"max_tokens":       budget.MaxPromptTokens,
		},
		"warm": map[string]interface{}{
			"mode":                       m.Strategy.ObservationMode,
			"selected_items":             0,
			"injected":                   false,
			"max_items":                  budget.MaxObservationItems,
			"suppressed_for_active_turn": false,
		},
		"cold": map[string]interface{}{
			"compaction_mode":  m.Strategy.CompactionMode,
			"recall_mode":      m.Strategy.RecallMode,
			"older_messages":   len(older),
			"compacted":        false,
			"ledger_injected":  false,
			"recall_injected":  false,
			"recall_count":     0,
			"max_recall_items": budget.MaxRecallResults,
		},
		"workspace": map[string]interface{}{
			"injected":                   false,
			"file_count":                 0,
			"symbol_count":               0,
			"chunk_count":                0,
			"suppressed_for_active_turn": false,
		},
		"team": map[string]interface{}{
			"injected": false,
			"team_id":  input.TeamID,
			"task_id":  input.TaskID,
		},
		"profile": map[string]interface{}{
			"injected":       false,
			"resource_count": 0,
		},
	}
	result.Metadata["context_layer_metrics"] = layerMetrics

	managed := make([]types.Message, 0, len(systemMessages)+len(recent)+3)
	managed = append(managed, cloneMessages(systemMessages)...)

	if profileMessage, profileMeta := buildProfileMessage(input.Profile); profileMessage != nil {
		managed = append(managed, *profileMessage)
		result.Metadata["profile_context_injected"] = true
		for key, value := range profileMeta {
			result.Metadata[key] = value
		}
		if metrics, ok := layerMetrics["profile"].(map[string]interface{}); ok {
			metrics["injected"] = true
			if count, ok := profileMeta["profile_resource_count"].(int); ok {
				metrics["resource_count"] = count
			}
			if value, ok := profileMeta["profile_name"].(string); ok {
				metrics["name"] = value
			}
			if value, ok := profileMeta["profile_agent"].(string); ok {
				metrics["agent"] = value
			}
		}
		m.emitEvent("context.profile.injected", input.TraceID, input.SessionID, profileMeta)
	}

	if len(older) >= maxInt(1, m.Strategy.MinCompactionMessages) {
		m.emitEvent("context.compact.started", input.TraceID, input.SessionID, map[string]interface{}{
			"task_id":          input.TaskID,
			"source_messages":  len(older),
			"keep_recent":      budget.KeepRecentMessages,
			"history_messages": len(input.History),
			"compaction_mode":  m.Strategy.CompactionMode,
		})
		if m.Strategy.CompactionMode == CompactionModeLedgerPreferred {
			if ledgerMessages, checkpointIDs := m.buildLedgerMessages(ctx, input.SessionID, input.TaskID, older, input.Profile); len(ledgerMessages) > 0 {
				for _, ledgerMessage := range ledgerMessages {
					managed = append(managed, ledgerMessage)
				}
				result.Metadata["compacted_messages"] = len(older)
				result.Metadata["ledger_injected"] = true
				layerMetrics["cold"].(map[string]interface{})["compacted"] = true
				layerMetrics["cold"].(map[string]interface{})["ledger_injected"] = true
				if len(checkpointIDs) > 0 {
					result.Metadata["checkpoint_id"] = checkpointIDs[len(checkpointIDs)-1]
					if len(checkpointIDs) > 1 {
						result.Metadata["checkpoint_ids"] = append([]string(nil), checkpointIDs...)
					}
				}
				m.emitEvent("context.compact.completed", input.TraceID, input.SessionID, map[string]interface{}{
					"task_id":         input.TaskID,
					"source_messages": len(older),
					"ledger":          true,
					"checkpoint_id":   result.Metadata["checkpoint_id"],
					"checkpoint_ids":  result.Metadata["checkpoint_ids"],
					"compaction_mode": m.Strategy.CompactionMode,
				})
			} else {
				compactedMessages, checkpointIDs := m.buildCompactionSummaryMessages(ctx, input.SessionID, input.TaskID, older)
				if len(compactedMessages) > 0 {
					for _, compacted := range compactedMessages {
						managed = append(managed, compacted)
					}
					result.Metadata["compacted_messages"] = len(older)
					layerMetrics["cold"].(map[string]interface{})["compacted"] = true
					if len(checkpointIDs) > 0 {
						result.Metadata["checkpoint_id"] = checkpointIDs[len(checkpointIDs)-1]
						if len(checkpointIDs) > 1 {
							result.Metadata["checkpoint_ids"] = append([]string(nil), checkpointIDs...)
						}
					}
					m.emitEvent("context.compact.completed", input.TraceID, input.SessionID, map[string]interface{}{
						"task_id":         input.TaskID,
						"source_messages": len(older),
						"ledger":          false,
						"checkpoint_id":   result.Metadata["checkpoint_id"],
						"checkpoint_ids":  result.Metadata["checkpoint_ids"],
						"compaction_mode": m.Strategy.CompactionMode,
					})
				}
			}
		} else if compactedMessages, checkpointIDs := m.buildCompactionSummaryMessages(ctx, input.SessionID, input.TaskID, older); len(compactedMessages) > 0 {
			for _, compacted := range compactedMessages {
				managed = append(managed, compacted)
			}
			result.Metadata["compacted_messages"] = len(older)
			layerMetrics["cold"].(map[string]interface{})["compacted"] = true
			if len(checkpointIDs) > 0 {
				result.Metadata["checkpoint_id"] = checkpointIDs[len(checkpointIDs)-1]
				if len(checkpointIDs) > 1 {
					result.Metadata["checkpoint_ids"] = append([]string(nil), checkpointIDs...)
				}
			}
			m.emitEvent("context.compact.completed", input.TraceID, input.SessionID, map[string]interface{}{
				"task_id":         input.TaskID,
				"source_messages": len(older),
				"ledger":          false,
				"checkpoint_id":   result.Metadata["checkpoint_id"],
				"checkpoint_ids":  result.Metadata["checkpoint_ids"],
				"compaction_mode": m.Strategy.CompactionMode,
			})
		}
	}

	managed = append(managed, cloneMessages(recent)...)
	activeTurnHasReplay := activeUserTurnHasReplay(recent)

	selectedObservations := selectObservationsForMode(input.Memory, input.Observations, m.Strategy.ObservationMode)
	layerMetrics["warm"].(map[string]interface{})["selected_items"] = len(selectedObservations)
	if activeTurnHasReplay {
		layerMetrics["warm"].(map[string]interface{})["suppressed_for_active_turn"] = true
	} else if observationMessage := buildObservationMessage(selectedObservations, budget.MaxObservationItems); observationMessage != nil {
		managed = append(managed, *observationMessage)
		result.Metadata["observation_injected"] = true
		layerMetrics["warm"].(map[string]interface{})["injected"] = true
	}

	if recalled, recallCount, recallRefs := m.buildRecallMessage(ctx, input.SessionID, input.Goal, budget.MaxRecallResults); recalled != nil {
		managed = append(managed, *recalled)
		result.Metadata["recall_injected"] = true
		result.Metadata["recall_count"] = recallCount
		layerMetrics["cold"].(map[string]interface{})["recall_injected"] = true
		layerMetrics["cold"].(map[string]interface{})["recall_count"] = recallCount
		payload := map[string]interface{}{
			"task_id": input.TaskID,
			"query":   input.Goal,
			"count":   recallCount,
		}
		if len(recallRefs) > 0 {
			payload["source_refs"] = recallRefs
		}
		m.emitEvent("recall.performed", input.TraceID, input.SessionID, payload)
	}

	if activeTurnHasReplay {
		layerMetrics["workspace"].(map[string]interface{})["suppressed_for_active_turn"] = true
	} else if m.Workspace != nil && m.shouldBuildWorkspace(input.Goal) {
		wsCtx := m.Workspace.Build(input.Goal)
		if wsMsg, wsSummary := buildWorkspaceMessage(wsCtx); wsMsg != nil {
			fileCount := 0
			symbolCount := 0
			chunkCount := 0
			if wsCtx != nil {
				fileCount = len(wsCtx.Files)
				symbolCount = len(wsCtx.Symbols)
				chunkCount = len(wsCtx.Chunks)
			}
			managed = append(managed, *wsMsg)
			result.Metadata["workspace_context_injected"] = true
			if wsSummary != "" {
				result.Metadata["workspace_summary"] = wsSummary
			}
			layerMetrics["workspace"].(map[string]interface{})["injected"] = true
			layerMetrics["workspace"].(map[string]interface{})["file_count"] = fileCount
			layerMetrics["workspace"].(map[string]interface{})["symbol_count"] = symbolCount
			layerMetrics["workspace"].(map[string]interface{})["chunk_count"] = chunkCount
			m.emitEvent("context.workspace.injected", input.TraceID, input.SessionID, map[string]interface{}{
				"task_id":      input.TaskID,
				"query":        input.Goal,
				"file_count":   fileCount,
				"symbol_count": symbolCount,
				"chunk_count":  chunkCount,
			})
		}
	}

	if m.TeamContext != nil && (strings.TrimSpace(input.TeamID) != "" || strings.TrimSpace(input.TaskID) != "") {
		teamBudget := maxInt(3, budget.MaxObservationItems)
		ctxSummary, err := m.TeamContext.Build(ctx, strings.TrimSpace(input.TeamID), strings.TrimSpace(input.TaskID), teamBudget)
		if err != nil {
			result.Metadata["team_context_error"] = err.Error()
			m.emitEvent("context.team.failed", input.TraceID, input.SessionID, map[string]interface{}{
				"task_id": input.TaskID,
				"team_id": input.TeamID,
				"error":   err.Error(),
				"budget":  teamBudget,
			})
		} else if ctxSummary != nil && strings.TrimSpace(ctxSummary.Summary) != "" {
			message := types.NewAssistantMessage(strings.TrimSpace(ctxSummary.Summary))
			message.Metadata["context_stage"] = "team"
			if strings.TrimSpace(ctxSummary.TeamID) != "" {
				message.Metadata["team_id"] = ctxSummary.TeamID
			}
			if strings.TrimSpace(ctxSummary.TaskID) != "" {
				message.Metadata["task_id"] = ctxSummary.TaskID
			}
			managed = append(managed, *message)
			result.Metadata["team_context_injected"] = true
			layerMetrics["team"].(map[string]interface{})["injected"] = true
			layerMetrics["team"].(map[string]interface{})["team_id"] = ctxSummary.TeamID
			layerMetrics["team"].(map[string]interface{})["task_id"] = ctxSummary.TaskID
			layerMetrics["team"].(map[string]interface{})["task_count"] = ctxSummary.TaskCount
			layerMetrics["team"].(map[string]interface{})["mail_count"] = ctxSummary.MailCount
			layerMetrics["team"].(map[string]interface{})["mate_count"] = ctxSummary.MateCount
			m.emitEvent("context.team.injected", input.TraceID, input.SessionID, map[string]interface{}{
				"task_id":    ctxSummary.TaskID,
				"team_id":    ctxSummary.TeamID,
				"task_count": ctxSummary.TaskCount,
				"mail_count": ctxSummary.MailCount,
				"mate_count": ctxSummary.MateCount,
				"budget":     teamBudget,
			})
		}
	}

	managed = trimMessageCount(managed, budget.MaxMessages)
	managed = trimByTokenBudget(managed, budget, input.CountTokens, result.Metadata)
	layerMetrics["hot"].(map[string]interface{})["final_messages"] = len(managed)
	result.Messages = managed

	return result
}

func normalizeBudgetProfile(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", BudgetProfileBalanced:
		return BudgetProfileBalanced
	case BudgetProfileCompact, BudgetProfileHot:
		return BudgetProfileCompact
	case BudgetProfileExtended, BudgetProfileCold:
		return BudgetProfileExtended
	case BudgetProfileWarm:
		return BudgetProfileBalanced
	default:
		return strings.ToLower(strings.TrimSpace(profile))
	}
}

func (m *Manager) buildRecallMessage(ctx context.Context, sessionID, goal string, limit int) (*types.Message, int, []string) {
	if m.Artifact == nil || strings.TrimSpace(goal) == "" || !m.shouldRecallGoal(goal) {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 3
	}

	hits, err := m.searchRecallHits(ctx, sessionID, goal, limit)
	if err != nil || len(hits) == 0 {
		return nil, 0, nil
	}

	lines := make([]string, 0, len(hits)+1)
	lines = append(lines, "Relevant recalled artifacts:")
	recallArtifacts := make([]map[string]interface{}, 0, len(hits))
	aggregatedRefs := make([]string, 0)
	for _, hit := range hits {
		line := fmt.Sprintf("- artifact=%s tool=%s %s", hit.ID, hit.ToolName, strings.TrimSpace(hit.Preview))
		if hint := summarizeEntrySources(hit.SourceRefs); hint != "" {
			line += " [" + hint + "]"
		}
		lines = append(lines, line)
		recallArtifacts = append(recallArtifacts, map[string]interface{}{
			"id":           hit.ID,
			"tool_name":    hit.ToolName,
			"tool_call_id": hit.ToolCallID,
			"summary":      hit.Summary,
			"preview":      hit.Preview,
			"source_refs":  append([]string(nil), hit.SourceRefs...),
			"metadata":     cloneMetadataMap(hit.Metadata),
		})
		aggregatedRefs = mergeSourceRefs(aggregatedRefs, hit.SourceRefs)
	}

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "recall"
	if len(aggregatedRefs) > 0 {
		message.Metadata["source_refs"] = aggregatedRefs
	}
	if len(recallArtifacts) > 0 {
		message.Metadata["recall_artifacts"] = recallArtifacts
	}
	return message, len(hits), aggregatedRefs
}

func (m *Manager) shouldRecallGoal(goal string) bool {
	if len(strings.TrimSpace(goal)) < maxInt(1, m.Strategy.MinRecallQueryLength) {
		return false
	}
	switch m.Strategy.RecallMode {
	case RecallModeDisabled:
		return false
	case RecallModeBroad:
		return strings.TrimSpace(goal) != ""
	default:
		return shouldRecall(goal)
	}
}

func (m *Manager) buildCompactionSummaryMessages(ctx context.Context, sessionID, taskID string, older []types.Message) ([]types.Message, []string) {
	if m == nil || m.Ledger == nil || len(older) == 0 {
		if compacted := compactMessages(older); compacted != nil {
			return []types.Message{*compacted}, nil
		}
		return nil, nil
	}

	matched, covered := m.matchSummaryCheckpoints(ctx, sessionID, older)
	messages := make([]types.Message, 0, len(matched)+1)
	checkpointIDs := make([]string, 0, len(matched)+1)
	for index, match := range matched {
		if message := compactionPromptMessage(summaryTextFromCheckpoint(match.Checkpoint), match.Checkpoint.ID, match.Start, match.End, index > 0); message != nil {
			messages = append(messages, *message)
			if strings.TrimSpace(match.Checkpoint.ID) != "" {
				checkpointIDs = append(checkpointIDs, strings.TrimSpace(match.Checkpoint.ID))
			}
		}
	}

	if covered < len(older) {
		if checkpoint, summaryText := m.saveSummaryCheckpointSegment(ctx, sessionID, taskID, older[covered:], covered); strings.TrimSpace(summaryText) != "" {
			if message := compactionPromptMessage(summaryText, checkpoint.ID, covered, covered+len(older[covered:]), len(messages) > 0); message != nil {
				messages = append(messages, *message)
				if strings.TrimSpace(checkpoint.ID) != "" {
					checkpointIDs = append(checkpointIDs, strings.TrimSpace(checkpoint.ID))
				}
			}
		}
	}

	if len(messages) > 0 {
		return messages, checkpointIDs
	}

	if compacted := compactMessages(older); compacted != nil {
		return []types.Message{*compacted}, checkpointIDs
	}
	return nil, checkpointIDs
}

func (m *Manager) buildLedgerMessages(ctx context.Context, sessionID, taskID string, older []types.Message, profile map[string]interface{}) ([]types.Message, []string) {
	if m == nil || m.Ledger == nil || len(older) == 0 {
		return nil, nil
	}

	matched, covered := m.matchLedgerCheckpoints(ctx, sessionID, older)
	messages := make([]types.Message, 0, len(matched)+1)
	checkpointIDs := make([]string, 0, len(matched)+1)
	for index, match := range matched {
		if message := ledgerPromptMessage(match.Checkpoint.Ledger, match.Checkpoint.ID, match.Start, match.End, index > 0); message != nil {
			messages = append(messages, *message)
			if strings.TrimSpace(match.Checkpoint.ID) != "" {
				checkpointIDs = append(checkpointIDs, strings.TrimSpace(match.Checkpoint.ID))
			}
		}
	}

	if covered < len(older) {
		if checkpoint, entries := m.saveLedgerCheckpointSegment(ctx, sessionID, taskID, older[covered:], covered, profile); len(entries) > 0 {
			if message := ledgerPromptMessage(entries, checkpoint.ID, covered, covered+len(older[covered:]), len(messages) > 0); message != nil {
				messages = append(messages, *message)
				if strings.TrimSpace(checkpoint.ID) != "" {
					checkpointIDs = append(checkpointIDs, strings.TrimSpace(checkpoint.ID))
				}
			}
		}
	}

	if len(messages) > 0 {
		return messages, checkpointIDs
	}

	loaded, err := m.Ledger.LoadMemoryEntries(ctx, sessionID, []string{"decision", "plan", "open_question", "failure", "fact"}, maxInt(1, m.Strategy.LedgerLoadLimit))
	if err != nil || len(loaded) == 0 {
		return nil, checkpointIDs
	}
	if message := ledgerPromptMessage(loaded, "", 0, len(older), false); message != nil {
		return []types.Message{*message}, checkpointIDs
	}
	return nil, checkpointIDs
}

func (m *Manager) matchLedgerCheckpoints(ctx context.Context, sessionID string, older []types.Message) ([]matchedLedgerCheckpoint, int) {
	checkpoints := m.listLedgerCheckpoints(ctx, sessionID)
	if len(checkpoints) == 0 || len(older) == 0 {
		return nil, 0
	}

	matched := make([]matchedLedgerCheckpoint, 0, 4)
	covered := 0
	for covered < len(older) {
		bestIndex := -1
		bestLength := 0
		for index, checkpoint := range checkpoints {
			start, end, ok := ledgerCheckpointRange(checkpoint)
			if !ok || start != covered || end > len(older) {
				continue
			}
			segmentLength := end - start
			if segmentLength <= 0 {
				continue
			}
			if checkpoint.HistoryHash != hashHistory(older[start:end]) {
				continue
			}
			if segmentLength > bestLength {
				bestIndex = index
				bestLength = segmentLength
			}
		}
		if bestIndex < 0 {
			break
		}

		checkpoint := checkpoints[bestIndex]
		start, end, _ := ledgerCheckpointRange(checkpoint)
		matched = append(matched, matchedLedgerCheckpoint{
			Checkpoint: checkpoint,
			Start:      start,
			End:        end,
		})
		covered = end
	}

	return matched, covered
}

func (m *Manager) matchSummaryCheckpoints(ctx context.Context, sessionID string, older []types.Message) ([]matchedLedgerCheckpoint, int) {
	checkpoints := m.listLedgerCheckpoints(ctx, sessionID)
	if len(checkpoints) == 0 || len(older) == 0 {
		return nil, 0
	}

	matched := make([]matchedLedgerCheckpoint, 0, 4)
	covered := 0
	for covered < len(older) {
		bestIndex := -1
		bestLength := 0
		for index, checkpoint := range checkpoints {
			if strings.TrimSpace(checkpoint.Reason) != summaryCheckpointSegmentReason {
				continue
			}
			start, end, ok := ledgerCheckpointRange(checkpoint)
			if !ok || start != covered || end > len(older) {
				continue
			}
			segmentLength := end - start
			if segmentLength <= 0 {
				continue
			}
			if checkpoint.HistoryHash != hashHistory(older[start:end]) {
				continue
			}
			if strings.TrimSpace(summaryTextFromCheckpoint(checkpoint)) == "" {
				continue
			}
			if segmentLength > bestLength {
				bestIndex = index
				bestLength = segmentLength
			}
		}
		if bestIndex < 0 {
			break
		}

		checkpoint := checkpoints[bestIndex]
		start, end, _ := ledgerCheckpointRange(checkpoint)
		matched = append(matched, matchedLedgerCheckpoint{
			Checkpoint: checkpoint,
			Start:      start,
			End:        end,
		})
		covered = end
	}

	return matched, covered
}

func (m *Manager) listLedgerCheckpoints(ctx context.Context, sessionID string) []artifact.Checkpoint {
	if m == nil || m.Ledger == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}

	if lister, ok := m.Ledger.(ledgerCheckpointLister); ok {
		checkpoints, err := lister.ListCheckpoints(ctx, sessionID, 128, 0)
		if err == nil && len(checkpoints) > 0 {
			return checkpoints
		}
	}

	checkpoint, err := m.Ledger.LatestCheckpoint(ctx, sessionID)
	if err != nil || checkpoint == nil {
		return nil
	}
	return []artifact.Checkpoint{*checkpoint}
}

func (m *Manager) saveSummaryCheckpointSegment(ctx context.Context, sessionID, taskID string, segment []types.Message, segmentStart int) (artifact.Checkpoint, string) {
	if m == nil || m.Ledger == nil || len(segment) == 0 {
		return artifact.Checkpoint{}, ""
	}

	summaryText := compactMessageText(segment, false)
	if strings.TrimSpace(summaryText) == "" {
		return artifact.Checkpoint{}, ""
	}

	normalizedTaskID := firstNonEmpty(taskID, sessionID)
	checkpoint := artifact.Checkpoint{
		SessionID:    sessionID,
		TaskID:       normalizedTaskID,
		Reason:       summaryCheckpointSegmentReason,
		HistoryHash:  hashHistory(segment),
		MessageCount: len(segment),
		Metadata: map[string]interface{}{
			"source_messages":               len(segment),
			compactionSummaryTextKey:        summaryText,
			ledgerCheckpointSegmentStartKey: segmentStart,
			ledgerCheckpointSegmentEndKey:   segmentStart + len(segment),
		},
	}
	checkpointID, err := m.Ledger.SaveCheckpoint(ctx, checkpoint)
	if err == nil {
		checkpoint.ID = checkpointID
	}
	return checkpoint, summaryText
}

func (m *Manager) saveLedgerCheckpointSegment(ctx context.Context, sessionID, taskID string, segment []types.Message, segmentStart int, profile map[string]interface{}) (artifact.Checkpoint, []artifact.MemoryEntry) {
	if m == nil || m.Ledger == nil || len(segment) == 0 {
		return artifact.Checkpoint{}, nil
	}

	normalizedTaskID := firstNonEmpty(taskID, sessionID)
	entries := deriveMemoryEntries(sessionID, normalizedTaskID, ledgerCheckpointReason, segment, buildProfileSourceRefs(profile))
	if len(entries) == 0 {
		return artifact.Checkpoint{}, nil
	}
	for _, entry := range entries {
		_, _ = m.Ledger.InsertMemoryEntry(ctx, entry)
	}

	sourceRefs := mergeSourceRefsFromEntries(entries)
	checkpoint := artifact.Checkpoint{
		SessionID:    sessionID,
		TaskID:       normalizedTaskID,
		Reason:       ledgerCheckpointSegmentReason,
		HistoryHash:  hashHistory(segment),
		MessageCount: len(segment),
		Ledger:       entries,
		Metadata: map[string]interface{}{
			"source_messages":               len(segment),
			"source_refs":                   sourceRefs,
			"profile_source_refs":           extractProfileSourceRefs(sourceRefs),
			"profile_resource_kinds":        profileSourceKindCounts(sourceRefs),
			ledgerCheckpointSegmentStartKey: segmentStart,
			ledgerCheckpointSegmentEndKey:   segmentStart + len(segment),
		},
	}
	checkpointID, err := m.Ledger.SaveCheckpoint(ctx, checkpoint)
	if err == nil {
		checkpoint.ID = checkpointID
	}
	return checkpoint, entries
}

func ledgerCheckpointRange(checkpoint artifact.Checkpoint) (int, int, bool) {
	length := checkpoint.MessageCount
	if length <= 0 {
		return 0, 0, false
	}

	start, hasStart := intValue(checkpoint.Metadata, ledgerCheckpointSegmentStartKey)
	end, hasEnd := intValue(checkpoint.Metadata, ledgerCheckpointSegmentEndKey)
	if !hasStart {
		switch strings.TrimSpace(checkpoint.Reason) {
		case ledgerCheckpointReason, "":
			start = 0
		default:
			return 0, 0, false
		}
	}
	if !hasEnd {
		end = start + length
	}
	if end-start != length || start < 0 || end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func intValue(metadata map[string]interface{}, key string) (int, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func ledgerPromptMessage(entries []artifact.MemoryEntry, checkpointID string, segmentStart, segmentEnd int, continued bool) *types.Message {
	if len(entries) == 0 {
		return nil
	}

	lines := []string{"Decision ledger:"}
	if continued {
		lines[0] = "Decision ledger (continued):"
	}
	sourceRefs := make([]string, 0)
	for _, entry := range entries {
		summary := summarizeLine(fmt.Sprintf("%v", entry.Content["summary"]), 220)
		if summary == "" {
			continue
		}
		line := fmt.Sprintf("- %s: %s", entry.Kind, summary)
		if hint := summarizeEntrySources(entry.SourceRefs); hint != "" {
			line += " [" + hint + "]"
		}
		lines = append(lines, line)
		sourceRefs = mergeSourceRefs(sourceRefs, entry.SourceRefs)
	}
	if len(lines) <= 1 {
		return nil
	}

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "ledger"
	if strings.TrimSpace(checkpointID) != "" {
		message.Metadata["checkpoint_id"] = strings.TrimSpace(checkpointID)
	}
	message.Metadata[ledgerCheckpointSegmentStartKey] = segmentStart
	message.Metadata[ledgerCheckpointSegmentEndKey] = segmentEnd
	if len(sourceRefs) > 0 {
		message.Metadata["source_refs"] = sourceRefs
	}
	return message
}

func summaryTextFromCheckpoint(checkpoint artifact.Checkpoint) string {
	if len(checkpoint.Metadata) == 0 {
		return ""
	}
	value, ok := checkpoint.Metadata[compactionSummaryTextKey]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func compactionPromptMessage(summaryText, checkpointID string, segmentStart, segmentEnd int, continued bool) *types.Message {
	summaryText = strings.TrimSpace(summaryText)
	if summaryText == "" {
		return nil
	}
	if continued {
		summaryText = strings.Replace(summaryText, compactionHeading(false), compactionHeading(true), 1)
	}
	message := types.NewAssistantMessage(summaryText)
	message.Metadata["context_stage"] = "compaction"
	if strings.TrimSpace(checkpointID) != "" {
		message.Metadata["checkpoint_id"] = strings.TrimSpace(checkpointID)
	}
	message.Metadata[ledgerCheckpointSegmentStartKey] = segmentStart
	message.Metadata[ledgerCheckpointSegmentEndKey] = segmentEnd
	return message
}

func (m *Manager) searchRecallHits(ctx context.Context, sessionID, goal string, limit int) ([]artifact.SearchResult, error) {
	hits, err := m.Artifact.Search(ctx, sessionID, goal, limit)
	if err == nil && len(hits) > 0 {
		return hits, nil
	}

	queries := deriveRecallQueries(goal)
	seen := make(map[string]bool)
	merged := make([]artifact.SearchResult, 0, limit)
	for _, query := range queries {
		results, searchErr := m.Artifact.Search(ctx, sessionID, query, limit)
		if searchErr != nil {
			continue
		}
		for _, result := range results {
			if seen[result.ID] {
				continue
			}
			seen[result.ID] = true
			merged = append(merged, result)
			if len(merged) >= limit {
				return merged, nil
			}
		}
	}

	return merged, err
}

func splitMessages(history []types.Message) ([]types.Message, []types.Message) {
	systemMessages := make([]types.Message, 0, 1)
	nonSystemMessages := make([]types.Message, 0, len(history))
	for _, message := range history {
		if message.Role == "system" {
			systemMessages = append(systemMessages, *message.Clone())
			continue
		}
		nonSystemMessages = append(nonSystemMessages, *message.Clone())
	}
	return systemMessages, nonSystemMessages
}

func keepRecent(messages []types.Message, count int) []types.Message {
	if count <= 0 || len(messages) <= count {
		return cloneMessages(messages)
	}
	start := recentWindowStart(messages, count)
	return cloneMessages(messages[start:])
}

func dropRecent(messages []types.Message, count int) []types.Message {
	if count <= 0 || len(messages) <= count {
		return nil
	}
	start := recentWindowStart(messages, count)
	if start <= 0 {
		return nil
	}
	return cloneMessages(messages[:start])
}

func recentWindowStart(messages []types.Message, count int) int {
	if count <= 0 || len(messages) <= count {
		return 0
	}
	start := len(messages) - count
	if start <= 0 || start >= len(messages) {
		return 0
	}
	start = adjustRecentWindowForToolBlock(messages, start)
	if turnStart := activeUserTurnStart(messages); turnStart >= 0 && turnStart < start {
		return turnStart
	}
	return start
}

func adjustRecentWindowForToolBlock(messages []types.Message, start int) int {
	if start <= 0 || start >= len(messages) {
		return 0
	}
	if messages[start].Role != "tool" {
		return start
	}

	blockStart := start
	for blockStart > 0 && messages[blockStart-1].Role == "tool" {
		blockStart--
	}
	if blockStart > 0 {
		previous := messages[blockStart-1]
		if previous.Role == "assistant" && len(previous.ToolCalls) > 0 {
			return blockStart - 1
		}
	}
	return blockStart
}

func activeUserTurnStart(messages []types.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return index
		}
	}
	return -1
}

func activeUserTurnHasReplay(messages []types.Message) bool {
	start := activeUserTurnStart(messages)
	if start < 0 || start >= len(messages)-1 {
		return false
	}

	for _, message := range messages[start+1:] {
		if message.Metadata.GetString("context_stage", "") != "" {
			continue
		}
		switch message.Role {
		case "assistant", "tool":
			return true
		}
	}
	return false
}

func cloneMessages(messages []types.Message) []types.Message {
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func trimMessageCount(messages []types.Message, maxMessages int) []types.Message {
	if maxMessages <= 0 || len(messages) <= maxMessages {
		return messages
	}

	systemMessages, stableMessages, dynamicMessages, rawMessages := splitManagedMessages(messages)
	keep := maxMessages - len(systemMessages) - len(stableMessages)
	if keep <= 0 {
		trimmed := append([]types.Message{}, systemMessages...)
		trimmed = append(trimmed, stableMessages...)
		if len(trimmed) > maxMessages {
			return trimmed[:maxMessages]
		}
		return trimmed
	}

	flex := len(dynamicMessages) + len(rawMessages)
	if flex > keep {
		rawMessages, dynamicMessages = trimFlexibleMessageCount(rawMessages, dynamicMessages, keep)
	}

	trimmed := make([]types.Message, 0, len(systemMessages)+len(stableMessages)+len(rawMessages)+len(dynamicMessages))
	trimmed = append(trimmed, systemMessages...)
	trimmed = append(trimmed, stableMessages...)
	trimmed = append(trimmed, rawMessages...)
	trimmed = append(trimmed, dynamicMessages...)
	return trimmed
}

func trimFlexibleMessageCount(rawMessages, dynamicMessages []types.Message, keep int) ([]types.Message, []types.Message) {
	unpinnedRaw, pinnedRaw := splitPinnedRawMessages(rawMessages)
	if keep <= 0 {
		return pinnedRaw, nil
	}

	if len(pinnedRaw) >= keep {
		return pinnedRaw, nil
	}
	keep -= len(pinnedRaw)

	if len(dynamicMessages) > keep {
		dynamicMessages = dynamicMessages[len(dynamicMessages)-keep:]
		keep = 0
	} else {
		keep -= len(dynamicMessages)
	}
	if keep < len(unpinnedRaw) {
		unpinnedRaw = unpinnedRaw[len(unpinnedRaw)-keep:]
	}
	if len(pinnedRaw) > 0 {
		unpinnedRaw = append(unpinnedRaw, pinnedRaw...)
	}
	return unpinnedRaw, dynamicMessages
}

func splitPinnedRawMessages(rawMessages []types.Message) ([]types.Message, []types.Message) {
	if len(rawMessages) == 0 {
		return nil, nil
	}
	start := activeUserTurnStart(rawMessages)
	if start < 0 {
		return rawMessages, nil
	}
	return rawMessages[:start], rawMessages[start:]
}

func assembleManagedMessages(systemMessages, stableMessages, rawMessages, dynamicMessages []types.Message) []types.Message {
	trimmed := make([]types.Message, 0, len(systemMessages)+len(stableMessages)+len(rawMessages)+len(dynamicMessages))
	trimmed = append(trimmed, systemMessages...)
	trimmed = append(trimmed, stableMessages...)
	trimmed = append(trimmed, rawMessages...)
	trimmed = append(trimmed, dynamicMessages...)
	return trimmed
}

func trimByTokenBudget(messages []types.Message, budget Budget, counter TokenCounter, metadata map[string]interface{}) []types.Message {
	if counter == nil || budget.MaxPromptTokens <= 0 {
		return messages
	}

	trimmed := cloneMessages(messages)
	for len(trimmed) > 1 && counter(trimmed) > budget.MaxPromptTokens {
		systemMessages, stableMessages, dynamicMessages, rawMessages := splitManagedMessages(trimmed)
		unpinnedRaw, pinnedRaw := splitPinnedRawMessages(rawMessages)

		if len(dynamicMessages) > 0 {
			dynamicMessages = dynamicMessages[:len(dynamicMessages)-1]
			trimmed = assembleManagedMessages(systemMessages, stableMessages, append(unpinnedRaw, pinnedRaw...), dynamicMessages)
			continue
		}

		if len(unpinnedRaw) > 0 {
			unpinnedRaw = unpinnedRaw[1:]
			trimmed = assembleManagedMessages(systemMessages, stableMessages, append(unpinnedRaw, pinnedRaw...), dynamicMessages)
			continue
		}

		if len(stableMessages) > 0 {
			stableMessages = stableMessages[:len(stableMessages)-1]
			trimmed = assembleManagedMessages(systemMessages, stableMessages, pinnedRaw, dynamicMessages)
			continue
		}

		if len(pinnedRaw) == 0 {
			break
		}
		break
	}

	if metadata != nil {
		metadata["estimated_tokens"] = counter(trimmed)
		metadata["final_message_count"] = len(trimmed)
	}

	return trimmed
}

func shouldRecall(goal string) bool {
	lower := strings.ToLower(goal)
	for _, needle := range []string{
		"stack trace",
		"trace",
		"error",
		"log",
		"failure",
		"evidence",
		"commit",
		"test",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func (m *Manager) shouldBuildWorkspace(goal string) bool {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return false
	}

	minLength := maxInt(1, m.Strategy.MinWorkspaceQueryLength)
	if len(goal) < minLength {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(m.Strategy.WorkspaceMode)) {
	case WorkspaceModeDisabled:
		return false
	case "", WorkspaceModeBroad:
		return true
	default:
		return hasWorkspaceSignals(goal)
	}
}

func hasWorkspaceSignals(goal string) bool {
	lower := strings.ToLower(strings.TrimSpace(goal))
	if lower == "" {
		return false
	}

	for _, needle := range []string{
		"/", "\\", "./", "../", ".\\", "..\\",
		".go", ".ts", ".tsx", ".js", ".jsx", ".json", ".yaml", ".yml", ".md", ".py", ".rs", ".java", ".cs",
		"workspace", "repo", "repository", "project", "codebase", "source", "directory", "folder",
		"path", "file", "files", "module", "package", "function", "method", "class", "symbol",
		"test", "tests", "error", "stack trace", "trace", "bug", "docs", "documentation", "readme", "config",
		"当前目录", "目录", "路径", "文件", "代码", "函数", "方法", "类", "模块", "包",
		"符号", "测试", "报错", "错误", "堆栈", "仓库", "工程", "项目", "文档", "配置",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}

	return shouldRecall(goal)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func deriveRecallQueries(goal string) []string {
	fields := strings.FieldsFunc(strings.ToLower(goal), func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ':' || r == ';' || r == '/' || r == '\\' || r == '-' || r == '_' || r == '\n' || r == '\t'
	})

	queries := make([]string, 0, len(fields))
	seen := make(map[string]bool)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len(field) < 4 {
			continue
		}
		if seen[field] {
			continue
		}
		seen[field] = true
		queries = append(queries, field)
	}
	return queries
}

func splitManagedMessages(messages []types.Message) ([]types.Message, []types.Message, []types.Message, []types.Message) {
	systemMessages := make([]types.Message, 0, 1)
	stableMessages := make([]types.Message, 0, 2)
	dynamicMessages := make([]types.Message, 0, 6)
	rawMessages := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" {
			systemMessages = append(systemMessages, *message.Clone())
			continue
		}
		if stage := message.Metadata.GetString("context_stage", ""); stage != "" {
			switch stage {
			case "ledger", "profile", "compaction":
				stableMessages = append(stableMessages, *message.Clone())
			default:
				dynamicMessages = append(dynamicMessages, *message.Clone())
			}
			continue
		}
		rawMessages = append(rawMessages, *message.Clone())
	}
	return systemMessages, stableMessages, dynamicMessages, rawMessages
}

func ledgerStoreFromSearcher(searcher ArtifactSearcher) LedgerStore {
	if searcher == nil {
		return nil
	}
	store, ok := searcher.(LedgerStore)
	if !ok {
		return nil
	}
	return store
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (m *Manager) emitEvent(eventType, traceID, sessionID string, payload map[string]interface{}) {
	if m == nil || m.Events == nil {
		return
	}
	m.Events.Publish(runtimeevents.Event{
		Type:      eventType,
		TraceID:   traceID,
		AgentName: m.Agent,
		SessionID: sessionID,
		Payload:   payload,
	})
}

func buildWorkspaceMessage(ctx *workspace.WorkspaceContext) (*types.Message, string) {
	if ctx == nil {
		return nil, ""
	}
	lines := make([]string, 0, 8)
	lines = append(lines, "Workspace recall:")
	summary := summarizeLine(ctx.Summary, 240)
	if summary != "" {
		lines = append(lines, "Summary: "+summary)
	}
	if len(ctx.Files) > 0 {
		lines = append(lines, "Top files: "+strings.Join(limitStrings(ctx.Files, 5), ", "))
	}
	if len(ctx.Symbols) > 0 {
		lines = append(lines, "Top symbols: "+strings.Join(limitSymbolNames(ctx.Symbols, 8), ", "))
	}
	if len(ctx.Chunks) > 0 {
		lines = append(lines, "Relevant snippets:")
		for i, chunk := range ctx.Chunks {
			if i >= 3 {
				break
			}
			excerpt := summarizeLine(chunk.Content, 360)
			if excerpt == "" {
				continue
			}
			location := chunk.FilePath
			if chunk.StartLine > 0 && chunk.EndLine >= chunk.StartLine {
				location = fmt.Sprintf("%s:%d-%d", location, chunk.StartLine, chunk.EndLine)
			}
			lines = append(lines, "- "+location+"\n"+excerpt)
		}
	}
	if len(lines) <= 1 {
		return nil, ""
	}
	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "workspace"
	return message, summary
}

func buildProfileMessage(profile map[string]interface{}) (*types.Message, map[string]interface{}) {
	if len(profile) == 0 {
		return nil, nil
	}

	metadata := map[string]interface{}{}
	lines := []string{"Profile context:"}
	if value := profileString(profile, "name"); value != "" {
		lines = append(lines, "Profile: "+value)
		metadata["profile_name"] = value
	}
	if value := profileString(profile, "agent"); value != "" {
		lines = append(lines, "Agent: "+value)
		metadata["profile_agent"] = value
	}
	if value := profileString(profile, "reference"); value != "" {
		metadata["profile_reference"] = value
	}
	if value := profileString(profile, "root"); value != "" {
		metadata["profile_root"] = value
	}

	resourceCount := 0
	if resources, ok := profile["resources"].(map[string]interface{}); ok {
		if memory := summarizeProfileResource(resources, "memory", 360); memory != "" {
			lines = append(lines, "Memory snapshot: "+memory)
			resourceCount++
		}
		if notes := summarizeProfileResource(resources, "notes", 360); notes != "" {
			lines = append(lines, "Notes: "+notes)
			resourceCount++
		}
	}
	if resourceCount == 0 {
		if value := profileString(profile, "memory_path"); value != "" {
			lines = append(lines, "Memory file: "+value)
			resourceCount++
		}
		if value := profileString(profile, "notes_path"); value != "" {
			lines = append(lines, "Notes file: "+value)
			resourceCount++
		}
	}
	if len(lines) <= 1 {
		return nil, nil
	}

	metadata["profile_resource_count"] = resourceCount
	sourceRefs := buildProfileSourceRefs(profile)
	if len(sourceRefs) > 0 {
		metadata["profile_source_refs"] = sourceRefs
		metadata["source_refs"] = sourceRefs
	}
	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "profile"
	for key, value := range metadata {
		message.Metadata[key] = value
	}
	if len(sourceRefs) > 0 {
		message.Metadata["source_refs"] = sourceRefs
	}
	return message, metadata
}

func buildProfileSourceRefs(profile map[string]interface{}) []string {
	if len(profile) == 0 {
		return nil
	}

	refs := make([]string, 0, 2)
	identity := firstNonEmpty(
		profileString(profile, "root"),
		profileString(profile, "reference"),
		profileString(profile, "name"),
	)

	if value := profileString(profile, "memory_path"); value != "" {
		refs = append(refs, "profile-resource:memory:"+value)
	} else if identity != "" && profileHasResource(profile, "memory") {
		refs = append(refs, "profile-resource:memory:"+identity)
	}
	if value := profileString(profile, "notes_path"); value != "" {
		refs = append(refs, "profile-resource:notes:"+value)
	} else if identity != "" && profileHasResource(profile, "notes") {
		refs = append(refs, "profile-resource:notes:"+identity)
	}
	return mergeSourceRefs(refs)
}

func mergeSourceRefsFromEntries(entries []artifact.MemoryEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	refs := make([]string, 0)
	for _, entry := range entries {
		refs = mergeSourceRefs(refs, entry.SourceRefs)
	}
	return refs
}

func extractProfileSourceRefs(refs []string) []string {
	filtered := make([]string, 0, len(refs))
	for _, ref := range refs {
		switch {
		case strings.HasPrefix(ref, "profile-resource:memory:"),
			strings.HasPrefix(ref, "profile-resource:notes:"):
			filtered = append(filtered, ref)
		}
	}
	return mergeSourceRefs(filtered)
}

func profileSourceKindCounts(refs []string) map[string]int {
	if len(refs) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, ref := range refs {
		switch {
		case strings.HasPrefix(ref, "profile-resource:memory:"):
			counts["memory"]++
		case strings.HasPrefix(ref, "profile-resource:notes:"):
			counts["notes"]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func profileHasResource(profile map[string]interface{}, key string) bool {
	resources, ok := profile["resources"].(map[string]interface{})
	if !ok || len(resources) == 0 {
		return false
	}
	item, ok := resources[key].(map[string]interface{})
	return ok && len(item) > 0
}

func summarizeEntrySources(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	labels := make([]string, 0, 2)
	for _, ref := range refs {
		switch {
		case strings.HasPrefix(ref, "profile-resource:memory:"):
			labels = append(labels, "source=profile_memory")
		case strings.HasPrefix(ref, "profile-resource:notes:"):
			labels = append(labels, "source=profile_notes")
		}
	}
	labels = mergeSourceRefs(labels)
	return strings.Join(labels, ", ")
}

func cloneMetadataMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneMetadataValue(value)
	}
	return cloned
}

func cloneMetadataValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMetadataMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneMetadataValue(item)
		}
		return cloned
	case []string:
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}

func summarizeProfileResource(resources map[string]interface{}, key string, limit int) string {
	item, ok := resources[key].(map[string]interface{})
	if !ok || len(item) == 0 {
		return ""
	}
	if value := profileString(item, "content"); value != "" {
		return summarizeLine(value, limit)
	}
	if value := profileString(item, "path"); value != "" {
		return value
	}
	return ""
}

func profileString(values map[string]interface{}, key string) string {
	if len(values) == 0 {
		return ""
	}
	raw, ok := values[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func limitSymbolNames(values []workspace.SymbolInfo, limit int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) > limit {
		values = values[:limit]
	}
	names := make([]string, 0, len(values))
	for _, symbol := range values {
		if strings.TrimSpace(symbol.Name) == "" {
			continue
		}
		names = append(names, strings.TrimSpace(symbol.Name))
	}
	return names
}
