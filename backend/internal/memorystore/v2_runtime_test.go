package memorystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnqueueCoreRebuildCoalescesRunningAndRequeuesTerminalJob(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-job-project",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	first, err := store.EnqueueCoreRebuild(ctx, 99)
	require.NoError(t, err)
	require.Equal(t, "core_rebuild", first.Kind)
	require.Equal(t, "project:"+store.ProjectKey(), first.JobKey)
	require.Equal(t, JobPriorityCoreRebuild, first.Priority)
	require.Equal(t, 10, first.MaxAttempts, "enqueue must clamp the retry budget")

	second, err := store.EnqueueCoreRebuild(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, JobStatusPending, second.Status)
	require.Equal(t, 10, second.MaxAttempts, "a pending request keeps its existing retry budget")

	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "core-worker", Kind: "core_rebuild",
		JobKey: first.JobKey, ProjectKey: store.ProjectKey(),
		Limit: 1, Lease: time.Minute, Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, JobStatusRunning, claimed[0].Status)

	running, err := store.EnqueueCoreRebuild(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, first.ID, running.ID)
	require.Equal(t, JobStatusRunning, running.Status)
	require.Equal(t, claimed[0].WorkerID, running.WorkerID)
	require.Equal(t, claimed[0].Attempt, running.Attempt)

	require.NoError(t, store.CompleteJobForWorkerAttempt(
		ctx, claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt, "core-v1:test",
	))
	succeeded, err := store.GetJob(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusSucceeded, succeeded.Status)
	require.Equal(t, "core-v1:test", succeeded.LastSuccessWatermark)

	requeued, err := store.EnqueueCoreRebuild(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, first.ID, requeued.ID)
	require.Equal(t, JobStatusPending, requeued.Status)
	require.Equal(t, 0, requeued.Attempt)
	require.Equal(t, 4, requeued.MaxAttempts)
	require.Empty(t, requeued.WorkerID)
	require.Empty(t, requeued.LastSuccessWatermark)
}

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
	if err := store.CompleteJobForWorker(context.Background(), claimed[0].ID, "w1", "stale"); err == nil {
		t.Fatal("stale worker must not complete a reclaimed job")
	}
	if err := store.CompleteJobForWorker(context.Background(), claimed[0].ID, "w2", "hash-a"); err != nil {
		t.Fatalf("CompleteJobForWorker: %v", err)
	}
	job, err = store.getJobByKey(context.Background(), "phase1_extract", ScopeProject, store.ProjectKey(), "session:s1:hash-a")
	if err != nil || job.Status != JobStatusSucceeded || job.LastSuccessWatermark != "hash-a" {
		t.Fatalf("completed job = %+v, %v", job, err)
	}
}

func TestJobHeartbeatExtendsLiveLeaseAndRejectsStaleAttempt(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	job, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "session:heartbeat:hash", MaxAttempts: 3,
	})
	require.NoError(t, err)
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "worker-heartbeat", Kind: "phase1_extract/v2", JobKey: job.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1, Now: time.Now().UTC(),
		Lease: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	beforeLease := claimed[0].LeaseUntil

	require.NoError(t, store.HeartbeatJobForWorkerAttempt(
		ctx, claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt, 2*time.Second))
	refreshed, err := store.getJobByKey(ctx, job.Kind, ScopeProject, store.ProjectKey(), job.JobKey)
	require.NoError(t, err)
	require.True(t, refreshed.LeaseUntil.After(beforeLease))
	require.False(t, refreshed.HeartbeatAt.IsZero())
	require.Equal(t, JobStatusRunning, refreshed.Status)

	require.ErrorIs(t, store.HeartbeatJobForWorkerAttempt(
		ctx, claimed[0].ID, "stale-worker", claimed[0].Attempt, time.Second),
		ErrJobLeaseLost)
	require.ErrorIs(t, store.HeartbeatJobForWorkerAttempt(
		ctx, claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt+1, time.Second),
		ErrJobLeaseLost)

	expired := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `
		UPDATE memory_jobs SET lease_until = ?, updated_at = ?
		WHERE id = ? AND project_key = ?
	`, expired, expired, claimed[0].ID, store.ProjectKey())
	require.NoError(t, err)
	require.ErrorIs(t, store.HeartbeatJobForWorkerAttempt(
		ctx, claimed[0].ID, claimed[0].WorkerID, claimed[0].Attempt, time.Second),
		ErrJobLeaseLost)
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

