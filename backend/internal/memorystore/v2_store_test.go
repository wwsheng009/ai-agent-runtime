package memorystore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestMemoryStoreSummaryGateAndInvalidation(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	_, err = store.Append(AppendNoteOptions{Text: "decision: keep memory injections append-only"})
	if err != nil {
		t.Fatalf("append decision: %v", err)
	}
	_, err = store.Append(AppendNoteOptions{Text: "workflow: run context tests before merging"})
	if err != nil {
		t.Fatalf("append workflow: %v", err)
	}
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
	_, err = store.Append(AppendNoteOptions{Text: strings.Repeat("derived ", 80), Pinned: boolPtr(false)})
	if err != nil {
		t.Fatalf("append derived: %v", err)
	}
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
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: strings.Repeat("summary ", 80), TokenEstimate: 700}, []string{pinnedID}); err != nil {
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
}

func stringPtr(value string) *string { return &value }
func boolPtr(value bool) *bool       { return &value }

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
	if err := store.createSummary(context.Background(), Summary{Revision: 1, Text: "stable summary", TokenEstimate: 20}, []string{pinned.ID}); err != nil {
		t.Fatalf("createSummary: %v", err)
	}
	selection, err := store.SelectForInjectV2(InjectOptions{Query: "queryTarget", Limit: 5, TokenBudget: 800})
	if err != nil || selection.Summary == nil || len(selection.Recall) != 0 {
		t.Fatalf("summary mode = %+v, %v", selection, err)
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
	note, err := store.Append(AppendNoteOptions{Text: "stable decision"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
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
