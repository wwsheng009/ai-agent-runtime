package memorystore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryStoreV2AppendListSearchAndReopen(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	note, err := store.Append(AppendNoteOptions{Text: "Prefer apply_patch for multi-hunk edits", Tags: []string{"workflow"}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !strings.HasPrefix(note.ID, "mem_") {
		t.Fatalf("unexpected note id: %q", note.ID)
	}
	notes, err := store.List(10)
	if err != nil || len(notes) != 1 {
		t.Fatalf("List = %d, %v", len(notes), err)
	}
	hits, err := store.Search(SearchOptions{Query: "apply_patch", Limit: 5})
	if err != nil || len(hits) == 0 {
		t.Fatalf("Search = %d, %v", len(hits), err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory.db")); err != nil {
		t.Fatalf("expected memory.db: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	defer reopened.Close()
	notes, err = reopened.List(10)
	if err != nil || len(notes) != 1 {
		t.Fatalf("reopen List = %d, %v", len(notes), err)
	}
}

func TestMemoryStoreRecordMetadataLifecycleAndArchivedVisibility(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	note, err := store.Append(AppendNoteOptions{
		Text:        "metadata lifecycle claim",
		Title:       "Metadata claim",
		Summary:     "A bounded metadata round trip.",
		CanonicalID: "claim:metadata-lifecycle",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	record, err := store.GetRecord(ctx, note.ID)
	if err != nil {
		t.Fatalf("GetRecord after append: %v", err)
	}
	if record.Title != "Metadata claim" || record.Summary != "A bounded metadata round trip." ||
		record.CanonicalID != "claim:metadata-lifecycle" || record.Archived {
		t.Fatalf("appended metadata = %+v", record)
	}
	listed, err := store.List(10)
	if err != nil || len(listed) != 1 || listed[0].Title != "Metadata claim" {
		t.Fatalf("List metadata = %+v, %v", listed, err)
	}

	archived := true
	updatedTitle := "Archived metadata claim"
	updatedSummary := "Updated summary"
	updatedCanonical := "claim:metadata-lifecycle:v2"
	record, err = store.UpdateRecord(ctx, note.ID, RecordPatch{
		Title: &updatedTitle, Summary: &updatedSummary, CanonicalID: &updatedCanonical, Archived: &archived,
	})
	if err != nil {
		t.Fatalf("UpdateRecord metadata: %v", err)
	}
	if !record.Archived || record.Title != updatedTitle || record.Summary != updatedSummary ||
		record.CanonicalID != updatedCanonical {
		t.Fatalf("updated metadata = %+v", record)
	}
	visible, err := store.ListRecords(ctx, RecordFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRecords default: %v", err)
	}
	if visible.Count != 0 {
		t.Fatalf("archived record leaked from default list: %+v", visible.Items)
	}
	archivedPage, err := store.ListRecords(ctx, RecordFilter{IncludeArchived: true, Limit: 10})
	if err != nil || archivedPage.Count != 1 || !archivedPage.Items[0].Archived {
		t.Fatalf("include archived page = %+v, %v", archivedPage, err)
	}
	if notes, err := store.List(10); err != nil || len(notes) != 0 {
		t.Fatalf("archived record leaked from note list: %+v, %v", notes, err)
	}

	candidate, err := store.InsertCandidate(ctx, Candidate{
		ProjectKey: store.ProjectKey(), SessionID: "metadata-session", Kind: "fact",
		Text: "candidate metadata", Title: "Candidate title", Summary: "Candidate summary",
		CanonicalID: "claim:candidate", Confidence: 0.8, SourceHash: "metadata-hash", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	if candidate.Title != "Candidate title" || candidate.Summary != "Candidate summary" ||
		candidate.CanonicalID != "claim:candidate" {
		t.Fatalf("candidate metadata = %+v", candidate)
	}

	active := StatusActive
	if _, err := store.UpdateRecord(ctx, candidate.ID, RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	corrected, err := store.CorrectRecord(ctx, candidate.ID, "corrected candidate metadata")
	if err != nil {
		t.Fatalf("CorrectRecord: %v", err)
	}
	old, err := store.GetRecord(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("GetRecord old correction: %v", err)
	}
	if old.Status != StatusSuperseded || old.SupersededBy != corrected.ID {
		t.Fatalf("old correction linkage = %+v, corrected=%+v", old, corrected)
	}
	if corrected.CanonicalID != "claim:candidate" || corrected.Title != "Candidate title" ||
		corrected.Summary != "Candidate summary" {
		t.Fatalf("corrected metadata = %+v", corrected)
	}
}

func TestMemoryStoreLegacyMetadataAndArchivedVisibility(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	active, err := store.Append(AppendNoteOptions{
		Text:        "legacy metadata record",
		Title:       "Legacy title",
		Summary:     "Legacy summary",
		CanonicalID: "claim:legacy",
	})
	if err != nil {
		t.Fatalf("Append active: %v", err)
	}
	archived, err := store.Append(AppendNoteOptions{
		Text:     "legacy archived record",
		Title:    "Archived legacy title",
		Archived: true,
	})
	if err != nil {
		t.Fatalf("Append archived: %v", err)
	}

	notes, err := store.List(10)
	if err != nil || len(notes) != 1 || notes[0].ID != active.ID ||
		notes[0].Title != "Legacy title" || notes[0].Summary != "Legacy summary" ||
		notes[0].CanonicalID != "claim:legacy" {
		t.Fatalf("legacy List = %+v, %v", notes, err)
	}
	hits, err := store.Search(SearchOptions{Query: "legacy", Limit: 10})
	if err != nil || len(hits) != 1 || hits[0].Note.ID != active.ID {
		t.Fatalf("legacy Search = %+v, %v", hits, err)
	}
	visible, err := store.ListRecords(context.Background(), RecordFilter{Limit: 10})
	if err != nil || visible.Count != 1 || visible.Items[0].ID != active.ID {
		t.Fatalf("legacy ListRecords = %+v, %v", visible, err)
	}
	all, err := store.ListRecords(context.Background(), RecordFilter{IncludeArchived: true, Limit: 10})
	if err != nil || all.Count != 2 {
		t.Fatalf("legacy ListRecords include archived = %+v, %v", all, err)
	}
	foundArchived := false
	for _, record := range all.Items {
		if record.ID == archived.ID && record.Archived {
			foundArchived = true
		}
	}
	if !foundArchived {
		t.Fatalf("legacy archived record missing: %+v", all.Items)
	}
}

func TestMemoryStoreListAndSearchIncludeAutomaticRecordsWithSourceFilter(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	manual, err := store.Append(AppendNoteOptions{Text: "shared manual project preference"})
	if err != nil {
		t.Fatalf("Append manual: %v", err)
	}
	automatic := seedActiveDerivedMemory(t, store, "shared extracted project preference", "fact")

	listed, err := store.List(20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("List returned %d notes, want manual and automatic active records: %+v", len(listed), listed)
	}
	seen := map[string]string{}
	for _, note := range listed {
		seen[note.ID] = note.Source
	}
	if seen[manual.ID] != "manual" || seen[automatic.ID] != "session_extract" {
		t.Fatalf("List did not expose both active sources: %+v", seen)
	}

	manualOnly, err := store.ListWithOptions(ListOptions{Limit: 20, SourceKind: "MANUAL"})
	if err != nil {
		t.Fatalf("ListWithOptions manual: %v", err)
	}
	if len(manualOnly) != 1 || manualOnly[0].ID != manual.ID {
		t.Fatalf("manual source filter returned %+v", manualOnly)
	}
	autoOnly, err := store.ListWithOptions(ListOptions{Limit: 20, SourceKind: "session_extract"})
	if err != nil {
		t.Fatalf("ListWithOptions automatic: %v", err)
	}
	if len(autoOnly) != 1 || autoOnly[0].ID != automatic.ID {
		t.Fatalf("automatic source filter returned %+v", autoOnly)
	}

	allHits, err := store.Search(SearchOptions{Query: "shared project preference", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(allHits) != 2 {
		t.Fatalf("Search returned %d hits, want both active sources", len(allHits))
	}
	filteredHits, err := store.Search(SearchOptions{
		Query: "shared project preference", SourceKind: "session_extract", Limit: 20,
	})
	if err != nil {
		t.Fatalf("Search with source filter: %v", err)
	}
	if len(filteredHits) != 1 || filteredHits[0].Note.ID != automatic.ID {
		t.Fatalf("automatic Search filter returned %+v", filteredHits)
	}

	records, err := store.ListRecords(ctx, RecordFilter{
		Status: StatusActive, SourceKind: "session_extract", Limit: 20,
	})
	if err != nil {
		t.Fatalf("ListRecords with source filter: %v", err)
	}
	if records.Count != 1 || records.Items[0].ID != automatic.ID {
		t.Fatalf("ListRecords source filter returned %+v", records)
	}
}

func TestMemoryStoreOffDoesNotCreateV2DB(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "off"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := store.Append(AppendNoteOptions{Text: "legacy note"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.List(10); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("off must not create memory.db, stat err=%v", err)
	}
}

func TestMemoryStoreLegacyListAndSearchCanFilterSourceKind(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "off"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	manual, err := store.Append(AppendNoteOptions{
		Text: "shared legacy manual preference", Source: "manual",
	})
	if err != nil {
		t.Fatalf("Append manual: %v", err)
	}
	extracted, err := store.Append(AppendNoteOptions{
		Text: "shared legacy extracted preference", Source: "session_extract",
	})
	if err != nil {
		t.Fatalf("Append extracted: %v", err)
	}

	notes, err := store.ListWithOptions(ListOptions{Limit: 10, SourceKind: "session_extract"})
	if err != nil || len(notes) != 1 || notes[0].ID != extracted.ID {
		t.Fatalf("legacy source-filtered List = %+v, %v", notes, err)
	}
	hits, err := store.Search(SearchOptions{
		Query: "shared legacy preference", SourceKind: "manual", Limit: 10,
	})
	if err != nil || len(hits) != 1 || hits[0].Note.ID != manual.ID {
		t.Fatalf("legacy source-filtered Search = %+v, %v", hits, err)
	}
}

func TestMemoryStoreMigratesNotesIdempotentlyAndCountsCorruptLines(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, DefaultNotesFile)
	body := `{"id":"legacy-1","text":"legacy note one","tags":["legacy"],"source":"manual","created_at":"2026-01-02T03:04:05Z"}
not-json
{"id":"legacy-2","text":"legacy note two","source":"manual","created_at":"2026-01-03T03:04:05Z"}
`
	if err := os.WriteFile(legacyPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	notes, err := store.List(10)
	if err != nil || len(notes) != 2 {
		t.Fatalf("migrated List = %d, %v", len(notes), err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second open must not duplicate imported records.
	reopened, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	notes, err = reopened.List(10)
	if err != nil || len(notes) != 2 {
		t.Fatalf("second migration List = %d, %v", len(notes), err)
	}
	db, err := reopened.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	var scanned, corrupt int
	err = db.QueryRow(`SELECT scanned, corrupt FROM memory_migration_runs ORDER BY started_at DESC LIMIT 1`).Scan(&scanned, &corrupt)
	if err != nil {
		t.Fatalf("migration run: %v", err)
	}
	if scanned != 3 || corrupt != 1 {
		t.Fatalf("migration counts scanned=%d corrupt=%d, want 3/1", scanned, corrupt)
	}
}

func TestMemoryStoreMigrationRetriesLegacyIDCollisionWithDifferentContent(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, DefaultNotesFile)
	body := `{"id":"legacy-dup","text":"first legacy body","source":"manual","created_at":"2026-01-02T03:04:05Z"}
{"id":"legacy-dup","text":"second legacy body","source":"manual","created_at":"2026-01-03T03:04:05Z"}
{"id":"legacy-copy","text":"first legacy body","source":"manual","created_at":"2026-01-04T03:04:05Z"}
`
	if err := os.WriteFile(legacyPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	page, err := store.ListRecords(context.Background(), RecordFilter{Limit: 10})
	if err != nil || page.Count != 2 {
		t.Fatalf("migrated records = %+v, %v", page, err)
	}
	foundRetry := false
	for _, record := range page.Items {
		if record.Metadata["legacy_id"] == "legacy-dup" {
			foundRetry = true
		}
	}
	if !foundRetry {
		t.Fatalf("ID-collision record was not retried with legacy_id metadata: %+v", page.Items)
	}
}

func TestMemoryStoreSummaryGateAndInvalidation(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	_ = seedActiveDerivedMemory(t, store, "decision: keep memory injections append-only", "decision")
	_ = seedActiveDerivedMemory(t, store, "workflow: run context tests before merging", "workflow")
	page, err := store.ListRecords(context.Background(), RecordFilter{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("ListRecords = %d, %v", len(page.Items), err)
	}
	ids := []string{page.Items[0].ID, page.Items[1].ID}
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: "stable summary", TokenEstimate: 20}, ids); err != nil {
		t.Fatalf("createSummary: %v", err)
	}
	selection, err := store.SelectForInjectV2(InjectOptions{Query: "memory", Limit: 5, TokenBudget: 800})
	if err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	if selection.Summary == nil {
		t.Fatal("expected active summary in selection")
	}
	if _, err := store.UpdateRecord(context.Background(), ids[0], RecordPatch{Text: stringPtr("corrected decision")}); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	selection, err = store.SelectForInjectV2(InjectOptions{Query: "memory", Limit: 5, TokenBudget: 800})
	if err != nil {
		t.Fatalf("SelectForInjectV2 after mutation: %v", err)
	}
	if selection.Summary != nil {
		t.Fatal("stale summary must not be injected after item mutation")
	}
	report, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.SummaryFreshness != "stale" {
		t.Fatalf("summary freshness=%q, want stale", report.SummaryFreshness)
	}
	if report.Jobs["pending"] == 0 {
		t.Fatal("expected summary_rebuild job")
	}
}

func TestMemoryStorePinnedBudgetSurvivesSummaryFull(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", InjectionMode: "summary", PinnedBudgetRatio: 0.2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	pinned, err := store.Append(AppendNoteOptions{Text: strings.Repeat("pinned ", 80)})
	if err != nil {
		t.Fatalf("append pinned: %v", err)
	}
	derived := seedActiveDerivedMemory(t, store, strings.Repeat("derived ", 80), "decision")
	page, err := store.ListRecords(context.Background(), RecordFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	var pinnedID string
	for _, record := range page.Items {
		if record.ID == pinned.ID {
			pinnedID = record.ID
		}
	}
	if pinnedID == "" {
		t.Fatal("pinned record missing")
	}
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: strings.Repeat("summary ", 80), TokenEstimate: 700}, []string{derived.ID}); err != nil {
		t.Fatalf("createSummary: %v", err)
	}
	selection, err := store.SelectForInjectV2(InjectOptions{Query: "pinned", Limit: 5, TokenBudget: 100})
	if err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	found := false
	for _, record := range selection.Stable {
		if record.ID == pinned.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("pinned/manual record must remain visible under budget pressure")
	}
	if selection.TokenEstimate > 100 {
		t.Fatalf("selection exceeded token budget: %d", selection.TokenEstimate)
	}
}

func stringPtr(value string) *string { return &value }
func boolPtr(value bool) *bool       { return &value }

func seedActiveDerivedMemory(t *testing.T, store *Store, text, kind string) Record {
	t.Helper()
	candidate, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "test-session", Kind: kind, Text: text, Confidence: 0.9,
		SourceHash: "test:" + text, SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate(%q): %v", text, err)
	}
	active := StatusActive
	record, err := store.UpdateRecord(context.Background(), candidate.ID, RecordPatch{Status: &active})
	if err != nil {
		t.Fatalf("activate derived record %s: %v", candidate.ID, err)
	}
	return record
}

func TestMemoryStoreSessionModePersists(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	if err := store.SetSessionMode(context.Background(), "s1", SessionModeDisabled, ""); err != nil {
		t.Fatalf("SetSessionMode: %v", err)
	}
	mode, err := store.GetSessionMode(context.Background(), "s1")
	if err != nil || mode != SessionModeDisabled {
		t.Fatalf("GetSessionMode = %q, %v", mode, err)
	}
}

func TestMemoryStoreSessionModeIsProjectScoped(t *testing.T) {
	sharedDB := filepath.Join(t.TempDir(), "shared-modes.db")
	p1, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on"})
	if err != nil {
		t.Fatalf("p1 New: %v", err)
	}
	defer p1.Close()
	p2, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on"})
	if err != nil {
		t.Fatalf("p2 New: %v", err)
	}
	defer p2.Close()
	if err := p1.SetSessionMode(context.Background(), "shared-session", SessionModeDisabled, ""); err != nil {
		t.Fatalf("p1 SetSessionMode: %v", err)
	}
	if err := p2.SetSessionMode(context.Background(), "shared-session", SessionModePolluted, ""); err != nil {
		t.Fatalf("p2 SetSessionMode: %v", err)
	}
	p1Mode, err := p1.GetSessionMode(context.Background(), "shared-session")
	if err != nil || p1Mode != SessionModeDisabled {
		t.Fatalf("p1 mode = %q, %v", p1Mode, err)
	}
	p2Mode, err := p2.GetSessionMode(context.Background(), "shared-session")
	if err != nil || p2Mode != SessionModePolluted {
		t.Fatalf("p2 mode = %q, %v", p2Mode, err)
	}
}

func TestMemoryStoreDeletedHashNotRecreated(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(AppendNoteOptions{Text: "forget this memory"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.DeleteRecord(context.Background(), note.ID); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if _, err := store.Append(AppendNoteOptions{Text: "forget this memory"}); err == nil {
		t.Fatal("deleted content must not be recreated by manual append")
	}
	page, err := store.ListRecords(context.Background(), RecordFilter{Status: StatusActive, Limit: 10})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if page.Count != 0 {
		t.Fatalf("deleted record visible in active list: %d", page.Count)
	}
}

func TestMemoryStoreSummaryGateRejectsMissingItems(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(AppendNoteOptions{Text: "summary input"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: "summary"}, []string{note.ID}); err != nil {
		t.Fatalf("createSummary: %v", err)
	}
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM memory_summary_items WHERE memory_id = ?`, note.ID); err != nil {
		t.Fatalf("delete summary item: %v", err)
	}
	selection, err := store.SelectForInjectV2(InjectOptions{Query: "summary", Limit: 5, TokenBudget: 800})
	if err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	if selection.Summary != nil {
		t.Fatal("summary with missing item must be rejected")
	}
}

func TestMemoryStoreSummaryRejectsPinnedManualItems(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	manual, err := store.Append(AppendNoteOptions{Text: "manual summary input"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "must fail"}, []string{manual.ID}); err == nil {
		t.Fatal("summary must reject pinned/manual items so they are not rendered twice")
	}
}

func TestMemoryStorePrivateRecordsAreNotInjectedOrSummarized(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "notes"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	candidate, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "s1", Kind: "fact", Text: "private deployment credential note",
		Confidence: 0.9, Sensitivity: "private", SourceHash: "private-hash", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	active := StatusActive
	record, err := store.UpdateRecord(context.Background(), candidate.ID, RecordPatch{Status: &active})
	if err != nil {
		t.Fatalf("activate private record: %v", err)
	}
	if record.Sensitivity != "private" {
		t.Fatalf("sensitivity = %q, want private", record.Sensitivity)
	}
	selection, err := store.SelectForInjectV2(InjectOptions{Query: "credential", Limit: 5, TokenBudget: 800})
	if err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	if len(selection.Stable) != 0 || len(selection.Recall) != 0 || selection.Summary != nil {
		t.Fatalf("private record leaked into injection: %+v", selection)
	}
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "must fail"}, []string{record.ID}); err == nil {
		t.Fatal("private record must not be compiled into an injectable summary")
	}
}

func TestMemoryStoreSummaryRejectsExpiredItem(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	record := seedActiveDerivedMemory(t, store, "expired summary input", "decision")
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	if _, err := db.Exec(`UPDATE memory_entries SET valid_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), record.ID); err != nil {
		t.Fatalf("seed valid_until: %v", err)
	}
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "must fail"}, []string{record.ID}); err == nil {
		t.Fatal("expired record must not be compiled into an injectable summary")
	}
}

func TestMemoryStoreSourceChangeInvalidatesSummaryAndAdvancesRevision(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	record := seedActiveDerivedMemory(t, store, "source change summary input", "decision")
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "source change summary"}, []string{record.ID}); err != nil {
		t.Fatalf("CompileSummary: %v", err)
	}
	changed, err := store.MarkSessionSourceChanged(context.Background(), "test-session", store.ProjectKey(), "new-source-hash", 2)
	if err != nil || changed != 1 {
		t.Fatalf("MarkSessionSourceChanged = %d, %v", changed, err)
	}
	updated, err := store.GetRecord(context.Background(), record.ID)
	if err != nil || updated.Freshness != FreshnessStale || updated.Revision <= record.Revision {
		t.Fatalf("updated record = %+v, %v", updated, err)
	}
	summary, err := store.ActiveSummary(context.Background())
	if err != nil || summary == nil || summary.Freshness != FreshnessStale {
		t.Fatalf("summary after source change = %+v, %v", summary, err)
	}
	jobs, err := store.ListJobs(context.Background(), 10)
	if err != nil || len(jobs) == 0 || jobs[0].Kind != "summary_rebuild" {
		t.Fatalf("summary rebuild jobs = %+v, %v", jobs, err)
	}
}

func TestMemoryStoreFailedSummaryRebuildCanBeRequeued(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	record := seedActiveDerivedMemory(t, store, "summary rebuild input", "decision")
	summary, err := store.CompileSummary(context.Background(), Summary{Text: "summary rebuild"}, []string{record.ID})
	if err != nil {
		t.Fatalf("CompileSummary: %v", err)
	}
	corrected := "summary rebuild input changed"
	if _, err := store.UpdateRecord(context.Background(), record.ID, RecordPatch{Text: &corrected}); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	if _, err := db.Exec(`
UPDATE memory_jobs SET status = 'failed', last_error = 'boom', finished_at = ?
WHERE project_key = ? AND kind = 'summary_rebuild' AND job_key = ?
`, time.Now().UTC().Format(time.RFC3339Nano), store.ProjectKey(), summary.ID+":"+summary.InputWatermark); err != nil {
		t.Fatalf("fail job: %v", err)
	}
	requeued, err := store.EnqueueStaleSummaryRebuilds(context.Background())
	if err != nil || requeued != 1 {
		t.Fatalf("EnqueueStaleSummaryRebuilds = %d, %v", requeued, err)
	}
	jobs, err := store.ListJobs(context.Background(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].Status != JobStatusPending || jobs[0].LastError != "" {
		t.Fatalf("requeued jobs = %+v, %v", jobs, err)
	}
}

func TestMemoryStoreRecallTraceSkipsDiagnosticsAndTracksInjected(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", InjectionMode: "notes",
		RecallTraceEnabled: true, RecallTraceSampleRate: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	if _, err := store.SelectForInjectV2(InjectOptions{
		Query: "diagnostic", SessionID: "s1", SkipTrace: true,
	}); err != nil {
		t.Fatalf("diagnostic selection: %v", err)
	}
	traces, err := store.ListRecallTraces(context.Background(), "", 10)
	if err != nil || len(traces) != 0 {
		t.Fatalf("diagnostic selection wrote traces: %d, %v", len(traces), err)
	}
	if _, err := store.SelectForInjectV2(InjectOptions{
		Query: "empty", SessionID: "s1",
	}); err != nil {
		t.Fatalf("context selection: %v", err)
	}
	traces, err = store.ListRecallTraces(context.Background(), "", 10)
	if err != nil || len(traces) != 1 {
		t.Fatalf("traces = %d, %v", len(traces), err)
	}
	if traces[0].Injected {
		t.Fatal("empty selection must not be recorded as injected")
	}
}

func TestMemoryStoreRejectsSecretsAndRedactsTracePreview(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", InjectionMode: "notes",
		RecallTraceEnabled: true, RecallTraceSampleRate: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	if _, err := store.Append(AppendNoteOptions{Text: "api_key=supersecretvalue123"}); err == nil {
		t.Fatal("manual append must reject secret-shaped content")
	}
	if _, err := store.SelectForInjectV2(InjectOptions{
		Query: "https://example.test/?token=supersecretvalue123", SessionID: "s1",
	}); err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	traces, err := store.ListRecallTraces(context.Background(), "", 10)
	if err != nil || len(traces) != 1 {
		t.Fatalf("traces = %d, %v", len(traces), err)
	}
	if strings.Contains(traces[0].QueryPreview, "supersecretvalue123") ||
		!strings.Contains(traces[0].QueryPreview, "REDACTED") {
		t.Fatalf("trace preview not redacted: %q", traces[0].QueryPreview)
	}
}

func TestMemoryStoreSearchDoesNotMatchUnrelatedRecordsByRecency(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	if _, err := store.Append(AppendNoteOptions{Text: "unrelated durable note"}); err != nil {
		t.Fatalf("Append unrelated: %v", err)
	}
	if _, err := store.Append(AppendNoteOptions{Text: "apply_patch workflow"}); err != nil {
		t.Fatalf("Append match: %v", err)
	}
	hits, err := store.Search(SearchOptions{Query: "apply_patch", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Note.Text, "apply_patch") {
		t.Fatalf("unexpected search hits: %+v", hits)
	}
}

func TestResolveProjectKeyStable(t *testing.T) {
	root := t.TempDir()
	first := ResolveProjectKey(root)
	second := ResolveProjectKey(root + string(filepath.Separator) + ".")
	if first == "" || first != second {
		t.Fatalf("project key mismatch: %q vs %q", first, second)
	}
}

func TestMemoryStoreInjectionModes(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	pinned, err := store.Append(AppendNoteOptions{Text: "stable pinned decision", Kind: "decision"})
	if err != nil {
		t.Fatalf("Append pinned: %v", err)
	}
	recall, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "s1", Text: "queryTarget workflow", Kind: "workflow",
		Confidence: 0.9, SourceHash: "hash-recall", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate recall: %v", err)
	}
	activeStatus := StatusActive
	if _, err := store.UpdateRecord(context.Background(), recall.ID, RecordPatch{Status: &activeStatus}); err != nil {
		t.Fatalf("activate recall: %v", err)
	}
	summaryInput := seedActiveDerivedMemory(t, store, "summary only decision", "decision")
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: "stable summary", TokenEstimate: 20}, []string{summaryInput.ID}); err != nil {
		t.Fatalf("createSummary: %v", err)
	}
	selection, err := store.SelectForInjectV2(InjectOptions{Query: "queryTarget", Limit: 5, TokenBudget: 800})
	if err != nil || selection.Summary == nil || len(selection.Recall) != 0 {
		t.Fatalf("summary mode = %+v, %v", selection, err)
	}
	if len(selection.Stable) != 1 || selection.Stable[0].ID != pinned.ID {
		t.Fatalf("summary mode stable records = %+v", selection.Stable)
	}
	store.cfg.InjectionMode = "hybrid"
	selection, err = store.SelectForInjectV2(InjectOptions{Query: "queryTarget", Limit: 5, TokenBudget: 800})
	if err != nil || selection.Summary == nil || len(selection.Recall) != 1 || selection.Recall[0].ID != recall.ID {
		t.Fatalf("hybrid mode = %+v, %v", selection, err)
	}
	store.cfg.InjectionMode = "notes"
	selection, err = store.SelectForInjectV2(InjectOptions{Query: "queryTarget", Limit: 5, TokenBudget: 800})
	if err != nil || selection.Summary != nil || len(selection.Recall) != 1 || selection.Recall[0].ID != recall.ID {
		t.Fatalf("notes mode = %+v, %v", selection, err)
	}
}

func TestMemoryStoreSummaryCompileFailurePreservesOldRevision(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note := seedActiveDerivedMemory(t, store, "stable decision", "decision")
	first, err := store.CompileSummary(context.Background(), Summary{Text: "revision one"}, []string{note.ID})
	if err != nil {
		t.Fatalf("CompileSummary first: %v", err)
	}
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "broken"}, []string{"mem_missing"}); err == nil {
		t.Fatal("compile with missing item must fail")
	}
	active, err := store.ActiveSummary(context.Background())
	if err != nil || active == nil || active.ID != first.ID || active.Revision != 1 || active.Freshness != FreshnessFresh {
		t.Fatalf("old summary was not preserved: %+v, %v", active, err)
	}
}

func TestMemoryStoreCorrectionSupersedesAndSuppresses(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	old, err := store.Append(AppendNoteOptions{Text: "Use the legacy memory writer.", Kind: "decision"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: "legacy summary"}, []string{old.ID}); err != nil {
		t.Fatalf("createSummary: %v", err)
	}
	corrected, err := store.CorrectRecord(context.Background(), old.ID, "Use the revisioned memory writer.")
	if err != nil {
		t.Fatalf("CorrectRecord: %v", err)
	}
	if corrected.Kind != "correction" || corrected.SupersedesID != old.ID || corrected.SourceKind != "manual" {
		t.Fatalf("unexpected correction record: %+v", corrected)
	}
	superseded, err := store.GetRecord(context.Background(), old.ID)
	if err != nil || superseded.Status != StatusSuperseded {
		t.Fatalf("old record = %+v, %v", superseded, err)
	}
	summary, err := store.ActiveSummary(context.Background())
	if err != nil || summary == nil || summary.Freshness != FreshnessStale {
		t.Fatalf("summary after correction = %+v, %v", summary, err)
	}
	if _, err := store.Append(AppendNoteOptions{Text: "Use the legacy memory writer.", Kind: "decision"}); err == nil {
		t.Fatal("corrected content must be suppressed from recreation")
	}
	page, err := store.ListRecords(context.Background(), RecordFilter{Status: StatusActive, Limit: 10})
	if err != nil || page.Count != 1 || page.Items[0].ID != corrected.ID {
		t.Fatalf("active records = %+v, %v", page, err)
	}
}

func TestMemoryStoreGovernanceLifecycle(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note := seedActiveDerivedMemory(t, store, "governance lifecycle memory", "fact")
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "governance summary"}, []string{note.ID}); err != nil {
		t.Fatalf("CompileSummary: %v", err)
	}
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("ensureV2: %v", err)
	}
	if _, err := db.Exec(`UPDATE memory_entries SET revalidate_after = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), note.ID); err != nil {
		t.Fatalf("seed revalidate_after: %v", err)
	}
	staleCount, err := store.SweepFreshness(context.Background(), time.Now().UTC())
	if err != nil || staleCount != 1 {
		t.Fatalf("SweepFreshness = %d, %v", staleCount, err)
	}
	stale, err := store.GetRecord(context.Background(), note.ID)
	if err != nil || stale.Freshness != FreshnessStale || stale.StaleReason != "revalidate_due" {
		t.Fatalf("stale record = %+v, %v", stale, err)
	}
	if summary, _ := store.ActiveSummary(context.Background()); summary == nil || summary.Freshness != FreshnessStale {
		t.Fatalf("summary not invalidated by sweep: %+v", summary)
	}
	fresh, err := store.RevalidateRecord(context.Background(), note.ID)
	if err != nil || fresh.Freshness != FreshnessFresh || fresh.LastVerifiedAt.IsZero() {
		t.Fatalf("RevalidateRecord = %+v, %v", fresh, err)
	}
	if !fresh.RevalidateAfter.IsZero() {
		t.Fatalf("due revalidation deadline should be consumed, got %v", fresh.RevalidateAfter)
	}
	if count, sweepErr := store.SweepFreshness(context.Background(), time.Now().UTC().Add(time.Minute)); sweepErr != nil || count != 0 {
		t.Fatalf("revalidated record became immediately due again: count=%d err=%v", count, sweepErr)
	}
	if err := store.DeleteRecord(context.Background(), note.ID); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	restored, err := store.RestoreRecord(context.Background(), note.ID)
	if err != nil || restored.Status != StatusActive || !restored.DeletedAt.IsZero() {
		t.Fatalf("RestoreRecord = %+v, %v", restored, err)
	}
	var suppressionCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM memory_suppressions WHERE source_memory_id = ? AND reason = 'deleted'`, note.ID).Scan(&suppressionCount); err != nil {
		t.Fatalf("count suppression: %v", err)
	}
	if suppressionCount != 0 {
		t.Fatalf("restore must remove deleted suppression, got %d", suppressionCount)
	}
	jsonl, err := store.ExportJSONL(context.Background())
	if err != nil || !strings.Contains(jsonl, note.ID) {
		t.Fatalf("ExportJSONL = %q, %v", jsonl, err)
	}
	markdown, err := store.ExportMarkdown(context.Background())
	if err != nil || !strings.Contains(markdown, "governance lifecycle memory") {
		t.Fatalf("ExportMarkdown = %q, %v", markdown, err)
	}
	events, err := store.ListEvents(context.Background(), 100)
	if err != nil || len(events) == 0 {
		t.Fatalf("ListEvents = %d, %v", len(events), err)
	}
}

func TestMemoryStoreImportJSONLIdempotentAndDryRun(t *testing.T) {
	source, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("source New: %v", err)
	}
	defer source.Close()
	if _, err := source.Append(AppendNoteOptions{Text: "imported manual memory", Kind: "reference"}); err != nil {
		t.Fatalf("source Append: %v", err)
	}
	derived := seedActiveDerivedMemory(t, source, "imported derived decision", "decision")
	if _, err := source.CompileSummary(context.Background(), Summary{Text: "imported summary"}, []string{derived.ID}); err != nil {
		t.Fatalf("source CompileSummary: %v", err)
	}
	body, err := source.ExportJSONL(context.Background())
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if strings.Count(body, `"type":"version"`) != 3 {
		t.Fatalf("ExportJSONL version lines = %d, want 3\n%s",
			strings.Count(body, `"type":"version"`), body)
	}

	target, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("target New: %v", err)
	}
	defer target.Close()
	dry, err := target.ImportJSONL(context.Background(), []byte(body), true)
	if err != nil || !dry.DryRun || dry.Imported != 2 || dry.Versions != 3 || dry.Corrupt != 0 {
		t.Fatalf("dry-run import = %+v, %v", dry, err)
	}
	if page, err := target.ListRecords(context.Background(), RecordFilter{Limit: 10}); err != nil || page.Count != 0 {
		t.Fatalf("dry-run changed records: %+v, %v", page, err)
	}
	if summary, err := target.ActiveSummary(context.Background()); err != nil || summary != nil {
		t.Fatalf("dry-run changed summary: %+v, %v", summary, err)
	}

	imported, err := target.ImportJSONL(context.Background(), []byte(body), false)
	if err != nil || imported.Imported != 2 || imported.Versions != 3 || imported.Summaries != 1 || imported.Corrupt != 0 {
		t.Fatalf("import = %+v, %v", imported, err)
	}
	page, err := target.ListRecords(context.Background(), RecordFilter{Limit: 10})
	if err != nil || page.Count != 2 {
		t.Fatalf("records after import = %+v, %v", page, err)
	}
	summary, err := target.ActiveSummary(context.Background())
	if err != nil || summary == nil || summary.Text != "imported summary" {
		t.Fatalf("summary after import = %+v, %v", summary, err)
	}
	for _, item := range page.Items {
		versions, versionErr := target.ListRecordVersions(context.Background(), item.ID)
		if versionErr != nil || len(versions) != int(item.Revision) ||
			versions[len(versions)-1].Record.ProjectKey != target.ProjectKey() {
			t.Fatalf("imported history for %s = %+v, %v", item.ID, versions, versionErr)
		}
	}
	again, err := target.ImportJSONL(context.Background(), []byte(body), false)
	if err != nil || again.Imported != 0 || again.Summaries != 0 || again.Skipped != 3 {
		t.Fatalf("idempotent import = %+v, %v", again, err)
	}
}

