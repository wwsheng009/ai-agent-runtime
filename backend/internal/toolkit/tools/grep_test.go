package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestGrepTool(t *testing.T) {
	// 创建测试目录结构
	tmpDir, err := os.MkdirTemp("", "grep-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建测试文件
	testFiles := map[string]string{
		"file1.go": "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"file2.go": "package main\n\nfunc helper() {\n\tprintln(\"helper\")\n}\n",
		"file3.txt": "hello world\nhello universe\n",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantFound bool
		wantError bool
	}{
		{
			name: "search for function",
			params: map[string]interface{}{
				"pattern": "func main",
				"path":    tmpDir,
			},
			wantFound: true,
			wantError: false,
		},
		{
			name: "search with include pattern",
			params: map[string]interface{}{
				"pattern": "println",
				"path":    tmpDir,
				"include": "*.go",
			},
			wantFound: true,
			wantError: false,
		},
		{
			name: "literal text search",
			params: map[string]interface{}{
				"pattern":      "hello",
				"path":         tmpDir,
				"literal_text": true,
			},
			wantFound: true,
			wantError: false,
		},
		{
			name: "missing pattern",
			params: map[string]interface{}{
				"path": tmpDir,
			},
			wantFound: false,
			wantError: true,
		},
		{
			name: "no matches",
			params: map[string]interface{}{
				"pattern": "nonexistent_pattern_xyz",
				"path":    tmpDir,
			},
			wantFound: false,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewGrepTool()
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

			if !result.Success && tt.wantFound {
				t.Errorf("expected to find matches but got failure: %v", result.Error)
			}

			if result.Success {
				t.Logf("Result: %s", result.Content)
			}
		})
	}
}

func TestGrepTool_Interface(t *testing.T) {
	tool := NewGrepTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "grep" {
		t.Errorf("expected name 'grep', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("grep tool should support direct call")
	}
}