func TestMemoryJobClaimIsProjectScopedAndBounded(t *testing.T) {
	sharedDB := filepath.Join(t.TempDir(), "shared-jobs.db")
	p1, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on"})
	if err != nil {
		t.Fatalf("p1 New: %v", err)
	}
	defer p1.Close()
	p2, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on"})
	if err != nil {
		t.Fatalf("p2 New: %v", err)
	}
	defer p2.Close()
	if _, err := p1.EnsureJob(context.Background(), MemoryJob{Kind: "phase1_extract", JobKey: "p1-job"}); err != nil {
		t.Fatalf("p1 EnsureJob: %v", err)
	}
	if _, err := p2.EnsureJob(context.Background(), MemoryJob{Kind: "phase1_extract", JobKey: "p2-job"}); err != nil {
		t.Fatalf("p2 EnsureJob: %v", err)
	}
	claimed, err := p1.ClaimJobs(context.Background(), ClaimJobOptions{
		WorkerID: "p1-worker", Limit: 1_000_000, Now: time.Now().UTC(),
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("p1 ClaimJobs = %+v, %v", claimed, err)
	}
	if claimed[0].ProjectKey != p1.ProjectKey() || claimed[0].JobKey != "p1-job" {
		t.Fatalf("p1 claimed foreign job: %+v", claimed[0])
	}
	p2Job, err := p2.getJobByKey(context.Background(), "phase1_extract", ScopeProject, p2.ProjectKey(), "p2-job")
	if err != nil || p2Job.Status != JobStatusPending || p2Job.WorkerID != "" {
		t.Fatalf("p2 job was touched: %+v, %v", p2Job, err)
	}
	jobs, err := p2.ListJobs(context.Background(), 1_000_000)
	if err != nil || len(jobs) != 1 || jobs[0].JobKey != "p2-job" {
		t.Fatalf("p2 ListJobs = %+v, %v", jobs, err)
	}
}

func TestMemoryJobGetJobIsProjectScopedAndValidatesID(t *testing.T) {
	ctx := context.Background()
	sharedDB := filepath.Join(t.TempDir(), "shared-jobs.db")
	projectA, err := New(Config{
		ProjectRoot: t.TempDir(), StorePath: sharedDB, ProjectKey: "get-job-project-a", Mode: "on",
	})
	if err != nil {
		t.Fatalf("project A New: %v", err)
	}
	defer projectA.Close()
	projectB, err := New(Config{
		ProjectRoot: t.TempDir(), StorePath: sharedDB, ProjectKey: "get-job-project-b", Mode: "on",
	})
	if err != nil {
		t.Fatalf("project B New: %v", err)
	}
	defer projectB.Close()

	created, err := projectA.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "project-a-job",
	})
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	got, err := projectA.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetJob in owning project: %v", err)
	}
	if got.ID != created.ID || got.ProjectKey != projectA.ProjectKey() ||
		got.JobKey != created.JobKey || got.Status != created.Status {
		t.Fatalf("GetJob = %+v, want created project A job %+v", got, created)
	}
	if _, err := projectB.GetJob(ctx, created.ID); err != sql.ErrNoRows {
		t.Fatalf("GetJob from foreign project error = %v, want sql.ErrNoRows", err)
	}
	if _, err := projectA.GetJob(ctx, "missing-job"); err != sql.ErrNoRows {
		t.Fatalf("GetJob missing id error = %v, want sql.ErrNoRows", err)
	}
	if _, err := projectA.GetJob(ctx, " "); err == nil {
		t.Fatal("GetJob with blank id succeeded, want validation error")
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

func TestCancelStaleBaseExtractionJobsKeepsWindowAndCurrentHash(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	jobs := []MemoryJob{
		{Kind: "phase1_extract", JobKey: "session:s1:hash-old"},
		{Kind: "phase1_extract", JobKey: "session:s1:hash-current"},
		{Kind: "phase1_extract", JobKey: "session:s1:window:1-20:hash-old"},
		{Kind: "phase1_extract", JobKey: "session:s1:byte-pages-v1:1-20:hash-old:0:page-old"},
	}
	for _, job := range jobs {
		job.ProjectKey = store.ProjectKey()
		if _, err := store.EnsureJob(ctx, job); err != nil {
			t.Fatalf("EnsureJob(%s): %v", job.JobKey, err)
		}
	}

	cancelled, err := store.CancelStaleBaseExtractionJobs(ctx, "s1", "hash-current", "superseded")
	if err != nil {
		t.Fatalf("CancelStaleBaseExtractionJobs: %v", err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", cancelled)
	}
	all, err := store.ListJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	statuses := make(map[string]JobStatus)
	for _, job := range all {
		statuses[job.JobKey] = job.Status
	}
	if statuses["session:s1:hash-old"] != JobStatusCancelled {
		t.Fatalf("old base job status = %s, want cancelled", statuses["session:s1:hash-old"])
	}
	if statuses["session:s1:hash-current"] != JobStatusPending {
		t.Fatalf("current base job status = %s, want pending", statuses["session:s1:hash-current"])
	}
	if statuses["session:s1:window:1-20:hash-old"] != JobStatusPending {
		t.Fatalf("window job must be preserved, status = %s", statuses["session:s1:window:1-20:hash-old"])
	}
	if statuses["session:s1:byte-pages-v1:1-20:hash-old:0:page-old"] != JobStatusPending {
		t.Fatalf("byte-page job must be preserved")
	}
}
