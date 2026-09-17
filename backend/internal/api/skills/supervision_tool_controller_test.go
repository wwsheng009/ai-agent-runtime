package skills

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P0-A 方案 A（doc 6.2）：runtime-server（HTTP）宿主的 supervision_descendants
// 验收。CLI 宿主的等价用例在 cmd/aicli/commands/chat_supervision_tools_test.go。

// recordingAPIDescendantProvider captures the scope passed to the provider, so
// the tests can assert the model cannot widen it.
type recordingAPIDescendantProvider struct {
	states []supervision.DescendantState
	scopes []supervision.Scope
}

func (p *recordingAPIDescendantProvider) ListDescendants(_ context.Context, scope supervision.Scope) ([]supervision.DescendantState, error) {
	p.scopes = append(p.scopes, scope)
	return p.states, nil
}

// TestApplyAgentRuntimeServicesGatesSupervisionToolController is the P0-A parity
// gate for the HTTP host (plan §7 conclusion 2 / §8 item 3): the four
// model-facing supervision tools must exist exactly when the host owns the
// durable control plane, mirroring the CLI host. The original gap was that the
// controller could be constructed while every API-path broker kept
// Supervision nil, so the model never saw the tools even though the
// /supervision/* HTTP routes worked.
func TestApplyAgentRuntimeServicesGatesSupervisionToolController(t *testing.T) {
	unwired := &Handler{}
	unwiredAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "supervision-gate-off", Model: "test-model"}, nil)
	unwired.applyAgentRuntimeServices(unwiredAgent, nil)
	if broker := unwiredAgent.GetToolBroker(); broker != nil {
		require.Nil(t, broker.Supervision, "a host without a durable store must not expose supervision tools")
		for _, def := range broker.Definitions() {
			require.NotEqual(t, toolbroker.ToolSupervisionDescendants, def.Name,
				"a dangling supervision tool must never reach Definitions()")
		}
	}

	handler, _ := newAPISupervisionToolTestHandler(t, "api-supervision-wiring")
	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "supervision-gate-on", Model: "test-model"}, nil)
	handler.applyAgentRuntimeServices(apiAgent, nil)
	broker := apiAgent.GetToolBroker()
	require.NotNil(t, broker)
	require.NotNil(t, broker.Supervision, "the durable control plane must reach the model tool surface")

	names := map[string]bool{}
	for _, def := range broker.Definitions() {
		names[def.Name] = true
	}
	for _, want := range []string{
		toolbroker.ToolSupervisionDescendants,
		toolbroker.ToolSupervisionSnapshot,
		toolbroker.ToolReadAgentResult,
		toolbroker.ToolAckLifecycle,
		toolbroker.ToolControlDescendant,
	} {
		require.True(t, names[want], "expected %s in Definitions()", want)
	}
}

func newAPISupervisionToolTestHandler(t *testing.T, name string) (*Handler, *supervision.SQLiteSupervisionStore) {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSupervisionStore(store)
	return handler, store
}

