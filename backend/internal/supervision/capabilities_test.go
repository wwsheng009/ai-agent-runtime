package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P2-12 方案 1 regression guard: the announced action set must never promise a
// channel this host has not wired, while the host-neutral set used for
// enforcement stays untouched.
func TestEvaluateAllowedActionsForHostNarrowsToWiredChannels(t *testing.T) {
	evaluator := Evaluator{}
	n := testNotification("agent-cap-1", 1)

	neutral := evaluator.EvaluateAllowedActions(n)
	require.Equal(t,
		[]string{"inspect", "acknowledge", "defer", "cancel", "close"},
		neutral,
		"host-neutral set is the enforcement policy and must not shrink")

	cases := []struct {
		name    string
		caps    *HostCapabilities
		allowed []string
		hint    string
	}{
		{
			name:    "undeclared capabilities keep the neutral set",
			caps:    nil,
			allowed: neutral,
			hint:    "",
		},
		{
			name:    "fully wired host keeps the neutral set",
			caps:    &HostCapabilities{DecisionActions: true, ControlActions: true},
			allowed: neutral,
			hint:    "",
		},
		{
			name:    "decision channel only",
			caps:    &HostCapabilities{DecisionActions: true},
			allowed: []string{"inspect", "acknowledge", "defer"},
			hint:    HintControlRequiresActionExecutor,
		},
		{
			name:    "control channel only",
			caps:    &HostCapabilities{ControlActions: true},
			allowed: []string{"inspect", "cancel", "close"},
			hint:    HintAcknowledgeRequiresLocalCommand,
		},
		{
			name:    "no action channel wired",
			caps:    &HostCapabilities{},
			allowed: []string{"inspect"},
			hint:    HintAcknowledgeRequiresLocalCommand + "; " + HintControlRequiresActionExecutor,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, hint := evaluator.EvaluateAllowedActionsForHost(n, tc.caps)
			require.Equal(t, tc.allowed, allowed)
			require.Equal(t, tc.hint, hint)
		})
	}
}

// A row with nothing to filter must not grow a next_action: the hint is an
// explanation for a removed channel, not decoration for every announcement.
func TestEvaluateAllowedActionsForHostExplainsOnlyFilteredChannels(t *testing.T) {
	evaluator := Evaluator{}
	noChannels := &HostCapabilities{}

	acknowledged := testNotification("agent-cap-acked", 2)
	acknowledged.DecisionState = DecisionAcknowledged
	allowed, hint := evaluator.EvaluateAllowedActionsForHost(acknowledged, noChannels)
	require.Equal(t, []string{"inspect"}, allowed)
	require.Empty(t, hint)

	resolved := testNotification("agent-cap-resolved", 3)
	resolved.ResolutionState = ResolutionClosed
	allowed, hint = evaluator.EvaluateAllowedActionsForHost(resolved, noChannels)
	require.Equal(t, []string{"inspect"}, allowed)
	require.Empty(t, hint)

	// A deferred-but-not-due row is a decision-only action set: a host with a
	// decision channel keeps it, a host without one must drop acknowledge too.
	deferred := testNotification("agent-cap-deferred", 4)
	future := time.Now().Add(time.Hour)
	deferred.DecisionState = DecisionDeferred
	deferred.DeferUntil = &future

	allowed, hint = evaluator.EvaluateAllowedActionsForHost(deferred, &HostCapabilities{DecisionActions: true})
	require.Equal(t, []string{"inspect", "acknowledge"}, allowed)
	require.Empty(t, hint, "the deferred row dropped no channel this host needs")

	allowed, hint = evaluator.EvaluateAllowedActionsForHost(deferred, noChannels)
	require.Equal(t, []string{"inspect"}, allowed)
	require.Equal(t, HintAcknowledgeRequiresLocalCommand, hint,
		"a host with no decision channel must not announce acknowledge")
}

// The preflight digest must announce only executable channels, and the
// next_action hint must tell the parent why the rest is missing.
func TestBuildDigestFiltersAnnouncedActionsByHostCapabilities(t *testing.T) {
	store := newTestStore(t, "supervision-digest-host-caps")
	ctx := context.Background()
	_, err := store.UpsertNotification(ctx, testNotification("agent-cap-digest", 5))
	require.NoError(t, err)

	req := DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Limit:                 20,
		HostCapabilities:      &HostCapabilities{DecisionActions: true},
	}
	digest, err := BuildDigest(ctx, store, req)
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Equal(t, []string{"inspect", "acknowledge", "defer"}, digest.Items[0].AllowedActions)
	require.Equal(t, HintControlRequiresActionExecutor, digest.Items[0].NextAction)
	require.Contains(t, digest.Text, "allowed=[inspect,acknowledge,defer]")
	require.Contains(t, digest.Text, "next_action="+HintControlRequiresActionExecutor)

	// Undeclared capabilities keep the pre-P2-12 wording: a host that has not
	// audited its wiring still learns every remediation path.
	req.HostCapabilities = nil
	digest, err = BuildDigest(ctx, store, req)
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Equal(t, []string{"inspect", "acknowledge", "defer", "cancel", "close"}, digest.Items[0].AllowedActions)
	require.Empty(t, digest.Items[0].NextAction)
	require.Contains(t, digest.Text, "allowed=[inspect,acknowledge,defer,cancel,close]")
	require.NotContains(t, digest.Text, "next_action=")
}

