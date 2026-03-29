package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestWriteTool_EmitsPatchMetadata(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "write-tool-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(path)

	tool := NewWriteTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": path,
		"content":   "hello\nworld\n",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	patch, _ := result.Metadata["patch"].(string)
	if !strings.Contains(patch, "+++ b/") || !strings.Contains(patch, "hello") {
		t.Fatalf("expected unified diff patch metadata, got %q", patch)
	}
}

func TestEditTool_EmitsPatchMetadata(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "edit-tool-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	if _, err := tmpFile.WriteString("line1\nline2\n"); err != nil {
		t.Fatal(err)
	}
	_ = tmpFile.Close()
	defer os.Remove(path)

	tool := NewEditTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  path,
		"old_string": "line2",
		"new_string": "LINE2",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	patch, _ := result.Metadata["patch"].(string)
	if !strings.Contains(patch, "-line2") || !strings.Contains(patch, "+LINE2") {
		t.Fatalf("expected edit diff patch metadata, got %q", patch)
	}
}

func TestWriteTool_EmitsMutatedPaths(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "write-tool-mutation-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(path)

	tool := NewWriteTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": path,
		"content":   "mutation\n",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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
