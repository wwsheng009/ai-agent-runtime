package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// TestLocalTeamObligationResolverLifecycle pins the CLI team obligation judge:
// only durable terminal team records settle a parked turn; active/paused keep
// it parked, and a missing row is never evidence of completion.
func TestLocalTeamObligationResolverLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	host := &localChatRuntimeHost{TeamStore: store}
	resolver := host.teamObligationResolver()
	require.NotNil(t, resolver)

	terminal, found, err := resolver.TeamTerminal(ctx, "team-missing")
	require.NoError(t, err)
	require.False(t, terminal)
	require.False(t, found, "a missing team row must report found=false, never completion")

	createTeam := func(id string, status team.TeamStatus) {
		t.Helper()
		_, err := store.CreateTeam(ctx, team.Team{
			ID:        id,
			Status:    status,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}

	createTeam("team-active", team.TeamStatusActive)
	terminal, found, err = resolver.TeamTerminal(ctx, "team-active")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, terminal)

	createTeam("team-paused", team.TeamStatusPaused)
	terminal, found, err = resolver.TeamTerminal(ctx, "team-paused")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, terminal, "paused is resumable control state, not completed work")

	for _, tc := range []struct {
		id     string
		status team.TeamStatus
	}{
		{"team-done", team.TeamStatusDone},
		{"team-failed", team.TeamStatusFailed},
		{"team-canceled", team.TeamStatusCanceled},
	} {
		createTeam(tc.id, tc.status)
		terminal, found, err = resolver.TeamTerminal(ctx, tc.id)
		require.NoError(t, err)
		require.True(t, found)
		require.True(t, terminal, "status %s must settle the parked turn", tc.status)
	}

	// Unwired team store ⇒ no resolver (conservative: never clear on a guess).
	empty := &localChatRuntimeHost{}
	require.Nil(t, empty.teamObligationResolver())
}
