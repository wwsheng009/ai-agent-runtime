package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Scope identifies the root scope of a supervision query (doc 6.2).
type Scope struct {
	RootSessionID string
	RootTeamID    string
	Mode          string // children | descendants
}

// DescendantState is the runtime-projected state of one descendant. The
// supervision package stays decoupled from agentcontrol/team by receiving
// these states through a provider callback.
type DescendantState struct {
	Kind               SubjectKind
	ID                 string
	ParentPath         []string
	ExecutionStatus    string
	SupervisionState   SupervisionState
	HeartbeatAgeMs     int64
	ProgressAgeMs      int64
	ExecutionDeadlineAt *time.Time
	Reason             string
}

// DescendantProvider supplies the current execution state of a scope's
// descendants. Implementations may aggregate agentcontrol agent records and
// team store state.
type DescendantProvider interface {
	ListDescendants(ctx context.Context, scope Scope) ([]DescendantState, error)
}

// Snapshot is the unified supervision read model (doc 6.2).
type Snapshot struct {
	Scope       Scope              `json:"scope"`
	SnapshotSeq int64              `json:"snapshot_seq,omitempty"`
	GeneratedAt time.Time          `json:"generated_at,omitempty"`
	Summary     SnapshotSummary    `json:"summary"`
	Descendants []SnapshotItem     `json:"descendants,omitempty"`
	Truncated   bool               `json:"truncated,omitempty"`
	NextSeq     int64              `json:"next_seq,omitempty"`
}

// SnapshotSummary is the rollup counters (doc 6.2 summary).
type SnapshotSummary struct {
	Running                int `json:"running,omitempty"`
	Blocked                int `json:"blocked,omitempty"`
	Stalled                int `json:"stalled,omitempty"`
	TimedOut               int `json:"timed_out,omitempty"`
	Orphaned               int `json:"orphaned,omitempty"`
	Invalid                int `json:"invalid,omitempty"`
	Canceling              int `json:"canceling,omitempty"`
	TerminalUnacknowledged int `json:"terminal_unacknowledged,omitempty"`
	ActionRequired         int `json:"action_required,omitempty"`
}

// SnapshotItem is one descendant row in the snapshot.
type SnapshotItem struct {
	Kind               SubjectKind        `json:"kind,omitempty"`
	ID                 string             `json:"id,omitempty"`
	ParentPath         []string           `json:"parent_path,omitempty"`
	ExecutionStatus    string             `json:"execution_status,omitempty"`
	SupervisionState   SupervisionState   `json:"supervision_state,omitempty"`
	HeartbeatAgeMs     int64              `json:"heartbeat_age_ms,omitempty"`
	ProgressAgeMs      int64              `json:"progress_age_ms,omitempty"`
	ExecutionDeadlineAt *time.Time        `json:"execution_deadline_at,omitempty"`
	Reason             string             `json:"reason,omitempty"`
	AutoAction         *SnapshotAutoAction `json:"auto_action,omitempty"`
	RecommendedAction  string             `json:"recommended_action,omitempty"`
	AllowedActions     []string           `json:"allowed_actions,omitempty"`
	ActionRequired     bool               `json:"action_required,omitempty"`
	NotificationID     string             `json:"notification_id,omitempty"`
	LastChangeSeq      int64              `json:"last_change_seq,omitempty"`
}

// SnapshotAutoAction reports the runtime action already in flight (doc 6.2
// rule 6) so the parent does not duplicate cancel/retry.
type SnapshotAutoAction struct {
	Action   ActionKind `json:"action,omitempty"`
	Status   ActionStatus `json:"status,omitempty"`
	ActionID string     `json:"action_id,omitempty"`
}

// SnapshotRequest configures a snapshot build.
type SnapshotRequest struct {
	Scope            Scope
	AfterSeq         int64
	Health           string // any | abnormal | action_required
	IncludeTerminal  bool
	Limit            int
	Provider         DescendantProvider
}

