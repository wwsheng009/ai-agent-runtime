package memorystore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMemoryJobClaimLeaseAndComplete(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	job, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind: "phase1_extract", JobKey: "session:s1:hash-a", MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	again, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind: "phase1_extract", JobKey: "session:s1:hash-a",
	})
	if err != nil {
		t.Fatalf("EnsureJob idempotent: %v", err)
	}
	if job.ID != again.ID {
		t.Fatalf("idempotent job ids differ: %s vs %s", job.ID, again.ID)
	}

	now := time.Now().UTC()
	claimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "w1", Kind: "phase1_extract", JobKey: "session:s1:hash-a",
		Lease: time.Second, Limit: 1, Now: now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimJobs = %d, %v", len(claimed), err)
	}
	if claimed[0].Status != JobStatusRunning || claimed[0].Attempt != 1 {
		t.Fatalf("unexpected claimed job: %+v", claimed[0])
	}
	// An active lease cannot be claimed twice.
	claimed, err = store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "w2", Kind: "phase1_extract", JobKey: "session:s1:hash-a",
		Lease: time.Second, Limit: 1, Now: now.Add(100 * time.Millisecond),
	})
	if err != nil || len(claimed) != 0 {
		t.Fatalf("active lease should not be reclaimed: %d, %v", len(claimed), err)
	}
	// After lease expiry another worker can take it over.
	claimed, err = store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "w2", Kind: "phase1_extract", JobKey: "session:s1:hash-a",
		Lease: time.Second, Limit: 1, Now: now.Add(2 * time.Second),
	})
	if err != nil || len(claimed) != 1 || claimed[0].Attempt != 2 {
		t.Fatalf("lease takeover = %+v, %v", claimed, err)
	}
	if err := store.CompleteJob(context.Background(), claimed[0].ID, "hash-a"); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	job, err = store.getJobByKey(context.Background(), "phase1_extract", ScopeProject, store.ProjectKey(), "session:s1:hash-a")
	if err != nil || job.Status != JobStatusSucceeded || job.LastSuccessWatermark != "hash-a" {
		t.Fatalf("completed job = %+v, %v", job, err)
	}
}

func TestMemoryJobRetryExhaustion(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	job, err := store.EnsureJob(context.Background(), MemoryJob{
		Kind: "phase1_extract", JobKey: "session:s2:hash", MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	now := time.Now().UTC()
	claimed, err := store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "w1", Kind: "phase1_extract", JobKey: job.JobKey, Limit: 1, Now: now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim: %d, %v", len(claimed), err)
	}
	if err := store.FailJob(context.Background(), job.ID, "invalid json", now.Add(time.Minute)); err != nil {
		t.Fatalf("FailJob 1: %v", err)
	}
	claimed, err = store.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "w2", Kind: "phase1_extract", JobKey: job.JobKey, Limit: 1, Now: now.Add(2 * time.Minute),
	})
	if err != nil || len(claimed) != 1 || claimed[0].Attempt != 2 {
		t.Fatalf("second claim: %+v, %v", claimed, err)
	}
	if err := store.FailJob(context.Background(), job.ID, "invalid json again", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("FailJob 2: %v", err)
	}
	job, err = store.getJobByKey(context.Background(), "phase1_extract", ScopeProject, store.ProjectKey(), job.JobKey)
	if err != nil || job.Status != JobStatusFailed {
		t.Fatalf("retry exhaustion = %+v, %v", job, err)
	}
}

func TestMemoryCandidateSourceAndStaleMarking(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	record, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use append-only memory writes.",
		Confidence: 0.9, SourceHash: "hash-a", SourceRevision: 1,
		SourceMessageIDs: []string{"msg-1"}, RevalidateAfter: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	if record.Status != StatusCandidate || record.SourceKind != "session_extract" {
		t.Fatalf("unexpected candidate: %+v", record)
	}
	changed, err := store.MarkSessionSourceChanged(context.Background(), "s1", store.ProjectKey(), "hash-b", 2)
	if err != nil || changed != 1 {
		t.Fatalf("MarkSessionSourceChanged = %d, %v", changed, err)
	}
	record, err = store.GetRecord(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if record.Freshness != FreshnessStale || record.StaleReason != "source_changed" {
		t.Fatalf("candidate not stale: %+v", record)
	}
}

func TestMemoryCandidateSuppressionAndDuplicateAreNotActive(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(AppendNoteOptions{Text: "never recreate this", Kind: "fact"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.DeleteRecord(context.Background(), note.ID); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	_, err = store.InsertCandidate(context.Background(), Candidate{
		SessionID: "s1", Kind: "fact", Text: "never recreate this", SourceHash: "h",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "suppressed") {
		t.Fatalf("suppressed candidate must be rejected, got %v", err)
	}
	page, err := store.ListRecords(context.Background(), RecordFilter{Status: StatusActive, Limit: 10})
	if err != nil || page.Count != 0 {
		t.Fatalf("active records after suppression = %d, %v", page.Count, err)
	}
}
