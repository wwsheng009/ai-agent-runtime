package memorystore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPurgeCapture(sessionID, receiptID, turnID string, generation int64) TurnCapture {
	return TurnCapture{
		ID:                receiptID,
		ProjectKey:        "project-session-purge",
		SessionID:         sessionID,
		SessionGeneration: generation,
		TurnID:            turnID,
		MessageSeqStart:   1,
		MessageSeqEnd:     2,
		CanonicalRevision: 1,
		RangeHash:         receiptID + "-range",
		Outcome:           "success",
		UsageJSON:         "{}",
		CreatedAt:         time.Now().UTC(),
	}
}

func TestPurgeSessionIsIdempotentAndAllowsNewIncarnation(t *testing.T) {
	ctx := context.Background()
	sharedDB := filepath.Join(t.TempDir(), "shared-memory.db")
	store, err := New(Config{
		Root:       t.TempDir(),
		StorePath:  sharedDB,
		Mode:       "on",
		ProjectKey: "project-session-purge",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	first := testPurgeCapture("session_purge", "receipt-purge-1", "turn-purge-1", 1)
	if _, err := store.MaterializeTurnCapture(ctx, first); err != nil {
		t.Fatalf("materialize first capture: %v", err)
	}
	if _, err := store.InsertCandidate(ctx, Candidate{
		ProjectKey:        store.ProjectKey(),
		SessionID:         first.SessionID,
		SessionGeneration: first.SessionGeneration,
		TurnID:            first.TurnID,
		Kind:              "decision",
		Text:              "purge this derived memory",
		Confidence:        0.9,
		SourceHash:        first.RangeHash,
		SourceRevision:    first.CanonicalRevision,
	}); err != nil {
		t.Fatalf("insert derived candidate: %v", err)
	}
	page, err := store.ListRecords(ctx, RecordFilter{Limit: 10, Status: StatusCandidate, SourceSessionID: first.SessionID})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list session candidate: records=%d err=%v", len(page.Items), err)
	}
	if err := store.createSummary(ctx, Summary{
		Revision: 1,
		Text:     "summary containing the purged session",
	}, []string{page.Items[0].ID}); err != nil {
		t.Fatalf("create session summary: %v", err)
	}
	// A shared memory database can contain more than one project. A matching
	// session ID in another project must not make its provenance rows
	// disappear during this project's purge.
	otherProject, err := New(Config{
		Root:       t.TempDir(),
		StorePath:  sharedDB,
		Mode:       "on",
		ProjectKey: store.ProjectKey() + "-other",
	})
	if err != nil {
		t.Fatalf("New other project store: %v", err)
	}
	defer otherProject.Close()
	foreign, err := otherProject.InsertCandidate(ctx, Candidate{
		SessionID:  first.SessionID,
		TurnID:     "turn-foreign",
		Kind:       "decision",
		Text:       "preserve provenance from another project",
		Confidence: 0.9,
		SourceHash: "foreign-range",
	})
	if err != nil {
		t.Fatalf("insert foreign-project candidate: %v", err)
	}
	for _, jobKey := range []string{
		"session:" + first.SessionID + ":legacy-turn",
		"session:" + first.SessionID + ":legacy-rollup",
		"session:session-purge:legacy-turn",
		"project:" + store.ProjectKey() + ":session:session-purge:turn:turn-other:revision:1",
	} {
		if _, err := store.EnsureJob(ctx, MemoryJob{
			Kind:       "legacy_session_job",
			Scope:      ScopeProject,
			ProjectKey: store.ProjectKey(),
			JobKey:     jobKey,
		}); err != nil {
			t.Fatalf("insert legacy session job %q: %v", jobKey, err)
		}
	}

	if _, err := store.PurgeSession(ctx, first.SessionID, first.SessionGeneration); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	if deleted, err := store.PurgeSession(ctx, first.SessionID, first.SessionGeneration); err != nil {
		t.Fatalf("idempotent purge: %v", err)
	} else if deleted != 0 {
		t.Fatalf("idempotent purge deleted %d rows, want 0", deleted)
	}

	var tombstoneGeneration int64
	if err := store.db.QueryRowContext(ctx, `
		SELECT generation
		FROM memory_session_purges
		WHERE project_key = ? AND session_id = ?
	`, store.ProjectKey(), first.SessionID).Scan(&tombstoneGeneration); err != nil {
		t.Fatalf("read purge tombstone: %v", err)
	}
	if tombstoneGeneration != first.SessionGeneration {
		t.Fatalf("tombstone generation=%d, want %d", tombstoneGeneration, first.SessionGeneration)
	}

	var entries, digests, jobs, legacyJobs, bindings int
	for _, query := range []struct {
		name string
		sql  string
	}{
		{"entries", `SELECT COUNT(1) FROM memory_entries WHERE project_key = ? AND source_session_id = ?`},
		{"digests", `SELECT COUNT(1) FROM memory_turn_digests WHERE project_key = ? AND session_id = ?`},
		{"jobs", `SELECT COUNT(1) FROM memory_jobs WHERE project_key = ?
			AND substr(job_key, 1, length(?)) = ?`},
		{"legacy_jobs", `SELECT COUNT(1) FROM memory_jobs WHERE project_key = ?
			AND substr(job_key, 1, length(?)) = ?`},
		{"bindings", `SELECT COUNT(1) FROM memory_turn_capture_generations WHERE project_key = ? AND session_id = ?`},
	} {
		var count int
		var args []interface{}
		if query.name == "jobs" {
			prefix := "project:" + store.ProjectKey() + ":session:" + first.SessionID + ":"
			args = []interface{}{store.ProjectKey(), prefix, prefix}
		} else if query.name == "legacy_jobs" {
			prefix := "session:" + first.SessionID + ":"
			args = []interface{}{store.ProjectKey(), prefix, prefix}
		} else {
			args = []interface{}{store.ProjectKey(), first.SessionID}
		}
		if err := store.db.QueryRowContext(ctx, query.sql, args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		switch query.name {
		case "entries":
			entries = count
		case "digests":
			digests = count
		case "jobs":
			jobs = count
		case "legacy_jobs":
			legacyJobs = count
		case "bindings":
			bindings = count
		}
	}
	// Generationless legacy jobs are retained as quarantined rows; the job
	// fence prevents them from being claimed or completed after purge.
	if entries != 0 || digests != 0 || jobs != 0 || legacyJobs != 2 || bindings != 0 {
		t.Fatalf("purge left session rows: entries=%d digests=%d jobs=%d legacy_jobs=%d bindings=%d",
			entries, digests, jobs, legacyJobs, bindings)
	}
	var staleSummaries int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_summaries
		WHERE project_key = ? AND freshness_status = 'stale'
	`, store.ProjectKey()).Scan(&staleSummaries); err != nil {
		t.Fatalf("count stale summaries: %v", err)
	}
	if staleSummaries != 1 {
		t.Fatalf("stale summaries after purge=%d, want 1", staleSummaries)
	}
	var rebuildJobs int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_jobs
		WHERE project_key = ? AND kind = 'summary_rebuild' AND status = 'pending'
	`, store.ProjectKey()).Scan(&rebuildJobs); err != nil {
		t.Fatalf("count summary rebuild jobs: %v", err)
	}
	if rebuildJobs != 1 {
		t.Fatalf("pending summary rebuild jobs after purge=%d, want 1", rebuildJobs)
	}
	var neighboringJobs int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_jobs
		WHERE project_key = ? AND job_key IN (?, ?)
	`, store.ProjectKey(),
		"session:session-purge:legacy-turn",
		"project:"+store.ProjectKey()+":session:session-purge:turn:turn-other:revision:1",
	).Scan(&neighboringJobs); err != nil {
		t.Fatalf("count neighboring session jobs: %v", err)
	}
	if neighboringJobs != 2 {
		t.Fatalf("purge removed neighboring session jobs: remaining=%d, want 2", neighboringJobs)
	}
	var foreignSources int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_sources WHERE memory_id = ?`, foreign.ID).Scan(&foreignSources); err != nil {
		t.Fatalf("count foreign-project sources: %v", err)
	}
	if foreignSources != 1 {
		t.Fatalf("foreign-project source rows after purge=%d, want 1", foreignSources)
	}

	if _, err := store.MaterializeTurnCapture(ctx, first); !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
		t.Fatalf("stale materialization error=%v, want ErrSessionPurgeGenerationMismatch", err)
	}

	next := testPurgeCapture(first.SessionID, "receipt-purge-2", "turn-purge-2", 2)
	nextMaterialized, err := store.MaterializeTurnCapture(ctx, next)
	if err != nil {
		t.Fatalf("new incarnation materialization: %v", err)
	}
	if deleted, err := store.PurgeSession(ctx, first.SessionID, first.SessionGeneration); err != nil {
		t.Fatalf("retry old incarnation purge after recreation: %v", err)
	} else if deleted != 0 {
		t.Fatalf("old incarnation retry deleted %d rows from the new incarnation", deleted)
	}
	var nextDigests, nextJobs int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_turn_digests
		WHERE project_key = ? AND session_id = ? AND session_generation = ?
	`, store.ProjectKey(), next.SessionID, next.SessionGeneration).Scan(&nextDigests); err != nil {
		t.Fatalf("count recreated incarnation digests: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_jobs
		WHERE project_key = ? AND session_id = ? AND session_generation = ?
		  AND capture_receipt_id = ?
	`, store.ProjectKey(), next.SessionID, next.SessionGeneration, next.ID).Scan(&nextJobs); err != nil {
		t.Fatalf("count recreated incarnation jobs: %v", err)
	}
	if nextDigests != 1 || nextJobs != 1 {
		t.Fatalf("retrying old purge damaged recreated incarnation: digests=%d jobs=%d job_key=%s",
			nextDigests, nextJobs, nextMaterialized.JobKey)
	}
	if _, err := store.PurgeSession(ctx, next.SessionID, next.SessionGeneration); err != nil {
		t.Fatalf("new incarnation purge: %v", err)
	}
	if _, err := store.PurgeSession(ctx, next.SessionID, first.SessionGeneration); !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
		t.Fatalf("older purge error=%v, want ErrSessionPurgeGenerationMismatch", err)
	}
}

