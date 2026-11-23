package memorystore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsureJobAppliesDefaultPriorityAndAbsoluteDeadline(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	before := time.Now().UTC()
	job, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind:   "phase1_extract",
		JobKey: "default-priority-and-deadline",
	})
	require.NoError(t, err)
	after := time.Now().UTC()

	require.Equal(t, JobPriorityPhase1Extract, job.Priority)
	require.False(t, job.DeadlineAt.IsZero())
	require.GreaterOrEqual(t, job.DeadlineAt, before.Add(DefaultMemoryJobTimeout))
	require.LessOrEqual(t, job.DeadlineAt, after.Add(DefaultMemoryJobTimeout))

	explicitDeadline := time.Now().UTC().Add(2 * time.Hour)
	explicit, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind:       "phase2_consolidate",
		JobKey:     "explicit-priority-and-deadline",
		Priority:   17,
		DeadlineAt: explicitDeadline,
	})
	require.NoError(t, err)
	require.Equal(t, 17, explicit.Priority)
	require.WithinDuration(t, explicitDeadline, explicit.DeadlineAt, time.Millisecond)
}

func TestClaimReclaimsExpiredLeaseWhenAbsoluteDeadlineIsStillFuture(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	deadline := now.Add(10 * time.Minute)
	job, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind:       "phase1_extract",
		JobKey:     "lease-reclaim-before-deadline",
		DeadlineAt: deadline,
	})
	require.NoError(t, err)

	first, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "worker-a", Kind: job.Kind, JobKey: job.JobKey,
		Lease: time.Second, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "worker-b", Kind: job.Kind, JobKey: job.JobKey,
		Lease: time.Second, Now: now.Add(2 * time.Second),
	})
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, first[0].Attempt+1, second[0].Attempt)
	require.Equal(t, "worker-b", second[0].WorkerID)
	require.WithinDuration(t, deadline, second[0].DeadlineAt, time.Millisecond)
}

func TestExpiredJobDeadlineTransitionsToFailedOrDeadLetter(t *testing.T) {
	t.Run("failed while attempts remain", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })

		now := time.Now().UTC()
		job, err := store.EnsureJob(context.Background(), MemoryJob{
			Kind:        "phase1_extract",
			JobKey:      "deadline-failed",
			MaxAttempts: 3,
			DeadlineAt:  now.Add(time.Second),
		})
		require.NoError(t, err)
		claimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
			WorkerID: "deadline-worker", Kind: job.Kind, JobKey: job.JobKey,
			Lease: time.Second, Now: now,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		reclaimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
			WorkerID: "replacement-worker", Kind: job.Kind, JobKey: job.JobKey,
			Lease: time.Second, Now: now.Add(2 * time.Second),
		})
		require.NoError(t, err)
		require.Empty(t, reclaimed)

		current, err := store.getJobByKey(context.Background(), job.Kind, ScopeProject,
			store.ProjectKey(), job.JobKey)
		require.NoError(t, err)
		require.Equal(t, JobStatusFailed, current.Status)
		require.Equal(t, "job deadline exceeded", current.LastError)
		require.False(t, current.FinishedAt.IsZero())
		require.True(t, current.DeadLetterAt.IsZero())
	})

	t.Run("dead letter at max attempts", func(t *testing.T) {
		store, err := New(Config{Root: t.TempDir(), Mode: "on"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })

		now := time.Now().UTC()
		job, err := store.EnsureJob(context.Background(), MemoryJob{
			Kind:        "phase1_extract",
			JobKey:      "deadline-dead-letter",
			Attempt:     2,
			MaxAttempts: 2,
			DeadlineAt:  now.Add(time.Second),
		})
		require.NoError(t, err)
		claimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
			WorkerID: "deadline-worker", Kind: job.Kind, JobKey: job.JobKey,
			Lease: time.Second, Now: now,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		reclaimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
			WorkerID: "replacement-worker", Kind: job.Kind, JobKey: job.JobKey,
			Lease: time.Second, Now: now.Add(2 * time.Second),
		})
		require.NoError(t, err)
		require.Empty(t, reclaimed)

		current, err := store.getJobByKey(context.Background(), job.Kind, ScopeProject,
			store.ProjectKey(), job.JobKey)
		require.NoError(t, err)
		require.Equal(t, JobStatusDeadLetter, current.Status)
		require.Equal(t, "job deadline exceeded", current.LastError)
		require.Equal(t, "job deadline exceeded", current.DeadLetterReason)
		require.False(t, current.DeadLetterAt.IsZero())
	})
}

