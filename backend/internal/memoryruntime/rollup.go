package memoryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	defaultTurnDigestWindowTurns = 100
	turnWindowRollupJobKind      = "phase1_rollup"
)

// WindowSummaryInput is the bounded L0 input for one L1 summary.
type WindowSummaryInput struct {
	Session        SessionRef
	WindowID       string
	WindowIndex    int
	StartDigestSeq int64
	EndDigestSeq   int64
	Digests        []memorystore.TurnDigest
}

// WindowSummary is the L1 output. Text must be redacted and bounded by the
// caller before it is persisted.
type WindowSummary struct {
	Text  string
	Model string
	Usage TokenUsage
}

// WindowSummarizer folds a consecutive range of completed turn digests into a
// compact L1 summary. It never mutates L0 rows; the runtime handles persistence
// and rollup watermarks.
type WindowSummarizer interface {
	SummarizeWindow(ctx context.Context, input WindowSummaryInput) (WindowSummary, error)
}

// LLMWindowSummarizer is the default L1 summarizer. It reuses the active
// runtime provider but remains outside the turn path: it only runs during
// session-end/idle recovery.
type LLMWindowSummarizer struct {
	Runtime *llm.LLMRuntime
	Config  Config
}

func (s *LLMWindowSummarizer) SummarizeWindow(ctx context.Context, input WindowSummaryInput) (WindowSummary, error) {
	if s == nil || s.Runtime == nil {
		return WindowSummary{}, fmt.Errorf("memoryruntime: LLM runtime is nil")
	}
	if len(input.Digests) == 0 {
		return WindowSummary{}, fmt.Errorf("memoryruntime: window has no turn digests")
	}
	hasText := false
	for _, digest := range input.Digests {
		if strings.TrimSpace(digest.UserText) != "" || strings.TrimSpace(digest.AssistantText) != "" {
			hasText = true
			break
		}
	}
	if !hasText {
		return WindowSummary{}, fmt.Errorf("memoryruntime: turn digest text is unavailable; enable turnDigest.storeText for L1 summaries")
	}
	user := renderWindowSummaryPrompt(input, s.Config)
	response, err := s.Runtime.Call(ctx, &llm.LLMRequest{
		Provider: s.Config.ExtractionProvider,
		Model:    s.Config.ExtractionModel,
		Messages: []types.Message{
			*types.NewSystemMessage(`Summarize completed coding-agent turns into a compact durable session window.
Return plain text only. Preserve decisions, corrections, preferences, open questions, and verified outcomes.
Do not invent facts, do not copy secrets, and do not include transient commands or raw tool output.`),
			*types.NewUserMessage(user),
		},
		MaxTokens:       windowSummaryMaxTokens(s.Config),
		Temperature:     0,
		ReasoningEffort: firstNonEmpty(s.Config.ExtractionReasoningEffort, "low"),
		Metadata: map[string]interface{}{
			"memory_turn_window": true,
			"session_id":         input.Session.ID,
			"window_id":          input.WindowID,
		},
	})
	if err != nil {
		return WindowSummary{}, err
	}
	text := strings.TrimSpace(response.Content)
	if text == "" {
		return WindowSummary{}, fmt.Errorf("memoryruntime: empty window summary response")
	}
	result := WindowSummary{Text: text}
	if response.Usage != nil {
		result.Usage = TokenUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}
	}
	if response.Model != "" {
		result.Model = response.Model
	}
	return result, nil
}

// RollupTurnDigests folds extraction-done L0 rows into L1 windows. When
// finalize is false only complete windowTurns-sized groups are summarized; a
// partial tail remains pending for a later run or session end.
func (r *Runtime) RollupTurnDigests(ctx context.Context, sessionID string, finalize bool) (RollupResult, error) {
	result := RollupResult{}
	if r == nil || r.Store == nil || !r.enabled() || r.Config.GenerationDisabled ||
		!r.Config.TurnDigestEnabled || !r.Config.TurnDigestWindowSummaryEnabled {
		return result, nil
	}
	if r.WindowSummarizer == nil {
		return result, nil
	}
	session, err := r.findSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	limit := r.Config.TurnDigestMaxPendingPerSession
	if limit <= 0 {
		limit = 200
	}
	if configured := r.Config.TurnDigestMaxDigestsPerRun; configured > 0 && configured < limit {
		limit = configured
	}
	digests, err := r.Store.ListTurnDigestsForRollup(ctx, session.ID, limit)
	if err != nil {
		return result, err
	}
	groups := selectTurnWindowGroups(digests, r.Config.TurnDigestWindowTurns, finalize)
	if len(groups) == 0 {
		return result, nil
	}
	windowIndex, _ := r.Store.CountTurnWindows(ctx, session.ID)
	for _, group := range groups {
		if ctx.Err() != nil {
			break
		}
		result.Examined++
		windowIndex++
		groupResult := r.rollupOneWindow(ctx, session, group, windowIndex)
		result.RolledUp += groupResult.RolledUp
		result.Skipped += groupResult.Skipped
		result.Failed += groupResult.Failed
		result.Errors = append(result.Errors, groupResult.Errors...)
	}
	return result, nil
}

