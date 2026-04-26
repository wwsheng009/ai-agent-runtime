package agent

import (
	"errors"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// PromptPreflightError describes a local fail-fast decision made before a
// provider request is sent because the prompt exceeded the allowed budget.
type PromptPreflightError struct {
	PromptTokens                         int
	PromptBudget                         int
	BudgetSource                         string
	BudgetSourceDetail                   string
	ResolvedProvider                     string
	ResolvedModel                        string
	ProviderContextLimit                 int
	ProviderOutputLimit                  int
	ModelCapabilityMaxContextTokens      int
	ModelCapabilityAutoCompactRatio      float64
	ModelCapabilityAutoCompactTokenLimit int
	Code                                 string
	Reason                               string
	Detail                               string
	SuggestedAction                      string
	CanRetryAfterCompaction              bool
	ActiveTurnCompacted                  bool
	ActiveTurnMessageCount               int
	LatestReplayBlockMessageCount        int
	ReplacementHistoryApplied            bool
	ReplacementHistory                   []types.Message
}

func (e *PromptPreflightError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Code {
	case "prompt_still_exceeds_budget_after_compaction":
		return fmt.Sprintf(
			"prompt preflight budget exceeded: prompt tokens %d > budget %d after active-turn compaction",
			e.PromptTokens,
			e.PromptBudget,
		)
	case "active_turn_not_compactable":
		return fmt.Sprintf(
			"prompt preflight budget exceeded: prompt tokens %d > budget %d; active-turn replay cannot be compacted further",
			e.PromptTokens,
			e.PromptBudget,
		)
	default:
		return fmt.Sprintf(
			"prompt preflight budget exceeded: prompt tokens %d > budget %d before provider request",
			e.PromptTokens,
			e.PromptBudget,
		)
	}
}

// Metadata returns structured observability fields for this error.
func (e *PromptPreflightError) Metadata() map[string]interface{} {
	if e == nil {
		return nil
	}
	metadata := map[string]interface{}{
		"failure_reason_code":        e.Code,
		"failure_reason":             e.Reason,
		"can_retry_after_compaction": e.CanRetryAfterCompaction,
		"active_turn_compacted":      e.ActiveTurnCompacted,
		"prompt_tokens":              e.PromptTokens,
		"prompt_budget":              e.PromptBudget,
	}
	if e.Detail != "" {
		metadata["failure_reason_detail"] = e.Detail
	}
	if e.SuggestedAction != "" {
		metadata["suggested_action"] = e.SuggestedAction
	}
	if e.BudgetSource != "" {
		metadata["budget_source"] = e.BudgetSource
	}
	if e.BudgetSourceDetail != "" {
		metadata["budget_source_detail"] = e.BudgetSourceDetail
	}
	if e.ResolvedProvider != "" {
		metadata["resolved_provider"] = e.ResolvedProvider
	}
	if e.ResolvedModel != "" {
		metadata["resolved_model"] = e.ResolvedModel
	}
	if e.ProviderContextLimit > 0 {
		metadata["provider_context_limit"] = e.ProviderContextLimit
	}
	if e.ProviderOutputLimit > 0 {
		metadata["provider_output_limit"] = e.ProviderOutputLimit
	}
	if e.ModelCapabilityMaxContextTokens > 0 {
		metadata["model_capability_max_context_tokens"] = e.ModelCapabilityMaxContextTokens
	}
	if e.ModelCapabilityAutoCompactRatio > 0 {
		metadata["model_capability_auto_compact_ratio"] = e.ModelCapabilityAutoCompactRatio
	}
	if e.ModelCapabilityAutoCompactTokenLimit > 0 {
		metadata["model_capability_auto_compact_token_limit"] = e.ModelCapabilityAutoCompactTokenLimit
	}
	if e.ActiveTurnMessageCount > 0 {
		metadata["active_turn_message_count"] = e.ActiveTurnMessageCount
	}
	if e.LatestReplayBlockMessageCount > 0 {
		metadata["latest_replay_block_message_count"] = e.LatestReplayBlockMessageCount
	}
	if count := len(e.ReplacementHistory); count > 0 {
		metadata["replacement_history_available"] = true
		metadata["replacement_history_message_count"] = count
	}
	if e.ReplacementHistoryApplied {
		metadata["replacement_history_applied"] = true
	}
	return metadata
}

// AsPromptPreflightError extracts a PromptPreflightError from an error chain.
func AsPromptPreflightError(err error) (*PromptPreflightError, bool) {
	if err == nil {
		return nil, false
	}
	var target *PromptPreflightError
	if errors.As(err, &target) && target != nil {
		return target, true
	}
	return nil, false
}

// CloneReplacementHistory returns a defensive copy of the recovery history that
// can be persisted after a fail-fast preflight decision. It is intentionally not
// exposed through Metadata to avoid leaking the full prompt into events/hooks.
func (e *PromptPreflightError) CloneReplacementHistory() []types.Message {
	if e == nil || len(e.ReplacementHistory) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(e.ReplacementHistory))
	for index := range e.ReplacementHistory {
		cloned[index] = *e.ReplacementHistory[index].Clone()
	}
	return cloned
}
