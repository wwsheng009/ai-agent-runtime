package compactruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type RemoteAdapter struct {
	llmRuntime *llm.LLMRuntime
	// contextManager is the only route to the durable fact ledger. The provider
	// owns the compaction summary; the declarative residue it drops has to be
	// recorded locally or nothing carries it past this boundary.
	contextManager *contextmgr.Manager
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
	// Remote providers return prose/history projections. Re-pin the latest
	// durable world-state from the source turn so goal/todo/facts are not left
	// only inside compacted text.
	replacement = mergeDurableWorldState(req.Phase, req.History, replacement, counter, remoteDurableTokenBudget(req, replacement, counter))
	// Anything the provider removed and did not re-pin is invisible to the next
	// request. Record its declarative residue at this boundary; the ledger only
	// ever reads it back once the transcript has crossed a compaction.
	a.persistDeclarativeResidue(ctx, req, req.History, replacement)
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
		TokenBefore:        resolveObservedTokenCount(req, counter),
		TokenAfter:         counter(replacement),
		Usage:              usage,
		UsageSource:        strings.TrimSpace(response.UsageSource),
		CompactedMessages:  maxInt(0, response.CompactedMessages),
		CheckpointIDs:      append([]string(nil), response.CheckpointIDs...),
		ReplacementHistory: replacement,
	}, "", nil
}

// persistDeclarativeResidue records what the remote replacement destroyed.
//
// A remote provider owns its own summary and hands back a projection, so the
// local store never sees that prose. Comparing by message substance identity
// keeps the record honest: only statements the model can no longer read are
// persisted. A turn the provider kept, or one mergeDurableWorldState re-pinned,
// is still visible and must not be duplicated as a "fact".
func (a *RemoteAdapter) persistDeclarativeResidue(ctx context.Context, req Request, input, replacement []types.Message) {
	if a == nil || a.contextManager == nil || strings.TrimSpace(req.SessionID) == "" {
		return
	}
	dropped := droppedCompactionInput(input, replacement)
	if len(dropped) == 0 {
		return
	}
	a.contextManager.PersistCompactionFacts(
		ctx,
		// Request carries no workspace identity; the ledger scopes by session, and
		// an empty workspace id matches every query filter.
		"",
		strings.TrimSpace(req.SessionID),
		strings.TrimSpace(req.TaskID),
		dropped,
	)
}

// droppedCompactionInput returns the input messages with no surviving identity
// in the replacement. System and tool rows are excluded downstream by the ledger;
// here the diff only answers "can the model still read this turn".
func droppedCompactionInput(input, replacement []types.Message) []types.Message {
	kept := make(map[string]struct{}, len(replacement))
	for _, message := range replacement {
		kept[types.MessageSubstanceKey(message)] = struct{}{}
	}
	dropped := make([]types.Message, 0, len(input))
	for _, message := range input {
		if _, alive := kept[types.MessageSubstanceKey(message)]; alive {
			continue
		}
		dropped = append(dropped, message)
	}
	return dropped
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func remoteDurableTokenBudget(req Request, replacement []types.Message, counter TokenCounter) int {
	if counter == nil {
		return 4000
	}
	if req.ReplacementTokenLimit > 0 {
		remaining := req.ReplacementTokenLimit - counter(replacement)
		if remaining <= 0 {
			return 1
		}
		return remaining
	}
	// Keep remote durable rehydrate bounded even when the caller did not set an
	// explicit replacement ceiling.
	return 4000
}
