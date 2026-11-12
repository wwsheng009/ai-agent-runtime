package memorystore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPruneDiagnosticsKeepsRecentAndCitedUsage(t *testing.T) {
	store, err := New(Config{
		ProjectRoot:              t.TempDir(),
		Mode:                     "on",
		RecallTraceRetentionDays: 14,
		ExposureRetentionDays:    30,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	note, err := store.Append(AppendNoteOptions{Text: "retention memory"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO memory_recall_traces (
    id, session_id, project_key, query_hash, selected_ids_json, dropped_json,
    token_estimate, token_budget, injected, created_at
) VALUES
    (?, 's1', ?, 'old', '[]', '[]', 0, 600, 0, ?),
    (?, 's1', ?, 'new', '[]', '[]', 0, 600, 0, ?)
`, "trace_old", store.ProjectKey(), now.Add(-20*24*time.Hour).Format(time.RFC3339Nano),
		"trace_new", store.ProjectKey(), now.Add(-1*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert recall traces: %v", err)
	}
	for _, item := range []struct {
		kind string
		at   time.Time
	}{
		{kind: string(UsageInjectedOnly), at: now.Add(-40 * 24 * time.Hour)},
		{kind: string(UsageInjectedOnly), at: now.Add(-1 * 24 * time.Hour)},
		{kind: string(UsageCited), at: now.Add(-40 * 24 * time.Hour)},
	} {
		if _, err := store.RecordExposure(ctx, Exposure{
			MemoryIDs: []string{note.ID}, SessionID: "s1", UseKind: UsageKind(item.kind), UsedAt: item.at,
		}); err != nil {
			t.Fatalf("RecordExposure(%s): %v", item.kind, err)
		}
	}
	result, err := store.PruneDiagnostics(ctx, now)
	if err != nil {
		t.Fatalf("PruneDiagnostics: %v", err)
	}
	if result.RecallTraces != 1 || result.Exposures != 1 {
		t.Fatalf("pruned = %+v", result)
	}
	var traces, injected, cited int
	if err := db.QueryRow(`SELECT COUNT(1) FROM memory_recall_traces WHERE project_key = ?`, store.ProjectKey()).Scan(&traces); err != nil {
		t.Fatalf("count traces: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM memory_usage WHERE memory_id = ? AND use_kind = 'injected_only'`, note.ID).Scan(&injected); err != nil {
		t.Fatalf("count exposures: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM memory_usage WHERE memory_id = ? AND use_kind = 'cited'`, note.ID).Scan(&cited); err != nil {
		t.Fatalf("count cited: %v", err)
	}
	if traces != 1 || injected != 1 || cited != 1 {
		t.Fatalf("remaining traces=%d injected=%d cited=%d", traces, injected, cited)
	}
}

func TestPruneDiagnosticsNoopWhenRetentionDisabled(t *testing.T) {
	store, err := New(Config{ProjectRoot: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	result, err := store.PruneDiagnostics(context.Background(), time.Now().UTC())
	if err != nil || result != (DiagnosticsPruneResult{}) {
		t.Fatalf("disabled prune = %+v, %v", result, err)
	}
}

func TestSweepLifecycleExpiresCandidatesAndUnusedRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on",
		Retention: RetentionPolicy{
			Enabled: true, CandidateDays: 30, EphemeralDays: 14,
			ActiveUnusedDays: 30,
			Classes: RetentionClasses{
				Permanent: []string{"decision"},
				Standard:  []string{"fact"},
				Ephemeral: []string{"task_trace"},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	insert := func(kind, text, hash string) Record {
		t.Helper()
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "s1", Kind: kind, Text: text, Confidence: 0.9,
			SourceHash: hash, SourceRevision: 1, RetentionClass: "standard",
		})
		require.NoError(t, insertErr)
		return record
	}
	candidate := insert("fact", "stale candidate", "hash-candidate")
	active := insert("fact", "unused active", "hash-active")
	active, err = store.PromoteCandidate(ctx, active.ID)
	require.NoError(t, err)
	permanent := insert("decision", "keep this decision", "hash-permanent")
	permanent, err = store.PromoteCandidate(ctx, permanent.ID)
	require.NoError(t, err)
	pinned := insert("fact", "pinned fact", "hash-pinned")
	pinned, err = store.PromoteCandidate(ctx, pinned.ID)
	require.NoError(t, err)
	manual, err := store.Append(AppendNoteOptions{
		Text: "manual note", Kind: "fact", CreatedAt: now.Add(-90 * 24 * time.Hour),
	})
	require.NoError(t, err)

	db, err := store.ensureV2()
	require.NoError(t, err)
	old := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
UPDATE memory_entries SET updated_at = ?, created_at = ?, last_used_at = NULL
WHERE id IN (?, ?, ?, ?)
`, old, old, candidate.ID, active.ID, permanent.ID, pinned.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE memory_entries SET pinned = 1 WHERE id = ?`, pinned.ID)
	require.NoError(t, err)

	result, err := store.SweepLifecycle(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.CandidatesExpired)
	require.EqualValues(t, 1, result.ActiveExpired)

	candidate, err = store.GetRecord(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, candidate.Status)
	active, err = store.GetRecord(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, active.Status)
	permanent, err = store.GetRecord(ctx, permanent.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, permanent.Status, "permanent class must not expire")
	pinned, err = store.GetRecord(ctx, pinned.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, pinned.Status, "pinned record must not expire")
	manualRecord, err := store.GetRecord(ctx, manual.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, manualRecord.Status, "manual record must not expire")

	var expiredEvents int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_events WHERE project_key = ? AND event_type = 'expired'
`, store.ProjectKey()).Scan(&expiredEvents))
	require.Equal(t, 2, expiredEvents)
}

func TestSweepLifecycleCapsNonPermanentRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on",
		Retention: RetentionPolicy{
			Enabled: true, MaxRecords: 2,
			Classes: RetentionClasses{Standard: []string{"fact"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var ids []string
	for index := 0; index < 3; index++ {
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "s1", Kind: "fact", Text: fmt.Sprintf("fact %d", index),
			Confidence: 0.9, SourceHash: fmt.Sprintf("hash-%d", index), SourceRevision: 1,
			RetentionClass: "standard",
		})
		require.NoError(t, insertErr)
		record, err = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, err)
		ids = append(ids, record.ID)
	}
	db, err := store.ensureV2()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_entries SET use_count = 5, last_used_at = ? WHERE id = ?
`, now.Add(-time.Hour).Format(time.RFC3339Nano), ids[1])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_entries SET use_count = 9, last_used_at = ? WHERE id = ?
`, now.Add(-time.Hour).Format(time.RFC3339Nano), ids[2])
	require.NoError(t, err)

	result, err := store.SweepLifecycle(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.MaxRecordsExpired)
	records, err := store.ListRecords(ctx, RecordFilter{Status: StatusActive, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, records.Count)
	for _, record := range records.Items {
		require.NotEqual(t, ids[0], record.ID, "lowest-use oldest record should be the one expired")
	}
}

func TestSweepLifecyclePurgesTombstonesAuditUsageAndExpiredSuppressions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on",
		Retention: RetentionPolicy{
			Enabled: true, DeletedDays: 30, AuditDays: 180,
			Classes: RetentionClasses{Standard: []string{"fact"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "s1", Kind: "fact", Text: "delete me after retention",
		Confidence: 0.9, SourceHash: "hash-delete", SourceRevision: 1, RetentionClass: "standard",
	})
	require.NoError(t, err)
	record, err = store.PromoteCandidate(ctx, record.ID)
	require.NoError(t, err)
	_, err = store.RecordExposure(ctx, Exposure{
		MemoryIDs: []string{record.ID}, SessionID: "s1", UseKind: UsageCited,
		UsedAt: now.Add(-200 * 24 * time.Hour),
	})
	require.NoError(t, err)
	_, err = store.RecordExposure(ctx, Exposure{
		MemoryIDs: []string{record.ID}, SessionID: "s1", UseKind: UsageRead,
		UsedAt: now.Add(-24 * time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, store.DeleteRecord(ctx, record.ID))

	db, err := store.ensureV2()
	require.NoError(t, err)
	old := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `UPDATE memory_entries SET deleted_at = ? WHERE id = ?`, old, record.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_events SET created_at = ? WHERE memory_id = ? AND project_key = ?
`, now.Add(-200*24*time.Hour).Format(time.RFC3339Nano), record.ID, store.ProjectKey())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_suppressions (
    id, scope, project_key, content_hash, reason, source_memory_id, created_at, expires_at, metadata_json
) VALUES ('sup-expired', 'project', ?, 'expired-hash', 'deleted', NULL, ?, ?, '{}')
`, store.ProjectKey(), now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-time.Minute).Format(time.RFC3339Nano))
	require.NoError(t, err)

	result, err := store.SweepLifecycle(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.TombstonesDeleted)
	require.EqualValues(t, 1, result.UsageDeleted)
	require.GreaterOrEqual(t, result.EventsDeleted, int64(1))
	require.EqualValues(t, 1, result.SuppressionsPurged)

	if _, err := store.GetRecord(ctx, record.ID); err == nil {
		t.Fatalf("tombstone should be physically deleted")
	}
	var sources, liveSuppressions, recentUsage int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(1) FROM memory_sources WHERE memory_id = ?`, record.ID).Scan(&sources))
	require.Zero(t, sources)
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_suppressions WHERE project_key = ? AND source_memory_id = ? AND reason = 'deleted'
`, store.ProjectKey(), record.ID).Scan(&liveSuppressions))
	require.Equal(t, 1, liveSuppressions, "live suppression must survive tombstone purge")
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_usage WHERE project_key = ? AND use_kind = 'read' AND used_at > ?
`, store.ProjectKey(), now.Add(-2*24*time.Hour).Format(time.RFC3339Nano)).Scan(&recentUsage))
	require.Equal(t, 1, recentUsage, "usage inside audit window must survive")
}

