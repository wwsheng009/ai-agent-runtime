package memorystore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTurnWindowRollupGatesL0Retention(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	version, err := store.SchemaVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, 6)

	var digests []TurnDigest
	for _, turnID := range []string{"turn-1", "turn-2"} {
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: "session-rollup", TurnID: turnID, Outcome: "success",
			UserText: "question " + turnID, AssistantText: "answer " + turnID,
			MessageSeqEnd: int64(len(digests) + 1),
		})
		require.NoError(t, err)
		digests = append(digests, digest)
	}
	_, err = store.MarkTurnDigestsProcessed(ctx, "session-rollup", digests[len(digests)-1].DigestSeq, "extract-job")
	require.NoError(t, err)

	pruned, err := store.PruneTurnDigests(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, pruned, "L0 must survive until L1 rollup is done")

	ready, err := store.ListTurnDigestsForRollup(ctx, "session-rollup", 10)
	require.NoError(t, err)
	require.Len(t, ready, 2)
	for _, digest := range ready {
		require.Equal(t, TurnDigestRollupPending, digest.RollupStatus)
	}

	window, sourceChanged, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey:      store.ProjectKey(),
		SessionID:       "session-rollup",
		WindowIndex:     1,
		StartDigestSeq:  digests[0].DigestSeq,
		EndDigestSeq:    digests[1].DigestSeq,
		StartMessageSeq: 1,
		EndMessageSeq:   2,
		TurnCount:       2,
		SourceHash:      TurnWindowHash(digests),
		SourceRevision:  digests[1].DigestSeq,
		Status:          TurnWindowPending,
	})
	require.NoError(t, err)
	require.False(t, sourceChanged)
	require.Equal(t, TurnWindowPending, window.Status)

	window, err = store.CompleteTurnWindow(ctx, window.ID, TurnWindowSummary{
		Text:  "Window summary: the runtime was updated.",
		Model: "fake",
	})
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, window.Status)
	require.EqualValues(t, 1, window.SummaryRevision)
	require.NotEmpty(t, window.SummaryHash)

	rolled, err := store.MarkTurnDigestsRolledUp(ctx, "session-rollup",
		window.StartDigestSeq, window.EndDigestSeq, window.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, rolled)

	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.TurnWindows["done"])
	require.Equal(t, 2, status.TurnDigests["done"])

	pruned, err = store.PruneTurnDigests(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 2, pruned)

	windows, err := store.ListTurnWindows(ctx, "session-rollup", 10)
	require.NoError(t, err)
	require.Len(t, windows, 1, "L1 survives L0 retention")
	require.Equal(t, "Window summary: the runtime was updated.", windows[0].SummaryText)
}

