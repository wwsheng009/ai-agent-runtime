package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// dedupSessionContext isolates the view dedup/ledger state per test: both
// stores are process-global and keyed by session ID.
func dedupSessionContext(t *testing.T) context.Context {
	t.Helper()
	t.Setenv("AICLI_VIEW_DEDUP", "")
	return toolctx.WithSessionID(context.Background(), "test-dedup-"+t.Name())
}

func seedLinesFile(t *testing.T, dir, name string, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return path
}

// TestViewDedupSameWindowReturnsStubThenConsumes pins consume-on-hit: the
// second identical read gets a stub, and the third call (after consumption)
// returns real content again instead of a second stub pointing at a result
// compaction may already have removed.
func TestViewDedupSameWindowReturnsStubThenConsumes(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	seedLinesFile(t, root, "notes.txt", 10)
	tool := newViewToolAt(t, root)

	first := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	if !strings.Contains(first.Content, "1: line-1") {
		t.Fatalf("first read must return real content, got %q", first.Content)
	}

	second := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	if second.Metadata["dedup_hit"] != true || second.Metadata["dedup_consumed"] != true {
		t.Fatalf("second read must be a consumed dedup stub, got %#v", second.Metadata)
	}
	if !strings.Contains(second.Content, "unchanged:") || !strings.Contains(second.Content, "call view again") {
		t.Fatalf("stub must carry the recovery route, got %q", second.Content)
	}

	third := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	if third.Metadata["dedup_hit"] == true {
		t.Fatalf("consume-on-hit must make the third call return real content, got %#v", third.Metadata)
	}
	if !strings.Contains(third.Content, "1: line-1") {
		t.Fatalf("third read must return real content, got %q", third.Content)
	}
}

// TestViewDedupFromLine1RequiresFullLedgerView: when the session ledger no
// longer holds a full view (eviction/restart), a from-line-1 re-read must
// return real content so write's read-before-write guard can rely on it.
func TestViewDedupFromLine1RequiresFullLedgerView(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	seedLinesFile(t, root, "notes.txt", 5)
	tool := newViewToolAt(t, root)

	executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	// Simulate ledger eviction/restart while the dedup entry survives.
	sessionReadLedgers.Delete(toolctx.SessionID(ctx))

	result := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	if result.Metadata["dedup_hit"] == true {
		t.Fatalf("from-line-1 read must not be stubbed without a full ledger view, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "1: line-1") {
		t.Fatalf("expected real content, got %q", result.Content)
	}
}

// TestViewDedupInvalidatedByModTime: the fast-path validity check is
// mtime+size; a touched file must return real bytes.
func TestViewDedupInvalidatedByModTime(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := seedLinesFile(t, root, "notes.txt", 3)
	tool := newViewToolAt(t, root)

	executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	touched := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, touched, touched); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	result := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	if result.Metadata["dedup_hit"] == true {
		t.Fatalf("changed mtime must invalidate the dedup entry, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "1: line-1") {
		t.Fatalf("expected real content, got %q", result.Content)
	}
}

// TestViewDedupKillSwitch: AICLI_VIEW_DEDUP=off disables the cache entirely.
func TestViewDedupKillSwitch(t *testing.T) {
	ctx := toolctx.WithSessionID(context.Background(), "test-dedup-killswitch-"+t.Name())
	t.Setenv("AICLI_VIEW_DEDUP", "off")
	root := t.TempDir()
	seedLinesFile(t, root, "notes.txt", 4)
	tool := newViewToolAt(t, root)

	executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	result := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "notes.txt"})
	if result.Metadata["dedup_hit"] == true {
		t.Fatalf("kill switch must disable dedup, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "1: line-1") {
		t.Fatalf("expected real content, got %q", result.Content)
	}
}

