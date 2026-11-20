package memoryruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestInvalidateFromRuntimeEventCompactionKeepsCanonicalMemory(t *testing.T) {
	ctx := context.Background()
	runtime, store := newInvalidationRuntimeForTest(t)
	digest := recordInvalidationDigest(t, store, "session-compact", "turn-1", "m1", "m2", 2)
	_, err := store.MarkTurnDigestsProcessed(ctx, "session-compact", digest.DigestSeq, "")
	require.NoError(t, err)
	candidate := insertInvalidationCandidate(t, store, "session-compact", "m1", 2, "kept compaction evidence")

	result, err := runtime.InvalidateFromRuntimeEvent(ctx, chat.EventSessionCompactCompleted, "session-compact", map[string]interface{}{
		"canonical_transcript_changed": false,
		"session_generation":           int64(1),
	})
	require.NoError(t, err)
	require.Zero(t, result.Digests)
	require.Zero(t, result.Memories)

	same, err := store.GetTurnDigestByTurn(ctx, "session-compact", "turn-1")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, same.Status)
	record, err := store.GetRecord(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.FreshnessFresh, record.Freshness)

	// Missing generation is quarantined; the session ID alone is not enough to
	// safely identify an incarnation.
	result, err = runtime.InvalidateFromRuntimeEvent(ctx, chat.EventSessionCompactCompleted, "session-compact", map[string]interface{}{})
	require.NoError(t, err)
	require.Zero(t, result.Digests)
	require.Zero(t, result.Memories)
	same, err = store.GetTurnDigestByTurn(ctx, "session-compact", "turn-1")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, same.Status)

	result, err = runtime.InvalidateFromRuntimeEvent(ctx, chat.EventSessionCompactCompleted, "session-compact", map[string]interface{}{
		"session_generation": int64(1),
	})
	require.NoError(t, err)
	require.NotZero(t, result.Digests)
	require.NotZero(t, result.Memories)
	same, err = store.GetTurnDigestByTurn(ctx, "session-compact", "turn-1")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestInvalidated, same.Status)
	record, err = store.GetRecord(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.FreshnessStale, record.Freshness)
}

func TestInvalidateFromRuntimeEventBacktrackUsesCanonicalRange(t *testing.T) {
	ctx := context.Background()
	runtime, store := newInvalidationRuntimeForTest(t)
	first := recordInvalidationDigest(t, store, "session-backtrack", "turn-1", "m1", "m2", 2)
	second := recordInvalidationDigest(t, store, "session-backtrack", "turn-2", "m3", "m4", 4)
	_, err := store.MarkTurnDigestsProcessed(ctx, "session-backtrack", second.DigestSeq, "")
	require.NoError(t, err)
	kept := insertInvalidationCandidate(t, store, "session-backtrack", "m1", 2, "kept backtrack evidence")
	removed := insertInvalidationCandidate(t, store, "session-backtrack", "m3", 4, "removed backtrack evidence")

	result, err := runtime.InvalidateFromRuntimeEvent(ctx, chat.EventBacktrackFinished, "session-backtrack", map[string]interface{}{
		"mode":                           string(chat.BacktrackModeConversation),
		"session_generation":             int64(1),
		"error":                          "",
		"canonical_cutoff_seq":           int64(2),
		"canonical_message_count_before": int64(4),
		"tombstone": map[string]interface{}{
			"removed_message_count": int64(2),
			"removed_message_ids":   []interface{}{"m3", "m4"},
			"removed_turn_ids":      []interface{}{"turn-2"},
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Digests)
	require.Equal(t, 1, result.Memories)

	sameFirst, err := store.GetTurnDigestByTurn(ctx, "session-backtrack", "turn-1")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, sameFirst.Status)
	sameSecond, err := store.GetTurnDigestByTurn(ctx, "session-backtrack", "turn-2")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestInvalidated, sameSecond.Status)
	require.Equal(t, memorystore.TurnDigestRollupSkipped, sameSecond.RollupStatus)
	require.EqualValues(t, first.DigestSeq+1, second.DigestSeq)

	keptRecord, err := store.GetRecord(ctx, kept.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.FreshnessFresh, keptRecord.Freshness)
	removedRecord, err := store.GetRecord(ctx, removed.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.FreshnessStale, removedRecord.Freshness)
}

