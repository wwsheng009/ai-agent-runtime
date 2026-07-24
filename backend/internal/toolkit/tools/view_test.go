package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestViewTool_DescriptionAndSchemaSupportBatchReads(t *testing.T) {
	tool := NewViewTool()

	desc := tool.Description()
	if !strings.Contains(desc, "多个文件") || !strings.Contains(desc, "files") {
		t.Fatalf("expected view description to advertise batch reads, got %q", desc)
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
	if !strings.Contains(pathDesc, "files") {
		t.Fatalf("expected file_path description to point to batch mode, got %q", pathDesc)
	}
	filesSchema, ok := props["files"].(map[string]interface{})
	if !ok || filesSchema["type"] != "array" {
		t.Fatalf("expected files array schema, got %#v", props["files"])
	}
}

func TestViewTool_OutputPreservesLinesAndAddsLineNumbers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    1,
		"limit":     2,
	})
	if err != nil || !result.Success {
		t.Fatalf("expected successful read, result=%#v err=%v", result, err)
	}
	if result.Content != "2: two\n3: three" {
		t.Fatalf("expected stable line-oriented output, got %q", result.Content)
	}
}

func TestViewTool_BatchReadsReturnSuccessfulFilesAndPartialErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bravo\ncharlie\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"files": []interface{}{
			map[string]interface{}{"file_path": "a.txt", "limit": 1},
			map[string]interface{}{"file_path": "b.txt", "offset": 1, "limit": 1},
			map[string]interface{}{"file_path": "missing.txt"},
		},
	})
	if err != nil || !result.Success {
		t.Fatalf("expected partial batch success, result=%#v err=%v", result, err)
	}
	for _, want := range []string{"===== a.txt =====", "1: alpha", "===== b.txt =====", "2: charlie", "===== errors =====", "missing.txt"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("expected batch output to contain %q, got %q", want, result.Content)
		}
	}
	if result.Metadata["succeeded_count"] != 2 || result.Metadata["failed_count"] != 1 || result.Metadata["partial_failure"] != true {
		t.Fatalf("unexpected batch metadata: %#v", result.Metadata)
	}
}

func TestViewTool_LongUnicodeLineIsReadableAndTruncatedSafely(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("界", 25000)
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write long line: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "long.txt", "limit": 1})
	if err != nil || !result.Success {
		t.Fatalf("expected long line read to succeed, result=%#v err=%v", result, err)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatalf("expected valid UTF-8 after truncation")
	}
	if !strings.HasSuffix(result.Content, "...") {
		t.Fatalf("expected long line truncation marker, got suffix %q", result.Content[len(result.Content)-10:])
	}
}

func TestViewTool_LargeFileCanBeReadInSmallRanges(t *testing.T) {
	root := t.TempDir()
	content := append([]byte("one\ntwo\n"), bytes.Repeat([]byte("x\n"), 3*1024*1024)...)
	if err := os.WriteFile(filepath.Join(root, "large.log"), content, 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "large.log",
		"limit":     1,
	})
	if err != nil || !result.Success {
		t.Fatalf("expected range read from large file, result=%#v err=%v", result, err)
	}
	if result.Content != "1: one" || result.Metadata["is_truncated"] != true {
		t.Fatalf("unexpected large-file range result: content=%q metadata=%#v", result.Content, result.Metadata)
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

func TestViewTool_DirectoryPathAutoListsContents(t *testing.T) {
	root := t.TempDir()
	dirPath := filepath.Join(root, "project", "setting")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "token.go"), []byte("package types\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dirPath, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected auto-list success, got error %v content %q", result.Error, result.Content)
	}
	if !strings.Contains(result.Content, "路径是目录，不是文件") {
		t.Fatalf("expected directory notice, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "token.go") {
		t.Fatalf("expected listed file token.go, got %q", result.Content)
	}
	if result.Metadata["is_directory"] != true || result.Metadata["auto_listed"] != true {
		t.Fatalf("expected directory auto-list metadata, got %#v", result.Metadata)
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
