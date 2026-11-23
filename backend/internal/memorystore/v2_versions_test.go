package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecordVersionsCaptureUpdatesAndRollbackAsNewRevision(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "versions-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	note, err := store.Append(AppendNoteOptions{
		ID: "mem_versioned", Text: "original memory", Title: "original title",
		Tags: []string{"initial"},
	})
	require.NoError(t, err)

	versions, err := store.ListRecordVersions(ctx, note.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, int64(1), versions[0].Version)
	require.Equal(t, "create", versions[0].ChangeType)
	require.Equal(t, note.ID, versions[0].Record.ID)
	require.NotEmpty(t, versions[0].SourceEventID)

	updatedText := "updated memory"
	updatedTitle := "updated title"
	updated, err := store.UpdateRecord(ctx, note.ID, RecordPatch{
		Text: &updatedText, Title: &updatedTitle,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Revision)

	restored, err := store.RollbackRecord(ctx, note.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: updated.Revision,
		Reason: "restore verified original text",
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), restored.Revision)
	require.Equal(t, "original memory", restored.Text)
	require.Equal(t, "original title", restored.Title)
	require.Equal(t, note.ID, restored.ID)
	require.Equal(t, "versions-project", restored.ProjectKey)

	versions, err = store.ListRecordVersions(ctx, note.ID)
	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, []int64{1, 2, 3}, []int64{
		versions[0].Version, versions[1].Version, versions[2].Version,
	})
	require.Equal(t, "rollback", versions[2].ChangeType)
	require.Equal(t, "restore verified original text", versions[2].ChangeReason)
	require.Equal(t, "updated memory", versions[1].Record.Text)
	require.Equal(t, "original memory", versions[2].Record.Text)
}

func TestRecordStatusChangesAndImportsCloseValidIntervals(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "valid-interval-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	activeRecord, err := store.Append(AppendNoteOptions{ID: "mem_status_expired", Text: "active before expiry"})
	require.NoError(t, err)
	expiredStatus := StatusExpired
	expiredRecord, err := store.UpdateRecord(ctx, activeRecord.ID, RecordPatch{Status: &expiredStatus})
	require.NoError(t, err)
	require.Equal(t, StatusExpired, expiredRecord.Status)
	require.False(t, expiredRecord.ValidUntil.IsZero(), "active-to-expired must close its valid interval")
	beforeExpiry, err := store.ListRecordsAsOf(ctx, RecordFilter{}, expiredRecord.ValidUntil.Add(-time.Nanosecond))
	require.NoError(t, err)
	require.Len(t, beforeExpiry.Items, 1)
	require.Equal(t, activeRecord.ID, beforeExpiry.Items[0].ID)
	versions, err := store.ListRecordVersions(ctx, activeRecord.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.True(t, versions[1].CreatedAt.Equal(expiredRecord.ValidUntil),
		"status transition and version creation must share one operation timestamp")
	atExpiry, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[1].CreatedAt)
	require.NoError(t, err)
	require.Empty(t, atExpiry.Items)

	candidate, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-candidate-expiry", Kind: "fact",
		Text: "candidate before expiry", SourceKind: "session_extract",
	})
	require.NoError(t, err)
	require.True(t, candidate.ValidUntil.IsZero())
	candidateExpiredStatus := StatusExpired
	candidate, err = store.UpdateRecord(ctx, candidate.ID, RecordPatch{Status: &candidateExpiredStatus})
	require.NoError(t, err)
	require.False(t, candidate.ValidUntil.IsZero(),
		"candidate-to-expired must close its valid interval")

	closedAt := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	imported := Record{
		ID: "mem_imported_superseded", Scope: ScopeProject, ProjectKey: "source-project",
		Kind: "fact", Status: StatusSuperseded, Text: "historical imported claim",
		CreatedAt: closedAt.Add(-24 * time.Hour), UpdatedAt: closedAt,
		ValidFrom: closedAt.Add(-24 * time.Hour),
	}
	body, err := json.Marshal(map[string]interface{}{"type": "record", "record": imported})
	require.NoError(t, err)
	result, err := store.ImportJSONL(ctx, body, false)
	require.NoError(t, err)
	require.Equal(t, 1, result.Imported)

	importedRecord, err := store.GetRecord(ctx, imported.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSuperseded, importedRecord.Status)
	require.True(t, importedRecord.ValidUntil.Equal(closedAt),
		"closed imported states without an explicit end should close at their last update time")
}

