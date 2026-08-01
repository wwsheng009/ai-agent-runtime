package runtimeserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func newTestSupervisionPlane(t *testing.T) *SupervisionControlPlane {
	t.Helper()
	plane, err := BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, SupervisionRuntimeHooks{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })
	return plane
}

// TestBuildSupervisionControlPlane_Wiring verifies the assembled plane exposes
// store, action service, wake scheduler and descendant provider.
func TestBuildSupervisionControlPlane_Wiring(t *testing.T) {
	plane := newTestSupervisionPlane(t)
	require.NotNil(t, plane.Store)
	require.NotNil(t, plane.Actions)
	require.NotNil(t, plane.Wakes)
	require.NotNil(t, plane.Provider)
}

// TestSupervisionControlPlane_BookkeepingAction completes inspect through the
// full durable pipeline without a runtime executor.
func TestSupervisionControlPlane_BookkeepingAction(t *testing.T) {
	plane := newTestSupervisionPlane(t)
	ctx := context.Background()

	_, err := plane.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "agent-1",
		SubjectVersion:        1,
		EventSeq:              1,
		EventType:             "timeout",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "deadline exceeded",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	record, err := plane.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      supervision.SubjectAgentRun,
		TargetID:        "agent-1",
		Action:          supervision.ActionInspect,
		Reason:          "review timeout",
	})
	require.NoError(t, err)
	require.Equal(t, supervision.ActionRequested, record.Status)

	accepted, err := plane.Actions.AcceptAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, supervision.ActionAccepted, accepted.Status)

	executed, err := plane.Actions.ExecuteAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, supervision.ActionCompleted, executed.Status)
}

// TestSupervisionControlPlane_MutationWithoutExecutor fails durably instead of
// pretending success (doc 6.6 constraint 7).
func TestSupervisionControlPlane_MutationWithoutExecutor(t *testing.T) {
	plane := newTestSupervisionPlane(t)
	ctx := context.Background()

	_, err := plane.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "agent-1",
		SubjectVersion:        1,
		EventSeq:              1,
		EventType:             "timeout",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "deadline exceeded",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	record, err := plane.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      supervision.SubjectAgentRun,
		TargetID:        "agent-1",
		Action:          supervision.ActionCancel,
		Reason:          "stop runaway child",
	})
	require.NoError(t, err)

	_, err = plane.Actions.AcceptAction(ctx, record.ActionID)
	require.NoError(t, err)

	executed, err := plane.Actions.ExecuteAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, supervision.ActionFailed, executed.Status)
	require.Contains(t, executed.Result, "executor not configured")
}

// TestSupervisionControlPlane_MutationWithExecutor wires the real runtime
// executor (close adapter + AgentControl writer) and verifies a cancel action
// closes the live session and persists the terminal subtree state, so the
// durable identity graph agrees with the supervision snapshot (doc 6.2/7.3).
func TestSupervisionControlPlane_MutationWithExecutor(t *testing.T) {
	plane := newTestSupervisionPlane(t)
	ctx := context.Background()

	registry, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	root, err := registry.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "root-agent",
		RootSessionID: "root-session-1",
		SessionID:     "root-session-1",
		AgentPath:     "/root",
		Depth:         0,
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	child, err := registry.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         "child-agent",
		RootSessionID:   "root-session-1",
		ParentAgentID:   root.AgentID,
		ParentSessionID: root.SessionID,
		SessionID:       "child-session-1",
		AgentPath:       "/root/worker",
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)
	require.False(t, child.Closed())

	var closedSessions []string
	plane.SetActionExecutor(SupervisionRuntimeExecutor{
		Store:               plane.Store,
		AgentRegistry:       registry,
		AgentRegistryWriter: registry,
		CloseAgent: func(ctx context.Context, sessionID string) error {
			closedSessions = append(closedSessions, sessionID)
			return nil
		},
	})

	_, err = plane.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-agent",
		SubjectVersion:        1,
		EventSeq:              1,
		EventType:             "timeout",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "deadline exceeded",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	record, err := plane.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "root-session-1",
		TargetKind:      supervision.SubjectAgentRun,
		TargetID:        "child-agent",
		Action:          supervision.ActionCancel,
		Reason:          "stop runaway child",
	})
	require.NoError(t, err)
	require.Equal(t, supervision.ActionRequested, record.Status)

	_, err = plane.Actions.AcceptAction(ctx, record.ActionID)
	require.NoError(t, err)

	executed, err := plane.Actions.ExecuteAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, supervision.ActionCompleted, executed.Status)
	// AgentID "child-agent" resolves through the durable graph to the live
	// session ID before the close adapter runs.
	require.Equal(t, []string{"child-session-1"}, closedSessions)

	records, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: "root-session-1",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	var closed *agentcontrol.AgentRecord
	for i := range records {
		if records[i].AgentID == "child-agent" {
			closed = &records[i]
			break
		}
	}
	require.NotNil(t, closed)
	require.True(t, closed.Closed())
	require.Equal(t, agentcontrol.AgentStatusClosed, closed.Status)
}

