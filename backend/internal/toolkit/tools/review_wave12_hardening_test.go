package tools

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestViewTailMinIntOffsetDoesNotPanic pins the review blocker: -MinInt
// overflows back to a negative value, and make([]string, negative) panics.
func TestViewTailMinIntOffsetDoesNotPanic(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tail.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	// 整数形态直达 readTailLines（-MinInt 溢出），这是 review B1 的触发面。
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "tail.txt",
		"offset":    math.MinInt64,
	})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected a bounded tail read instead of a panic, got %+v", result)
	}
	if !strings.Contains(result.Content, "three") {
		t.Fatalf("expected at least the last line, got %q", result.Content)
	}

	// provider 常见的 float64 形态会先被参数解析拒掉：也必须是结构化失败，
	// 不能 panic。
	floatResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "tail.txt",
		"offset":    float64(math.MinInt64),
	})
	if err != nil {
		t.Fatalf("unexpected tool error for float form: %v", err)
	}
	if floatResult == nil || floatResult.Success {
		t.Fatalf("float form should fail in argument parsing, got %+v", floatResult)
	}
}

// TestEditStaleRefusalDoesNotSuggestUnsupportedArgument: the refusal message
// must not tell the model to retry edit with expected_sha256, which edit does
// not accept (review M1).
func TestEditStaleRefusalDoesNotSuggestUnsupportedArgument(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	viewTool := newViewToolAt(t, root)
	ctx := toolctx.WithSessionID(context.Background(), "review-stale-edit-"+t.Name())
	if _, err := viewTool.Execute(ctx, map[string]interface{}{"file_path": "notes.txt"}); err != nil {
		t.Fatalf("view: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed externally\nsecond\n"), 0o644); err != nil {
		t.Fatalf("external change: %v", err)
	}

	editTool := NewEditTool()
	editTool.SetBasePath(root)
	result, err := editTool.Execute(ctx, map[string]interface{}{
		"file_path":  "notes.txt",
		"old_string": "second",
		"new_string": "second-updated",
	})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a stale refusal, got %+v", result)
	}
	if strings.Contains(result.Error.Error(), "expected_sha256=") {
		t.Fatalf("edit must not advertise an argument it ignores: %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "view") {
		t.Fatalf("expected the re-view recovery route, got %v", result.Error)
	}
}

// TestWriteStaleRefusalKeepsExpectedSHAHint: write does accept the argument, so
// its hint stays.
func TestWriteStaleRefusalKeepsExpectedSHAHint(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	path := filepath.Join(root, "state.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	viewTool := newViewToolAt(t, root)
	ctx := toolctx.WithSessionID(context.Background(), "review-stale-write-"+t.Name())
	if _, err := viewTool.Execute(ctx, map[string]interface{}{"file_path": "state.txt"}); err != nil {
		t.Fatalf("view: %v", err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external change: %v", err)
	}

	writeTool := NewWriteTool()
	writeTool.SetBasePath(root)
	result, err := writeTool.Execute(ctx, map[string]interface{}{
		"file_path": "state.txt",
		"content":   "v2\n",
	})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a stale refusal, got %+v", result)
	}
	if !strings.Contains(result.Error.Error(), "expected_sha256=") {
		t.Fatalf("write must keep the explicit-confirmation hint: %v", result.Error)
	}
}

// TestViewDedupSkippedWithoutSessionID: the empty session bucket would share
// "already read" state across unrelated calls.
func TestViewDedupSkippedWithoutSessionID(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nosession.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	first := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "nosession.txt"})
	if !strings.Contains(first.Content, "payload") {
		t.Fatalf("unexpected first read: %q", first.Content)
	}
	second := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "nosession.txt"})
	if second.Metadata["dedup_hit"] == true {
		t.Fatalf("dedup must be skipped without a session ID, got %#v", second.Metadata)
	}
	if !strings.Contains(second.Content, "payload") {
		t.Fatalf("second read must return the real content, got %q", second.Content)
	}
}
