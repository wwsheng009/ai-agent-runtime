package memoryruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// RunSessionEnd handles the session-end boundary. Receipt-backed sessions are
// completed only by the durable capture path. Historical sessions without a
// receipt retain the legacy compatibility/recovery path.
func (r *Runtime) RunSessionEnd(ctx context.Context, sessionID string) (RunResult, error) {
	result := RunResult{Trigger: TriggerSessionEnd}
	if r == nil || !r.enabled() {
		return result, nil
	}
	if r.Store == nil {
		if r.Config.GenerationDisabled {
			return result, nil
		}
		return r.RunSession(ctx, TriggerSessionEnd, sessionID)
	}
	session, err := r.findSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if _, ok := r.Source.(TurnCaptureReceiptSource); ok {
		if _, err := r.MaterializeTurnCaptureReceiptsForSessionGeneration(
			ctx, sessionID, session.SessionGeneration, r.Config.TurnDigestMaxDigestsPerRun,
		); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, "capture_materialization: "+err.Error())
			return result, err
		}
		if r.Config.TurnDigestEnabled && !r.Config.GenerationDisabled {
			captureRun, err := r.runPendingTurnCaptureReceiptsForGeneration(
				ctx, TriggerSessionEnd, sessionID, session.SessionGeneration,
			)
			mergeRunResult(&result, captureRun)
			copyCompletedCoverage(&result, captureRun)
			if err != nil {
				return result, err
			}
		}
	}
	captureBacked, err := r.captureBackedSession(ctx, session)
	if err != nil {
		return result, err
	}
	// A canonical receipt is an exclusive routing decision, even when an old
	// TurnDigest row happens to exist for the same session. Never drain that
	// legacy row or replay the full transcript after a receipt-backed turn.
	if captureBacked {
		if r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
			return result, nil
		}
		if ok, _ := r.eligible(ctx, session, TriggerSessionEnd); ok && result.Failed == 0 {
			r.applyRollup(ctx, sessionID, r.shouldFinalizeRollup(ctx, session, true), &result)
		}
		return result, nil
	}
	if r.Config.GenerationDisabled {
		return result, nil
	}
	if !r.Config.TurnDigestEnabled {
		return r.RunSession(ctx, TriggerSessionEnd, sessionID)
	}
	// Snapshot before reading the canonical source. A turn arriving during
	// extraction must not be acknowledged by that older extraction receipt.
	digests, err := r.Store.ListPendingTurnDigestsForGeneration(
		ctx, sessionID, session.SessionGeneration, r.Config.TurnDigestMaxPendingPerSession,
	)
	if err != nil {
		return result, err
	}
	useDigests, err := r.useDigestWindows(ctx, session)
	if err != nil {
		return result, err
	}
	if useDigests {
		if len(digests) > 0 {
			legacyRun, err := r.runSessionEndDrain(ctx, sessionID, session.SessionGeneration)
			mergeRunResult(&result, legacyRun)
			copyCompletedCoverage(&result, legacyRun)
			return result, err
		}
		if ok, _ := r.eligible(ctx, session, TriggerSessionEnd); ok {
			r.applyRollup(ctx, sessionID, r.shouldFinalizeRollup(ctx, session, true), &result)
		}
		return result, nil
	}
	legacyRun, err := r.RunSession(ctx, TriggerSessionEnd, sessionID)
	mergeRunResult(&result, legacyRun)
	copyCompletedCoverage(&result, legacyRun)
	if err == nil && legacyRun.Completed {
		if maxSeq := coveredTurnDigestSeq(legacyRun, session, digests); maxSeq > 0 {
			if _, err := r.markTurnDigestsProcessed(ctx, sessionID, session.SessionGeneration, maxSeq, ""); err != nil {
				return result, err
			}
		}
		r.applyRollup(ctx, sessionID, r.shouldFinalizeRollup(ctx, session, true), &result)
	}
	return result, err
}

// RunPendingTurnDigests processes only pre-receipt/historical outbox sessions.
// It is an explicit compatibility/recovery consumer, never a new-turn
// generation trigger. The store query and the routing fence below both exclude
// receipt-backed work.
func (r *Runtime) RunPendingTurnDigests(ctx context.Context, trigger Trigger) (RunResult, error) {
	return r.runPendingTurnDigests(ctx, trigger, "")
}

// RunPendingTurnDigestsForSession is the session-end/startup variant that
// restricts recovery to one session.
func (r *Runtime) RunPendingTurnDigestsForSession(ctx context.Context, trigger Trigger, sessionID string) (RunResult, error) {
	return r.runPendingTurnDigests(ctx, trigger, sessionID)
}

// RunPendingTurnCaptureReceipts consumes materialized v2 turn ranges. Unlike
// legacy session recovery it may process a completed range from an active
// session, but it never reads beyond the receipt's closed interval.
func (r *Runtime) RunPendingTurnCaptureReceipts(ctx context.Context, trigger Trigger) (RunResult, error) {
	return r.runPendingTurnCaptureReceipts(ctx, trigger, "", 0)
}

