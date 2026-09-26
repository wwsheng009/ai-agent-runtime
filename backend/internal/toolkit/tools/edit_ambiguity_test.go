package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newEditTestTool(t *testing.T, root string) *EditTool {
	t.Helper()
	tool := NewEditTool()
	tool.SetBasePath(root)
	return tool
}

func writeEditTestFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestEditRejectsAmbiguousExactMatch(t *testing.T) {
	root := t.TempDir()
	path := writeEditTestFile(t, root, "ambiguous.txt", "target line\nother\ntarget line\nother\ntarget line\n")

	tool := newEditTestTool(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "ambiguous.txt",
		"old_string": "target line",
		"new_string": "changed line",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected ambiguity rejection, got success: %s", result.Content)
	}
	if !strings.Contains(result.Error.Error(), "ambiguous edit") {
		t.Fatalf("expected ambiguous edit error, got %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "命中 3 处") {
		t.Fatalf("expected occurrence count in error, got %v", result.Error)
	}
	if result.Metadata["failure_class"] != "ambiguous_edit" {
		t.Fatalf("expected failure_class=ambiguous_edit, got %#v", result.Metadata)
	}
	if result.Metadata["occurrences"] != 3 {
		t.Fatalf("expected occurrences=3, got %#v", result.Metadata["occurrences"])
	}
	lines, _ := result.Metadata["occurrence_lines"].([]int)
	if len(lines) != 3 || lines[0] != 1 || lines[1] != 3 || lines[2] != 5 {
		t.Fatalf("expected occurrence lines [1 3 5], got %#v", lines)
	}
	if content, _ := os.ReadFile(path); string(content) != "target line\nother\ntarget line\nother\ntarget line\n" {
		t.Fatalf("ambiguous edit must not modify the file, got %q", content)
	}
}

func TestEditReplacementCountReplacesFirstN(t *testing.T) {
	root := t.TempDir()
	path := writeEditTestFile(t, root, "counter.txt", "FOO\nbar\nFOO\nbaz\nFOO\n")

	tool := newEditTestTool(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":         "counter.txt",
		"old_string":        "FOO",
		"new_string":        "QUX",
		"replacement_count": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %v", result.Error)
	}
	if result.Metadata["replacements"] != 2 || result.Metadata["occurrences"] != 3 {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if result.Metadata["replacement_count"] != 2 {
		t.Fatalf("expected replacement_count in metadata, got %#v", result.Metadata["replacement_count"])
	}
	content, _ := os.ReadFile(path)
	if string(content) != "QUX\nbar\nQUX\nbaz\nFOO\n" {
		t.Fatalf("expected first two occurrences replaced, got %q", content)
	}
}

func TestEditReplacementCountValidation(t *testing.T) {
	root := t.TempDir()
	writeEditTestFile(t, root, "validate.txt", "value\n")
	tool := newEditTestTool(t, root)

	mutual, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":         "validate.txt",
		"old_string":        "value",
		"new_string":        "other",
		"replace_all":       true,
		"replacement_count": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mutual.Success || !strings.Contains(mutual.Error.Error(), "互斥") {
		t.Fatalf("expected mutual exclusion error, got %+v", mutual)
	}

	zero, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":         "validate.txt",
		"old_string":        "value",
		"new_string":        "other",
		"replacement_count": 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zero.Success || !strings.Contains(zero.Error.Error(), "必须大于 0") {
		t.Fatalf("expected positive-count error, got %+v", zero)
	}
}

func TestEditRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "huge.txt")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), editMaxFileBytes+1), 0o644); err != nil {
		t.Fatalf("write huge file: %v", err)
	}

	tool := newEditTestTool(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "huge.txt",
		"old_string": "a",
		"new_string": "b",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected oversized file rejection")
	}
	if result.Metadata["failure_class"] != "file_too_large" {
		t.Fatalf("expected failure_class=file_too_large, got %#v", result.Metadata)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Size() != editMaxFileBytes+1 {
		t.Fatalf("oversized file must stay untouched, size=%d err=%v", info, statErr)
	}
}

func TestEditSuccessIncludesEditedSnippet(t *testing.T) {
	root := t.TempDir()
	writeEditTestFile(t, root, "snippet_success.txt", "alpha\nbeta\ngamma\n")

	tool := newEditTestTool(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "snippet_success.txt",
		"old_string": "beta",
		"new_string": "beta-updated",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("expected success, got %v %+v", err, result)
	}
	snippet, _ := result.Metadata["edited_snippet"].(string)
	if !strings.Contains(snippet, "beta-updated") {
		t.Fatalf("expected edited snippet with new text, got %#v", result.Metadata)
	}
	if result.Metadata["edited_snippet_start_line"] == nil {
		t.Fatalf("expected edited_snippet_start_line, got %#v", result.Metadata)
	}
}
