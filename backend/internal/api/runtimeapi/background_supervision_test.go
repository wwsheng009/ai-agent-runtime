package runtimeapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func terminalJobEvent(jobID, eventType, sessionID string, payload map[string]interface{}) background.JobEvent {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payload["job_id"] = jobID
	payload["session_id"] = sessionID
	return background.JobEvent{
		JobID:     jobID,
		Type:      eventType,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}

func pendingWakesFor(t *testing.T, store *supervision.SQLiteSupervisionStore, rootScope, sessionID string) []supervision.WakePending {
	t.Helper()
	pending, err := store.ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:           rootScope,
		TargetParentSessionID: sessionID,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	return pending
}

// TestProjectBackgroundJobTerminal_CompletedSchedulesWakeOnce covers the core
// contract: a completed job produces one durable inbox item and one wake, and
// replaying the same transition changes nothing (idempotent epoch + notify key).
func TestProjectBackgroundJobTerminal_CompletedSchedulesWakeOnce(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-bg-job-completed")
	ctx := context.Background()
	event := terminalJobEvent("job-1", "completed", "sess-1", map[string]interface{}{
		"status":    "completed",
		"exit_code": 0,
	})

	handler.projectBackgroundJobTerminal(ctx, event, "sess-1")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:           "sess-1",
		TargetParentSessionID: "sess-1",
	})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	notification := notifications[0]
	require.Equal(t, supervision.SubjectJob, notification.SubjectKind)
	require.Equal(t, "job-1", notification.SubjectID)
	require.Equal(t, "background_job_completed", notification.EventType)
	require.Equal(t, supervision.SeverityWarning, notification.Severity)
	require.Equal(t, supervision.SupervisionTerminated, notification.SupervisionState)
	require.Contains(t, notification.Reason, "job-1")
	require.Contains(t, notification.Reason, "completed")

	require.Len(t, pendingWakesFor(t, store, "sess-1", "sess-1"), 1)

	// A replay of the same terminal transition must not add a second inbox row,
	// must not advance the notification seq, and must not add a wake.
	handler.projectBackgroundJobTerminal(ctx, event, "sess-1")
	replayed, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:           "sess-1",
		TargetParentSessionID: "sess-1",
	})
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	require.Equal(t, notification.EventSeq, replayed[0].EventSeq, "replay must keep the terminal epoch")
	require.Len(t, pendingWakesFor(t, store, "sess-1", "sess-1"), 1)
}

// TestProjectBackgroundJobTerminal_DigestCarriesTheJob proves the wake is
// deliverable rather than a content-free no-op: the lifecycle digest rendered
// for the owning session must contain the job notification.
func TestProjectBackgroundJobTerminal_DigestCarriesTheJob(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-bg-job-digest")
	ctx := context.Background()
	event := terminalJobEvent("job-9", "failed", "sess-digest", map[string]interface{}{
		"status":     "failed",
		"exit_code":  2,
		"error_code": "TOOL_EXECUTION",
		"message":    "boom",
	})

	handler.projectBackgroundJobTerminal(ctx, event, "sess-digest")

	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           "sess-digest",
		TargetParentSessionID: "sess-digest",
	})
	require.NoError(t, err)
	require.NotEmpty(t, digest.Items)
	require.Contains(t, digest.Text, "job-9")
	require.Contains(t, digest.Text, "exit_code=2")

	foundJob := false
	for _, item := range digest.Items {
		if item.SubjectID == "job-9" {
			foundJob = true
			require.Equal(t, supervision.SupervisionTerminated, item.SupervisionState)
			break
		}
	}
	require.True(t, foundJob, "digest must carry the job item: %+v", digest.Items)
}

// TestProjectBackgroundJobTerminal_FailureSeverityAndReason pins the failure
// classification the auto-wake budget consumes.
func TestProjectBackgroundJobTerminal_FailureSeverityAndReason(t *testing.T) {
	handler, store, scheduler := newAPIWakeTestHandler(t, "api-bg-job-failure")
	ctx := context.Background()

	handler.projectBackgroundJobTerminal(ctx, terminalJobEvent("job-2", "failed", "sess-2", map[string]interface{}{
		"status":     "failed",
		"exit_code":  137,
		"error_code": "TOOL_TIMEOUT",
		"message":    "killed",
	}), "sess-2")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: "sess-2"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, supervision.SeverityCritical, notifications[0].Severity)
	require.Equal(t, supervision.SubjectJob, notifications[0].SubjectKind)
	require.Contains(t, notifications[0].Reason, "exit_code=137")
	require.Contains(t, notifications[0].Reason, "error_code=TOOL_TIMEOUT")
	require.Contains(t, notifications[0].Reason, "message: killed")

	// The wake reason is the notification event type, which drives the budget
	// class: background_job_failed must land outside the progress bucket.
	require.Equal(t, supervision.WakeBudgetClassFailure, supervision.WakeBudgetClassOf(notifications[0].EventType))

	pending := pendingWakesFor(t, store, "sess-2", "sess-2")
	require.Len(t, pending, 1)
	require.True(t, scheduler != nil)
}

