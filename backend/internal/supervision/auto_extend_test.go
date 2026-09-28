package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// autoExtendTestConfig is the §2.2/§2.3 fixture: a 10s execution budget with
// the 5m operator progress default, so a scan at +11s sits past the execution
// deadline while a run that ticked recently is still healthy.
func autoExtendTestConfig() ExecutionSupervisorConfig {
	return ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   10 * time.Second,
		DefaultProgressTimeout:    5 * time.Minute,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}
}

// AC (§7): a run that is still making progress at its execution deadline is
// auto-extended once instead of being hard-cut: one original-budget window is
// granted (the same per-call maximum a parent could grant), charged to the I5
// counters, and projected as the shared obligation.deadline.extended event.
func TestAutoExtend_HealthyRunAtExecutionDeadlineIsExtended(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-healthy", autoExtendTestConfig(), interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-auto-extend",
		ExecutionTimeout: 10 * time.Second,
	})
	require.NoError(t, err)

	// Meaningful progress after admission: the run is alive at the deadline.
	supervisor.Now = func() time.Time { return now.Add(5 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
	require.NoError(t, err)

	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "auto_extended", decisions[0].Decision)
	require.Equal(t, "auto_extended", decisions[0].ActionTaken)
	require.Contains(t, decisions[0].Reason, "healthy")
	require.Zero(t, interrupter.count(), "a healthy run is never interrupted")

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status, "the run keeps running")
	require.Equal(t, 1, got.ExtensionCount)
	require.Equal(t, 10*time.Second, got.ExtendedTotal, "one original-budget window")
	require.WithinDuration(t, now.Add(21*time.Second), *got.ExecutionDeadlineAt, time.Second)
	require.Nil(t, got.DecisionWindowUntil)

	extended := extendLifecycleEvent(t, store, run.RunID, "obligation.deadline.extended")
	require.Equal(t, SeverityInfo, extended.Severity)
	require.Contains(t, extended.Reason, "已延长 ×1")
	require.Contains(t, extended.Reason, "healthy")
}

// The evaluation only rescues runs with evidence of life: a run that went
// quiet past the operator progress window keeps the forced branch.
func TestAutoExtend_StalledRunAtExecutionDeadlineIsStillCut(t *testing.T) {
	interrupter := &fakeInterrupter{}
	cfg := autoExtendTestConfig()
	cfg.DefaultProgressTimeout = 5 * time.Second
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-stalled", cfg, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-stalled",
		ExecutionTimeout: 10 * time.Second,
	})
	require.NoError(t, err)

	// One early tick, then silence: at +11s the last progress is 10s old while
	// the operator window is 5s, so the run is not healthy.
	supervisor.Now = func() time.Time { return now.Add(1 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
	require.NoError(t, err)

	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "execution_timed_out", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "execution_timed_out", got.CancelSource)
}

// The automatic extension spends the shared I5 budget: once the call budget is
// exhausted a still-healthy run falls back to the forced branch instead of
// extending without bound.
func TestAutoExtend_BudgetExhaustionFallsBackToCut(t *testing.T) {
	interrupter := &fakeInterrupter{}
	cfg := autoExtendTestConfig()
	cfg.MaxExtensions = 1
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-budget", cfg, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-budget",
		ExecutionTimeout: 10 * time.Second,
	})
	require.NoError(t, err)
	supervisor.Now = func() time.Time { return now.Add(5 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
	require.NoError(t, err)

	// First deadline: auto-extended (budget 0 -> 1 of 1).
	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "auto_extended", decisions[0].Decision)

	// Second deadline (+21s, one budget window later): the call budget is
	// spent, so the healthy check no longer buys an extension.
	supervisor.Now = func() time.Time { return now.Add(22 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "execution_timed_out", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "execution_timed_out", got.CancelSource)
}

// Explicit rollback: AutoExtendHealthy=false restores the pre-§2.2 forced
// branch even for a run that is demonstrably alive.
func TestAutoExtend_RollbackSwitchKeepsLegacyCut(t *testing.T) {
	interrupter := &fakeInterrupter{}
	cfg := autoExtendTestConfig()
	cfg.AutoExtendHealthy = boolPtr(false)
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-rollback", cfg, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-rollback",
		ExecutionTimeout: 10 * time.Second,
	})
	require.NoError(t, err)
	supervisor.Now = func() time.Time { return now.Add(5 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
	require.NoError(t, err)

	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "execution_timed_out", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, 0, got.ExtensionCount)
}

// Observe mode never mutates: the judgment the enforce pass would take is
// projected as an informational auto_extended decision instead.
func TestAutoExtend_ObserveModeProjectsWithoutMutating(t *testing.T) {
	cfg := autoExtendTestConfig()
	cfg.Mode = "observe"
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-observe", cfg, &fakeInterrupter{}, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-observe",
		ExecutionTimeout: 10 * time.Second,
	})
	require.NoError(t, err)
	supervisor.Now = func() time.Time { return now.Add(5 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
	require.NoError(t, err)

	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "auto_extended", decisions[0].Decision)
	require.Equal(t, "none_observe", decisions[0].ActionTaken)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status)
	require.Equal(t, 0, got.ExtensionCount)
	require.WithinDuration(t, now.Add(10*time.Second), *got.ExecutionDeadlineAt, time.Second,
		"observe mode must not move the deadline")

	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, "auto_extended", notifications[0].EventType)
	require.Equal(t, SeverityInfo, notifications[0].Severity)
}