func TestMemoryStoreImportJSONLRestoresVersionHistoryAndRemapsProject(t *testing.T) {
	ctx := context.Background()
	source, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "source-project",
	})
	if err != nil {
		t.Fatalf("source New: %v", err)
	}
	defer source.Close()

	record, err := source.Append(AppendNoteOptions{
		ID:   "mem_import_history",
		Text: "history before import",
		Kind: "fact",
	})
	if err != nil {
		t.Fatalf("source Append: %v", err)
	}
	updatedText := "history after import"
	if _, err := source.UpdateRecord(ctx, record.ID, RecordPatch{Text: &updatedText}); err != nil {
		t.Fatalf("source UpdateRecord: %v", err)
	}
	body, err := source.ExportJSONL(ctx)
	if err != nil {
		t.Fatalf("source ExportJSONL: %v", err)
	}
	if got := strings.Count(body, `"type":"version"`); got != 2 {
		t.Fatalf("exported version lines = %d, want 2", got)
	}

	target, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "target-project",
	})
	if err != nil {
		t.Fatalf("target New: %v", err)
	}
	defer target.Close()
	result, err := target.ImportJSONL(ctx, []byte(body), false)
	if err != nil {
		t.Fatalf("target ImportJSONL: %v", err)
	}
	if result.Imported != 1 || result.Versions != 2 || result.Corrupt != 0 {
		t.Fatalf("target import result = %+v", result)
	}

	imported, err := target.GetRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("target GetRecord: %v", err)
	}
	if imported.ProjectKey != "target-project" || imported.Text != updatedText || imported.Revision != 2 {
		t.Fatalf("imported current record = %+v", imported)
	}
	versions, err := target.ListRecordVersions(ctx, record.ID)
	if err != nil {
		t.Fatalf("target ListRecordVersions: %v", err)
	}
	if len(versions) != 2 ||
		versions[0].Record.Text != "history before import" ||
		versions[1].Record.Text != updatedText {
		t.Fatalf("imported version history = %+v", versions)
	}
	for _, version := range versions {
		if version.ProjectKey != "target-project" ||
			version.Record.ProjectKey != "target-project" ||
			version.ScopeKind != ScopeProject ||
			version.ScopeKey != "target-project" {
			t.Fatalf("imported version was not remapped = %+v", version)
		}
	}
}