// RunPendingTurnCaptureReceiptsBounded drains at most limit eligible receipts.
// A non-positive limit preserves the runtime's configured per-run bound.
func (r *Runtime) RunPendingTurnCaptureReceiptsBounded(
	ctx context.Context, trigger Trigger, limit int,
) (RunResult, error) {
	return r.runPendingTurnCaptureReceipts(ctx, trigger, "", limit)
}

func (r *Runtime) runPendingTurnCaptureReceipts(
	ctx context.Context, trigger Trigger, onlySession string, maxWork int,
) (RunResult, error) {
	return r.runPendingTurnCaptureReceiptsGeneration(ctx, trigger, onlySession, nil, maxWork)
}

func (r *Runtime) runPendingTurnCaptureReceiptsForGeneration(
	ctx context.Context, trigger Trigger, onlySession string, generation int64,
) (RunResult, error) {
	return r.runPendingTurnCaptureReceiptsGeneration(ctx, trigger, onlySession, &generation, 0)
}

func (r *Runtime) runPendingTurnCaptureReceiptsGeneration(
	ctx context.Context, trigger Trigger, onlySession string, generation *int64, maxWork int,
) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if r == nil || r.Store == nil || !r.enabled() || r.Config.GenerationDisabled ||
		!r.Config.TurnDigestEnabled || r.Source == nil || r.Extractor == nil {
		return result, nil
	}
	if trigger == TriggerStartup && !r.Config.TurnDigestRecoveryOnStartup {
		return result, nil
	}
	receiptSource, ok := r.Source.(TurnCaptureReceiptSource)
	if !ok {
		return result, nil
	}
	onlySession = strings.TrimSpace(onlySession)
	pageSource, paginated := r.Source.(MaterializedTurnCaptureReceiptPageSource)
	generationPageSource, generationPaginated := r.Source.(SessionGenerationMaterializedTurnCaptureReceiptPageSource)
	if generation != nil && *generation <= 0 {
		return result, nil
	}
	if generation != nil && onlySession == "" {
		return result, fmt.Errorf("memoryruntime: generation-scoped capture recovery requires a session ID")
	}
	if generation != nil && !generationPaginated {
		return result, fmt.Errorf("memoryruntime: generation-scoped materialized capture receipt listing unavailable")
	}
	pageLimit := r.Config.TurnDigestMaxDigestsPerRun
	if pageLimit <= 0 {
		pageLimit = 200
	}
	workLimit := pageLimit
	if maxWork > 0 {
		workLimit = maxWork
		if pageLimit > maxWork {
			pageLimit = maxWork
		}
	}
	var after *chat.TurnCaptureReceipt
	workItems := 0
	// A materialized canonical receipt remains materialized after extraction.
	// Walk keyset pages until the run's work budget is consumed so completed
	// receipts cannot occupy the first page forever and starve later pending
	// captures.
	// Only materialized receipts are eligible here. Captured/deferred rows are
	// handled by the materializer and must not trigger an inline fallback.
	for {
		var receipts []chat.TurnCaptureReceipt
		var err error
		if generation != nil {
			receipts, err = generationPageSource.ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
				ctx, onlySession, *generation, after, pageLimit,
			)
		} else if paginated {
			if onlySession != "" {
				receipts, err = pageSource.ListMaterializedTurnCaptureReceiptsForSessionPage(
					ctx, onlySession, after, pageLimit)
			} else {
				receipts, err = pageSource.ListMaterializedTurnCaptureReceiptsPage(ctx, after, pageLimit)
			}
		} else if onlySession != "" {
			if scoped, ok := r.Source.(SessionTurnCaptureReceiptSource); ok {
				receipts, err = scoped.ListMaterializedTurnCaptureReceiptsForSession(ctx, onlySession, pageLimit)
			} else {
				receipts, err = receiptSource.ListMaterializedTurnCaptureReceipts(ctx, pageLimit)
			}
		} else {
			receipts, err = receiptSource.ListMaterializedTurnCaptureReceipts(ctx, pageLimit)
		}
		if err != nil {
			return result, err
		}
		if len(receipts) == 0 {
			break
		}
		for _, receipt := range receipts {
			if workItems >= workLimit {
				break
			}
			if onlySession != "" && receipt.SessionID != onlySession {
				continue
			}
			if receipt.SessionGeneration <= 0 {
				result.Skipped++
				continue
			}
			if generation != nil && receipt.SessionGeneration != *generation {
				continue
			}
			if receipt.State != "materialized" {
				continue
			}
			digest, err := r.Store.GetTurnDigestByCaptureReceipt(ctx, receipt.ID)
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors, receipt.ID+": "+err.Error())
				continue
			}
			if digest.Status != memorystore.TurnDigestPending {
				continue
			}
			if digest.MessageSeqStart <= 0 || digest.MessageSeqEnd < digest.MessageSeqStart {
				continue
			}
			session, err := r.findSession(ctx, receipt.SessionID)
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors, receipt.ID+": "+err.Error())
				continue
			}
			if session.SessionGeneration != receipt.SessionGeneration {
				// The ID now names another canonical incarnation. Leave the old
				// receipt quarantined; it must never read the new transcript.
				result.Skipped++
				continue
			}
			// A v2 capture receipt is created in the canonical terminal-commit
			// transaction and freezes an exact, immutable message interval. Startup
			// can therefore recover this committed range even when the containing
			// session still says "active" (for example, after a process crash),
			// without inferring that the session itself is orphaned or reading an
			// unfinished turn.
			if ok, reason := r.eligible(ctx, session, TriggerSessionEnd); !ok {
				result.Skipped++
				result.Errors = append(result.Errors, receipt.ID+": ineligible_"+reason)
				continue
			}
			if err := receiptSource.ValidateTurnCaptureReceipt(ctx, receipt); err != nil {
				result.Failed++
				result.Errors = append(result.Errors, receipt.ID+": "+err.Error())
				continue
			}
			windowSource, ok := r.Source.(TranscriptWindowSource)
			if !ok {
				result.Failed++
				result.Errors = append(result.Errors, receipt.ID+": transcript window source unavailable")
				continue
			}
			projectKey := strings.TrimSpace(receipt.ProjectKey)
			if projectKey == "" {
				projectKey = strings.TrimSpace(r.Config.ProjectKey)
			}
			jobKey := memorystore.TurnCaptureJobKey(memorystore.TurnCapture{
				ProjectKey: projectKey, SessionID: receipt.SessionID,
				SessionGeneration: receipt.SessionGeneration, TurnID: receipt.TurnID,
				CanonicalRevision: receipt.CanonicalRevision,
				MessageSeqStart:   receipt.MessageSeqStart, MessageSeqEnd: receipt.MessageSeqEnd,
				RangeHash: receipt.RangeHash,
			})
			part, err := r.runMaterializedTurnCapture(ctx, trigger, session, receipt, digest, windowSource, jobKey)
			if part.Completed || err != nil {
				workItems++
			}
			mergeRunResult(&result, part)
			copyCompletedCoverage(&result, part)
			if err != nil || !part.Completed {
				if err != nil {
					result.Failed++
					result.Errors = append(result.Errors, receipt.ID+": "+err.Error())
				}
			}
		}
		canContinue := paginated
		if generation != nil {
			canContinue = generationPaginated
		}
		if !canContinue || len(receipts) < pageLimit || workItems >= workLimit {
			break
		}
		last := receipts[len(receipts)-1]
		if after != nil && !captureReceiptCursorAfter(last, *after) {
			return result, fmt.Errorf("memoryruntime: materialized receipt page cursor did not advance")
		}
		after = &last
	}
	return result, nil
}

