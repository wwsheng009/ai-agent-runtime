package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimetoolkit "github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// TestViewBatchCapDoesNotStubDroppedItem: an item dropped by the aggregate cap
// was still registered in the dedup cache, so the first standalone read of that
// file returned an "unchanged" stub for content the model never received
// (2026-09-27 review).
func TestViewBatchCapDoesNotStubDroppedItem(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	const marker = "THIRD-FILE-MARKER"
	for _, name := range []string{"one.txt", "two.txt"} {
		body := strings.Repeat(strings.Repeat("a", 179)+"\n", 150)
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	third := strings.Repeat(strings.Repeat("b", 179)+"\n", 150) + marker + "\n"
	if err := os.WriteFile(filepath.Join(root, "three.txt"), []byte(third), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newViewToolAt(t, root)
	batch, err := tool.Execute(ctx, map[string]interface{}{"files": []interface{}{
		map[string]interface{}{"file_path": "one.txt"},
		map[string]interface{}{"file_path": "two.txt"},
		map[string]interface{}{"file_path": "three.txt"},
	}})
	if err != nil || batch == nil || !batch.Success {
		t.Fatalf("batch read failed: result=%#v err=%v", batch, err)
	}
	skipped, _ := batch.Metadata["batch_skipped_files"].([]map[string]interface{})
	if len(skipped) != 1 {
		t.Fatalf("fixture: expected the third item to be dropped by the cap, got %#v", batch.Metadata["batch_skipped_files"])
	}
	single := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "three.txt"})
	if hit, _ := single.Metadata["dedup_hit"].(bool); hit {
		t.Fatalf("cap-dropped item was remembered as delivered: %#v", single.Metadata)
	}
	if !strings.Contains(single.Content, marker) {
		t.Fatalf("standalone read of the dropped file must return its content, got %d bytes", len(single.Content))
	}
}

// TestViewDedupStubKeepsContinuationContract: the stub used to report
// is_truncated=false and drop suggested_next_offset, so a repeat read of a
// truncated window looked complete.
func TestViewDedupStubKeepsContinuationContract(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	var body strings.Builder
	for i := 0; i < 50; i++ {
		body.WriteString(strings.Repeat("x", 20))
		body.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(root, "lines.txt"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newViewToolAt(t, root)
	first := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "lines.txt", "offset": 10, "limit": 5})
	if first.Metadata["is_truncated"] != true {
		t.Fatalf("fixture: expected a truncated first window, got %#v", first.Metadata)
	}
	second := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "lines.txt", "offset": 10, "limit": 5})
	if hit, _ := second.Metadata["dedup_hit"].(bool); !hit {
		t.Fatalf("expected the exact repeat to hit the dedup stub, got %#v", second.Metadata)
	}
	if second.Metadata["is_truncated"] != true {
		t.Fatalf("stub must keep is_truncated=true for a truncated window, got %#v", second.Metadata)
	}
	if got, _ := second.Metadata["suggested_next_offset"].(int); got != 15 {
		t.Fatalf("stub must keep suggested_next_offset=15, got %#v", second.Metadata["suggested_next_offset"])
	}
	if !strings.Contains(second.Content, "offset=15") {
		t.Fatalf("stub text must carry the continuation route, got %q", second.Content)
	}
	// The stub mirrors whatever the first window published: total_lines is only
	// known when the reader scanned the file end, and the stub must not invent it.
	firstTotal, firstHasTotal := first.Metadata["total_lines"]
	secondTotal, secondHasTotal := second.Metadata["total_lines"]
	if firstHasTotal != secondHasTotal || (firstHasTotal && firstTotal != secondTotal) {
		t.Fatalf("stub total_lines must mirror the first window: first=%#v second=%#v", firstTotal, secondTotal)
	}
}

// TestViewBatchFailureDetailBounded: the errors block and failed_items contract
// were unbounded, so a batch of unreadable files delivered far more than the
// declared aggregate window.
func TestViewBatchFailureDetailBounded(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []interface{}{map[string]interface{}{"file_path": "ok.txt"}}
	for i := 0; i < 60; i++ {
		files = append(files, map[string]interface{}{"file_path": filepath.Join("missing", "file"+string(rune('a'+i%26))+".txt")})
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(ctx, map[string]interface{}{"files": files})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("mixed batch should succeed with partial failures: result=%#v err=%v", result, err)
	}
	if len(result.Content) > viewBatchAggregateBudgetBytes {
		t.Fatalf("failure detail must stay inside the declared aggregate window: got %d bytes", len(result.Content))
	}
	if !strings.Contains(result.Content, "未逐条列出") {
		t.Fatalf("bounded errors block must disclose the omitted failures, got %q", result.Content)
	}
	rows, _ := result.Metadata["failed_items"].([]map[string]interface{})
	if len(rows) > viewBatchFailureDetailLimit {
		t.Fatalf("failed_items rows must be bounded to %d, got %d", viewBatchFailureDetailLimit, len(rows))
	}
	if got, _ := result.Metadata["failures_omitted"].(int); got <= 0 {
		t.Fatalf("expected failures_omitted > 0, got %#v", result.Metadata["failures_omitted"])
	}
}

// TestDerivedRenderClampIsNotRememberedAsCompleteWindow: a notebook/document
// render that folded a long line still registered a complete window, so the next
// identical read returned a stub for content the model never saw.
func TestDerivedRenderClampIsNotRememberedAsCompleteWindow(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := filepath.Join(root, "derived.ipynb")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tool := newViewToolAt(t, root)
	result := &runtimetoolkit.ToolResult{
		Success: true,
		Metadata: map[string]interface{}{
			"offset":               0,
			"limit":                10,
			"lines_read":           10,
			"eof":                  true,
			"is_truncated":         false,
			"long_lines_truncated": 1,
		},
	}
	tool.recordDerivedRender(ctx, path, result, ViewFileRequest{Offset: 0, Limit: 10}, info)
	if _, hit, _ := viewDedupPeek(ctx, path, info, 0, 10); hit {
		t.Fatal("a clamp-folded derived render must not be remembered as a complete window")
	}
}
