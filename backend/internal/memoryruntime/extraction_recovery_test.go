package memoryruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

type recoveryExtractorFunc func(context.Context, SessionRef, Transcript) (ExtractionResult, error)

func (f recoveryExtractorFunc) Extract(ctx context.Context, session SessionRef, transcript Transcript) (ExtractionResult, error) {
	return f(ctx, session, transcript)
}

type recoverySource struct {
	session      SessionRef
	messages     []TranscriptMessage
	readCalls    int
	windowCalls  int
	dropFirstSeq bool
}

func (s *recoverySource) ListSessions(context.Context, string, int) ([]SessionRef, error) {
	return []SessionRef{s.session}, nil
}

func (s *recoverySource) ReadSessionIdentity(_ context.Context, id string) (SessionIdentity, error) {
	if id != s.session.ID {
		return SessionIdentity{}, fmt.Errorf("unknown session %s", id)
	}
	return SessionIdentity{Session: s.session}, nil
}

func (s *recoverySource) transcript(messages []TranscriptMessage) Transcript {
	return Transcript{
		SessionID: s.session.ID, WorkspacePath: s.session.WorkspacePath,
		Revision: int64(len(s.messages)), UpdatedAt: s.session.LastActivityAt,
		Messages: append([]TranscriptMessage(nil), messages...), Hash: TranscriptHash(messages),
	}
}

func (s *recoverySource) ReadTranscript(_ context.Context, _ SessionRef, limit int) (Transcript, error) {
	s.readCalls++
	messages := s.messages
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return s.transcript(messages), nil
}

func (s *recoverySource) ReadTranscriptWindow(_ context.Context, _ SessionRef, start, end, limit int) (Transcript, error) {
	s.windowCalls++
	if end-start+1 > limit {
		end = start + limit - 1
	}
	messages := make([]TranscriptMessage, 0, limit)
	for _, message := range s.messages {
		if message.Seq >= start && message.Seq <= end && (!s.dropFirstSeq || message.Seq != start) {
			messages = append(messages, message)
		}
	}
	return s.transcript(messages), nil
}

func newRecoveryRuntime(t *testing.T, count, limit int) (*Runtime, *recoverySource, *memorystore.Store) {
	t.Helper()
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	source := &recoverySource{session: SessionRef{
		ID: "recovery-session", WorkspacePath: "/workspace", State: "closed",
		CanonicalCount: count, LastActivityAt: now.Add(-time.Hour),
	}}
	for seq := 1; seq <= count; seq++ {
		source.messages = append(source.messages, TranscriptMessage{
			ID: fmt.Sprintf("message-%d", seq), Seq: seq, Role: "assistant", Content: fmt.Sprintf("canonical message %d", seq),
		})
	}
	rt := New(store, source, recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
		return ExtractionResult{}, nil
	}), fakeModes{}, Config{
		Mode: "shadow", Workspace: "/workspace", ProjectKey: store.ProjectKey(),
		MaxSourceMessages: limit, TurnDigestEnabled: true,
	})
	rt.Clock = func() time.Time { return now }
	return rt, source, store
}