func captureReceiptCursorAfter(receipt, cursor chat.TurnCaptureReceipt) bool {
	if !receipt.MaterializedAt.Equal(cursor.MaterializedAt) {
		return receipt.MaterializedAt.After(cursor.MaterializedAt)
	}
	if !receipt.CreatedAt.Equal(cursor.CreatedAt) {
		return receipt.CreatedAt.After(cursor.CreatedAt)
	}
	return receipt.ID > cursor.ID
}

// runMaterializedTurnCapture is the v2 worker path. It claims the job created
// by the receipt materializer, reads exactly the receipt's closed range, and
// commits candidates, output receipt, digest completion, and job completion in
// one memory-store transaction.
func (r *Runtime) runMaterializedTurnCapture(
	ctx context.Context,
	trigger Trigger,
	session SessionRef,
	receipt chat.TurnCaptureReceipt,
	digest memorystore.TurnDigest,
	windowSource TranscriptWindowSource,
	jobKey string,
) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	projectKey := strings.TrimSpace(receipt.ProjectKey)
	if projectKey == "" {
		projectKey = strings.TrimSpace(r.Config.ProjectKey)
	}
	if projectKey == "" || projectKey != r.Store.ProjectKey() {
		return result, fmt.Errorf("memoryruntime: turn capture project identity mismatch")
	}
	job, err := r.Store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: "phase1_extract/v2", JobKey: jobKey, ProjectKey: projectKey,
		MaxAttempts: r.Config.MaxAttempts,
	})
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(digest.ProcessJobID) == "" || job.ID != digest.ProcessJobID {
		return result, fmt.Errorf("memoryruntime: turn capture job identity mismatch")
	}
	now := r.now()
	if job.Status == memorystore.JobStatusSucceeded {
		result.Skipped = 1
		return result, nil
	}
	if job.Status == memorystore.JobStatusFailed {
		result.Skipped = 1
		return result, nil
	}
	if job.Status == memorystore.JobStatusRunning && job.LeaseUntil.After(now) {
		result.Skipped = 1
		return result, nil
	}
	claimed, err := r.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: r.workerID(), Kind: "phase1_extract/v2", JobKey: jobKey,
		ProjectKey: projectKey, Limit: 1, Now: now, Lease: r.Config.Lease,
	})
	if err != nil {
		return result, err
	}
	if len(claimed) == 0 {
		result.Skipped = 1
		return result, nil
	}
	claimedJob := claimed[0]
	fail := func(cause error) (RunResult, error) {
		return result, r.failExtractionJob(ctx, claimedJob, cause)
	}
	receiptSource, ok := r.Source.(TurnCaptureReceiptSource)
	if !ok {
		return fail(fmt.Errorf("memoryruntime: turn capture receipt validator unavailable"))
	}
	transcript, err := readTranscriptRange(ctx, windowSource, session,
		receipt.MessageSeqStart, receipt.MessageSeqEnd, r.Config.MaxSourceMessages)
	if err != nil {
		return fail(err)
	}
	// Revalidate after reading the range. The canonical source validates both
	// its current revision and the stored per-message hashes against the receipt;
	// Transcript.Hash is a separate, redacted extraction hash and is not
	// interchangeable with the canonical range hash.
	if err := receiptSource.ValidateTurnCaptureReceipt(ctx, receipt); err != nil {
		return fail(fmt.Errorf("memoryruntime: turn capture changed after range read: %w", err))
	}
	if transcript.SessionID == "" {
		transcript.SessionID = session.ID
	}
	if transcript.ProjectKey == "" {
		transcript.ProjectKey = projectKey
	}
	if transcript.WorkspacePath == "" {
		transcript.WorkspacePath = session.WorkspacePath
	}
	pages, err := planExtractionPages(session, transcript, r.Config)
	if err != nil {
		return fail(err)
	}
	if len(pages) == 0 {
		return fail(fmt.Errorf("memoryruntime: turn capture range is empty"))
	}
	digestText, err := captureDigestText(transcript, r.Config)
	if err != nil {
		return fail(err)
	}
	extraction := ExtractionResult{}
	extractErr := withJobLeaseHeartbeat(ctx, r.Store, r.Config.Lease, claimedJob,
		func(heartbeatCtx context.Context) error {
			for _, page := range pages {
				part, pageErr := r.Extractor.Extract(heartbeatCtx, session, page)
				if pageErr != nil {
					return pageErr
				}
				extraction.Candidates = append(extraction.Candidates, part.Candidates...)
				extraction.Ignored = append(extraction.Ignored, part.Ignored...)
				if strings.TrimSpace(part.Model) != "" {
					extraction.Model = part.Model
				}
				extraction.Usage.PromptTokens += part.Usage.PromptTokens
				extraction.Usage.CompletionTokens += part.Usage.CompletionTokens
				extraction.Usage.TotalTokens += part.Usage.TotalTokens
			}
			return nil
		})
	if extractErr != nil {
		return fail(extractErr)
	}
	now = r.now()
	candidates, reasons := r.validateCandidates(extraction, transcript, now)
	result.Skipped += len(reasons) + len(extraction.Ignored)
	result.Ignored = append(result.Ignored, reasons...)
	for index := range candidates {
		candidates[index].ProjectKey = projectKey
		candidates[index].SessionID = receipt.SessionID
		candidates[index].SessionGeneration = receipt.SessionGeneration
		candidates[index].TurnID = receipt.TurnID
		candidates[index].SourceHash = receipt.RangeHash
		candidates[index].SourceRevision = receipt.CanonicalRevision
		candidates[index].SourceWindow = fmt.Sprintf("%d-%d", receipt.MessageSeqStart, receipt.MessageSeqEnd)
	}
	outputHash, err := turnCaptureOutputHash(extraction, candidates)
	if err != nil {
		return fail(err)
	}
	// Extraction can be slow. Refuse to commit output if the canonical source
	// changed while the extractor was running.
	if err := receiptSource.ValidateTurnCaptureReceipt(ctx, receipt); err != nil {
		return fail(fmt.Errorf("memoryruntime: turn capture changed before extraction commit: %w", err))
	}
	commitCtx, commitCancel := memoryJobCommitContext(ctx)
	defer commitCancel()
	inserted, err := r.Store.CommitTurnCaptureExtractionWithDiagnostics(commitCtx, claimedJob.ID,
		claimedJob.WorkerID, claimedJob.Attempt, memorystore.TurnCapture{
			ID:                 receipt.ID,
			ProjectKey:         projectKey,
			SessionID:          receipt.SessionID,
			SessionGeneration:  receipt.SessionGeneration,
			TurnID:             receipt.TurnID,
			MessageSeqStart:    receipt.MessageSeqStart,
			MessageSeqEnd:      receipt.MessageSeqEnd,
			CanonicalRevision:  receipt.CanonicalRevision,
			RangeHash:          receipt.RangeHash,
			TraceID:            receipt.TraceID,
			UserMessageID:      receipt.UserMessageID,
			AssistantMessageID: receipt.AssistantMessageID,
			Outcome:            receipt.Outcome,
			Steps:              receipt.Steps,
			UsageJSON:          receipt.UsageJSON,
			CreatedAt:          receipt.CreatedAt,
		}, digest.ID, outputHash, digestText, candidates, extractionDiagnostics(
			claimedJob.ID, projectKey, session, transcript, receipt.TurnID,
			extraction, reasons, now,
		))
	if err != nil {
		return fail(err)
	}
	result.Completed = true
	result.Processed = 1
	result.Candidates = inserted
	result.SourceHash = receipt.RangeHash
	result.SourceStartSeq = receipt.MessageSeqStart
	result.SourceEndSeq = receipt.MessageSeqEnd
	return result, nil
}

