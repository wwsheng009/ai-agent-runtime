package chat

import runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"

// PruneRequestScopedHistory returns the durable view of a transcript: the
// messages that belong to the conversation rather than to the provider request
// that is currently running.
//
// Two classes of rows have repeatedly been written into the canonical
// transcript although they are rebuilt for every request and therefore must not
// become history:
//
//  1. transient prompts (auto-continuation / completion-audit re-prompts). They
//     are marked with MetadataKeyTransientPrompt and are dropped unconditionally:
//     the marker is explicit intent, and a replayed prompt stored as a new user
//     turn is what the workspace renders as a duplicated message.
//  2. request-scoped context layers (fact ledger, recall, correction, ...). The
//     context manager rebuilds them per request with fresh identities, so every
//     checkpoint appended one more copy. They are only collapsed when the same
//     substance already appears earlier in the same transcript: the first copy
//     stays (prompt assembly still reuses a layer that is already stored), the
//     surplus copies are dropped instead of being appended as new rows.
//
// The input is not mutated; a nil result means the transcript is already
// durable.
func PruneRequestScopedHistory(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(messages))
	durable := make([]runtimetypes.Message, 0, len(messages))
	changed := false
	for _, message := range messages {
		if message.Metadata.GetBool(runtimetypes.MetadataKeyTransientPrompt, false) {
			changed = true
			continue
		}
		key := runtimetypes.MessageSubstanceKey(message)
		if runtimetypes.IsRequestScopedContextMessage(message) && seen[key] {
			changed = true
			continue
		}
		seen[key] = true
		durable = append(durable, message)
	}
	if !changed {
		return nil
	}
	return durable
}

// PruneRequestScopedHistory drops request-scoped messages from the session
// history and reports how many rows were removed. It is called on every durable
// write (mid-turn checkpoint and end-of-turn sync) so a long turn cannot
// accumulate a fresh copy of every context layer per checkpoint.
func (s *Session) PruneRequestScopedHistory() int {
	if s == nil || len(s.History) == 0 {
		return 0
	}
	history := s.GetMessages()
	durable := PruneRequestScopedHistory(history)
	if durable == nil {
		return 0
	}
	removed := len(history) - len(durable)
	replaceSessionHistoryAndAdvancePromptCacheEpoch(s, durable)
	return removed
}
