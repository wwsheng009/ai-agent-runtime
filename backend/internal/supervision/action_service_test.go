package supervision

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeActionExecutor struct {
	result ActionResult
	err    error
	called bool
}

func (f *fakeActionExecutor) Execute(ctx context.Context, a ActionRecord) (ActionResult, error) {
	f.called = true
	return f.result, f.err
}

type fakeAuthorizer struct {
	err error
}

func (f fakeAuthorizer) Authorize(ctx context.Context, rootScopeID, requestedByKind, requestedByID, targetKind, targetID string) error {
	return f.err
}

func newActionTestEnv(t *testing.T, name string) (*SQLiteSupervisionStore, *ActionService, *fakeActionExecutor) {
	t.Helper()
	store := newTestStore(t, name)
	executor := &fakeActionExecutor{}
	svc := NewActionService(store, executor, nil)
	return store, svc, executor
}

func seededTimeoutNotification(t *testing.T, store Store, subject string) Notification {
	t.Helper()
	n := testNotification(subject, 1)
	n.TargetParentSessionID = "root-session-1"
	created, err := store.UpsertNotification(context.Background(), n)
	require.NoError(t, err)
	return created
}

// TestActionService_RequestAction_AllowedAndNotAllowed verifies server-side
// allowed-actions revalidation (doc 6.6 constraint 2): a model-passed action
// name is never implicit authorization.
func TestActionService_RequestAction_AllowedAndNotAllowed(t *testing.T) {
	store, svc, _ := newActionTestEnv(t, "supervision-action-request")
	ctx := context.Background()
	seededTimeoutNotification(t, store, "agent-1")

	record, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-1",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
	})
	require.NoError(t, err)
	require.Equal(t, ActionRequested, record.Status)
	require.NotEmpty(t, record.ActionID)

	// retry is not in the allowed set for an agent timeout.
	_, err = svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-1",
		Action:          ActionRetry,
		Reason:          "try again",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed)
}

// TestActionService_RequestAction_NoRecordOnlyInspect verifies that without a
// durable lifecycle record only read-only inspect is permitted.
func TestActionService_RequestAction_NoRecordOnlyInspect(t *testing.T) {
	_, svc, _ := newActionTestEnv(t, "supervision-action-norecord")
	ctx := context.Background()

	_, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "unknown-agent",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed)

	record, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "unknown-agent",
		Action:          ActionInspect,
	})
	require.NoError(t, err)
	require.Equal(t, ActionRequested, record.Status)
}

// TestActionService_RequestAction_StaleCAS verifies expected_version fencing.
func TestActionService_RequestAction_StaleCAS(t *testing.T) {
	store, svc, _ := newActionTestEnv(t, "supervision-action-stale")
	ctx := context.Background()
	created := seededTimeoutNotification(t, store, "agent-stale")

	_, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-stale",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
		ExpectedVersion: created.Version + 5,
	})
	require.ErrorIs(t, err, ErrActionConflict)
}

// TestActionService_AcceptAction_CAS verifies requested -> accepted with a
// CAS guard and no double-accept.
func TestActionService_AcceptAction_CAS(t *testing.T) {
	store, svc, _ := newActionTestEnv(t, "supervision-action-accept")
	ctx := context.Background()
	seededTimeoutNotification(t, store, "agent-accept")

	record, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-accept",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
	})
	require.NoError(t, err)

	accepted, err := svc.AcceptAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, ActionAccepted, accepted.Status)
	require.NotNil(t, accepted.StartedAt)

	_, err = svc.AcceptAction(ctx, record.ActionID)
	require.ErrorIs(t, err, ErrActionNotActionable)
}

// TestActionService_ExecuteAction_TerminalAndResolution verifies doc 6.6
// constraint 7: a successful mutation produces a resolution notification and
// resolves the original item.
func TestActionService_ExecuteAction_TerminalAndResolution(t *testing.T) {
	store, svc, executor := newActionTestEnv(t, "supervision-action-execute")
	ctx := context.Background()
	seededTimeoutNotification(t, store, "agent-exec")

	record, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-exec",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
	})
	require.NoError(t, err)

	executor.result = ActionResult{Status: ActionCompleted, Result: "agent closed"}
	terminal, err := svc.ExecuteAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, ActionCompleted, terminal.Status)
	require.True(t, executor.called)

	// Resolution notification exists for the subject.
	items, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:     "root-session-1",
		SubjectKind:     SubjectAgentRun,
		SubjectID:       "agent-exec",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.Len(t, items, 2, "original + resolution")

	var resolution *Notification
	for i := range items {
		if items[i].EventType == "action_cancel_resolution" {
			resolution = &items[i]
		}
	}
	require.NotNil(t, resolution)
	require.Equal(t, ResolutionClosed, resolution.ResolutionState)
	require.Equal(t, SupervisionRecovered, resolution.SupervisionState)

	// Original item left the action-required set.
	unresolved, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID: "root-session-1",
		SubjectKind: SubjectAgentRun,
		SubjectID:   "agent-exec",
	})
	require.NoError(t, err)
	require.Len(t, unresolved, 0)
}

