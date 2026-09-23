package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Child approval/question channel event types (doc P0-2). A request and its
// resolution intentionally share one event type: the durable notification is
// then updated in place instead of accumulating a stale unresolved row plus a
// second resolved row for the same request.
const (
	EventAgentApprovalRequested = "agent_approval_requested"
	EventAgentQuestionAsked     = "agent_question_asked"
)

// Runtime event names published by the chat actor for the same channel. They
// are duplicated here (instead of importing the chat package) so supervision
// stays host-neutral and free of runtime dependencies.
const (
	RuntimeEventApprovalRequested = "approval_requested"
	RuntimeEventApprovalResolved  = "approval_resolved"
	RuntimeEventQuestionAsked     = "question_asked"
	RuntimeEventQuestionAnswered  = "question_answered"
)

// ApprovalRequestEventType maps a runtime request/resolution event to the
// request event type that owns the durable notification row.
func ApprovalRequestEventType(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case RuntimeEventQuestionAsked, RuntimeEventQuestionAnswered:
		return EventAgentQuestionAsked
	default:
		return EventAgentApprovalRequested
	}
}

// ApprovalMode distinguishes a tool approval (parent/lead must allow or deny)
// from an open question raised by the child.
type ApprovalMode string

const (
	ApprovalModeApproval ApprovalMode = "approval"
	ApprovalModeQuestion ApprovalMode = "question"
)

// ApprovalNotice is the host-neutral input for projecting a child
// waiting_approval / waiting_input transition into the parent lifecycle inbox.
// Hosts call it right after the runtime publishes the child event; the
// projection is best-effort and never changes the child outcome.
type ApprovalNotice struct {
	RootScopeID           string
	TargetParentSessionID string
	TargetParentTeamID    string
	ChildSessionID        string
	ChildPath             string
	RequestID             string
	ToolName              string
	Mode                  ApprovalMode
	Summary               string
	RequestedAt           time.Time
	ExpiresAt             time.Time
	// Wake asks for a durable auto-wake when the parent is runnable. Approvals
	// default to waking; questions stay digest-only because they are answered
	// by a human, not by a parent turn.
	Wake bool
}

// ApprovalSubjectVersion derives a stable per-request subject version so the
// notification idempotency key (root_scope + subject + version + event_type)
// deduplicates the same request while a second request from the same child
// still creates its own row.
func ApprovalSubjectVersion(requestID string) int64 {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 1
	}
	var version int64 = 1
	for _, r := range requestID {
		version = version*31 + int64(r)
		if version < 0 {
			version = -version
		}
		if version == 0 {
			version = 1
		}
	}
	return version
}

// ProjectApprovalRequest persists (or refreshes) the durable pending-approval
// notification for a child session. It is idempotent per request id: a
// replayed event updates the existing row instead of creating inbox noise.
func ProjectApprovalRequest(ctx context.Context, store Store, wakes *WakeScheduler, notice ApprovalNotice) (Notification, error) {
	if store == nil {
		return Notification{}, fmt.Errorf("supervision store is required")
	}
	notice = normalizeApprovalNotice(notice)
	if notice.RootScopeID == "" {
		return Notification{}, fmt.Errorf("supervision: root scope id is required")
	}
	if notice.ChildSessionID == "" {
		return Notification{}, fmt.Errorf("supervision: child session id is required")
	}
	if notice.RequestID == "" {
		return Notification{}, fmt.Errorf("supervision: request id is required")
	}
	eventType := EventAgentApprovalRequested
	severity := SeverityCritical
	recommended := string(ActionInspect)
	if notice.Mode == ApprovalModeQuestion {
		eventType = EventAgentQuestionAsked
		// A question needs parent attention but must not claim the failure
		// budget or trigger a rate-limited auto wake.
		severity = SeverityWarning
		recommended = string(ActionInspect)
	}
	notification, err := store.UpsertNotification(ctx, Notification{
		RootScopeID:           notice.RootScopeID,
		TargetParentSessionID: notice.TargetParentSessionID,
		TargetParentTeamID:    notice.TargetParentTeamID,
		SubjectKind:           SubjectAgentSession,
		SubjectID:             notice.ChildSessionID,
		SubjectVersion:        ApprovalSubjectVersion(notice.RequestID),
		EventType:             eventType,
		Severity:              severity,
		SupervisionState:      SupervisionBlocked,
		Reason:                approvalReason(notice),
		DiagnosticRef:         approvalDiagnosticRef(notice),
		RecommendedAction:     recommended,
		// The server recomputes allowed actions from the subject kind; this
		// copy keeps the stored record self-describing for API consumers.
		AllowedActions: []string{
			string(ActionInspect),
			string(ActionAcknowledge),
			string(ActionDefer),
			string(ActionCancel),
			string(ActionClose),
		},
		DecisionState:         "",
		ResolutionState:       ResolutionUnresolved,
	})
	if err != nil {
		return Notification{}, fmt.Errorf("supervision: persist approval projection: %w", err)
	}
	if wakes != nil && notice.Wake && severity == SeverityCritical && notification.ActionRequired() {
		_, err := wakes.ScheduleWake(ctx, WakeRequest{
			RootScopeID:           notification.RootScopeID,
			TargetParentSessionID: notification.TargetParentSessionID,
			TargetParentTeamID:    notification.TargetParentTeamID,
			WakeReason:            notification.EventType,
			NotificationSeq:       notification.EventSeq,
			// C2-4 (#15) / B4: the approval family has its own notify key, so
			// re-projecting the same request cannot start a second resume while
			// a later request for the same child still can.
			ObligationID: notification.SubjectID,
			EventKind:    WakeEventApproval,
			EventSeq:     notification.EventSeq,
		})
		if err != nil {
			return notification, fmt.Errorf("supervision: schedule approval wake: %w", err)
		}
	}
	return notification, nil
}

