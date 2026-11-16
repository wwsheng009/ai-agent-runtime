package runtimehost

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestTeamOwnerAllowedFencesLiveLease(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	lease, acquired, err := store.AcquireOrchestratorLease(ctx, team.OrchestratorLease{
		TeamID:  "team-1",
		OwnerID: "owner-a",
	}, 2*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease)

	require.True(t, TeamOwnerAllowed(ctx, store, "team-1", "owner-a", now))
	require.False(t, TeamOwnerAllowed(ctx, store, "team-1", "owner-b", now),
		"a live foreign lease must stop a second supervisor from starting")
	require.True(t, TeamOwnerAllowed(ctx, store, "team-2", "", now),
		"an unowned team has no fence to honor")
}
