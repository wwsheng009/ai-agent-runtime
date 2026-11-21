package memoryruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type Runtime struct {
	Store            *memorystore.Store
	Source           SessionSource
	Extractor        Extractor
	WindowSummarizer WindowSummarizer
	Modes            SessionModeReader
	Config           Config
	Clock            func() time.Time
	WorkerID         string

	rollupRecoveryMu     sync.Mutex
	rollupRecoveryCursor string

	digestStatsMu sync.Mutex
	digestStats   DigestStats
}

func New(store *memorystore.Store, source SessionSource, extractor Extractor, modes SessionModeReader, cfg Config) *Runtime {
	return &Runtime{
		Store: store, Source: source, Extractor: extractor, Modes: modes,
		Config: cfg.normalized(), WorkerID: strings.TrimSpace(cfg.WorkerID),
		Clock: time.Now,
	}
}

func (r *Runtime) now() time.Time {
	if r != nil && r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

// SweepFreshness runs one bounded freshness pass using the configured
// revalidateBatchSize. It is a no-op when freshness policy is disabled.
func (r *Runtime) SweepFreshness(ctx context.Context) (int, error) {
	if r == nil || r.Store == nil || r.Config.FreshnessDisabled {
		return 0, nil
	}
	return r.Store.SweepFreshnessBatch(ctx, r.now(), r.Config.FreshnessRevalidateBatchSize)
}

func (r *Runtime) enabled() bool {
	if r == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(r.Config.Mode))
	return mode == "shadow" || mode == "on"
}

func (r *Runtime) generationAllowed(trigger Trigger) bool {
	if r == nil {
		return false
	}
	return !r.Config.GenerationDisabled || trigger == TriggerManual
}