func TestMemoryStoreImportInvalidVersionKeepsRecordBaseline(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "invalid-version-project",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	record := Record{
		ID:         "mem_invalid_history",
		Scope:      ScopeProject,
		ProjectKey: "source-project",
		Kind:       "fact",
		Status:     StatusActive,
		Text:       "safe record without a valid history line",
	}
	invalidVersion := RecordVersion{
		ID:         "mver_invalid",
		MemoryID:   record.ID,
		Version:    1,
		ProjectKey: record.ProjectKey,
		ScopeKind:  ScopeProject,
		ScopeKey:   record.ProjectKey,
		Record: Record{
			ID:         record.ID,
			Scope:      ScopeProject,
			ProjectKey: record.ProjectKey,
			Kind:       "fact",
			Status:     Status("not-a-status"),
			Text:       record.Text,
		},
	}
	recordLine, err := json.Marshal(map[string]interface{}{"type": "record", "record": record})
	if err != nil {
		t.Fatalf("marshal record line: %v", err)
	}
	versionLine, err := json.Marshal(map[string]interface{}{"type": "version", "version": invalidVersion})
	if err != nil {
		t.Fatalf("marshal version line: %v", err)
	}
	body := append(recordLine, '\n')
	body = append(body, versionLine...)
	result, err := store.ImportJSONL(ctx, body, false)
	if err != nil {
		t.Fatalf("ImportJSONL: %v", err)
	}
	if result.Imported != 1 || result.Versions != 1 || result.Corrupt != 1 {
		t.Fatalf("import result = %+v", result)
	}
	versions, err := store.ListRecordVersions(ctx, record.ID)
	if err != nil {
		t.Fatalf("ListRecordVersions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("baseline history after invalid import = %+v", versions)
	}
}

func TestMemoryStoreProjectScopedAuditExportAndReset(t *testing.T) {
	sharedDB := filepath.Join(t.TempDir(), "shared-memory.db")
	p1, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("p1 New: %v", err)
	}
	defer p1.Close()
	p2, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("p2 New: %v", err)
	}
	defer p2.Close()
	if p1.ProjectKey() == p2.ProjectKey() {
		t.Fatal("test projects must have distinct project keys")
	}
	p1Note, err := p1.Append(AppendNoteOptions{Text: "project one only", Kind: "reference"})
	if err != nil {
		t.Fatalf("p1 Append: %v", err)
	}
	p1Derived := seedActiveDerivedMemory(t, p1, "project one derived summary input", "decision")
	if _, err := p1.CompileSummary(context.Background(), Summary{Text: "project one summary"}, []string{p1Derived.ID}); err != nil {
		t.Fatalf("p1 CompileSummary: %v", err)
	}
	p2Note, err := p2.Append(AppendNoteOptions{Text: "project two only", Kind: "reference"})
	if err != nil {
		t.Fatalf("p2 Append: %v", err)
	}
	p1DB, err := p1.ensureV2()
	if err != nil {
		t.Fatalf("p1 ensureV2: %v", err)
	}
	if _, err := p1DB.ExecContext(context.Background(),
		`DELETE FROM memory_versions WHERE project_key = ?`, p1.ProjectKey()); err == nil {
		t.Fatal("ordinary version deletion must remain blocked by append-only trigger")
	}
	p1Events, err := p1.ListEvents(context.Background(), 100)
	if err != nil {
		t.Fatalf("p1 ListEvents: %v", err)
	}
	for _, event := range p1Events {
		if event.MemoryID == p2Note.ID {
			t.Fatalf("p1 audit leaked p2 event: %+v", event)
		}
	}
	p1Markdown, err := p1.ExportMarkdown(context.Background())
	if err != nil || strings.Contains(p1Markdown, "project two only") || !strings.Contains(p1Markdown, "project one only") {
		t.Fatalf("p1 markdown export leaked/omitted project data: %q, %v", p1Markdown, err)
	}
	if err := p1.Reset(context.Background()); err != nil {
		t.Fatalf("p1 Reset: %v", err)
	}
	if page, err := p1.ListRecords(context.Background(), RecordFilter{Limit: 10}); err != nil || page.Count != 0 {
		t.Fatalf("p1 records after reset = %+v, %v", page, err)
	}
	if page, err := p2.ListRecords(context.Background(), RecordFilter{Limit: 10}); err != nil || page.Count != 1 || page.Items[0].ID != p2Note.ID {
		t.Fatalf("p2 records after p1 reset = %+v, %v", page, err)
	}
	if events, err := p1.ListEvents(context.Background(), 100); err != nil || len(events) != 0 {
		t.Fatalf("p1 audit after reset = %d, %v", len(events), err)
	}
	var p1Versions, p2Versions, activeGuards int
	if err := p1DB.QueryRowContext(context.Background(), `
		SELECT COUNT(1) FROM memory_versions WHERE project_key = ? AND memory_id = ?
	`, p1.ProjectKey(), p1Note.ID).Scan(&p1Versions); err != nil {
		t.Fatalf("p1 versions after reset: %v", err)
	}
	if err := p1DB.QueryRowContext(context.Background(), `
		SELECT COUNT(1) FROM memory_versions WHERE project_key = ? AND memory_id = ?
	`, p2.ProjectKey(), p2Note.ID).Scan(&p2Versions); err != nil {
		t.Fatalf("p2 versions after p1 reset: %v", err)
	}
	if err := p1DB.QueryRowContext(context.Background(), `
		SELECT COUNT(1) FROM memory_version_purge_guards
		WHERE project_key IN (?, ?)
	`, p1.ProjectKey(), p2.ProjectKey()).Scan(&activeGuards); err != nil {
		t.Fatalf("version purge guards after reset: %v", err)
	}
	if p1Versions != 0 || p2Versions != 1 || activeGuards != 0 {
		t.Fatalf("versions/guards after reset: p1=%d p2=%d guards=%d",
			p1Versions, p2Versions, activeGuards)
	}
}

