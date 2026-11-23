package memorystore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceLeaseSerializesOwnersAndFencesTakeover(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "project-lease"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	first, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-a", "test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire = %+v, acquired=%v, err=%v", first, acquired, err)
	}
	second, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-b", "test", time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if acquired {
		t.Fatalf("live lease was unexpectedly taken over: %+v", second)
	}
	if second.OwnerID != first.OwnerID || second.FencingToken != first.FencingToken {
		t.Fatalf("losing owner observed wrong lease: %+v vs %+v", second, first)
	}

	if err := store.ReleaseMaintenanceLease(ctx, first); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	replacement, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-b", "test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("takeover = %+v, acquired=%v, err=%v", replacement, acquired, err)
	}
	if replacement.FencingToken <= first.FencingToken {
		t.Fatalf("takeover did not advance fence: old=%d new=%d", first.FencingToken, replacement.FencingToken)
	}
	if err := store.CheckMaintenanceLease(ctx, first); !errors.Is(err, ErrMaintenanceLeaseLost) {
		t.Fatalf("old owner check = %v, want ErrMaintenanceLeaseLost", err)
	}
	if _, err := store.RenewMaintenanceLease(ctx, first, time.Minute); !errors.Is(err, ErrMaintenanceLeaseLost) {
		t.Fatalf("old owner renew = %v, want ErrMaintenanceLeaseLost", err)
	}
	if err := store.CheckMaintenanceLease(ctx, replacement); err != nil {
		t.Fatalf("replacement owner check: %v", err)
	}
}

func TestConcurrentMaintenanceLeaseAcquiresHaveSingleWinner(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Mode: "on", ProjectKey: "project-contended"}
	firstStore, err := New(cfg)
	if err != nil {
		t.Fatalf("New first store: %v", err)
	}
	defer firstStore.Close()
	secondStore, err := New(cfg)
	if err != nil {
		t.Fatalf("New second store: %v", err)
	}
	defer secondStore.Close()

	type outcome struct {
		owner    string
		acquired bool
		err      error
		lease    MaintenanceLease
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, item := range []struct {
		owner string
		store *Store
	}{
		{owner: "concurrent-a", store: firstStore},
		{owner: "concurrent-b", store: secondStore},
	} {
		go func(owner string, store *Store) {
			<-start
			lease, acquired, err := store.AcquireMaintenanceLease(context.Background(), owner, "test", time.Minute)
			results <- outcome{owner: owner, acquired: acquired, err: err, lease: lease}
		}(item.owner, item.store)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent acquire errors: first=%T(%#v) second=%T(%#v)",
			first.err, first.err, second.err, second.err)
	}
	if first.acquired == second.acquired {
		t.Fatalf("expected exactly one owner, got %+v / %+v", first, second)
	}
	winner, loser := first, second
	if !winner.acquired {
		winner, loser = second, first
	}
	if loser.lease.OwnerID != winner.owner || loser.lease.FencingToken != winner.lease.FencingToken {
		t.Fatalf("loser observed inconsistent owner row: winner=%+v loser=%+v", winner, loser)
	}
}