// Run is the normal background entry point. A receipt-capable source owns the
// normal path: materialize and process durable turn captures, then return.
// It must not fall through to session-wide extraction because that would make
// the same canonical turn eligible for both the capture worker and a legacy
// session replay. Historical extraction is exposed only through the explicit
// compatibility/recovery entry points.
func (r *Runtime) Run(ctx context.Context, trigger Trigger) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if !r.enabled() {
		return result, nil
	}
	if r.Source == nil {
		return result, fmt.Errorf("memoryruntime: session source is nil")
	}
	if _, receiptSource := r.Source.(TurnCaptureReceiptSource); !receiptSource {
		// A source without the receipt contract is a legacy adapter. Keep its
		// bounded sweep for migration/recovery callers, but never use this
		// branch for canonical receipt-capable sources.
		return r.runLegacySweep(ctx, trigger)
	}
	if r.Store == nil {
		return result, fmt.Errorf("memoryruntime: memory store is nil")
	}
	// Materialization is deliberately independent of the LLM generation
	// gate. A disabled generator must not strand the canonical handoff.
	if _, err := r.MaterializeTurnCaptureReceipts(ctx, r.Config.TurnDigestMaxDigestsPerRun); err != nil {
		return result, err
	}
	if !r.generationAllowed(trigger) {
		return result, nil
	}
	captureRun, err := r.RunPendingTurnCaptureReceipts(ctx, trigger)
	mergeRunResult(&result, captureRun)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *Runtime) runLegacySweep(ctx context.Context, trigger Trigger) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if !r.generationAllowed(trigger) {
		return result, nil
	}
	if r.Extractor == nil {
		return result, fmt.Errorf("memoryruntime: extractor is nil")
	}
	limit := r.Config.MaxSessionsPerRun * 10
	if limit <= 0 {
		limit = 20
	}
	sessions, err := r.Source.ListSessions(ctx, r.Config.Workspace, limit)
	if err != nil {
		return result, err
	}
	result.Sessions = len(sessions)
	processedThisRun := 0
	for _, session := range sessions {
		if processedThisRun >= r.Config.MaxSessionsPerRun {
			break
		}
		ok, _ := r.eligible(ctx, session, trigger)
		if !ok {
			result.Skipped++
			continue
		}
		useDigests, err := r.useDigestWindows(ctx, session)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, session.ID+": "+err.Error())
			continue
		}
		if useDigests {
			processed, err := r.runPendingTurnDigests(ctx, trigger, session.ID)
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors, session.ID+": "+err.Error())
				continue
			}
			mergeRunResult(&result, processed)
			if processed.Processed > 0 {
				processedThisRun++
			}
			continue
		}
		processed, err := r.RunSession(ctx, trigger, session.ID)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, session.ID+": "+err.Error())
			continue
		}
		mergeRunResult(&result, processed)
		if processed.Processed > 0 {
			processedThisRun++
		}
	}
	if err := r.recoverPendingRollups(ctx, trigger, "", nil, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (r *Runtime) RunSession(ctx context.Context, trigger Trigger, sessionID string) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if !r.enabled() {
		return result, nil
	}
	if !r.generationAllowed(trigger) {
		return result, nil
	}
	if r.Source == nil || r.Extractor == nil || r.Store == nil {
		return result, fmt.Errorf("memoryruntime: runtime is not fully configured")
	}
	session, err := r.findSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if captureBacked, err := r.captureBackedSession(ctx, session); err != nil {
		return result, err
	} else if captureBacked {
		result.Skipped = 1
		return result, nil
	}
	ok, reason := r.eligible(ctx, session, trigger)
	if !ok {
		result.Skipped = 1
		if reason == "job_done" {
			result.Skipped = 1
		}
		return result, nil
	}
	transcript, err := r.Source.ReadTranscript(ctx, session, r.Config.MaxSourceMessages)
	if err != nil {
		return result, err
	}
	if transcript.SessionID == "" {
		transcript.SessionID = session.ID
	}
	if transcript.ProjectKey == "" {
		transcript.ProjectKey = r.Config.ProjectKey
	}
	if transcript.WorkspacePath == "" {
		transcript.WorkspacePath = session.WorkspacePath
	}
	if transcript.Hash == "" {
		transcript.Hash = TranscriptHash(transcript.Messages)
	}
	if transcript.Revision <= 0 {
		transcript.Revision = int64(len(transcript.Messages))
	}
	if len(transcript.Messages) == 0 {
		result.Skipped = 1
		return result, nil
	}
	pages, err := planExtractionPages(session, transcript, r.Config)
	if err != nil {
		return result, err
	}
	if len(pages) > 1 {
		return r.runExtractionPages(ctx, trigger, session, transcript, pages, "")
	}
	jobKey := "session:" + strings.TrimSpace(session.ID) + ":" + transcript.Hash
	job, err := r.Store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: jobKey, ProjectKey: r.Config.ProjectKey,
		MaxAttempts: r.Config.MaxAttempts,
	})
	if err != nil {
		return result, err
	}
	now := r.now()
	if job.Status == memorystore.JobStatusSucceeded || job.Status == memorystore.JobStatusFailed {
		result.Skipped = 1
		if job.Status == memorystore.JobStatusSucceeded {
			if job.LastSuccessWatermark != transcript.Hash {
				return result, fmt.Errorf("memoryruntime: job %s has no matching success watermark", job.ID)
			}
			completeTranscriptResult(&result, transcript)
		}
		return result, nil
	}
	if job.Status == memorystore.JobStatusRunning && job.LeaseUntil.After(now) {
		result.Skipped = 1
		return result, nil
	}
	claimed, err := r.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: r.workerID(), Kind: "phase1_extract", JobKey: jobKey,
		ProjectKey: r.Config.ProjectKey, Limit: 1, Now: now, Lease: r.Config.Lease,
	})
	if err != nil {
		return result, err
	}
	if len(claimed) == 0 {
		result.Skipped = 1
		return result, nil
	}
	var extraction ExtractionResult
	err = withJobLeaseHeartbeat(ctx, r.Store, r.Config.Lease, claimed[0],
		func(heartbeatCtx context.Context) error {
			var extractErr error
			extraction, extractErr = r.Extractor.Extract(heartbeatCtx, session, transcript)
			return extractErr
		})
	if err != nil {
		return result, r.failExtractionJob(ctx, claimed[0], err)
	}
	commitCtx, commitCancel := memoryJobCommitContext(ctx)
	defer commitCancel()
	candidates, reasons := r.validateCandidates(extraction, transcript, now)
	result.Skipped += len(reasons)
	result.Ignored = append(result.Ignored, reasons...)
	stale, err := r.Store.MarkSessionSourceChanged(commitCtx, session.ID, r.Config.ProjectKey, transcript.Hash, transcript.Revision)
	if err != nil {
		return result, r.failExtractionJob(ctx, claimed[0], err)
	}
	result.Stale += stale
	if len(candidates) == 0 {
		if err := r.Store.CompleteJobForWorker(commitCtx, claimed[0].ID, claimed[0].WorkerID, transcript.Hash); err != nil {
			return result, err
		}
		completeTranscriptResult(&result, transcript)
		result.Processed = 1
		return result, nil
	}
	inserted := 0
	for _, candidate := range candidates {
		if inserted >= r.Config.MaxCandidatesPerSession {
			break
		}
		if _, err := r.Store.InsertCandidate(commitCtx, candidate); err != nil {
			if isCandidateSkipError(err) {
				result.Skipped++
				continue
			}
			return result, r.failExtractionJob(ctx, claimed[0], err)
		}
		inserted++
	}
	if err := r.Store.CompleteJobForWorker(commitCtx, claimed[0].ID, claimed[0].WorkerID, transcript.Hash); err != nil {
		return result, err
	}
	result.Processed = 1
	result.Candidates = inserted
	completeTranscriptResult(&result, transcript)
	return result, nil
}

