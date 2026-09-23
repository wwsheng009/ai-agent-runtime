package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LifecycleProjection is the host-neutral input for a child lifecycle
// transition. Hosts call ProjectLifecycle after their normal spawn/completion
// bookkeeping succeeds; the projection is deliberately best-effort and never
// changes the outcome of the child operation.
type LifecycleProjection struct {
	RootScopeID           string
	TargetParentSessionID string
	TargetParentTeamID    string
	SubjectKind           SubjectKind
	SubjectID             string
	SubjectVersion        int64
	EventType             string
	Severity              Severity
	SupervisionState      SupervisionState
	Reason                string
	RecommendedAction     string
	AllowedActions        []string
	ResolutionState       ResolutionState
	// TurnID names the parent turn that must be resumed when this projection
	// schedules an auto-wake (plan C2-1 / I3: 通知携带 turn_id，恢复同一
	// turn). It is only a hint — delivery re-derives the authoritative turn id
	// from the obligation ledger — so an empty value keeps the legacy
	// "new turn" wake.
	TurnID string
}

// ProjectLifecycle stores a durable parent/lead notification for an abnormal
// lifecycle transition. Healthy/running child starts are represented by the
// descendant provider and intentionally do not create inbox noise. Critical
// transitions also create a durable wake request, so a busy parent cannot
// lose the next-turn preflight.
func ProjectLifecycle(ctx context.Context, store Store, wakes *WakeScheduler, event LifecycleProjection) (Notification, error) {
	if store == nil {
		return Notification{}, fmt.Errorf("supervision store is required")
	}
	event.RootScopeID = strings.TrimSpace(event.RootScopeID)
	event.TargetParentSessionID = strings.TrimSpace(event.TargetParentSessionID)
	event.TargetParentTeamID = strings.TrimSpace(event.TargetParentTeamID)
	event.SubjectID = strings.TrimSpace(event.SubjectID)
	event.EventType = strings.TrimSpace(event.EventType)
	if event.RootScopeID == "" {
		return Notification{}, fmt.Errorf("supervision: root scope id is required")
	}
	if event.SubjectKind == "" || event.SubjectID == "" || event.EventType == "" {
		return Notification{}, fmt.Errorf("supervision: subject kind, subject id and event type are required")
	}
	if event.SubjectVersion <= 0 {
		event.SubjectVersion = 1
	}
	if event.Severity == "" {
		event.Severity = SeverityWarning
	}
	if event.SupervisionState == "" {
		event.SupervisionState = SupervisionBlocked
	}
	if event.ResolutionState == "" {
		event.ResolutionState = ResolutionUnresolved
	}
	notification, err := store.UpsertNotification(ctx, Notification{
		RootScopeID:           event.RootScopeID,
		TargetParentSessionID: event.TargetParentSessionID,
		TargetParentTeamID:    event.TargetParentTeamID,
		SubjectKind:           event.SubjectKind,
		SubjectID:             event.SubjectID,
		SubjectVersion:        event.SubjectVersion,
		// The store allocates a root-scope monotonic sequence for projections
		// that arrive without their own source-event cursor.
		EventSeq:          0,
		EventType:         event.EventType,
		Severity:          event.Severity,
		SupervisionState:  event.SupervisionState,
		Reason:            strings.TrimSpace(event.Reason),
		RecommendedAction: strings.TrimSpace(event.RecommendedAction),
		AllowedActions:    append([]string(nil), event.AllowedActions...),
		// Leave decision state empty here. The store initializes new rows as
		// unacknowledged, while an idempotent replay must preserve an existing
		// acknowledgement/action instead of reopening the parent decision.
		DecisionState:   "",
		ResolutionState: event.ResolutionState,
	})
	if err != nil {
		return Notification{}, fmt.Errorf("supervision: persist lifecycle projection: %w", err)
	}
	// Only a critical transition that still needs a parent decision may
	// schedule an auto turn. Idempotent terminal replay preserves an existing
	// acknowledgement/action in UpsertNotification; scheduling solely from
	// severity+resolution would otherwise wake the parent again on every
	// restart even though the durable decision is already complete.
	if wakes != nil && notification.Severity == SeverityCritical && notification.ActionRequired() {
		_, err := wakes.ScheduleWake(ctx, WakeRequest{
			RootScopeID:           notification.RootScopeID,
			TargetParentSessionID: notification.TargetParentSessionID,
			TargetParentTeamID:    notification.TargetParentTeamID,
			WakeReason:            notification.EventType,
			NotificationSeq:       notification.EventSeq,
			TurnID:                strings.TrimSpace(event.TurnID),
			// C2-4 (#15) / B4: the notification identity feeds the notify key.
			// notification.EventSeq is the store-allocated cursor of this
			// subject+version+event-type, so an idempotent replay of the same
			// projection keeps the same key and is delivered only once
			// (AC-P1-4a), while a genuinely new event for the same subject
			// advances the seq and wakes again.
			ObligationID: notification.SubjectID,
			EventKind:    lifecycleWakeEventKind(notification.SupervisionState),
			EventSeq:     notification.EventSeq,
		})
		if err != nil {
			return notification, fmt.Errorf("supervision: schedule lifecycle wake: %w", err)
		}
	}
	return notification, nil
}