func TestPurgeSessionFencesClaimedExtractionCommit(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "project-session-purge",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	capture := testPurgeCapture("session-worker-fence", "receipt-worker-fence", "turn-worker-fence", 1)
	materialized, err := store.MaterializeTurnCapture(ctx, capture)
	if err != nil {
		t.Fatalf("materialize capture: %v", err)
	}
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID:   "worker-session-fence",
		Kind:       "phase1_extract/v2",
		JobKey:     materialized.JobKey,
		ProjectKey: store.ProjectKey(),
		Limit:      1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim extraction job = %+v, err=%v", claimed, err)
	}

	if _, err := store.PurgeSession(ctx, capture.SessionID, capture.SessionGeneration); err != nil {
		t.Fatalf("purge session: %v", err)
	}
	claimedAfterPurge, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID:   "worker-after-purge",
		Kind:       "phase1_extract/v2",
		JobKey:     materialized.JobKey,
		ProjectKey: store.ProjectKey(),
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("claim after purge: %v", err)
	}
	if len(claimedAfterPurge) != 0 {
		t.Fatalf("purged extraction job was claimable")
	}

	candidate := Candidate{
		ProjectKey: store.ProjectKey(), SessionID: capture.SessionID,
		SessionGeneration: capture.SessionGeneration, TurnID: capture.TurnID,
		Kind: "decision", Text: "must not survive purge",
		Confidence: 0.9, SourceHash: capture.RangeHash,
		SourceRevision: capture.CanonicalRevision,
	}
	if _, err := store.InsertCandidate(ctx, candidate); !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
		t.Fatalf("candidate insert after purge error=%v, want ErrSessionPurgeGenerationMismatch", err)
	}
	_, err = store.CommitTurnCaptureExtraction(
		ctx,
		claimed[0].ID,
		claimed[0].WorkerID,
		claimed[0].Attempt,
		capture,
		materialized.DigestID,
		"stale-output",
		[]Candidate{candidate},
	)
	if !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
		t.Fatalf("stale extraction commit error=%v, want ErrSessionPurgeGenerationMismatch", err)
	}

	var entries int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_entries WHERE project_key = ? AND source_session_id = ?`,
		store.ProjectKey(), capture.SessionID).Scan(&entries); err != nil {
		t.Fatalf("count stale entries: %v", err)
	}
	if entries != 0 {
		t.Fatalf("stale extraction created %d entries", entries)
	}
	var receipts int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_turn_output_receipts
		WHERE project_key = ? AND capture_receipt_id = ?
	`, store.ProjectKey(), capture.ID).Scan(&receipts); err != nil {
		t.Fatalf("count stale output receipts: %v", err)
	}
	if receipts != 0 {
		t.Fatalf("stale extraction created %d output receipts", receipts)
	}
}