func TestHeartbeatRejectsExpiredAbsoluteDeadline(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	job, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind:       "phase1_extract",
		JobKey:     "heartbeat-after-deadline",
		DeadlineAt: now.Add(time.Minute),
	})
	require.NoError(t, err)
	claimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "deadline-worker", Kind: job.Kind, JobKey: job.JobKey,
		Lease: time.Minute, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = store.db.ExecContext(context.Background(),
		`UPDATE memory_jobs SET deadline_at = ? WHERE id = ?`,
		now.Add(-time.Second).Format(time.RFC3339Nano), claimed[0].ID)
	require.NoError(t, err)

	err = store.HeartbeatJobForWorkerAttempt(
		context.Background(), claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt, time.Minute)
	require.ErrorIs(t, err, ErrJobLeaseLost)
}

func TestClaimDoesNotStartExpiredPendingJobs(t *testing.T) {
	for _, test := range []struct {
		name        string
		attempt     int
		maxAttempts int
		wantStatus  JobStatus
	}{
		{name: "retryable", attempt: 0, maxAttempts: 3, wantStatus: JobStatusFailed},
		{name: "exhausted", attempt: 3, maxAttempts: 3, wantStatus: JobStatusDeadLetter},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := New(Config{Root: t.TempDir(), Mode: "on"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			now := time.Now().UTC()
			job, err := store.EnsureJob(context.Background(), MemoryJob{
				Kind: "phase1_extract", JobKey: "expired-pending",
				Attempt: test.attempt, MaxAttempts: test.maxAttempts,
				DeadlineAt: now.Add(-time.Second),
			})
			require.NoError(t, err)

			claimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
				WorkerID: "deadline-worker", Kind: job.Kind, JobKey: job.JobKey,
				Lease: time.Minute, Now: now,
			})
			require.NoError(t, err)
			require.Empty(t, claimed, "expired pending work must not start")

			current, err := store.getJobByKey(context.Background(), job.Kind, ScopeProject,
				store.ProjectKey(), job.JobKey)
			require.NoError(t, err)
			require.Equal(t, test.wantStatus, current.Status)
			require.Equal(t, "job deadline exceeded", current.LastError)
			if test.wantStatus == JobStatusDeadLetter {
				require.Equal(t, "job deadline exceeded", current.DeadLetterReason)
				require.False(t, current.DeadLetterAt.IsZero())
			} else {
				require.True(t, current.DeadLetterAt.IsZero())
			}
		})
	}
}

func TestClaimGrantsFreshDeadlineToHistoricalPendingJobs(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "claim-deadline-inference"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	futureCreatedAt := now.Add(-time.Minute)
	futureJob, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "missing-deadline-future",
		Status: JobStatusPending, CreatedAt: futureCreatedAt,
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx,
		`UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, futureJob.ID)
	require.NoError(t, err)

	expiredCreatedAt := now.Add(-DefaultMemoryJobTimeout - time.Minute)
	expiredJob, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "missing-deadline-expired",
		Status: JobStatusPending, CreatedAt: expiredCreatedAt,
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx,
		`UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, expiredJob.ID)
	require.NoError(t, err)

	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "inference-worker", Kind: "phase1_extract",
		Lease: time.Minute, Now: now, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 2,
		"historical rows without a persisted deadline must remain claimable")
	claimedByID := make(map[string]MemoryJob, len(claimed))
	for _, job := range claimed {
		claimedByID[job.ID] = job
	}
	for _, id := range []string{futureJob.ID, expiredJob.ID} {
		job, ok := claimedByID[id]
		require.True(t, ok, "job %s was not claimed", id)
		require.Equal(t, JobStatusRunning, job.Status)
		require.WithinDuration(t, now.Add(DefaultMemoryJobTimeout), job.DeadlineAt, time.Second)
	}

	for _, seeded := range []MemoryJob{futureJob, expiredJob} {
		persisted, getErr := store.getJobByKey(ctx, seeded.Kind, ScopeProject,
			store.ProjectKey(), seeded.JobKey)
		require.NoError(t, getErr)
		require.Equal(t, JobStatusRunning, persisted.Status)
		require.WithinDuration(t, now.Add(DefaultMemoryJobTimeout), persisted.DeadlineAt, time.Second)
		require.Zero(t, persisted.FinishedAt)
		require.Empty(t, persisted.LastError)
	}
}

