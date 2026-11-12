package memorystore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTurnDigestLifecycleAndBacklog(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	version, err := store.SchemaVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, 6)

	digest, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID:          "session-turn-digest",
		TurnID:             "turn-1",
		TraceID:            "trace-1",
		UserMessageID:      "msg-user",
		AssistantMessageID: "msg-assistant",
		UserText:           "What changed?",
		AssistantText:      "The runtime was updated.",
		Outcome:            "success",
		MessageSeqEnd:      4,
	})
	require.NoError(t, err)
	require.NotEmpty(t, digest.ID)
	require.Greater(t, digest.DigestSeq, int64(0))
	require.Equal(t, TurnDigestPending, digest.Status)
	require.NotEmpty(t, digest.DigestHash)
	require.EqualValues(t, 4, digest.MessageSeqEnd)
	prevEnd, err := store.PreviousTurnDigestMessageSeqEnd(ctx, "session-turn-digest", digest.DigestSeq+1)
	require.NoError(t, err)
	require.EqualValues(t, 4, prevEnd)

	pendingSessions, err := store.ListPendingTurnDigestSessions(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, pendingSessions, 1)
	require.Equal(t, 1, pendingSessions[0].Pending)

	pending, err := store.ListPendingTurnDigests(ctx, "session-turn-digest", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "turn-1", pending[0].TurnID)

	updated, err := store.MarkTurnDigestsProcessed(ctx, "session-turn-digest", digest.DigestSeq, "job-1")
	require.NoError(t, err)
	require.EqualValues(t, 1, updated)
	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.TurnDigests["done"])

	done, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID:          "session-turn-digest",
		TurnID:             "turn-1",
		TraceID:            "trace-1",
		UserMessageID:      "msg-user",
		AssistantMessageID: "msg-assistant",
		UserText:           "What changed?",
		AssistantText:      "The runtime was updated.",
		Outcome:            "success",
		MessageSeqEnd:      4,
	})
	require.NoError(t, err)
	require.Equal(t, TurnDigestDone, done.Status, "identical retry must not reopen a done digest")

	reopened, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID:          "session-turn-digest",
		TurnID:             "turn-1",
		TraceID:            "trace-1",
		UserMessageID:      "msg-user",
		AssistantMessageID: "msg-assistant",
		UserText:           "What changed?",
		AssistantText:      "The runtime was updated again.",
		Outcome:            "success",
		MessageSeqEnd:      4,
	})
	require.NoError(t, err)
	require.Equal(t, TurnDigestPending, reopened.Status)
	require.True(t, reopened.ProcessedAt.IsZero(), "changed content must clear processed_at")

	deferred, err := store.MarkTurnDigestsSkipped(ctx, "session-turn-digest", reopened.DigestSeq, "job-2", "oversize_wait_window_reader")
	require.NoError(t, err)
	require.EqualValues(t, 1, deferred)

	pruned, err := store.PruneTurnDigests(ctx, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, pruned)
}

func TestTurnDigestDeleteAndInvalidate(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	for _, turnID := range []string{"turn-1", "turn-2"} {
		_, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: "session-delete", TurnID: turnID, Outcome: "success",
			UserText: "question", AssistantText: "answer",
		})
		require.NoError(t, err)
	}
	invalidated, err := store.InvalidateTurnDigestsForSession(ctx, "session-delete")
	require.NoError(t, err)
	require.EqualValues(t, 2, invalidated)
	count, err := store.CountPendingTurnDigests(ctx, "")
	require.NoError(t, err)
	require.Zero(t, count)

	deleted, err := store.DeleteTurnDigestsForSession(ctx, "session-delete")
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)
}

func TestTurnDigestDeferredSurvivesPrune(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	digest, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-deferred", TurnID: "turn-1", Outcome: "success",
	})
	require.NoError(t, err)
	updated, err := store.MarkTurnDigestsDeferred(ctx, "session-deferred", digest.DigestSeq, "", "oversize_wait_window_reader")
	require.NoError(t, err)
	require.EqualValues(t, 1, updated)
	same, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-deferred", TurnID: "turn-1", Outcome: "success",
	})
	require.NoError(t, err)
	require.Equal(t, TurnDigestDeferred, same.Status, "identical retry must not reopen deferred backlog")
	pruned, err := store.PruneTurnDigests(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, pruned, "deferred backlog must survive retention until Phase 1.5 resets it")
}