func turnCaptureOutputHash(extraction ExtractionResult, candidates []memorystore.Candidate) (string, error) {
	payload := struct {
		Model      string
		Usage      TokenUsage
		Candidates []memorystore.Candidate
		Ignored    []IgnoredCandidate
	}{
		Model: extraction.Model, Usage: extraction.Usage,
		Candidates: candidates, Ignored: extraction.Ignored,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("memoryruntime: hash turn extraction output: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func copyCompletedCoverage(dst *RunResult, src RunResult) {
	if !src.Completed {
		return
	}
	dst.Completed = true
	dst.SourceHash = src.SourceHash
	dst.SourceStartSeq = src.SourceStartSeq
	dst.SourceEndSeq = src.SourceEndSeq
}

func (r *Runtime) runPendingTurnDigests(ctx context.Context, trigger Trigger, onlySession string) (RunResult, error) {
	return r.runPendingTurnDigestsWithFinalize(ctx, trigger, onlySession, false)
}

func (r *Runtime) runSessionEndDrain(ctx context.Context, sessionID string, generation int64) (RunResult, error) {
	result := RunResult{Trigger: TriggerSessionEnd}
	maxDigests := r.Config.TurnDigestMaxDigestsPerRun
	if maxDigests <= 0 {
		maxDigests = 200
	}
	batchTurns := r.Config.TurnDigestMaxTurnsPerBatch
	if batchTurns <= 0 {
		batchTurns = 20
	}
	maxBatches := (maxDigests + batchTurns - 1) / batchTurns
	if maxBatches < 1 {
		maxBatches = 1
	}
	if maxBatches > 50 {
		maxBatches = 50
	}
	for batch := 0; batch < maxBatches; batch++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		before, err := r.Store.ListPendingTurnDigestsForGeneration(ctx, sessionID, generation, 1)
		if err != nil || len(before) == 0 {
			return result, err
		}
		part, err := r.runPendingTurnDigestsWithFinalize(ctx, TriggerSessionEnd, sessionID, true)
		if err != nil {
			return result, err
		}
		if part.Sessions > 0 {
			result.Sessions = part.Sessions
		}
		mergeRunResult(&result, part)
		if part.Failed > 0 {
			break
		}
		pending, err := r.Store.ListPendingTurnDigestsForGeneration(ctx, sessionID, generation, 1)
		if err != nil {
			return result, err
		}
		if len(pending) == 0 || pending[0].DigestSeq <= before[0].DigestSeq {
			break
		}
	}
	return result, nil
}

func (r *Runtime) runPendingTurnDigestsWithFinalize(ctx context.Context, trigger Trigger, onlySession string, finalize bool) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if r == nil || r.Store == nil || !r.enabled() || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
		return result, nil
	}
	limit := r.Config.MaxSessionsPerRun * 10
	if limit <= 0 {
		limit = 20
	}
	pendingSessions, err := r.pendingDigestSessions(ctx, onlySession, limit)
	if err != nil {
		return result, err
	}
	recoveredSessions := make(map[string]struct{})
	result.Sessions = len(pendingSessions)
	for _, pending := range pendingSessions {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		session, findErr := r.findSession(ctx, pending.SessionID)
		if findErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, pending.SessionID+": "+findErr.Error())
			continue
		}
		captureBacked, captureErr := r.captureBackedSession(ctx, session)
		if captureErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, session.ID+": "+captureErr.Error())
			continue
		}
		if captureBacked {
			result.Skipped++
			continue
		}
		digests, listErr := r.Store.ListPendingTurnDigestsForGeneration(
			ctx, session.ID, session.SessionGeneration, r.Config.TurnDigestMaxPendingPerSession,
		)
		if listErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, session.ID+": "+listErr.Error())
			continue
		}
		if len(digests) == 0 {
			result.Skipped++
			continue
		}
		if (trigger == TriggerIdle || trigger == TriggerBoth) &&
			!r.pendingDigestIdleEligible(session, pending.Pending) {
			result.Skipped++
			continue
		}
		maxSeq := digests[len(digests)-1].DigestSeq
		// Idle thresholding is handled per-session by
		// pendingDigestIdleEligible; eligible only supplies the shared
		// workspace/rollout/session-mode gates here. Startup terminal-only
		// semantics are enforced by digestRecoveryEligible below.
		if ok, reason := r.eligible(ctx, session, TriggerSessionEnd); !ok {
			if reason == "session_mode_error" {
				result.Skipped++
				continue
			}
			if _, err := r.Store.MarkTurnDigestsSkippedForGeneration(ctx, session.ID, session.SessionGeneration, maxSeq, "", "ineligible_"+reason); err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		if !r.digestRecoveryEligible(trigger, session) {
			result.Skipped++
			continue
		}
		if !hasExtractableTurnDigests(digests) {
			if _, err := r.Store.MarkTurnDigestsSkippedForGeneration(ctx, session.ID, session.SessionGeneration, maxSeq, "", "no_success_turn"); err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		if session.CanonicalCount > r.Config.MaxSourceMessages {
			if _, ok := r.Source.(TranscriptWindowSource); !ok {
				if _, err := r.Store.MarkTurnDigestsDeferredForGeneration(ctx, session.ID, session.SessionGeneration, maxSeq, "", "window_reader_unavailable"); err != nil {
					return result, err
				}
				result.Skipped++
				continue
			}
			batch := selectTurnDigestBatch(digests, r.Config.TurnDigestMaxTurnsPerBatch, r.Config.TurnDigestMaxBytesPerBatch)
			if len(batch) == 0 {
				result.Skipped++
				continue
			}
			batchMaxSeq := batch[len(batch)-1].DigestSeq
			endSeq := batch[len(batch)-1].MessageSeqEnd
			if endSeq <= 0 {
				if _, err := r.Store.MarkTurnDigestsDeferredForGeneration(ctx, session.ID, session.SessionGeneration, batchMaxSeq, "", "missing_message_seq"); err != nil {
					return result, err
				}
				result.Skipped++
				continue
			}
			prevEnd, prevErr := r.Store.PreviousTurnDigestMessageSeqEndForGeneration(ctx, session.ID, session.SessionGeneration, batch[0].DigestSeq)
			if prevErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, session.ID+": "+prevErr.Error())
				continue
			}
			startSeq := prevEnd + 1
			run, runErr := r.RunSessionWindow(ctx, TriggerSessionEnd, session.ID, startSeq, endSeq)
			mergeRunResult(&result, run)
			if runErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, session.ID+": "+runErr.Error())
				continue
			}
			if run.Completed {
				if _, err := r.markTurnDigestsProcessed(ctx, session.ID, session.SessionGeneration, batchMaxSeq, ""); err != nil {
					return result, err
				}
				r.applyRollup(ctx, session.ID, r.shouldFinalizeRollup(ctx, session, finalize), &result)
				recoveredSessions[session.ID] = struct{}{}
			}
			continue
		}
		run, runErr := r.RunSession(ctx, TriggerSessionEnd, session.ID)
		if runErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, session.ID+": "+runErr.Error())
			continue
		}
		mergeRunResult(&result, run)
		if maxSeq := coveredTurnDigestSeq(run, session, digests); maxSeq > 0 {
			if _, err := r.markTurnDigestsProcessed(ctx, session.ID, session.SessionGeneration, maxSeq, ""); err != nil {
				return result, err
			}
			r.applyRollup(ctx, session.ID, r.shouldFinalizeRollup(ctx, session, finalize), &result)
			recoveredSessions[session.ID] = struct{}{}
		}
	}
	if (trigger == TriggerStartup || trigger == TriggerIdle || trigger == TriggerBoth) && len(recoveredSessions) > 0 {
		if err := r.Store.IncrementRuntimeMetric(ctx, memorystore.MetricRecoveredDigestSessions, int64(len(recoveredSessions))); err != nil {
			return result, err
		}
	}
	if err := r.recoverPendingRollups(ctx, trigger, onlySession, recoveredSessions, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (r *Runtime) pendingDigestIdleEligible(session SessionRef, pending int) bool {
	if isTerminalSessionState(session.State) {
		return true
	}
	threshold := r.Config.TurnDigestExtractEveryNTurns
	if threshold <= 0 {
		threshold = 50
	}
	if pending >= threshold {
		return true
	}
	activity := session.LastActivityAt
	if activity.IsZero() {
		activity = session.UpdatedAt
	}
	if activity.IsZero() {
		return true
	}
	return r.now().Sub(activity) >= r.Config.MinIdle
}

func (r *Runtime) markTurnDigestsProcessed(ctx context.Context, sessionID string, generation, maxSeq int64, jobID string) (int64, error) {
	updated, err := r.Store.MarkTurnDigestsProcessedForGeneration(ctx, sessionID, generation, maxSeq, jobID)
	if err == nil && !r.Config.TurnDigestWindowSummaryEnabled {
		// Window summaries are disabled, so extraction completion is the final
		// rollup state and the digest must not be pinned by retention.
		_, err = r.Store.MarkTurnDigestsRollupSkippedForGeneration(ctx, sessionID, generation, maxSeq)
	}
	return updated, err
}

func (r *Runtime) applyRollup(ctx context.Context, sessionID string, finalize bool, result *RunResult) {
	if r == nil || result == nil {
		return
	}
	rolled, err := r.RollupTurnDigests(ctx, sessionID, finalize)
	if err != nil {
		result.RollupFailed++
		result.Errors = append(result.Errors, sessionID+": rollup: "+err.Error())
		return
	}
	result.RolledUp += rolled.RolledUp
	result.RollupFailed += rolled.Failed
	result.Errors = append(result.Errors, rolled.Errors...)
}

// MarkPendingTurnDigestsProcessed verifies a successful persisted session
// extraction (running it if necessary) before acknowledging its covered prefix
// of the captured batch.
// Oversized sessions must use the windowed recovery consumer instead.
func (r *Runtime) MarkPendingTurnDigestsProcessed(ctx context.Context, sessionID string) (int64, error) {
	if r == nil || r.Store == nil || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	session, findErr := r.findSession(ctx, sessionID)
	if findErr != nil {
		return 0, findErr
	}
	digests, err := r.Store.ListPendingTurnDigestsForGeneration(
		ctx, sessionID, session.SessionGeneration, r.Config.TurnDigestMaxPendingPerSession,
	)
	if err != nil || len(digests) == 0 {
		return 0, err
	}
	maxSeq := digests[len(digests)-1].DigestSeq
	if ok, reason := r.eligible(ctx, session, TriggerSessionEnd); !ok {
		if reason == "session_mode_error" {
			return 0, nil
		}
		return r.Store.MarkTurnDigestsSkippedForGeneration(ctx, sessionID, session.SessionGeneration, maxSeq, "", "ineligible_"+reason)
	}
	if !hasExtractableTurnDigests(digests) {
		return r.Store.MarkTurnDigestsSkippedForGeneration(ctx, sessionID, session.SessionGeneration, maxSeq, "", "no_success_turn")
	}
	if session.CanonicalCount > r.Config.MaxSourceMessages {
		return r.Store.MarkTurnDigestsDeferredForGeneration(ctx, sessionID, session.SessionGeneration, maxSeq, "", "oversize_wait_window_reader")
	}
	run, err := r.RunSession(ctx, TriggerSessionEnd, sessionID)
	if err != nil {
		return 0, err
	}
	maxSeq = coveredTurnDigestSeq(run, session, digests)
	if maxSeq == 0 {
		return 0, nil
	}
	return r.markTurnDigestsProcessed(ctx, sessionID, session.SessionGeneration, maxSeq, "")
}

// PruneTurnDigests removes terminal rows after the configured retention.
func (r *Runtime) PruneTurnDigests(ctx context.Context) (int64, error) {
	if r == nil || r.Store == nil || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
		return 0, nil
	}
	days := r.Config.TurnDigestRetentionDays
	if days <= 0 {
		days = 7
	}
	return r.Store.PruneTurnDigests(ctx, time.Now().UTC().Add(-time.Duration(days)*24*time.Hour))
}