// BuildSnapshot serves the same announcement口径 as the digest, including the
// standalone-notification branch for subjects that are not in runtime state.
func TestBuildSnapshotFiltersAnnouncedActionsByHostCapabilities(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-host-caps")
	ctx := context.Background()
	_, err := store.UpsertNotification(ctx, testNotification("agent-cap-snapshot", 6))
	require.NoError(t, err)

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:            Scope{RootSessionID: "root-session-1"},
		HostCapabilities: &HostCapabilities{ControlActions: true},
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 1)
	item := snapshot.Descendants[0]
	require.Equal(t, "agent-cap-snapshot", item.ID)
	require.Equal(t, []string{"inspect", "cancel", "close"}, item.AllowedActions)
	require.Equal(t, HintAcknowledgeRequiresLocalCommand, item.NextAction)

	snapshot, err = BuildSnapshot(ctx, store, SnapshotRequest{
		Scope: Scope{RootSessionID: "root-session-1"},
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 1)
	item = snapshot.Descendants[0]
	require.Contains(t, item.AllowedActions, "cancel")
	require.Contains(t, item.AllowedActions, "acknowledge")
	require.Empty(t, item.NextAction)
}

// The capability announced by hosts is only as honest as ExecutorReady: a nil
// or cleared executor must not look wired, otherwise preflight would go back to
// advertising cancel/close that can only be recorded.
func TestActionServiceExecutorReadyTracksWiring(t *testing.T) {
	store := newTestStore(t, "supervision-executor-ready")

	var nilService *ActionService
	require.False(t, nilService.ExecutorReady())

	service := NewActionService(store, nil, nil)
	require.False(t, service.ExecutorReady(), "nil executor must not look wired")

	service.SetExecutor(&fakeActionExecutor{})
	require.True(t, service.ExecutorReady())

	service.SetExecutor(nil)
	require.False(t, service.ExecutorReady(), "clearing the executor flips the capability back")
}

// readinessProbeExecutor is the shape of an adapter that must stay installed
// without a runtime hook (so bookkeeping actions keep their semantics and
// wording) while reporting honestly that mutations cannot run. See
// runtimeserver.runtimeActionExecutor.
type readinessProbeExecutor struct {
	ready bool
}

func (readinessProbeExecutor) Execute(context.Context, ActionRecord) (ActionResult, error) {
	return ActionResult{Status: ActionCompleted, Result: "completed"}, nil
}

func (e readinessProbeExecutor) ExecutorReady() bool { return e.ready }

// Installing an executor value is not the same as being able to execute:
// an adapter may be present for the read-only/bookkeeping half of the contract
// and still answer "not ready" until its runtime hook is wired.
func TestActionServiceExecutorReadinessProbeWinsOverWiring(t *testing.T) {
	store := newTestStore(t, "supervision-executor-readiness")

	service := NewActionService(store, readinessProbeExecutor{}, nil)
	require.False(t, service.ExecutorReady(),
		"an installed adapter whose runtime hook is missing must not announce control actions")

	service.SetExecutor(readinessProbeExecutor{ready: true})
	require.True(t, service.ExecutorReady(), "the probe must win once the hook is wired")
}

// Declaring a capability may never widen enforcement: the durable action path
// still recomputes the host-neutral set.
func TestAllowedActionsForHostNeverWidensEnforcementSet(t *testing.T) {
	evaluator := Evaluator{}
	n := testNotification("agent-cap-enforce", 7)

	full, hint := evaluator.EvaluateAllowedActionsForHost(n, &HostCapabilities{
		DecisionActions: true,
		ControlActions:  true,
	})
	require.Empty(t, hint)
	require.Equal(t, evaluator.EvaluateAllowedActions(n), full)

	for _, caps := range []*HostCapabilities{
		nil,
		{},
		{DecisionActions: true},
		{ControlActions: true},
		{DecisionActions: true, ControlActions: true},
	} {
		allowed, _ := evaluator.EvaluateAllowedActionsForHost(n, caps)
		for _, value := range allowed {
			require.Contains(t, evaluator.EvaluateAllowedActions(n), value,
				"capability filtering must only subtract from the neutral set")
			require.False(t, strings.TrimSpace(value) == "", "empty action kind leaked into the announcement")
		}
	}
}

// The wake digest is injected as a parent turn prompt, so it applies the same
// announcement contract as the preflight digest, and it must re-read the
// capability every build: a control plane wires its executor after the
// scheduler exists (SetActionExecutor), so a snapshot taken at construction
// time would report the wrong channel set.
func TestWakeDigestFiltersAnnouncedActionsByHostCapabilities(t *testing.T) {
	store := newTestStore(t, "supervision-wake-host-caps")
	ctx := context.Background()

	controlWired := false
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		HostCapabilities: func() *HostCapabilities {
			caps := FullHostCapabilities()
			caps.ControlActions = controlWired
			return &caps
		},
	})
	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "agent-cap-wake",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	claimed, digest, err := scheduler.DrainRunnable(ctx, "root-session-1", "", "root-session-1",
		func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true })
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Len(t, digest.Items, 1)
	require.Equal(t, []string{"inspect", "acknowledge", "defer"}, digest.Items[0].AllowedActions)
	require.Contains(t, digest.Text, "next_action="+HintControlRequiresActionExecutor)

	// Executor wired later (the CLI/HTTP hosts call SetActionExecutor after the
	// control plane exists): the next digest must announce the control channel.
	controlWired = true
	digest, err = scheduler.RootDigest(ctx, "root-session-1", "root-session-1", "")
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Equal(t, []string{"inspect", "acknowledge", "defer", "cancel", "close"}, digest.Items[0].AllowedActions)
	require.Empty(t, digest.Items[0].NextAction)

	// A scheduler without the resolver stays host-neutral (nil = undeclared).
	undefinedCaps := NewWakeScheduler(store, WakeSchedulerConfig{})
	digest, err = undefinedCaps.RootDigest(ctx, "root-session-1", "root-session-1", "")
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Contains(t, digest.Items[0].AllowedActions, "cancel")
	require.Empty(t, digest.Items[0].NextAction)
}