func TestCurrentListHonorsActiveValidityAndImportPreservesBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "validity-list-project",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	now := time.Now().UTC()
	bounded := Record{
		ID: "mem_bounded_active", Scope: ScopeProject, ProjectKey: "source-project",
		Kind: "bounded", Status: StatusActive, Text: "bounded active fact",
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
		ValidFrom: now.Add(-2 * time.Hour), ValidUntil: now.Add(2 * time.Hour),
	}
	expired := bounded
	expired.ID = "mem_expired_active"
	expired.Kind = "expired"
	expired.Text = "expired active fact"
	expired.ValidUntil = now.Add(-time.Minute)
	boundedLine, err := json.Marshal(map[string]interface{}{"type": "record", "record": bounded})
	require.NoError(t, err)
	expiredLine, err := json.Marshal(map[string]interface{}{"type": "record", "record": expired})
	require.NoError(t, err)
	body := append(boundedLine, '\n')
	body = append(body, expiredLine...)
	result, err := store.ImportJSONL(ctx, body, false)
	require.NoError(t, err)
	require.Equal(t, 2, result.Imported)

	importedBounded, err := store.GetRecord(ctx, bounded.ID)
	require.NoError(t, err)
	require.True(t, importedBounded.ValidUntil.Equal(bounded.ValidUntil))
	page, err := store.ListRecords(ctx, RecordFilter{Kind: "bounded"})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	expiredPage, err := store.ListRecords(ctx, RecordFilter{Kind: "expired"})
	require.NoError(t, err)
	require.Empty(t, expiredPage.Items)
	expiredPage, err = store.ListRecords(ctx, RecordFilter{Kind: "expired", Status: StatusActive})
	require.NoError(t, err)
	require.Empty(t, expiredPage.Items)
}

func TestRecordVersionBaselineBackfillIsIdempotentAndAppendOnly(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "baseline-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	note, err := store.Append(AppendNoteOptions{ID: "mem_baseline", Text: "existing record"})
	require.NoError(t, err)
	db, err := store.ensureV2()
	require.NoError(t, err)

	// Simulate a pre-v20 database row that has no version history. The test DB
	// is temporary; dropping this trigger is only used to construct that
	// migration fixture.
	_, err = db.ExecContext(ctx, `DROP TRIGGER memory_versions_no_delete`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM memory_versions WHERE memory_id = ?`, note.ID)
	require.NoError(t, err)

	require.NoError(t, backfillRecordVersionBaselines(ctx, db))
	require.NoError(t, backfillRecordVersionBaselines(ctx, db))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM memory_versions WHERE memory_id = ? AND version = ?
`, note.ID, 1).Scan(&count))
	require.Equal(t, 1, count)

	var changeType string
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT change_type FROM memory_versions WHERE memory_id = ? AND version = ?
`, note.ID, 1).Scan(&changeType))
	require.Equal(t, "baseline", changeType)

	_, err = db.ExecContext(ctx, `
CREATE TRIGGER memory_versions_no_delete
BEFORE DELETE ON memory_versions
BEGIN
  SELECT RAISE(ABORT, 'memory_versions is append-only');
