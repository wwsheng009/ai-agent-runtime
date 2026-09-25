package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
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
		// Archive into a temp store so plan-mode tests never touch
		// $HOME/.aicli/plans.
		PlanStore: planstore.NewStore(t.TempDir()),
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

func TestSessionActorEnterPlanModeAllowsAdditionalPlanPaths(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-multi-path-1", runtimepolicy.ModeDefault)
	ctx := context.Background()

	result, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{
		PlanPath: "docs/plan/primary.md",
		PlanWritePaths: []string{
			"docs/plan/child-a.md",
			"docs/plan/child-b.md",
			"docs/plan/primary.md", // duplicate of the primary path must not repeat
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "docs/plan/primary.md", result.PlanPath)
	want := []string{
		"docs/plan/primary.md",
		"docs/plan/child-a.md",
		"docs/plan/child-b.md",
	}
	assert.ElementsMatch(t, want, result.WriteAllowPaths)
	assert.ElementsMatch(t, want, engine.PlanWriteAllowPaths)

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.True(t, planmode.IsActive(state))
	assert.Equal(t, "docs/plan/primary.md", state.PlanPath)
	assert.ElementsMatch(t, want, state.WriteAllowPaths)
}

// TestSessionActorPinsRunMetaToPlanWhilePlanStateActive pins the cross-turn
// contract behind remote smoke test finding (2026-09-18): the host builds the
// run meta from its session snapshot, which can still carry the pre-plan
// bypass_permissions after plan mode was entered via a tool call. policy.Engine
// lets EvalRequest.Mode (from run meta) win over engine.Mode, so without pinning
// every write in the next turn bypassed plan-mode write gating.
func TestSessionActorPinsRunMetaToPlanWhilePlanStateActive(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-run-meta-pin-1", runtimepolicy.ModeBypassPermissions)
	ctx := context.Background()

	// Stale host snapshot: run meta still carries the pre-plan mode.
	// WithRunMeta clones, so the ctx-carried copy is what the loop reads.
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{PermissionMode: string(runtimepolicy.ModeBypassPermissions)})

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "docs/plan/pinned.md"})
	require.NoError(t, err)

	// prepareRun reloads the durable session before run mode is re-applied.
	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	actor.applyDurablePlanModeToRun(runCtx, session)

	liveMeta, ok := team.GetRunMeta(runCtx)
	require.True(t, ok)
	assert.Equal(t, string(runtimepolicy.ModePlan), liveMeta.PermissionMode, "active plan state must pin run meta")
	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode)
	assert.Contains(t, engine.PlanWriteAllowPaths, "docs/plan/pinned.md")

	// Leaving plan mode must stop pinning and leave the caller's mode alone.
	_, err = actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{Decision: "quit"})
	require.NoError(t, err)
	session, err = actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	liveMeta.PermissionMode = string(runtimepolicy.ModeAcceptEdits)
	actor.applyDurablePlanModeToRun(runCtx, session)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), liveMeta.PermissionMode)
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

// newPlanModeTestActorWithStore builds the same fixture as
// newPlanModeTestActor but also exposes the concrete storage so tests can
// simulate a deleted/expired session record.
func newPlanModeTestActorWithStore(t *testing.T, mode runtimepolicy.Mode) (*SessionActor, *Session, *InMemoryStorage) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "plan-mode-user")
	require.NoError(t, err)

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
	return actor, session, storage
}

func TestSessionActorEnterPlanModeTypedSessionNotFound(t *testing.T) {
	actor, session, storage := newPlanModeTestActorWithStore(t, runtimepolicy.ModeDefault)
	ctx := context.Background()

	// Simulate a deleted/expired session record: the store no longer holds it.
	require.NoError(t, storage.Delete(ctx, session.ID))

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.Error(t, err)
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrSessionNotFound), "got %v", err)
	// The historical mislabel: raw "not found" text was classified as a path
	// failure by the broker. The source must emit the typed code instead.
	assert.False(t, runtimeerrors.Is(err, runtimeerrors.ErrToolPathNotFound), "session-not-found must not be classified as a path error")
}

func TestSessionActorPersistSessionTypedSessionNotFound(t *testing.T) {
	actor, session, storage := newPlanModeTestActorWithStore(t, runtimepolicy.ModeDefault)
	ctx := context.Background()

	require.NoError(t, storage.Delete(ctx, session.ID))

	err := actor.persistSession(ctx, session)
	require.Error(t, err)
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrSessionNotFound), "got %v", err)
}

