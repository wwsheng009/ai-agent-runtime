package compactruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type RemoteAdapter struct {
	llmRuntime *llm.LLMRuntime
}

func (a *RemoteAdapter) Compact(ctx context.Context, req Request, limit threshold, counter TokenCounter) (*Result, string, error) {
	if a == nil || a.llmRuntime == nil {
		return nil, "remote_adapter_unavailable", nil
	}

	provider, err := a.llmRuntime.GetProvider(strings.TrimSpace(limit.ResolvedProvider))
	if err != nil || provider == nil {
		return nil, "provider_unavailable", nil
	}

	compactor, ok := provider.(llm.RemoteCompactionProvider)
	if !ok || compactor == nil {
		return nil, "remote_compact_unsupported", nil
	}

	response, err := compactor.RemoteCompact(ctx, llm.RemoteCompactRequest{
		SessionID:          strings.TrimSpace(req.SessionID),
		TaskID:             strings.TrimSpace(req.TaskID),
		Provider:           strings.TrimSpace(limit.ResolvedProvider),
		Model:              strings.TrimSpace(limit.ResolvedModel),
		History:            cloneMessages(req.History),
		KeepRecentMessages: req.KeepRecentMessages,
		Phase:              normalizedPhase(req.Phase),
		TriggerTokenLimit:  limit.TriggerTokenLimit,
		MaxContextTokens:   limit.MaxContextTokens,
	})
	if err != nil {
		if errors.Is(err, llm.ErrRemoteCompactUnsupported) {
			return nil, "remote_compact_unsupported", nil
		}
		return nil, "remote_compact_failed", fmt.Errorf("remote compact failed: %w", err)
	}
	if response == nil || len(response.ReplacementHistory) == 0 {
		return nil, "remote_compact_empty", nil
	}

	replacement := cloneMessages(response.ReplacementHistory)
	var usage *types.TokenUsage
	if response.Usage != nil {
		usage = response.Usage.Clone()
	}
	return &Result{
		Mode:               ModeRemote,
		Phase:              normalizedPhase(req.Phase),
		ResolvedProvider:   limit.ResolvedProvider,
		ResolvedModel:      limit.ResolvedModel,
		TriggerTokenLimit:  limit.TriggerTokenLimit,
		MaxContextTokens:   limit.MaxContextTokens,
		TokenBefore:        counter(req.History),
		TokenAfter:         counter(replacement),
		Usage:              usage,
		UsageSource:        strings.TrimSpace(response.UsageSource),
		CompactedMessages:  maxInt(0, response.CompactedMessages),
		CheckpointIDs:      append([]string(nil), response.CheckpointIDs...),
		ReplacementHistory: replacement,
	}, "", nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