func TestGenerationQualifiedTurnWindowStateTransitionsFenceStaleWorker(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	_, _, err = store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window-fence",
		SessionGeneration: 0, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "source-g1", Status: TurnWindowPending,
	})
	require.EqualError(t, err, "memorystore: positive session generation required for turn window")

	_, _, err = store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window-fence",
		SessionGeneration: 1, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		Status: TurnWindowPending,
	})
	require.EqualError(t, err, "memorystore: turn window source hash required")

	window, _, err := store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window-fence",
		SessionGeneration: 1, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "source-g1", Status: TurnWindowPending,
	})
	require.NoError(t, err)

	require.ErrorIs(t, store.MarkTurnWindowProcessingForGeneration(ctx, window.ID, 2, window.SourceHash), sql.ErrNoRows)
	current, err := store.GetTurnWindow(ctx, window.ID)
	require.NoError(t, err)
	require.Equal(t, TurnWindowPending, current.Status)

	require.NoError(t, store.MarkTurnWindowProcessingForGeneration(ctx, window.ID, 1, window.SourceHash))
	staleSourceHash := window.SourceHash
	window, sourceChanged, err := store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window-fence",
		SessionGeneration: 1, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "source-g1-updated", Status: TurnWindowPending,
	})
	require.NoError(t, err)
	require.True(t, sourceChanged)
	require.Equal(t, TurnWindowPending, window.Status)
	_, err = store.CompleteTurnWindowForGeneration(ctx, window.ID, 2, window.SourceHash,
		TurnWindowSummary{Text: "stale generation summary"})
	require.ErrorIs(t, err, sql.ErrNoRows)
	current, err = store.GetTurnWindow(ctx, window.ID)
	require.NoError(t, err)
	require.Equal(t, TurnWindowPending, current.Status)
	require.Empty(t, current.SummaryText)

	require.ErrorIs(t, store.MarkTurnWindowProcessingForGeneration(ctx, window.ID, 1, staleSourceHash), sql.ErrNoRows)
	_, err = store.CompleteTurnWindowForGeneration(ctx, window.ID, 1, staleSourceHash,
		TurnWindowSummary{Text: "stale source summary"})
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.ErrorIs(t, store.FailTurnWindowForGeneration(ctx, window.ID, 1, staleSourceHash, "stale failure"), sql.ErrNoRows)
	current, err = store.GetTurnWindow(ctx, window.ID)
	require.NoError(t, err)
	require.Equal(t, TurnWindowPending, current.Status)
	require.Empty(t, current.SummaryText)

	require.NoError(t, store.MarkTurnWindowProcessingForGeneration(ctx, window.ID, 1, window.SourceHash))
	completed, err := store.CompleteTurnWindowForGeneration(ctx, window.ID, 1, window.SourceHash,
		TurnWindowSummary{Text: "current summary"})
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, completed.Status)
	require.Equal(t, "current summary", completed.SummaryText)

	// A positive-generation row with no source hash cannot prove which input
	// produced its summary. The generation-qualified path must reset it rather
	// than carrying unverified derived state forward.
	unverified, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window-unknown-source",
		SessionGeneration: 1, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		Status: TurnWindowPending,
	})
	require.NoError(t, err)
	unverified, err = store.CompleteTurnWindow(ctx, unverified.ID, TurnWindowSummary{Text: "unverified legacy summary"})
	require.NoError(t, err)

	refreshed, sourceChanged, err := store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window-unknown-source",
		SessionGeneration: 1, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "verified-source", Status: TurnWindowPending,
	})
	require.NoError(t, err)
	require.True(t, sourceChanged)
	require.Equal(t, TurnWindowPending, refreshed.Status)
	require.Empty(t, refreshed.SummaryText)
}

func TestTurnWindowWritesRespectSessionPurgeFence(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const sessionID = "session-window-purge"

	legacy, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: sessionID,
		SessionGeneration: 0, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "legacy-source", Status: TurnWindowPending,
	})
	require.NoError(t, err)
	legacy, err = store.CompleteTurnWindow(ctx, legacy.ID, TurnWindowSummary{
		Text: "legacy summary before purge",
	})
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, legacy.Status)

	_, err = store.PurgeSessionWithGenerations(ctx, sessionID, 1, 1)
	require.NoError(t, err)
	current, err := store.GetTurnWindow(ctx, legacy.ID)
	require.NoError(t, err, "generation-specific purge must leave generationless quarantine rows")
	require.Zero(t, current.SessionGeneration)
	require.Equal(t, TurnWindowDone, current.Status)

	_, _, err = store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: sessionID,
		SessionGeneration: 1, WindowIndex: 2, StartDigestSeq: 2, EndDigestSeq: 2,
		SourceHash: "stale-source", Status: TurnWindowPending,
	})
	require.ErrorIs(t, err, ErrSessionPurgeGenerationMismatch,
		"a worker from the purged incarnation must not create a window")

	_, _, err = store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: sessionID,
		SessionGeneration: 0, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "legacy-source-refresh", Status: TurnWindowPending,
	})
	require.ErrorIs(t, err, ErrSessionPurgeGenerationMismatch,
		"generationless writes cannot prove incarnation after any tombstone")

	require.ErrorIs(t,
		store.MarkTurnWindowProcessing(ctx, legacy.ID),
		ErrSessionPurgeGenerationMismatch)
	_, err = store.CompleteTurnWindow(ctx, legacy.ID, TurnWindowSummary{Text: "late legacy summary"})
	require.ErrorIs(t, err, ErrSessionPurgeGenerationMismatch)
	require.ErrorIs(t,
		store.FailTurnWindow(ctx, legacy.ID, "late legacy failure"),
		ErrSessionPurgeGenerationMismatch)
	current, err = store.GetTurnWindow(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, current.Status)
	require.Equal(t, "legacy summary before purge", current.SummaryText)

	modern, _, err := store.EnsureTurnWindowForGeneration(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: sessionID,
		SessionGeneration: 2, WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		SourceHash: "modern-source", Status: TurnWindowPending,
	})
	require.NoError(t, err, "a newer incarnation must be able to create its own window")
	require.EqualValues(t, 2, modern.SessionGeneration)
	require.NoError(t, store.MarkTurnWindowProcessingForGeneration(
		ctx, modern.ID, 2, modern.SourceHash))
	modern, err = store.CompleteTurnWindowForGeneration(
		ctx, modern.ID, 2, modern.SourceHash, TurnWindowSummary{Text: "modern summary"})
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, modern.Status)
}