// RunSessionWindow extracts an entire canonical range through bounded,
// contiguous subwindows. Each subwindow has its own durable job, so a restart
// skips already-persisted work but cannot acknowledge a partly extracted range.
func (r *Runtime) RunSessionWindow(ctx context.Context, trigger Trigger, sessionID string, startSeq, endSeq int64) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if !r.enabled() || !r.generationAllowed(trigger) {
		return result, nil
	}
	if r.Source == nil || r.Extractor == nil || r.Store == nil {
		return result, fmt.Errorf("memoryruntime: runtime is not fully configured")
	}
	session, err := r.findSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if captureBacked, err := r.captureBackedSession(ctx, session); err != nil {
		return result, err
	} else if captureBacked {
		result.Skipped = 1
		return result, nil
	}
	if ok, _ := r.eligible(ctx, session, trigger); !ok {
		result.Skipped = 1
		return result, nil
	}
	windowSource, ok := r.Source.(TranscriptWindowSource)
	if !ok {
		result.Skipped = 1
		return result, nil
	}
	if startSeq <= 0 {
		startSeq = 1
	}
	if endSeq < startSeq {
		return result, fmt.Errorf("memoryruntime: invalid window %d-%d", startSeq, endSeq)
	}
	limit := int64(r.Config.MaxSourceMessages)
	if limit <= 0 {
		limit = 200
	}
	h := sha256.New()
	for first := startSeq; first <= endSeq; {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		last := endSeq
		if endSeq-first >= limit {
			last = first + limit - 1
		}
		part, err := r.runSessionWindowPart(ctx, trigger, session, windowSource, first, last)
		mergeRunResult(&result, part)
		if err != nil || !part.Completed {
			return result, err
		}
		_, _ = fmt.Fprintf(h, "%d-%d:%s\n", first, last, part.SourceHash)
		if last == endSeq {
			result.Completed = true
			result.SourceStartSeq, result.SourceEndSeq = startSeq, endSeq
			result.SourceHash = part.SourceHash
			if first != startSeq {
				result.SourceHash = hex.EncodeToString(h.Sum(nil))
			}
			break
		}
		first = last + 1
	}
	return result, nil
}