func TestMemoryStoreResetDoesNotReimportUnchangedLegacyNotes(t *testing.T) {
	projectRoot := t.TempDir()
	legacyPath := filepath.Join(projectRoot, DefaultDirName, DefaultNotesFile)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"id":"legacy-reset","text":"legacy reset note","source":"manual","created_at":"2026-01-02T03:04:05Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := New(Config{ProjectRoot: projectRoot, Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if page, err := store.ListRecords(context.Background(), RecordFilter{Limit: 10}); err != nil || page.Count != 1 {
		t.Fatalf("migrated records = %+v, %v", page, err)
	}
	if err := store.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := New(Config{ProjectRoot: projectRoot, Mode: "on"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if page, err := reopened.ListRecords(context.Background(), RecordFilter{Limit: 10}); err != nil || page.Count != 0 {
		t.Fatalf("reset legacy notes were re-imported: %+v, %v", page, err)
	}
}

func TestMemoryStoreActivateSummaryRevision(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note := seedActiveDerivedMemory(t, store, "activate summary input", "decision")
	first, err := store.CompileSummary(context.Background(), Summary{Text: "summary one"}, []string{note.ID})
	if err != nil {
		t.Fatalf("CompileSummary first: %v", err)
	}
	second, err := store.CompileSummary(context.Background(), Summary{Text: "summary two"}, []string{note.ID})
	if err != nil {
		t.Fatalf("CompileSummary second: %v", err)
	}
	activated, err := store.ActivateSummary(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("ActivateSummary: %v", err)
	}
	if activated.ID != first.ID || activated.Revision != 1 || activated.Status != "active" {
		t.Fatalf("activated = %+v", activated)
	}
	active, err := store.ActiveSummary(context.Background())
	if err != nil || active == nil || active.ID != first.ID {
		t.Fatalf("active summary = %+v, %v", active, err)
	}
	previous, err := store.GetSummary(context.Background(), second.ID)
	if err != nil || previous.Status != "superseded" {
		t.Fatalf("second summary = %+v, %v", previous, err)
	}
	artifact, err := os.ReadFile(filepath.Join(store.Root(), "memory_summary.md"))
	if err != nil || !strings.Contains(string(artifact), "summary one") {
		t.Fatalf("activation did not refresh summary artifact: %q, %v", artifact, err)
	}
}

func TestMemoryStoreMetricsAggregatesUsageAndJobs(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(AppendNoteOptions{Text: "metrics input", Kind: "decision"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.RecordExposure(context.Background(), Exposure{
		MemoryIDs: []string{note.ID}, SessionID: "s1", UseKind: UsageInjectedOnly,
	}); err != nil {
		t.Fatalf("RecordExposure: %v", err)
	}
	derived := seedActiveDerivedMemory(t, store, "metrics summary input", "decision")
	if _, err := store.CompileSummary(context.Background(), Summary{Text: "metrics summary"}, []string{derived.ID}); err != nil {
		t.Fatalf("CompileSummary: %v", err)
	}
	if _, err := store.UpdateRecord(context.Background(), derived.ID, RecordPatch{Text: stringPtr("metrics input changed")}); err != nil {
		t.Fatalf("trigger summary rebuild: %v", err)
	}
	metrics, err := store.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if metrics.PinnedCount != 1 || metrics.ByKind["decision"] == 0 {
		t.Fatalf("unexpected metrics counts: %+v", metrics)
	}
	if metrics.Usage[string(UsageInjectedOnly)] != 1 {
		t.Fatalf("usage metrics = %+v", metrics.Usage)
	}
	if metrics.SummaryRevision != 1 || metrics.SummaryFreshness != "stale" {
		t.Fatalf("summary metrics = rev=%d freshness=%s", metrics.SummaryRevision, metrics.SummaryFreshness)
	}
	if metrics.SummaryRebuildJobs["pending"] == 0 {
		t.Fatalf("missing summary rebuild job metric: %+v", metrics.SummaryRebuildJobs)
	}
}
