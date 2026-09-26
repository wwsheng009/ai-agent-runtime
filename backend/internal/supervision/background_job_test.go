package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackgroundJobTerminalDisposition(t *testing.T) {
	cases := map[string]struct {
		eventType   string
		severity    Severity
		state       SupervisionState
		schedulable bool
	}{
		"completed": {"background_job_completed", SeverityWarning, SupervisionTerminated, true},
		"failed":    {"background_job_failed", SeverityCritical, SupervisionTerminated, true},
		"timed_out": {"background_job_timed_out", SeverityCritical, SupervisionTimedOut, true},
		"orphaned":  {"background_job_orphaned", SeverityCritical, SupervisionOrphaned, true},
		"cancelled": {"background_job_cancelled", SeverityWarning, SupervisionTerminated, true},
		"FAILED":    {"background_job_failed", SeverityCritical, SupervisionTerminated, true},
		"running":   {"", "", "", false},
		"output":    {"", "", "", false},
		"":          {"", "", "", false},
	}
	for status, want := range cases {
		eventType, severity, state := BackgroundJobTerminalDisposition(status)
		require.Equal(t, want.eventType, eventType, "status %q", status)
		require.Equal(t, want.severity, severity, "status %q", status)
		require.Equal(t, want.state, state, "status %q", status)
		require.Equal(t, want.schedulable, IsTerminalBackgroundJobStatus(status), "status %q", status)
	}
}

// TestProjectBackgroundJobTerminal_SchedulesBudgetedWakeOnce pins the wake
// identity: one notification, one pending wake, and a replay with the same
// epoch changes nothing.
func TestProjectBackgroundJobTerminal_SchedulesBudgetedWakeOnce(t *testing.T) {
	store := newTestStore(t, "background-job-wake-once")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	in := BackgroundJobTerminalInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-1",
		Status:                "completed",
		Command:               "go   test  ./...",
		ExitCode:              "0",
		Epoch:                 1700000000000000000,
	}

	notification, err := ProjectBackgroundJobTerminal(ctx, store, scheduler, in)
	require.NoError(t, err)
	require.Equal(t, SubjectJob, notification.SubjectKind)
	require.Equal(t, "job-1", notification.SubjectID)
	require.Equal(t, "background_job_completed", notification.EventType)
	require.Equal(t, SeverityWarning, notification.Severity)
	require.Equal(t, ResolutionUnresolved, notification.ResolutionState)
	require.Contains(t, notification.Reason, "command: go test ./...")

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// Replay: same epoch => same row (seq unchanged) and no second wake.
	replayed, err := ProjectBackgroundJobTerminal(ctx, store, scheduler, in)
	require.NoError(t, err)
	require.Equal(t, notification.EventSeq, replayed.EventSeq)
	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-1"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	pending, err = store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// A rerun (new epoch) is a new terminal transition: a second inbox row and
	// a second wake are expected.
	in.Epoch = 1700000000000000001
	if _, err := ProjectBackgroundJobTerminal(ctx, store, scheduler, in); err != nil {
		t.Fatalf("rerun projection failed: %v", err)
	}
	notifications, err = store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-1"})
	require.NoError(t, err)
	require.Len(t, notifications, 2)
}

// TestProjectBackgroundJobTerminal_FailureClassesShareTheFailureBudget keeps
// the budget classification explicit, since it decides whether a job terminal
// wake can be starved by other failure wakes.
func TestProjectBackgroundJobTerminal_FailureClassesShareTheFailureBudget(t *testing.T) {
	require.Equal(t, WakeBudgetClassFailure, WakeBudgetClassOf("background_job_failed"))
	require.Equal(t, WakeBudgetClassFailure, WakeBudgetClassOf("background_job_timed_out"))
	require.Equal(t, WakeBudgetClassOther, WakeBudgetClassOf("background_job_completed"))
	require.Equal(t, WakeBudgetClassOther, WakeBudgetClassOf("background_job_cancelled"))
	require.Equal(t, WakeBudgetClassOther, WakeBudgetClassOf("background_job_orphaned"))
}

