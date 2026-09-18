package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
)

// TestPersistSessionKeepsNewerStoredExitedPlanState pins the 2026-09-18 live
// finding: exit_plan_mode mutates and persists a store-loaded session, while the
// actor keeps the run-scoped snapshot that still carries plan_mode=active. The
// end-of-turn persist used to write that stale active state back and resurrect
// plan mode after the user exited. The persist-time merge must prefer the store
// copy whenever it carries the newer lifecycle transition.
func TestPersistSessionKeepsNewerStoredExitedPlanState(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStorage()
	session := NewSession("plan-exit-persist-user")
	require.NoError(t, store.Save(ctx, session))

	// The run starts while plan mode is active and keeps that snapshot.
	active, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	planmode.Save(active, planmode.Enter("bypass_permissions", "plan.md"))
	require.NoError(t, store.Update(ctx, active))

	staleRunSnapshot, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.True(t, planmode.IsActive(planmode.Load(staleRunSnapshot)))

	// Mid-turn exit_plan_mode runs on a fresh store-loaded session.
	mutationSession, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	exited, err := planmode.Exit(planmode.Load(mutationSession), planmode.ExitQuit, "operator exit")
	require.NoError(t, err)
	planmode.Save(mutationSession, exited)
	require.NoError(t, store.Update(ctx, mutationSession))

	// End-of-run persist from the stale snapshot must not resurrect active.
	actor := &SessionActor{sessionStore: store}
	require.NoError(t, actor.persistSession(ctx, staleRunSnapshot))

	persisted, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, planmode.StatusExited, planmode.Load(persisted).Status,
		"stale run snapshot must not resurrect plan mode after a newer exit")
}

// TestPersistSessionLetsNewerOutgoingPlanEntryWin guards the opposite direction:
// enter_plan_mode persists its own freshly mutated copy through the same merge,
// so an older stored row (or none) must never override it.
func TestPersistSessionLetsNewerOutgoingPlanEntryWin(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStorage()
	session := NewSession("plan-enter-persist-user")
	require.NoError(t, store.Save(ctx, session))

	// The row already carries a completed exit from an earlier plan cycle.
	stored, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	exited, err := planmode.Exit(planmode.Enter("default", "plan.md"), planmode.ExitApprove, "approved")
	require.NoError(t, err)
	planmode.Save(stored, exited)
	require.NoError(t, store.Update(ctx, stored))

	// A later enter writes a newer active state through the actor's persist.
	outgoing, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	planmode.Save(outgoing, planmode.Enter("bypass_permissions", "plan.md"))

	actor := &SessionActor{sessionStore: store}
	require.NoError(t, actor.persistSession(ctx, outgoing))

	persisted, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, planmode.StatusActive, planmode.Load(persisted).Status,
		"newer outgoing plan entry must win over an older stored exit")
}
