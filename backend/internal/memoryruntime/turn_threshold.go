package memoryruntime

import (
	"context"
	"fmt"
	"strings"
)

// RunTurnThreshold processes bounded groups of canonical, materialized turn
// captures whose pending count reached TurnDigestExtractEveryNTurns. It never
// reads a transcript directly; the capture worker validates and reads each
// receipt's frozen canonical range.
//
// The method is intended to run from the maintenance loop, which supplies the
// maintenance lease context. Job identity and generation fences make repeated
// threshold sweeps safe.
func (r *Runtime) RunTurnThreshold(ctx context.Context, maxSessions int) (RunResult, error) {
	result := RunResult{Trigger: TriggerTurnThreshold}
	if r == nil || !r.enabled() || r.Config.GenerationDisabled ||
		r.Config.GenerationTrigger != string(TriggerTurnThreshold) ||
		!r.Config.TurnDigestEnabled || r.Store == nil {
		return result, nil
	}
	if r.Source == nil || r.Extractor == nil {
		return result, fmt.Errorf("memoryruntime: turn-threshold runtime is not fully configured")
	}
	if _, ok := r.Source.(TurnCaptureReceiptSource); !ok {
		return result, fmt.Errorf("memoryruntime: turn-threshold requires canonical capture receipts")
	}
	if _, ok := r.Source.(SessionGenerationMaterializedTurnCaptureReceiptPageSource); !ok {
		return result, fmt.Errorf("memoryruntime: turn-threshold requires generation-scoped receipt pages")
	}
	if maxSessions <= 0 {
		maxSessions = r.Config.MaxSessionsPerRun
	}
	if maxSessions <= 0 {
		maxSessions = 2
	}
	if maxSessions > 100 {
		maxSessions = 100
	}
	threshold := r.Config.TurnDigestExtractEveryNTurns
	if threshold <= 0 {
		threshold = 50
	}
	pending, err := r.Store.ListPendingTurnCaptureSessions(ctx, threshold, maxSessions)
	if err != nil {
		return result, err
	}
	for _, group := range pending {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Sessions++
		if strings.TrimSpace(group.ProjectKey) != r.Store.ProjectKey() ||
			strings.TrimSpace(group.SessionID) == "" || group.SessionGeneration <= 0 {
			result.Skipped++
			continue
		}
		session, err := r.findSession(ctx, group.SessionID)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, "turn_threshold: session identity unavailable")
			continue
		}
		// Reused session IDs must never let stale captures satisfy the active
		// incarnation's threshold.
		if session.SessionGeneration <= 0 || session.SessionGeneration != group.SessionGeneration {
			result.Skipped++
			continue
		}
		if ok, _ := r.eligible(ctx, session, TriggerTurnThreshold); !ok {
			result.Skipped++
			continue
		}
		part, err := r.runPendingTurnCaptureReceiptsForGeneration(
			ctx, TriggerTurnThreshold, group.SessionID, group.SessionGeneration,
		)
		mergeRunResult(&result, part)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, "turn_threshold: capture extraction failed")
			continue
		}
	}
	return result, nil
}
