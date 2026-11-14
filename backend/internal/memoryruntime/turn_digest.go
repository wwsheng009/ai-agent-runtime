package memoryruntime

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// RunSessionEnd handles the session-end boundary. Small sessions use the
// existing latest-page path; oversized sessions with an outbox drain bounded
// canonical windows. Historical sessions without an outbox retain extraction.
func (r *Runtime) RunSessionEnd(ctx context.Context, sessionID string) (RunResult, error) {
	result := RunResult{Trigger: TriggerSessionEnd}
	if r == nil || !r.enabled() || r.Config.GenerationDisabled {
		return result, nil
	}
	if r.Store == nil || !r.Config.TurnDigestEnabled {
		return r.RunSession(ctx, TriggerSessionEnd, sessionID)
	}
	session, err := r.findSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	useDigests, err := r.useDigestWindows(ctx, session)
	if err != nil {
		return result, err
	}
	// Snapshot before reading the canonical source. A turn arriving during
	// extraction must not be acknowledged by that older extraction receipt.
	digests, err := r.Store.ListPendingTurnDigests(ctx, sessionID, r.Config.TurnDigestMaxPendingPerSession)
	if err != nil {
		return result, err
	}
	if useDigests {
		if len(digests) > 0 {
			return r.runSessionEndDrain(ctx, sessionID)
		}
		if ok, _ := r.eligible(ctx, session, TriggerSessionEnd); ok {
			r.applyRollup(ctx, sessionID, r.shouldFinalizeRollup(ctx, session, true), &result)
		}
		return result, nil
	}
	result, err = r.RunSession(ctx, TriggerSessionEnd, sessionID)
	if err == nil && result.Completed {
		if maxSeq := coveredTurnDigestSeq(result, session, digests); maxSeq > 0 {
			if _, err := r.markTurnDigestsProcessed(ctx, sessionID, maxSeq, ""); err != nil {
				return result, err
			}
		}
		r.applyRollup(ctx, sessionID, r.shouldFinalizeRollup(ctx, session, true), &result)
	}
	return result, err
}

// RunPendingTurnDigests processes pending outbox sessions using the canonical
// transcript path. It is a recovery consumer, not a new generation trigger.
func (r *Runtime) RunPendingTurnDigests(ctx context.Context, trigger Trigger) (RunResult, error) {
	return r.runPendingTurnDigests(ctx, trigger, "")
}

// RunPendingTurnDigestsForSession is the session-end/startup variant that
// restricts recovery to one session.
func (r *Runtime) RunPendingTurnDigestsForSession(ctx context.Context, trigger Trigger, sessionID string) (RunResult, error) {
	return r.runPendingTurnDigests(ctx, trigger, sessionID)
}

func (r *Runtime) runPendingTurnDigests(ctx context.Context, trigger Trigger, onlySession string) (RunResult, error) {
	return r.runPendingTurnDigestsWithFinalize(ctx, trigger, onlySession, false)
}

func (r *Runtime) runSessionEndDrain(ctx context.Context, sessionID string) (RunResult, error) {
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
		before, err := r.Store.ListPendingTurnDigests(ctx, sessionID, 1)
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
		pending, err := r.Store.ListPendingTurnDigests(ctx, sessionID, 1)
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
		digests, listErr := r.Store.ListPendingTurnDigests(ctx, session.ID, r.Config.TurnDigestMaxPendingPerSession)
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
			if _, err := r.Store.MarkTurnDigestsSkipped(ctx, session.ID, maxSeq, "", "ineligible_"+reason); err != nil {
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
			if _, err := r.Store.MarkTurnDigestsSkipped(ctx, session.ID, maxSeq, "", "no_success_turn"); err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		if session.CanonicalCount > r.Config.MaxSourceMessages {
			if _, ok := r.Source.(TranscriptWindowSource); !ok {
				if _, err := r.Store.MarkTurnDigestsDeferred(ctx, session.ID, maxSeq, "", "window_reader_unavailable"); err != nil {
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
				if _, err := r.Store.MarkTurnDigestsDeferred(ctx, session.ID, batchMaxSeq, "", "missing_message_seq"); err != nil {
					return result, err
				}
				result.Skipped++
				continue
			}
			prevEnd, prevErr := r.Store.PreviousTurnDigestMessageSeqEnd(ctx, session.ID, batch[0].DigestSeq)
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
				if _, err := r.markTurnDigestsProcessed(ctx, session.ID, batchMaxSeq, ""); err != nil {
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
			if _, err := r.markTurnDigestsProcessed(ctx, session.ID, maxSeq, ""); err != nil {
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

func (r *Runtime) markTurnDigestsProcessed(ctx context.Context, sessionID string, maxSeq int64, jobID string) (int64, error) {
	updated, err := r.Store.MarkTurnDigestsProcessed(ctx, sessionID, maxSeq, jobID)
	if err == nil && !r.Config.TurnDigestWindowSummaryEnabled {
		// Window summaries are disabled, so extraction completion is the final
		// rollup state and the digest must not be pinned by retention.
		_, err = r.Store.MarkTurnDigestsRollupSkipped(ctx, sessionID, maxSeq)
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
	digests, err := r.Store.ListPendingTurnDigests(ctx, sessionID, r.Config.TurnDigestMaxPendingPerSession)
	if err != nil || len(digests) == 0 {
		return 0, err
	}
	maxSeq := digests[len(digests)-1].DigestSeq
	session, findErr := r.findSession(ctx, sessionID)
	if findErr != nil {
		return 0, findErr
	}
	if ok, reason := r.eligible(ctx, session, TriggerSessionEnd); !ok {
		if reason == "session_mode_error" {
			return 0, nil
		}
		return r.Store.MarkTurnDigestsSkipped(ctx, sessionID, maxSeq, "", "ineligible_"+reason)
	}
	if !hasExtractableTurnDigests(digests) {
		return r.Store.MarkTurnDigestsSkipped(ctx, sessionID, maxSeq, "", "no_success_turn")
	}
	if session.CanonicalCount > r.Config.MaxSourceMessages {
		return r.Store.MarkTurnDigestsDeferred(ctx, sessionID, maxSeq, "", "oversize_wait_window_reader")
	}
	run, err := r.RunSession(ctx, TriggerSessionEnd, sessionID)
	if err != nil {
		return 0, err
	}
	maxSeq = coveredTurnDigestSeq(run, session, digests)
	if maxSeq == 0 {
		return 0, nil
	}
	return r.markTurnDigestsProcessed(ctx, sessionID, maxSeq, "")
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
