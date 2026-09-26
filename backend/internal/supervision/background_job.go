package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BackgroundJobTerminalReasonLimit bounds the model-visible reason line of a
// background job notification.
const BackgroundJobTerminalReasonLimit = 320

// backgroundJobCommandLimit bounds the command excerpt inside the reason.
const backgroundJobCommandLimit = 160

// BackgroundJobTerminalDisposition maps a background manager terminal event
// type onto the supervision dimensions. The event-type spelling also drives the
// auto-wake budget class (WakeBudgetClassOf is substring based): the *_failed
// and *_timed_out names land in the failure class, completed/cancelled/
// orphaned in the bounded "other" class.
func BackgroundJobTerminalDisposition(status string) (string, Severity, SupervisionState) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return "background_job_completed", SeverityWarning, SupervisionTerminated
	case "failed":
		return "background_job_failed", SeverityCritical, SupervisionTerminated
	case "timed_out":
		return "background_job_timed_out", SeverityCritical, SupervisionTimedOut
	case "orphaned":
		return "background_job_orphaned", SeverityCritical, SupervisionOrphaned
	case "cancelled":
		return "background_job_cancelled", SeverityWarning, SupervisionTerminated
	default:
		return "", "", ""
	}
}

// IsTerminalBackgroundJobStatus reports whether a job event type marks a
// terminal transition. Hosts must use it as a whitelist before projecting:
// the background manager calls its event handler on the high-frequency output
// path as well, and only a terminal transition may schedule a parent wake.
func IsTerminalBackgroundJobStatus(status string) bool {
	eventType, _, _ := BackgroundJobTerminalDisposition(status)
	return eventType != ""
}

// BackgroundJobEventFamily* 是宿主投影对 manager 事件的分类结果：终态、巡检
// 到点，或「与监督面无关」的高频 output 路径（必须被忽略）。
const (
	BackgroundJobFamilyNone     = ""
	BackgroundJobFamilyTerminal = "terminal"
	BackgroundJobFamilyMonitor  = "monitor"
)

// ClassifyBackgroundJobEvent 是全部宿主共用的唯一分类口径，避免 CLI 与 API
// 各自维护一份白名单而漂移。
func ClassifyBackgroundJobEvent(eventType string) string {
	if IsTerminalBackgroundJobStatus(eventType) {
		return BackgroundJobFamilyTerminal
	}
	if IsBackgroundJobMonitorCheck(eventType) {
		return BackgroundJobFamilyMonitor
	}
	return BackgroundJobFamilyNone
}

// BackgroundJobTerminalInput is the host-neutral input for a terminal
// background job projection. Hosts fill it from their background manager event
// and the job record; the projection below owns the durable shape so every host
// produces the same inbox item and wake identity.
type BackgroundJobTerminalInput struct {
	RootScopeID           string
	TargetParentSessionID string
	JobID                 string
	// Status is the terminal event type: completed|failed|timed_out|orphaned|
	// cancelled.
	Status string
	// Command is an optional command excerpt for the digest reason.
	Command string
	// ExitCode is the formatted exit code ("0", "137"); empty when unknown.
	ExitCode     string
	ErrorCode    string
	Message      string
	CancelSource string
	// Observed marks a terminal transition the model already saw (or is about
	// to see) through a task_output wait. The durable inbox item is still
	// recorded, but no wake is scheduled: forcing a new turn would only replay
	// evidence the model already has, and it would spend wake budget that a
	// genuinely unnoticed transition needs.
	Observed bool
	// Epoch is the stable per-transition identity (normally the event
	// timestamp in unix nanos). Replays of the same transition must reuse it so
	// the notification row and the wake notify key stay idempotent; a rerun
	// gets a new epoch and may wake again.
	Epoch int64
}