func TestClaimGrantsDeadlineToMalformedHistoricalCreatedAt(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "claim-deadline-malformed"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	job, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "malformed-created-at",
		Status: JobStatusPending,
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
UPDATE memory_jobs SET created_at = 'not-a-timestamp', deadline_at = NULL WHERE id = ?
`, job.ID)
	require.NoError(t, err)

	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "malformed-worker", Kind: "phase1_extract",
		Lease: time.Minute, Now: now, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1, "a malformed historical timestamp must not strand a claimable row")
	require.WithinDuration(t, now.Add(DefaultMemoryJobTimeout), claimed[0].DeadlineAt, time.Second)

	current, err := store.getJobByKey(ctx, job.Kind, ScopeProject,
		store.ProjectKey(), job.JobKey)
	require.NoError(t, err)
	require.Equal(t, JobStatusRunning, current.Status)
	require.WithinDuration(t, now.Add(DefaultMemoryJobTimeout), current.DeadlineAt, time.Second)
}

func TestRepairMissingJobDeadlinesDryRunAndApply(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "deadline-repair"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	job, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "historical-no-deadline",
		Status: JobStatusPending, CreatedAt: createdAt,
		SessionID: "session-a", SessionGeneration: 1, CaptureReceiptID: "receipt-a",
		SourceStartSeq: 1, SourceEndSeq: 2, SourceRevision: 1, SourceHash: "range",
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, job.ID)
	require.NoError(t, err)

	plan, err := store.PlanMissingJobDeadlines(ctx, 20)
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.Equal(t, 1, plan.Scanned)
	require.Equal(t, 1, plan.Eligible)
	require.Zero(t, plan.Updated)
	require.Len(t, plan.Items, 1)
	require.Equal(t, createdAt.Add(DefaultMemoryJobTimeout), plan.Items[0].DeadlineAt)
	var deadline sql.NullString
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT deadline_at FROM memory_jobs WHERE id = ?`, job.ID).Scan(&deadline))
	require.False(t, deadline.Valid, "dry run must not write deadline")
	var auditCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_job_operator_audit WHERE job_id = ?`, job.ID).Scan(&auditCount))
	require.Zero(t, auditCount)

	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "deadline-repair-owner", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	ownedCtx := WithMaintenanceLease(ctx, lease)
	applied, err := store.RepairMissingJobDeadlines(ownedCtx, JobDeadlineRepairOptions{
		Limit: 20, Reason: "historical timeout backfill",
	})
	require.NoError(t, err)
	require.False(t, applied.DryRun)
	require.Equal(t, 1, applied.Updated)
	require.True(t, applied.Items[0].Applied)
	require.NoError(t, store.ReleaseMaintenanceLease(ctx, lease))

	repaired, err := store.getJobByKey(ctx, job.Kind, ScopeProject, store.ProjectKey(), job.JobKey)
	require.NoError(t, err)
	require.WithinDuration(t, createdAt.Add(DefaultMemoryJobTimeout), repaired.DeadlineAt, time.Millisecond)
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_job_operator_audit WHERE job_id = ?`, job.ID).Scan(&auditCount))
	require.Equal(t, 1, auditCount)

	summary, err := store.SummarizeJobs(ctx)
	require.NoError(t, err)
	require.Zero(t, summary.MissingDeadlineJobs)
}

