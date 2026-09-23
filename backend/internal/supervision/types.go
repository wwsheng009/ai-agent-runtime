// Package supervision implements the P2 Parent/Lead Supervision Control
// Plane: a durable descendant graph, critical lifecycle inbox, preflight
// digest, wake scheduling and durable action service shared by API and CLI.
//
// Design contract: docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md
// section 6 (6.1-6.9) and section 13 P2.
package supervision

import (
	"strconv"
	"strings"
	"time"
)

// Severity ranks a lifecycle notification channel. Critical notifications
// must never be blocked behind ordinary completion backlog.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// DeliveryState tracks how far a notification travelled toward the
// parent/lead. Delivery is orthogonal to decision state: `seen` only means
// the digest was injected or read, never that the parent accepted the risk.
type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliveryDelivered DeliveryState = "delivered"
	DeliverySeen      DeliveryState = "seen"
)

// DecisionState is the parent/lead acknowledgement state. `seen` must never
// be conflated with `acknowledged`; `deferred` re-enters the digest after
// DeferUntil.
type DecisionState string

const (
	DecisionUnacknowledged DecisionState = "unacknowledged"
	DecisionAcknowledged   DecisionState = "acknowledged"
	DecisionDeferred       DecisionState = "deferred"
	DecisionActioned       DecisionState = "actioned"
)

// ResolutionState describes whether the underlying issue is still present.
type ResolutionState string

const (
	ResolutionUnresolved ResolutionState = "unresolved"
	ResolutionRecovered  ResolutionState = "recovered"
	ResolutionClosed     ResolutionState = "closed"
	ResolutionFailed     ResolutionState = "failed"
)

// SubjectKind identifies what the notification/action targets.
type SubjectKind string

const (
	SubjectAgentSession SubjectKind = "agent_session"
	SubjectAgentRun     SubjectKind = "agent_run"
	SubjectTeam         SubjectKind = "team"
	SubjectTeamTask     SubjectKind = "team_task"
)

// SupervisionState mirrors the unified health matrix (doc 5.5/6.2). At least
// these states must be representable; callers may extend.
type SupervisionState string

const (
	SupervisionHealthy           SupervisionState = "healthy"
	SupervisionRunning           SupervisionState = "running"
	SupervisionBlocked           SupervisionState = "blocked"
	SupervisionStalled           SupervisionState = "stalled"
	SupervisionTimedOut          SupervisionState = "timed_out"
	SupervisionOrphanSuspected   SupervisionState = "orphan_suspected"
	SupervisionOrphaned          SupervisionState = "orphaned"
	SupervisionInvalid           SupervisionState = "invalid"
	SupervisionCancelRequested   SupervisionState = "cancel_requested"
	SupervisionCanceling         SupervisionState = "canceling"
	SupervisionTerminated        SupervisionState = "terminated"
	SupervisionRecovered         SupervisionState = "recovered"
)

// Notification is the durable projection of a critical lifecycle change for a
// specific parent/lead (doc 6.3). The durable lifecycle event is the source of
// truth; the notification is a view.
type Notification struct {
	NotificationID       string           `json:"notification_id,omitempty"`
	RootScopeID          string           `json:"root_scope_id,omitempty"`
	TargetParentSessionID string          `json:"target_parent_session_id,omitempty"`
	TargetParentTeamID   string           `json:"target_parent_team_id,omitempty"`
	SubjectKind          SubjectKind      `json:"subject_kind,omitempty"`
	SubjectID            string           `json:"subject_id,omitempty"`
	SubjectVersion       int64            `json:"subject_version,omitempty"`
	EventSeq             int64            `json:"event_seq,omitempty"`
	EventType            string           `json:"event_type,omitempty"`
	Severity             Severity         `json:"severity,omitempty"`
	SupervisionState     SupervisionState `json:"supervision_state,omitempty"`
	Reason               string           `json:"reason,omitempty"`
	DiagnosticRef        string           `json:"diagnostic_ref,omitempty"`
	RecommendedAction    string           `json:"recommended_action,omitempty"`
	AllowedActions       []string         `json:"allowed_actions,omitempty"`
	AutoActionID         string           `json:"auto_action_id,omitempty"`
	DeliveryState        DeliveryState    `json:"delivery_state,omitempty"`
	DecisionState        DecisionState    `json:"decision_state,omitempty"`
	ResolutionState      ResolutionState  `json:"resolution_state,omitempty"`
	DeferUntil           *time.Time       `json:"defer_until,omitempty"`
	DeliveredAt          *time.Time       `json:"delivered_at,omitempty"`
	SeenAt               *time.Time       `json:"seen_at,omitempty"`
	AcknowledgedAt       *time.Time       `json:"acknowledged_at,omitempty"`
	ResolvedAt           *time.Time       `json:"resolved_at,omitempty"`
	CreatedAt            time.Time        `json:"created_at,omitempty"`
	UpdatedAt            time.Time        `json:"updated_at,omitempty"`
	Version              int64            `json:"version,omitempty"`
}

