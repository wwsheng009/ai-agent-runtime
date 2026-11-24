package memorystore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryTurnOperationalQueriesAreTenantScoped(t *testing.T) {
	root := t.TempDir()
	project := "tenant-turn-project"
	tenantA, err := New(Config{
		Root: root, Mode: "on", ProjectKey: project,
		Scope: ScopeContext{TenantID: "tenant-a", ProjectKey: project},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tenantA.Close()) })
	tenantB, err := New(Config{
		Root: root, Mode: "on", ProjectKey: project,
		Scope: ScopeContext{TenantID: "tenant-b", ProjectKey: project},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tenantB.Close()) })
	ctx := context.Background()

	digestA, err := tenantA.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "shared-session", SessionGeneration: 1, TurnID: "shared-turn",
		Outcome: "success", UserText: "tenant A question", AssistantText: "tenant A answer",
	})
	require.NoError(t, err)
	digestB, err := tenantB.RecordTurnDigest(ctx, TurnDigest{
		SessionID: "shared-session", SessionGeneration: 1, TurnID: "shared-turn",
		Outcome: "success", UserText: "tenant B question", AssistantText: "tenant B answer",
	})
	require.NoError(t, err)
	require.NotEqual(t, digestA.ID, digestB.ID,
		"the same project/session/turn identity must coexist across tenants")

	pendingA, err := tenantA.ListPendingTurnDigests(ctx, "shared-session", 10)
	require.NoError(t, err)
	require.Len(t, pendingA, 1)
	require.Equal(t, digestA.ID, pendingA[0].ID)
	pendingB, err := tenantB.ListPendingTurnDigests(ctx, "shared-session", 10)
	require.NoError(t, err)
	require.Len(t, pendingB, 1)
	require.Equal(t, digestB.ID, pendingB[0].ID)

	_, err = tenantA.GetTurnDigestByID(ctx, digestB.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = tenantB.GetTurnDigestByID(ctx, digestA.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)

	countA, err := tenantA.CountPendingTurnDigests(ctx, "")
	require.NoError(t, err)
	require.Equal(t, 1, countA)
	countB, err := tenantB.CountPendingTurnDigests(ctx, "")
	require.NoError(t, err)
	require.Equal(t, 1, countB)

	statusA, err := tenantA.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, statusA.TurnDigests["pending"])
	statusB, err := tenantB.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, statusB.TurnDigests["pending"])

	windowA, _, err := tenantA.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: project, SessionID: "shared-session", SessionGeneration: 1,
		WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		TurnCount: 1, SourceHash: "tenant-a-source", Status: TurnWindowPending,
	})
	require.NoError(t, err)
	windowB, _, err := tenantB.EnsureTurnWindow(ctx, TurnWindow{
		ProjectKey: project, SessionID: "shared-session", SessionGeneration: 1,
		WindowIndex: 1, StartDigestSeq: 1, EndDigestSeq: 1,
		TurnCount: 1, SourceHash: "tenant-b-source", Status: TurnWindowPending,
	})
	require.NoError(t, err)
	require.NotEqual(t, windowA.ID, windowB.ID,
		"the same window identity must coexist across tenants")

	windowsA, err := tenantA.ListTurnWindows(ctx, "shared-session", 10)
	require.NoError(t, err)
	require.Len(t, windowsA, 1)
	require.Equal(t, windowA.ID, windowsA[0].ID)
	windowsB, err := tenantB.ListTurnWindows(ctx, "shared-session", 10)
	require.NoError(t, err)
	require.Len(t, windowsB, 1)
	require.Equal(t, windowB.ID, windowsB[0].ID)

	_, err = tenantA.GetTurnWindow(ctx, windowB.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, tenantA.MarkTurnWindowProcessing(ctx, windowA.ID))
	windowA, err = tenantA.CompleteTurnWindow(ctx, windowA.ID, TurnWindowSummary{Text: "tenant A window"})
	require.NoError(t, err)
	require.Equal(t, TurnWindowDone, windowA.Status)
	require.ErrorIs(t,
		tenantA.MarkTurnWindowProcessingForGeneration(ctx, windowB.ID, 1, windowB.SourceHash),
		sql.ErrNoRows,
		"window state transitions must not cross tenants")

	var tenantID, scopeKind, scopeKey string
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT tenant_id, scope_kind, scope_key FROM memory_turn_digests WHERE id = ?
`, digestA.ID).Scan(&tenantID, &scopeKind, &scopeKey))
	require.Equal(t, "tenant-a", tenantID)
	require.Equal(t, string(ScopeProject), scopeKind)
	require.Equal(t, project, scopeKey)
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT tenant_id, scope_kind, scope_key FROM memory_turn_windows WHERE id = ?
`, windowA.ID).Scan(&tenantID, &scopeKind, &scopeKey))
	require.Equal(t, "tenant-a", tenantID)
	require.Equal(t, string(ScopeProject), scopeKind)
	require.Equal(t, project, scopeKey)
}