// PruneTurnWindows removes terminal L1 rows after their independent retention.
func (r *Runtime) PruneTurnWindows(ctx context.Context) (int64, error) {
	if r == nil || r.Store == nil || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
		return 0, nil
	}
	days := r.Config.TurnDigestWindowRetentionDays
	if days <= 0 {
		days = 30
	}
	return r.Store.PruneTurnWindows(ctx, time.Now().UTC().Add(-time.Duration(days)*24*time.Hour))
}

// PurgeTurnWindows removes L1 rows for one session or the whole project. It is
// an explicit operator action and does not delete L2 memory.
func (r *Runtime) PurgeTurnWindows(ctx context.Context, sessionID string) (memorystore.TurnWindowPurgeResult, error) {
	if r == nil || r.Store == nil {
		return memorystore.TurnWindowPurgeResult{}, nil
	}
	return r.Store.PurgeTurnWindows(ctx, sessionID)
}

// DeleteSessionDigests removes the outbox rows for an explicitly deleted
// session. Memory DB and session DB are separate, so this must be called by the
// application-level session deletion path.
func (r *Runtime) DeleteSessionDigests(ctx context.Context, sessionID string) (int64, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	digests, err := r.Store.DeleteTurnDigestsForSession(ctx, sessionID)
	if err != nil {
		return digests, err
	}
	windows, err := r.Store.DeleteTurnWindowsForSession(ctx, sessionID)
	return digests + windows, err
}

