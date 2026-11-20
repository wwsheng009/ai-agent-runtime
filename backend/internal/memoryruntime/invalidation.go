package memoryruntime

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// InvalidateFromRuntimeEvent applies generation-scoped invalidation for
// compaction/backtrack events. Events without a positive session generation
// are quarantined because a reused session ID cannot safely identify a source
// incarnation.
func (r *Runtime) InvalidateFromRuntimeEvent(ctx context.Context, eventType, sessionID string, payload map[string]interface{}) (memorystore.SessionRangeInvalidationResult, error) {
	result := memorystore.SessionRangeInvalidationResult{}
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return result, nil
	}
	switch eventType {
	case chat.EventSessionCompactCompleted:
		// Compaction rewrites only the prompt projection. Canonical transcript
		// (the memory source of truth) is unchanged, so canonical-derived L0/L1
		// and memory must stay fresh. Missing flag = legacy conservative mode.
		if changed, ok := payloadBool(payload, "canonical_transcript_changed"); ok && !changed {
			return result, nil
		}
		generation := payloadSessionGeneration(payload)
		if generation == nil {
			// Session IDs can be reused. Legacy events without a trustworthy
			// incarnation must not invalidate every generation sharing the ID.
			return result, nil
		}
		return r.invalidateSessionAllGeneration(ctx, sessionID, "compaction", *generation)
	case chat.EventBacktrackFinished:
		return r.invalidateBacktrackRange(ctx, sessionID, payload)
	default:
		return result, nil
	}
}

// InvalidateSessionRange exposes the precise store primitive for callers that
// already resolved a canonical range.
func (r *Runtime) InvalidateSessionRange(ctx context.Context, sessionID string, inv memorystore.SessionRangeInvalidation, reason string) (memorystore.SessionRangeInvalidationResult, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return memorystore.SessionRangeInvalidationResult{}, nil
	}
	if inv.SessionGeneration == nil || *inv.SessionGeneration <= 0 {
		return memorystore.SessionRangeInvalidationResult{}, nil
	}
	return r.Store.InvalidateSessionRange(ctx, strings.TrimSpace(sessionID), inv, reason)
}

func (r *Runtime) invalidateBacktrackRange(ctx context.Context, sessionID string, payload map[string]interface{}) (memorystore.SessionRangeInvalidationResult, error) {
	result := memorystore.SessionRangeInvalidationResult{}
	mode := strings.ToLower(strings.TrimSpace(payloadString(payload, "mode")))
	generation := payloadSessionGeneration(payload)
	if mode == string(chat.BacktrackModeCode) {
		return result, nil
	}
	if generation == nil {
		// Range seqs and message IDs are not sufficient to establish which
		// incarnation they belong to after a session ID has been reused.
		return result, nil
	}
	if strings.TrimSpace(payloadString(payload, "error")) != "" {
		// The rewind may have partially persisted before failing. Fall back to
		// the conservative session-wide invalidation for conversation modes.
		if mode == string(chat.BacktrackModeConversation) || mode == string(chat.BacktrackModeBoth) {
			return r.invalidateSessionAllGeneration(ctx, sessionID, "backtrack", *generation)
		}
		return result, nil
	}
	tombstone := payloadMap(payload, "tombstone")
	if tombstone == nil {
		// No conversation truncation was recorded.
		if payloadInt64(payload, "removed_message_count") <= 0 {
			return result, nil
		}
		tombstone = map[string]interface{}{}
	}
	cutoff := payloadInt64(payload, "canonical_cutoff_seq")
	before := payloadInt64(payload, "canonical_message_count_before")
	cutoffAt := payloadTime(payload, "canonical_cutoff_at")
	messageIDs := payloadStringSlice(tombstone, "removed_message_ids")
	if len(messageIDs) == 0 {
		messageIDs = payloadStringSlice(payload, "removed_message_ids")
	}
	turnIDs := payloadStringSlice(tombstone, "removed_turn_ids")
	if len(turnIDs) == 0 {
		turnIDs = payloadStringSlice(payload, "removed_turn_ids")
	}
	removedCount := payloadInt64(tombstone, "removed_message_count")
	if removedCount <= 0 {
		removedCount = payloadInt64(payload, "removed_message_count")
	}
	if cutoff > 0 && before > cutoff {
		return r.InvalidateSessionRange(ctx, sessionID, memorystore.SessionRangeInvalidation{
			CutoffMessageSeq:  cutoff,
			BeforeMessageSeq:  before,
			MessageIDs:        messageIDs,
			TurnIDs:           turnIDs,
			SessionGeneration: generation,
			CutoffAt:          cutoffAt,
		}, "backtrack")
	}
	if len(messageIDs) > 0 || len(turnIDs) > 0 {
		if cutoff <= 0 && removedCount > int64(len(messageIDs)) {
			// Legacy tombstones cap removed ids. Without a canonical cutoff the
			// remaining range cannot be reconstructed safely.
			return r.invalidateSessionAllGeneration(ctx, sessionID, "backtrack", *generation)
		}
		return r.InvalidateSessionRange(ctx, sessionID, memorystore.SessionRangeInvalidation{
			MessageIDs:        messageIDs,
			TurnIDs:           turnIDs,
			SessionGeneration: generation,
		}, "backtrack")
	}
	if removedCount > 0 {
		return r.invalidateSessionAllGeneration(ctx, sessionID, "backtrack", *generation)
	}
	return result, nil
}

func (r *Runtime) invalidateSessionAllGeneration(ctx context.Context, sessionID, reason string, generation int64) (memorystore.SessionRangeInvalidationResult, error) {
	result := memorystore.SessionRangeInvalidationResult{}
	if generation <= 0 {
		return result, nil
	}
	affected, err := r.InvalidateSessionDigestsForGeneration(ctx, sessionID, generation)
	if err != nil {
		return result, err
	}
	result.Digests = affected
	memories, err := r.InvalidateSessionMemoriesForGeneration(ctx, sessionID, generation, reason)
	if err != nil {
		return result, err
	}
	result.Memories = memories
	return result, nil
}

func payloadSessionGeneration(payload map[string]interface{}) *int64 {
	if payload == nil {
		return nil
	}
	if _, ok := payload["session_generation"]; !ok {
		return nil
	}
	generation := payloadInt64(payload, "session_generation")
	if generation <= 0 {
		return nil
	}
	return &generation
}

func payloadInt64(payload map[string]interface{}, key string) int64 {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	default:
		return 0
	}
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func payloadBool(payload map[string]interface{}, key string) (bool, bool) {
	if payload == nil {
		return false, false
	}
	switch value := payload[key].(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return false, false
	}
}

func payloadTime(payload map[string]interface{}, key string) time.Time {
	raw := payloadString(payload, key)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func payloadMap(payload map[string]interface{}, key string) map[string]interface{} {
	if payload == nil {
		return nil
	}
	switch value := payload[key].(type) {
	case map[string]interface{}:
		return value
	case map[string]string:
		out := make(map[string]interface{}, len(value))
		for k, v := range value {
			out[k] = v
		}
		return out
	default:
		return nil
	}
}

func payloadStringSlice(payload map[string]interface{}, key string) []string {
	if payload == nil {
		return nil
	}
	switch value := payload[key].(type) {
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}
