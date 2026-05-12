package artifact

import (
	"context"
	"path/filepath"
	"strings"
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

func TestStore_SearchFallsBackWhenFTSQueryUsesHyphen(t *testing.T) {
	store, err := NewStore(nil)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), Record{
		SessionID: "session-hyphen",
		ToolName:  "read_logs",
		Summary:   "needle summary",
		Content:   "output contains unique-needle marker",
	})
	if err != nil {
		t.Fatalf("put artifact: %v", err)
	}

	results, err := store.Search(context.Background(), "session-hyphen", "unique-needle", 5)
	if err != nil {
		t.Fatalf("search artifacts: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if !strings.Contains(results[0].Preview, "unique-needle") {
		t.Fatalf("expected search preview to include hyphenated query, got %#v", results[0])
	}
}

func TestStore_CheckpointsPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "runtime", "artifacts.sqlite")

	store, err := NewStore(&StoreConfig{Path: storePath})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	beforeBlobID, beforeHash, err := store.SaveBlob(ctx, []byte("before\n"))
	if err != nil {
		t.Fatalf("save before blob: %v", err)
	}
	afterBlobID, afterHash, err := store.SaveBlob(ctx, []byte("after\n"))
	if err != nil {
		t.Fatalf("save after blob: %v", err)
	}

	checkpointID, err := store.SaveCheckpoint(ctx, Checkpoint{
		SessionID:    "session-shared",
		TaskID:       "task-checkpoint",
		Reason:       "mutation",
		HistoryHash:  "history-hash",
		MessageCount: 7,
		Ledger: []MemoryEntry{
			{
				SessionID: "session-shared",
				TaskID:    "task-checkpoint",
				Kind:      "decision",
				Priority:  80,
				Content: map[string]interface{}{
					"summary": "persist shared checkpoint state",
				},
				SourceRefs: []string{"artifact:shared"},
			},
		},
		Metadata: map[string]interface{}{
			"client": "aicli",
			"mode":   "server",
		},
	})
	if err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	if err := store.SaveCheckpointFiles(ctx, checkpointID, []CheckpointFile{
		{
			Path:         "notes.md",
			Op:           "modify",
			BeforeBlobID: beforeBlobID,
			AfterBlobID:  afterBlobID,
			BeforeHash:   beforeHash,
			AfterHash:    afterHash,
			DiffText:     "-before\n+after\n",
		},
	}); err != nil {
		t.Fatalf("save checkpoint files: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewStore(&StoreConfig{Path: storePath})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	checkpoint, err := reopened.GetCheckpoint(ctx, checkpointID)
	if err != nil {
		t.Fatalf("get checkpoint: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("expected checkpoint after reopen")
	}
	if checkpoint.SessionID != "session-shared" || checkpoint.HistoryHash != "history-hash" {
		t.Fatalf("unexpected checkpoint after reopen: %#v", checkpoint)
	}
	if checkpoint.Metadata["client"] != "aicli" || checkpoint.Metadata["mode"] != "server" {
		t.Fatalf("expected checkpoint metadata to persist, got %#v", checkpoint.Metadata)
	}
	if len(checkpoint.Ledger) != 1 || checkpoint.Ledger[0].Kind != "decision" {
		t.Fatalf("expected checkpoint ledger to persist, got %#v", checkpoint.Ledger)
	}

	checkpoints, err := reopened.ListCheckpoints(ctx, "session-shared", 10, 0)
	if err != nil {
		t.Fatalf("list checkpoints: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ID != checkpointID {
		t.Fatalf("expected checkpoint in list after reopen, got %#v", checkpoints)
	}

	files, err := reopened.GetCheckpointFiles(ctx, checkpointID)
	if err != nil {
		t.Fatalf("get checkpoint files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 checkpoint file after reopen, got %#v", files)
	}
	file := files[0]
	if file.Path != "notes.md" || file.Op != "modify" || file.BeforeBlobID != beforeBlobID || file.AfterBlobID != afterBlobID {
		t.Fatalf("unexpected checkpoint file after reopen: %#v", file)
	}

	blob, err := reopened.LoadBlob(ctx, afterBlobID)
	if err != nil {
		t.Fatalf("load after blob: %v", err)
	}
	if string(blob) != "after\n" {
		t.Fatalf("expected after blob content to persist, got %q", string(blob))
	}
}
