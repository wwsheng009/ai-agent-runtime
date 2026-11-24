package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryJobOperationalQueriesAreTenantScoped(t *testing.T) {
	root := t.TempDir()
	project := "tenant-job-project"
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

	jobA, err := tenantA.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "tenant-a-job", ProjectKey: project,
		Status: JobStatusPending,
	})
	require.NoError(t, err)
	jobB, err := tenantB.EnsureJob(ctx, MemoryJob{
		Kind: "phase1_extract", JobKey: "tenant-b-job", ProjectKey: project,
		Status: JobStatusPending,
	})
	require.NoError(t, err)
	require.NotEqual(t, jobA.ID, jobB.ID)

	jobsA, err := tenantA.ListJobs(ctx, 20)
	require.NoError(t, err)
	require.Len(t, jobsA, 1)
	require.Equal(t, jobA.ID, jobsA[0].ID)
	jobsB, err := tenantB.ListJobs(ctx, 20)
	require.NoError(t, err)
	require.Len(t, jobsB, 1)
	require.Equal(t, jobB.ID, jobsB[0].ID)

	summaryA, err := tenantA.SummarizeJobs(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, summaryA.TotalJobs)
	summaryB, err := tenantB.SummarizeJobs(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, summaryB.TotalJobs)

	statusA, err := tenantA.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, statusA.Jobs["pending"])
	statusB, err := tenantB.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, statusB.Jobs["pending"])

	diagnosticsA, err := tenantA.ListJobDiagnostics(ctx, JobDiagnosticFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, diagnosticsA, 1)
	require.Equal(t, jobA.ID, diagnosticsA[0].ID)
}