// BuildSnapshot assembles the unified snapshot from durable notifications,
// pending actions and runtime descendant state. Default ordering prefers
// abnormal/action-required items (doc 6.2 rule 7).
func BuildSnapshot(ctx context.Context, store Store, req SnapshotRequest) (*Snapshot, error) {
	if store == nil {
		return nil, fmt.Errorf("supervision store is required")
	}
	snapshot := &Snapshot{
		Scope:       req.Scope,
		GeneratedAt: timeNow().UTC(),
	}
	var descendants []DescendantState
	if req.Provider != nil {
		list, err := req.Provider.ListDescendants(ctx, req.Scope)
		if err != nil {
			return nil, fmt.Errorf("list descendants: %w", err)
		}
		descendants = list
	}

	// Load unresolved notifications for the scope.
	notifications, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:          rootScopeIDFor(req.Scope),
		TargetParentSessionID: req.Scope.RootSessionID,
		TargetParentTeamID:   req.Scope.RootTeamID,
		IncludeResolved:      req.IncludeTerminal,
	})
	if err != nil {
		return nil, err
	}
	notificationBySubject := map[string]Notification{}
	for _, n := range notifications {
		key := string(n.SubjectKind) + "|" + n.SubjectID
		if existing, ok := notificationBySubject[key]; ok {
			if n.EventSeq > existing.EventSeq {
				notificationBySubject[key] = n
			}
		} else {
			notificationBySubject[key] = n
		}
		if n.EventSeq > snapshot.SnapshotSeq {
			snapshot.SnapshotSeq = n.EventSeq
		}
	}

	// Load pending actions for the scope to surface auto/in-flight actions.
	actions, err := store.ListActions(ctx, ActionFilter{RootScopeID: rootScopeIDFor(req.Scope)})
	if err != nil {
		return nil, err
	}
	actionByTarget := map[string]ActionRecord{}
	for _, a := range actions {
		if a.Status == ActionRequested || a.Status == ActionAccepted || a.Status == ActionExecuting {
			key := string(a.TargetKind) + "|" + a.TargetID
			if existing, ok := actionByTarget[key]; ok {
				if a.CreatedAt.After(existing.CreatedAt) {
					actionByTarget[key] = a
				}
			} else {
				actionByTarget[key] = a
			}
		}
	}

	evaluator := Evaluator{}
	for _, d := range descendants {
		key := string(d.Kind) + "|" + d.ID
		n, hasNotification := notificationBySubject[key]
		item := SnapshotItem{
			Kind:                d.Kind,
			ID:                  d.ID,
			ParentPath:          d.ParentPath,
			ExecutionStatus:     d.ExecutionStatus,
			SupervisionState:    d.SupervisionState,
			HeartbeatAgeMs:      d.HeartbeatAgeMs,
			ProgressAgeMs:       d.ProgressAgeMs,
			ExecutionDeadlineAt: d.ExecutionDeadlineAt,
			Reason:              d.Reason,
		}
		if hasNotification {
			item.Reason = firstNonEmpty(d.Reason, n.Reason)
			item.RecommendedAction = evaluator.EvaluateRecommendedAction(n)
			item.AllowedActions = evaluator.EvaluateAllowedActions(n)
			item.ActionRequired = n.ActionRequired()
			item.NotificationID = n.NotificationID
			item.LastChangeSeq = n.EventSeq
			if n.SupervisionState != "" {
				item.SupervisionState = n.SupervisionState
			}
			if n.Reason != "" {
				item.Reason = n.Reason
			}
		} else {
			item.RecommendedAction = evaluator.EvaluateRecommendedAction(Notification{
				SubjectKind:      d.Kind,
				SupervisionState: d.SupervisionState,
			})
			item.AllowedActions = evaluator.EvaluateAllowedActions(Notification{
				SubjectKind:      d.Kind,
				SupervisionState: d.SupervisionState,
				DecisionState:    DecisionUnacknowledged,
				ResolutionState:  ResolutionUnresolved,
			})
		}
		if action, ok := actionByTarget[key]; ok {
			item.AutoAction = &SnapshotAutoAction{
				Action:   action.Action,
				Status:   action.Status,
				ActionID: action.ActionID,
			}
			// Runtime already acting: never recommend a duplicate.
			item.AllowedActions = evaluator.EvaluateAllowedActions(Notification{
				SubjectKind:         d.Kind,
				SupervisionState:    d.SupervisionState,
				DecisionState:       DecisionUnacknowledged,
				ResolutionState:     ResolutionUnresolved,
				AutoActionID:        action.ActionID,
				RecommendedAction:   string(action.Action),
			})
			item.RecommendedAction = "inspect_cancel_result"
			item.ActionRequired = false
		}
		snapshot.Descendants = append(snapshot.Descendants, item)
	}

	// Merge standalone notifications (subjects not present in runtime state).
	for _, n := range notifications {
		key := string(n.SubjectKind) + "|" + n.SubjectID
		if mapContainsDescendant(snapshot.Descendants, n.SubjectKind, n.SubjectID) {
			continue
		}
		if _, ok := notificationBySubject[key]; !ok {
			continue
		}
		item := SnapshotItem{
			Kind:              n.SubjectKind,
			ID:                n.SubjectID,
			SupervisionState:  n.SupervisionState,
			Reason:            n.Reason,
			RecommendedAction: evaluator.EvaluateRecommendedAction(n),
			AllowedActions:    evaluator.EvaluateAllowedActions(n),
			ActionRequired:    n.ActionRequired(),
			NotificationID:    n.NotificationID,
			LastChangeSeq:     n.EventSeq,
		}
		if action, ok := actionByTarget[key]; ok {
			item.AutoAction = &SnapshotAutoAction{Action: action.Action, Status: action.Status, ActionID: action.ActionID}
		}
		snapshot.Descendants = append(snapshot.Descendants, item)
	}

	// Summary rollup.
	for _, item := range snapshot.Descendants {
		switch item.SupervisionState {
		case SupervisionRunning, SupervisionHealthy:
			snapshot.Summary.Running++
		case SupervisionBlocked:
			snapshot.Summary.Blocked++
		case SupervisionStalled:
			snapshot.Summary.Stalled++
		case SupervisionTimedOut:
			snapshot.Summary.TimedOut++
		case SupervisionOrphaned, SupervisionOrphanSuspected:
			snapshot.Summary.Orphaned++
		case SupervisionInvalid:
			snapshot.Summary.Invalid++
		case SupervisionCancelRequested, SupervisionCanceling:
			snapshot.Summary.Canceling++
		case SupervisionTerminated:
			if item.NotificationID != "" && !item.ActionRequired && item.LastChangeSeq > req.AfterSeq {
				snapshot.Summary.TerminalUnacknowledged++
			}
		}
		if item.ActionRequired {
			snapshot.Summary.ActionRequired++
		}
	}

	// Filter by health and limit.
	limit := req.Limit
	if limit <= 0 {
		limit = defaultSnapshotLimit
	}
	filtered := snapshot.Descendants[:0]
	for _, item := range snapshot.Descendants {
		switch req.Health {
		case "abnormal":
			if !itemAbnormal(item) {
				continue
			}
		case "action_required":
			if !item.ActionRequired {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	snapshot.Descendants = filtered
	if len(snapshot.Descendants) > limit {
		snapshot.Descendants = snapshot.Descendants[:limit]
		snapshot.Truncated = true
	}
	if snapshot.SnapshotSeq < req.AfterSeq {
		snapshot.SnapshotSeq = req.AfterSeq
	}
	snapshot.NextSeq = snapshot.SnapshotSeq
	return snapshot, nil
}

const defaultSnapshotLimit = 200

func itemAbnormal(item SnapshotItem) bool {
	switch item.SupervisionState {
	case SupervisionStalled, SupervisionTimedOut, SupervisionOrphaned, SupervisionOrphanSuspected,
		SupervisionInvalid, SupervisionCancelRequested, SupervisionCanceling, SupervisionTerminated:
		return true
	}
	return item.ActionRequired
}

func mapContainsDescendant(items []SnapshotItem, kind SubjectKind, id string) bool {
	for _, item := range items {
		if item.Kind == kind && strings.TrimSpace(item.ID) == strings.TrimSpace(id) {
			return true
		}
	}
	return false
}

func rootScopeIDFor(scope Scope) string {
	if strings.TrimSpace(scope.RootSessionID) != "" {
		return strings.TrimSpace(scope.RootSessionID)
	}
	return strings.TrimSpace(scope.RootTeamID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
