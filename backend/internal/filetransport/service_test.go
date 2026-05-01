package filetransport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalServiceWriteAndAppendFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "chapter.md")
	service := NewLocalService()

	writeResult, err := service.WriteFile(context.Background(), path, []byte("hello"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if !writeResult.Created || writeResult.Action != "create" {
		t.Fatalf("unexpected write result: %+v", writeResult)
	}

	appendResult, err := service.AppendFile(context.Background(), path, []byte("\nworld"))
	if err != nil {
		t.Fatalf("AppendFile failed: %v", err)
	}
	if appendResult.Action != "append" {
		t.Fatalf("unexpected append action: %+v", appendResult)
	}

	data, absPath, err := service.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "hello\nworld" {
		t.Fatalf("unexpected file contents: %q", string(data))
	}
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("expected absolute path to exist: %v", err)
	}
}

func TestLocalServiceRejectsDirectoryTargetsWithHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project", "setting"), 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}

	service := NewLocalService()
	_, err := service.WriteFile(context.Background(), filepath.Join(root, "project", "setting"), []byte("hello"))
	if err == nil {
		t.Fatal("expected directory target to fail")
	}
	if !strings.Contains(err.Error(), "目标路径是目录，不是文件") {
		t.Fatalf("expected kind mismatch guidance, got %v", err)
	}
	if !strings.Contains(err.Error(), candidate) {
		t.Fatalf("expected candidate path %q in hint, got %v", candidate, err)
	}
}