// PurgeSession fences and removes every memory-side projection belonging to a
// canonical session incarnation. The generation is mandatory: unlike the
// legacy DeleteSessionDigests compatibility helper, this operation is safe
// when a session ID is deleted and later recreated while an old worker is
// still finishing.
func (r *Runtime) PurgeSession(ctx context.Context, sessionID string, generation int64) (int64, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	return r.Store.PurgeSession(ctx, sessionID, generation)
}

// PurgeSessionWithGenerations is the generation-qualified cross-store purge
// entrypoint. The session incarnation and deletion attempt are independently
// fenced by the memory store.
func (r *Runtime) PurgeSessionWithGenerations(
	ctx context.Context,
	sessionID string,
	sessionGeneration, purgeGeneration int64,
) (int64, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	return r.Store.PurgeSessionWithGenerations(ctx, sessionID, sessionGeneration, purgeGeneration)
}

// InvalidateSessionDigests prevents pending outbox rows from being extracted
// after a backtrack or compaction changed the canonical transcript.
func (r *Runtime) InvalidateSessionDigests(ctx context.Context, sessionID string) (int64, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	digests, err := r.Store.InvalidateTurnDigestsForSession(ctx, sessionID)
	if err != nil {
		return digests, err
	}
	windows, err := r.Store.InvalidateTurnWindowsForSession(ctx, sessionID, "source_invalidated")
	return digests + windows, err
}

