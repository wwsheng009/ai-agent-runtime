package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EventBackgroundJobMonitor is the inbox event type of a scheduled background
// job check. Unlike the terminal family it reports a job that is still pending
// or running, so it never gates on severity and always carries the current
// state plus a pointer to the output tail.
const EventBackgroundJobMonitor = "background_job_monitor"

// BackgroundJobMonitorCheckEventType mirrors background.MonitorCheckEventType
// (the manager emitting the check). It is duplicated as a plain string so the
// supervision package keeps no dependency on the background package; the
// equality is pinned by a test.
const BackgroundJobMonitorCheckEventType = "monitor_check"

// IsBackgroundJobMonitorCheck reports whether a manager event is a scheduled
// monitor check (a non-terminal nudge) rather than an output/lifecycle event.
func IsBackgroundJobMonitorCheck(eventType string) bool {
	return strings.EqualFold(strings.TrimSpace(eventType), BackgroundJobMonitorCheckEventType)
}

// BackgroundJobMonitorInput is the host-neutral input for a monitor check
// projection.
type BackgroundJobMonitorInput struct {
	RootScopeID           string
	TargetParentSessionID string
	JobID                 string
	// Status is the job's current state at check time (pending|running).
	Status string
	// Command is an optional command excerpt for the digest reason.
	Command string
	// Message is an optional extra line (e.g. why the job is still queued).
	Message string
	// Elapsed is how long the job has been alive at check time.
	Elapsed time.Duration
	// CheckAfter is the monitor's configured check deadline.
	CheckAfter time.Duration
	// MaxDuration is the configured auto-termination deadline (0 = none).
	MaxDuration time.Duration
	// Epoch is the stable per-check identity (normally the event timestamp in
	// unix nanos). A replayed check updates the same row and cannot wake twice.
	Epoch int64
}

// ProjectBackgroundJobMonitor records a durable inbox item for a periodic job
// check and schedules one progress-class wake, so the model gets a turn with the
// job's current state instead of polling task_output. The item stays unresolved
// on purpose: it is the evidence behind the wake, and the model acknowledges it
// once it has read the output.
//
// Best-effort at the call sites: a supervision outage never changes the job.
func ProjectBackgroundJobMonitor(ctx context.Context, store Store, wakes *WakeScheduler, in BackgroundJobMonitorInput) (Notification, error) {
	in.RootScopeID = strings.TrimSpace(in.RootScopeID)
	in.TargetParentSessionID = strings.TrimSpace(in.TargetParentSessionID)
	in.JobID = strings.TrimSpace(in.JobID)
	in.Status = strings.TrimSpace(in.Status)
	if in.RootScopeID == "" || in.TargetParentSessionID == "" || in.JobID == "" {
		return Notification{}, fmt.Errorf("supervision: background job monitor projection requires root scope, target session and job id")
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
		EventType:             EventBackgroundJobMonitor,
		Severity:              SeverityInfo,
		SupervisionState:      SupervisionRunning,
		Reason:                FormatBackgroundJobMonitorReason(in),
		RecommendedAction:     string(ActionInspect),
		AllowedActions:        []string{string(ActionInspect)},
		ResolutionState:       ResolutionUnresolved,
	})
	if err != nil {
		return Notification{}, fmt.Errorf("supervision: persist background job monitor projection: %w", err)
	}
	if wakes == nil || strings.TrimSpace(notification.NotificationID) == "" {
		return notification, nil
	}
	// Progress family: the same check epoch never wakes twice, while each new
	// check (new epoch) may wake again under the bounded "other" budget class.
	if _, err := wakes.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           notification.RootScopeID,
		TargetParentSessionID: notification.TargetParentSessionID,
		WakeReason:            notification.EventType,
		NotificationSeq:       notification.EventSeq,
		ObligationID:          notification.SubjectID,
		EventKind:             WakeEventProgress,
		EventSeq:              notification.EventSeq,
	}); err != nil {
		return notification, fmt.Errorf("supervision: schedule background job monitor wake: %w", err)
	}
	return notification, nil
}

// FormatBackgroundJobMonitorReason renders the one-line, model-visible digest
// reason of a job check: state, elapsed time, the (truncated) command and the
// pointer to the output tail.
func FormatBackgroundJobMonitorReason(in BackgroundJobMonitorInput) string {
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = "running"
	}
	reason := fmt.Sprintf("background job %s still %s after %s", strings.TrimSpace(in.JobID), status, formatMonitorDuration(in.Elapsed))
	if in.CheckAfter > 0 {
		reason += fmt.Sprintf(" (check deadline %s", formatMonitorDuration(in.CheckAfter))
		if in.MaxDuration > 0 {
			reason += fmt.Sprintf(", auto-terminate at %s", formatMonitorDuration(in.MaxDuration))
		}
		reason += ")"
	} else if in.MaxDuration > 0 {
		reason += fmt.Sprintf(" (auto-terminate at %s)", formatMonitorDuration(in.MaxDuration))
	}
	if command := strings.Join(strings.Fields(in.Command), " "); command != "" {
		reason += "; command: " + truncateBackgroundJobField(command, backgroundJobCommandLimit)
	}
	if message := strings.Join(strings.Fields(in.Message), " "); message != "" {
		reason += "; message: " + truncateBackgroundJobField(message, backgroundJobCommandLimit)
	}
	reason += "; read the tail with task_output"
	return truncateBackgroundJobField(reason, BackgroundJobTerminalReasonLimit)
}

// formatMonitorDuration renders a deadline in the coarsest useful unit so the
// digest line stays short.
func formatMonitorDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	if value < time.Minute {
		return fmt.Sprintf("%ds", int(value.Seconds()))
	}
	if value < time.Hour {
		minutes := int(value.Minutes())
		seconds := int(value.Seconds()) - minutes*60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	hours := int(value.Hours())
	minutes := int(value.Minutes()) - hours*60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}