func TestMaintenanceLeaseContextFencesJobClaim(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "project-fence"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-a", "test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %+v, acquired=%v, err=%v", lease, acquired, err)
	}
	job, err := store.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "capture:fenced", MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("ensure job: %v", err)
	}

	ownedCtx := WithMaintenanceLease(ctx, lease)
	claimed, err := store.ClaimJobs(ownedCtx, ClaimJobOptions{
		WorkerID: "worker-a", Kind: job.Kind, JobKey: job.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("fenced claim = %+v, err=%v", claimed, err)
	}
	if err := store.ReleaseMaintenanceLease(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := store.EnsureJob(ownedCtx, MemoryJob{
		Kind: "phase1_extract/v2", JobKey: "capture:stale-submit", MaxAttempts: 3,
	}); !errors.Is(err, ErrMaintenanceLeaseLost) {
		t.Fatalf("stale fenced job submission = %v, want ErrMaintenanceLeaseLost", err)
	}
	if err := store.CompleteJobForWorker(ownedCtx, claimed[0].ID, claimed[0].WorkerID, "stale"); !errors.Is(err, ErrMaintenanceLeaseLost) {
		t.Fatalf("stale fenced completion = %v, want ErrMaintenanceLeaseLost", err)
	}
	if _, err := store.ClaimJobs(ownedCtx, ClaimJobOptions{
		WorkerID: "worker-a", Kind: job.Kind, JobKey: job.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1,
	}); !errors.Is(err, ErrMaintenanceLeaseLost) {
		t.Fatalf("stale fenced claim = %v, want ErrMaintenanceLeaseLost", err)
	}
}

func TestMaintenanceLeaseContextFencesCaptureOutputCommit(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "project-output-fence"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-a", "test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %+v, acquired=%v, err=%v", lease, acquired, err)
	}
	ownedCtx := WithMaintenanceLease(ctx, lease)
	capture := TurnCapture{
		ID: "receipt-fenced", ProjectKey: store.ProjectKey(),
		SessionID: "session-fenced", TurnID: "turn-fenced",
		MessageSeqStart: 1, MessageSeqEnd: 1, CanonicalRevision: 1,
		RangeHash: "range-fenced", Outcome: "success", UsageJSON: "{}",
		CreatedAt: time.Now().UTC(),
	}
	materialized, err := store.MaterializeTurnCapture(ownedCtx, capture)
	if err != nil {
		t.Fatalf("materialize capture: %v", err)
	}
	claimed, err := store.ClaimJobs(ownedCtx, ClaimJobOptions{
		WorkerID: "worker-fenced", Kind: "phase1_extract/v2", JobKey: materialized.JobKey,
		ProjectKey: store.ProjectKey(), Limit: 1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim capture job = %+v, err=%v", claimed, err)
	}
	if err := store.ReleaseMaintenanceLease(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-b", "test", time.Minute); err != nil || !acquired {
		t.Fatalf("takeover acquired=%v, err=%v", acquired, err)
	}

	candidate := Candidate{
		ProjectKey: store.ProjectKey(), SessionID: capture.SessionID, TurnID: capture.TurnID,
		Kind: "decision", Text: "must not commit after owner takeover", Confidence: 0.9,
		SourceHash: capture.RangeHash, SourceRevision: capture.CanonicalRevision,
		SourceMessageIDs: []string{"message-1"},
	}
	_, err = store.CommitTurnCaptureExtraction(ownedCtx, claimed[0].ID, claimed[0].WorkerID,
		claimed[0].Attempt, capture, materialized.DigestID, "output-fenced", []Candidate{candidate})
	if !errors.Is(err, ErrMaintenanceLeaseLost) {
		t.Fatalf("stale owner output commit = %v, want ErrMaintenanceLeaseLost", err)
	}
	var candidates int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM memory_entries WHERE project_key = ?`, store.ProjectKey()).Scan(&candidates); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if candidates != 0 {
		t.Fatalf("stale owner committed %d candidates", candidates)
	}
}

func TestMaintenanceLeaseContextFencesRetentionAndFreshnessWrites(t *testing.T) {
	store, err := New(Config{
		Root:                     t.TempDir(),
		Mode:                     "on",
		ProjectKey:               "project-retention-fence",
		RecallTraceRetentionDays: 1,
		ExposureRetentionDays:    1,
		Retention:                RetentionPolicy{Enabled: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-a", "test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %+v, acquired=%v, err=%v", lease, acquired, err)
	}
	ownedCtx := WithMaintenanceLease(ctx, lease)
	if err := store.ReleaseMaintenanceLease(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "freshness sweep",
			run: func() error {
				_, err := store.SweepFreshnessBatch(ownedCtx, time.Now().UTC(), 1)
				return err
			},
		},
		{
			name: "diagnostics prune",
			run: func() error {
				_, err := store.PruneDiagnostics(ownedCtx, time.Now().UTC())
				return err
			},
		},
		{
			name: "lifecycle sweep",
			run: func() error {
				_, err := store.SweepLifecycle(ownedCtx, time.Now().UTC())
				return err
			},
		},
		{
			name: "turn digest prune",
			run: func() error {
				_, err := store.PruneTurnDigests(ownedCtx, time.Now().UTC())
				return err
			},
		},
		{
			name: "turn window prune",
			run: func() error {
				_, err := store.PruneTurnWindows(ownedCtx, time.Now().UTC())
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrMaintenanceLeaseLost) {
				t.Fatalf("stale maintenance write = %v, want ErrMaintenanceLeaseLost", err)
			}
		})
	}
}

func TestMaintenanceLeaseContextFencesConsolidationAndTurnStateWrites(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "project-consolidation-fence"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	candidate := func(text string) Record {
		t.Helper()
		record, err := store.InsertCandidate(ctx, Candidate{
			ProjectKey: store.ProjectKey(), SessionID: "session-fence", TurnID: "turn-" + text,
			Kind: "fact", Text: text, Confidence: 0.95, Sensitivity: "internal",
		})
		if err != nil {
			t.Fatalf("insert candidate %q: %v", text, err)
		}
		return record
	}
	promote := func(record Record) Record {
		t.Helper()
		active, err := store.PromoteCandidate(ctx, record.ID)
		if err != nil {
			t.Fatalf("promote candidate %q: %v", record.ID, err)
		}
		return active
	}
	loser := promote(candidate("fenced loser content"))
	winner := promote(candidate("fenced winner content"))
	pending := candidate("must remain candidate content")
	baselineSummary, err := store.CompileSummary(ctx, Summary{Text: "stable summary for fence"}, []string{winner.ID})
	if err != nil {
		t.Fatalf("compile baseline summary: %v", err)
	}
	digest, err := store.RecordTurnDigest(ctx, TurnDigest{
		ProjectKey: store.ProjectKey(), SessionID: "session-fence-digest", TurnID: "turn-fence",
		MessageSeqEnd: 1, UserMessageID: "u1", AssistantMessageID: "a1",
	})
	if err != nil {
		t.Fatalf("record baseline digest: %v", err)
	}
	window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "session-fence-digest",
		WindowIndex: 1, StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
		TurnCount: 1, SourceHash: "source-hash",
	})
	if err != nil {
		t.Fatalf("ensure baseline window: %v", err)
	}

	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "owner-stale", "test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %+v, acquired=%v, err=%v", lease, acquired, err)
	}
	ownedCtx := WithMaintenanceLease(ctx, lease)
	if err := store.ReleaseMaintenanceLease(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "promote candidate",
			run: func() error {
				_, err := store.PromoteCandidate(ownedCtx, pending.ID)
				return err
			},
		},
		{
			name: "supersede record",
			run: func() error {
				_, err := store.SupersedeRecord(ownedCtx, loser.ID, winner.ID, "test")
				return err
			},
		},
		{
			name: "mark conflict open question",
			run: func() error {
				return store.MarkConflictOpenQuestion(ownedCtx, []string{loser.ID, winner.ID}, "test")
			},
		},
		{
			name: "compile summary",
			run: func() error {
				_, err := store.CompileSummary(ownedCtx, Summary{Text: "stale owner summary"}, []string{winner.ID})
				return err
			},
		},
		{
			name: "activate summary",
			run: func() error {
				_, err := store.ActivateSummary(ownedCtx, baselineSummary.ID)
				return err
			},
		},
		{
			name: "publish summary artifact",
			run: func() error {
				return store.writeSummaryArtifactsIfCurrent(ownedCtx, baselineSummary)
			},
		},
		{
			name: "retire summary",
			run: func() error {
				_, err := store.RetireActiveSummary(ownedCtx)
				return err
			},
		},
		{
			name: "enqueue stale summary rebuilds",
			run: func() error {
				_, err := store.EnqueueStaleSummaryRebuilds(ownedCtx)
				return err
			},
		},
		{
			name: "reset store",
			run: func() error {
				return store.Reset(ownedCtx)
			},
		},
		{
			name: "ensure turn window",
			run: func() error {
				_, _, err := store.EnsureTurnWindow(ownedCtx, TurnWindow{
					ProjectKey: store.ProjectKey(), SessionID: "session-stale-window",
					WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1, TurnCount: 1,
					SourceHash: "stale-owner",
				})
				return err
			},
		},
		{
			name: "mark turn window processing",
			run: func() error {
				return store.MarkTurnWindowProcessing(ownedCtx, window.ID)
			},
		},
		{
			name: "complete turn window",
			run: func() error {
				_, err := store.CompleteTurnWindow(ownedCtx, window.ID, TurnWindowSummary{Text: "stale summary"})
				return err
			},
		},
		{
			name: "fail turn window",
			run: func() error {
				return store.FailTurnWindow(ownedCtx, window.ID, "stale failure")
			},
		},
		{
			name: "mark turn digests processed",
			run: func() error {
				_, err := store.MarkTurnDigestsProcessed(ownedCtx, digest.SessionID, digest.DigestSeq, "")
				return err
			},
		},
		{
			name: "mark turn digests rollup skipped",
			run: func() error {
				_, err := store.MarkTurnDigestsRollupSkipped(ownedCtx, digest.SessionID, digest.DigestSeq)
				return err
			},
		},
		{
			name: "mark turn digests rolled up",
			run: func() error {
				_, err := store.MarkTurnDigestsRolledUp(ownedCtx, digest.SessionID, digest.DigestSeq, digest.DigestSeq, window.ID)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrMaintenanceLeaseLost) {
				t.Fatalf("stale maintenance write = %v, want ErrMaintenanceLeaseLost", err)
			}
		})
	}

	stillCandidate, err := store.GetRecord(ctx, pending.ID)
	if err != nil {
		t.Fatalf("read candidate after rejected writes: %v", err)
	}
	if stillCandidate.Status != StatusCandidate {
		t.Fatalf("stale owner changed candidate status to %q", stillCandidate.Status)
	}
	activeSummary, err := store.ActiveSummary(ctx)
	if err != nil {
		t.Fatalf("read summary after rejected writes: %v", err)
	}
	if activeSummary == nil || activeSummary.Text != "stable summary for fence" {
		t.Fatalf("stale owner changed active summary: %+v", activeSummary)
	}
	artifact, err := os.ReadFile(filepath.Join(store.Root(), "memory_summary.md"))
	if err != nil {
		t.Fatalf("read summary artifact after rejected writes: %v", err)
	}
	if string(artifact) != "stable summary for fence\n" {
		t.Fatalf("stale owner changed summary artifact to %q", artifact)
	}
	newerSummary, err := store.CompileSummary(ctx, Summary{Text: "newer summary for fence"}, []string{winner.ID})
	if err != nil {
		t.Fatalf("compile newer summary: %v", err)
	}
	if err := store.writeSummaryArtifactsIfCurrent(ctx, baselineSummary); err != nil {
		t.Fatalf("skip stale summary artifact publication: %v", err)
	}
	artifact, err = os.ReadFile(filepath.Join(store.Root(), "memory_summary.md"))
	if err != nil {
		t.Fatalf("read summary artifact after stale publication attempt: %v", err)
	}
	if string(artifact) != newerSummary.Text+"\n" {
		t.Fatalf("superseded summary overwrote artifact: got %q, want %q", artifact, newerSummary.Text+"\n")
	}
}
