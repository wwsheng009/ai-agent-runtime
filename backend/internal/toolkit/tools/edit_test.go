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
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected line-ending diagnostic, got nil")
	}
	message := result.Error.Error()
	if !strings.Contains(message, "CRLF/LF") {
		t.Fatalf("expected line-ending diagnostic, got %q", message)
	}
}
