package memorystore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newG2TenantStores(t *testing.T, project string) (*Store, *Store) {
	t.Helper()
	root := t.TempDir()
	open := func(tenantID string) *Store {
		store, err := New(Config{
			Root: root, Mode: "on", ProjectKey: project,
			Scope: ScopeContext{TenantID: tenantID, ProjectKey: project},
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	}
	return open("tenant-a"), open("tenant-b")
}

func TestMemoryG2StatusRecordCountsAreTenantScoped(t *testing.T) {
	tenantA, tenantB := newG2TenantStores(t, "g2-status-project")
	ctx := context.Background()

	_, err := tenantA.AppendWithContext(ctx, AppendNoteOptions{Text: "tenant A status marker"})
	require.NoError(t, err)
	_, err = tenantB.AppendWithContext(ctx, AppendNoteOptions{Text: "tenant B status marker"})
	require.NoError(t, err)

	statusA, err := tenantA.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, statusA.Total)
	require.Equal(t, 1, statusA.ManualCount)

	statusB, err := tenantB.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, statusB.Total)
	require.Equal(t, 1, statusB.ManualCount)
}

func TestMemoryG2JobIdentityAndOperatorAreTenantScoped(t *testing.T) {
	ctx := context.Background()
	project := "g2-job-project"

	t.Run("same job key fails closed across tenants", func(t *testing.T) {
		tenantA, tenantB := newG2TenantStores(t, project)
		_, err := tenantA.EnsureJob(ctx, MemoryJob{
			Kind: "phase1_extract", JobKey: "shared-job", ProjectKey: project,
		})
		require.NoError(t, err)
		_, err = tenantB.EnsureJob(ctx, MemoryJob{
			Kind: "phase1_extract", JobKey: "shared-job", ProjectKey: project,
		})
		require.Error(t, err, "a project-only job identity must not resolve another tenant's row")
	})

	t.Run("read claim and operator stay in tenant", func(t *testing.T) {
		tenantA, tenantB := newG2TenantStores(t, project)
		jobA, err := tenantA.EnsureJob(ctx, MemoryJob{
			Kind: "phase1_extract", JobKey: "tenant-a-job", ProjectKey: project,
		})
		require.NoError(t, err)
		jobB, err := tenantB.EnsureJob(ctx, MemoryJob{
			Kind: "phase1_extract", JobKey: "tenant-b-job", ProjectKey: project,
		})
		require.NoError(t, err)

		_, err = tenantB.GetJob(ctx, jobA.ID)
		require.ErrorIs(t, err, sql.ErrNoRows)

		claimedB, err := tenantB.ClaimJobs(ctx, ClaimJobOptions{Limit: 10, WorkerID: "tenant-b-worker"})
		require.NoError(t, err)
		require.Len(t, claimedB, 1)
		require.Equal(t, jobB.ID, claimedB[0].ID)
		require.NotEqual(t, jobA.ID, claimedB[0].ID)

		require.ErrorIs(t, tenantB.DeadLetterJob(ctx, jobA.ID, "cross-tenant attempt"), ErrJobNotFound)
		jobA, err = tenantA.GetJob(ctx, jobA.ID)
		require.NoError(t, err)
		require.Equal(t, JobStatusPending, jobA.Status)
	})
}

func TestMemoryG2CorrectionSuppressionIsTenantScoped(t *testing.T) {
	tenantA, tenantB := newG2TenantStores(t, "g2-correction-project")
	ctx := context.Background()

	noteA, err := tenantA.AppendWithContext(ctx, AppendNoteOptions{Text: "shared correction text"})
	require.NoError(t, err)
	corrected, err := tenantA.CorrectRecord(ctx, noteA.ID, "tenant A corrected text")
	require.NoError(t, err)
	require.Equal(t, "tenant-a", corrected.TenantID)

	var tenantID, scopeKind, scopeKey string
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT tenant_id, scope_kind, scope_key
FROM memory_suppressions
WHERE source_memory_id = ? AND reason = 'corrected'
`, noteA.ID).Scan(&tenantID, &scopeKind, &scopeKey))
	require.Equal(t, "tenant-a", tenantID)
	require.Equal(t, string(ScopeProject), scopeKind)
	require.Equal(t, "g2-correction-project", scopeKey)

	// A's correction suppression must not suppress the same project-scoped
	// content when tenant B writes it independently.
	noteB, err := tenantB.AppendWithContext(ctx, AppendNoteOptions{Text: "shared correction text"})
	require.NoError(t, err)
	require.NotEqual(t, noteA.ID, noteB.ID)
	var noteBTenant string
	require.NoError(t, tenantB.db.QueryRowContext(ctx,
		`SELECT tenant_id FROM memory_entries WHERE id = ?`, noteB.ID).Scan(&noteBTenant))
	require.Equal(t, "tenant-b", noteBTenant)
}

func TestMemoryG2SourceInvalidationIsTenantScoped(t *testing.T) {
	tenantA, tenantB := newG2TenantStores(t, "g2-invalidation-project")
	ctx := context.Background()

	candidateA, err := tenantA.InsertCandidate(ctx, Candidate{
		SessionID: "shared-source-session", SessionGeneration: 1, TurnID: "turn-a",
		Kind: "fact", Text: "tenant A stale marker", Confidence: 0.9,
		SourceHash: "source-a", SourceRevision: 1,
	})
	require.NoError(t, err)
	candidateB, err := tenantB.InsertCandidate(ctx, Candidate{
		SessionID: "shared-source-session", SessionGeneration: 1, TurnID: "turn-b",
		Kind: "fact", Text: "tenant B fresh marker", Confidence: 0.9,
		SourceHash: "source-b", SourceRevision: 1,
	})
	require.NoError(t, err)

	updated, err := tenantA.MarkSessionSourcesStale(ctx, "shared-source-session", "g2-invalidation-project", "test_invalidation")
	require.NoError(t, err)
	require.Equal(t, 1, updated)

	var freshnessA, freshnessB string
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT freshness_status FROM memory_entries WHERE id = ?
`, candidateA.ID).Scan(&freshnessA))
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT freshness_status FROM memory_entries WHERE id = ?
`, candidateB.ID).Scan(&freshnessB))
	require.Equal(t, string(FreshnessStale), freshnessA)
	require.Equal(t, string(FreshnessFresh), freshnessB)
}

func TestMemoryG2VersionHistoryAndRollbackAreTenantScoped(t *testing.T) {
	tenantA, tenantB := newG2TenantStores(t, "g2-version-project")
	ctx := context.Background()

	note, err := tenantA.AppendWithContext(ctx, AppendNoteOptions{Text: "tenant A version v1"})
	require.NoError(t, err)
	corrected, err := tenantA.CorrectRecord(ctx, note.ID, "tenant A version v2")
	require.NoError(t, err)

	versionsA, err := tenantA.ListRecordVersions(ctx, note.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(versionsA), 2)

	_, err = tenantB.ListRecordVersions(ctx, note.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)

	asOfB, err := tenantB.ListRecordsAsOf(ctx, RecordFilter{Limit: 20}, time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, asOfB.Items)

	asOfA, err := tenantA.ListRecordsAsOf(ctx, RecordFilter{Limit: 20}, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, asOfA.Items, 1)
	require.Equal(t, corrected.ID, asOfA.Items[0].ID)

	_, err = tenantB.RollbackRecord(ctx, note.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: corrected.Revision, Reason: "cross tenant rollback",
	})
	require.Error(t, err)
}
