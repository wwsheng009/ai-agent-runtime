package memorystore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreStatusExtractionBacklog(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	addJob := func(job MemoryJob) MemoryJob {
		t.Helper()
		created, err := store.EnsureJob(ctx, job)
		if err != nil {
			t.Fatalf("EnsureJob(%s): %v", job.JobKey, err)
		}
		return created
	}
	pendingLegacy := addJob(MemoryJob{
		Kind: "phase1_extract", JobKey: "legacy-pending", SessionID: "session-pending",
		Status: JobStatusPending, Priority: 12, Attempt: 2,
		CreatedAt: now.Add(-5 * time.Minute), AvailableAt: now.Add(-time.Minute),
		RetryAt: now.Add(time.Minute), LastError: "temporary provider error",
	})
	pendingV2 := addJob(MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "v2-pending", SessionID: "session-pending",
		SessionGeneration: 1, CaptureReceiptID: "receipt-v2-pending",
		SourceStartSeq: 1, SourceEndSeq: 2, SourceRevision: 1, SourceHash: "hash-v2-pending",
		Status: JobStatusPending, Priority: 20, CreatedAt: now.Add(-2 * time.Minute),
		AvailableAt: now.Add(-time.Minute),
	})
	leased := addJob(MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "v2-leased", SessionID: "session-leased",
		SessionGeneration: 1, CaptureReceiptID: "receipt-v2-leased",
		SourceStartSeq: 1, SourceEndSeq: 2, SourceRevision: 1, SourceHash: "hash-v2-leased",
		Status: JobStatusRunning, Priority: 30, Attempt: 1, CreatedAt: now.Add(-3 * time.Minute),
	})
	addJob(MemoryJob{
		Kind: "phase1_extract", JobKey: "legacy-failed", SessionID: "session-failed",
		Status: JobStatusFailed, Attempt: 3, CreatedAt: now.Add(-4 * time.Minute),
		LastError: "provider unavailable",
	})
	deadLetter := addJob(MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "v2-dead-letter", SessionID: "session-dead",
		SessionGeneration: 1, CaptureReceiptID: "receipt-v2-dead-letter",
		SourceStartSeq: 1, SourceEndSeq: 2, SourceRevision: 1, SourceHash: "hash-v2-dead-letter",
		Status: JobStatusFailed, Attempt: 3, CreatedAt: now.Add(-6 * time.Minute),
		DeadLetterAt: now.Add(-time.Minute), DeadLetterReason: "source permanently unavailable",
	})
	succeeded := addJob(MemoryJob{
		Kind: "phase1_extract", JobKey: "legacy-succeeded", SessionID: "session-done",
		Status: JobStatusSucceeded, Attempt: 1, CreatedAt: now.Add(-10 * time.Minute),
	})

	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	finishedAt := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	leaseUntil := now.Add(time.Minute).Format(time.RFC3339Nano)
	deadlineAt := now.Add(2 * time.Minute).Format(time.RFC3339Nano)
	for _, update := range []struct {
		id     string
		query  string
		values []interface{}
	}{
		{leased.ID, `UPDATE memory_jobs SET lease_until = ?, deadline_at = ? WHERE id = ?`, []interface{}{leaseUntil, deadlineAt, leased.ID}},
		{succeeded.ID, `UPDATE memory_jobs SET finished_at = ? WHERE id = ?`, []interface{}{finishedAt, succeeded.ID}},
		{deadLetter.ID, `UPDATE memory_jobs SET dead_letter_at = ?, dead_letter_reason = ? WHERE id = ?`, []interface{}{now.Add(-time.Minute).Format(time.RFC3339Nano), "source permanently unavailable", deadLetter.ID}},
		{pendingV2.ID, `UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, []interface{}{pendingV2.ID}},
	} {
		if _, err := db.ExecContext(ctx, update.query, update.values...); err != nil {
			t.Fatalf("update job %s: %v", update.id, err)
		}
	}
	report, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.SearchBackend != "like_fallback" {
		t.Fatalf("SearchBackend = %q, want explicit LIKE fallback", report.SearchBackend)
	}
	if report.PendingExtractionJobs != 2 || report.PendingExtractionSessions != 1 {
		t.Fatalf("pending extraction counts = jobs:%d sessions:%d, want 2/1",
			report.PendingExtractionJobs, report.PendingExtractionSessions)
	}
	if report.MissingDeadlineJobs != 1 {
		t.Fatalf("missing deadline jobs = %d, want 1", report.MissingDeadlineJobs)
	}
	if report.OldestPendingJobAgeSeconds < 298 || report.OldestPendingJobAgeSeconds > 302 {
		t.Fatalf("oldest pending age = %d seconds, want about 300", report.OldestPendingJobAgeSeconds)
	}
	if report.DeadLetterJobs != 1 || report.ExtractionJobCounts["dead_letter"] != 1 {
		t.Fatalf("dead-letter counts = report:%d buckets:%v, want 1",
			report.DeadLetterJobs, report.ExtractionJobCounts)
	}
	for bucket, want := range map[string]int{
		"pending": 2, "retry": 1, "leased": 1, "failed": 1, "succeeded": 1,
	} {
		if got := report.ExtractionJobCounts[bucket]; got != want {
			t.Errorf("ExtractionJobCounts[%q] = %d, want %d; all=%v",
				bucket, got, want, report.ExtractionJobCounts)
		}
	}
	if got := report.LastExtractionAt; got != finishedAt {
		t.Fatalf("LastExtractionAt = %q, want %q", got, finishedAt)
	}
	if got := report.JobsByKind["phase1_extract"]["pending"]; got != 1 {
		t.Errorf("legacy kind pending count = %d, want 1; jobs_by_kind=%v", got, report.JobsByKind)
	}
	if got := report.JobsByKind["phase1_extract/v2"]["pending"]; got != 1 {
		t.Errorf("v2 kind pending count = %d, want 1; jobs_by_kind=%v", got, report.JobsByKind)
	}
	if got := report.JobsByKind["phase1_extract/v2"]["running"]; got != 1 {
		t.Errorf("v2 kind running count = %d, want 1; jobs_by_kind=%v", got, report.JobsByKind)
	}
	if got := report.Jobs["failed"]; got != 2 {
		t.Errorf("generic failed count = %d, want 2 including dead-letter row", got)
	}
	if got := report.Jobs["pending"]; got != 2 {
		t.Errorf("generic pending count = %d, want 2", got)
	}
	if len(report.ExtractionJobs) != 6 {
		t.Fatalf("recent extraction jobs = %d, want 6", len(report.ExtractionJobs))
	}
	var sawLegacy, sawV2, sawLeaseDetails, sawDeadLetterDetails bool
	for _, job := range report.ExtractionJobs {
		switch job.Kind {
		case "phase1_extract":
			sawLegacy = true
		case "phase1_extract/v2":
			sawV2 = true
		}
		if job.ID == leased.ID {
			sawLeaseDetails = job.Priority == 30 && job.TimeoutAt == deadlineAt && job.LeaseUntil == leaseUntil
		}
		if job.ID == deadLetter.ID {
			sawDeadLetterDetails = job.DeadLetterAt != "" && job.DeadLetterReason == "source permanently unavailable"
		}
		if job.ID == pendingLegacy.ID &&
			(job.JobKey != pendingLegacy.JobKey || job.SessionID != "session-pending" || job.CreatedAt == "") {
			t.Errorf("pending diagnostic missing additive identity fields: %+v", job)
		}
	}
	if !sawLegacy || !sawV2 || !sawLeaseDetails || !sawDeadLetterDetails {
		t.Fatalf("recent diagnostics omitted kind/lease/dead-letter data: legacy=%t v2=%t lease=%t dead_letter=%t; jobs=%+v",
			sawLegacy, sawV2, sawLeaseDetails, sawDeadLetterDetails, report.ExtractionJobs)
	}
}

func TestStoreStatusOffKeepsLegacyFallbackWithoutCreatingDB(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "off"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	if _, err := store.Append(AppendNoteOptions{Text: "legacy status note"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	report, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !report.Fallback || report.FallbackReason != "mode_off" ||
		report.SearchBackend != "legacy_jsonl" || report.Total != 1 {
		t.Fatalf("Mode=off status changed legacy fallback contract: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(root, "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("Mode=off Status created v2 DB, stat err=%v", err)
	}
}
