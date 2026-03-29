package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestGlobTool(t *testing.T) {
	// 创建测试目录结构
	tmpDir, err := os.MkdirTemp("", "glob-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建测试文件
	testFiles := []string{
		"file1.go",
		"file2.go",
		"file3.txt",
		"subdir/file4.go",
		"subdir/file5.txt",
		"subdir/nested/file6.go",
	}

	for _, f := range testFiles {
		path := filepath.Join(tmpDir, f)
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantCount int
		wantError bool
		wantPaths []string
	}{
		{
			name: "match all go files",
			params: map[string]interface{}{
				"pattern": "**/*.go",
				"path":    tmpDir,
			},
			wantCount: 4, // file1.go, file2.go, subdir/file4.go, subdir/nested/file6.go
			wantError: false,
			wantPaths: []string{
				"file1.go",
				"file2.go",
				filepath.Join("subdir", "file4.go"),
				filepath.Join("subdir", "nested", "file6.go"),
			},
		},
		{
			name: "match top-level go files",
			params: map[string]interface{}{
				"pattern": "*.go",
				"path":    tmpDir,
			},
			wantCount: 2, // file1.go, file2.go
			wantError: false,
			wantPaths: []string{"file1.go", "file2.go"},
		},
		{
			name: "match txt files",
			params: map[string]interface{}{
				"pattern": "*.txt",
				"path":    tmpDir,
			},
			wantCount: 1, // file3.txt (top level only)
			wantError: false,
			wantPaths: []string{"file3.txt"},
		},
		{
			name: "match docs directories recursively",
			params: map[string]interface{}{
				"pattern": "**/docs",
				"path":    tmpDir,
			},
			wantCount: 0,
			wantError: false,
		},
		{
			name: "match paths under docs recursively",
			params: map[string]interface{}{
				"pattern": "**/docs/**",
				"path":    tmpDir,
			},
			wantCount: 0,
			wantError: false,
		},
		{
			name: "missing pattern",
			params: map[string]interface{}{
				"path": tmpDir,
			},
			wantCount: 0,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewGlobTool()
			if tt.name == "match docs directories recursively" || tt.name == "match paths under docs recursively" {
				requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "docs", "aicli"), 0755))
				requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "nested", "docs"), 0755))
				requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "docs", "README.md"), []byte("docs"), 0644))
				requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "docs", "aicli", "README.md"), []byte("aicli"), 0644))
				requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "nested", "docs", "guide.md"), []byte("guide"), 0644))
				if tt.name == "match docs directories recursively" {
					tt.wantCount = 2
					tt.wantPaths = []string{"docs", filepath.Join("nested", "docs")}
				} else {
					tt.wantCount = 6
					tt.wantPaths = []string{
						"docs",
						filepath.Join("docs", "README.md"),
						filepath.Join("docs", "aicli"),
						filepath.Join("docs", "aicli", "README.md"),
						filepath.Join("nested", "docs"),
						filepath.Join("nested", "docs", "guide.md"),
					}
				}
			}
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

			// Check result contains expected count
			files, ok := result.Metadata["files"].([]string)
			if !ok {
				t.Fatalf("expected files metadata, got: %#v", result.Metadata)
			}
			if len(files) != tt.wantCount {
				t.Errorf("expected %d files, got %d: %v", tt.wantCount, len(files), files)
			}
			for _, want := range tt.wantPaths {
				found := false
				for _, got := range files {
					if filepath.Clean(got) == filepath.Clean(want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected %q in matches, got %v", want, files)
				}
			}
		})
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestGlobTool_Interface(t *testing.T) {
	tool := NewGlobTool()

	// Test Tool interface
	var _ toolkit.Tool = tool

	if tool.Name() != "glob" {
		t.Errorf("expected name 'glob', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if tool.Version() != "1.0.0" {
		t.Errorf("expected version '1.0.0', got '%s'", tool.Version())
	}

	if !tool.CanDirectCall() {
		t.Error("glob tool should support direct call")
	}

	params := tool.Parameters()
	if params == nil {
		t.Error("parameters should not be nil")
	}

	if params["type"] != "object" {
		t.Error("parameters should be of type object")
	}
}
