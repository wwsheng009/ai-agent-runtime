package chat

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
)

// TestSQLiteSessionStorageKeepsActorOwnedPlanModeContext pins the 2026-09-18
// live finding: host-side writers persist their own pre-turn snapshot of the
// session row and used to erase the plan-mode context the actor wrote mid-turn
// through enter_plan_mode. With the key erased, the next turn loads an inactive
// plan state and plan-mode gating silently disappears.
func TestSQLiteSessionStorageKeepsActorOwnedPlanModeContext(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.CloseStorage()) })

	ctx := context.Background()
	session := NewSession("plan-context-user")
	require.NoError(t, store.Save(ctx, session))

	// Actor write: active plan mode lands mid-turn.
	stored, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	planmode.Save(stored, planmode.Enter("bypass_permissions", "docs/plan.md", "docs/extra.md"))
	require.NoError(t, store.Update(ctx, stored))

	// Host write: stale snapshot whose context never saw plan mode.
	snapshot, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	delete(snapshot.Metadata.Context, planmode.ContextKey)
	snapshot.SetContext("aicli_permission_mode", "bypass_permissions")
	require.NoError(t, store.Update(ctx, snapshot))

	reloaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	state := planmode.Load(reloaded)
	require.True(t, planmode.IsActive(state), "storage update without plan_mode must preserve the actor-owned key: %+v", state)
	require.Equal(t, "docs/plan.md", state.PlanPath)

	// A legitimate exit (explicit inactive state) must still persist.
	planmode.Clear(reloaded)
	require.NoError(t, store.Update(ctx, reloaded))
	again, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.False(t, planmode.IsActive(planmode.Load(again)), "explicit plan-mode exit must not be resurrected")
}