// captureBackedSession is the routing fence between the canonical receipt
// path and historical session extraction. The memory-side check covers rows
// that have already crossed the SQLite boundary; the canonical-side check
// covers receipts that are still captured/deferred and have not materialized.
func (r *Runtime) captureBackedSession(ctx context.Context, session SessionRef) (bool, error) {
	if r == nil || r.Store == nil || strings.TrimSpace(session.ID) == "" {
		return false, nil
	}
	if backed, err := r.Store.HasTurnCaptureDigestsForSessionGeneration(
		ctx, session.ID, session.SessionGeneration,
	); err != nil {
		return false, err
	} else if backed {
		return true, nil
	}
	if source, ok := r.Source.(SessionTurnCaptureReceiptGenerationSource); ok {
		return source.HasTurnCaptureReceiptsForSessionGeneration(
			ctx, session.ID, session.SessionGeneration,
		)
	}
	if source, ok := r.Source.(SessionTurnCaptureReceiptSource); ok {
		// A source that cannot scope receipts by generation cannot prove that
		// a reused session ID is safe for legacy extraction. Fail closed.
		return source.HasTurnCaptureReceiptsForSession(ctx, session.ID)
	}
	if _, receiptSource := r.Source.(TurnCaptureReceiptSource); receiptSource {
		// Receipt-aware sources without a scoped existence query cannot prove
		// that session-wide replay is safe. Fail closed instead of guessing.
		return true, nil
	}
	return false, nil
}

func (r *Runtime) runSessionWindowPart(ctx context.Context, trigger Trigger, session SessionRef, windowSource TranscriptWindowSource, startSeq, endSeq int64) (RunResult, error) {
	return r.runSessionWindowPartWithJob(ctx, trigger, session, windowSource, startSeq, endSeq, "", "")
}

