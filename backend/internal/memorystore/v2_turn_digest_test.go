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

func TestTurnDigestIdentityIncludesSessionGeneration(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	for _, generation := range []int64{1, 2} {
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: "session-reused", SessionGeneration: generation,
			TurnID: "turn-reused", Outcome: "success",
			UserText: "question", AssistantText: "answer",
		})
		require.NoError(t, err)
		require.Equal(t, generation, digest.SessionGeneration)
	}

	gen1, err := store.GetTurnDigestByTurnGeneration(ctx, "session-reused", "turn-reused", 1)
	require.NoError(t, err)
	gen2, err := store.GetTurnDigestByTurnGeneration(ctx, "session-reused", "turn-reused", 2)
	require.NoError(t, err)
	require.NotEqual(t, gen1.DigestSeq, gen2.DigestSeq)
	require.EqualValues(t, 1, gen1.SessionGeneration)
	require.EqualValues(t, 2, gen2.SessionGeneration)

	var count int
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND turn_id = ? AND capture_receipt_id = ''
`, store.ProjectKey(), "session-reused", "turn-reused").Scan(&count))
	require.Equal(t, 2, count)
}

func TestRecordTurnDigestHonorsSessionPurgeFence(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "project-digest-purge-fence",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	_, err = store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-digest-purge-fence", SessionGeneration: 1,
		TurnID: "turn-old", Outcome: "success",
	})
	require.NoError(t, err)
	_, err = store.PurgeSession(ctx, "session-digest-purge-fence", 1)
	require.NoError(t, err)

	_, err = store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-digest-purge-fence", SessionGeneration: 1,
		TurnID: "turn-stale", Outcome: "success",
	})
	require.ErrorIs(t, err, ErrSessionPurgeGenerationMismatch)

	_, err = store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-digest-purge-fence", TurnID: "turn-legacy",
		Outcome: "success",
	})
	require.ErrorIs(t, err, ErrSessionPurgeGenerationMismatch)

	newGeneration, err := store.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "session-digest-purge-fence", SessionGeneration: 2,
		TurnID: "turn-new", Outcome: "success",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, newGeneration.SessionGeneration)

	foreignProject, err := store.RecordTurnDigest(ctx, TurnDigest{
		ProjectKey: "project-digest-purge-fence-foreign",
		SessionID:  "session-digest-purge-fence", SessionGeneration: 1,
		TurnID: "turn-foreign", Outcome: "success",
	})
	require.NoError(t, err)
	require.Equal(t, "project-digest-purge-fence-foreign", foreignProject.ProjectKey)
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

func TestMaterializeTurnCaptureIsAtomicAndIdempotent(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	capture := TurnCapture{
		ID: "capture_once", ProjectKey: store.ProjectKey(),
		SessionID: "session-capture", TurnID: "turn-capture",
		MessageSeqStart: 2, MessageSeqEnd: 4, CanonicalRevision: 1,
		RangeHash: "hash-range", TraceID: "trace-capture",
		UserMessageID: "user-msg", AssistantMessageID: "assistant-msg",
		Outcome: "success", UsageJSON: "{}", CreatedAt: time.Now().UTC(),
	}

	first, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)
	require.NotEmpty(t, first.DigestID)
	require.NotEmpty(t, first.JobID)

	second, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)
	require.Equal(t, first, second, "replaying an unacknowledged receipt must reuse its digest/job")

	digest, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Equal(t, capture.ID, digest.CaptureReceiptID)
	require.EqualValues(t, 2, digest.MessageSeqStart)
	require.EqualValues(t, 4, digest.MessageSeqEnd)
	require.EqualValues(t, capture.CanonicalRevision, digest.SourceRevision)
	require.Equal(t, capture.RangeHash, digest.RangeHash)

	var digestCount, jobCount int
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_turn_digests
WHERE project_key = ? AND capture_receipt_id = ?
`, capture.ProjectKey, capture.ID).Scan(&digestCount))
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_jobs
WHERE project_key = ? AND kind = 'phase1_extract/v2' AND job_key = ?
`, capture.ProjectKey, first.JobKey).Scan(&jobCount))
	require.Equal(t, 1, digestCount)
	require.Equal(t, 1, jobCount)
}

func TestMaterializeTurnCaptureRejectsChangedSourceIdentity(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	capture := TurnCapture{
		ID:                "capture-identity",
		ProjectKey:        store.ProjectKey(),
		SessionID:         "session-identity",
		TurnID:            "turn-identity",
		MessageSeqStart:   1,
		MessageSeqEnd:     2,
		CanonicalRevision: 1,
		RangeHash:         "range-v1",
		Outcome:           "success",
		UsageJSON:         "{}",
		CreatedAt:         time.Now().UTC(),
	}
	first, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)

	changed := capture
	changed.CanonicalRevision = 2
	changed.RangeHash = "range-v2"
	_, err = store.MaterializeTurnCapture(ctx, changed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity conflicts")

	digest, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Equal(t, first.DigestID, digest.ID)
	require.Equal(t, capture.ID, digest.CaptureReceiptID)
	require.Equal(t, int64(1), digest.SourceRevision)
	require.Equal(t, capture.RangeHash, digest.RangeHash)

	var count int
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_jobs WHERE project_key = ? AND kind = 'phase1_extract/v2'
`, store.ProjectKey()).Scan(&count))
	require.Equal(t, 1, count)
}

