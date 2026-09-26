package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newViewToolAt builds a view tool bound to root; every hardening test needs
// the same three lines.
func newViewToolAt(t *testing.T, root string) *ViewTool {
	t.Helper()
	tool := NewViewTool()
	tool.SetBasePath(root)
	return tool
}

// TestViewBatchAggregateCapStopsAtBudget pins the §3.1 fix: per-item byte
// budgets cannot bound the combined payload, so executeBatch must stop at the
// aggregate window and say how many files it skipped.
func TestViewBatchAggregateCapStopsAtBudget(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", 100)
	for i := 0; i < 20; i++ {
		var b strings.Builder
		for j := 0; j < viewBatchDefaultLimit; j++ {
			fmt.Fprintf(&b, "%s%02d\n", line, j)
		}
		name := filepath.Join(root, fmt.Sprintf("f%02d.txt", i))
		if err := os.WriteFile(name, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	tool := newViewToolAt(t, root)
	files := make([]interface{}, 0, 20)
	for i := 0; i < 20; i++ {
		files = append(files, map[string]interface{}{"file_path": fmt.Sprintf("f%02d.txt", i)})
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{"files": files})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("batch read failed: result=%#v err=%v", result, err)
	}

	emitted, _ := result.Metadata["batch_bytes_emitted"].(int)
	if emitted <= 0 || emitted > viewBatchAggregateBudgetBytes {
		t.Fatalf("emitted=%d must be within (0, %d]", emitted, viewBatchAggregateBudgetBytes)
	}
	skipped, _ := result.Metadata["batch_skipped_count"].(int)
	if skipped == 0 {
		t.Fatalf("expected skipped files once the aggregate cap is reached, metadata=%#v", result.Metadata)
	}
	if result.Metadata["batch_aggregate_budget_bytes"] != viewBatchAggregateBudgetBytes {
		t.Fatalf("aggregate budget metadata missing: %#v", result.Metadata["batch_aggregate_budget_bytes"])
	}
	if !strings.Contains(result.Content, "batch window") {
		t.Fatalf("expected a batch window summary, got %q", result.Content[len(result.Content)-200:])
	}
	// The rendered body must respect the cap (plus small join/summary overhead).
	if len(result.Content) > viewBatchAggregateBudgetBytes+4*1024 {
		t.Fatalf("batch content %d bytes exceeds cap %d", len(result.Content), viewBatchAggregateBudgetBytes)
	}
	items, _ := result.Metadata["items"].([]map[string]interface{})
	if len(items) >= len(files) {
		t.Fatalf("expected skipped items to stay out of items[], got %d of %d", len(items), len(files))
	}
}

// TestViewBatchAggregateKeepsPartialFailureSemantics checks the cap does not
// disturb the existing failed/partial accounting when it never triggers.
func TestViewBatchAggregateKeepsPartialFailureSemantics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write ok: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"files": []interface{}{
			map[string]interface{}{"file_path": "ok.txt"},
			map[string]interface{}{"file_path": "missing.txt"},
		},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("batch read failed: result=%#v err=%v", result, err)
	}
	if result.Metadata["failed_count"] != 1 || result.Metadata["partial_failure"] != true {
		t.Fatalf("unexpected failure accounting: %#v", result.Metadata)
	}
	if _, exists := result.Metadata["batch_skipped_count"]; exists {
		t.Fatalf("no file should be skipped below the cap: %#v", result.Metadata)
	}
}

// TestViewBatchIgnoresTopLevelWindowWithNote pins the "silence still costs"
// fix: a files[]-only call carrying top-level offset/limit must say the
// parameters did not apply instead of dropping them silently.
func TestViewBatchIgnoresTopLevelWindowWithNote(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "five.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write five: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"files":  []interface{}{map[string]interface{}{"file_path": "five.txt"}},
		"offset": float64(2),
		"limit":  float64(1),
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("batch read failed: result=%#v err=%v", result, err)
	}
	if !strings.Contains(result.Content, "未生效") {
		t.Fatalf("expected a note about the ignored top-level window, got %q", result.Content)
	}
	if result.Metadata["ignored_top_level_window"] == nil {
		t.Fatalf("expected ignored_top_level_window metadata: %#v", result.Metadata)
	}
	items, _ := result.Metadata["items"].([]map[string]interface{})
	if len(items) != 1 {
		t.Fatalf("expected one item, got %#v", result.Metadata["items"])
	}
	if items[0]["lines_read"] != 5 {
		t.Fatalf("top-level limit must not apply inside files[], got %#v", items[0])
	}
	if items[0]["offset"] != 0 {
		t.Fatalf("top-level offset must not apply inside files[], got %#v", items[0])
	}
}

