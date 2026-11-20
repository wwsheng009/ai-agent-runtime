package memoryruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// RecoverPendingPhase1 retries pre-receipt phase1_extract jobs stranded by a
// process exit or lease loss. Receipt-backed sessions belong exclusively to
// the v2 capture worker and are skipped even if an old phase1 job remains.
// Recovery is intentionally independent of generation.trigger.
//
// Recovery is bounded by MaxSessionsPerRun. Active sessions still need to pass
// the idle eligibility gate; terminal sessions are retried immediately.
func (r *Runtime) RecoverPendingPhase1(ctx context.Context, trigger Trigger) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	if r == nil || r.Store == nil || r.Source == nil || r.Extractor == nil ||
		!r.enabled() || r.Config.GenerationDisabled {
		return result, nil
	}
	jobs, err := r.Store.ListJobs(ctx, 500)
	if err != nil {
		return result, err
	}
	maxSessions := r.Config.MaxSessionsPerRun
	if maxSessions <= 0 {
		maxSessions = 2
	}
	seen := make(map[string]struct{})
	processed := 0
	for _, job := range jobs {
		if job.Kind != "phase1_extract" || job.Status != memorystore.JobStatusPending {
			continue
		}
		sessionID := phase1JobSessionID(job.JobKey)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		if processed >= maxSessions {
			break
		}
		session, findErr := r.findSession(ctx, sessionID)
		if findErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, sessionID+": recover pending phase1: "+findErr.Error())
			continue
		}
		captureBacked, captureErr := r.captureBackedSession(ctx, session)
		if captureErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, sessionID+": recover pending phase1: "+captureErr.Error())
			continue
		}
		if captureBacked {
			result.Skipped++
			continue
		}
		eligibilityTrigger := TriggerIdle
		if isTerminalSessionState(session.State) {
			eligibilityTrigger = TriggerSessionEnd
		}
		if ok, reason := r.eligible(ctx, session, eligibilityTrigger); !ok {
			result.Skipped++
			if reason == "session_mode_error" {
				result.Errors = append(result.Errors, sessionID+": recover pending phase1: "+reason)
			}
			continue
		}
		run, runErr := r.RunSession(ctx, eligibilityTrigger, sessionID)
		mergeRunResult(&result, run)
		if runErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, sessionID+": recover pending phase1: "+runErr.Error())
			continue
		}
		if run.Completed && strings.TrimSpace(run.SourceHash) != "" {
			if _, cleanErr := r.Store.CancelStaleBaseExtractionJobs(ctx, sessionID, run.SourceHash, "superseded_by_transcript_hash"); cleanErr != nil {
				result.Errors = append(result.Errors, sessionID+": cancel stale base extraction job: "+cleanErr.Error())
			}
		}
		if run.Processed > 0 || run.Skipped > 0 {
			processed++
		}
	}
	return result, recoverPendingPhase1Error(result)
}

// phase1JobSessionID extracts the session id from the stable job-key prefixes
// used by the base, window, and byte-page extraction paths:
//
//	session:<session-id>:<hash>
//	session:<session-id>:window:<start>-<end>:<hash>
//	session:<session-id>:byte-pages-v1:<window>:<source-hash>:<page>:<page-hash>
func phase1JobSessionID(jobKey string) string {
	jobKey = strings.TrimSpace(jobKey)
	if !strings.HasPrefix(jobKey, "session:") {
		return ""
	}
	rest := strings.TrimPrefix(jobKey, "session:")
	if index := strings.IndexByte(rest, ':'); index > 0 {
		return strings.TrimSpace(rest[:index])
	}
	return ""
}

// recoverPendingPhase1Error reports a partial recovery failure while keeping
// the per-session errors available to callers.
func recoverPendingPhase1Error(result RunResult) error {
	if result.Failed == 0 {
		return nil
	}
	return fmt.Errorf("memoryruntime: pending phase1 recovery failed for %d session(s)", result.Failed)
}