// ProjectBackgroundJobTerminal persists a durable supervision notification for
// a terminal background job and schedules exactly one wake for the owning
// session, so an idle session resumes and reads the job outcome instead of
// polling. It is best-effort at the call sites: failures never change the job
// outcome.
//
// Idempotency stack (shared with the lifecycle projections):
//   - the host only calls this on the first terminal transition;
//   - UpsertNotification identity is root_scope+subject+subject_version+
//     event_type, so a replayed event updates the same row and keeps its seq;
//   - ObligationID+EventKind+EventSeq feed DeriveNotifyKey, making a replayed
//     wake a no-op via the delivery ledger.
func ProjectBackgroundJobTerminal(ctx context.Context, store Store, wakes *WakeScheduler, in BackgroundJobTerminalInput) (Notification, error) {
	in.RootScopeID = strings.TrimSpace(in.RootScopeID)
	in.TargetParentSessionID = strings.TrimSpace(in.TargetParentSessionID)
	in.JobID = strings.TrimSpace(in.JobID)
	in.Status = strings.TrimSpace(in.Status)
	eventType, severity, state := BackgroundJobTerminalDisposition(in.Status)
	if eventType == "" {
		return Notification{}, fmt.Errorf("supervision: background job status %q is not terminal", in.Status)
	}
	if in.RootScopeID == "" || in.TargetParentSessionID == "" || in.JobID == "" {
		return Notification{}, fmt.Errorf("supervision: background job projection requires root scope, target session and job id")
	}
	if store == nil {
		return Notification{}, fmt.Errorf("supervision store is required")
	}
	if in.Epoch <= 0 {
		in.Epoch = time.Now().UTC().UnixNano()
	}
	notification, err := store.UpsertNotification(ctx, Notification{
		RootScopeID:           in.RootScopeID,
		TargetParentSessionID: in.TargetParentSessionID,
		SubjectKind:           SubjectJob,
		SubjectID:             in.JobID,
		SubjectVersion:        in.Epoch,
		EventType:             eventType,
		Severity:              severity,
		SupervisionState:      state,
		Reason:                FormatBackgroundJobTerminalReason(in),
		RecommendedAction:     string(ActionInspect),
		AllowedActions:        []string{string(ActionInspect)},
		// Completed/cancelled stay unresolved on purpose: the parent turn that
		// wakes up must still see the item in its digest, then acknowledge it.
		ResolutionState: ResolutionUnresolved,
	})
	if err != nil {
		return Notification{}, fmt.Errorf("supervision: persist background job projection: %w", err)
	}
	if wakes == nil || strings.TrimSpace(notification.NotificationID) == "" {
		return notification, nil
	}
	if in.Observed {
		return notification, nil
	}
	// Schedule regardless of severity: a completed job is exactly the async
	// evidence the model asked for, so it must not be dropped as inbox noise.
	if _, err := wakes.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           notification.RootScopeID,
		TargetParentSessionID: notification.TargetParentSessionID,
		WakeReason:            notification.EventType,
		NotificationSeq:       notification.EventSeq,
		ObligationID:          notification.SubjectID,
		EventKind:             WakeEventTerminal,
		EventSeq:              notification.EventSeq,
	}); err != nil {
		return notification, fmt.Errorf("supervision: schedule background job wake: %w", err)
	}
	return notification, nil
}

// FormatBackgroundJobTerminalReason renders the one-line, model-visible digest
// reason: status, the (truncated) command, exit code and error details when the
// host could supply them.
func FormatBackgroundJobTerminalReason(in BackgroundJobTerminalInput) string {
	status := strings.ToLower(strings.TrimSpace(in.Status))
	parts := []string{fmt.Sprintf("background job %s %s", strings.TrimSpace(in.JobID), status)}
	if command := strings.Join(strings.Fields(in.Command), " "); command != "" {
		parts = append(parts, "command: "+truncateBackgroundJobField(command, backgroundJobCommandLimit))
	}
	if exitCode := strings.TrimSpace(in.ExitCode); exitCode != "" {
		parts = append(parts, "exit_code="+exitCode)
	}
	if code := strings.TrimSpace(in.ErrorCode); code != "" {
		parts = append(parts, "error_code="+code)
	}
	if message := strings.Join(strings.Fields(in.Message), " "); message != "" {
		parts = append(parts, "message: "+truncateBackgroundJobField(message, backgroundJobCommandLimit))
	}
	if source := strings.TrimSpace(in.CancelSource); source != "" {
		parts = append(parts, "cancel_source="+source)
	}
	return truncateBackgroundJobField(strings.Join(parts, "; "), BackgroundJobTerminalReasonLimit)
}

func truncateBackgroundJobField(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