// InvalidateSessionDigestsForGeneration invalidates digest/window backlog only
// for one canonical session incarnation.
func (r *Runtime) InvalidateSessionDigestsForGeneration(ctx context.Context, sessionID string, generation int64) (int64, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	digests, err := r.Store.InvalidateTurnDigestsForSessionGeneration(ctx, sessionID, generation)
	if err != nil {
		return digests, err
	}
	windows, err := r.Store.InvalidateTurnWindowsForSessionGeneration(ctx, sessionID, generation, "source_invalidated")
	return digests + windows, err
}

// InvalidateSessionMemories conservatively marks records derived from the
// session stale after compaction/backtrack. Pending digests are invalidated by
// InvalidateSessionDigests; this method covers already materialized candidates.
func (r *Runtime) InvalidateSessionMemories(ctx context.Context, sessionID, reason string) (int, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "compaction_or_backtrack"
	}
	return r.Store.MarkSessionSourcesStale(ctx, sessionID, r.Config.ProjectKey, reason)
}

// InvalidateSessionMemoriesForGeneration marks only records derived from one
// canonical session incarnation stale.
func (r *Runtime) InvalidateSessionMemoriesForGeneration(ctx context.Context, sessionID string, generation int64, reason string) (int, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "compaction_or_backtrack"
	}
	return r.Store.MarkSessionSourcesStaleForGeneration(ctx, sessionID, r.Config.ProjectKey, generation, reason)
}

