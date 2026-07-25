package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTool_PathNotFoundIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "runtime.yaml")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("old"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":   "project/setting/runtime.yaml",
		"old_string":  "old",
		"new_string":  "new",
		"replace_all": false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected path error, got nil")
	}
	hint := result.Error.Error()
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
}

func TestEditTool_DirectoryPathIncludesKindMismatchHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project", "setting"), 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "project/setting",
		"old_string": "old",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected path error, got nil")
	}
	hint := result.Error.Error()
	if !strings.Contains(hint, "路径是目录，不是文件") {
		t.Fatalf("expected kind mismatch guidance, got %q", hint)
	}
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
}

func TestEditTool_NotFoundGuidesApplyPatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("current text\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "README.md",
		"old_string": "stale text",
		"new_string": "updated text",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected old_string error, got nil")
	}
	message := result.Error.Error()
	if !strings.Contains(message, "apply_patch") || !strings.Contains(message, "view/grep") {
		t.Fatalf("expected apply_patch/view guidance, got %q", message)
	}
	if !strings.Contains(message, "old_string 预览") {
		t.Fatalf("expected old_string preview, got %q", message)
	}
	if !strings.Contains(message, "next_action") {
		t.Fatalf("expected next_action text in error message, got %q", message)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "apply_patch") && !strings.Contains(next, "view") {
		t.Fatalf("expected structured next_action metadata, got %q metadata=%#v", next, result.Metadata)
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT error_code, got %#v", result.Metadata)
	}
	if retryable, _ := result.Metadata["retryable"].(bool); retryable {
		t.Fatalf("STALE_CONTEXT must not be retryable, got %#v", result.Metadata)
	}
}

func TestEditTool_NotFoundDetectsLineEndingDifference(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "crlf.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "crlf.txt",
		"old_string": "alpha\nbeta\n",
		"new_string": "updated\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected CRLF/LF auto-heal success, got error: %v content=%q", result.Error, result.Content)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	if got := string(data); got != "updated\r\n" {
		t.Fatalf("expected CRLF-preserving replacement, got %q", got)
	}
}

func TestEditTool_NotFoundIncludesClosestSnippet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "snippet.txt")
	if err := os.WriteFile(path, []byte("func HelloWorld() {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "snippet.txt",
		"old_string": "func HelloWord() {\n\treturn 2\n}",
		"new_string": "func HelloWorld() {\n\treturn 2\n}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success")
	}
	message := result.Error.Error()
	if !strings.Contains(message, "最接近片段") {
		t.Fatalf("expected closest snippet guidance, got %q", message)
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT error_code, got %#v", result.Metadata)
	}
	if offset, ok := result.Metadata["suggested_view_offset"].(int); !ok || offset != 0 {
		t.Fatalf("expected suggested_view_offset=0 for first-line closest match, got %#v", result.Metadata)
	}
	if limit, ok := result.Metadata["suggested_view_limit"].(int); !ok || limit != 40 {
		t.Fatalf("expected suggested_view_limit=40, got %#v", result.Metadata)
	}
	if !strings.Contains(message, "suggested_view_offset=") {
		t.Fatalf("expected suggested_view_offset hint in error text, got %q", message)
	}
}

func TestEditTool_HealsCRLFOldStringAgainstLFFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lf.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "lf.txt",
		"old_string": "alpha\r\nbeta\r\n",
		"new_string": "updated\r\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected CRLF old_string vs LF file auto-heal, got error: %v content=%q", result.Error, result.Content)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	if got := string(data); got != "updated\n" {
		t.Fatalf("expected LF-preserving replacement, got %q", got)
	}
}

func TestEditTool_NotFoundIncludesStructuredNextAction(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "sample.txt",
		"old_string": "missing",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected failure, got %#v", result)
	}
	next, _ := result.Metadata["next_action"].(string)
	if next == "" {
		t.Fatalf("expected next_action metadata, got %#v", result.Metadata)
	}
}