// TestSupervisionControlPlane_DefaultAuthorizer rejects empty and cross-scope
// requests conservatively.
func TestSupervisionControlPlane_DefaultAuthorizer(t *testing.T) {
	plane := newTestSupervisionPlane(t)
	ctx := context.Background()

	_, err := plane.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "",
		TargetKind:      supervision.SubjectAgentRun,
		TargetID:        "agent-1",
		Action:          supervision.ActionInspect,
	})
	require.Error(t, err)

	_, err = plane.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-1",
		TargetKind:      supervision.SubjectTeam,
		TargetID:        "other-root-team-9",
		Action:          supervision.ActionCancel,
		Reason:          "cross-scope test",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not authorized")
}

// TestSupervisionControlPlane_ProviderListsDurableChildTeams verifies the
// descendant provider surfaces child Teams from the durable edge table even
// without runtime projection hooks.
func TestSupervisionControlPlane_ProviderListsDurableChildTeams(t *testing.T) {
	plane := newTestSupervisionPlane(t)
	ctx := context.Background()

	edge, err := plane.Store.UpsertTeamEdge(ctx, supervision.TeamEdge{
		RootScopeID:  "root-session-1",
		RootTeamID:   "root-team",
		ParentTeamID: "root-team",
		ParentKind:   "team_lead",
		ParentID:     "lead-1",
		ChildTeamID:  "child-team-1",
		Relation:     "child",
		CreatedBy:    "lead-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, edge.EdgeID)

	states, err := plane.Provider.ListDescendants(ctx, supervision.Scope{
		RootSessionID: "root-session-1",
		RootTeamID:    "root-team",
		Mode:          "descendants",
	})
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.Equal(t, supervision.SubjectTeam, states[0].Kind)
	require.Equal(t, "child-team-1", states[0].ID)
	require.Equal(t, supervision.TeamEdgeStatusActive, states[0].ExecutionStatus)

	// Closed edges are filtered out.
	err = plane.Store.CloseTeamEdge(ctx, edge.EdgeID, time.Now().UTC())
	require.NoError(t, err)
	states, err = plane.Provider.ListDescendants(ctx, supervision.Scope{
		RootSessionID: "root-session-1",
		RootTeamID:    "root-team",
	})
	require.NoError(t, err)
	for _, s := range states {
		require.False(t, strings.EqualFold(s.ID, "child-team-1"), "closed edge must not appear")
	}
}

func TestSupervisionControlPlane_ProviderProjectsAgentControlGraph(t *testing.T) {
	ctx := context.Background()
	agents, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agents.Close() })

	_, err = agents.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "root:root-session",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
	})
	require.NoError(t, err)
	_, err = agents.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         "child-session",
		RootSessionID:   "root-session",
		ParentSessionID: "root-session",
		SessionID:       "child-session",
		AgentPath:       "/root/child-session",
		AgentType:       agentcontrol.AgentTypeChild,
	})
	require.NoError(t, err)

	plane, err := BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, SupervisionRuntimeHooks{
		AgentRegistry: agents,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })

	states, err := plane.Provider.ListDescendants(ctx, supervision.Scope{RootSessionID: "root-session"})
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.Equal(t, supervision.SubjectAgentSession, states[0].Kind)
	require.Equal(t, "child-session", states[0].ID)
	require.Equal(t, supervision.SupervisionRunning, states[0].SupervisionState)
}

func TestSupervisionControlPlane_GraphAuthorizerRejectsForeignAgent(t *testing.T) {
	ctx := context.Background()
	agents, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agents.Close() })
	_, err = agents.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "foreign-child",
		RootSessionID: "foreign-root",
		SessionID:     "foreign-child",
		AgentPath:     "/root/foreign-child",
		AgentType:     agentcontrol.AgentTypeChild,
	})
	require.NoError(t, err)

	plane, err := BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, SupervisionRuntimeHooks{
		AgentRegistry: agents,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })
	_, err = plane.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "root-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentSession,
		SubjectID:             "foreign-child",
		SubjectVersion:        1,
		EventSeq:              1,
		EventType:             "failed",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	_, err = plane.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     "root-session",
		RequestedByKind: "parent_session",
		RequestedByID:   "parent-session",
		TargetKind:      supervision.SubjectAgentSession,
		TargetID:        "foreign-child",
		Action:          supervision.ActionCancel,
		Reason:          "foreign graph must be rejected",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not authorized")
}