func recoverySQL(t *testing.T, store *memorystore.Store) *sql.DB {
	t.Helper()
	_, err := store.Status(context.Background())
	require.NoError(t, err)
	db, err := sqliteutil.OpenFile(store.DBPath(), true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRecoveryCompletionReceiptRequiresSucceededJob(t *testing.T) {
	for _, windowed := range []bool{false, true} {
		for _, state := range []string{"running", "backoff", "failed", "succeeded", "wrong_watermark"} {
			t.Run(fmt.Sprintf("window=%t/%s", windowed, state), func(t *testing.T) {
				ctx := context.Background()
				count := 4
				if windowed {
					count = 12
				}
				rt, source, store := newRecoveryRuntime(t, count, 4)
				transcript := source.transcript(source.messages[:4])
				key := "session:" + source.session.ID + ":" + transcript.Hash
				if windowed {
					key = fmt.Sprintf("session:%s:window:1-4:%s", source.session.ID, transcript.Hash)
				}
				attempts := 3
				if state == "failed" {
					attempts = 1
				}
				job, err := store.EnsureJob(ctx, memorystore.MemoryJob{
					Kind: "phase1_extract", JobKey: key, MaxAttempts: attempts,
				})
				require.NoError(t, err)
				claimed, err := store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
					WorkerID: "other-worker", JobKey: key, Now: rt.now(), Limit: 1,
				})
				require.NoError(t, err)
				require.Len(t, claimed, 1)
				switch state {
				case "backoff", "failed":
					require.NoError(t, store.FailJobForWorker(ctx, job.ID, "other-worker", "provider unavailable", rt.now().Add(time.Hour)))
				case "succeeded":
					require.NoError(t, store.CompleteJobForWorker(ctx, job.ID, "other-worker", transcript.Hash))
				case "wrong_watermark":
					require.NoError(t, store.CompleteJobForWorker(ctx, job.ID, "other-worker", "different-source"))
				}
				_, err = store.RecordTurnDigest(ctx, memorystore.TurnDigest{
					SessionID: source.session.ID, TurnID: "turn", Outcome: "success",
					MessageSeqEnd: 4, AssistantText: "answer",
				})
				require.NoError(t, err)
				calls := 0
				rt.Extractor = recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
					calls++
					return ExtractionResult{}, nil
				})
				var result RunResult
				if windowed {
					result, err = rt.RunSessionWindow(ctx, TriggerSessionEnd, source.session.ID, 1, 4)
				} else {
					result, err = rt.RunSession(ctx, TriggerSessionEnd, source.session.ID)
				}
				if state == "wrong_watermark" {
					require.ErrorContains(t, err, "matching success watermark")
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, state == "succeeded", result.Completed)
				if !result.Completed {
					require.Empty(t, result.SourceHash)
				}
				_, _ = rt.RunSessionEnd(ctx, source.session.ID)
				digest, err := store.GetTurnDigestByTurn(ctx, source.session.ID, "turn")
				require.NoError(t, err)
				expected := memorystore.TurnDigestPending
				if state == "succeeded" {
					expected = memorystore.TurnDigestDone
				}
				require.Equal(t, expected, digest.Status)
				require.Zero(t, calls, "lease, backoff, exhausted, and succeeded jobs must not re-extract")
			})
		}
	}
}