func TestProjectBackgroundJobTerminal_RejectsInvalidInput(t *testing.T) {
	store := newTestStore(t, "background-job-invalid")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	_, err := ProjectBackgroundJobTerminal(ctx, store, scheduler, BackgroundJobTerminalInput{
		RootScopeID: "root-1", TargetParentSessionID: "parent-1", JobID: "job-1", Status: "running",
	})
	require.Error(t, err, "non-terminal status must be rejected instead of waking the parent")

	_, err = ProjectBackgroundJobTerminal(ctx, store, scheduler, BackgroundJobTerminalInput{
		RootScopeID: "root-1", JobID: "job-1", Status: "completed",
	})
	require.Error(t, err, "missing target session must be rejected")

	notification, err := ProjectBackgroundJobTerminal(ctx, store, nil, BackgroundJobTerminalInput{
		RootScopeID: "root-1", TargetParentSessionID: "parent-1", JobID: "job-2", Status: "failed",
	})
	require.NoError(t, err, "a host without a wake scheduler still records the inbox item")
	require.Equal(t, "job-2", notification.SubjectID)
	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-1"})
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestFormatBackgroundJobTerminalReason(t *testing.T) {
	reason := FormatBackgroundJobTerminalReason(BackgroundJobTerminalInput{
		JobID:        "job-7",
		Status:       "timed_out",
		Command:      "sleep   600",
		ErrorCode:    "TOOL_TIMEOUT",
		Message:      "no output for 10m\nsecond line",
		CancelSource: "timeout",
	})
	require.Contains(t, reason, "background job job-7 timed_out")
	require.Contains(t, reason, "command: sleep 600")
	require.Contains(t, reason, "error_code=TOOL_TIMEOUT")
	require.Contains(t, reason, "message: no output for 10m second line")
	require.Contains(t, reason, "cancel_source=timeout")
	require.NotContains(t, reason, "\n")

	long := strings.Repeat("x", 500)
	reason = FormatBackgroundJobTerminalReason(BackgroundJobTerminalInput{JobID: "job-8", Status: "completed", Command: long})
	require.LessOrEqual(t, len([]rune(reason)), BackgroundJobTerminalReasonLimit)
	require.True(t, strings.HasSuffix(reason, "…"), "truncated reasons must be marked: %q", reason)
	require.LessOrEqual(t, strings.Count(reason, "command: "), 1)
}

// TestProjectBackgroundJobTerminal_ZeroEpochStaysIdempotent guards the host
// that forgets to pass an epoch: the projection must synthesize one instead of
// leaving SubjectVersion at 0 (which the store would reject or coalesce).
func TestProjectBackgroundJobTerminal_ZeroEpochStaysIdempotent(t *testing.T) {
	store := newTestStore(t, "background-job-zero-epoch")
	ctx := context.Background()
	notification, err := ProjectBackgroundJobTerminal(ctx, store, nil, BackgroundJobTerminalInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-1",
		Status:                "failed",
	})
	require.NoError(t, err)
	require.Greater(t, notification.SubjectVersion, int64(0))
	require.True(t, notification.SubjectVersion <= time.Now().UTC().UnixNano())
}

// TestProjectBackgroundJobTerminal_ObservedJobSkipsTheWake pins the
// suppression semantics: the durable inbox item is still recorded (the job
// outcome must never be lost), but no wake is scheduled because the model
// already holds the same evidence from a task_output wait.
func TestProjectBackgroundJobTerminal_ObservedJobSkipsTheWake(t *testing.T) {
	store := newTestStore(t, "background-job-observed")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	in := BackgroundJobTerminalInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-observed",
		Status:                "failed",
		Command:               "go test ./...",
		ExitCode:              "1",
		Observed:              true,
		Epoch:                 1700000000000000123,
	}

	notification, err := ProjectBackgroundJobTerminal(ctx, store, scheduler, in)
	require.NoError(t, err)
	require.Equal(t, SubjectJob, notification.SubjectKind)
	require.Equal(t, "background_job_failed", notification.EventType)
	require.Equal(t, SeverityCritical, notification.Severity)
	require.Equal(t, ResolutionUnresolved, notification.ResolutionState,
		"the item stays unresolved so the next natural turn still shows the job outcome")

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "an already observed terminal state must not force a wake turn")

	// The digest must still carry the item: suppression only skips the turn,
	// never the evidence.
	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
	})
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Equal(t, "job-observed", digest.Items[0].SubjectID)
}