END;
`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE memory_versions SET change_reason = 'tampered' WHERE memory_id = ?`, note.ID)
	require.Error(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM memory_versions WHERE memory_id = ?`, note.ID)
	require.Error(t, err)
}

func TestListRecordVersionsPageReturnsLatestBoundedWindowInOrder(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "version-page-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	note, err := store.Append(AppendNoteOptions{ID: "mem_version_page", Text: "revision 1"})
	require.NoError(t, err)
	var record Record
	for _, text := range []string{"revision 2", "revision 3", "revision 4"} {
		record, err = store.UpdateRecord(ctx, note.ID, RecordPatch{Text: &text})
		require.NoError(t, err)
	}

	page, hasMore, err := store.ListRecordVersionsPage(ctx, record.ID, 2)
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Len(t, page, 2)
	require.Equal(t, int64(3), page[0].Version)
	require.Equal(t, int64(4), page[1].Version)
	require.Equal(t, "revision 3", page[0].Record.Text)
	require.Equal(t, "revision 4", page[1].Record.Text)

	page, hasMore, err = store.ListRecordVersionsPage(ctx, record.ID, 4)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, page, 4)
	require.Equal(t, int64(1), page[0].Version)
	require.Equal(t, int64(4), page[3].Version)

	_, _, err = store.ListRecordVersionsPage(ctx, record.ID, 501)
	require.Error(t, err)
}

func TestRecordVersionPurgeGuardRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "purge-guard-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	note, err := store.Append(AppendNoteOptions{ID: "mem_guard_failure", Text: "must remain"})
	require.NoError(t, err)
	db, err := store.ensureV2()
	require.NoError(t, err)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = purgeRecordVersionsTx(ctx, tx, store.ProjectKey(), `
DELETE FROM table_that_does_not_exist
`)
	require.Error(t, err)
	require.NoError(t, tx.Commit(), "failed version purge must leave its transaction safe to close")

	var guards, versions int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_version_purge_guards WHERE project_key = ?
`, store.ProjectKey()).Scan(&guards))
	require.Zero(t, guards, "failed purge must not leave an erasure guard enabled")
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_versions WHERE project_key = ? AND memory_id = ?
`, store.ProjectKey(), note.ID).Scan(&versions))
	require.Equal(t, 1, versions, "failed purge must leave history intact")
}

func TestRecordVersionListAndRollbackAreProjectScopedAndRevisionGuarded(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storeA, err := New(Config{Root: root, Mode: "on", ProjectKey: "project-a"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeA.Close()) })
	note, err := storeA.Append(AppendNoteOptions{ID: "mem_project_a", Text: "project A"})
	require.NoError(t, err)

	storeB, err := New(Config{Root: root, Mode: "on", ProjectKey: "project-b"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeB.Close()) })
	_, err = storeB.ListRecordVersions(ctx, note.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = storeB.RollbackRecord(ctx, note.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: 1, Reason: "cross-project read",
	})
	require.ErrorIs(t, err, sql.ErrNoRows)

	text := "changed"
	updated, err := storeA.UpdateRecord(ctx, note.ID, RecordPatch{Text: &text})
	require.NoError(t, err)
	_, err = storeA.RollbackRecord(ctx, note.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: 1, Reason: "stale compare-and-swap",
	})
	require.ErrorContains(t, err, "changed concurrently")

	versions, err := storeA.ListRecordVersions(ctx, note.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.Equal(t, int64(2), updated.Revision)
}

func TestRecordVersionsCoverDeleteRestoreCorrectionAndSupersede(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "lifecycle-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	deleted, err := store.Append(AppendNoteOptions{ID: "mem_delete_restore", Text: "delete then restore"})
	require.NoError(t, err)
	require.NoError(t, store.DeleteRecord(ctx, deleted.ID))
	restored, err := store.RestoreRecord(ctx, deleted.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, restored.Status)
	versions, err := store.ListRecordVersions(ctx, deleted.ID)
	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "delete", versions[1].ChangeType)
	require.Equal(t, "restore", versions[2].ChangeType)
	require.Equal(t, int64(3), versions[2].Version)

	original, err := store.Append(AppendNoteOptions{ID: "mem_correction_target", Text: "old claim"})
	require.NoError(t, err)
	correction, err := store.CorrectRecord(ctx, original.ID, "corrected claim")
	require.NoError(t, err)
	versions, err = store.ListRecordVersions(ctx, original.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.Equal(t, "correct", versions[1].ChangeType)
	require.Equal(t, original.ID, correction.SupersedesID)
	require.Equal(t, int64(1), correction.Revision)
	correctionVersions, err := store.ListRecordVersions(ctx, correction.ID)
	require.NoError(t, err)
	require.Len(t, correctionVersions, 1)
	require.Equal(t, int64(1), correctionVersions[0].Version)
	require.False(t, correctionVersions[0].Record.ValidFrom.IsZero())
	db, err := store.ensureV2()
	require.NoError(t, err)
	var correctionSuppressions int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_suppressions
WHERE source_memory_id = ? AND project_key = ? AND reason = 'corrected'
`, original.ID, store.ProjectKey()).Scan(&correctionSuppressions))
	require.Equal(t, 1, correctionSuppressions)

	rolledBack, err := store.RollbackRecord(ctx, original.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: 2, Reason: "undo correction",
		RevertSupersede: true,
	})
	require.NoError(t, err)
	require.Equal(t, StatusActive, rolledBack.Status)
	require.Equal(t, int64(3), rolledBack.Revision)
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_suppressions
WHERE source_memory_id = ? AND project_key = ? AND reason = 'corrected'
`, original.ID, store.ProjectKey()).Scan(&correctionSuppressions))
	require.Zero(t, correctionSuppressions, "rollback must remove the correction suppression it reverses")
	correctionAfter, err := store.GetRecord(ctx, correction.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSuperseded, correctionAfter.Status)
	require.Equal(t, int64(2), correctionAfter.Revision)
	correctionVersions, err = store.ListRecordVersions(ctx, correction.ID)
	require.NoError(t, err)
	require.Len(t, correctionVersions, 2)
	require.Equal(t, "rollback", correctionVersions[1].ChangeType)

	loser, err := store.Append(AppendNoteOptions{ID: "mem_loser", Text: "losing claim"})
	require.NoError(t, err)
	winner, err := store.Append(AppendNoteOptions{ID: "mem_winner", Text: "winning claim"})
	require.NoError(t, err)
	superseded, err := store.SupersedeRecord(ctx, loser.ID, winner.ID, "consolidation chose winner")
	require.NoError(t, err)
	require.Equal(t, StatusSuperseded, superseded.Status)
	require.False(t, superseded.ValidUntil.IsZero())
	winnerAfter, err := store.GetRecord(ctx, winner.ID)
	require.NoError(t, err)
	require.Equal(t, loser.ID, winnerAfter.SupersedesID)
	loserVersions, err := store.ListRecordVersions(ctx, loser.ID)
	require.NoError(t, err)
	require.Len(t, loserVersions, 2)
	require.Equal(t, "supersede", loserVersions[1].ChangeType)
	winnerVersions, err := store.ListRecordVersions(ctx, winner.ID)
	require.NoError(t, err)
	require.Len(t, winnerVersions, 2)
	require.Equal(t, "supersede", winnerVersions[1].ChangeType)

	loserRestored, err := store.RollbackRecord(ctx, loser.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: superseded.Revision,
		Reason: "revert consolidation", RevertSupersede: true,
	})
	require.NoError(t, err)
	require.Equal(t, StatusActive, loserRestored.Status)
	winnerAfter, err = store.GetRecord(ctx, winner.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSuperseded, winnerAfter.Status)
	require.Empty(t, winnerAfter.SupersedesID, "rollback must remove the reverted supersedes link")
	winnerVersions, err = store.ListRecordVersions(ctx, winner.ID)
	require.NoError(t, err)
	require.Len(t, winnerVersions, 3)
	require.Equal(t, "rollback", winnerVersions[2].ChangeType)
	require.Empty(t, winnerVersions[2].Record.SupersedesID)
}

func TestRecordRollbackHonorsPurgeAndMaintenanceFences(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "rollback-fence-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	note, err := store.Append(AppendNoteOptions{
		ID: "mem_fenced_rollback", Text: "fenced content", SessionID: "purged-session",
	})
	require.NoError(t, err)
	text := "new content"
	updated, err := store.UpdateRecord(ctx, note.ID, RecordPatch{Text: &text})
	require.NoError(t, err)
	db, err := store.ensureV2()
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_session_purges(
    project_key, session_id, generation, state, reason, created_at, updated_at,
    fenced_session_generation, purge_generation
) VALUES (?, ?, 1, 'fenced', 'test', ?, ?, 1, 1)
`, store.ProjectKey(), "purged-session", now, now)
	require.NoError(t, err)

	_, err = store.RollbackRecord(ctx, note.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: updated.Revision, Reason: "must honor purge fence",
	})
	require.ErrorIs(t, err, ErrSessionPurgeGenerationMismatch)

	// A maintenance context is also fenced against stale owner tokens. The
	// invalid token must fail before it can append a rollback version.
	lease, acquired, err := store.AcquireMaintenanceLease(ctx, "rollback-owner", "test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	lease.FencingToken++
	_, err = store.RollbackRecord(WithMaintenanceLease(ctx, lease), note.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: updated.Revision, Reason: "stale maintenance owner",
	})
	require.ErrorIs(t, err, ErrMaintenanceLeaseLost)

	versions, err := store.ListRecordVersions(ctx, note.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
}