func TestTurnWindowSourceChangeResetsAndInvalidation(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	first, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-window", TurnID: "turn-1", Outcome: "success",
		AssistantText: "first summary", MessageSeqEnd: 2,
	})
	require.NoError(t, err)
	_, err = store.MarkTurnDigestsProcessed(ctx, "session-window", first.DigestSeq, "")
	require.NoError(t, err)

	window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window", WindowIndex: 1,
		StartDigestSeq: first.DigestSeq, EndDigestSeq: first.DigestSeq,
		TurnCount: 1, SourceHash: TurnWindowHash([]TurnDigest{first}), Status: TurnWindowPending,
	})
	require.NoError(t, err)
	window, err = store.CompleteTurnWindow(ctx, window.ID, TurnWindowSummary{Text: "first summary"})
	require.NoError(t, err)

	changed := first
	changed.AssistantText = "changed summary"
	changed.DigestHash = TurnDigestHash(changed)
	refreshed, sourceChanged, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-window", WindowIndex: 1,
		StartDigestSeq: first.DigestSeq, EndDigestSeq: first.DigestSeq,
		TurnCount: 1, SourceHash: TurnWindowHash([]TurnDigest{changed}), Status: TurnWindowPending,
	})
	require.NoError(t, err)
	require.True(t, sourceChanged)
	require.Equal(t, TurnWindowPending, refreshed.Status)
	require.Empty(t, refreshed.SummaryText)
	require.EqualValues(t, 1, refreshed.SummaryRevision)

	invalidated, err := store.InvalidateTurnWindowsForSession(ctx, "session-window", "compaction")
	require.NoError(t, err)
	require.EqualValues(t, 1, invalidated)
	invalidated, err = store.InvalidateTurnWindowsForSession(ctx, "session-window", "compaction")
	require.NoError(t, err)
	require.Zero(t, invalidated, "invalidation must be idempotent")

	deleted, err := store.DeleteTurnWindowsForSession(ctx, "session-window")
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
}

func TestTurnWindowDerivedCandidateStale(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-candidate", ProjectKey: store.ProjectKey(),
		SourceKind: "turn_window", SourceWindow: TurnWindowSourceWindow(1, 2),
		Kind: "reference", Text: "window summary candidate", Confidence: 1,
		SourceHash: "window-hash", SourceRevision: 2,
	})
	require.NoError(t, err)
	require.Equal(t, "turn_window", record.SourceKind)

	stale, err := store.MarkTurnWindowCandidatesStale(ctx, "session-candidate", TurnWindowSourceWindow(1, 2), "turn_window_source_changed")
	require.NoError(t, err)
	require.Equal(t, 1, stale)
	stale, err = store.MarkTurnWindowCandidatesStale(ctx, "session-candidate", TurnWindowSourceWindow(1, 2), "turn_window_source_changed")
	require.NoError(t, err)
	require.Zero(t, stale, "stale marking must be idempotent")
}

func TestResetClearsTurnDigestsAndWindows(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	digest, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-reset", TurnID: "turn-1", Outcome: "success",
	})
	require.NoError(t, err)
	_, _, err = store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-reset", WindowIndex: 1,
		StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
		TurnCount: 1, SourceHash: TurnWindowHash([]TurnDigest{digest}),
		Status: TurnWindowPending,
	})
	require.NoError(t, err)

	require.NoError(t, store.Reset(ctx))
	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Empty(t, status.TurnDigests)
	require.Empty(t, status.TurnWindows)
}