func TestMaterializeTurnCaptureRejectsDifferentSessionGeneration(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	first := TurnCapture{
		ID:                "capture-generation-1",
		ProjectKey:        store.ProjectKey(),
		SessionID:         "same-session",
		SessionGeneration: 1,
		TurnID:            "same-turn",
		MessageSeqStart:   1,
		MessageSeqEnd:     2,
		CanonicalRevision: 7,
		RangeHash:         "same-range",
		Outcome:           "success",
		UsageJSON:         "{}",
		CreatedAt:         time.Now().UTC(),
	}
	materialized, err := store.MaterializeTurnCapture(ctx, first)
	require.NoError(t, err)

	replay := first
	replay.SessionGeneration = 1
	replayed, err := store.MaterializeTurnCapture(ctx, replay)
	require.NoError(t, err)
	require.Equal(t, materialized, replayed, "same receipt and generation must remain idempotent")

	next := first
	next.ID = "capture-generation-2"
	next.SessionGeneration = 2
	_, err = store.MaterializeTurnCapture(ctx, next)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity conflicts",
		"a new session incarnation must not silently reuse the old turn digest")

	digest, err := store.GetTurnDigestByTurn(ctx, first.SessionID, first.TurnID)
	require.NoError(t, err)
	require.Equal(t, materialized.DigestID, digest.ID)
	require.Equal(t, first.ID, digest.CaptureReceiptID)

	var digestCount, jobCount, generationCount int
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND turn_id = ?
`, store.ProjectKey(), first.SessionID, first.TurnID).Scan(&digestCount))
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_jobs
WHERE project_key = ? AND kind = 'phase1_extract/v2'
`, store.ProjectKey()).Scan(&jobCount))
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_turn_capture_generations
WHERE project_key = ? AND session_id = ? AND turn_id = ?
`, store.ProjectKey(), first.SessionID, first.TurnID).Scan(&generationCount))
	require.Equal(t, 1, digestCount)
	require.Equal(t, 1, jobCount)
	require.Equal(t, 1, generationCount,
		"the rejected incarnation must not leave an orphan generation binding")
}

func TestCommitTurnCaptureExtractionIsAtomicAndLeaseBound(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	capture := TurnCapture{
		ID: "capture_commit", ProjectKey: store.ProjectKey(),
		SessionID: "session-commit", TurnID: "turn-commit",
		MessageSeqStart: 1, MessageSeqEnd: 2, CanonicalRevision: 3,
		RangeHash: "range-commit", Outcome: "success", UsageJSON: "{}",
		CreatedAt: time.Now().UTC(),
	}
	materialized, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)
	claimAt := time.Now().UTC()
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "worker-commit", Kind: "phase1_extract/v2",
		JobKey: materialized.JobKey, ProjectKey: store.ProjectKey(),
		Lease: time.Minute, Now: claimAt,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	candidate := Candidate{
		ProjectKey: store.ProjectKey(), SessionID: capture.SessionID, TurnID: capture.TurnID,
		Kind: "decision", Text: "atomic turn output", Confidence: 0.9,
		SourceHash: capture.RangeHash, SourceRevision: capture.CanonicalRevision,
		SourceMessageIDs: []string{"message-2"},
	}
	_, err = store.CommitTurnCaptureExtractionWithDigestText(ctx, claimed[0].ID, "stale-worker", claimed[0].Attempt,
		capture, materialized.DigestID, "output-hash",
		TurnCaptureDigestText{UserText: "stale user text", AssistantText: "stale assistant text"},
		[]Candidate{candidate})
	require.Error(t, err)
	pendingDigest, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Empty(t, pendingDigest.UserText, "a stale owner must not update digest text")
	require.Empty(t, pendingDigest.AssistantText, "a stale owner must not update digest text")
	var entries int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_entries WHERE project_key = ?`, store.ProjectKey()).Scan(&entries))
	require.Zero(t, entries, "stale owner must not expose candidates")

	reclaimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "worker-commit", Kind: "phase1_extract/v2",
		JobKey: materialized.JobKey, ProjectKey: store.ProjectKey(),
		Lease: time.Minute, Now: claimAt.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.Equal(t, claimed[0].Attempt+1, reclaimed[0].Attempt)

	_, err = store.CommitTurnCaptureExtractionWithDigestText(ctx, claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt,
		capture, materialized.DigestID, "old-output-hash",
		TurnCaptureDigestText{UserText: "old user text", AssistantText: "old assistant text"},
		[]Candidate{candidate})
	require.Error(t, err, "the old claim must be fenced even when the worker id is reused")
	pendingDigest, err = store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Empty(t, pendingDigest.UserText, "the expired claim must not update digest text")
	require.Empty(t, pendingDigest.AssistantText, "the expired claim must not update digest text")
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_entries WHERE project_key = ?`, store.ProjectKey()).Scan(&entries))
	require.Zero(t, entries, "a fenced claim must not expose candidates")
	err = store.FailJobForWorkerAttempt(ctx, claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt,
		"late old-worker failure", claimAt.Add(3*time.Minute))
	require.Error(t, err, "a stale extraction failure must not clear the replacement lease")
	currentJob, err := store.getJobByKey(ctx, "phase1_extract/v2", ScopeProject, store.ProjectKey(), materialized.JobKey)
	require.NoError(t, err)
	require.Equal(t, JobStatusRunning, currentJob.Status)
	require.Equal(t, reclaimed[0].Attempt, currentJob.Attempt)

	digestText := TurnCaptureDigestText{
		UserText: "bounded user evidence", AssistantText: "bounded assistant evidence", TextTruncated: true,
	}
	inserted, err := store.CommitTurnCaptureExtractionWithDigestText(
		ctx, reclaimed[0].ID, reclaimed[0].WorkerID, reclaimed[0].Attempt,
		capture, materialized.DigestID, "output-hash", digestText, []Candidate{candidate})
	require.NoError(t, err)
	require.Equal(t, 1, inserted)
	replayed, err := store.CommitTurnCaptureExtractionWithDigestText(
		ctx, reclaimed[0].ID, reclaimed[0].WorkerID, reclaimed[0].Attempt,
		capture, materialized.DigestID, "output-hash", digestText, []Candidate{candidate})
	require.NoError(t, err, "a committed extraction replay should return its durable output receipt")
	require.Equal(t, inserted, replayed)
	digest, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Equal(t, TurnDigestDone, digest.Status)
	require.Equal(t, digestText.UserText, digest.UserText)
	require.Equal(t, digestText.AssistantText, digest.AssistantText)
	require.True(t, digest.TextTruncated)
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_entries WHERE project_key = ?`, store.ProjectKey()).Scan(&entries))
	require.Equal(t, 1, entries, "replaying the completed worker must not duplicate candidates")
	var outputCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_turn_output_receipts WHERE job_id = ?`, reclaimed[0].ID).Scan(&outputCount))
	require.Equal(t, 1, outputCount)
	var jobStatus string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT status FROM memory_jobs WHERE id = ?`, claimed[0].ID).Scan(&jobStatus))
	require.Equal(t, string(JobStatusSucceeded), jobStatus)
}
