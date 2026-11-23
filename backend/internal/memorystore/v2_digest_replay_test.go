package memorystore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReplayDigestDryRunAndOrphanReplayAreIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "digest-replay"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	capture := replayTestCapture(store, "replay-receipt", 1)
	materialized, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)
	makeDigestReplayOrphan(t, store, materialized.DigestID, materialized.JobID)

	before := replayStateCounts(t, store, materialized.DigestID)
	preview, err := store.ReplayDigestDryRun(ctx, materialized.DigestID, capture)
	require.NoError(t, err)
	require.Equal(t, DigestReplayEligible, preview.Classification)
	require.True(t, preview.DryRun)
	require.Empty(t, preview.JobID)
	require.Equal(t, before, replayStateCounts(t, store, materialized.DigestID),
		"dry-run must not modify digest, jobs, or audit rows")

	replayed, err := store.ReplayDigestWithResult(ctx, materialized.DigestID, capture, "operator replay")
	require.NoError(t, err)
	require.Equal(t, DigestReplayApplied, replayed.Classification)
	require.False(t, replayed.DryRun)
	require.NotEmpty(t, replayed.JobID)

	again, err := store.ReplayDigestWithResult(ctx, materialized.DigestID, capture, "second replay")
	require.NoError(t, err)
	require.Equal(t, DigestReplayAlreadyQueued, again.Classification)
	require.Equal(t, replayed.JobID, again.JobID)

	var jobCount, auditCount int
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM memory_jobs WHERE project_key = ? AND job_key = ?
`, capture.ProjectKey, materialized.JobKey).Scan(&jobCount))
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM memory_job_operator_audit WHERE job_id = ?
`, replayed.JobID).Scan(&auditCount))
	require.Equal(t, 1, jobCount)
	require.Equal(t, 1, auditCount, "idempotent replay must not duplicate its audit")

	var status, linkedJob string
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT status, process_job_id FROM memory_turn_digests WHERE id = ?
`, materialized.DigestID).Scan(&status, &linkedJob))
	require.Equal(t, string(TurnDigestPending), status)
	require.Equal(t, replayed.JobID, linkedJob)
}

func TestReplayDigestDryRunClassifiesSafeNoWriteOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("already processed", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		capture := replayTestCapture(store, "processed-receipt", 1)
		materialized, err := store.MaterializeTurnCapture(ctx, capture)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `
UPDATE memory_turn_digests SET status = 'done' WHERE id = ?
`, materialized.DigestID)
		require.NoError(t, err)

		result, err := store.ReplayDigestDryRun(ctx, materialized.DigestID, capture)
		require.NoError(t, err)
		require.Equal(t, DigestReplayAlreadyProcessed, result.Classification)
		require.True(t, result.DryRun)
	})

	t.Run("missing capture receipt", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		digest, err := store.RecordTurnDigest(ctx, TurnDigest{
			ProjectKey: store.ProjectKey(), SessionID: "legacy-session",
			TurnID: "legacy-turn", SessionGeneration: 1, Status: TurnDigestPending,
		})
		require.NoError(t, err)

		result, err := store.ReplayDigestDryRun(ctx, digest.ID, TurnCapture{})
		require.NoError(t, err)
		require.Equal(t, DigestReplayMissingReceipt, result.Classification)
		require.True(t, result.DryRun)
	})

	t.Run("invalid receipt identity", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		capture := replayTestCapture(store, "identity-receipt", 1)
		materialized, err := store.MaterializeTurnCapture(ctx, capture)
		require.NoError(t, err)
		makeDigestReplayOrphan(t, store, materialized.DigestID, materialized.JobID)
		capture.ID = "different-receipt"

		result, err := store.ReplayDigestDryRun(ctx, materialized.DigestID, capture)
		require.NoError(t, err)
		require.Equal(t, DigestReplayInvalidIdentity, result.Classification)
		require.True(t, result.DryRun)
	})

	t.Run("stale generation fence", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		capture := replayTestCapture(store, "stale-receipt", 1)
		materialized, err := store.MaterializeTurnCapture(ctx, capture)
		require.NoError(t, err)
		makeDigestReplayOrphan(t, store, materialized.DigestID, materialized.JobID)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = store.db.ExecContext(ctx, `