func TestRecordRollbackRejectsChangedSupersedingSuccessor(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "successor-rollback-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	loser, err := store.Append(AppendNoteOptions{ID: "mem_successor_loser", Text: "older claim"})
	require.NoError(t, err)
	winner, err := store.Append(AppendNoteOptions{ID: "mem_successor_winner", Text: "newer claim"})
	require.NoError(t, err)
	superseded, err := store.SupersedeRecord(ctx, loser.ID, winner.ID, "prefer newer claim")
	require.NoError(t, err)

	title := "edited after supersede"
	updatedWinner, err := store.UpdateRecord(ctx, winner.ID, RecordPatch{Title: &title})
	require.NoError(t, err)
	require.Equal(t, int64(3), updatedWinner.Revision)

	_, err = store.RollbackRecord(ctx, loser.ID, RecordRollbackOptions{
		TargetVersion: 1, ExpectedRevision: superseded.Revision,
		Reason: "must not discard successor edits", RevertSupersede: true,
	})
	require.ErrorContains(t, err, "successor changed")

	currentLoser, err := store.GetRecord(ctx, loser.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSuperseded, currentLoser.Status)
	require.Equal(t, superseded.Revision, currentLoser.Revision)
	currentWinner, err := store.GetRecord(ctx, winner.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, currentWinner.Status)
	require.Equal(t, int64(3), currentWinner.Revision)
	require.Equal(t, title, currentWinner.Title)

	loserVersions, err := store.ListRecordVersions(ctx, loser.ID)
	require.NoError(t, err)
	require.Len(t, loserVersions, 2, "failed rollback must not append a partial loser version")
	winnerVersions, err := store.ListRecordVersions(ctx, winner.ID)
	require.NoError(t, err)
	require.Len(t, winnerVersions, 3, "failed rollback must not append a partial successor version")
}

