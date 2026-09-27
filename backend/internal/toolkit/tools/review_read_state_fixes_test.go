package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestBatchDroppedSectionDoesNotRefreshLedger pins finding H4: the aggregate
// cap skips a batch section after executeSingle has already read the file, but
// the read ledger must not learn "this session read the current bytes" — the
// model never saw them, and the next write would silently overwrite an
// external edit.
func TestBatchDroppedSectionDoesNotRefreshLedger(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	body := strings.Repeat(strings.Repeat("x", 179)+"\n", 150)
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool := newViewToolAt(t, root)
	executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "three.txt"})

	// An external actor changes the file after the session read it.
	changed := body + "EXTERNAL-EDIT-NOT-DELIVERED\n"
	if err := os.WriteFile(filepath.Join(root, "three.txt"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := NewWriteTool()
	writer.SetBasePath(root)
	args := map[string]interface{}{"file_path": "three.txt", "content": body}
	before, err := writer.Execute(ctx, args)
	if err != nil || before == nil || before.Success {
		t.Fatalf("fixture must start stale: %v %+v", err, before)
	}

	batch := executeViewParams(t, tool, ctx, map[string]interface{}{"files": []interface{}{
		map[string]interface{}{"file_path": "one.txt"},
		map[string]interface{}{"file_path": "two.txt"},
		map[string]interface{}{"file_path": "three.txt"},
	}})
	if strings.Contains(batch.Content, "EXTERNAL-EDIT-NOT-DELIVERED") {
		t.Fatal("fixture must drop the changed third file at the aggregate cap")
	}
	if batch.Metadata["batch_skipped_count"] != 1 {
		t.Fatalf("expected one skipped file, got %#v", batch.Metadata["batch_skipped_count"])
	}

	after, err := writer.Execute(ctx, args)
	if err != nil || after == nil {
		t.Fatalf("write: %v", err)
	}
	if after.Success {
		t.Fatal("a cap-dropped read must not authorize overwriting an unseen external edit")
	}
	if disk, _ := os.ReadFile(filepath.Join(root, "three.txt")); !strings.Contains(string(disk), "EXTERNAL-EDIT-NOT-DELIVERED") {
		t.Fatalf("external edit must survive, got %q", disk)
	}
}

// TestAppendWriteLedgerIntegration pins finding H6: a same-session append must
// be recorded (so the next edit is not mistaken for an external change), and a
// truncating overwrite must refuse to drop an unseen external edit.
func TestAppendWriteLedgerIntegration(t *testing.T) {
	t.Run("own_append_is_not_external", func(t *testing.T) {
		ctx := dedupSessionContext(t)
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		view := newViewToolAt(t, root)
		executeViewParams(t, view, ctx, map[string]interface{}{"file_path": "notes.txt"})

		appender := NewAppendWriteTool()
		appender.SetBasePath(root)
		if result, err := appender.Execute(ctx, map[string]interface{}{"file_path": "notes.txt", "content": "two\n"}); err != nil || result == nil || !result.Success {
			t.Fatalf("append failed: %v %+v", err, result)
		}

		edit := NewEditTool()
		edit.SetBasePath(root)
		result, err := edit.Execute(ctx, map[string]interface{}{
			"file_path":  "notes.txt",
			"old_string": "two",
			"new_string": "three",
		})
		if err != nil || result == nil || !result.Success {
			t.Fatalf("edit after the session's own append must succeed: %v %+v", err, result)
		}
	})

	t.Run("truncate_refuses_stale", func(t *testing.T) {
		ctx := dedupSessionContext(t)
		root := t.TempDir()
		path := filepath.Join(root, "notes.txt")
		if err := os.WriteFile(path, []byte("v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		view := newViewToolAt(t, root)
		executeViewParams(t, view, ctx, map[string]interface{}{"file_path": "notes.txt"})
		if err := os.WriteFile(path, []byte("external\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		appender := NewAppendWriteTool()
		appender.SetBasePath(root)
		result, err := appender.Execute(ctx, map[string]interface{}{
			"file_path":      "notes.txt",
			"content":        "replacement\n",
			"truncate_first": true,
		})
		if err != nil || result == nil {
			t.Fatalf("append_write: %v", err)
		}
		if result.Success {
			t.Fatal("a truncating append_write must not overwrite an unseen external edit")
		}
		if result.Metadata["stale_write"] != true {
			t.Fatalf("expected stale_write metadata, got %#v", result.Metadata)
		}
		if disk, _ := os.ReadFile(path); string(disk) != "external\n" {
			t.Fatalf("file must stay untouched, got %q", disk)
		}
	})
}

// TestBOMStaleRecoveryHashMatchesPrecondition pins finding H7: the hash the
// stale failure advertises must be the one write's expected_sha256 compares
// against, BOM/UTF-16 files included.
func TestBOMStaleRecoveryHashMatchesPrecondition(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := filepath.Join(root, "bom.txt")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbfv1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view := newViewToolAt(t, root)
	executeViewParams(t, view, ctx, map[string]interface{}{"file_path": "bom.txt"})
	if err := os.WriteFile(path, []byte("\xef\xbb\xbfexternal\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	writer := NewWriteTool()
	writer.SetBasePath(root)
	first, err := writer.Execute(ctx, map[string]interface{}{"file_path": "bom.txt", "content": "v2\n"})
	if err != nil || first == nil || first.Success {
		t.Fatalf("expected a stale refusal: %v %+v", err, first)
	}
	suggested, _ := first.Metadata["actual_revision"].(string)
	if strings.TrimSpace(suggested) == "" {
		t.Fatalf("stale failure must advertise actual_revision, got %#v", first.Metadata)
	}
	second, err := writer.Execute(ctx, map[string]interface{}{
		"file_path":       "bom.txt",
		"content":         "v2\n",
		"expected_sha256": suggested,
	})
	if err != nil || second == nil || !second.Success {
		t.Fatalf("the advertised confirmation hash must be accepted: %v %+v", err, second)
	}
}

// TestDedupEvictionKeepsLiveEntryAcrossChurn pins finding H11: repeated
// consume-and-re-register cycles must not leave stale queue slots that evict
// the freshly registered entry once the queue is full.
func TestDedupEvictionKeepsLiveEntryAcrossChurn(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := seedLinesFile(t, root, "notes.txt", 3)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	recordFileReadFromDisk(ctx, path, true, "view", fileReadWindow{})
	for i := 0; i < viewDedupMaxEntries+2; i++ {
		recordViewWindowRead(ctx, path, info, 0, 400, 3, 3, true)
	}
	if _, hit, _ := viewDedupPeek(ctx, path, info, 0, 400); !hit {
		t.Fatal("a live dedup entry must survive its own stale queue slots")
	}
}

// TestLedgerEvictionDoesNotDropConcurrentRecord pins finding H19: a record
// written through a bucket pointer the idle sweep just evicted must still land
// in the session's state instead of being dropped with the old bucket.
func TestLedgerEvictionDoesNotDropConcurrentRecord(t *testing.T) {
	ctx := toolctx.WithSessionID(context.Background(), "test-ledger-evict-"+t.Name())
	root := t.TempDir()
	path := seedLinesFile(t, root, "notes.txt", 2)
	sessionID := toolctx.SessionID(ctx)

	held := ledgerForSession(sessionID)
	held.mu.Lock()
	held.touched = time.Now().Add(-2 * sessionReadStateIdleTTL)
	held.mu.Unlock()
	if removed := sweepIdleSessionReadState(time.Now(), sessionReadStateIdleTTL); removed == 0 {
		t.Fatal("sweep must evict the idle bucket")
	}

	held.record(path, fileReadRecord{
		SHA256:    fileBytesSHA256([]byte("x")),
		Size:      1,
		FullRead:  true,
		ReadAt:    time.Now(),
		Source:    "view",
		HashScope: readLedgerHashFull,
	})
	if _, ok := ledgerForSession(sessionID).lookup(path); !ok {
		t.Fatal("a record written through a swept bucket must land in a fresh bucket")
	}
}

// TestRecordFileWriteMatchesReadDigestScopeForLargeFile pins finding H17: for a
// file above the full-hash limit the write record must use the sampled digest
// a later view uses, otherwise FullRead inheritance always fails and the
// session loses its "has seen the whole file" fact.
func TestRecordFileWriteMatchesReadDigestScopeForLargeFile(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := filepath.Join(root, "big.txt")
	data := bytes.Repeat([]byte("a"), int(readLedgerHashMaxBytes)+1024)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	recordFileWrite(ctx, path, data, "edit")
	if !sessionHasFullRead(ctx, path) {
		t.Fatal("a write must establish a full-read record")
	}
	recordFileReadFromDisk(ctx, path, false, "view", fileReadWindow{Offset: 1, Limit: 1, LinesRead: 1})
	if !sessionHasFullRead(ctx, path) {
		t.Fatal("a partial view of the same large file must not clear FullRead")
	}
	if verdict := evaluateStaleWrite(ctx, path, data); verdict.State != staleWriteStateFresh {
		t.Fatalf("expected a fresh verdict, got %+v", verdict)
	}
}

// TestSuspectLedgerRecordRefusesWrite pins finding H20: when the file changes
// between the content read and the fingerprint read, the record must be marked
// suspect and never authorize a write.
func TestSuspectLedgerRecordRefusesWrite(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := filepath.Join(root, "race.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file changes between the content read and the fingerprint read.
	if err := os.WriteFile(path, []byte("after-and-longer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recordFileReadFromDiskObserved(ctx, path, true, "view", fileReadWindow{}, observed)
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	verdict := evaluateStaleWrite(ctx, path, current)
	if !shouldRefuseStaleWrite(verdict) {
		t.Fatalf("a suspect record must refuse the write, got %+v", verdict)
	}

	writer := NewWriteTool()
	writer.SetBasePath(root)
	result, err := writer.Execute(ctx, map[string]interface{}{"file_path": "race.txt", "content": "overwrite\n"})
	if err != nil || result == nil || result.Success {
		t.Fatalf("write must be refused after a read race: %v %+v", err, result)
	}
	if result.Metadata["read_race_detected"] != true {
		t.Fatalf("expected read_race_detected metadata, got %#v", result.Metadata)
	}
	if disk, _ := os.ReadFile(path); string(disk) != "after-and-longer\n" {
		t.Fatalf("file must stay untouched, got %q", disk)
	}
}
