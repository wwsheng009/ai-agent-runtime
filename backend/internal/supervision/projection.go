package supervision

import (
	"context"
	"fmt"
	"strings"
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
	if wakes != nil && notification.Severity == SeverityCritical && notification.ResolutionState == ResolutionUnresolved {
		_, err := wakes.ScheduleWake(ctx, WakeRequest{
			RootScopeID:           notification.RootScopeID,
			TargetParentSessionID: notification.TargetParentSessionID,
			TargetParentTeamID:    notification.TargetParentTeamID,
			WakeReason:            notification.EventType,
			NotificationSeq:       notification.EventSeq,
		})
		if err != nil {
			return notification, fmt.Errorf("supervision: schedule lifecycle wake: %w", err)
		}
	}
	return notification, nil
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
	return ProjectLifecycle(ctx, store, wakes, LifecycleProjection{
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