func TestListRecordsAsOfTracksCreateUpdateDeleteAndRestore(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "as-of-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	created, err := store.Append(AppendNoteOptions{
		ID: "mem_as_of_lifecycle", Text: "original claim", Kind: "fact",
	})
	require.NoError(t, err)
	versions, err := store.ListRecordVersions(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)

	createdPage, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[0].CreatedAt)
	require.NoError(t, err)
	require.Len(t, createdPage.Items, 1)
	require.Equal(t, "original claim", createdPage.Items[0].Text)

	updatedText := "updated claim"
	updated, err := store.UpdateRecord(ctx, created.ID, RecordPatch{Text: &updatedText})
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Revision)
	versions, err = store.ListRecordVersions(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)

	beforeUpdate, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[0].CreatedAt)
	require.NoError(t, err)
	require.Len(t, beforeUpdate.Items, 1)
	require.Equal(t, "original claim", beforeUpdate.Items[0].Text)
	afterUpdate, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[1].CreatedAt)
	require.NoError(t, err)
	require.Len(t, afterUpdate.Items, 1)
	require.Equal(t, "updated claim", afterUpdate.Items[0].Text)
	require.True(t, versions[1].CreatedAt.Equal(versions[1].Record.ValidFrom),
		"semantic update and version creation must share one operation timestamp")
	require.False(t, versions[1].Record.ValidFrom.After(versions[1].CreatedAt))

	require.NoError(t, store.DeleteRecord(ctx, created.ID))
	versions, err = store.ListRecordVersions(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, versions, 3)
	beforeDelete, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[1].CreatedAt)
	require.NoError(t, err)
	require.Len(t, beforeDelete.Items, 1)
	require.Equal(t, "updated claim", beforeDelete.Items[0].Text)
	afterDelete, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[2].CreatedAt)
	require.NoError(t, err)
	require.Empty(t, afterDelete.Items, "a deleted fact is outside its valid interval")
	require.False(t, versions[2].Record.ValidUntil.IsZero())
	require.False(t, versions[2].Record.ValidUntil.After(versions[2].CreatedAt),
		"the closed interval must end no later than its deletion snapshot")

	db, err := store.ensureV2()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_entries