func TestRecoveryPropagatesExtractionStateWriteFailures(t *testing.T) {
	for _, windowed := range []bool{false, true} {
		for _, failure := range []string{"complete", "fail"} {
			t.Run(fmt.Sprintf("window=%t/%s", windowed, failure), func(t *testing.T) {
				ctx := context.Background()
				rt, source, store := newRecoveryRuntime(t, 4, 4)
				db := recoverySQL(t, store)
				status := "succeeded"
				if failure == "fail" {
					status = "pending"
					rt.Extractor = recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
						return ExtractionResult{}, errors.New("provider unavailable")
					})
				}
				_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER reject_job_transition
BEFORE UPDATE OF status ON memory_jobs WHEN OLD.status = 'running' AND NEW.status = '%s'
BEGIN SELECT RAISE(FAIL, 'injected job state write failure'); END`, status))
				require.NoError(t, err)
				var result RunResult
				if windowed {
					result, err = rt.RunSessionWindow(ctx, TriggerSessionEnd, source.session.ID, 1, 4)
				} else {
					result, err = rt.RunSession(ctx, TriggerSessionEnd, source.session.ID)
				}
				require.ErrorContains(t, err, "injected job state write failure")
				if failure == "fail" {
					require.ErrorContains(t, err, "provider unavailable")
				}
				require.False(t, result.Completed)
				require.Empty(t, result.SourceHash)
				require.Zero(t, result.Processed)
			})
		}
	}
}

func TestExtractionCommitSurvivesCancellationAfterSuccessfulExtraction(t *testing.T) {
	for _, windowed := range []bool{false, true} {
		t.Run(fmt.Sprintf("windowed=%t", windowed), func(t *testing.T) {
			rt, source, store := newRecoveryRuntime(t, 4, 4)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt.Extractor = recoveryExtractorFunc(func(_ context.Context, _ SessionRef, transcript Transcript) (ExtractionResult, error) {
				cancel()
				last := transcript.Messages[len(transcript.Messages)-1]
				return ExtractionResult{Candidates: []Candidate{{
					Kind: "decision", Text: "Persist completed extraction after cancellation.",
					Confidence: 0.9, SourceMessageIDs: []string{last.ID},
				}}}, nil
			})

			var result RunResult
			var err error
			if windowed {
				result, err = rt.RunSessionWindow(ctx, TriggerSessionEnd, source.session.ID, 1, 4)
			} else {
				result, err = rt.RunSession(ctx, TriggerSessionEnd, source.session.ID)
			}
			require.NoError(t, err)
			require.True(t, result.Completed)
			require.Equal(t, 1, result.Processed)
			records, err := store.ListRecords(context.Background(), memorystore.RecordFilter{
				Status: memorystore.StatusCandidate, Limit: 10,
			})
			require.NoError(t, err)
			require.Len(t, records.Items, 1)
			require.Equal(t, "Persist completed extraction after cancellation.", records.Items[0].Text)
		})
	}
}

func TestRecoveryAcknowledgementWriteFailureRetriesWithoutExtraction(t *testing.T) {
	ctx := context.Background()
	rt, source, store := newRecoveryRuntime(t, 4, 4)
	_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		SessionID: source.session.ID, TurnID: "turn", Outcome: "success", MessageSeqEnd: 4,
	})
	require.NoError(t, err)
	db := recoverySQL(t, store)
	_, err = db.ExecContext(ctx, `CREATE TRIGGER reject_ack BEFORE UPDATE OF status ON memory_turn_digests
WHEN NEW.status = 'done' BEGIN SELECT RAISE(FAIL, 'injected acknowledgement failure'); END`)
	require.NoError(t, err)
	calls := 0
	rt.Extractor = recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
		calls++
		return ExtractionResult{}, nil
	})
	_, err = rt.RunPendingTurnDigestsForSession(ctx, TriggerStartup, source.session.ID)
	require.ErrorContains(t, err, "injected acknowledgement failure")
	pending, err := store.ListPendingTurnDigests(ctx, source.session.ID, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	_, err = db.ExecContext(ctx, `DROP TRIGGER reject_ack`)
	require.NoError(t, err)
	result, err := rt.RunPendingTurnDigestsForSession(ctx, TriggerStartup, source.session.ID)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Zero(t, result.Processed)
	require.Equal(t, 1, calls)
	pending, err = store.ListPendingTurnDigests(ctx, source.session.ID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestRecoveryRejectsIncompleteCanonicalSubwindow(t *testing.T) {
	ctx := context.Background()
	rt, source, store := newRecoveryRuntime(t, 12, 4)
	source.dropFirstSeq = true
	_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		SessionID: source.session.ID, TurnID: "large-turn", Outcome: "success", MessageSeqEnd: 12,
	})
	require.NoError(t, err)
	result, err := rt.RunSessionWindow(ctx, TriggerSessionEnd, source.session.ID, 1, 12)
	require.ErrorContains(t, err, "incomplete canonical window 1-4")
	require.False(t, result.Completed)
	result, err = rt.RunPendingTurnDigestsForSession(ctx, TriggerStartup, source.session.ID)
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	pending, err := store.ListPendingTurnDigests(ctx, source.session.ID, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

func TestRecoveryOversizedFallbackWithoutDigestCoverage(t *testing.T) {
	for _, entrypoint := range []string{"idle", "session_end"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/digest=%t", entrypoint, enabled), func(t *testing.T) {
				ctx := context.Background()
				rt, source, store := newRecoveryRuntime(t, 12, 4)
				rt.Config.TurnDigestEnabled = enabled
				source.session.LastActivityAt = rt.now().Add(-7 * time.Hour)
				if !enabled {
					_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
						SessionID: source.session.ID, TurnID: "uncovered", Outcome: "success", MessageSeqEnd: 4,
					})
					require.NoError(t, err)
				}
				var seen []int
				rt.Extractor = recoveryExtractorFunc(func(_ context.Context, _ SessionRef, transcript Transcript) (ExtractionResult, error) {
					for _, message := range transcript.Messages {
						seen = append(seen, message.Seq)
					}
					return ExtractionResult{}, nil
				})
				var result RunResult
				var err error
				if entrypoint == "idle" {
					result, err = rt.Run(ctx, TriggerIdle)
				} else {
					result, err = rt.RunSessionEnd(ctx, source.session.ID)
				}
				require.NoError(t, err)
				require.Empty(t, result.Errors)
				require.Equal(t, 1, result.Processed)
				require.Equal(t, []int{9, 10, 11, 12}, seen)
				require.Zero(t, source.windowCalls)
				if !enabled {
					digest, err := store.GetTurnDigestByTurn(ctx, source.session.ID, "uncovered")
					require.NoError(t, err)
					require.Equal(t, memorystore.TurnDigestPending, digest.Status)
				}
			})
		}
	}
}

func TestRecoveryTargetedSessionBypassesGlobalDigestFirstPage(t *testing.T) {
	ctx := context.Background()
	rt, source, store := newRecoveryRuntime(t, 4, 4)
	rt.Config.MaxSessionsPerRun = 1
	for index := 0; index < 15; index++ {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: fmt.Sprintf("unrelated-%02d", index), TurnID: "turn", Outcome: "success",
		})
		require.NoError(t, err)
	}
	_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		SessionID: source.session.ID, TurnID: "target", Outcome: "success", MessageSeqEnd: 4,
	})
	require.NoError(t, err)
	result, err := rt.RunPendingTurnDigestsForSession(ctx, TriggerStartup, source.session.ID)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 1, result.Processed)
	target, err := store.GetTurnDigestByTurn(ctx, source.session.ID, "target")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, target.Status)
}