func TestSweepLifecycleReclassifiesDerivedKindsFromConfig(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on",
		Retention: RetentionPolicy{
			Enabled: true,
			Classes: RetentionClasses{Permanent: []string{"decision"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "s1", Kind: "decision", Text: "reclassify me",
		Confidence: 0.9, SourceHash: "hash-reclass", SourceRevision: 1, RetentionClass: "standard",
	})
	require.NoError(t, err)
	unlisted, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "s1", Kind: "workflow", Text: "unlisted kind falls back to standard",
		Confidence: 0.9, SourceHash: "hash-reclass-unlisted", SourceRevision: 1, RetentionClass: "permanent",
	})
	require.NoError(t, err)
	result, err := store.SweepLifecycle(ctx, now)
	require.NoError(t, err)
	require.GreaterOrEqual(t, result.Reclassified, int64(2))
	record, err = store.GetRecord(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, "permanent", record.RetentionClass)
	unlisted, err = store.GetRecord(ctx, unlisted.ID)
	require.NoError(t, err)
	require.Equal(t, "standard", unlisted.RetentionClass)
}

func TestLoadInjectableRecordsWarnModeIsKindScoped(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on", InjectionMode: "notes",
		StaleBehavior: "warn",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	insertStale := func(kind, text, hash string) Record {
		t.Helper()
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "s1", Kind: kind, Text: text,
			Confidence: 0.9, SourceHash: hash, SourceRevision: 1, RetentionClass: "standard",
		})
		require.NoError(t, insertErr)
		record, insertErr = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, insertErr)
		db, dbErr := store.ensureV2()
		require.NoError(t, dbErr)
		_, dbErr = db.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = 'stale', stale_reason = 'ttl_expired' WHERE id = ?