func (r *Runtime) runSessionWindowPartWithJob(ctx context.Context, trigger Trigger, session SessionRef, windowSource TranscriptWindowSource, startSeq, endSeq int64, jobKind, jobKey string) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	transcript, err := windowSource.ReadTranscriptWindow(ctx, session, int(startSeq), int(endSeq), r.Config.MaxSourceMessages)
	if err != nil {
		return result, err
	}
	if err := verifyTranscriptCoverage(transcript, startSeq, endSeq); err != nil {
		return result, err
	}
	if transcript.SessionID == "" {
		transcript.SessionID = session.ID
	}
	if transcript.ProjectKey == "" {
		transcript.ProjectKey = r.Config.ProjectKey
	}
	if transcript.WorkspacePath == "" {
		transcript.WorkspacePath = session.WorkspacePath
	}
	if transcript.Hash == "" {
		transcript.Hash = TranscriptHash(transcript.Messages)
	}
	if transcript.Revision <= 0 {
		transcript.Revision = endSeq
	}
	pages, err := planExtractionPages(session, transcript, r.Config)
	if err != nil {
		return result, err
	}
	if len(pages) > 1 {
		return r.runExtractionPages(ctx, trigger, session, transcript, pages, fmt.Sprintf("%d-%d", startSeq, endSeq))
	}
	if strings.TrimSpace(jobKind) == "" {
		jobKind = "phase1_extract"
	}
	if strings.TrimSpace(jobKey) == "" {
		jobKey = fmt.Sprintf("session:%s:window:%d-%d:%s", strings.TrimSpace(session.ID), startSeq, endSeq, transcript.Hash)
	}
	job, err := r.Store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: jobKind, JobKey: jobKey, ProjectKey: r.Config.ProjectKey,
		MaxAttempts: r.Config.MaxAttempts,
	})
	if err != nil {
		return result, err
	}
	now := r.now()
	if job.Status == memorystore.JobStatusSucceeded || job.Status == memorystore.JobStatusFailed {
		result.Skipped = 1
		if job.Status == memorystore.JobStatusSucceeded {
			if job.LastSuccessWatermark != transcript.Hash {
				return result, fmt.Errorf("memoryruntime: job %s has no matching success watermark", job.ID)
			}
			completeTranscriptResult(&result, transcript)
		}
		return result, nil
	}
	if job.Status == memorystore.JobStatusRunning && job.LeaseUntil.After(now) {
		result.Skipped = 1
		return result, nil
	}
	claimed, err := r.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: r.workerID(), Kind: jobKind, JobKey: jobKey,
		ProjectKey: r.Config.ProjectKey, Limit: 1, Now: now, Lease: r.Config.Lease,
	})
	if err != nil {
		return result, err
	}
	if len(claimed) == 0 {
		result.Skipped = 1
		return result, nil
	}
	var extraction ExtractionResult
	err = withJobLeaseHeartbeat(ctx, r.Store, r.Config.Lease, claimed[0],
		func(heartbeatCtx context.Context) error {
			var extractErr error
			extraction, extractErr = r.Extractor.Extract(heartbeatCtx, session, transcript)
			return extractErr
		})
	if err != nil {
		return result, r.failExtractionJob(ctx, claimed[0], err)
	}
	commitCtx, commitCancel := memoryJobCommitContext(ctx)
	defer commitCancel()
	candidates, reasons := r.validateCandidates(extraction, transcript, now)
	result.Skipped += len(reasons)
	result.Ignored = append(result.Ignored, reasons...)
	windowID := fmt.Sprintf("%d-%d", startSeq, endSeq)
	for index := range candidates {
		candidates[index].SourceWindow = windowID
	}
	stale, err := r.Store.MarkWindowSourceChanged(commitCtx, session.ID, r.Config.ProjectKey, windowID, transcript.Hash, transcript.Revision)
	if err != nil {
		return result, r.failExtractionJob(ctx, claimed[0], err)
	}
	result.Stale += stale
	if len(candidates) == 0 {
		if err := r.Store.CompleteJobForWorker(commitCtx, claimed[0].ID, claimed[0].WorkerID, transcript.Hash); err != nil {
			return result, err
		}
		completeTranscriptResult(&result, transcript)
		result.Processed = 1
		return result, nil
	}
	inserted := 0
	for _, candidate := range candidates {
		if inserted >= r.Config.MaxCandidatesPerSession {
			break
		}
		if _, err := r.Store.InsertCandidate(commitCtx, candidate); err != nil {
			if isCandidateSkipError(err) {
				result.Skipped++
				continue
			}
			return result, r.failExtractionJob(ctx, claimed[0], err)
		}
		inserted++
	}
	if err := r.Store.CompleteJobForWorker(commitCtx, claimed[0].ID, claimed[0].WorkerID, transcript.Hash); err != nil {
		return result, err
	}
	result.Processed = 1
	result.Candidates = inserted
	completeTranscriptResult(&result, transcript)
	return result, nil
}

func (r *Runtime) findSession(ctx context.Context, sessionID string) (SessionRef, error) {
	if r.Source == nil {
		return SessionRef{}, fmt.Errorf("memoryruntime: session source is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionRef{}, fmt.Errorf("memoryruntime: session id is required")
	}
	if source, ok := r.Source.(SessionIdentitySource); ok {
		identity, err := source.ReadSessionIdentity(ctx, sessionID)
		if err != nil {
			return SessionRef{}, err
		}
		if identity.Session.ID != sessionID {
			return SessionRef{}, fmt.Errorf("memoryruntime: session identity mismatch")
		}
		return identity.Session, nil
	}
	sessions, err := r.Source.ListSessions(ctx, r.Config.Workspace, 200)
	if err != nil {
		return SessionRef{}, err
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return session, nil
		}
	}
	return SessionRef{}, fmt.Errorf("memoryruntime: session %s not found", sessionID)
}

