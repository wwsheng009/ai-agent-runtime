package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryAuditAndExportAreTenantScoped(t *testing.T) {
	root := t.TempDir()
	project := "tenant-export-project"
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
	noteA, err := tenantA.Append(AppendNoteOptions{Text: "tenant A export marker", Kind: "reference"})
	require.NoError(t, err)
	noteB, err := tenantB.Append(AppendNoteOptions{Text: "tenant B export marker", Kind: "reference"})
	require.NoError(t, err)

	eventsA, err := tenantA.ListEvents(ctx, 100)
	require.NoError(t, err)
	require.NotEmpty(t, eventsA)
	for _, event := range eventsA {
		require.NotEqual(t, noteB.ID, event.MemoryID, "tenant A audit must not expose tenant B")
	}
	eventsB, err := tenantB.ListEvents(ctx, 100)
	require.NoError(t, err)
	require.NotEmpty(t, eventsB)
	for _, event := range eventsB {
		require.NotEqual(t, noteA.ID, event.MemoryID, "tenant B audit must not expose tenant A")
	}

	exportA, err := tenantA.ExportJSONL(ctx)
	require.NoError(t, err)
	require.Contains(t, exportA, noteA.ID)
	require.NotContains(t, exportA, noteB.ID)
	exportB, err := tenantB.ExportJSONL(ctx)
	require.NoError(t, err)
	require.Contains(t, exportB, noteB.ID)
	require.NotContains(t, exportB, noteA.ID)

	markdownA, err := tenantA.ExportMarkdown(ctx)
	require.NoError(t, err)
	require.Contains(t, markdownA, "tenant A export marker")
	require.NotContains(t, markdownA, "tenant B export marker")
}