func (r *Runtime) rollupOneWindow(ctx context.Context, session SessionRef, digests []memorystore.TurnDigest, windowIndex int) RollupResult {
	result := RollupResult{}
	if len(digests) == 0 {
		return result
	}
	startDigestSeq := digests[0].DigestSeq
	endDigestSeq := digests[len(digests)-1].DigestSeq
	if startDigestSeq <= 0 || endDigestSeq < startDigestSeq {
		result.Skipped++
		return result
	}
	sourceHash := memorystore.TurnWindowHash(digests)
	sourceWindow := memorystore.TurnWindowSourceWindow(startDigestSeq, endDigestSeq)
	startMessageSeq := int64(1)
	if previousEnd, err := r.Store.PreviousTurnDigestMessageSeqEnd(ctx, session.ID, startDigestSeq); err == nil && previousEnd > 0 {
		startMessageSeq = previousEnd + 1
	}
	endMessageSeq := int64(0)
	for i := len(digests) - 1; i >= 0; i-- {
		if digests[i].MessageSeqEnd > 0 {
			endMessageSeq = digests[i].MessageSeqEnd
			break
		}
	}
	window, sourceChanged, err := r.Store.EnsureTurnWindow(ctx, memorystore.TurnWindow{
		Scope:           memorystore.ScopeProject,
		ProjectKey:      r.Config.ProjectKey,
		SessionID:       session.ID,
		WindowIndex:     windowIndex,
		StartDigestSeq:  startDigestSeq,
		EndDigestSeq:    endDigestSeq,
		StartMessageSeq: startMessageSeq,
		EndMessageSeq:   endMessageSeq,
		TurnCount:       len(digests),
		SourceHash:      sourceHash,
		SourceRevision:  endDigestSeq,
		Status:          memorystore.TurnWindowPending,
	})
	if err != nil {
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	if sourceChanged {
		_, _ = r.Store.MarkTurnWindowCandidatesStale(ctx, session.ID, sourceWindow, "turn_window_source_changed")
	}
	if window.Status == memorystore.TurnWindowInvalidated {
		result.Skipped++
		return result
	}
	jobKey := fmt.Sprintf("session:%s:rollup:%d-%d:%s", session.ID, startDigestSeq, endDigestSeq, sourceHash)
	job, err := r.Store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: turnWindowRollupJobKind, JobKey: jobKey, ProjectKey: r.Config.ProjectKey,
		MaxAttempts: r.Config.MaxAttempts,
	})
	if err != nil {
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	now := r.now()
	switch {
	case job.Status == memorystore.JobStatusSucceeded:
		if window.Status == memorystore.TurnWindowDone {
			if _, err := r.finalizeTurnWindowRollup(ctx, session, window, digests); err != nil {
				result.Failed++
				result.Errors = append(result.Errors, session.ID+": "+err.Error())
			}
		} else {
			result.Skipped++
		}
		return result
	case job.Status == memorystore.JobStatusFailed:
		result.Failed++
		return result
	case job.Status == memorystore.JobStatusRunning && job.LeaseUntil.After(now):
		result.Skipped++
		return result
	}
	claimed, err := r.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: r.workerID(), Kind: turnWindowRollupJobKind, JobKey: jobKey,
		ProjectKey: r.Config.ProjectKey, Limit: 1, Now: now,
	})
	if err != nil {
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	if len(claimed) == 0 {
		result.Skipped++
		return result
	}
	jobID := claimed[0].ID
	workerID := claimed[0].WorkerID
	if window.Status == memorystore.TurnWindowDone {
		rolled, err := r.finalizeTurnWindowRollup(ctx, session, window, digests)
		if err != nil {
			_ = r.Store.FailJobForWorker(ctx, jobID, workerID, err.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
			result.Failed++
			result.Errors = append(result.Errors, session.ID+": "+err.Error())
			return result
		}
		_ = r.Store.CompleteJobForWorker(ctx, jobID, workerID, window.SourceHash)
		if rolled > 0 {
			result.RolledUp++
		}
		return result
	}
	if err := r.Store.MarkTurnWindowProcessing(ctx, window.ID); err != nil {
		_ = r.Store.FailJobForWorker(ctx, jobID, workerID, err.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	summary, summarizeErr := r.WindowSummarizer.SummarizeWindow(ctx, WindowSummaryInput{
		Session:        session,
		WindowID:       window.ID,
		WindowIndex:    windowIndex,
		StartDigestSeq: startDigestSeq,
		EndDigestSeq:   endDigestSeq,
		Digests:        digests,
	})
	if summarizeErr != nil {
		_ = r.Store.FailTurnWindow(ctx, window.ID, summarizeErr.Error())
		_ = r.Store.FailJobForWorker(ctx, jobID, workerID, summarizeErr.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+summarizeErr.Error())
		return result
	}
	cleanSummary, sanitizeErr := r.sanitizeWindowSummary(summary)
	if sanitizeErr != nil {
		_ = r.Store.FailTurnWindow(ctx, window.ID, sanitizeErr.Error())
		_ = r.Store.FailJobForWorker(ctx, jobID, workerID, sanitizeErr.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+sanitizeErr.Error())
		return result
	}
	metadata := map[string]interface{}{
		"compiler":          "llm_window_summary_v1",
		"window_index":      windowIndex,
		"start_digest_seq":  startDigestSeq,
		"end_digest_seq":    endDigestSeq,
		"start_message_seq": startMessageSeq,
		"end_message_seq":   endMessageSeq,
		"turn_count":        len(digests),
		"source_hash":       sourceHash,
	}
	window, err = r.Store.CompleteTurnWindow(ctx, window.ID, memorystore.TurnWindowSummary{
		Text:         cleanSummary.Text,
		Model:        cleanSummary.Model,
		UsageJSON:    windowUsageJSON(cleanSummary.Usage),
		MetadataJSON: memorystore.EncodeTurnWindowMetadata(metadata),
	})
	if err != nil {
		_ = r.Store.FailJobForWorker(ctx, jobID, workerID, err.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	if _, err := r.finalizeTurnWindowRollup(ctx, session, window, digests); err != nil {
		_ = r.Store.FailJobForWorker(ctx, jobID, workerID, err.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	if err := r.Store.CompleteJobForWorker(ctx, jobID, workerID, window.SourceHash); err != nil {
		result.Failed++
		result.Errors = append(result.Errors, session.ID+": "+err.Error())
		return result
	}
	result.RolledUp++
	return result
}

func (r *Runtime) finalizeTurnWindowRollup(ctx context.Context, session SessionRef, window memorystore.TurnWindow, digests []memorystore.TurnDigest) (int64, error) {
	if err := r.ensureTurnWindowCandidate(ctx, session, window, digests); err != nil {
		return 0, err
	}
	return r.Store.MarkTurnDigestsRolledUp(ctx, session.ID, window.StartDigestSeq, window.EndDigestSeq, window.ID)
}

func (r *Runtime) ensureTurnWindowCandidate(ctx context.Context, session SessionRef, window memorystore.TurnWindow, digests []memorystore.TurnDigest) error {
	text := strings.TrimSpace(window.SummaryText)
	if text == "" {
		return fmt.Errorf("memoryruntime: turn window %s has no summary text", window.ID)
	}
	messageIDs := make([]string, 0, len(digests)*2)
	seen := make(map[string]struct{}, len(digests)*2)
	for _, digest := range digests {
		for _, id := range []string{digest.UserMessageID, digest.AssistantMessageID} {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			messageIDs = append(messageIDs, id)
		}
	}
	now := r.now()
	sourceWindow := memorystore.TurnWindowSourceWindow(window.StartDigestSeq, window.EndDigestSeq)
	_, err := r.Store.InsertCandidate(ctx, memorystore.Candidate{
		SessionID:        session.ID,
		SourceWindow:     sourceWindow,
		SourceKind:       "turn_window",
		SourceRefs:       []string{"session:" + session.ID, "turn_window:" + window.ID},
		ProjectKey:       r.Config.ProjectKey,
		Kind:             "reference",
		Text:             text,
		Tags:             []string{"turn_window", "window_summary"},
		Confidence:       1,
		Sensitivity:      "internal",
		SourceHash:       window.SourceHash,
		SourceRevision:   window.SourceRevision,
		SourceUpdatedAt:  window.ProcessedAt,
		SourceMessageIDs: messageIDs,
		RetentionClass:   r.retentionClassForKind("reference"),
		RevalidateAfter:  r.revalidateAfterForKind("reference", now),
		Model:            window.Model,
		TokenUsage:       decodeUsageJSON(window.UsageJSON),
		Metadata: map[string]interface{}{
			"window_id":        window.ID,
			"window_index":     window.WindowIndex,
			"start_digest_seq": window.StartDigestSeq,
			"end_digest_seq":   window.EndDigestSeq,
			"turn_count":       window.TurnCount,
			"summary_hash":     window.SummaryHash,
			"summary_revision": window.SummaryRevision,
		},
	})
	if err != nil && isCandidateSkipError(err) {
		// The same summary text may already exist in another session, or the
		// content may be suppressed. The L1 row is still valid; do not pin L0
		// forever on an idempotent L2 seed collision.
		return nil
	}
	return err
}

func (r *Runtime) sanitizeWindowSummary(summary WindowSummary) (WindowSummary, error) {
	text := strings.TrimSpace(summary.Text)
	if text == "" {
		return WindowSummary{}, fmt.Errorf("memoryruntime: window summary is empty")
	}
	if r.Config.RedactionEnabled {
		redacted, _ := memoryredact.Redact(text)
		text = strings.TrimSpace(redacted)
		if rule := memoryredact.DetectSecret(text); rule != "" {
			return WindowSummary{}, fmt.Errorf("memoryruntime: window summary contains secret-shaped content (%s)", rule)
		}
	}
	maxRunes := r.Config.SummaryTokenBudget * 4
	if maxRunes < 512 {
		maxRunes = 512
	}
	if maxRunes > 8192 {
		maxRunes = 8192
	}
	text = truncateWindowSummaryText(text, maxRunes)
	return WindowSummary{Text: text, Model: strings.TrimSpace(summary.Model), Usage: summary.Usage}, nil
}

func selectTurnWindowGroups(digests []memorystore.TurnDigest, windowTurns int, finalize bool) [][]memorystore.TurnDigest {
	if len(digests) == 0 {
		return nil
	}
	if windowTurns <= 0 {
		windowTurns = defaultTurnDigestWindowTurns
	}
	groups := make([][]memorystore.TurnDigest, 0)
	for len(digests) > 0 {
		size := windowTurns
		if len(digests) < size {
			if !finalize {
				break
			}
			size = len(digests)
		}
		groups = append(groups, digests[:size])
		digests = digests[size:]
	}
	return groups
}

func isTerminalSessionState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "closed", "archived", "orphaned", "stale":
		return true
	default:
		return false
	}
}

func (r *Runtime) shouldFinalizeRollup(ctx context.Context, session SessionRef, requested bool) bool {
	if r == nil || r.Store == nil {
		return false
	}
	if !requested && !isTerminalSessionState(session.State) {
		return false
	}
	pending, err := r.Store.ListPendingTurnDigests(ctx, session.ID, 1)
	if err != nil {
		// Fail closed: keep full-window behavior rather than creating a partial
		// L1 window while the L0 backlog may still be draining.
		return false
	}
	return len(pending) == 0
}

func windowSummaryMaxTokens(cfg Config) int {
	maxTokens := cfg.SummaryTokenBudget
	if maxTokens <= 0 {
		maxTokens = 800
	}
	if maxTokens < 256 {
		maxTokens = 256
	}
	if maxTokens > 1024 {
		maxTokens = 1024
	}
	return maxTokens
}

func renderWindowSummaryPrompt(input WindowSummaryInput, cfg Config) string {
	var b strings.Builder
	b.WriteString("Session: ")
	b.WriteString(strings.TrimSpace(input.Session.ID))
	b.WriteString("\nWorkspace: ")
	b.WriteString(strings.TrimSpace(input.Session.WorkspacePath))
	b.WriteString(fmt.Sprintf("\nWindow: %s (%d turns, digest %d-%d)\nTurns:\n",
		input.WindowID, len(input.Digests), input.StartDigestSeq, input.EndDigestSeq))
	remaining := cfg.MaxSourceBytes
	if remaining <= 0 {
		remaining = 512 * 1024
	}
	for _, digest := range input.Digests {
		if remaining <= 0 {
			break
		}
		userText, _ := RedactForPrompt(strings.TrimSpace(digest.UserText))
		assistantText, _ := RedactForPrompt(strings.TrimSpace(digest.AssistantText))
		userText = truncateUTF8Bytes(userText, 4096)
		assistantText = truncateUTF8Bytes(assistantText, 8192)
		entry := fmt.Sprintf("[digest %d turn=%s]\nUSER: %s\nASSISTANT: %s\n",
			digest.DigestSeq, digest.TurnID, userText, assistantText)
		if len(entry) > remaining {
			entry = truncateUTF8Bytes(entry, remaining) + "…"
		}
		b.WriteString(entry)
		remaining -= len(entry)
	}
	return b.String()
}

func truncateWindowSummaryText(text string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "…"
}

func windowUsageJSON(usage TokenUsage) string {
	raw, err := json.Marshal(map[string]interface{}{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func decodeUsageJSON(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var usage map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return nil
	}
	return usage
}