func TestPruneTurnWindowsRetention(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)

	create := func(sessionID string, createdAt time.Time, status TurnWindowStatus, complete, invalidate, fail bool) TurnWindow {
		t.Helper()
		window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
			ProjectKey: store.ProjectKey(), SessionID: sessionID, WindowIndex: 1,
			StartDigestSeq: 1, EndDigestSeq: 1, StartMessageSeq: 1, EndMessageSeq: 2,
			TurnCount: 1, SourceHash: "hash-" + sessionID, Status: status, CreatedAt: createdAt,
		})
		require.NoError(t, err)
		if complete {
			window, err = store.CompleteTurnWindow(ctx, window.ID, TurnWindowSummary{Text: "summary " + sessionID})
			require.NoError(t, err)
		}
		if invalidate {
			_, err = store.InvalidateTurnWindowsForSession(ctx, sessionID, "backtrack")
			require.NoError(t, err)
			window, err = store.GetTurnWindow(ctx, window.ID)
			require.NoError(t, err)
		}
		if fail {
			require.NoError(t, store.FailTurnWindow(ctx, window.ID, "summarizer failed"))
			window, err = store.GetTurnWindow(ctx, window.ID)
			require.NoError(t, err)
		}
		return window
	}

	oldDone := create("window-old-done", old, TurnWindowPending, true, false, false)
	oldInvalidated := create("window-old-invalidated", old, TurnWindowPending, false, true, false)
	oldFailed := create("window-old-failed", old, TurnWindowPending, false, false, true)
	oldPending := create("window-old-pending", old, TurnWindowPending, false, false, false)
	recentDone := create("window-recent-done", recent, TurnWindowPending, true, false, false)

	deleted, err := store.PruneTurnWindows(ctx, now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)

	for _, id := range []string{oldDone.ID, oldInvalidated.ID} {
		_, err := store.GetTurnWindow(ctx, id)
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	for _, id := range []string{oldFailed.ID, oldPending.ID, recentDone.ID} {
		_, err := store.GetTurnWindow(ctx, id)
		require.NoError(t, err)
	}
}

func TestPurgeTurnWindowsReleasesRollupWatermarks(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	createRolledUp := func(sessionID, turnID string, seq int64) TurnWindow {
		t.Helper()
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: sessionID, TurnID: turnID, Outcome: "success", MessageSeqEnd: seq,
		})
		require.NoError(t, err)
		_, err = store.MarkTurnDigestsProcessed(ctx, sessionID, digest.DigestSeq, "")
		require.NoError(t, err)
		window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
			ProjectKey: store.ProjectKey(), SessionID: sessionID, WindowIndex: 1,
			StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
			StartMessageSeq: 1, EndMessageSeq: seq, TurnCount: 1,
			SourceHash: "hash-" + sessionID, SourceRevision: digest.DigestSeq,
			Status: TurnWindowPending,
		})
		require.NoError(t, err)
		window, err = store.CompleteTurnWindow(ctx, window.ID, TurnWindowSummary{Text: "summary " + sessionID})
		require.NoError(t, err)
		_, err = store.MarkTurnDigestsRolledUp(ctx, sessionID, digest.DigestSeq, digest.DigestSeq, window.ID)
		require.NoError(t, err)
		return window
	}

	windowA := createRolledUp("purge-a", "turn-a", 2)
	windowB := createRolledUp("purge-b", "turn-b", 4)

	result, err := store.PurgeTurnWindows(ctx, "purge-a")
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Windows)
	require.EqualValues(t, 1, result.Digests)
	_, err = store.GetTurnWindow(ctx, windowA.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.GetTurnWindow(ctx, windowB.ID)
	require.NoError(t, err)
	digestA, err := store.GetTurnDigestByTurn(ctx, "purge-a", "turn-a")
	require.NoError(t, err)
	require.Equal(t, TurnDigestRollupSkipped, digestA.RollupStatus)
	require.Empty(t, digestA.RollupWindowID)
	digestB, err := store.GetTurnDigestByTurn(ctx, "purge-b", "turn-b")
	require.NoError(t, err)
	require.Equal(t, TurnDigestRollupDone, digestB.RollupStatus)

	result, err = store.PurgeTurnWindows(ctx, "")
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Windows)
	require.EqualValues(t, 1, result.Digests)
	_, err = store.GetTurnWindow(ctx, windowB.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	digestB, err = store.GetTurnDigestByTurn(ctx, "purge-b", "turn-b")
	require.NoError(t, err)
	require.Equal(t, TurnDigestRollupSkipped, digestB.RollupStatus)
}