// The agent loop evaluates every tool call with permissionModeFromContext(ctx),
// which reads the RunMeta pointer attached to the run context. A mid-turn plan
// transition must therefore republish the new mode on that very pointer,
// otherwise the rest of the turn keeps the pre-transition mode (observed live
// 2026-09-22: exit_plan_mode returned status=exited while the same-turn write
// and shell calls stayed denied as plan).
func TestSessionActorPlanModeToolsRepublishLiveRunMeta(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-live-meta-1", runtimepolicy.ModeAcceptEdits)
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{
		PermissionMode: string(runtimepolicy.ModeDefault),
	})

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{})
	require.NoError(t, err)
	live, ok := team.GetRunMeta(ctx)
	require.True(t, ok)
	require.NotNil(t, live)
	assert.Equal(t, string(runtimepolicy.ModePlan), live.PermissionMode)
	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Notes:    "ship it",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Active)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), live.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeAcceptEdits, engine.Mode)
}

// Model-authored approve/quit is a review request, not a verdict: the session
// stays in plan mode until the host decides (S0 of
// docs/analysis/commandcode-plan-mode-design-borrowing-20260925.md §4.1).
func TestSessionActorModelApproveBecomesPendingExitRequest(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-model-approve-1", runtimepolicy.ModeDefault)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Notes:    "plan is ready",
		Source:   "model",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Active, "model approve must not exit plan mode")
	assert.Equal(t, "active", result.Status)
	assert.True(t, result.PendingExitRequest)
	assert.Equal(t, "model", result.ExitSource)
	assert.Equal(t, "", result.ExitDecision)
	assert.Equal(t, string(runtimepolicy.ModePlan), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode, "engine must stay in plan mode")

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.True(t, planmode.IsActive(state))
	assert.True(t, state.PendingExitRequest)
	assert.Equal(t, planmode.ExitSourceModel, state.LastExitSource)
	assert.Equal(t, planmode.ExitNone, state.ExitDecision)
	assert.Equal(t, "plan is ready", state.Notes)
}

// Headless/autonomous hosts opt back into direct model verdicts.
func TestSessionActorModelApproveAppliesWithAutonomyEnabled(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "1")
	actor, _, engine := newPlanModeTestActor(t, "plan-model-approve-2", runtimepolicy.ModeAcceptEdits)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Source:   "model",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Active)
	assert.Equal(t, "approve", result.ExitDecision)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeAcceptEdits, engine.Mode)
}

// A user approve must never silently restore bypass_permissions: the session
// lands in accept_edits instead.
func TestSessionActorApproveFromBypassDowngradesToAcceptEdits(t *testing.T) {
	actor, _, engine := newPlanModeTestActor(t, "plan-bypass-approve-1", runtimepolicy.ModeBypassPermissions)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Source:   "user",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Active)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeAcceptEdits, engine.Mode)
}

// User review feedback is recorded for delivery and moves the review round.
func TestSessionActorUserRequestChangesRecordsPendingReviewNotes(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-review-notes-1", runtimepolicy.ModeDefault)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "request_changes",
		Notes:    "add rollback risks",
		Source:   "user",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Active)
	assert.Equal(t, "add rollback risks", result.PendingReviewNotes)
	assert.Equal(t, 1, result.ReviewRound)

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	state := planmode.Load(session)
	assert.Equal(t, "add rollback risks", state.PendingReviewNotes)
	assert.Equal(t, 1, state.ReviewRound)
}

// Model self-revision notes must not be echoed back to the model as user review
// feedback.
func TestSessionActorModelRequestChangesDoesNotQueueReviewNotes(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-review-notes-2", runtimepolicy.ModeDefault)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	_, err = actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "request_changes",
		Notes:    "self revision",
		Source:   "model",
	})
	require.NoError(t, err)

	session, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	assert.Empty(t, planmode.Load(session).PendingReviewNotes)
}

// Review feedback is delivered into the turn channel exactly once.
func TestSessionActorConsumePlanReviewNotesDeliversOnce(t *testing.T) {
	actor, _, _ := newPlanModeTestActor(t, "plan-consume-notes-1", runtimepolicy.ModeDefault)
	ctx := context.Background()

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)
	_, err = actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "request_changes",
		Notes:    "add rollback risks",
		Source:   "user",
	})
	require.NoError(t, err)

	loaded, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	msg := actor.consumePlanReviewNotes(ctx, loaded)
	require.NotNil(t, msg)
	assert.Contains(t, msg.Content, "add rollback risks")
	assert.Equal(t, agent.ReminderKindPlanReview, agent.ReminderKindOf(*msg))

	reloaded, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	assert.Empty(t, planmode.Load(reloaded).PendingReviewNotes)
	assert.Nil(t, actor.consumePlanReviewNotes(ctx, reloaded), "feedback must not be delivered twice")
}