// TestActionService_ExecuteAction_FailedEmitsUnresolvedOutcome verifies failed
// mutations create a durable lifecycle outcome without resolving the source.
func TestActionService_ExecuteAction_FailedEmitsUnresolvedOutcome(t *testing.T) {
	store, svc, executor := newActionTestEnv(t, "supervision-action-fail")
	ctx := context.Background()
	seededTimeoutNotification(t, store, "agent-fail")

	record, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-fail",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
	})
	require.NoError(t, err)

	executor.err = errors.New("cancel rejected by runtime")
	terminal, err := svc.ExecuteAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, ActionFailed, terminal.Status)

	items, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:     "root-session-1",
		SubjectKind:     SubjectAgentRun,
		SubjectID:       "agent-fail",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.Len(t, items, 2, "source + failed action lifecycle outcome")
	var outcome *Notification
	for i := range items {
		if items[i].EventType == "action_cancel_resolution" {
			outcome = &items[i]
		}
	}
	require.NotNil(t, outcome)
	require.Equal(t, ResolutionUnresolved, outcome.ResolutionState)
	require.Equal(t, SupervisionBlocked, outcome.SupervisionState)
	require.Equal(t, SeverityWarning, outcome.Severity)
}

// TestActionService_AuthorizerDenies verifies scope authorization is enforced
// before any action is persisted (doc 6.6 constraint 6).
func TestActionService_AuthorizerDenies(t *testing.T) {
	store := newTestStore(t, "supervision-action-authz")
	ctx := context.Background()
	seededTimeoutNotification(t, store, "agent-other")
	svc := NewActionService(store, &fakeActionExecutor{}, fakeAuthorizer{
		err: errors.New("not your subtree"),
	})

	_, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-other",
		Action:          ActionCancel,
		Reason:          "deadline exceeded",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed)

	// Nothing was persisted.
	actions, err := store.ListActions(ctx, ActionFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, actions, 0)
}

// TestActionService_RequestAction_Validation verifies structural validation.
func TestActionService_RequestAction_Validation(t *testing.T) {
	_, svc, _ := newActionTestEnv(t, "supervision-action-validate")
	ctx := context.Background()

	_, err := svc.RequestAction(ctx, ActionRequest{
		RequestedByID: "parent-1",
		TargetKind:    SubjectAgentRun,
		TargetID:      "agent-1",
		Action:        ActionCancel,
		Reason:        "deadline exceeded",
	})
	require.ErrorIs(t, err, ErrActionInvalid)

	_, err = svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        "agent-1",
		Action:          ActionCancel,
	})
	require.ErrorIs(t, err, ErrActionInvalid)
}

func TestActionService_CascadeFreeze(t *testing.T) {
	store, svc, _ := newActionTestEnv(t, "supervision-action-cascade")
	ctx := context.Background()

	// A fresh Team notification (do not mutate the AgentRun row returned by
	// seededTimeoutNotification: its NotificationID would collide on insert).
	teamNotif := testNotification("team-1", 1)
	teamNotif.SubjectKind = SubjectTeam
	teamNotif.SupervisionState = SupervisionOrphaned
	_, err := store.UpsertNotification(ctx, teamNotif)
	require.NoError(t, err)

	record, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      SubjectTeam,
		TargetID:        "team-1",
		Action:          ActionCancelSubtree,
		CascadeMode:     CascadeDescendants,
		Reason:          "root team orphaned",
	})
	require.NoError(t, err)

	accepted, err := svc.AcceptAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, ActionAccepted, accepted.Status)

	// The root notification is frozen as canceling with only inspect allowed.
	latest, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:     "root-session-1",
		SubjectKind:     SubjectTeam,
		SubjectID:       "team-1",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.Len(t, latest, 1)
	require.Equal(t, SupervisionCanceling, latest[0].SupervisionState)
	require.Equal(t, DecisionActioned, latest[0].DecisionState)
	require.Equal(t, []string{"inspect"}, latest[0].AllowedActions)
}
