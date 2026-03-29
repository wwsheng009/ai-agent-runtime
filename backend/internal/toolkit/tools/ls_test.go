package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestLsTool(t *testing.T) {
	// 创建测试目录结构
	tmpDir, err := os.MkdirTemp("", "ls-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建测试文件和目录
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	testFiles := []string{
		"file1.txt",
		"file2.go",
		"subdir/file3.txt",
	}

	for _, f := range testFiles {
		path := filepath.Join(tmpDir, f)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantError bool
	}{
		{
			name: "list directory",
			params: map[string]interface{}{
				"path": tmpDir,
			},
			wantError: false,
		},
		{
			name: "list with depth",
			params: map[string]interface{}{
				"path":  tmpDir,
				"depth": 2,
			},
			wantError: false,
		},
		{
			name: "list non-existent directory",
			params: map[string]interface{}{
				"path": "/nonexistent/path",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewLsTool()
			result, err := tool.Execute(context.Background(), tt.params)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantError {
				if result.Success {
					t.Error("expected error but got success")
				}
				return
			}

			if !result.Success {
				t.Errorf("unexpected failure: %v", result.Error)
				return
			}

			t.Logf("Result:\n%s", result.Content)
		})
	}
}

func TestLsTool_Interface(t *testing.T) {
	tool := NewLsTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "ls" {
		t.Errorf("expected name 'ls', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("ls tool should support direct call")
	}
}

func TestLsTool_PreservesParentPathForNestedEntries(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "protocol", "openai")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "guide.md"), []byte("guide"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewLsTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":  tmpDir,
		"depth": 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("unexpected tool failure: %v", result.Error)
	}

	if !strings.Contains(result.Content, "📁 protocol/") {
		t.Fatalf("expected top-level protocol directory in output, got:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "📁 protocol/openai/") {
		t.Fatalf("expected nested path to preserve parent context, got:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "\n  📁 openai/") {
		t.Fatalf("expected nested directory not to be rendered as ambiguous base name, got:\n%s", result.Content)
	}
}