func TestRepairMissingJobDeadlinesApplyRequiresLeaseAndRespectsProject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	projectA, err := New(Config{Root: root, Mode: "on", ProjectKey: "deadline-project-a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = projectA.Close() })
	projectB, err := New(Config{Root: root, Mode: "on", ProjectKey: "deadline-project-b"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = projectB.Close() })

	old := time.Now().UTC().Add(-time.Hour)
	jobA, err := projectA.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "missing-deadline-a",
		Status: JobStatusPending, CreatedAt: old,
	})
	require.NoError(t, err)
	jobB, err := projectB.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "missing-deadline-b",
		Status: JobStatusPending, CreatedAt: old,
	})
	require.NoError(t, err)
	_, err = projectA.db.ExecContext(ctx, `UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, jobA.ID)
	require.NoError(t, err)
	_, err = projectB.db.ExecContext(ctx, `UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, jobB.ID)
	require.NoError(t, err)

	_, err = projectA.RepairMissingJobDeadlines(ctx, JobDeadlineRepairOptions{})
	require.ErrorIs(t, err, ErrMaintenanceLeaseLost)
	lease, acquired, err := projectA.AcquireMaintenanceLease(ctx, "deadline-project-a-owner", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	applied, err := projectA.RepairMissingJobDeadlines(WithMaintenanceLease(ctx, lease),
		JobDeadlineRepairOptions{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, applied.Updated)
	require.NoError(t, projectA.ReleaseMaintenanceLease(ctx, lease))

	summaryA, err := projectA.SummarizeJobs(ctx)
	require.NoError(t, err)
	summaryB, err := projectB.SummarizeJobs(ctx)
	require.NoError(t, err)
	require.Zero(t, summaryA.MissingDeadlineJobs)
	require.Equal(t, 1, summaryB.MissingDeadlineJobs, "repair must stay within project scope")
}

func TestRepairMissingJobDeadlinesSkipsRunningAndPurgeFencedJobs(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "deadline-fence"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	running, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "running-without-deadline",
		DeadlineAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "active-worker", Kind: running.Kind, JobKey: running.JobKey,
		Lease: time.Minute, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = store.db.ExecContext(ctx,
		`UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, running.ID)
	require.NoError(t, err)

	fenced, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "session-fenced",
		Status: JobStatusPending, CreatedAt: now.Add(-time.Hour),
		SessionID: "session-fenced", SessionGeneration: 1, CaptureReceiptID: "receipt-fenced",
		SourceStartSeq: 1, SourceEndSeq: 2, SourceRevision: 1, SourceHash: "range-fenced",
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, fenced.ID)
	require.NoError(t, err)
	nowText := now.Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `
INSERT INTO memory_session_purges(
    project_key, session_id, generation, state, reason, created_at, updated_at,
    fenced_session_generation, purge_generation
) VALUES (?, ?, 1, 'fenced', 'test', ?, ?, 1, 1)
`, store.ProjectKey(), fenced.SessionID, nowText, nowText)
	require.NoError(t, err)

	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "deadline-fence-owner", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	result, err := store.RepairMissingJobDeadlines(WithMaintenanceLease(ctx, lease),
		JobDeadlineRepairOptions{Limit: 20})
	require.NoError(t, err)
	require.NoError(t, store.ReleaseMaintenanceLease(ctx, lease))

	require.Equal(t, 1, result.Scanned, "running jobs must not be repaired")
	require.Zero(t, result.Updated)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, "purge_fence", result.Items[0].Reason)
	runningAfter, err := store.getJobByKey(ctx, running.Kind, ScopeProject, store.ProjectKey(), running.JobKey)
	require.NoError(t, err)
	require.True(t, runningAfter.DeadlineAt.IsZero(), "active worker row must remain untouched")
}

func TestRepairMissingJobDeadlinesSkipsMalformedCreatedAt(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "deadline-invalid-created-at"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	job, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "invalid-created-at",
		Status: JobStatusPending,
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
UPDATE memory_jobs SET created_at = 'not-a-timestamp', deadline_at = NULL WHERE id = ?
`, job.ID)
	require.NoError(t, err)

	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "invalid-created-at-repair", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	result, err := store.RepairMissingJobDeadlines(WithMaintenanceLease(ctx, lease),
		JobDeadlineRepairOptions{Limit: 10})
	require.NoError(t, err)
	require.NoError(t, store.ReleaseMaintenanceLease(ctx, lease))

	require.Equal(t, 1, result.Scanned)
	require.Zero(t, result.Eligible)
	require.Zero(t, result.Updated)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, "invalid_created_at", result.Items[0].Reason)
	current, err := store.getJobByKey(ctx, job.Kind, ScopeProject, store.ProjectKey(), job.JobKey)
	require.NoError(t, err)
	require.True(t, current.DeadlineAt.IsZero(), "repair must not invent a deadline")
	require.Equal(t, JobStatusPending, current.Status)
}
