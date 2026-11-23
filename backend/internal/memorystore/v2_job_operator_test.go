package memorystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestOperatorRejectsSucceededRetry(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.EnsureJob(ctx, MemoryJob{Kind: "operator", JobKey: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE memory_jobs SET status='succeeded' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueJob(ctx, job.ID, "manual retry"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("RequeueJob succeeded job = %v, want ErrJobConflict", err)
	}
}

func TestOperatorRetryPendingRequiresRetryableMetadata(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.EnsureJob(ctx, MemoryJob{Kind: "operator", JobKey: "fresh-pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueJob(ctx, job.ID, "manual retry"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("RequeueJob fresh pending = %v, want ErrJobConflict", err)
	}
}

func TestOperatorCASFailureRollsBackWithoutAudit(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.EnsureJob(ctx, MemoryJob{Kind: "operator", JobKey: "cas-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE memory_jobs SET status='failed',last_error='worker failed' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER block_operator_job_update BEFORE UPDATE OF status ON memory_jobs BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueJob(ctx, job.ID, "manual retry"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("RequeueJob CAS failure = %v, want ErrJobConflict", err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM memory_jobs WHERE id=?`, job.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(JobStatusFailed) {
		t.Fatalf("status after failed CAS = %q, want failed", status)
	}
	var audits int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_job_operator_audit WHERE job_id=?`, job.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("audit rows after failed CAS = %d, want 0", audits)
	}
}

func TestOperatorDeadLetterAndRetryMigration(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.EnsureJob(ctx, MemoryJob{Kind: "operator", JobKey: "dead-letter"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE memory_jobs SET status='failed',last_error='worker failed' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeadLetterJob(ctx, job.ID, "operator decision"); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := store.db.QueryRowContext(ctx, `SELECT status,dead_letter_reason FROM memory_jobs WHERE id=?`, job.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != string(JobStatusDeadLetter) || reason != "operator decision" {
		t.Fatalf("dead-letter row = status %q reason %q", status, reason)
	}
	if err := store.RequeueJob(ctx, job.ID, "operator retry"); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status,COALESCE(dead_letter_reason,'') FROM memory_jobs WHERE id=?`, job.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != string(JobStatusPending) || reason != "" {
		t.Fatalf("retried row = status %q reason %q", status, reason)
	}
	var audits int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_job_operator_audit WHERE job_id=?`, job.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("audit rows after migration = %d, want 2", audits)
	}
}

func TestOperatorRetryRefreshesExpiredDeadline(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "expired-deadline",
		SessionID: "session-expired", SessionGeneration: 1, CaptureReceiptID: "receipt-expired",
		SourceStartSeq: 1, SourceEndSeq: 2, SourceRevision: 1, SourceHash: "source-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx,
		`UPDATE memory_jobs SET status='failed',last_error='deadline exceeded',deadline_at=? WHERE id=?`,
		expired, job.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.RequeueJob(ctx, job.ID, "operator approved retry"); err != nil {
		t.Fatalf("RequeueJob: %v", err)
	}
	retried, err := store.getJobByKey(ctx, job.Kind, job.Scope, store.ProjectKey(), job.JobKey)
	if err != nil {
		t.Fatalf("get retried job: %v", err)
	}
	if retried.Status != JobStatusPending || !retried.DeadlineAt.After(time.Now().UTC()) {
		t.Fatalf("retried job = status:%s deadline:%s, want pending with a fresh future deadline",
			retried.Status, retried.DeadlineAt)
	}
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "retry-worker", Kind: job.Kind, JobKey: job.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ClaimJobs after operator retry: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != job.ID {
		t.Fatalf("ClaimJobs after operator retry = %+v, want the requeued job", claimed)
	}
}

func TestSummarizeJobsClassifiesProjectBacklogWithoutDiagnostics(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	future := now.Add(time.Hour)

	jobs := []MemoryJob{
		{
			Kind: "capture", JobKey: "due-secret-job-key", Status: JobStatusPending,
			CreatedAt: old, AvailableAt: now.Add(-time.Minute),
		},
		{
			Kind: "capture", JobKey: "deferred", Status: JobStatusPending,
			CreatedAt: now.Add(-time.Hour), DeferredUntil: future,
		},
		{
			Kind: "rollup", JobKey: "leased", Status: JobStatusRunning,
			LeaseUntil: future,
		},
		{
			Kind: "capture", JobKey: "failed", Status: JobStatusFailed,
			Attempt: 1, MaxAttempts: 3, LastError: "provider-secret-diagnostic",
		},
		{
			Kind: "capture", JobKey: "dead-letter", Status: JobStatusDeadLetter,
			Attempt: 3, MaxAttempts: 3, DeadLetterAt: now, DeadLetterReason: "private reason",
		},
		{Kind: "rollup", JobKey: "succeeded", Status: JobStatusSucceeded},
	}
	for _, job := range jobs {
		if _, err := store.EnsureJob(ctx, job); err != nil {
			t.Fatalf("EnsureJob(%s): %v", job.JobKey, err)
		}
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE memory_jobs SET lease_until = ? WHERE project_key = ? AND job_key = ?`,
		future.Format(time.RFC3339Nano), store.ProjectKey(), "leased"); err != nil {
		t.Fatalf("set leased job fixture: %v", err)
	}

	summary, err := store.SummarizeJobs(ctx)
	if err != nil {
		t.Fatalf("SummarizeJobs: %v", err)
	}
	if summary.TotalJobs != len(jobs) {
		t.Fatalf("TotalJobs = %d, want %d", summary.TotalJobs, len(jobs))
	}
	if summary.PendingDueJobs != 1 || summary.PendingDeferredJobs != 1 {
		t.Fatalf("pending classification due=%d deferred=%d, want 1/1",
			summary.PendingDueJobs, summary.PendingDeferredJobs)
	}
	if summary.LeasedJobs != 1 || summary.RetryableJobs != 1 ||
		summary.DeadLetterJobs != 1 || summary.BlockedJobs != 2 {
		t.Fatalf("operator counts leased=%d retryable=%d dead-letter=%d blocked=%d",
			summary.LeasedJobs, summary.RetryableJobs, summary.DeadLetterJobs, summary.BlockedJobs)
	}
	if summary.OldestPendingAgeSeconds < int64((time.Hour).Seconds()) {
		t.Fatalf("OldestPendingAgeSeconds = %d, want at least one hour",
			summary.OldestPendingAgeSeconds)
	}
	if summary.ByKind.Counts["capture"] != 4 || summary.ByKind.Counts["rollup"] != 2 {
		t.Fatalf("ByKind = %#v", summary.ByKind.Counts)
	}
	if summary.ByStatus.Counts[string(JobStatusPending)] != 2 ||
		summary.ByStatus.Counts[string(JobStatusDeadLetter)] != 1 {
		t.Fatalf("ByStatus = %#v", summary.ByStatus.Counts)
	}
	if summary.ByPriority.Counts["0"] != len(jobs) {
		t.Fatalf("ByPriority = %#v, want all default-priority jobs grouped as 0", summary.ByPriority.Counts)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"due-secret-job-key", "provider-secret-diagnostic", "private reason"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSummarizeJobsIsProjectScopedAndBoundsGroups(t *testing.T) {
	root := t.TempDir()
	projectA, err := New(Config{Root: root, Mode: "on", ProjectKey: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer projectA.Close()
	projectB, err := New(Config{Root: root, Mode: "on", ProjectKey: "project-b"})
	if err != nil {
		t.Fatal(err)
	}
	defer projectB.Close()
	ctx := context.Background()

	const jobCount = maxJobOperatorSummaryGroups + 2
	for i := 0; i < jobCount; i++ {
		kind := fmt.Sprintf("kind-%02d", i)
		if _, err := projectA.EnsureJob(ctx, MemoryJob{Kind: kind, JobKey: "a-" + kind}); err != nil {
			t.Fatalf("EnsureJob project A %s: %v", kind, err)
		}
	}
	if _, err := projectB.EnsureJob(ctx, MemoryJob{Kind: "other-project-only", JobKey: "b-only"}); err != nil {
		t.Fatalf("EnsureJob project B: %v", err)
	}

	summary, err := projectA.SummarizeJobs(ctx)
	if err != nil {
		t.Fatalf("SummarizeJobs: %v", err)
	}
	if summary.TotalJobs != jobCount {
		t.Fatalf("TotalJobs = %d, want %d (another project must not be counted)", summary.TotalJobs, jobCount)
	}
	if len(summary.ByKind.Counts) != maxJobOperatorSummaryGroups ||
		summary.ByKind.OmittedGroups != 2 || summary.ByKind.OmittedJobs != 2 {
		t.Fatalf("ByKind bounds = groups:%d omitted-groups:%d omitted-jobs:%d",
			len(summary.ByKind.Counts), summary.ByKind.OmittedGroups, summary.ByKind.OmittedJobs)
	}
	if _, leaked := summary.ByKind.Counts["other-project-only"]; leaked {
		t.Fatalf("summary contains another project's group: %#v", summary.ByKind.Counts)
	}
}

func TestSummarizeJobsBoundsAndRedactsGroupLabels(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret := "sk-abcdefghijklmnop"
	kind := strings.Repeat("very-long-kind ", 20) + secret + "\nsecond-line\x1b[31m"
	if _, err := store.EnsureJob(context.Background(), MemoryJob{Kind: kind, JobKey: "label-test"}); err != nil {
		t.Fatal(err)
	}

	summary, err := store.SummarizeJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ByKind.Counts) != 1 {
		t.Fatalf("ByKind groups = %#v", summary.ByKind.Counts)
	}
	for label := range summary.ByKind.Counts {
		if utf8.RuneCountInString(label) > maxJobOperatorSummaryGroupKeyRunes {
			t.Fatalf("group label exceeds rune limit: %d", utf8.RuneCountInString(label))
		}
		for _, r := range label {
			if unicode.IsControl(r) {
				t.Fatalf("group label contains control character %U: %q", r, label)
			}
		}
		if strings.Contains(label, secret) {
			t.Fatalf("group label contains unsafe content: %q", label)
		}
	}
}
