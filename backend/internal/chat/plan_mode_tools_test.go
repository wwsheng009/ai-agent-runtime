package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func newPlanModeTestActor(t *testing.T, sessionID string, mode runtimepolicy.Mode) (*SessionActor, *Session, *runtimepolicy.Engine) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "plan-mode-user")
	require.NoError(t, err)
	// Force a stable id for session-id checks when requested.
	if sessionID != "" && session.ID != sessionID {
		// Re-save under desired id by cloning into storage with custom id.
		session.ID = sessionID
		require.NoError(t, storage.Save(ctx, session))
	}

	apiAgent := agent.NewAgent(&agent.Config{
		Name:     "plan-mode-agent",
		Model:    "test-model",
		MaxSteps: 1,
	}, nil)
	engine := agent.NewPermissionEngine()
	engine.Mode = mode
	apiAgent.SetPermissionEngine(engine)

	runtimeStore := NewInMemoryRuntimeStore(32)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	// Seed session permission mode context so enter sees the pre-plan mode.
	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	loaded.SetContext(planModePermissionModeKey, string(mode))
	loaded.SetContext(planModeRequestedPermissionModeKey, string(mode))
	loaded.SetContext(planModeEffectivePermissionModeKey, string(mode))
	require.NoError(t, storage.Save(ctx, loaded))

	return actor, loaded, engine
}

func TestSessionActorEnterPlanModeActivatesAndUpdatesEngine(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-enter-1", runtimepolicy.ModeDefault)
	ctx := context.Background()

	result, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{
		PlanPath: "docs/feature-plan.md",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Active)
	assert.Equal(t, "active", result.Status)
	assert.Equal(t, "docs/feature-plan.md", result.PlanPath)
	assert.Equal(t, string(runtimepolicy.ModePlan), result.PermissionMode)
	assert.Equal(t, string(runtimepolicy.ModeDefault), result.PreviousMode)
	require.NotEmpty(t, result.WriteAllowPaths)
	assert.Equal(t, "docs/feature-plan.md", result.WriteAllowPaths[0])

	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode)
	assert.Contains(t, engine.PlanWriteAllowPaths, "docs/feature-plan.md")

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.True(t, planmode.IsActive(state))
	assert.Equal(t, "docs/feature-plan.md", state.PlanPath)

	modeRaw, ok := session.GetContext(planModePermissionModeKey)
	require.True(t, ok)
	assert.Equal(t, string(runtimepolicy.ModePlan), modeRaw)
}

func TestSessionActorEnterPlanModeNestedKeepsOriginalPreviousMode(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-nested-1", runtimepolicy.ModeAcceptEdits)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)
	result, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "docs/revised.md"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Active)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), result.PreviousMode)
	assert.Equal(t, "docs/revised.md", result.PlanPath)

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), state.PreviousMode)
	assert.Equal(t, "docs/revised.md", state.PlanPath)
}

func TestSessionActorExitPlanModeApproveRestoresPreviousMode(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-approve-1", runtimepolicy.ModeAcceptEdits)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{})
	require.NoError(t, err)
	require.Equal(t, runtimepolicy.ModePlan, engine.Mode)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Notes:    "ship it",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Active)
	assert.Equal(t, "exited", result.Status)
	assert.Equal(t, "approve", result.ExitDecision)
	assert.Equal(t, "ship it", result.Notes)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeAcceptEdits, engine.Mode)

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.False(t, planmode.IsActive(state))
	assert.Equal(t, planmode.ExitApprove, state.ExitDecision)
}

func TestSessionActorExitPlanModeRequestChangesStaysActive(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-changes-1", runtimepolicy.ModeDefault)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "request_changes",
		Notes:    "need more risks",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Active)
	assert.Equal(t, "active", result.Status)
	assert.Equal(t, "request_changes", result.ExitDecision)
	assert.Equal(t, "need more risks", result.Notes)
	assert.Equal(t, string(runtimepolicy.ModePlan), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode)

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.True(t, planmode.IsActive(state))
	assert.Equal(t, planmode.ExitRequestChanges, state.ExitDecision)
	assert.False(t, state.PendingExitRequest)
}

func TestSessionActorExitPlanModeQuitRestoresPreviousMode(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-quit-1", runtimepolicy.ModeAcceptEdits)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "quit",
		Notes:    "not now",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Active)
	assert.Equal(t, "quit", result.ExitDecision)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeAcceptEdits, engine.Mode)
}

func TestSessionActorExitPlanModeBarePermissionModeAllowed(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-bare-1", runtimepolicy.ModePlan)
	ctx := context.Background()
	// No durable plan_mode state; only bare permission_mode=plan.
	engine.Mode = runtimepolicy.ModePlan

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{Decision: "quit"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Active)
	assert.Equal(t, string(runtimepolicy.ModeDefault), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeDefault, engine.Mode)
}

func TestSessionActorExitPlanModeRequiresActiveOrPlan(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-inactive-1", runtimepolicy.ModeDefault)
	_, err := actor.ExitPlanMode(context.Background(), "", toolbroker.ExitPlanModeArgs{Decision: "approve"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in plan mode")
}

func TestSessionActorPlanModeToolsRejectOtherSessionID(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-self-1", runtimepolicy.ModeDefault)
	_, err := actor.EnterPlanMode(context.Background(), "other-session", toolbroker.EnterPlanModeArgs{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only operate on the current session")

	_, err = actor.ExitPlanMode(context.Background(), "other-session", toolbroker.ExitPlanModeArgs{Decision: "quit"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only operate on the current session")
}

func TestSessionActorEnterPlanModeUpdatesLiveRunMeta(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-runmeta-1", runtimepolicy.ModeDefault)
	meta := &team.RunMeta{PermissionMode: string(runtimepolicy.ModeDefault)}
	ctx := team.WithRunMeta(context.Background(), meta)

	// Seed runtime state CurrentRunMeta so syncLivePermissionMode can update it.
	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.CurrentRunMeta = &team.RunMeta{PermissionMode: string(runtimepolicy.ModeDefault)}
		return nil
	}))

	result, err := actor.EnterPlanMode(ctx, actor.id, toolbroker.EnterPlanModeArgs{})
	require.NoError(t, err)
	require.NotNil(t, result)

	// WithRunMeta clones; mid-turn updates mutate the context-attached copy
	// (and CurrentRunMeta), not the caller's original pointer.
	liveMeta, ok := team.GetRunMeta(ctx)
	require.True(t, ok)
	require.NotNil(t, liveMeta)
	assert.Equal(t, string(runtimepolicy.ModePlan), liveMeta.PermissionMode)
	assert.Equal(t, string(runtimepolicy.ModeDefault), meta.PermissionMode)

	state := actor.State()
	require.NotNil(t, state)
	require.NotNil(t, state.CurrentRunMeta)
	assert.Equal(t, string(runtimepolicy.ModePlan), state.CurrentRunMeta.PermissionMode)

	_, err = actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{Decision: "approve"})
	require.NoError(t, err)
	liveMeta, ok = team.GetRunMeta(ctx)
	require.True(t, ok)
	assert.Equal(t, string(runtimepolicy.ModeDefault), liveMeta.PermissionMode)
	state = actor.State()
	require.NotNil(t, state.CurrentRunMeta)
	assert.Equal(t, string(runtimepolicy.ModeDefault), state.CurrentRunMeta.PermissionMode)
}

func TestSessionActorEnterPlanModeWorksWhileRunning(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-running-1", runtimepolicy.ModeDefault)
	ctx := context.Background()
	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRunning
		return nil
	}))

	result, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Active)
}