// TestViewTool_EmptyFileNote: an empty file is a fact with an explicit note,
// not an offset-beyond-EOF message (analysis §3.5).
func TestViewTool_EmptyFileNote(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "empty.txt"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("empty read failed: result=%#v err=%v", result, err)
	}
	if !strings.Contains(result.Content, "file is empty (0 lines)") {
		t.Fatalf("expected an explicit empty-file note, got %q", result.Content)
	}
	if result.Metadata["empty"] != true || result.Metadata["eof"] != true {
		t.Fatalf("expected empty/eof metadata, got %#v", result.Metadata)
	}
	if _, exists := result.Metadata["is_truncated"]; !exists {
		t.Fatalf("metadata contract must keep is_truncated: %#v", result.Metadata)
	}
}

// TestViewTool_NegativeOffsetReadsTail pins offset<0 as "last N lines" with
// absolute line numbers and resolved window metadata.
func TestViewTool_NegativeOffsetReadsTail(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(-3),
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("tail read failed: result=%#v err=%v", result, err)
	}
	for _, want := range []string{"8: line-8", "9: line-9", "10: line-10"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("tail content missing %q: %q", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "7: line-7") {
		t.Fatalf("tail read returned too many lines: %q", result.Content)
	}
	if result.Metadata["tail"] != true || result.Metadata["offset"] != 7 ||
		result.Metadata["lines_read"] != 3 || result.Metadata["total_lines"] != 10 {
		t.Fatalf("unexpected tail metadata: %#v", result.Metadata)
	}
}

// TestViewTool_LongLineFollowedByNormalLines: an over-reader-cap line is
// clamped with a byte-honest marker and the following lines stay readable.
func TestViewTool_LongLineFollowedByNormalLines(t *testing.T) {
	root := t.TempDir()
	huge := strings.Repeat("x", 300*1024)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(huge+"\nafter\n"), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "big.txt"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("long-line read failed: result=%#v err=%v", result, err)
	}
	if !strings.Contains(result.Content, fmt.Sprintf(viewReaderClampedMarker, viewMaxLineChars, 300*1024)) {
		t.Fatalf("expected the reader clamp marker, got %q", result.Content[:min(400, len(result.Content))])
	}
	if !strings.Contains(result.Content, "\n2: after") {
		t.Fatalf("expected the line after the clamped one, got %q", result.Content[len(result.Content)-80:])
	}
	if result.Metadata["long_lines_truncated"] != 1 {
		t.Fatalf("expected long_lines_truncated=1, got %#v", result.Metadata)
	}
	if result.Metadata["reader_clamped_lines"] != 1 {
		t.Fatalf("expected reader_clamped_lines=1, got %#v", result.Metadata)
	}
	if len(result.Content) > viewByteBudgetBytes() {
		t.Fatalf("clamped content must stay inside the byte budget, got %d", len(result.Content))
	}
}

// TestViewTool_SingleLineOverReaderCapIsClampedNotFatal replaces the old
// "token too long" dead end for multi-megabyte single-line files.
func TestViewTool_SingleLineOverReaderCapIsClampedNotFatal(t *testing.T) {
	root := t.TempDir()
	payload := []byte(strings.Repeat("y", 8<<20))
	if err := os.WriteFile(filepath.Join(root, "huge.txt"), payload, 0o644); err != nil {
		t.Fatalf("write huge: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "huge.txt"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("huge single-line read must not be fatal: result=%#v err=%v", result, err)
	}
	if !strings.Contains(result.Content, "line truncated") {
		t.Fatalf("expected a clamp marker, got %q", result.Content[:min(200, len(result.Content))])
	}
	clampedBytes, _ := result.Metadata["reader_clamped_bytes"].(int64)
	if clampedBytes < int64(8<<20)-4000 {
		t.Fatalf("expected hidden bytes to account for the drained remainder, got %d", clampedBytes)
	}
	if len(result.Content) > viewByteBudgetBytes() {
		t.Fatalf("content %d exceeds the byte budget", len(result.Content))
	}
}
