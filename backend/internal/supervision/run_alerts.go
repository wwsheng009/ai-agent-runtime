package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// runAlertEventTypes lists the run-scoped decision projections that describe a
// live condition on a run (doc 5.5 non-healthy verdicts). Every one of them is
// stale by definition once the run itself reaches a terminal state, yet nothing
// converged them: the 2026-09-16 audit (plan §4.3) found three `progress_stalled`
// rows still `critical`/`unresolved` hours after their runs had finished, so the
// parent turn kept re-injecting an action-required row for completed work.
var runAlertEventTypes = []string{
	"progress_stalled",
	"execution_timed_out",
	"approval_timeout",
	"cancel_grace_expired",
	"orphan_suspected",
}

// runAlertConvergeLimit bounds the per-run lookup. A run owns at most one row
// per decision event type; the cap only exists so a corrupted subject id cannot
// pull an unbounded list.
const runAlertConvergeLimit = 32

// notificationResolveAttempts bounds the CAS retry in resolveUnresolvedNotification.
const notificationResolveAttempts = 3

// runAlertResolution maps a terminal run status onto the resolution state of the
// run's stale alerts (plan §4.3): a run that succeeded recovered whatever the
// watchdog reported while it was live, while failed / orphaned / timed-out /
// canceled runs ended without success and keep that on the record instead of
// silently looking healthy.
func runAlertResolution(terminalStatus string) ResolutionState {
	if strings.EqualFold(strings.TrimSpace(terminalStatus), RunStatusSucceeded) {
		return ResolutionRecovered
	}
	return ResolutionFailed
}

func isRunAlertEventType(eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	for _, candidate := range runAlertEventTypes {
		if eventType == candidate {
			return true
		}
	}
	return false
}

// ConvergeRunAlerts resolves the still-unresolved decision rows a run projected
// while it was live, so a finished run stops counting as critical/action-required
// in the parent inbox, digest and snapshot (plan §4.3).
//
// This does not contradict "terminal rows are never auto-resolved" (plan §4.1.1):
// the rows converged here are the run's live-condition *alerts*, not a decision
// the parent already acted on. A resolution someone else recorded is never
// rewritten (the store keeps the first resolution, and the CAS below re-reads on
// conflict), and a nil store or blank subject degrades to a no-op so
// notification-only hosts keep their current behaviour.
func ConvergeRunAlerts(ctx context.Context, store Store, rootScopeID, runID, terminalStatus string, at time.Time) (int, error) {
	if store == nil {
		return 0, nil
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	runID = strings.TrimSpace(runID)
	if rootScopeID == "" || runID == "" {
		return 0, nil
	}
	// IncludeResolved stays false: only unresolved rows need a transition.
	rows, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID: rootScopeID,
		SubjectKind: SubjectAgentRun,
		SubjectID:   runID,
		Limit:       runAlertConvergeLimit,
	})
	if err != nil {
		return 0, fmt.Errorf("supervision: list run alerts: %w", err)
	}
	resolution := runAlertResolution(terminalStatus)
	converged := 0
	for _, row := range rows {
		if !isRunAlertEventType(row.EventType) {
			continue
		}
		ok, err := resolveUnresolvedNotification(ctx, store, row, resolution, at)
		if err != nil {
			return converged, fmt.Errorf("supervision: converge run alert %s: %w", row.NotificationID, err)
		}
		if ok {
			converged++
		}
	}
	return converged, nil
}

// resolveUnresolvedNotification resolves one row with a bounded CAS retry.
// Preflight marks rows delivered/seen while the parent works, so a stale version
// is the common case rather than the exception; without the retry a busy parent
// would permanently block the very convergence it depends on. A row someone else
// already resolved is left alone.
func resolveUnresolvedNotification(ctx context.Context, store Store, row Notification, resolution ResolutionState, at time.Time) (bool, error) {
	current := row
	for attempt := 0; attempt < notificationResolveAttempts; attempt++ {
		if current.ResolutionState != "" && current.ResolutionState != ResolutionUnresolved {
			return false, nil
		}
		ok, err := store.ResolveNotification(ctx, current.NotificationID, resolution, at, current.Version)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		latest, err := store.GetNotification(ctx, current.NotificationID)
		if err != nil {
			return false, err
		}
		if latest == nil {
			return false, nil
		}
		current = *latest
	}
	return false, nil
}