func (r *Runtime) eligible(ctx context.Context, session SessionRef, trigger Trigger) (bool, string) {
	if !r.enabled() {
		return false, "mode_off"
	}
	if session.Ephemeral {
		return false, "ephemeral"
	}
	if session.IsSubagent && !r.Config.IncludeSubagents {
		return false, "subagent"
	}
	if r.Modes != nil {
		mode, err := r.Modes.GetSessionMode(ctx, session.ID)
		if err != nil {
			return false, "session_mode_error"
		}
		if mode == memorystore.SessionModeDisabled || mode == memorystore.SessionModePolluted {
			return false, "session_mode_" + string(mode)
		}
	}
	if r.Config.Workspace != "" {
		if strings.TrimSpace(session.WorkspacePath) == "" {
			if r.Config.RequireWorkspaceMatch {
				return false, "workspace_missing"
			}
		} else if !sameWorkspace(r.Config.Workspace, session.WorkspacePath) {
			return false, "workspace_mismatch"
		}
	}
	now := r.now()
	activity := session.LastActivityAt
	if activity.IsZero() {
		activity = session.UpdatedAt
	}
	if !activity.IsZero() && now.Sub(activity) > r.Config.MaxRolloutAge {
		return false, "rollout_age"
	}
	if trigger == TriggerIdle || trigger == TriggerBoth {
		if !activity.IsZero() && now.Sub(activity) < r.Config.MinIdle {
			return false, "not_idle"
		}
	}
	return true, ""
}

func (r *Runtime) validateCandidates(extraction ExtractionResult, transcript Transcript, now time.Time) ([]memorystore.Candidate, []string) {
	allowed := map[string]struct{}{
		"preference": {}, "decision": {}, "fact": {}, "workflow": {},
		"pitfall": {}, "reference": {}, "open_question": {}, "correction": {},
	}
	validMessageIDs := make(map[string]struct{}, len(transcript.Messages))
	for _, message := range transcript.Messages {
		if strings.TrimSpace(message.ID) != "" {
			validMessageIDs[message.ID] = struct{}{}
		}
	}
	out := make([]memorystore.Candidate, 0, len(extraction.Candidates))
	reasons := make([]string, 0)
	for _, candidate := range extraction.Candidates {
		if len(out) >= r.Config.MaxCandidatesPerSession {
			break
		}
		kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
		if _, ok := allowed[kind]; !ok {
			reasons = append(reasons, "kind")
			continue
		}
		text := strings.TrimSpace(candidate.Text)
		if text == "" || utf8.RuneCountInString(text) > r.Config.MaxCandidateTextRunes {
			reasons = append(reasons, "text")
			continue
		}
		if r.Config.RedactionEnabled {
			if rule := DetectSecret(text); rule != "" {
				if r.Config.RedactionDropOnSecret {
					reasons = append(reasons, "secret:"+rule)
					continue
				}
				redacted, _ := RedactForPrompt(text)
				text = strings.TrimSpace(redacted)
				if text == "" {
					reasons = append(reasons, "secret:"+rule)
					continue
				}
			}
		}
		confidence := candidate.Confidence
		if confidence < 0 || confidence > 1 {
			reasons = append(reasons, "confidence")
			continue
		}
		sourceIDs := make([]string, 0, len(candidate.SourceMessageIDs))
		for _, id := range candidate.SourceMessageIDs {
			id = strings.TrimSpace(id)
			if _, ok := validMessageIDs[id]; ok {
				sourceIDs = append(sourceIDs, id)
			}
		}
		if len(sourceIDs) == 0 {
			reasons = append(reasons, "source")
			continue
		}
		tags := normalizeTags(candidate.Tags)
		if len(tags) > r.Config.MaxTagsPerCandidate {
			tags = tags[:r.Config.MaxTagsPerCandidate]
		}
		out = append(out, memorystore.Candidate{
			SessionID: transcript.SessionID, ProjectKey: transcript.ProjectKey,
			Kind: kind, Text: text, Tags: tags, Confidence: confidence,
			Sensitivity: firstNonEmpty(candidate.Sensitivity, "internal"),
			SourceHash:  transcript.Hash, SourceRevision: transcript.Revision,
			SourceUpdatedAt: transcript.UpdatedAt, SourceMessageIDs: sourceIDs,
			RetentionClass:  r.retentionClassForKind(kind),
			RevalidateAfter: r.revalidateAfterForKind(kind, now),
			Model:           firstNonEmpty(extraction.Model, r.Config.ExtractionModel),
			TokenUsage: map[string]interface{}{
				"prompt_tokens":     extraction.Usage.PromptTokens,
				"completion_tokens": extraction.Usage.CompletionTokens,
				"total_tokens":      extraction.Usage.TotalTokens,
			},
		})
	}
	return out, reasons
}

