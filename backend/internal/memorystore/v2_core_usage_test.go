package memorystore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newActiveCoreForUsageTest(t *testing.T, cfg Config) (*Store, CoreMemory, Note) {
	t.Helper()
	store, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	source, err := store.Append(AppendNoteOptions{
		Text: "Core usage must remain separate from ordinary memory usage.",
		Kind: "decision",
	})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(context.Background(), CoreMemoryCandidateOptions{
		Content:   "Reviewed project Core usage is recorded in its own ledger.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	active, err := store.ActivateCore(context.Background(), candidate.ID, "admin:core-usage-test")
	require.NoError(t, err)
	return store, active, source
}

func TestRecordCoreExposureUsesDedicatedLedgerAndPreservesOrdinaryUsage(t *testing.T) {
	store, core, source := newActiveCoreForUsageTest(t, Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-usage-project",
	})
	ctx := context.Background()
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	result, err := store.RecordCoreExposure(ctx, CoreExposure{
		CoreIDs:   []string{core.ID, core.ID, "core_missing"},
		SessionID: "session-core-usage",
		TurnID:    "turn-1",
		MessageID: "message-1",
		UseKind:   UsageInjectedOnly,
		UsedAt:    at,
	})
	require.NoError(t, err)
	require.Equal(t, CoreExposureResult{Recorded: 1, Skipped: 1}, result)

	_, err = store.RecordCoreExposure(ctx, CoreExposure{
		CoreIDs:   []string{core.ID},
		SessionID: "session-core-usage",
		TurnID:    "turn-2",
		UseKind:   UsageCited,
		UsedAt:    at.Add(time.Minute),
	})
	require.NoError(t, err)

	rows, err := store.ListCoreUsage(ctx, CoreUsageFilter{
		CoreID: core.ID, SessionID: "session-core-usage", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, core.ID, rows[0].CoreID)
	require.Equal(t, core.Version, rows[0].CoreVersion)
	require.Equal(t, UsageCited, rows[0].UseKind)
	require.Equal(t, UsageInjectedOnly, rows[1].UseKind)
	require.Equal(t, "message-1", rows[1].MessageID)

	var ordinaryUsage int
	require.NoError(t, store.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_usage WHERE project_key = ?
`, store.ProjectKey()).Scan(&ordinaryUsage))
	require.Zero(t, ordinaryUsage, "Core IDs must not be written to memory_usage")

	ordinary, err := store.GetRecord(ctx, source.ID)
	require.NoError(t, err)
	require.Zero(t, ordinary.UseCount)
	require.True(t, ordinary.LastUsedAt.IsZero())
}

func TestRecordCoreExposureValidatesSessionAndKindAndScopesProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-memory.db")
	store, core, _ := newActiveCoreForUsageTest(t, Config{
		Root: t.TempDir(), StorePath: dbPath, Mode: "on", ProjectKey: "core-project-a",
	})
	other, foreignCore, _ := newActiveCoreForUsageTest(t, Config{
		Root: t.TempDir(), StorePath: dbPath, Mode: "on", ProjectKey: "core-project-b",
	})
	require.Equal(t, "core-project-b", other.ProjectKey())
	ctx := context.Background()

	_, err := store.RecordCoreExposure(ctx, CoreExposure{
		CoreIDs: []string{core.ID}, UseKind: UsageCited,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "session id is required")

	_, err = store.RecordCoreExposure(ctx, CoreExposure{
		CoreIDs: []string{core.ID}, SessionID: "s", UseKind: UsageKind("unknown"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid Core usage kind")

	result, err := store.RecordCoreExposure(ctx, CoreExposure{
		CoreIDs: []string{foreignCore.ID}, SessionID: "s", UseKind: UsageRead,
	})
	require.NoError(t, err)
	require.Equal(t, CoreExposureResult{Skipped: 1}, result)
	rows, err := store.ListCoreUsage(ctx, CoreUsageFilter{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestListCoreUsageFiltersAndBoundsLimit(t *testing.T) {
	store, core, _ := newActiveCoreForUsageTest(t, Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-usage-filter",
	})
	ctx := context.Background()
	sessionIDs := []string{"session-a", "session-b", "session-c"}
	turnIDs := []string{"turn-1", "turn-2", "turn-3"}
	for index, kind := range []UsageKind{UsageInjectedOnly, UsageCited, UsageRead} {
		_, err := store.RecordCoreExposure(ctx, CoreExposure{
			CoreIDs:   []string{core.ID},
			SessionID: sessionIDs[index],
			TurnID:    turnIDs[index],
			UseKind:   kind,
			UsedAt:    time.Date(2026, 11, 23, 11+index, 0, 0, 0, time.UTC),
		})
		require.NoError(t, err)
	}

	rows, err := store.ListCoreUsage(ctx, CoreUsageFilter{
		SessionID: "session-b", UseKind: UsageCited, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "turn-2", rows[0].TurnID)

	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	for index := 3; index < 501; index++ {
		_, err := tx.ExecContext(ctx, `
INSERT INTO memory_core_usage (
    id, project_key, core_id, core_version, session_id, used_at, use_kind
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, fmt.Sprintf("core_usage_limit_%03d", index), store.ProjectKey(), core.ID,
			core.Version, "session-limit",
			time.Date(2026, 11, 23, 0, 0, index, 0, time.UTC).Format(time.RFC3339Nano),
			string(UsageInjectedOnly))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())

	rows, err = store.ListCoreUsage(ctx, CoreUsageFilter{Limit: 1000})
	require.NoError(t, err)
	require.Len(t, rows, 500, "the public query must clamp caller-supplied limits")
}

func TestPruneDiagnosticsAlsoPrunesCoreInjectedOnlyExposure(t *testing.T) {
	store, core, _ := newActiveCoreForUsageTest(t, Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "core-usage-retention",
		ExposureRetentionDays: 30,
	})
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		kind UsageKind
		at   time.Time
	}{
		{kind: UsageInjectedOnly, at: now.Add(-40 * 24 * time.Hour)},
		{kind: UsageInjectedOnly, at: now.Add(-time.Hour)},
		{kind: UsageCited, at: now.Add(-40 * 24 * time.Hour)},
	} {
		_, err := store.RecordCoreExposure(ctx, CoreExposure{
			CoreIDs: []string{core.ID}, SessionID: "session-retention",
			UseKind: item.kind, UsedAt: item.at,
		})
		require.NoError(t, err)
	}

	pruned, err := store.PruneDiagnostics(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, pruned.Exposures)

	rows, err := store.ListCoreUsage(ctx, CoreUsageFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Contains(t, []UsageKind{rows[0].UseKind, rows[1].UseKind}, UsageCited)
	require.Contains(t, []UsageKind{rows[0].UseKind, rows[1].UseKind}, UsageInjectedOnly)
}