func upsertAPITeamNotification(t *testing.T, store *supervision.SQLiteSupervisionStore, teamID, leadSessionID string) supervision.Notification {
	t.Helper()
	record, err := store.UpsertNotification(context.Background(), supervision.Notification{
		RootScopeID:           teamID,
		TargetParentSessionID: leadSessionID,
		TargetParentTeamID:    teamID,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "worker-team-1",
		SubjectVersion:        1,
		EventSeq:              5,
		EventType:             "timeout",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "execution deadline exceeded",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)
	return record
}

// TestHandlerSupervisionToolController_DescendantsCoversWholeBatch is the P0-A
// acceptance scenario on the HTTP host: a parent that spawned three children
// reads all three in one call (2 running + 1 stalled) instead of polling
// wait_agent row by row.
func TestHandlerSupervisionToolController_DescendantsCoversWholeBatch(t *testing.T) {
	handler, _ := newAPISupervisionToolTestHandler(t, "api-supervision-descendants")

	// Capability gate: no durable store => no controller => the tools stay out
	// of Definitions() entirely.
	require.Nil(t, newHandlerSupervisionToolController(&Handler{}))
	require.Nil(t, newHandlerSupervisionToolController(nil))

	controller := newHandlerSupervisionToolController(handler)
	require.NotNil(t, controller)

	provider := &recordingAPIDescendantProvider{states: []supervision.DescendantState{
		{Kind: supervision.SubjectAgentSession, ID: "worker-1", ExecutionStatus: "running", SupervisionState: supervision.SupervisionRunning, ProgressAgeMs: 1200},
		{Kind: supervision.SubjectAgentSession, ID: "worker-2", ExecutionStatus: "running", SupervisionState: supervision.SupervisionRunning, ProgressAgeMs: 800},
		{Kind: supervision.SubjectAgentSession, ID: "worker-3", ExecutionStatus: "running", SupervisionState: supervision.SupervisionStalled, ProgressAgeMs: 90000, Reason: "no progress event"},
	}}
	handler.SetSupervisionDescendantProvider(provider)

	snapshot, err := controller.SupervisionDescendants(context.Background(), "parent-session", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 3, "one call must cover the whole batch")
	require.Equal(t, 2, snapshot.Summary.Running)
	require.Equal(t, 1, snapshot.Summary.Stalled)
	require.Equal(t, supervision.Scope{RootSessionID: "parent-session"}, provider.scopes[0],
		"the host derives the subtree from the caller session; the model cannot name a scope")

	filtered, err := controller.SupervisionDescendants(context.Background(), "parent-session", toolbroker.SupervisionDescendantsArgs{
		Mode:   "children",
		Health: "abnormal",
		Limit:  1,
	})
	require.NoError(t, err)
	require.Len(t, filtered.Descendants, 1)
	require.Equal(t, "worker-3", filtered.Descendants[0].ID)
	require.Equal(t, "no progress event", filtered.Descendants[0].Reason)
	require.Equal(t, supervision.Scope{RootSessionID: "parent-session", Mode: "children"}, provider.scopes[1])
	require.NotContains(t, provider.scopes[1].RootSessionID, "worker", "the caller scope is never taken from the model")
}

// TestHandlerSupervisionToolController_TeamLeadUsesTeamScope pins the team-lead
// 口径: the projected subtree stays the lead's own session (so agent rows remain
// visible) while the durable root scope stays the team, which is what makes
// team-addressed rows show up in the matrix — the same split the preflight
// digest uses (supervision_handlers.go:277-302).
func TestHandlerSupervisionToolController_TeamLeadUsesTeamScope(t *testing.T) {
	handler, store := newAPISupervisionToolTestHandler(t, "api-supervision-team-lead")
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)

	teamID, err := teamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-tools",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	controller := newHandlerSupervisionToolController(handler)
	notification := upsertAPITeamNotification(t, store, teamID, "lead-session")

	snapshot, err := controller.SupervisionDescendants(context.Background(), "lead-session", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Equal(t, teamID, snapshot.Scope.RootTeamID, "a lead reads its team's durable rows")
	require.Equal(t, "lead-session", snapshot.Scope.RootSessionID, "the projected subtree stays the lead session")
	require.Len(t, snapshot.Descendants, 1)
	require.Equal(t, notification.NotificationID, snapshot.Descendants[0].NotificationID)

	// A session that does not lead the team neither resolves the team scope nor
	// sees the team-addressed row.
	other, err := controller.SupervisionDescendants(context.Background(), "other-session", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Empty(t, other.Scope.RootTeamID)
	require.Empty(t, other.Descendants)
}

// TestSupervisionBudgets_ComeFromHostConfig covers the plan §9 收口: the API host
// used to hardcode the preflight digest budget (20) and the snapshot row cap
// (200) while the CLI read supervision.Config, so the two hosts could silently
// disagree on how much supervision state reaches the model. Both knobs now come
// from the host config, and an explicit tool limit still wins.
func TestSupervisionBudgets_ComeFromHostConfig(t *testing.T) {
	handler, store := newAPISupervisionToolTestHandler(t, "api-supervision-budgets")
	handler.SetSupervisionConfig(supervision.Config{DigestMaxItems: 1, SnapshotMaxItems: 2})
	ctx := context.Background()

	states := make([]supervision.DescendantState, 0, 5)
	for _, id := range []string{"worker-1", "worker-2", "worker-3", "worker-4", "worker-5"} {
		states = append(states, supervision.DescendantState{
			Kind:             supervision.SubjectAgentSession,
			ID:               id,
			ExecutionStatus:  "running",
			SupervisionState: supervision.SupervisionRunning,
		})
	}
	handler.SetSupervisionDescendantProvider(&recordingAPIDescendantProvider{states: states})
	controller := newHandlerSupervisionToolController(handler)
	require.NotNil(t, controller)

	snapshot, err := controller.SupervisionDescendants(ctx, "parent-session", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 2, "SnapshotMaxItems caps the model-facing projection")
	require.True(t, snapshot.Truncated)

	snapshot, err = controller.SupervisionDescendants(ctx, "parent-session", toolbroker.SupervisionDescendantsArgs{Limit: 4})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 4, "an explicit tool limit wins over the host default")

	// DigestMaxItems=1: of three durable critical rows only one may reach the
	// parent turn, otherwise the injection budget silently doubles.
	for i, subjectID := range []string{"timed-out-1", "timed-out-2", "timed-out-3"} {
		_, err := store.UpsertNotification(ctx, supervision.Notification{
			RootScopeID:           "parent-session",
			TargetParentSessionID: "parent-session",
			SubjectKind:           supervision.SubjectAgentRun,
			SubjectID:             subjectID,
			SubjectVersion:        1,
			EventSeq:              int64(i + 1),
			EventType:             "timeout",
			Severity:              supervision.SeverityCritical,
			SupervisionState:      supervision.SupervisionTimedOut,
			DecisionState:         supervision.DecisionUnacknowledged,
			ResolutionState:       supervision.ResolutionUnresolved,
		})
		require.NoError(t, err)
	}
	prompt, err := handler.InjectSupervisionPreflight(ctx, "parent-session", "USER PROMPT", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "USER PROMPT")
	require.Equal(t, 1, strings.Count(prompt, "- agent_run "), "DigestMaxItems caps the injected rows")
}
