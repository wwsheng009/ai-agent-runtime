package memorystore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInvalidateSessionRangeTargetsOnlyRemovedWindows(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	generation := int64(1)

	var digests []TurnDigest
	for index, turnID := range []string{"turn-1", "turn-2", "turn-3"} {
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: "session-range", SessionGeneration: generation, TurnID: turnID, Outcome: "success",
			UserMessageID:      "m" + string(rune('1'+index*2)),
			AssistantMessageID: "m" + string(rune('2'+index*2)),
			UserText:           "question", AssistantText: "answer",
			MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
		digests = append(digests, digest)
	}
	_, err = store.MarkTurnDigestsProcessed(ctx, "session-range", digests[1].DigestSeq, "")
	require.NoError(t, err)

	windows := make([]TurnWindow, 0, 2)
	for index, digest := range digests {
		window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
			ProjectKey: store.ProjectKey(), SessionID: "session-range", SessionGeneration: generation, WindowIndex: index + 1,
			StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
			StartMessageSeq: int64(index*2 + 1), EndMessageSeq: int64((index + 1) * 2),
			TurnCount: 1, SourceHash: TurnWindowHash([]TurnDigest{digest}),
			SourceRevision: digest.DigestSeq, Status: TurnWindowPending,
		})
		require.NoError(t, err)
		window, err = store.CompleteTurnWindow(ctx, window.ID, TurnWindowSummary{Text: "window " + digest.TurnID})
		require.NoError(t, err)
		windows = append(windows, window)
		_, err = store.MarkTurnDigestsRolledUp(ctx, "session-range", digest.DigestSeq, digest.DigestSeq, window.ID)
		require.NoError(t, err)
	}

	first, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-range", SessionGeneration: generation, TurnID: "turn-1", ProjectKey: store.ProjectKey(),
		SourceKind: "session_extract", SourceWindow: "1-2",
		Kind: "decision", Text: "kept evidence", Confidence: 0.9,
		SourceMessageIDs: []string{digests[0].UserMessageID, digests[0].AssistantMessageID},
		SourceHash:       "hash-1", SourceRevision: 2,
	})
	require.NoError(t, err)
	second, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-range", SessionGeneration: generation, TurnID: "turn-2", ProjectKey: store.ProjectKey(),
		SourceKind: "session_extract", SourceWindow: "3-4",
		Kind: "decision", Text: "removed evidence", Confidence: 0.9,
		SourceMessageIDs: []string{digests[1].UserMessageID, digests[1].AssistantMessageID},
		SourceHash:       "hash-2", SourceRevision: 4,
	})
	require.NoError(t, err)
	firstWindowCandidate, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-range", SessionGeneration: generation, ProjectKey: store.ProjectKey(),
		SourceKind: "turn_window", SourceWindow: TurnWindowSourceWindowGeneration(generation, digests[0].DigestSeq, digests[0].DigestSeq),
		Kind: "reference", Text: "kept window summary", Confidence: 1,
		SourceHash: windows[0].SourceHash, SourceRevision: windows[0].SourceRevision,
	})
	require.NoError(t, err)
	secondWindowCandidate, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-range", SessionGeneration: generation, ProjectKey: store.ProjectKey(),
		SourceKind: "turn_window", SourceWindow: TurnWindowSourceWindowGeneration(generation, digests[1].DigestSeq, digests[1].DigestSeq),
		Kind: "reference", Text: "removed window summary", Confidence: 1,
		SourceHash: windows[1].SourceHash, SourceRevision: windows[1].SourceRevision,
	})
	require.NoError(t, err)
	third, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-range", SessionGeneration: generation, TurnID: "turn-3", ProjectKey: store.ProjectKey(),
		SourceKind: "session_extract", SourceWindow: "5-6",
		Kind: "decision", Text: "future evidence", Confidence: 0.9,
		SourceMessageIDs: []string{digests[2].UserMessageID, digests[2].AssistantMessageID},
		SourceHash:       "hash-3", SourceRevision: 6,
	})
	require.NoError(t, err)
	thirdWindowCandidate, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-range", SessionGeneration: generation, ProjectKey: store.ProjectKey(),
		SourceKind: "turn_window", SourceWindow: TurnWindowSourceWindowGeneration(generation, digests[2].DigestSeq, digests[2].DigestSeq),
		Kind: "reference", Text: "future window summary", Confidence: 1,
		SourceHash: windows[2].SourceHash, SourceRevision: windows[2].SourceRevision,
	})
	require.NoError(t, err)

	result, err := store.InvalidateSessionRange(ctx, "session-range", SessionRangeInvalidation{
		CutoffMessageSeq:  2,
		BeforeMessageSeq:  4,
		MessageIDs:        []string{digests[1].UserMessageID, digests[1].AssistantMessageID},
		TurnIDs:           []string{"turn-2"},
		SessionGeneration: &generation,
	}, "backtrack")
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Digests)
	require.EqualValues(t, 1, result.Windows)
	require.Equal(t, 2, result.Memories)

	status1, rollup1 := turnDigestStateForTest(t, store, digests[0].DigestSeq)
	status2, rollup2 := turnDigestStateForTest(t, store, digests[1].DigestSeq)
	require.Equal(t, string(TurnDigestDone), status1)
	require.Equal(t, string(TurnDigestRollupDone), rollup1)
	require.Equal(t, string(TurnDigestInvalidated), status2)
	require.Equal(t, string(TurnDigestRollupSkipped), rollup2)

	window1, err := store.GetTurnWindow(ctx, windows[0].ID)
	require.NoError(t, err)
	window2, err := store.GetTurnWindow(ctx, windows[1].ID)
	require.NoError(t, err)
	window3, err := store.GetTurnWindow(ctx, windows[2].ID)
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, window1.Status)
	require.Equal(t, TurnWindowInvalidated, window2.Status)
	require.Equal(t, TurnWindowDone, window3.Status, "windows after the removed range must stay fresh")

	for _, record := range []Record{first, firstWindowCandidate, third, thirdWindowCandidate} {
		got, err := store.GetRecord(ctx, record.ID)
		require.NoError(t, err)
		require.Equal(t, FreshnessFresh, got.Freshness, record.Text)
	}
	for _, record := range []Record{second, secondWindowCandidate} {
		got, err := store.GetRecord(ctx, record.ID)
		require.NoError(t, err)
		require.Equal(t, FreshnessStale, got.Freshness, record.Text)
		require.Equal(t, "backtrack", got.StaleReason)
	}
}