SET revalidate_after = ?, stale_reason = 'revalidate_due'
WHERE id = ?
`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), created.ID)
	require.NoError(t, err)
	restored, err := store.RestoreRecord(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, restored.Status)
	require.Equal(t, FreshnessFresh, restored.Freshness)
	require.Empty(t, restored.StaleReason)
	require.True(t, restored.RevalidateAfter.IsZero(),
		"restore must not immediately re-stale a freshly verified record")
	versions, err = store.ListRecordVersions(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, versions, 4)
	duringDelete, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[2].CreatedAt)
	require.NoError(t, err)
	require.Empty(t, duringDelete.Items, "restore must not rewrite the historical deleted interval")
	afterRestore, err := store.ListRecordsAsOf(ctx, RecordFilter{}, versions[3].CreatedAt)
	require.NoError(t, err)
	require.Len(t, afterRestore.Items, 1)
	require.Equal(t, StatusActive, afterRestore.Items[0].Status)
	require.Equal(t, "updated claim", afterRestore.Items[0].Text)
	require.False(t, versions[3].Record.ValidFrom.After(versions[3].CreatedAt))
	require.True(t, versions[3].Record.ValidUntil.IsZero())
}

func TestListRecordsAsOfValidatesScopeAndPaginatesWithinProject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", ProjectKey: "as-of-project-a"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	otherProject, err := New(Config{Root: root, Mode: "on", ProjectKey: "as-of-project-b"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, otherProject.Close()) })

	for _, id := range []string{"mem_as_of_a", "mem_as_of_b", "mem_as_of_c"} {
		_, err := store.Append(AppendNoteOptions{ID: id, Text: "project A " + id})
		require.NoError(t, err)
	}
	_, err = otherProject.Append(AppendNoteOptions{ID: "mem_as_of_foreign", Text: "project B"})
	require.NoError(t, err)

	zero := time.Time{}
	_, err = store.ListRecords(ctx, RecordFilter{AsOf: &zero})
	require.ErrorContains(t, err, "as-of time is required")
	_, err = store.ListRecordsAsOf(ctx, RecordFilter{Scope: ScopeUser}, time.Now().UTC())
	require.ErrorContains(t, err, "project scope only")
	_, err = store.ListRecordsAsOf(ctx, RecordFilter{ProjectKey: "as-of-project-b"}, time.Now().UTC())
	require.ErrorContains(t, err, "current project")

	asOf := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	first, err := store.ListRecordsAsOf(ctx, RecordFilter{Limit: 1}, asOf)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, "1", first.NextCursor)
	second, err := store.ListRecordsAsOf(ctx, RecordFilter{Limit: 1, Cursor: first.NextCursor}, asOf)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
	require.NotContains(t, []string{"mem_as_of_foreign"}, first.Items[0].ID)
	require.NotContains(t, []string{"mem_as_of_foreign"}, second.Items[0].ID)
}

func TestListRecordsAsOfUsesParsedTimestampsNotSQLiteTextOrdering(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "as-of-time-project"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	note, err := store.Append(AppendNoteOptions{ID: "mem_as_of_time_order", Text: "before"})
	require.NoError(t, err)

	updatedText := "after"
	_, err = store.UpdateRecord(ctx, note.ID, RecordPatch{Text: &updatedText})
	require.NoError(t, err)
	db, err := store.ensureV2()
	require.NoError(t, err)
	// Model two valid RFC3339Nano timestamps in one second whose lexical order
	// is opposite their chronological order ("...00Z" sorts after "...00.1Z").
	_, err = db.ExecContext(ctx, `
UPDATE memory_versions SET created_at = CASE version
    WHEN 1 THEN '2024-01-01T00:00:00Z'
    WHEN 2 THEN '2024-01-01T00:00:00.1Z'
END
WHERE memory_id = ?
`, note.ID)
	require.Error(t, err, "version snapshots must remain append-only")

	// The read path is intentionally exercised with legal stored timestamp
	// spellings. Build a temporary migration fixture by dropping only the
	// append-only update trigger, then restore it before assertions finish.
	_, err = db.ExecContext(ctx, `DROP TRIGGER memory_versions_no_update`)
	require.NoError(t, err)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for version, validFrom := range map[int64]time.Time{
		1: base,
		2: base.Add(100 * time.Millisecond),
	} {
		var body string
		require.NoError(t, db.QueryRowContext(ctx, `
SELECT record_json FROM memory_versions WHERE memory_id = ? AND version = ?
`, note.ID, version).Scan(&body))
		var snapshot Record
		require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
		snapshot.ValidFrom = validFrom
		snapshot.CreatedAt = validFrom
		updatedBody, marshalErr := json.Marshal(snapshot)
		require.NoError(t, marshalErr)
		_, err = db.ExecContext(ctx, `