`, record.ID)
		require.NoError(t, dbErr)
		return record
	}
	preference := insertStale("preference", "likes concise answers", "hash-pref")
	fact := insertStale("fact", "port is 8080", "hash-fact")

	db, err := store.ensureV2()
	require.NoError(t, err)
	records, err := store.loadInjectableRecords(ctx, db, now)
	require.NoError(t, err)
	ids := make(map[string]struct{}, len(records))
	for _, record := range records {
		ids[record.ID] = struct{}{}
	}
	require.Contains(t, ids, preference.ID)
	require.NotContains(t, ids, fact.ID, "fact must never enter warn-mode recall")
}

func TestSweepLifecycleDisabledIsNoop(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{ProjectRoot: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "s1", Kind: "fact", Text: "keep candidate",
		Confidence: 0.9, SourceHash: "hash-noop", SourceRevision: 1, RetentionClass: "standard",
	})
	require.NoError(t, err)
	result, err := store.SweepLifecycle(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, RetentionSweepResult{}, result)
	record, err = store.GetRecord(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCandidate, record.Status)
}

func TestSweepFreshnessBatchHonorsConfiguredLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store, err := New(Config{ProjectRoot: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for index := 0; index < 3; index++ {
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "s1", Kind: "fact", Text: fmt.Sprintf("freshness %d", index),
			Confidence: 0.9, SourceHash: fmt.Sprintf("freshness-%d", index), SourceRevision: 1,
			RevalidateAfter: now.Add(-time.Hour),
		})
		require.NoError(t, insertErr)
		_, insertErr = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, insertErr)
	}
	stale, err := store.SweepFreshnessBatch(ctx, now, 2)
	require.NoError(t, err)
	require.Equal(t, 2, stale)
	page, err := store.ListRecords(ctx, RecordFilter{Status: StatusActive, Limit: 10})
	require.NoError(t, err)
	var staleCount, freshCount int
	for _, record := range page.Items {
		if record.Freshness == FreshnessStale {
			staleCount++
		} else {
			freshCount++
		}
	}
	require.Equal(t, 2, staleCount)
	require.Equal(t, 1, freshCount)
}

func TestStatusReportsDigestBacklogAndRecoveredMetric(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on",
		TurnDigestBatchSize: 2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for _, digest := range []struct {
		sessionID string
		turnID    string
	}{
		{"s1", "t1"}, {"s1", "t2"}, {"s2", "t1"},
	} {
		_, err := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: digest.sessionID, TurnID: digest.turnID, Outcome: "success",
			AssistantText: "pending",
		})
		require.NoError(t, err)
	}
	db, err := store.ensureV2()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
UPDATE memory_turn_digests SET created_at = ? WHERE project_key = ? AND session_id = 's1' AND turn_id = 't1'
`, now.Add(-time.Hour).Format(time.RFC3339Nano), store.ProjectKey())
	require.NoError(t, err)
	require.NoError(t, store.IncrementRuntimeMetric(ctx, MetricRecoveredDigestSessions, 2))

	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, status.PendingDigestSessions)
	require.Equal(t, 2, status.PendingDigestBacklogBatches)
	require.GreaterOrEqual(t, status.OldestPendingDigestAgeSeconds, int64(3600))
	require.EqualValues(t, 2, status.RecoveredDigestSessions)
}

