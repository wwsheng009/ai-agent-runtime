package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestMultieditTool(t *testing.T) {
	// 创建测试文件
	tmpFile, err := osCreateTempFile("multiedit-test-*.txt", "line1\nline2\nline3\n")
	if err != nil {
		t.Fatal(err)
	}
	defer osRemove(tmpFile)

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantError bool
	}{
		{
			name: "single edit",
			params: map[string]interface{}{
				"file_path": tmpFile,
				"edits": []interface{}{
					map[string]interface{}{
						"old_string": "line1",
						"new_string": "LINE1",
					},
				},
			},
			wantError: false,
		},
		{
			name: "multiple edits",
			params: map[string]interface{}{
				"file_path": tmpFile,
				"edits": []interface{}{
					map[string]interface{}{
						"old_string": "line1",
						"new_string": "LINE1",
					},
					map[string]interface{}{
						"old_string": "line2",
						"new_string": "LINE2",
					},
				},
			},
			wantError: false,
		},
		{
			name: "missing file_path",
			params: map[string]interface{}{
				"edits": []interface{}{
					map[string]interface{}{
						"old_string": "test",
						"new_string": "TEST",
					},
				},
			},
			wantError: true,
		},
		{
			name: "missing edits",
			params: map[string]interface{}{
				"file_path": tmpFile,
			},
			wantError: true,
		},
		{
			name: "non-matching old_string",
			params: map[string]interface{}{
				"file_path": tmpFile,
				"edits": []interface{}{
					map[string]interface{}{
						"old_string": "nonexistent_string_xyz",
						"new_string": "TEST",
					},
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset file content for each test
			if tt.params["file_path"] != nil {
				osWriteFile(tmpFile, "line1\nline2\nline3\n")
			}

			tool := NewMultieditTool()
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

			t.Logf("Result: %s", result.Content)
		})
	}
}

func TestMultieditTool_Interface(t *testing.T) {
	tool := NewMultieditTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "multiedit" {
		t.Errorf("expected name 'multiedit', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("multiedit tool should support direct call")
	}
}

func TestMultieditTool_EmitsMutatedPaths(t *testing.T) {
	tmpFile, err := osCreateTempFile("multiedit-mutation-*.txt", "line1\nline2\n")
	if err != nil {
		t.Fatal(err)
	}
	defer osRemove(tmpFile)

	tool := NewMultieditTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": tmpFile,
		"edits": []interface{}{
			map[string]interface{}{
				"old_string": "line2",
				"new_string": "LINE2",
			},
		},
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
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				paths = append(paths, text)
			}
		}
	}
	if len(paths) == 0 {
		t.Fatalf("expected mutated_paths metadata, got %#v", raw)
	}
}

func TestMultieditTool_PathNotFoundIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "runtime.yaml")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	tool := NewMultieditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting/runtime.yaml",
		"edits": []interface{}{
			map[string]interface{}{
				"old_string": "line1",
				"new_string": "LINE1",
			},
		},
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

func TestMultieditTool_DirectoryPathIncludesKindMismatchHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project", "setting"), 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}

	tool := NewMultieditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting",
		"edits": []interface{}{
			map[string]interface{}{
				"old_string": "line1",
				"new_string": "LINE1",
			},
		},
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

// Helper functions
func osCreateTempFile(pattern, content string) (string, error) {
	tmpFile, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	if _, err := tmpFile.WriteString(content); err != nil {
		return "", err
	}

	return tmpFile.Name(), nil
}

func osRemove(path string) {
	os.Remove(path)
}

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
