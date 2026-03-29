package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/memory"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
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
	Profile               string
	CompactionMode        string
	RecallMode            string
	ObservationMode       string
	MinCompactionMessages int
	MinRecallQueryLength  int
	LedgerLoadLimit       int
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
			Profile:               BudgetProfileCompact,
			CompactionMode:        CompactionModeSummary,
			RecallMode:            RecallModeDisabled,
			ObservationMode:       ObservationModeFailures,
			MinCompactionMessages: 2,
			MinRecallQueryLength:  12,
			LedgerLoadLimit:       6,
		}
	case BudgetProfileExtended:
		return Strategy{
			Profile:               BudgetProfileExtended,
			CompactionMode:        CompactionModeLedgerPreferred,
			RecallMode:            RecallModeBroad,
			ObservationMode:       ObservationModeAll,
			MinCompactionMessages: 1,
			MinRecallQueryLength:  4,
			LedgerLoadLimit:       20,
		}
	default:
		return Strategy{
			Profile:               BudgetProfileBalanced,
			CompactionMode:        CompactionModeLedgerPreferred,
			RecallMode:            RecallModeSignals,
			ObservationMode:       ObservationModeAll,
			MinCompactionMessages: 1,
			MinRecallQueryLength:  8,
			LedgerLoadLimit:       12,
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
	if overrides.MinCompactionMessages > 0 {
		strategy.MinCompactionMessages = overrides.MinCompactionMessages
	}
	if overrides.MinRecallQueryLength > 0 {
		strategy.MinRecallQueryLength = overrides.MinRecallQueryLength
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
				"compaction_mode":         m.Strategy.CompactionMode,
				"recall_mode":             m.Strategy.RecallMode,
				"observation_mode":        m.Strategy.ObservationMode,
				"min_compaction_messages": m.Strategy.MinCompactionMessages,
				"min_recall_query_length": m.Strategy.MinRecallQueryLength,
				"ledger_load_limit":       m.Strategy.LedgerLoadLimit,
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
			"mode":           m.Strategy.ObservationMode,
			"selected_items": 0,
			"injected":       false,
			"max_items":      budget.MaxObservationItems,
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
			"injected":     false,
			"file_count":   0,
			"symbol_count": 0,
			"chunk_count":  0,
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
			if ledgerMessage, checkpointID := m.buildLedgerMessage(ctx, input.SessionID, input.TaskID, older, input.Profile); ledgerMessage != nil {
				managed = append(managed, *ledgerMessage)
				result.Metadata["compacted_messages"] = len(older)
				result.Metadata["ledger_injected"] = true
				layerMetrics["cold"].(map[string]interface{})["compacted"] = true
				layerMetrics["cold"].(map[string]interface{})["ledger_injected"] = true
				if checkpointID != "" {
					result.Metadata["checkpoint_id"] = checkpointID
				}
				m.emitEvent("context.compact.completed", input.TraceID, input.SessionID, map[string]interface{}{
					"task_id":         input.TaskID,
					"source_messages": len(older),
					"ledger":          true,
					"checkpoint_id":   checkpointID,
					"compaction_mode": m.Strategy.CompactionMode,
				})
			} else {
				compacted := compactMessages(older)
				if compacted != nil {
					managed = append(managed, *compacted)
					result.Metadata["compacted_messages"] = len(older)
					layerMetrics["cold"].(map[string]interface{})["compacted"] = true
					m.emitEvent("context.compact.completed", input.TraceID, input.SessionID, map[string]interface{}{
						"task_id":         input.TaskID,
						"source_messages": len(older),
						"ledger":          false,
						"compaction_mode": m.Strategy.CompactionMode,
					})
				}
			}
		} else if compacted := compactMessages(older); compacted != nil {
			managed = append(managed, *compacted)
			result.Metadata["compacted_messages"] = len(older)
			layerMetrics["cold"].(map[string]interface{})["compacted"] = true
			m.emitEvent("context.compact.completed", input.TraceID, input.SessionID, map[string]interface{}{
				"task_id":         input.TaskID,
				"source_messages": len(older),
				"ledger":          false,
				"compaction_mode": m.Strategy.CompactionMode,
			})
		}
	}

	selectedObservations := selectObservationsForMode(input.Memory, input.Observations, m.Strategy.ObservationMode)
	layerMetrics["warm"].(map[string]interface{})["selected_items"] = len(selectedObservations)
	if observationMessage := buildObservationMessage(selectedObservations, budget.MaxObservationItems); observationMessage != nil {
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

	if m.Workspace != nil && strings.TrimSpace(input.Goal) != "" {
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

	managed = append(managed, cloneMessages(recent)...)
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

func (m *Manager) buildLedgerMessage(ctx context.Context, sessionID, taskID string, older []types.Message, profile map[string]interface{}) (*types.Message, string) {
	if m.Ledger == nil || len(older) == 0 {
		return nil, ""
	}

	historyHash := hashHistory(older)
	checkpoint, err := m.Ledger.LatestCheckpoint(ctx, sessionID)
	if err != nil {
		return nil, ""
	}

	var entries []artifact.MemoryEntry
	checkpointID := ""
	if checkpoint != nil && checkpoint.HistoryHash == historyHash && checkpoint.MessageCount == len(older) {
		entries = checkpoint.Ledger
		checkpointID = checkpoint.ID
	} else {
		entries = deriveMemoryEntries(sessionID, firstNonEmpty(taskID, sessionID), "history_window", older, buildProfileSourceRefs(profile))
		for _, entry := range entries {
			_, _ = m.Ledger.InsertMemoryEntry(ctx, entry)
		}
		checkpointID, _ = m.Ledger.SaveCheckpoint(ctx, artifact.Checkpoint{
			SessionID:    sessionID,
			TaskID:       firstNonEmpty(taskID, sessionID),
			Reason:       "history_window",
			HistoryHash:  historyHash,
			MessageCount: len(older),
			Ledger:       entries,
			Metadata: map[string]interface{}{
				"source_messages":        len(older),
				"source_refs":            mergeSourceRefsFromEntries(entries),
				"profile_source_refs":    extractProfileSourceRefs(mergeSourceRefsFromEntries(entries)),
				"profile_resource_kinds": profileSourceKindCounts(mergeSourceRefsFromEntries(entries)),
			},
		})
	}

	if len(entries) == 0 {
		loaded, err := m.Ledger.LoadMemoryEntries(ctx, sessionID, []string{"decision", "plan", "open_question", "failure", "fact"}, maxInt(1, m.Strategy.LedgerLoadLimit))
		if err != nil {
			return nil, checkpointID
		}
		entries = loaded
	}
	if len(entries) == 0 {
		return nil, checkpointID
	}

	lines := []string{"Decision ledger:"}
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

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "ledger"
	if checkpointID != "" {
		message.Metadata["checkpoint_id"] = checkpointID
	}
	if len(sourceRefs) > 0 {
		message.Metadata["source_refs"] = sourceRefs
	}
	return message, checkpointID
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

	systemMessages, protectedMessages, rawMessages := splitManagedMessages(messages)
	keep := maxMessages - len(systemMessages) - len(protectedMessages)
	if keep <= 0 {
		trimmed := append([]types.Message{}, systemMessages...)
		trimmed = append(trimmed, protectedMessages...)
		if len(trimmed) > maxMessages {
			return trimmed[:maxMessages]
		}
		return trimmed
	}
	if len(rawMessages) > keep {
		rawMessages = rawMessages[len(rawMessages)-keep:]
	}

	trimmed := make([]types.Message, 0, len(systemMessages)+len(protectedMessages)+len(rawMessages))
	trimmed = append(trimmed, systemMessages...)
	trimmed = append(trimmed, protectedMessages...)
	trimmed = append(trimmed, rawMessages...)
	return trimmed
}

func trimByTokenBudget(messages []types.Message, budget Budget, counter TokenCounter, metadata map[string]interface{}) []types.Message {
	if counter == nil || budget.MaxPromptTokens <= 0 {
		return messages
	}

	trimmed := cloneMessages(messages)
	for len(trimmed) > 1 && counter(trimmed) > budget.MaxPromptTokens {
		systemMessages, protectedMessages, rawMessages := splitManagedMessages(trimmed)
		if len(rawMessages) == 0 {
			break
		}
		rawMessages = rawMessages[1:]
		trimmed = append(systemMessages, protectedMessages...)
		trimmed = append(trimmed, rawMessages...)
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

func splitManagedMessages(messages []types.Message) ([]types.Message, []types.Message, []types.Message) {
	systemMessages := make([]types.Message, 0, 1)
	protectedMessages := make([]types.Message, 0, 3)
	rawMessages := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" {
			systemMessages = append(systemMessages, *message.Clone())
			continue
		}
		if stage := message.Metadata.GetString("context_stage", ""); stage != "" {
			protectedMessages = append(protectedMessages, *message.Clone())
			continue
		}
		rawMessages = append(rawMessages, *message.Clone())
	}
	return systemMessages, protectedMessages, rawMessages
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
