package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// newPlanReviewBackstopActor builds an actor whose session has a workspace and
// whose events are observable through the returned runtime store.
func newPlanReviewBackstopActor(t *testing.T, mode runtimepolicy.Mode, workspace string) (*SessionActor, *Session, *InMemoryRuntimeStore) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "plan-review-user")
	require.NoError(t, err)
	session.SetContext(planModeWorkspacePathKey, workspace)
	require.NoError(t, manager.Update(ctx, session))

	apiAgent := agent.NewAgent(&agent.Config{Name: "plan-review-agent", Model: "test-model", MaxSteps: 1}, nil)
	engine := agent.NewPermissionEngine()
	engine.Mode = mode
	apiAgent.SetPermissionEngine(engine)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
		PlanStore:    planstore.NewStore(t.TempDir()),
	})
	require.NoError(t, err)

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	loaded.SetContext(planModePermissionModeKey, string(mode))
	loaded.SetContext(planModeRequestedPermissionModeKey, string(mode))
	loaded.SetContext(planModeEffectivePermissionModeKey, string(mode))
	require.NoError(t, storage.Save(ctx, loaded))
	return actor, loaded, runtimeStore
}

func countPlanReviewEvents(t *testing.T, store *InMemoryRuntimeStore, sessionID string) []runtimeevents.Event {
	t.Helper()
	events, err := store.ListEvents(context.Background(), sessionID, 0, 0)
	require.NoError(t, err)
	review := make([]runtimeevents.Event, 0, 1)
	for _, event := range events {
		if event.Type == EventPlanReviewAvailable {
			review = append(review, event)
		}
	}
	return review
}

func writeBackstopPlanFile(t *testing.T, workspace, relPath, content string) {
	t.Helper()
	abs := filepath.Join(workspace, filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

func TestPlanReviewBackstopAnnouncesUnreviewedRevisionOnce(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	planPath := "docs/plan.md"
	writeBackstopPlanFile(t, workspace, planPath, "# Plan\n\nfirst revision\n")

	actor, session, store := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)
	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: planPath})
	require.NoError(t, err)
	// EnterPlanMode persists its own copy; reload so the test observes the
	// durable plan state instead of the pre-enter snapshot.
	session, err = actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)

	actor.maybeAnnouncePlanReview(ctx, session)
	events := countPlanReviewEvents(t, store, session.ID)
	require.Len(t, events, 1)
	require.Equal(t, planPath, events[0].Payload["plan_path"])
	require.Equal(t, "/plan review", events[0].Payload["review_entry_point"])
	require.NotEmpty(t, events[0].Payload["plan_hash"])

	// The same revision must not announce twice.
	actor.maybeAnnouncePlanReview(ctx, session)
	require.Len(t, countPlanReviewEvents(t, store, session.ID), 1)

	// A rewritten plan is a new revision and announces again.
	writeBackstopPlanFile(t, workspace, planPath, "# Plan\n\nsecond revision\n")
	actor.maybeAnnouncePlanReview(ctx, session)
	require.Len(t, countPlanReviewEvents(t, store, session.ID), 2)

	// A pending model verdict request is its own signal: no extra announcement.
	state := planmode.Load(session)
	state.PendingExitRequest = true
	planmode.Save(session, state)
	writeBackstopPlanFile(t, workspace, planPath, "# Plan\n\nthird revision\n")
	actor.maybeAnnouncePlanReview(ctx, session)
	require.Len(t, countPlanReviewEvents(t, store, session.ID), 2)
}

func TestPlanReviewBackstopSkipsNonReviewModesAndEmptyPlans(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	planPath := "docs/plan.md"
	writeBackstopPlanFile(t, workspace, planPath, "# Plan\n\nbody\n")

	// accept_edits skips review even while plan state is active.
	actor, session, store := newPlanReviewBackstopActor(t, runtimepolicy.ModeAcceptEdits, workspace)
	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: planPath})
	require.NoError(t, err)
	session, err = actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	session.SetContext(planModePermissionModeKey, string(runtimepolicy.ModeAcceptEdits))
	require.NoError(t, actor.sessionStore.Save(ctx, session))
	actor.maybeAnnouncePlanReview(ctx, session)
	require.Empty(t, countPlanReviewEvents(t, store, session.ID))

	// An active plan without a written file has nothing to review yet.
	emptyWorkspace := t.TempDir()
	actor2, session2, store2 := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, emptyWorkspace)
	_, err = actor2.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: planPath})
	require.NoError(t, err)
	session2, err = actor2.sessionStore.Load(ctx, actor2.id)
	require.NoError(t, err)
	actor2.maybeAnnouncePlanReview(ctx, session2)
	require.Empty(t, countPlanReviewEvents(t, store2, session2.ID))

	// Inactive plan mode never announces.
	actor3, session3, store3 := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)
	actor3.maybeAnnouncePlanReview(ctx, session3)
	require.Empty(t, countPlanReviewEvents(t, store3, session3.ID))
}

// The plan write exemption on the tool-execution policy must follow durable plan
// state exactly: narrow --allow-tool lists still write the plan file in plan
// mode, and lose the exemption the moment plan mode ends (report §4.7).
func TestSessionActorPlanWriteExemptionTracksPlanState(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false)
	actor.agent.SetToolExecutionPolicy(policy)
	require.Error(t, policy.AllowTool("write"), "the narrow allowlist blocks writes before plan mode")

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "docs/plan.md"})
	require.NoError(t, err)
	require.True(t, policy.PlanModeActive)
	require.NotEmpty(t, policy.PlanWriteAllowPathsResolved,
		"enter must anchor the plan allowlist to the session workspace")
	require.NoError(t, policy.AllowTool("write"),
		"plan mode must let a narrow allowlist write the plan file")

	_, err = actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Source:   string(planmode.ExitSourceUser),
	})
	require.NoError(t, err)
	require.False(t, policy.PlanModeActive)
	require.Error(t, policy.AllowTool("write"), "leaving plan mode must drop the exemption")
}
