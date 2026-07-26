package memorystore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAppendListSearchAndInject(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if store.Path() != filepath.Join(root, DefaultNotesFile) {
		t.Fatalf("unexpected path %q", store.Path())
	}

	n1, err := store.Append(AppendNoteOptions{
		Text:      "Prefer toolkit grep over shell rg for code search",
		Tags:      []string{"tools", "search"},
		Source:    "manual",
		SessionID: "s1",
		CreatedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Append n1: %v", err)
	}
	if !strings.HasPrefix(n1.ID, "mem_") {
		t.Fatalf("expected generated id, got %q", n1.ID)
	}

	n2, err := store.Append(AppendNoteOptions{
		Text:   "Plan mode only allows writes to plan.md by default",
		Tags:   []string{"plan", "policy"},
		Source: "session_end",
	})
	if err != nil {
		t.Fatalf("Append n2: %v", err)
	}
	if n2.Text == "" {
		t.Fatal("expected note text")
	}

	listed, err := store.List(10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(listed))
	}

	hits, err := store.Search(SearchOptions{Query: "plan mode writes", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected search hits")
	}
	if !strings.Contains(strings.ToLower(hits[0].Note.Text), "plan") {
		t.Fatalf("expected plan note first, got %q", hits[0].Note.Text)
	}

	tagHits, err := store.Search(SearchOptions{Query: "grep", Tags: []string{"tools"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search tags: %v", err)
	}
	if len(tagHits) != 1 {
		t.Fatalf("expected 1 tagged hit, got %d", len(tagHits))
	}

	selected, err := store.SelectForInject(InjectOptions{
		Query:       "permission plan writes",
		Limit:       3,
		TokenBudget: 200,
	})
	if err != nil {
		t.Fatalf("SelectForInject: %v", err)
	}
	if len(selected) == 0 {
		t.Fatal("expected inject selection")
	}

	block := FormatNotes(selected, 200)
	if !strings.Contains(block, "Project durable memory") {
		t.Fatalf("expected header in block, got %q", block)
	}
	if !strings.Contains(block, selected[0].Text) && !strings.Contains(block, "...") {
		t.Fatalf("expected note body or truncation, got %q", block)
	}

	// Reload from disk.
	store2, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	relisted, err := store2.List(0)
	if err != nil {
		t.Fatalf("reload List: %v", err)
	}
	if len(relisted) != 2 {
		t.Fatalf("expected durable 2 notes, got %d", len(relisted))
	}
}

func TestStoreAppendRequiresText(t *testing.T) {
	store, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := store.Append(AppendNoteOptions{Text: "  "}); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestResolveRootPrefersProject(t *testing.T) {
	project := t.TempDir()
	profile := filepath.Join(t.TempDir(), "memory")
	got := ResolveRoot(project, profile)
	want := filepath.Join(project, filepath.FromSlash(DefaultDirName))
	if got != want {
		t.Fatalf("ResolveRoot = %q, want %q", got, want)
	}
	if ResolveRoot("", profile) != filepath.Clean(profile) {
		t.Fatalf("expected profile fallback, got %q", ResolveRoot("", profile))
	}
}

func TestEstimateTokensAndFormatBudget(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("empty text should be 0 tokens")
	}
	if EstimateTokens("abcd") < 1 {
		t.Fatal("expected positive tokens")
	}
	notes := []Note{
		{ID: "mem_a", Text: strings.Repeat("alpha ", 40), CreatedAt: time.Now().UTC()},
		{ID: "mem_b", Text: strings.Repeat("beta ", 40), CreatedAt: time.Now().UTC()},
	}
	block := FormatNotes(notes, 40)
	if block == "" {
		t.Fatal("expected non-empty formatted block")
	}
	// Tight budget should not always include both full notes.
	if EstimateTokens(block) > 80 {
		t.Fatalf("format exceeded soft budget, tokens=%d block=%q", EstimateTokens(block), block)
	}
}

func TestLoadSkipsCorruptLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, DefaultNotesFile)
	content := "{\"id\":\"mem_ok\",\"text\":\"good note\",\"created_at\":\"2026-07-01T00:00:00Z\"}\n" +
		"not-json\n" +
		"{\"id\":\"mem_empty\",\"text\":\"\",\"created_at\":\"2026-07-01T00:00:00Z\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	notes, err := store.List(10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 || notes[0].Text != "good note" {
		t.Fatalf("expected only good note, got %+v", notes)
	}
}