// TestBackgroundJobTerminalEventTypeWhitelist guards the high-frequency output
// path: only the five terminal types may be projected.
func TestBackgroundJobTerminalEventTypeWhitelist(t *testing.T) {
	for _, eventType := range []string{"completed", "failed", "timed_out", "orphaned", "cancelled", "COMPLETED"} {
		require.True(t, isTerminalBackgroundJobEventType(eventType), "expected %q to be terminal", eventType)
	}
	for _, eventType := range []string{"queued", "running", "output", "dispatching", "retry", ""} {
		require.False(t, isTerminalBackgroundJobEventType(eventType), "expected %q to be non-terminal", eventType)
	}
}

// TestProjectBackgroundJobTerminal_SkipsIncompleteIdentity: without a session
// or job id there is no inbox target, so the projection must stay a no-op.
func TestProjectBackgroundJobTerminal_SkipsIncompleteIdentity(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-bg-job-skip")
	ctx := context.Background()

	handler.projectBackgroundJobTerminal(ctx, terminalJobEvent("job-3", "completed", "", nil), "")
	handler.projectBackgroundJobTerminal(ctx, background.JobEvent{Type: "completed", CreatedAt: time.Now()}, "sess-3")
	handler.projectBackgroundJobTerminal(ctx, terminalJobEvent("job-3", "output", "sess-3", nil), "sess-3")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: "sess-3"})
	require.NoError(t, err)
	require.Empty(t, notifications)
	require.Len(t, pendingWakesFor(t, store, "sess-3", "sess-3"), 0)
}

// TestHandleBackgroundEvent_ProjectsTerminalJobWire covers the wiring: the
// handler's event callback projects terminal jobs (asynchronously) and never
// projects output events.
func TestHandleBackgroundEvent_ProjectsTerminalJobWire(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-bg-job-wire")
	ctx := context.Background()

	handler.handleBackgroundEvent(terminalJobEvent("job-4", "output", "sess-4", map[string]interface{}{
		"status": "running",
		"output": "noise",
	}))
	require.Len(t, pendingWakesFor(t, store, "sess-4", "sess-4"), 0)

	handler.handleBackgroundEvent(terminalJobEvent("job-4", "timed_out", "sess-4", map[string]interface{}{
		"status": "timed_out",
	}))

	require.Eventually(t, func() bool {
		pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
			RootScopeID:           "sess-4",
			TargetParentSessionID: "sess-4",
			UnclaimedOnly:         true,
		})
		return err == nil && len(pending) == 1
	}, 5*time.Second, 10*time.Millisecond, "terminal job event must schedule exactly one wake")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: "sess-4"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, "background_job_timed_out", notifications[0].EventType)
	require.Equal(t, supervision.SeverityCritical, notifications[0].Severity)
}

// TestProjectBackgroundJobTerminal_CancelledReasonWithoutManager keeps the
// adapter safe for hosts with no cached background manager (tests, embedded
// hosts): the cancel source still reaches the digest reason.
func TestProjectBackgroundJobTerminal_CancelledReasonWithoutManager(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-bg-job-cancelled")
	ctx := context.Background()

	handler.projectBackgroundJobTerminal(ctx, terminalJobEvent("job-5", "cancelled", "sess-5", map[string]interface{}{
		"status":        "cancelled",
		"cancel_source": "user_request",
	}), "sess-5")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: "sess-5"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	reason := notifications[0].Reason
	require.Contains(t, reason, "job-5")
	require.Contains(t, reason, "cancelled")
	require.Contains(t, reason, "cancel_source=user_request")
	require.LessOrEqual(t, len(reason), supervision.BackgroundJobTerminalReasonLimit)
	require.False(t, strings.Contains(reason, "\n"))
	require.Equal(t, supervision.SeverityWarning, notifications[0].Severity)
	require.Equal(t, "background_job_cancelled", notifications[0].EventType)
	require.Equal(t, supervision.WakeBudgetClassOther, supervision.WakeBudgetClassOf(notifications[0].EventType))
}

// newObservedBackgroundManager seeds a job whose terminal state was already
// surfaced to the model through a task_output wait, which is what the broker's
// in-flight waiter leaves behind.
func newObservedBackgroundManager(t *testing.T, jobID string) *background.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background.sqlite")
	seed, err := background.NewSQLiteStore(&background.StoreConfig{Path: path})
	require.NoError(t, err)
	require.NoError(t, seed.SaveJob(context.Background(), background.Job{
		ID:        jobID,
		SessionID: "sess-1",
		Kind:      "shell",
		Status:    background.StatusCompleted,
		Command:   "go test ./...",
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]interface{}{background.MetadataTerminalObserved: true},
	}))
	require.NoError(t, seed.Close())
	manager := background.NewManager(background.Config{StorePath: path})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// TestProjectBackgroundJobTerminal_ObservedJobSkipsTheWake pins the API host's
// suppression wiring: the inbox item is recorded (with its command excerpt) but
// no wake is scheduled for a terminal state the model already read.
func TestProjectBackgroundJobTerminal_ObservedJobSkipsTheWake(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-bg-job-observed")
	ctx := context.Background()
	manager := newObservedBackgroundManager(t, "job-observed")
	handler.backgroundMu.Lock()
	handler.backgroundManager = manager
	handler.backgroundMu.Unlock()

	handler.projectBackgroundJobTerminal(ctx, terminalJobEvent("job-observed", "completed", "sess-1", map[string]interface{}{
		"status":    "completed",
		"exit_code": 0,
	}), "sess-1")

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: "sess-1"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Contains(t, notifications[0].Reason, "command: go test ./...")
	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "sess-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "an observed terminal state must not schedule a wake")
}
