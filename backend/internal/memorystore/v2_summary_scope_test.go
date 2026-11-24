package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemorySummaryOperationalQueriesAreTenantScoped(t *testing.T) {
	root := t.TempDir()
	project := "tenant-summary-project"
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

	noteA := seedActiveDerivedMemory(t, tenantA, "tenant A summary item", "fact")
	noteB := seedActiveDerivedMemory(t, tenantB, "tenant B summary item", "fact")
	_, err = tenantA.CompileSummary(ctx, Summary{
		Text: "tenant A active summary", SummaryKind: "long_term",
	}, []string{noteA.ID})
	require.NoError(t, err)
	_, err = tenantB.CompileSummary(ctx, Summary{
		Text: "tenant B active summary", SummaryKind: "long_term",
	}, []string{noteB.ID})
	require.NoError(t, err)

	summariesA, err := tenantA.ListSummaries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, summariesA, 1)
	require.Equal(t, "tenant A active summary", summariesA[0].Text)
	summariesB, err := tenantB.ListSummaries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, summariesB, 1)
	require.Equal(t, "tenant B active summary", summariesB[0].Text)

	activeA, err := tenantA.ActiveSummary(ctx)
	require.NoError(t, err)
	require.NotNil(t, activeA)
	require.Equal(t, "tenant A active summary", activeA.Text)
	activeB, err := tenantB.ActiveSummary(ctx)
	require.NoError(t, err)
	require.NotNil(t, activeB)
	require.Equal(t, "tenant B active summary", activeB.Text)
}
