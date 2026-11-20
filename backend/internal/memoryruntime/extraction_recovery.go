package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// failExtractionJob preserves both the extraction error and a failed durable
// retry transition (for example when the worker lost its lease).
func (r *Runtime) failExtractionJob(ctx context.Context, job memorystore.MemoryJob, cause error) error {
	// Persist failure even when the provider exhausted its request deadline.
	// This is a bounded bookkeeping context, never a continuation of model work.
	cleanupCtx, cancel := memoryJobCleanupContext(ctx)
	defer cancel()
	safeCause := safeMemoryError(cause)
	err := r.Store.FailJobForWorkerAttempt(cleanupCtx, job.ID, job.WorkerID, job.Attempt,
		safeMemoryErrorText(safeCause), r.now().Add(retryBackoff(job.Attempt)))
	if err != nil {
		return errors.Join(safeCause, fmt.Errorf("memoryruntime: persist extraction failure: %w", err))
	}
	return safeCause
}

func memoryJobCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func memoryJobCommitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func failMemoryJobForWorker(ctx context.Context, store *memorystore.Store, id, workerID, errText string, retryAt time.Time) error {
	cleanupCtx, cancel := memoryJobCleanupContext(ctx)
	defer cancel()
	return store.FailJobForWorker(cleanupCtx, id, workerID, safeMemoryErrorText(errors.New(errText)), retryAt)
}

func verifyTranscriptCoverage(transcript Transcript, startSeq, endSeq int64) error {
	if int64(len(transcript.Messages)) != endSeq-startSeq+1 {
		return fmt.Errorf("memoryruntime: incomplete canonical window %d-%d: got %d messages", startSeq, endSeq, len(transcript.Messages))
	}
	for i, message := range transcript.Messages {
		if int64(message.Seq) != startSeq+int64(i) {
			return fmt.Errorf("memoryruntime: incomplete canonical window %d-%d: expected seq %d, got %d", startSeq, endSeq, startSeq+int64(i), message.Seq)
		}
	}
	return nil
}

func completeTranscriptResult(result *RunResult, transcript Transcript) {
	result.Completed = true
	result.SourceHash = transcript.Hash
	if len(transcript.Messages) == 0 {
		return
	}
	start := int64(transcript.Messages[0].Seq)
	end := int64(transcript.Messages[len(transcript.Messages)-1].Seq)
	if start > 0 && verifyTranscriptCoverage(transcript, start, end) == nil {
		result.SourceStartSeq, result.SourceEndSeq = start, end
	}
}

func coversSession(result RunResult, session SessionRef) bool {
	return result.Completed && result.SourceStartSeq == 1 &&
		result.SourceEndSeq >= int64(session.CanonicalCount)
}

// coveredTurnDigestSeq limits a session receipt to the captured, covered
// outbox prefix. Session metadata alone is not proof that a newer digest's
// canonical endpoint was extracted. Stop at the first uncovered row because
// the store's acknowledgement is inclusive of all earlier digest sequences.
func coveredTurnDigestSeq(result RunResult, session SessionRef, digests []memorystore.TurnDigest) int64 {
	if !coversSession(result, session) {
		return 0
	}
	var maxSeq int64
	for _, digest := range digests {
		if digest.MessageSeqEnd > result.SourceEndSeq {
			break
		}
		// Legacy rows without a canonical endpoint still require the full
		// session coverage check above; they never allow skipping a gap.
		maxSeq = digest.DigestSeq
	}
	return maxSeq
}

// The outbox is an optional optimization, not an extraction prerequisite.
// With no outbox (including pre-upgrade history), retain the bounded latest-page
// extraction path. Already-consumed/skipped outboxes are not legacy history.
func (r *Runtime) useDigestWindows(ctx context.Context, session SessionRef) (bool, error) {
	if session.CanonicalCount <= r.Config.MaxSourceMessages || !r.Config.TurnDigestEnabled || r.Store == nil {
		return false, nil
	}
	return r.Store.HasTurnDigestsForSession(ctx, session.ID)
}