func TestInvalidateSessionRangeUsesMessageIDsWithoutCutoff(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	generation := int64(1)

	digest, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-id-range", SessionGeneration: generation, TurnID: "turn-1", Outcome: "success",
		UserMessageID: "m1", AssistantMessageID: "m2", MessageSeqEnd: 2,
	})
	require.NoError(t, err)
	_, err = store.MarkTurnDigestsProcessed(ctx, "session-id-range", digest.DigestSeq, "")
	require.NoError(t, err)
	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-id-range", SessionGeneration: generation, ProjectKey: store.ProjectKey(),
		SourceKind: "session_extract", Kind: "fact", Text: "legacy id-only evidence",
		SourceMessageIDs: []string{"m1"}, SourceHash: "legacy", SourceRevision: 1,
	})
	require.NoError(t, err)

	result, err := store.InvalidateSessionRange(ctx, "session-id-range", SessionRangeInvalidation{
		MessageIDs: []string{"m1"}, SessionGeneration: &generation,
	}, "backtrack")
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Digests)
	require.Equal(t, 1, result.Memories)
	got, err := store.GetRecord(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, FreshnessStale, got.Freshness)
}

func TestInvalidateSessionRangeDoesNotTouchRowsCreatedAfterEvent(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	generation := int64(1)

	oldDigests := make([]TurnDigest, 0, 2)
	for index, turnID := range []string{"turn-1", "turn-2"} {
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: "session-race", SessionGeneration: generation, TurnID: turnID, Outcome: "success",
			UserMessageID:      "m" + string(rune('1'+index*2)),
			AssistantMessageID: "m" + string(rune('2'+index*2)),
			MessageSeqEnd:      int64((index + 1) * 2),
		})
		require.NoError(t, err)
		oldDigests = append(oldDigests, digest)
	}
	_, err = store.MarkTurnDigestsProcessed(ctx, "session-race", oldDigests[1].DigestSeq, "")
	require.NoError(t, err)

	eventTime := time.Now().UTC()
	after, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-race", SessionGeneration: generation, TurnID: "turn-after", Outcome: "success",
		UserMessageID: "m5", AssistantMessageID: "m6", MessageSeqEnd: 6,
		CreatedAt: eventTime.Add(time.Second),
	})
	require.NoError(t, err)
	_, err = store.MarkTurnDigestsProcessed(ctx, "session-race", after.DigestSeq, "")
	require.NoError(t, err)
	lateWindow, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-race", SessionGeneration: generation, WindowIndex: 1,
		StartDigestSeq: oldDigests[1].DigestSeq, EndDigestSeq: oldDigests[1].DigestSeq,
		StartMessageSeq: 3, EndMessageSeq: 4, TurnCount: 1,
		SourceHash: "late-window", SourceRevision: oldDigests[1].DigestSeq,
		Status: TurnWindowPending, CreatedAt: eventTime.Add(time.Second),
	})
	require.NoError(t, err)

	result, err := store.InvalidateSessionRange(ctx, "session-race", SessionRangeInvalidation{
		CutoffMessageSeq:  2,
		BeforeMessageSeq:  6,
		CutoffAt:          eventTime,
		SessionGeneration: &generation,
	}, "backtrack")
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Digests, "only the old row in the removed range is invalidated")
	require.EqualValues(t, 1, result.Windows, "a late L1 window covering removed L0 evidence must still be invalidated")

	statusAfter, rollupAfter := turnDigestStateForTest(t, store, after.DigestSeq)
	require.Equal(t, string(TurnDigestDone), statusAfter)
	require.Equal(t, string(TurnDigestRollupPending), rollupAfter)
	windowAfter, err := store.GetTurnWindow(ctx, lateWindow.ID)
	require.NoError(t, err)
	require.Equal(t, TurnWindowInvalidated, windowAfter.Status)
}