// lifecycleWakeEventKind maps a projected supervision state to the notify-key
// family (plan C2-4 #15 / B4, doc 6.6). A terminal state dedups by
// terminal_epoch — its notification seq, which the store only advances on a
// new event — while everything else stays in the coalescing lifecycle family.
func lifecycleWakeEventKind(state SupervisionState) string {
	switch state {
	case SupervisionTimedOut, SupervisionOrphaned:
		return WakeEventTerminal
	default:
		return WakeEventLifecycle
	}
}

// ProjectAgentCompletion translates a child terminal runtime event into the
// durable parent lifecycle inbox. Successful completions are retained as
// resolved lifecycle records so snapshot/digest cursors remain complete but
// they never demand a supervision decision.
func ProjectAgentCompletion(ctx context.Context, store Store, wakes *WakeScheduler, rootScopeID, parentSessionID, childSessionID, status, sourceEventType string) (Notification, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	sourceEventType = strings.TrimSpace(sourceEventType)
	if sourceEventType == "" {
		sourceEventType = "session_completed"
	}
	state := SupervisionTerminated
	severity := SeverityInfo
	recommended := string(ActionInspect)
	reason := "child session completed with status " + status
	eventType := "agent_" + sourceEventType
	resolution := ResolutionClosed
	switch status {
	case "failed", "error":
		severity = SeverityCritical
		recommended = string(ActionCancel)
		reason = "child session failed"
		eventType = "agent_failed"
		resolution = ResolutionUnresolved
	case "stopped", "interrupted", "canceled", "cancelled":
		severity = SeverityWarning
		reason = "child session interrupted"
		eventType = "agent_interrupted"
	default:
		eventType = "agent_completed"
	}
	// A terminal child must also terminalize its execution-run registration.
	// A spawn_agent run left in a live status keeps firing progress_stalled /
	// execution_timed_out notifications at the parent for work that already
	// finished, so the durable run has to follow the child to a terminal
	// state here. Best-effort: the lifecycle notification is still projected
	// when the store does not persist runs or the run write fails.
	finalizeErr := finalizeChildExecutionRuns(ctx, store, childSessionID, status, time.Now().UTC())
	notification, err := ProjectLifecycle(ctx, store, wakes, LifecycleProjection{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		SubjectKind:           SubjectAgentSession,
		SubjectID:             childSessionID,
		SubjectVersion:        lifecycleVersion(status),
		EventType:             eventType,
		Severity:              severity,
		SupervisionState:      state,
		Reason:                reason,
		RecommendedAction:     recommended,
		ResolutionState:       resolution,
	})
	if err != nil {
		return notification, err
	}
	if finalizeErr != nil {
		return notification, finalizeErr
	}
	return notification, nil
}

// finalizeChildExecutionRuns closes the execution-run registrations a host
// opened for a child session (workflow spawn_agent). The store is optional for
// hosts that only use the notification plane, mirroring the alerts/snapshot
// degrading pattern.
func finalizeChildExecutionRuns(ctx context.Context, store Store, childSessionID, childStatus string, now time.Time) error {
	runStore, ok := store.(ExecutionRunStore)
	if !ok {
		return nil
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return nil
	}
	runs, err := runStore.ListExecutionRunsBySession(ctx, childSessionID, childRunFinalizeLimit)
	if err != nil {
		return fmt.Errorf("supervision: list child execution runs: %w", err)
	}
	status := childRunTerminalStatus(childStatus)
	for _, run := range runs {
		if run.Active() {
			if _, err := runStore.MarkExecutionRunTerminal(ctx, run.RunID, status, "", "", now); err != nil {
				return fmt.Errorf("supervision: finalize child execution run %s: %w", run.RunID, err)
			}
		}
		// The child session is terminal, so the live-condition alerts this run
		// projected (progress_stalled and friends) are stale (plan §4.3).
		// Converging already-terminal runs too repairs rows that were written
		// before this path existed; a resolution recorded earlier is never
		// rewritten because ConvergeRunAlerts only touches unresolved rows.
		if _, err := ConvergeRunAlerts(ctx, store, run.RootSessionID, run.RunID, status, now); err != nil {
			return fmt.Errorf("supervision: converge child run alerts %s: %w", run.RunID, err)
		}
	}
	return nil
}

// childRunFinalizeLimit bounds the per-child run lookup; a child session
// normally owns exactly one registration run plus retries.
const childRunFinalizeLimit = 16

// childRunTerminalStatus maps a terminal child session status onto the
// execution-run status vocabulary (doc 5.3).
func childRunTerminalStatus(childStatus string) string {
	switch strings.ToLower(strings.TrimSpace(childStatus)) {
	case "failed", "error":
		return RunStatusFailed
	case "stopped", "interrupted", "canceled", "cancelled":
		return RunStatusCanceled
	default:
		return RunStatusSucceeded
	}
}

func lifecycleVersion(status string) int64 {
	var version int64 = 1
	for _, r := range status {
		version = version*31 + int64(r)
	}
	if version < 0 {
		return -version
	}
	if version == 0 {
		return 1
	}
	return version
}
