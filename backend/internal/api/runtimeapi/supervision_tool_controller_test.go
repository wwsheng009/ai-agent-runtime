package runtimeapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	chat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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
// gate for the HTTP host (plan §7 conclusion 2 / §8 item 3): the model-facing
// supervision tools must exist exactly when the host owns the durable control
// plane, mirroring the CLI host. The original gap was that the controller could
// be constructed while every API-path broker kept Supervision nil, so the model
// never saw the tools even though the /supervision/* HTTP routes worked.
//
// 断言口径是**合并后的可见面**（C3-1 收口）：每个能力只广播一个名字
// （subagent_status / subagent_inspect_task / subagent_ack_lifecycle /
// subagent_control），退役的 supervision_* / read_agent_result 名字仍然可调用
// 但不再出现在 Definitions() 里。与 internal/toolbroker/supervision_tools_test.go
// 的同名断言保持同一事实源，避免只改一处就"看起来接好了"。
func TestApplyAgentRuntimeServicesGatesSupervisionToolController(t *testing.T) {
	unwired := &Handler{}
	unwiredAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "supervision-gate-off", Model: "test-model"}, nil)
	unwired.applyAgentRuntimeServices(unwiredAgent, nil)
	if broker := unwiredAgent.GetToolBroker(); broker != nil {
		require.Nil(t, broker.Supervision, "a host without a durable store must not expose supervision tools")
		for _, def := range broker.Definitions() {
			require.False(t, supervisionToolFamilyNames[def.Name],
				"a dangling supervision tool must never reach Definitions(): %s", def.Name)
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
	// 可见面：每个能力一个名字。
	for _, want := range []string{
		toolbroker.ToolSubagentStatus,
		toolbroker.ToolSubagentInspectTask,
		toolbroker.ToolAckLifecycle,
		toolbroker.ToolControlDescendant,
	} {
		require.True(t, names[want], "expected %s in Definitions()", want)
	}
	// 兼容面：退役名字仍可 dispatch（executeSupervisionTool 的别名表未动），
	// 但不再广播 —— 两个名字描述一个能力正是本次合并要消除的漂移。
	for _, gone := range []string{
		toolbroker.ToolSupervisionSnapshot,
		toolbroker.ToolSupervisionDescendants,
		toolbroker.ToolReadAgentResult,
		"ack_lifecycle",
		"control_descendant",
	} {
		require.False(t, names[gone], "legacy supervision name %s must not be advertised", gone)
	}
}

// supervisionToolFamilyNames 是 supervision 控制面**可调用名字**的全集（可见面 +
// 兼容面）。没有控制面的宿主一个都不许广播，所以反向断言按名字集合而不是按单点
// 名字判断：新增/退役一个名字时不会漏掉这处守卫。
var supervisionToolFamilyNames = map[string]bool{
	toolbroker.ToolSupervisionSnapshot:    true,
	toolbroker.ToolSupervisionDescendants: true,
	toolbroker.ToolReadAgentResult:        true,
	"ack_lifecycle":                       true,
	"control_descendant":                  true,
	toolbroker.ToolSubagentStatus:         true,
	toolbroker.ToolSubagentInspectTask:    true,
	toolbroker.ToolAckLifecycle:           true,
	toolbroker.ToolControlDescendant:      true,
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

// F1 回归（HTTP 宿主）：team lead 的 ack/control 必须同时接受会话域与团队域
// 通知，与 /supervision 命令的 chatDebugSupervisionScopes 并集口径一致。
func TestHandlerSupervisionToolController_TeamLeadRetainsSessionScopeForAck(t *testing.T) {
	handler, store := newAPISupervisionToolTestHandler(t, "api-supervision-scope-union")
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)

	teamID, err := teamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-scope-union",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	controller := newHandlerSupervisionToolController(handler)
	require.NotNil(t, controller)
	require.Equal(t, []string{"lead-session", teamID}, controller.decisionScopes(context.Background(), "lead-session"))

	// A direct spawn_agent child of the lead: the durable row is rooted at the
	// session, not the team. The digest advertises it; ack must accept it.
	record, err := store.UpsertNotification(context.Background(), supervision.Notification{
		RootScopeID:           "lead-session",
		TargetParentSessionID: "lead-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "session-scope-child",
		SubjectVersion:        1,
		EventSeq:              3,
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
		Reason:                "child failed before the team existed",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	updated, err := controller.AckLifecycle(context.Background(), "lead-session", toolbroker.AckLifecycleArgs{
		NotificationID: record.NotificationID,
		Decision:       "acknowledge",
		Note:           "lead handles its own direct child",
	})
	require.NoError(t, err, "the live regression: a session-scope row must stay actionable for the lead")
	require.Equal(t, supervision.DecisionAcknowledged, updated.DecisionState)

	_, err = controller.AckLifecycle(context.Background(), "other-session", toolbroker.AckLifecycleArgs{
		NotificationID: record.NotificationID,
		Decision:       "acknowledge",
		Note:           "foreign caller",
	})
	require.ErrorIs(t, err, supervision.ErrActionNotAllowed, "scope stays enforced for everyone else")
}

// F2 回归（HTTP 宿主）：与 CLI 宿主保持同一口径 —— stop 状态优先于
// success=false 的失败启发式。
func TestAgentCompletionStatus_StopStatusWinsOverFailureHeuristic(t *testing.T) {
	stopped := runtimeevents.Event{
		Type: chat.EventSessionEnd,
		Payload: map[string]interface{}{
			"success": false,
			"status":  string(chat.SessionStopped),
			"error":   "context canceled",
		},
	}
	require.Equal(t, string(chat.SessionStopped), agentCompletionStatus(stopped))

	failed := runtimeevents.Event{
		Type: chat.EventSessionEnd,
		Payload: map[string]interface{}{
			"success": false,
			"status":  string(chat.SessionIdle),
			"error":   "provider unavailable",
		},
	}
	require.Equal(t, "failed", agentCompletionStatus(failed),
		"a genuine failure keeps the failed classification")

	interrupted := runtimeevents.Event{Type: chat.EventSessionInterrupted}
	require.Equal(t, string(chat.SessionStopped), agentCompletionStatus(interrupted))
}