// IdempotencyKey is the at-least-once delivery key (doc 6.3 rule 2):
// root_scope + subject + subject_version + event_type.
func (n Notification) IdempotencyKey() string {
	return strings.Join([]string{
		strings.TrimSpace(n.RootScopeID),
		strings.TrimSpace(string(n.SubjectKind)),
		strings.TrimSpace(n.SubjectID),
		formatInt(n.SubjectVersion),
		strings.TrimSpace(n.EventType),
	}, "|")
}

// Unresolved reports whether the underlying issue still requires attention.
func (n Notification) Unresolved() bool {
	if n.ResolutionState != "" && n.ResolutionState != ResolutionUnresolved {
		return false
	}
	if n.DecisionState == DecisionAcknowledged {
		return false
	}
	if n.DecisionState == DecisionDeferred && n.DeferUntil != nil {
		// Deferred but not yet due: still action-required, but not urgent.
		return true
	}
	return n.Severity == SeverityCritical || n.Severity == SeverityWarning
}

// ActionRequired reports whether the parent/lead should act on this item.
func (n Notification) ActionRequired() bool {
	if !n.Unresolved() {
		return false
	}
	if n.DecisionState == DecisionAcknowledged || n.DecisionState == DecisionActioned {
		return false
	}
	if n.DecisionState == DecisionDeferred && n.DeferUntil != nil && time.Now().Before(*n.DeferUntil) {
		return false
	}
	return true
}

// NotificationFilter selects notifications for read and preflight.
type NotificationFilter struct {
	RootScopeID          string
	TargetParentSessionID string
	TargetParentTeamID   string
	SubjectKind          SubjectKind
	SubjectID            string
	Severity             Severity
	DecisionState        DecisionState
	ResolutionState      ResolutionState
	AfterSeq             int64
	IncludeResolved      bool
	ActionRequiredOnly   bool
	Limit                int
}

// ActionStatus is the durable action lifecycle (doc 6.6).
type ActionStatus string

const (
	ActionRequested          ActionStatus = "requested"
	ActionAccepted           ActionStatus = "accepted"
	ActionExecuting          ActionStatus = "executing"
	ActionCompleted          ActionStatus = "completed"
	ActionPartiallyCompleted ActionStatus = "partially_completed"
	ActionRejected           ActionStatus = "rejected"
	ActionFailed             ActionStatus = "failed"
)

// ActionKind enumerates supported control actions (doc 6.6).
type ActionKind string

const (
	ActionInspect       ActionKind = "inspect"
	ActionAcknowledge   ActionKind = "acknowledge"
	ActionDefer         ActionKind = "defer"
	ActionCancel        ActionKind = "cancel"
	ActionClose         ActionKind = "close"
	ActionCancelSubtree ActionKind = "cancel_subtree"
	ActionRetry         ActionKind = "retry"
	ActionReassign      ActionKind = "reassign"
	// ActionExtendDeadline moves a run's deadlines forward instead of ending
	// the obligation (doc 6.5). It is the decision the escalate-first window
	// exists for, so it is a mutation action with its own payload
	// (extend_by | new_deadline + extend_which).
	ActionExtendDeadline ActionKind = "extend_deadline"
	// ActionTakeover is the explicit, audited ownership override (§6.11): a
	// mutation on a run whose OwnerID/lease does not belong to the acting
	// session must first take ownership through this action (reason required,
	// ActorID + reason recorded, fencing token advanced so the previous
	// owner's in-flight writes are rejected).
	ActionTakeover ActionKind = "takeover"
)

