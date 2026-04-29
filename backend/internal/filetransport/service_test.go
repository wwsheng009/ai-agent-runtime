package filetransport

import (
	"context"
	"os"
	"path/filepath"
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