// TestWriteAfterPartialViewIsAllowedWithPartialNote pins the relational
// invariant fix: a fresh partial view allows the overwrite (bytes unchanged)
// but the result must say which window the session has seen instead of a bare
// "fresh" (analysis §3.8, CommandCode §4 fix #1/#2).
func TestWriteAfterPartialViewIsAllowedWithPartialNote(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	seedLinesFile(t, root, "notes.txt", 100)
	view := newViewToolAt(t, root)
	result := executeViewParams(t, view, ctx, map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(10),
		"limit":     float64(5),
	})
	if result.Metadata["lines_read"] != 5 || result.Metadata["is_truncated"] != true {
		t.Fatalf("precondition: expected a 5-line partial window, got %#v", result.Metadata)
	}

	write := NewWriteTool()
	write.SetBasePath(root)
	writeResult, err := write.Execute(ctx, map[string]interface{}{"file_path": "notes.txt", "content": "updated\n"})
	if err != nil || writeResult == nil || !writeResult.Success {
		t.Fatalf("partial view must not block the overwrite: %v %+v", err, writeResult)
	}
	if writeResult.Metadata["read_before_write"] != "partial" {
		t.Fatalf("expected partial read_before_write, got %#v", writeResult.Metadata["read_before_write"])
	}
	if writeResult.Metadata["seen_offset"] != 10 || writeResult.Metadata["seen_lines"] != 5 {
		t.Fatalf("expected seen-window metadata, got %#v", writeResult.Metadata)
	}
	if note, _ := writeResult.Metadata["read_before_write_note"].(string); !strings.Contains(note, "部分窗口") {
		t.Fatalf("expected a partial-view note, got %q", note)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "notes.txt")); string(raw) != "updated\n" {
		t.Fatalf("write must have been applied, got %q", raw)
	}
}

// TestWriteAfterClampedReadIsAllowed is the regression for the read/clamp/
// write loop: a byte-clamped read still records the full on-disk hash, so the
// overwrite is allowed (no refusal loop).
func TestWriteAfterClampedReadIsAllowed(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	huge := strings.Repeat("z", 300*1024)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(huge+"\n"), 0o644); err != nil {
		t.Fatalf("seed big: %v", err)
	}
	view := newViewToolAt(t, root)
	result := executeViewParams(t, view, ctx, map[string]interface{}{"file_path": "big.txt"})
	if result.Metadata["reader_clamped_lines"] != 1 {
		t.Fatalf("precondition: expected a reader-clamped line, got %#v", result.Metadata)
	}

	write := NewWriteTool()
	write.SetBasePath(root)
	writeResult, err := write.Execute(ctx, map[string]interface{}{"file_path": "big.txt", "content": "small\n"})
	if err != nil || writeResult == nil || !writeResult.Success {
		t.Fatalf("clamped read must not block the overwrite: %v %+v", err, writeResult)
	}
	if writeResult.Metadata["read_before_write"] != "partial" {
		t.Fatalf("expected partial read_before_write, got %#v", writeResult.Metadata["read_before_write"])
	}
}

// TestViewBatchRecordsLedger corrects the earlier claim that batch reads skip
// the ledger: every successful text item is recorded (analysis §2.2).
func TestViewBatchRecordsLedger(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	seedLinesFile(t, root, "a.txt", 3)
	seedLinesFile(t, root, "b.txt", 3)
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"files": []interface{}{
			map[string]interface{}{"file_path": "a.txt"},
			map[string]interface{}{"file_path": "b.txt"},
		},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("batch read failed: %v %+v", err, result)
	}
	ledger := ledgerForSession(toolctx.SessionID(ctx))
	for _, name := range []string{"a.txt", "b.txt"} {
		record, ok := ledger.lookup(filepath.Join(root, name))
		if !ok || record.Source != "view" || !record.FullRead {
			t.Fatalf("expected a full-view ledger record for %s, got %#v ok=%v", name, record, ok)
		}
	}
}

func executeViewParams(t *testing.T, tool *ViewTool, ctx context.Context, params map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	result, err := tool.Execute(ctx, params)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("view failed: %v %+v", err, result)
	}
	return result
}