// CascadeMode controls how far a control action propagates.
type CascadeMode string

const (
	CascadeNone        CascadeMode = "none"
	CascadeDescendants CascadeMode = "descendants"
)

// ActionRecord is the durable control action (doc 6.6). All state changes are
// CAS-guarded via Version.
type ActionRecord struct {
	ActionID             string       `json:"action_id,omitempty"`
	RootScopeID          string       `json:"root_scope_id,omitempty"`
	RequestedByKind      string       `json:"requested_by_kind,omitempty"`
	RequestedByID        string       `json:"requested_by_id,omitempty"`
	TargetKind           SubjectKind  `json:"target_kind,omitempty"`
	TargetID             string       `json:"target_id,omitempty"`
	Action               ActionKind   `json:"action,omitempty"`
	CascadeMode          CascadeMode  `json:"cascade_mode,omitempty"`
	Reason               string       `json:"reason,omitempty"`
	ExpectedVersion      int64        `json:"expected_version,omitempty"`
	ExpectedFencingToken string       `json:"expected_fencing_token,omitempty"`
	Status               ActionStatus `json:"status,omitempty"`
	Result               string       `json:"result,omitempty"`
	ResultDetail         string       `json:"result_detail,omitempty"`
	// ExtendBy / NewDeadline / ExtendWhich are the extend_deadline payload
	// (doc 6.5). Exactly one of ExtendBy / NewDeadline is set. They are
	// persisted with the request so the accept/execute split (HTTP host) and a
	// crash between the two steps keep the parameters, and so the audit trail
	// answers "extended by how much" without replaying the tool call.
	ExtendBy    time.Duration `json:"extend_by,omitempty"`
	NewDeadline *time.Time    `json:"new_deadline,omitempty"`
	ExtendWhich string        `json:"extend_which,omitempty"`
	CreatedAt   time.Time     `json:"created_at,omitempty"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	FinishedAt  *time.Time    `json:"finished_at,omitempty"`
	Version     int64         `json:"version,omitempty"`
}

// ActionFilter selects durable actions.
type ActionFilter struct {
	RootScopeID string
	TargetKind  SubjectKind
	TargetID    string
	Action      ActionKind
	Status      ActionStatus
	Limit       int
}

// WakePending is the durable parent-turn wake request (doc 6.5 rule 3). It
// survives runtime restart and busy parent sessions; the turn scheduler
// claims it when the parent becomes runnable.
type WakePending struct {
	WakeID               string     `json:"wake_id,omitempty"`
	RootScopeID          string     `json:"root_scope_id,omitempty"`
	TargetParentSessionID string    `json:"target_parent_session_id,omitempty"`
	TargetParentTeamID   string     `json:"target_parent_team_id,omitempty"`
	WakeReason           string     `json:"wake_reason,omitempty"`
	NotificationSeq      int64      `json:"notification_seq,omitempty"`
	// TurnID names the parked turn this wake resumes (C2-1 #4 / I3). It is a
	// hint: the authoritative identity is re-derived from the obligation
	// ledger at delivery time, so a coalesced row can never resume a turn that
	// already ended. Empty keeps the legacy "new turn" delivery.
	TurnID               string     `json:"turn_id,omitempty"`
	// NotifyKey is the stable notification idempotency key (C2-4 #15 / B4,
	// doc 6.6): notify_key = hash(turn_id, obligation_id, event_kind,
	// terminal_epoch|progress_seq). It survives the wake row being resolved, so
	// the same terminal state is delivered once and the same progress seq never
	// wakes twice. Empty keeps the legacy coalescing-only behavior.
	NotifyKey            string     `json:"notify_key,omitempty"`
	// EventKind classifies the notification family (terminal / progress /
	// approval / lifecycle); it is part of the notify key.
	EventKind            string     `json:"event_kind,omitempty"`
	// ObligationID names the ledger row this notification belongs to.
	ObligationID         string     `json:"obligation_id,omitempty"`
	// EventSeq is the dedup counter behind the notify key: terminal_epoch for
	// terminal notifications, progress_seq for progress notifications (doc 6.6).
	EventSeq             int64      `json:"event_seq,omitempty"`
	DedupKey             string     `json:"dedup_key,omitempty"`
	CreatedAt            time.Time  `json:"created_at,omitempty"`
	ClaimedAt            *time.Time `json:"claimed_at,omitempty"`
	ClaimedBy            string     `json:"claimed_by,omitempty"`
}

// WakeDelivered is the durable notification delivery ledger (C2-4 #15 / B4,
// doc 6.6). It records that one notify_key was delivered, which makes the
// idempotency key outlive the wake row: a replayed terminal event finds the
// key here and is suppressed instead of starting a second resume
// (AC-P1-4a), while a failed delivery is never recorded and can be retried
// with the same key (AC-P1-4b).
type WakeDelivered struct {
	NotifyKey             string    `json:"notify_key,omitempty"`
	RootScopeID           string    `json:"root_scope_id,omitempty"`
	TargetParentSessionID string    `json:"target_parent_session_id,omitempty"`
	TargetParentTeamID    string    `json:"target_parent_team_id,omitempty"`
	WakeID                string    `json:"wake_id,omitempty"`
	TurnID                string    `json:"turn_id,omitempty"`
	EventKind             string    `json:"event_kind,omitempty"`
	ObligationID          string    `json:"obligation_id,omitempty"`
	EventSeq              int64     `json:"event_seq,omitempty"`
	DeliveredAt           time.Time `json:"delivered_at,omitempty"`
	DeliveredBy           string    `json:"delivered_by,omitempty"`
}

// WakeFilter selects wake pending rows.
type WakeFilter struct {
	RootScopeID          string
	TargetParentSessionID string
	TargetParentTeamID   string
	UnclaimedOnly        bool
	Limit                int
}

// WakeClaim records one delivered auto-wake for budget accounting (doc 6.5
// rule 4, plan P1-6 方案 2). Rows are append-only and pruned once the rate
// window elapses. With WakeBudgetModeDurable the scheduler counts these rows
// instead of an in-process map, so the budget stays consistent across
// processes and survives a restart.
type WakeClaim struct {
	// ClaimID is the idempotency key: a retried drain must not double count.
	ClaimID               string          `json:"claim_id,omitempty"`
	RootScopeID           string          `json:"root_scope_id,omitempty"`
	BudgetClass           WakeBudgetClass `json:"budget_class,omitempty"`
	WakeReason            string          `json:"wake_reason,omitempty"`
	TargetParentSessionID string          `json:"target_parent_session_id,omitempty"`
	ClaimedAt             time.Time       `json:"claimed_at,omitempty"`
	ClaimedBy             string          `json:"claimed_by,omitempty"`
}

// TeamEdge records a durable parent/child Team relationship (doc 6.1/6.7).
// Agent-side edges already exist in agentcontrol.agent_control_agents; this
// record covers Team -> Team nesting which is not expressible there.
type TeamEdge struct {
	EdgeID         string    `json:"edge_id,omitempty"`
	RootScopeID    string    `json:"root_scope_id,omitempty"`
	RootTeamID     string    `json:"root_team_id,omitempty"`
	ParentTeamID   string    `json:"parent_team_id,omitempty"`
	ParentKind     string    `json:"parent_kind,omitempty"`
	ParentID       string    `json:"parent_id,omitempty"`
	ChildTeamID    string    `json:"child_team_id,omitempty"`
	Relation       string    `json:"relation,omitempty"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	Status         string    `json:"status,omitempty"`
	Version        int64     `json:"version,omitempty"`
}

// TeamEdgeStatus mirrors the graph edge lifecycle (doc 6.1).
const (
	TeamEdgeStatusActive  = "active"
	TeamEdgeStatusDetached = "detached"
	TeamEdgeStatusClosed  = "closed"
)

func formatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}