// ResolveApprovalRequest closes the pending approval/question notification for
// the same request id. The row is updated in place so the parent's preflight
// stops showing a decision that the child already received.
func ResolveApprovalRequest(ctx context.Context, store Store, notice ApprovalNotice, outcome string) (Notification, error) {
	if store == nil {
		return Notification{}, fmt.Errorf("supervision store is required")
	}
	notice = normalizeApprovalNotice(notice)
	if notice.RootScopeID == "" || notice.ChildSessionID == "" || notice.RequestID == "" {
		return Notification{}, fmt.Errorf("supervision: root scope, child session and request id are required")
	}
	eventType := EventAgentApprovalRequested
	if notice.Mode == ApprovalModeQuestion {
		eventType = EventAgentQuestionAsked
	}
	notification, err := store.UpsertNotification(ctx, Notification{
		RootScopeID:           notice.RootScopeID,
		TargetParentSessionID: notice.TargetParentSessionID,
		TargetParentTeamID:    notice.TargetParentTeamID,
		SubjectKind:           SubjectAgentSession,
		SubjectID:             notice.ChildSessionID,
		SubjectVersion:        ApprovalSubjectVersion(notice.RequestID),
		EventType:             eventType,
		Severity:              SeverityInfo,
		SupervisionState:      SupervisionRunning,
		Reason:                approvalResolutionReason(notice, outcome),
		DiagnosticRef:         approvalDiagnosticRef(notice),
		ResolutionState:       ResolutionClosed,
	})
	if err != nil {
		return Notification{}, fmt.Errorf("supervision: resolve approval projection: %w", err)
	}
	return notification, nil
}

// ApprovalResolutionFromPayload maps a runtime approval/question event payload
// to the durable resolution outcome. Expired approvals are a failure of the
// decision, everything else the child received is a normal closure.
func ApprovalResolutionFromPayload(payload map[string]interface{}) string {
	if payload == nil {
		return "resolved"
	}
	if text, ok := payload["resolution"].(string); ok && strings.TrimSpace(text) != "" {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "expired":
			return "expired"
		}
	}
	if allowed, ok := payload["allowed"].(bool); ok && !allowed {
		return "denied"
	}
	return "resolved"
}