UPDATE memory_versions SET created_at = ?, record_json = ?
WHERE memory_id = ? AND version = ?
`, validFrom.Format(time.RFC3339Nano), string(updatedBody), note.ID, version)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `
CREATE TRIGGER memory_versions_no_update
BEFORE UPDATE ON memory_versions
BEGIN
  SELECT RAISE(ABORT, 'memory_versions is append-only');
END
`)
	require.NoError(t, err)

	beforeSecond, err := store.ListRecordsAsOf(ctx, RecordFilter{}, time.Date(2024, 1, 1, 0, 0, 0, 50_000_000, time.UTC))
	require.NoError(t, err)
	require.Len(t, beforeSecond.Items, 1)
	require.Equal(t, "before", beforeSecond.Items[0].Text)
	afterSecond, err := store.ListRecordsAsOf(ctx, RecordFilter{}, time.Date(2024, 1, 1, 0, 0, 0, 100_000_000, time.UTC))
	require.NoError(t, err)
	require.Len(t, afterSecond.Items, 1)
	require.Equal(t, "after", afterSecond.Items[0].Text)
}

func TestSupersedeAuditCapturesBothRevisionsAndInvalidatesWinnerSummary(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "supersede-audit-project",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	insertActive := func(text, sourceID string, confidence float64) Record {
		t.Helper()
		candidate, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "supersede-audit-session", Kind: "decision", Text: text,
			Confidence: confidence, SourceRefs: []string{sourceID},
			SourceHash: sourceID, SourceRevision: 1,
		})
		require.NoError(t, insertErr)
		active, promoteErr := store.PromoteCandidate(ctx, candidate.ID)
		require.NoError(t, promoteErr)
		return active
	}

	loser := insertActive("old claim", "source-old", 0.71)
	winner := insertActive("new claim", "source-new", 0.94)
	summary, err := store.CompileSummary(ctx, Summary{Text: "winner-only summary"}, []string{winner.ID})
	require.NoError(t, err)

	superseded, err := store.SupersedeRecord(ctx, loser.ID, winner.ID, "higher-priority fact")
	require.NoError(t, err)
	require.Equal(t, StatusSuperseded, superseded.Status)
	require.False(t, superseded.ValidUntil.IsZero())

	loserVersions, err := store.ListRecordVersions(ctx, loser.ID)
	require.NoError(t, err)
	require.Len(t, loserVersions, 3)
	winnerVersions, err := store.ListRecordVersions(ctx, winner.ID)
	require.NoError(t, err)
	require.Len(t, winnerVersions, 3)

	events, err := store.ListEvents(ctx, 100)
	require.NoError(t, err)
	var supersedeEvent *AuditEvent
	for i := range events {
		if events[i].EventType == "superseded" && events[i].MemoryID == loser.ID {
			supersedeEvent = &events[i]
			break
		}
	}
	require.NotNil(t, supersedeEvent)
	require.Equal(t, "higher-priority fact", supersedeEvent.Payload["reason"])
	require.Equal(t, "supersede", supersedeEvent.Payload["change_type"])
	require.Equal(t, loser.ID, supersedeEvent.Payload["old_record_id"])
	require.Equal(t, float64(loserVersions[2].Version), supersedeEvent.Payload["old_version"])
	require.Equal(t, winner.ID, supersedeEvent.Payload["new_record_id"])
	require.Equal(t, float64(winnerVersions[2].Version), supersedeEvent.Payload["new_version"])
	require.Equal(t, loser.Confidence, supersedeEvent.Payload["old_confidence"])
	require.Equal(t, winner.Confidence, supersedeEvent.Payload["new_confidence"])
	require.NotEmpty(t, supersedeEvent.Payload["old_source_ids"])
	require.NotEmpty(t, supersedeEvent.Payload["new_source_ids"])
	oldRecord, ok := supersedeEvent.Payload["old_record"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, loser.ID, oldRecord["memory_id"])
	newRecord, ok := supersedeEvent.Payload["new_record"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, winner.ID, newRecord["memory_id"])

	updatedSummary, err := store.GetSummary(ctx, summary.ID)
	require.NoError(t, err)
	require.Equal(t, FreshnessStale, updatedSummary.Freshness,
		"changing the winner's supersede link must invalidate summaries containing it")
}