func TestInvalidateFromRuntimeEventLegacyBacktrackFallsBack(t *testing.T) {
	ctx := context.Background()
	runtime, store := newInvalidationRuntimeForTest(t)
	first := recordInvalidationDigest(t, store, "session-legacy", "turn-1", "m1", "m2", 2)
	second := recordInvalidationDigest(t, store, "session-legacy", "turn-2", "m3", "m4", 4)
	_, err := store.MarkTurnDigestsProcessed(ctx, "session-legacy", second.DigestSeq, "")
	require.NoError(t, err)

	result, err := runtime.InvalidateFromRuntimeEvent(ctx, chat.EventBacktrackFinished, "session-legacy", map[string]interface{}{
		"mode":               string(chat.BacktrackModeConversation),
		"session_generation": int64(1),
		"tombstone": map[string]interface{}{
			"removed_message_count": int64(40),
			"removed_message_ids":   []interface{}{"m3"},
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, result.Digests, "capped legacy IDs must fall back to full session invalidation")

	for _, digest := range []memorystore.TurnDigest{first, second} {
		same, err := store.GetTurnDigestByTurn(ctx, "session-legacy", digest.TurnID)
		require.NoError(t, err)
		require.Equal(t, memorystore.TurnDigestInvalidated, same.Status)
	}
}

func TestInvalidateFromRuntimeEventCodeOnlyBacktrackIsNoop(t *testing.T) {
	ctx := context.Background()
	runtime, store := newInvalidationRuntimeForTest(t)
	digest := recordInvalidationDigest(t, store, "session-code", "turn-1", "m1", "m2", 2)
	_, err := store.MarkTurnDigestsProcessed(ctx, "session-code", digest.DigestSeq, "")
	require.NoError(t, err)

	result, err := runtime.InvalidateFromRuntimeEvent(ctx, chat.EventBacktrackFinished, "session-code", map[string]interface{}{
		"mode": string(chat.BacktrackModeCode),
	})
	require.NoError(t, err)
	require.Zero(t, result.Digests)
	same, err := store.GetTurnDigestByTurn(ctx, "session-code", "turn-1")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, same.Status)
}

func TestInvalidateFromRuntimeEventBacktrackErrorFallsBack(t *testing.T) {
	ctx := context.Background()
	runtime, store := newInvalidationRuntimeForTest(t)
	digest := recordInvalidationDigest(t, store, "session-error", "turn-1", "m1", "m2", 2)
	_, err := store.MarkTurnDigestsProcessed(ctx, "session-error", digest.DigestSeq, "")
	require.NoError(t, err)

	result, err := runtime.InvalidateFromRuntimeEvent(ctx, chat.EventBacktrackFinished, "session-error", map[string]interface{}{
		"mode":               string(chat.BacktrackModeConversation),
		"session_generation": int64(1),
		"error":              "persist failed",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Digests)
	same, err := store.GetTurnDigestByTurn(ctx, "session-error", "turn-1")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestInvalidated, same.Status)
}

func newInvalidationRuntimeForTest(t *testing.T) (*Runtime, *memorystore.Store) {
	t.Helper()
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runtime := New(store, &fakeSource{}, &fakeExtractor{}, fakeModes{}, Config{
		Mode: "shadow", ProjectKey: store.ProjectKey(),
	})
	return runtime, store
}

func recordInvalidationDigest(t *testing.T, store *memorystore.Store, sessionID, turnID, userMessageID, assistantMessageID string, seq int64) memorystore.TurnDigest {
	t.Helper()
	digest, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: sessionID, SessionGeneration: 1, TurnID: turnID, Outcome: "success",
		UserMessageID: userMessageID, AssistantMessageID: assistantMessageID,
		UserText: "question", AssistantText: "answer", MessageSeqEnd: seq,
	})
	require.NoError(t, err)
	return digest
}

func insertInvalidationCandidate(t *testing.T, store *memorystore.Store, sessionID, messageID string, seq int64, text string) memorystore.Record {
	t.Helper()
	record, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: sessionID, SessionGeneration: 1, ProjectKey: store.ProjectKey(),
		SourceKind: "session_extract", Kind: "decision", Text: text,
		Confidence: 0.9, SourceMessageIDs: []string{messageID},
		SourceHash: "hash-" + messageID, SourceRevision: seq,
	})
	require.NoError(t, err)
	return record
}