func TestCandidateWriterPurgeFenceAndGenerationIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "project-candidate-generation-fence",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	const sessionID = "session-candidate-generation-fence"
	if _, err := store.InsertCandidate(ctx, Candidate{
		ProjectKey: store.ProjectKey(), SessionID: sessionID, SessionGeneration: 1,
		TurnID: "turn-1", Kind: "decision", Text: "old generation candidate",
		Confidence: 0.9, SourceHash: "generation-1", SourceRevision: 1,
	}); err != nil {
		t.Fatalf("insert generation-one candidate: %v", err)
	}
	if _, err := store.PurgeSession(ctx, sessionID, 1); err != nil {
		t.Fatalf("purge generation one: %v", err)
	}

	// A writer that cannot identify its session incarnation must fail closed
	// once that session ID has a durable purge tombstone.
	_, err = store.InsertCandidate(ctx, Candidate{
		ProjectKey: store.ProjectKey(), SessionID: sessionID,
		TurnID: "turn-legacy", Kind: "decision", Text: "legacy candidate after purge",
		Confidence: 0.9, SourceHash: "legacy-after-purge", SourceRevision: 2,
	})
	if !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
		t.Fatalf("generationless candidate after purge error=%v, want generation mismatch", err)
	}

	// A recreated incarnation with the same session ID is allowed when its
	// generation is strictly newer than the tombstone.
	recreated, err := store.InsertCandidate(ctx, Candidate{
		ProjectKey: store.ProjectKey(), SessionID: sessionID, SessionGeneration: 2,
		TurnID: "turn-2", Kind: "decision", Text: "new generation candidate",
		Confidence: 0.9, SourceHash: "generation-2", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("insert generation-two candidate: %v", err)
	}
	if recreated.SourceSessionGeneration != 2 {
		t.Fatalf("recreated candidate generation=%d, want 2", recreated.SourceSessionGeneration)
	}

	// Content de-duplication must not revive a record from another
	// incarnation. Doing so would silently rewrite its provenance to the new
	// session generation without creating a generation-qualified record.
	const duplicateSession = "session-revive-generation"
	if _, err := store.InsertCandidate(ctx, Candidate{
		ProjectKey: store.ProjectKey(), SessionID: duplicateSession, SessionGeneration: 1,
		TurnID: "turn-old", Kind: "decision", Text: "generation-isolated duplicate",
		Confidence: 0.9, SourceHash: "duplicate-generation-1", SourceRevision: 1,
	}); err != nil {
		t.Fatalf("insert duplicate generation-one candidate: %v", err)
	}
	_, err = store.InsertCandidate(ctx, Candidate{
		ProjectKey: store.ProjectKey(), SessionID: duplicateSession, SessionGeneration: 2,
		TurnID: "turn-new", Kind: "decision", Text: "generation-isolated duplicate",
		Confidence: 0.9, SourceHash: "duplicate-generation-2", SourceRevision: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate memory content or id") {
		t.Fatalf("cross-generation duplicate candidate error=%v, want duplicate rejection", err)
	}
}

func TestCommitTurnCaptureExtractionRequiresGenerationBinding(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "project-missing-capture-binding",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	capture := testPurgeCapture("session-missing-binding", "receipt-missing-binding",
		"turn-missing-binding", 1)
	capture.ProjectKey = store.ProjectKey()
	materialized, err := store.MaterializeTurnCapture(ctx, capture)
	if err != nil {
		t.Fatalf("materialize capture: %v", err)
	}
	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID:   "worker-missing-binding",
		Kind:       "phase1_extract/v2",
		JobKey:     materialized.JobKey,
		ProjectKey: store.ProjectKey(),
		Limit:      1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim extraction job = %+v, err=%v", claimed, err)
	}
	if _, err := store.db.ExecContext(ctx, `
		DELETE FROM memory_turn_capture_generations
		WHERE project_key = ? AND capture_receipt_id = ?
	`, store.ProjectKey(), capture.ID); err != nil {
		t.Fatalf("remove generation binding: %v", err)
	}

	candidate := Candidate{
		ProjectKey: store.ProjectKey(), SessionID: capture.SessionID,
		SessionGeneration: capture.SessionGeneration, TurnID: capture.TurnID,
		Kind: "decision", Text: "must not be committed without a binding",
		Confidence: 0.9, SourceHash: capture.RangeHash,
		SourceRevision: capture.CanonicalRevision,
	}
	_, err = store.CommitTurnCaptureExtraction(ctx, claimed[0].ID, claimed[0].WorkerID,
		claimed[0].Attempt, capture, materialized.DigestID, "missing-binding-output",
		[]Candidate{candidate})
	if err == nil || !strings.Contains(err.Error(), "generation binding missing") {
		t.Fatalf("commit without generation binding error=%v, want missing-binding error", err)
	}

	var entries, receipts int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_entries
		WHERE project_key = ? AND source_session_id = ?
	`, store.ProjectKey(), capture.SessionID).Scan(&entries); err != nil {
		t.Fatalf("count candidate entries: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM memory_turn_output_receipts
		WHERE project_key = ? AND capture_receipt_id = ?
	`, store.ProjectKey(), capture.ID).Scan(&receipts); err != nil {
		t.Fatalf("count output receipts: %v", err)
	}
	if entries != 0 || receipts != 0 {
		t.Fatalf("unbound extraction left derived rows: entries=%d output_receipts=%d", entries, receipts)
	}
}