// ApprovalNoticeFromEvent builds the projection input from a runtime event
// payload. It returns ok=false when the event does not carry a usable request
// id, in which case hosts must not project anything.
func ApprovalNoticeFromEvent(rootScopeID, parentSessionID, parentTeamID, childSessionID, childPath string, eventType string, payload map[string]interface{}, at time.Time) (ApprovalNotice, bool) {
	mode := ApprovalModeApproval
	switch strings.TrimSpace(eventType) {
	case EventAgentApprovalRequested, RuntimeEventApprovalRequested, RuntimeEventApprovalResolved:
		mode = ApprovalModeApproval
	case EventAgentQuestionAsked, RuntimeEventQuestionAsked, RuntimeEventQuestionAnswered:
		mode = ApprovalModeQuestion
	default:
		return ApprovalNotice{}, false
	}
	requestID := ""
	for _, key := range []string{"request_id", "question_id", "approval_id"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			requestID = strings.TrimSpace(value)
			break
		}
	}
	if requestID == "" {
		return ApprovalNotice{}, false
	}
	notice := ApprovalNotice{
		RootScopeID:           strings.TrimSpace(rootScopeID),
		TargetParentSessionID: strings.TrimSpace(parentSessionID),
		TargetParentTeamID:    strings.TrimSpace(parentTeamID),
		ChildSessionID:        strings.TrimSpace(childSessionID),
		ChildPath:             strings.TrimSpace(childPath),
		RequestID:             requestID,
		Mode:                  mode,
		Wake:                  mode == ApprovalModeApproval,
	}
	for _, key := range []string{"tool_name", "tool"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			notice.ToolName = strings.TrimSpace(value)
			break
		}
	}
	for _, key := range []string{"prompt", "question", "reason", "summary", "message"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			notice.Summary = strings.TrimSpace(value)
			break
		}
	}
	if expiresAt, ok := payload["expires_at"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(expiresAt)); err == nil {
			notice.ExpiresAt = parsed
		}
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	notice.RequestedAt = at
	return notice, true
}

func normalizeApprovalNotice(notice ApprovalNotice) ApprovalNotice {
	notice.RootScopeID = strings.TrimSpace(notice.RootScopeID)
	notice.TargetParentSessionID = strings.TrimSpace(notice.TargetParentSessionID)
	notice.TargetParentTeamID = strings.TrimSpace(notice.TargetParentTeamID)
	notice.ChildSessionID = strings.TrimSpace(notice.ChildSessionID)
	notice.ChildPath = strings.TrimSpace(notice.ChildPath)
	notice.RequestID = strings.TrimSpace(notice.RequestID)
	notice.ToolName = strings.TrimSpace(notice.ToolName)
	notice.Summary = strings.TrimSpace(notice.Summary)
	if notice.Mode == "" {
		notice.Mode = ApprovalModeApproval
	}
	return notice
}

// approvalReason renders the parent-facing summary of a pending decision. The
// preflight digest line carries it verbatim so a parent turn knows which child
// session, tool and request id is blocked.
func approvalReason(notice ApprovalNotice) string {
	kind := "approval"
	if notice.Mode == ApprovalModeQuestion {
		kind = "question"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "child %s pending", kind)
	if notice.ToolName != "" {
		fmt.Fprintf(&b, ": tool=%s", notice.ToolName)
	}
	fmt.Fprintf(&b, ", request_id=%s", notice.RequestID)
	if notice.ChildPath != "" {
		fmt.Fprintf(&b, ", path=%s", notice.ChildPath)
	}
	if !notice.RequestedAt.IsZero() {
		fmt.Fprintf(&b, ", waiting=%s", formatApprovalWait(time.Since(notice.RequestedAt)))
	}
	if !notice.ExpiresAt.IsZero() {
		if remaining := time.Until(notice.ExpiresAt); remaining > 0 {
			fmt.Fprintf(&b, ", expires_in=%s", formatApprovalWait(remaining))
		}
	}
	if notice.Summary != "" {
		fmt.Fprintf(&b, ", summary=%s", truncateApprovalSummary(notice.Summary))
	}
	return b.String()
}

func approvalResolutionReason(notice ApprovalNotice, outcome string) string {
	kind := "approval"
	if notice.Mode == ApprovalModeQuestion {
		kind = "question"
	}
	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		outcome = "resolved"
	}
	reason := fmt.Sprintf("child %s %s", kind, outcome)
	if notice.ToolName != "" {
		reason += ", tool=" + notice.ToolName
	}
	reason += ", request_id=" + notice.RequestID
	return reason
}

func approvalDiagnosticRef(notice ApprovalNotice) string {
	parts := []string{"session=" + notice.ChildSessionID}
	if notice.ChildPath != "" {
		parts = append(parts, "path="+notice.ChildPath)
	}
	if notice.ToolName != "" {
		parts = append(parts, "tool="+notice.ToolName)
	}
	parts = append(parts, "request_id="+notice.RequestID)
	return strings.Join(parts, " ")
}

func formatApprovalWait(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func truncateApprovalSummary(summary string) string {
	const max = 120
	summary = strings.Join(strings.Fields(summary), " ")
	if len(summary) <= max {
		return summary
	}
	return summary[:max] + "..."
}