// §2.3 无人应答安全默认: when nobody answers the escalation and the decision
// window expires, a run that kept making progress is extended instead of
// cancelled; the fallback cancel stays reserved for runs that are still quiet.
func TestAutoExtend_NoResponseWindowExtendsHealthyRun(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-window", ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   1 * time.Hour,
		DefaultProgressTimeout:    5 * time.Second,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-window",
	})
	require.NoError(t, err)

	// +10s: the stall escalates and the window opens (nobody has answered).
	supervisor.Now = func() time.Time { return now.Add(10 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "escalated", decisions[0].ActionTaken)

	// The run keeps working while the window burns down.
	supervisor.Now = func() time.Time { return now.Add(18 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
	require.NoError(t, err)

	// +21s: window expired without a decision. The safe default is the
	// extension, not the cancel.
	supervisor.Now = func() time.Time { return now.Add(21 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "auto_extended", decisions[0].Decision)
	require.Equal(t, "auto_extended", decisions[0].ActionTaken)
	require.Zero(t, interrupter.count(), "the healthy run must not be cancelled")

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status)
	require.Equal(t, 1, got.ExtensionCount)
	require.Nil(t, got.DecisionWindowUntil)

	extended := extendLifecycleEvent(t, store, run.RunID, "obligation.deadline.extended")
	require.Contains(t, extended.Reason, "decision_window")
}

// A parent-granted extension lengthens ProgressDeadlineAt (and with it the
// derived soft threshold), but it must not loosen the liveness check: a run
// quiet for longer than the operator default is not healthy, no matter how
// wide its extended window is.
func TestAutoExtend_ExtendedWindowDoesNotLoosenLiveness(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-liveness", autoExtendTestConfig(), interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	executionDeadline := now.Add(-1 * time.Minute)
	progressDeadline := now.Add(19 * time.Minute) // span 30m: an extended window
	run := ExecutionRun{
		RunID:               "run-auto-liveness",
		Kind:                RunKindAgentRun,
		Workflow:            RunWorkflowSpawnAgent,
		RootSessionID:       "root-session",
		ParentSessionID:     "parent-session",
		SessionID:           "child-liveness",
		AgentID:             "child-liveness",
		Attempt:             1,
		Status:              RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           now.Add(-11 * time.Minute),
		LastHeartbeatAt:     now.Add(-6 * time.Minute),
		LastProgressAt:      now.Add(-6 * time.Minute),
		ProgressSeq:         1,
		ExecutionDeadlineAt: &executionDeadline,
		ProgressDeadlineAt:  &progressDeadline,
		MaxAttempts:         1,
		FencingToken:        1,
		ExtensionCount:      1,
		ExtendedTotal:       20 * time.Minute,
		Version:             1,
		CreatedAt:           now.Add(-11 * time.Minute),
		UpdatedAt:           now.Add(-6 * time.Minute),
	}
	created, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.True(t, created)

	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "execution_timed_out", decisions[0].Decision,
		"6m of silence is beyond the operator default even inside a 30m window")
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())
}

// Acceptance (§2.2): a genuinely active run that outlives the operator
// progress window is never falsely stall-reported. While its meaningful
// progress stays within one soft window the ladder stays quiet; only a real
// quiet spell re-enters the escalation tiers.
func TestAutoExtend_HealthyRunIsNotStallReported(t *testing.T) {
	interrupter := &fakeInterrupter{}
	cfg := autoExtendTestConfig()
	cfg.DefaultExecutionTimeout = 1 * time.Hour
	cfg.DefaultProgressTimeout = 5 * time.Second
	supervisor, store := newTestExecutionSupervisor(t, "sup-auto-extend-no-false-stall", cfg, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-no-false-stall",
		ExecutionTimeout: 1 * time.Hour,
	})
	require.NoError(t, err)

	// Meaningful progress lands inside every soft window: the run stays quiet
	// even though its static progress deadline is long past.
	for i, tick := range []time.Duration{4 * time.Second, 8 * time.Second, 12 * time.Second} {
		supervisor.Now = func() time.Time { return now.Add(tick) }
		_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_end"})
		require.NoError(t, err)
		decisions, err := supervisor.ScanOnce(ctx)
		require.NoError(t, err)
		require.Empty(t, decisions, "scan %d: a ticking run must not be stall-reported", i)
	}
	require.Zero(t, interrupter.count(), "a ticking run is never interrupted")
	notifs, err := store.ListNotifications(ctx, NotificationFilter{IncludeResolved: true})
	require.NoError(t, err)
	require.Empty(t, notifs, "the healthy phase must not report a stall to the parent")

	// The ticks stop: one full soft window of silence re-enters the ladder.
	supervisor.Now = func() time.Time { return now.Add(18 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "progress_stalled", decisions[0].Decision)
	require.Equal(t, "escalated", decisions[0].ActionTaken)
	require.Zero(t, interrupter.count(), "escalate-first still never cancels")
	notifs, err = store.ListNotifications(ctx, NotificationFilter{IncludeResolved: true})
	require.NoError(t, err)
	require.NotEmpty(t, notifs, "a real stall still reaches the parent")
}