func TestDiagnoseInjectionExplainsFilteredRecords(t *testing.T) {
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on", InjectionMode: "notes",
		PinnedBudgetRatio: 0.2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.Append(AppendNoteOptions{Text: "alpha pinned", Pinned: boolPtr(true)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	addCandidate := func(kind, text, sensitivity string) Record {
		t.Helper()
		record, err := store.InsertCandidate(ctx, Candidate{
			SessionID: "why-not", Kind: kind, Text: text, Sensitivity: sensitivity,
			Confidence: 0.9, SourceHash: fmt.Sprintf("why-not-%s-%s", kind, text), SourceRevision: 1,
		})
		if err != nil {
			t.Fatalf("InsertCandidate: %v", err)
		}
		return record
	}
	alpha := addCandidate("fact", "alpha derived", "internal")
	active := StatusActive
	if _, err := store.UpdateRecord(ctx, alpha.ID, RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate alpha: %v", err)
	}
	beta := addCandidate("fact", "beta derived", "internal")
	if _, err := store.UpdateRecord(ctx, beta.ID, RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate beta: %v", err)
	}
	stale := addCandidate("fact", "alpha stale", "internal")
	if _, err := store.UpdateRecord(ctx, stale.ID, RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate stale: %v", err)
	}
	private := addCandidate("fact", "alpha private", "private")
	if _, err := store.UpdateRecord(ctx, private.ID, RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate private: %v", err)
	}
	expired := addCandidate("fact", "alpha expired", "internal")
	if _, err := store.UpdateRecord(ctx, expired.ID, RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate expired: %v", err)
	}
	candidate := addCandidate("fact", "alpha candidate", "internal")
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE memory_entries SET freshness_status='stale' WHERE id=?`, stale.ID); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE memory_entries SET valid_until=? WHERE id=?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatalf("expire record: %v", err)
	}
	selection, err := store.DiagnoseInjection(InjectOptions{
		Query: "alpha", Limit: 10, TokenBudget: 600, SkipTrace: true,
	})
	if err != nil {
		t.Fatalf("DiagnoseInjection: %v", err)
	}
	reasons := make(map[string]string, len(selection.Dropped))
	for _, item := range selection.Dropped {
		reasons[item.ID] = item.Reason
	}
	if got := reasons[stale.ID]; got != "stale" {
		t.Fatalf("stale reason = %q", got)
	}
	if got := reasons[private.ID]; got != "private" {
		t.Fatalf("private reason = %q", got)
	}
	if got := reasons[expired.ID]; got != "expired" {
		t.Fatalf("expired reason = %q", got)
	}
	if got := reasons[candidate.ID]; got != "status_candidate" {
		t.Fatalf("candidate reason = %q", got)
	}
	if got := reasons[beta.ID]; got != "no_match" {
		t.Fatalf("no-match reason = %q", got)
	}
	if selection.CandidateCount < 6 || selection.FilteredCount < 5 {
		t.Fatalf("diagnostic counts = candidates=%d filtered=%d", selection.CandidateCount, selection.FilteredCount)
	}
}