// digestRecoveryEligible keeps startup recovery terminal-only. Startup runs
// before any session can claim the outbox, so an active/idle session is either
// still producing turns or will be handled by the idle sweep; extracting it
// here would violate the M2 startup contract and can spend LLM calls on a live
// conversation. Session-end and idle triggers use their own eligibility in
// eligible/pendingDigestIdleEligible.
func (r *Runtime) digestRecoveryEligible(trigger Trigger, session SessionRef) bool {
	if trigger == TriggerStartup {
		return isTerminalSessionState(session.State)
	}
	return true
}

func truncateTurnDigestText(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return text, false
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text, false
	}
	return string(runes[:maxRunes]) + "…", true
}

func hasExtractableTurnDigests(digests []memorystore.TurnDigest) bool {
	for _, digest := range digests {
		if strings.EqualFold(strings.TrimSpace(digest.Outcome), "success") {
			return true
		}
	}
	return false
}

func selectTurnDigestBatch(digests []memorystore.TurnDigest, maxTurns, maxBytes int) []memorystore.TurnDigest {
	if len(digests) == 0 {
		return nil
	}
	if maxTurns <= 0 {
		maxTurns = 20
	}
	if maxBytes <= 0 {
		maxBytes = 128 * 1024
	}
	batch := make([]memorystore.TurnDigest, 0, maxTurns)
	usedBytes := 0
	for _, digest := range digests {
		size := len(digest.UserText) + len(digest.AssistantText) + 512
		if len(batch) > 0 && (len(batch) >= maxTurns || usedBytes+size > maxBytes) {
			break
		}
		batch = append(batch, digest)
		usedBytes += size
	}
	return batch
}
