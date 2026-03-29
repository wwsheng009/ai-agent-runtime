package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

func TestDownloadTool_Interface(t *testing.T) {
	tool := NewDownloadTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "download" {
		t.Errorf("expected name 'download', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("download tool should support direct call")
	}

	params := tool.Parameters()
	if params == nil {
		t.Error("parameters should not be nil")
	}
}

func TestDownloadTool_MissingURL(t *testing.T) {
	tool := NewDownloadTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "/tmp/test.txt",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing URL")
	}
}

func TestDownloadTool_InvalidURL(t *testing.T) {
	tool := NewDownloadTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":       "not-a-valid-url",
		"file_path": "/tmp/test.txt",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for invalid URL")
	}
}

func TestDownloadTool_EmitsMutatedPathsMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	dir, err := os.MkdirTemp("", "download-tool-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	target := filepath.Join(dir, "file.txt")

	tool := NewDownloadTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":       server.URL,
		"file_path": target,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	raw, ok := result.Metadata["mutated_paths"]
	if !ok {
		t.Fatalf("expected mutated_paths metadata, got %#v", result.Metadata)
	}
	paths, ok := raw.([]string)
	if !ok {
		rawList, ok := raw.([]interface{})
		if !ok {
			t.Fatalf("expected mutated_paths slice, got %#v", raw)
		}
		paths = make([]string, 0, len(rawList))
		for _, item := range rawList {
			if text, ok := item.(string); ok && text != "" {
				paths = append(paths, text)
			}
		}
	}
	if len(paths) == 0 {
		t.Fatalf("expected mutated_paths metadata, got %#v", raw)
	}
}
