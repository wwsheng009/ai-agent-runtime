package memorystore

import (
	"context"
	"fmt"
	"testing"
	"time"
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
