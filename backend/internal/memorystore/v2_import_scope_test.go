package memorystore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryImportIsTenantScoped(t *testing.T) {
	root := t.TempDir()
	project := "tenant-import-project"
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

	importedA := Record{
		ID: "mem_import_tenant_a", Scope: ScopeProject, ProjectKey: project,
		TenantID: "tenant-a", Kind: "fact", Status: StatusActive,
		Text: "tenant A imported record", SourceKind: "import",
	}
	payloadA, err := json.Marshal(map[string]interface{}{"type": "record", "record": importedA})
	require.NoError(t, err)
	resultB, err := tenantB.ImportJSONL(ctx, append(payloadA, '\n'), false)
	require.NoError(t, err)
	require.Equal(t, 1, resultB.Imported)

	recordB, err := tenantB.GetRecord(ctx, importedA.ID)
	require.NoError(t, err)
	require.Equal(t, "tenant-b", recordB.TenantID)
	require.Equal(t, ScopeProject, recordB.ScopeKind)
	require.Equal(t, project, recordB.ScopeKey)
	_, err = tenantA.GetRecord(ctx, importedA.ID)
	require.Error(t, err, "tenant A must not see tenant B import")

	exportA := Record{
		ID: "mem_export_tenant_a", Scope: ScopeProject, ProjectKey: project,
		TenantID: "tenant-a", Kind: "fact", Status: StatusActive,
		Text: "tenant A exported record", SourceKind: "import",
	}
	payloadExportA, err := json.Marshal(map[string]interface{}{"type": "record", "record": exportA})
	require.NoError(t, err)
	resultA, err := tenantA.ImportJSONL(ctx, append(payloadExportA, '\n'), false)
	require.NoError(t, err)
	require.Equal(t, 1, resultA.Imported)
	exported, err := tenantA.ExportJSONL(ctx)
	require.NoError(t, err)

	resultB, err = tenantB.ImportJSONL(ctx, []byte(exported), false)
	require.NoError(t, err)
	require.Equal(t, 0, resultB.Imported)
	_, err = tenantB.GetRecord(ctx, exportA.ID)
	require.Error(t, err, "cross-tenant import with an existing global id must fail closed")
	_, err = tenantA.GetRecord(ctx, exportA.ID)
	require.NoError(t, err)
}