func TestPurgeSessionFencesLegacyJobsWithoutGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "project-legacy-fence",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	const sessionID = "legacy-fenced-session"
	if _, err := store.PurgeSessionWithGenerations(ctx, sessionID, 1, 1); err != nil {
		t.Fatalf("purge session: %v", err)
	}
	for _, job := range []MemoryJob{
		{Kind: "phase1_extract", JobKey: "session:" + sessionID + ":new-legacy-job"},
		{Kind: "phase1_extract/v2", JobKey: "session:" + sessionID + ":new-legacy-v2-job"},
		{
			Kind: "phase1_extract/v2", JobKey: "session:" + sessionID + ":repair-only-job",
			SessionID: sessionID, CaptureReceiptID: "stale-receipt", RepairOnly: true,
		},
	} {
		if _, err := store.EnsureJob(ctx, job); !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
			t.Fatalf("enqueue stale job kind=%s key=%s after purge = %v, want generation mismatch",
				job.Kind, job.JobKey, err)
		}
	}

	insertLegacy := func(id, suffix, status, worker string) {
		t.Helper()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, insertErr := store.db.ExecContext(ctx, `
INSERT INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, worker_id, attempt,
    max_attempts, created_at, updated_at
) VALUES (?, 'phase1_extract', ?, 'project', ?, ?, NULLIF(?, ''), 1, 3, ?, ?)
`, id, "session:"+sessionID+":"+suffix, store.ProjectKey(), status, worker, now, now)
		if insertErr != nil {
			t.Fatalf("insert legacy job %s: %v", id, insertErr)
		}
	}

	insertLegacy("legacy-claim", "claim", "pending", "")
	insertLegacy("legacy-complete", "complete", "running", "worker-complete")
	insertLegacy("legacy-fail", "fail", "running", "worker-fail")
	insertLegacy("legacy-fail-attempt", "fail-attempt", "running", "worker-fail-attempt")
	insertLegacy("legacy-unowned-complete", "unowned-complete", "running", "worker-unowned")
	insertLegacy("legacy-unowned-fail", "unowned-fail", "running", "worker-unowned")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, attempt,
    max_attempts, created_at, updated_at
) VALUES ('legacy-v2-claim', 'phase1_extract/v2', ?, 'project', ?, 'pending', 0, 3, ?, ?)
`, "session:"+sessionID+":v2-claim", store.ProjectKey(), now, now); err != nil {
		t.Fatalf("insert generationless v2 job: %v", err)
	}

	claimed, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "worker-claim", Kind: "phase1_extract", ProjectKey: store.ProjectKey(), Limit: 10,
	})
	if err != nil {
		t.Fatalf("claim legacy job: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claim after purge returned jobs: %+v", claimed)
	}
	claimJob, err := store.getJobByKey(ctx, "phase1_extract", ScopeProject, store.ProjectKey(),
		"session:"+sessionID+":claim")
	if err != nil || claimJob.Status != JobStatusCancelled {
		t.Fatalf("fenced legacy claim status=%q err=%v, want cancelled", claimJob.Status, err)
	}
	claimedV2, err := store.ClaimJobs(ctx, ClaimJobOptions{
		WorkerID: "worker-v2-claim", Kind: "phase1_extract/v2", ProjectKey: store.ProjectKey(), Limit: 10,
	})
	if err != nil || len(claimedV2) != 0 {
		t.Fatalf("claim generationless v2 job after purge = %+v, err=%v; want no jobs", claimedV2, err)
	}
	var v2Status string
	if err := store.db.QueryRowContext(ctx,
		`SELECT status FROM memory_jobs WHERE id = 'legacy-v2-claim'`).Scan(&v2Status); err != nil {
		t.Fatalf("read generationless v2 job: %v", err)
	}
	if v2Status != string(JobStatusCancelled) {
		t.Fatalf("generationless v2 job status=%q, want cancelled", v2Status)
	}

	for _, test := range []struct {
		name string
		run  func() error
	}{
		{
			name: "complete worker",
			run: func() error {
				return store.CompleteJobForWorker(ctx, "legacy-complete", "worker-complete", "watermark")
			},
		},
		{
			name: "fail worker",
			run: func() error {
				return store.FailJobForWorker(ctx, "legacy-fail", "worker-fail", "failure", time.Now().Add(time.Minute))
			},
		},
		{
			name: "fail worker attempt",
			run: func() error {
				return store.FailJobForWorkerAttempt(ctx, "legacy-fail-attempt", "worker-fail-attempt", 1,
					"failure", time.Now().Add(time.Minute))
			},
		},
		{
			name: "complete unowned",
			run: func() error {
				return store.CompleteJob(ctx, "legacy-unowned-complete", "watermark")
			},
		},
		{
			name: "fail unowned",
			run: func() error {
				return store.FailJob(ctx, "legacy-unowned-fail", "failure", time.Now().Add(time.Minute))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrSessionPurgeGenerationMismatch) {
				t.Fatalf("legacy operation error=%v, want generation mismatch", err)
			}
		})
	}

	if _, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase2_consolidate", JobKey: "project-maintenance-task",
	}); err != nil {
		t.Fatalf("unscoped project job should remain enqueueable: %v", err)
	}
}
