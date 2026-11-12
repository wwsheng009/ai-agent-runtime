package memoryruntime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

var _ chat.TurnDigestSink = (*Runtime)(nil)

// RecordTurnDigest implements chat.TurnDigestSink. It persists only bounded,
// redacted turn text and never invokes an LLM.
func (r *Runtime) RecordTurnDigest(ctx context.Context, digest chat.TurnDigest) error {
	if r == nil || r.Store == nil || !r.enabled() || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
		return nil
	}
	userText := strings.TrimSpace(digest.Prompt)
	assistantText := strings.TrimSpace(digest.AssistantOutput)
	if r.Config.RedactionEnabled {
		userText, _ = memoryredact.Redact(userText)
		assistantText, _ = memoryredact.Redact(assistantText)
	}
	userText, userTruncated := truncateTurnDigestText(userText, r.Config.TurnDigestMaxUserChars)
	assistantText, assistantTruncated := truncateTurnDigestText(assistantText, r.Config.TurnDigestMaxAssistantChars)
	if !r.Config.TurnDigestStoreText {
		userText = ""
		assistantText = ""
	}
	outcome := strings.TrimSpace(digest.Outcome)
	if outcome == "" {
		outcome = "success"
	}
	usageJSON := "{}"
	if digest.Usage != nil {
		if raw, err := json.Marshal(digest.Usage); err == nil {
			usageJSON = string(raw)
		}
	}
	metadataJSON := "{}"
	if raw, err := json.Marshal(map[string]interface{}{
		"assistant_stream_id": strings.TrimSpace(digest.AssistantStreamID),
		"store_text":          r.Config.TurnDigestStoreText,
		"user_truncated":      userTruncated,
		"assistant_truncated": assistantTruncated,
	}); err == nil {
		metadataJSON = string(raw)
	}
	now := time.Now().UTC()
	_, err := r.Store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		Scope:              memorystore.ScopeProject,
		ProjectKey:         r.Config.ProjectKey,
		SessionID:          strings.TrimSpace(digest.SessionID),
		TurnID:             strings.TrimSpace(digest.TurnID),
		TraceID:            strings.TrimSpace(digest.TraceID),
		UserMessageID:      strings.TrimSpace(digest.UserMessageID),
		AssistantMessageID: strings.TrimSpace(digest.AssistantMessageID),
		MessageSeqEnd:      digest.MessageSeqEnd,
		UserText:           userText,
		AssistantText:      assistantText,
		TextTruncated:      userTruncated || assistantTruncated,
		Outcome:            outcome,
		Steps:              digest.Steps,
		UsageJSON:          usageJSON,
		MetadataJSON:       metadataJSON,
		Status:             memorystore.TurnDigestPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	return err
}

// RunSessionEnd handles the session-end boundary. Small sessions use the
// existing latest-page path; oversized sessions drain one windowed batch.
func (r *Runtime) RunSessionEnd(ctx context.Context, sessionID string) (RunResult, error) {
	var result RunResult
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
	if session.CanonicalCount > r.Config.MaxSourceMessages {
		return r.runSessionEndDrain(ctx, sessionID)
	}
	result, err = r.RunSession(ctx, TriggerSessionEnd, sessionID)
	if err == nil && result.SourceHash != "" {
		_, _ = r.MarkPendingTurnDigestsProcessed(ctx, sessionID)
		r.applyRollup(ctx, sessionID, true, &result)
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
			break
		}
		part, err := r.runPendingTurnDigestsWithFinalize(ctx, TriggerSessionEnd, sessionID, true)
		if err != nil {
			return result, err
		}
		if part.Sessions > 0 {
			result.Sessions = part.Sessions
		}
		mergeRunResult(&result, part)
		if part.Processed == 0 || part.Failed > 0 {
			break
		}
		pending, err := r.Store.ListPendingTurnDigests(ctx, sessionID, 1)
		if err != nil {
			return result, err
		}
		if len(pending) == 0 {
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
	pendingSessions, err := r.Store.ListPendingTurnDigestSessions(ctx, r.Config.ProjectKey, limit)
	if err != nil {
		return result, err
	}
	recoveredSessions := make(map[string]struct{})
	result.Sessions = len(pendingSessions)
	for _, pending := range pendingSessions {
		if onlySession != "" && pending.SessionID != onlySession {
			continue
		}
		if ctx.Err() != nil {
			break
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
			_, _ = r.Store.MarkTurnDigestsSkipped(ctx, session.ID, maxSeq, "", "ineligible_"+reason)
			result.Skipped++
			continue
		}
		if !r.digestRecoveryEligible(trigger, session) {
			result.Skipped++
			continue
		}
		if !hasExtractableTurnDigests(digests) {
			_, _ = r.Store.MarkTurnDigestsSkipped(ctx, session.ID, maxSeq, "", "no_success_turn")
			result.Skipped++
			continue
		}
		if session.CanonicalCount > r.Config.MaxSourceMessages {
			if _, ok := r.Source.(TranscriptWindowSource); !ok {
				_, _ = r.Store.MarkTurnDigestsDeferred(ctx, session.ID, maxSeq, "", "window_reader_unavailable")
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
				_, _ = r.Store.MarkTurnDigestsDeferred(ctx, session.ID, batchMaxSeq, "", "missing_message_seq")
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
			if runErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, session.ID+": "+runErr.Error())
				continue
			}
			mergeRunResult(&result, run)
			if run.SourceHash != "" {
				_, _ = r.markTurnDigestsProcessed(ctx, session.ID, batchMaxSeq, "")
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
		if run.SourceHash != "" {
			_, _ = r.markTurnDigestsProcessed(ctx, session.ID, maxSeq, "")
			r.applyRollup(ctx, session.ID, r.shouldFinalizeRollup(ctx, session, finalize), &result)
			recoveredSessions[session.ID] = struct{}{}
		}
	}
	if (trigger == TriggerStartup || trigger == TriggerIdle || trigger == TriggerBoth) && len(recoveredSessions) > 0 {
		_ = r.Store.IncrementRuntimeMetric(ctx, memorystore.MetricRecoveredDigestSessions, int64(len(recoveredSessions)))
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
		_, _ = r.Store.MarkTurnDigestsRollupSkipped(ctx, sessionID, maxSeq)
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

// MarkPendingTurnDigestsProcessed marks the current pending batch done after a
// successful session-end extraction. Large sessions are deferred until the
// windowed canonical reader exists.
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
		return r.Store.MarkTurnDigestsSkipped(ctx, sessionID, maxSeq, "", "ineligible_"+reason)
	}
	if !hasExtractableTurnDigests(digests) {
		return r.Store.MarkTurnDigestsSkipped(ctx, sessionID, maxSeq, "", "no_success_turn")
	}
	if session.CanonicalCount > r.Config.MaxSourceMessages {
		return r.Store.MarkTurnDigestsDeferred(ctx, sessionID, maxSeq, "", "oversize_wait_window_reader")
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