func TestInvalidateSessionRangeRejectsGenerationlessIdentity(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	digest, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-generationless", TurnID: "legacy-turn",
		UserMessageID: "legacy-user", AssistantMessageID: "legacy-assistant",
		MessageSeqEnd: 2,
	})
	require.NoError(t, err)

	for _, generation := range []*int64{nil, int64Pointer(0), int64Pointer(-1)} {
		result, err := store.InvalidateSessionRange(ctx, "session-generationless", SessionRangeInvalidation{
			MessageIDs:        []string{"legacy-user"},
			SessionGeneration: generation,
		}, "backtrack")
		require.ErrorContains(t, err, "positive session generation required")
		require.Zero(t, result)
	}
	got, err := store.GetTurnDigestByTurn(ctx, digest.SessionID, digest.TurnID)
	require.NoError(t, err)
	require.Equal(t, TurnDigestPending, got.Status, "rejected generationless invalidation must not mutate legacy rows")
}

func TestInvalidateSessionRangeIsolatesMixedGenerations(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const sessionID = "session-mixed-generations"
	generations := []int64{0, 1, 2}
	type fixture struct {
		generation int64
		digest     TurnDigest
		window     TurnWindow
		records    []Record
	}
	fixtures := make([]fixture, 0, len(generations))

	for _, generation := range generations {
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: sessionID, SessionGeneration: generation,
			TurnID: fmt.Sprintf("turn-g%d", generation), Outcome: "success",
			UserMessageID:      fmt.Sprintf("user-g%d", generation),
			AssistantMessageID: fmt.Sprintf("assistant-g%d", generation),
			UserText:           "question", AssistantText: "answer", MessageSeqEnd: 2,
		})
		require.NoError(t, err)
		window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
			ProjectKey: store.ProjectKey(), SessionID: sessionID, SessionGeneration: generation,
			WindowIndex: 1, StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
			StartMessageSeq: 1, EndMessageSeq: 2, TurnCount: 1,
			SourceHash: TurnWindowHash([]TurnDigest{digest}), SourceRevision: digest.DigestSeq,
		})
		require.NoError(t, err)
		window, err = store.CompleteTurnWindow(ctx, window.ID, TurnWindowSummary{Text: fmt.Sprintf("summary g%d", generation)})
		require.NoError(t, err)

		sessionRecord, err := store.InsertCandidate(ctx, Candidate{
			SessionID: sessionID, SessionGeneration: generation, TurnID: digest.TurnID,
			ProjectKey: store.ProjectKey(), SourceKind: "session_extract",
			SourceWindow: "1-2", SourceMessageIDs: []string{digest.UserMessageID},
			Kind: "fact", Text: fmt.Sprintf("session evidence g%d", generation),
			Confidence: 0.9, SourceHash: fmt.Sprintf("session-hash-g%d", generation), SourceRevision: 1,
		})
		require.NoError(t, err)
		windowRecord, err := store.InsertCandidate(ctx, Candidate{
			SessionID: sessionID, SessionGeneration: generation, ProjectKey: store.ProjectKey(),
			SourceKind:   "turn_window",
			SourceWindow: TurnWindowSourceWindowGeneration(generation, digest.DigestSeq, digest.DigestSeq),
			Kind:         "reference", Text: fmt.Sprintf("window evidence g%d", generation),
			Confidence: 1, SourceHash: window.SourceHash, SourceRevision: window.SourceRevision,
		})
		require.NoError(t, err)
		fixtures = append(fixtures, fixture{
			generation: generation, digest: digest, window: window,
			records: []Record{sessionRecord, windowRecord},
		})
	}

	targetGeneration := int64(1)
	target := fixtures[1]
	result, err := store.InvalidateSessionRange(ctx, sessionID, SessionRangeInvalidation{
		MessageIDs:        []string{target.digest.UserMessageID},
		SessionGeneration: &targetGeneration,
	}, "backtrack")
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Digests)
	require.EqualValues(t, 1, result.Windows)
	require.Equal(t, 2, result.Memories)

	for _, item := range fixtures {
		gotDigest, err := store.GetTurnDigestByTurn(ctx, sessionID, item.digest.TurnID)
		require.NoError(t, err)
		gotWindow, err := store.GetTurnWindow(ctx, item.window.ID)
		require.NoError(t, err)
		if item.generation == targetGeneration {
			require.Equal(t, TurnDigestInvalidated, gotDigest.Status)
			require.Equal(t, TurnWindowInvalidated, gotWindow.Status)
		} else {
			require.Equal(t, TurnDigestPending, gotDigest.Status, "generation %d digest must remain unchanged", item.generation)
			require.Equal(t, TurnWindowDone, gotWindow.Status, "generation %d window must remain unchanged", item.generation)
		}
		for _, record := range item.records {
			gotRecord, err := store.GetRecord(ctx, record.ID)
			require.NoError(t, err)
			if item.generation == targetGeneration {
				require.Equal(t, FreshnessStale, gotRecord.Freshness)
			} else {
				require.Equal(t, FreshnessFresh, gotRecord.Freshness,
					"generation %d source must remain unchanged", item.generation)
			}
		}
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func turnDigestStateForTest(t *testing.T, store *Store, digestSeq int64) (string, string) {
	t.Helper()
	db, err := store.ensureV2()
	require.NoError(t, err)
	var status, rollup string
	err = db.QueryRow(`
SELECT status, rollup_status FROM memory_turn_digests WHERE digest_seq = ?
`, digestSeq).Scan(&status, &rollup)
	require.NoError(t, err)
	return status, rollup
}
