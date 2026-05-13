package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewTool_DescriptionGuidesSingleFileFocus(t *testing.T) {
	tool := NewViewTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个文件") {
		t.Fatalf("expected view description to guide single-file focus, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	pathSchema, ok := props["file_path"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected file_path schema in properties, got %#v", props)
	}
	pathDesc, _ := pathSchema["description"].(string)
	if !strings.Contains(pathDesc, "拆分") || !strings.Contains(pathDesc, "多个文件") {
		t.Fatalf("expected file_path description to guide single-file focus, got %q", pathDesc)
	}
}

func TestViewTool_PathNotFoundIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "file.txt")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting/file.txt",
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

func TestViewTool_DirectoryPathIncludesKindMismatchHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project", "setting"), 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting",
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

func TestViewTool_OffsetBeyondEOFReturnsExplicitMessage(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(10),
		"limit":     float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "Reached end of file: offset 10 is beyond total lines 3.") {
		t.Fatalf("expected explicit EOF message, got %q", result.Content)
	}
	if result.Metadata["total_lines"] != 3 {
		t.Fatalf("expected total_lines metadata 3, got %#v", result.Metadata["total_lines"])
	}
	if result.Metadata["eof"] != true {
		t.Fatalf("expected eof metadata true, got %#v", result.Metadata["eof"])
	}
}

func TestViewTool_TruncatedReadDoesNotRequireTotalLineCount(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(0),
		"limit":     float64(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected truncated metadata true, got %#v", result.Metadata["is_truncated"])
	}
	if _, ok := result.Metadata["total_lines"]; ok {
		t.Fatalf("did not expect total_lines on truncated read, got %#v", result.Metadata["total_lines"])
	}
}

func TestViewTool_OffsetAtEOFReturnsExplicitMessage(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(3),
		"limit":     float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "Reached end of file: offset 3 equals total lines 3.") {
		t.Fatalf("expected exact EOF message, got %q", result.Content)
	}
	if result.Metadata["total_lines"] != 3 {
		t.Fatalf("expected total_lines metadata 3, got %#v", result.Metadata["total_lines"])
	}
	if result.Metadata["is_truncated"] != false {
		t.Fatalf("expected truncated metadata false, got %#v", result.Metadata["is_truncated"])
	}
	if result.Metadata["eof"] != true {
		t.Fatalf("expected eof metadata true, got %#v", result.Metadata["eof"])
	}
}