func (r *Runtime) workerID() string {
	if strings.TrimSpace(r.WorkerID) != "" {
		return strings.TrimSpace(r.WorkerID)
	}
	return "memoryruntime"
}

func mergeRunResult(dst *RunResult, src RunResult) {
	dst.Processed += src.Processed
	dst.Skipped += src.Skipped
	dst.Candidates += src.Candidates
	dst.Stale += src.Stale
	dst.Failed += src.Failed
	dst.RolledUp += src.RolledUp
	dst.RollupFailed += src.RollupFailed
	dst.Ignored = append(dst.Ignored, src.Ignored...)
	dst.Errors = append(dst.Errors, src.Errors...)
}

func isCandidateSkipError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "suppressed") ||
		strings.Contains(text, "duplicate memory") ||
		strings.Contains(text, "secret-shaped content")
}

func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	wait := 30 * time.Second
	for i := 1; i < attempt && wait < 30*time.Minute; i++ {
		wait *= 2
	}
	if wait > 30*time.Minute {
		wait = 30 * time.Minute
	}
	return wait
}

func (r *Runtime) retentionClassForKind(kind string) string {
	if r != nil && r.Config.RetentionClasses.Configured() {
		return r.Config.RetentionClasses.ClassForKind(kind)
	}
	switch kind {
	case "preference", "decision", "correction":
		return "permanent"
	case "task_trace":
		return "ephemeral"
	default:
		return "standard"
	}
}

func (r *Runtime) revalidateAfterForKind(kind string, now time.Time) time.Time {
	if r != nil && r.Config.FreshnessDisabled {
		return time.Time{}
	}
	if r != nil && len(r.Config.FreshnessPerKind) > 0 {
		if ttl, ok := r.Config.FreshnessPerKind[strings.ToLower(strings.TrimSpace(kind))]; ok {
			if ttl <= 0 {
				return time.Time{}
			}
			return now.Add(ttl)
		}
		if r.Config.FreshnessDefaultTTL > 0 {
			return now.Add(r.Config.FreshnessDefaultTTL)
		}
	}
	switch kind {
	case "preference", "decision", "correction":
		return time.Time{}
	case "workflow", "pitfall":
		return now.Add(180 * 24 * time.Hour)
	case "fact", "reference":
		return now.Add(30 * 24 * time.Hour)
	case "open_question":
		return now.Add(14 * 24 * time.Hour)
	default:
		return now.Add(90 * 24 * time.Hour)
	}
}

func normalizeTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sameWorkspace(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(strings.TrimSpace(left))
	rightAbs, rightErr := filepath.Abs(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func TranscriptHash(messages []TranscriptMessage) string {
	sorted := append([]TranscriptMessage(nil), messages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	h := sha256.New()
	for _, message := range sorted {
		_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00",
			message.Seq, message.ID, message.Role, message.ToolName, message.ToolCallID, message.Content)
		if message.FragmentTotal > 0 {
			_, _ = fmt.Fprintf(h, "fragment:%d:%d\x00", message.FragmentOffset, message.FragmentTotal)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
