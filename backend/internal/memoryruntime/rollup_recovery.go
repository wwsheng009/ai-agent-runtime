package memoryruntime

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func (r *Runtime) pendingDigestSessions(ctx context.Context, onlySession string, limit int) ([]memorystore.TurnDigestSession, error) {
	onlySession = strings.TrimSpace(onlySession)
	if onlySession == "" {
		return r.Store.ListPendingTurnDigestSessions(ctx, r.Config.ProjectKey, limit)
	}
	// Apply a targeted session filter before limiting, rather than searching a
	// global first page which can be filled by unrelated (or ineligible) work.
	readLimit := r.Config.TurnDigestMaxPendingPerSession
	if readLimit < r.Config.TurnDigestExtractEveryNTurns {
		readLimit = r.Config.TurnDigestExtractEveryNTurns
	}
	digests, err := r.Store.ListPendingTurnDigests(ctx, onlySession, readLimit)
	if err != nil || len(digests) == 0 {
		return nil, err
	}
	return []memorystore.TurnDigestSession{{
		ProjectKey: r.Store.ProjectKey(), SessionID: onlySession, Pending: len(digests),
		MinSeq: digests[0].DigestSeq, MaxSeq: digests[len(digests)-1].DigestSeq,
	}}, nil
}

// recoverPendingRollups is independent of extraction discovery: a crash after
// the extraction acknowledgement or a failed summarizer leaves only L1 work.
// Jobs/windows already provide durable idempotency and lease/backoff guards.
func (r *Runtime) recoverPendingRollups(ctx context.Context, trigger Trigger, onlySession string, attempted map[string]struct{}, result *RunResult) error {
	if r == nil || r.Store == nil || !r.enabled() || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
		return nil
	}
	if trigger != TriggerStartup && trigger != TriggerIdle && trigger != TriggerBoth {
		return nil
	}
	if r.Config.TurnDigestWindowSummaryEnabled && r.WindowSummarizer == nil {
		return nil
	}
	r.rollupRecoveryMu.Lock()
	defer r.rollupRecoveryMu.Unlock()

	var sessions []memorystore.TurnDigestSession
	onlySession = strings.TrimSpace(onlySession)
	if onlySession != "" {
		if _, ok := attempted[onlySession]; ok {
			return nil
		}
		digests, err := r.Store.ListTurnDigestsForRollup(ctx, onlySession, r.Config.TurnDigestMaxPendingPerSession)
		if err != nil {
			return err
		}
		if len(digests) > 0 {
			sessions = []memorystore.TurnDigestSession{{
				SessionID: onlySession, Pending: len(digests), MaxSeq: digests[len(digests)-1].DigestSeq,
			}}
		}
	} else {
		limit := r.Config.MaxSessionsPerRun * 10
		if limit <= 0 {
			limit = 20
		}
		if limit > 1000 {
			limit = 1000
		}
		var err error
		sessions, err = r.Store.ListTurnDigestRollupSessions(ctx, r.Config.ProjectKey, r.rollupRecoveryCursor, limit)
		if err != nil {
			return err
		}
		if len(sessions) == 0 && r.rollupRecoveryCursor != "" {
			r.rollupRecoveryCursor = ""
			sessions, err = r.Store.ListTurnDigestRollupSessions(ctx, r.Config.ProjectKey, "", limit)
			if err != nil {
				return err
			}
		}
		if len(sessions) == 0 {
			return nil
		}
	}
	processed := 0
	for _, pending := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if processed >= r.Config.MaxSessionsPerRun {
			break
		}
		if onlySession == "" {
			// Move past blocked and ineligible entries too, so a bounded first
			// page cannot permanently starve later rollup-only sessions.
			r.rollupRecoveryCursor = pending.SessionID
		}
		if _, ok := attempted[pending.SessionID]; ok {
			continue
		}
		session, err := r.findSession(ctx, pending.SessionID)
		if err != nil {
			result.RollupFailed++
			result.Errors = append(result.Errors, pending.SessionID+": rollup recovery: "+err.Error())
			continue
		}
		if ok, _ := r.eligible(ctx, session, TriggerSessionEnd); !ok {
			continue
		}
		if !r.digestRecoveryEligible(trigger, session) {
			continue
		}
		if (trigger == TriggerIdle || trigger == TriggerBoth) && !r.pendingDigestIdleEligible(session, pending.Pending) {
			continue
		}
		processed++
		if !r.Config.TurnDigestWindowSummaryEnabled {
			if _, err := r.Store.MarkTurnDigestsRollupSkipped(ctx, session.ID, pending.MaxSeq); err != nil {
				return err
			}
			continue
		}
		r.applyRollup(ctx, session.ID, r.shouldFinalizeRollup(ctx, session, false), result)
	}
	return nil
}