INSERT INTO memory_session_purges(
    project_key, session_id, generation, state, reason, created_at, updated_at,
    fenced_session_generation, purge_generation
) VALUES (?, ?, 1, 'fenced', 'test', ?, ?, 1, 1)
`, capture.ProjectKey, capture.SessionID, now, now)
		require.NoError(t, err)

		result, err := store.ReplayDigestDryRun(ctx, materialized.DigestID, capture)
		require.NoError(t, err)
		require.Equal(t, DigestReplayStaleGeneration, result.Classification)
		require.True(t, result.DryRun)
	})

	t.Run("already queued", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		capture := replayTestCapture(store, "queued-receipt", 1)
		materialized, err := store.MaterializeTurnCapture(ctx, capture)
		require.NoError(t, err)

		result, err := store.ReplayDigestDryRun(ctx, materialized.DigestID, capture)
		require.NoError(t, err)
		require.Equal(t, DigestReplayAlreadyQueued, result.Classification)
		require.Equal(t, materialized.JobID, result.JobID)
	})
}

func TestReplayDigestRequiresMatchingMaintenanceLease(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "digest-replay-lease"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	capture := replayTestCapture(store, "lease-receipt", 1)
	materialized, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)
	makeDigestReplayOrphan(t, store, materialized.DigestID, materialized.JobID)

	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "owner", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	lease.FencingToken++
	ownedCtx := WithMaintenanceLease(ctx, lease)

	result, err := store.ReplayDigestDryRun(ownedCtx, materialized.DigestID, capture)
	require.NoError(t, err)
	require.Equal(t, DigestReplayBlockedMaintenance, result.Classification)
	require.True(t, result.DryRun)
	_, err = store.ReplayDigestWithResult(ownedCtx, materialized.DigestID, capture, "retry")
	require.ErrorIs(t, err, ErrMaintenanceLeaseLost)
}

func TestReplayDigestRejectsMissingGenerationBinding(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	capture := replayTestCapture(store, "binding-receipt", 1)
	materialized, err := store.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)
	makeDigestReplayOrphan(t, store, materialized.DigestID, materialized.JobID)
	_, err = store.db.ExecContext(ctx, `
DELETE FROM memory_turn_capture_generations
WHERE project_key = ? AND capture_receipt_id = ?
`, capture.ProjectKey, capture.ID)
	require.NoError(t, err)

	result, err := store.ReplayDigestDryRun(ctx, materialized.DigestID, capture)
	require.NoError(t, err)
	require.Equal(t, DigestReplayMissingReceipt, result.Classification)
	require.True(t, result.DryRun)
	_, err = store.ReplayDigestWithResult(ctx, materialized.DigestID, capture, "retry")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDigestReplayIneligible))
}

func replayTestCapture(store *Store, receiptID string, generation int64) TurnCapture {
	return TurnCapture{
		ID: receiptID, ProjectKey: store.ProjectKey(),
		SessionID: "session-" + receiptID, SessionGeneration: generation,
		TurnID: "turn-" + receiptID, MessageSeqStart: 1, MessageSeqEnd: 2,
		CanonicalRevision: 1, RangeHash: "range-" + receiptID,
		Outcome: "success", UsageJSON: "{}", CreatedAt: time.Now().UTC(),
	}
}

func makeDigestReplayOrphan(t *testing.T, store *Store, digestID, jobID string) {
	t.Helper()
	ctx := context.Background()
	_, err := store.db.ExecContext(ctx, `
DELETE FROM memory_jobs WHERE project_key = ? AND id = ?
`, store.ProjectKey(), jobID)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
UPDATE memory_turn_digests
SET status = 'failed', process_job_id = NULL, last_error = 'fixture failure'
WHERE project_key = ? AND id = ?
`, store.ProjectKey(), digestID)
	require.NoError(t, err)
}

func replayStateCounts(t *testing.T, store *Store, digestID string) [4]int {
	t.Helper()
	ctx := context.Background()
	var digestRows, jobs, audits int
	var digestStatus string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(status), '') FROM memory_turn_digests WHERE id = ?`,
		digestID).Scan(&digestRows, &digestStatus))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_jobs WHERE project_key = ?`, store.ProjectKey()).Scan(&jobs))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_job_operator_audit WHERE project_key = ?`, store.ProjectKey()).Scan(&audits))
	statusCode := 0
	if digestStatus == string(TurnDigestPending) {
		statusCode = 1
	} else if digestStatus == string(TurnDigestFailed) {
		statusCode = 2
	}
	return [4]int{digestRows, statusCode, jobs, audits}
}
