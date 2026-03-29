package artifact

import (
	"context"
	"testing"
)

func TestStore_MemoryEntriesAndCheckpoints(t *testing.T) {
	store, err := NewStore(nil)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	entryID, err := store.InsertMemoryEntry(context.Background(), MemoryEntry{
		SessionID: "session-1",
		TaskID:    "task-1",
		Kind:      "decision",
		Priority:  90,
		Content: map[string]interface{}{
			"summary": "Use artifact-backed context.",
		},
		SourceRefs: []string{"art_1"},
	})
	if err != nil {
		t.Fatalf("insert memory entry: %v", err)
	}
	if entryID == "" {
		t.Fatal("expected memory entry id")
	}

	entries, err := store.LoadMemoryEntries(context.Background(), "session-1", []string{"decision"}, 10)
	if err != nil {
		t.Fatalf("load memory entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 memory entry, got %d", len(entries))
	}

	checkpointID, err := store.SaveCheckpoint(context.Background(), Checkpoint{
		SessionID:    "session-1",
		TaskID:       "task-1",
		Reason:       "history_window",
		HistoryHash:  "hash-1",
		MessageCount: 4,
		Ledger:       entries,
		Metadata: map[string]interface{}{
			"source_messages": 4,
		},
	})
	if err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	if checkpointID == "" {
		t.Fatal("expected checkpoint id")
	}

	checkpoint, err := store.LatestCheckpoint(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("load latest checkpoint: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.HistoryHash != "hash-1" {
		t.Fatalf("expected history hash hash-1, got %s", checkpoint.HistoryHash)
	}
	if len(checkpoint.Ledger) != 1 {
		t.Fatalf("expected checkpoint ledger size 1, got %d", len(checkpoint.Ledger))
	}
}

func TestStore_SearchReturnsMetadataAndSourceRefs(t *testing.T) {
	store, err := NewStore(nil)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), Record{
		SessionID: "session-search",
		ToolName:  "read_notes",
		Summary:   "profile recall summary",
		Content:   "profile notes mention a failing integration path",
		Metadata: map[string]interface{}{
			"source_refs": []string{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
			"profile": "dev",
		},
	})
	if err != nil {
		t.Fatalf("put artifact: %v", err)
	}

	results, err := store.Search(context.Background(), "session-search", "integration path", 5)
	if err != nil {
		t.Fatalf("search artifacts: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].Summary != "profile recall summary" {
		t.Fatalf("unexpected summary: %q", results[0].Summary)
	}
	if len(results[0].SourceRefs) != 2 {
		t.Fatalf("expected 2 source refs, got %#v", results[0].SourceRefs)
	}
	if results[0].Metadata["profile"] != "dev" {
		t.Fatalf("expected metadata to be preserved, got %#v", results[0].Metadata)
	}
}
