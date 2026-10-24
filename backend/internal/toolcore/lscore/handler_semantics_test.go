package lscore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// TestHandlerRendersTreeWithDepth pins the local shape: a directory header, one
// indented line per entry, and a statistics trailer. The remote surface used a
// flat `d name/` list; with one handler there is one shape.
func TestHandlerRendersTreeWithDepth(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "protocol", "openai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "protocol", "openai", "guide.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"depth": 3},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %+v, want success", result)
	}
	for _, want := range []string{"目录:", "📁 protocol/", "📁 protocol/openai/", "📄 protocol/openai/guide.md", "统计:"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
	// A nested entry must keep its parent path: an indented bare "openai/" is
	// ambiguous when two parents share a directory name.
	if strings.Contains(result.Content, "\n  📁 openai/") {
		t.Fatalf("nested entry lost its parent path:\n%s", result.Content)
	}
}

// TestHandlerDepthOneMatchesOneLevelListing pins the default: depth defaults to
// 1, so a caller that never heard of depth gets exactly the old one-level list.
func TestHandlerDepthOneMatchesOneLevelListing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Content, "README.md") || !strings.Contains(result.Content, "src/") {
		t.Fatalf("one-level listing missing entries:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "nested") {
		t.Fatalf("depth 1 must not recurse:\n%s", result.Content)
	}
}

// TestHandlerFileTargetReportsSize pins that pointing ls at a file answers the
// question instead of failing: size and mode are the useful facts.
func TestHandlerFileTargetReportsSize(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"path": "main.go"},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %+v, want success", result)
	}
	if result.Metadata["isDir"] != false {
		t.Fatalf("isDir = %v, want false", result.Metadata["isDir"])
	}
	if size, _ := result.Metadata["size"].(int64); size != int64(len("package main\n")) {
		t.Fatalf("size = %v, want %d", result.Metadata["size"], len("package main\n"))
	}
}

// TestHandlerEmptyDirectoryIsEmptySuccess pins the disposition: an empty
// directory is a successful answer, not a failure the caller should retry.
func TestHandlerEmptyDirectoryIsEmptySuccess(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"path": "empty"},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %+v, want success", result)
	}
	if result.Metadata["empty_result"] != true || result.Metadata["outcome"] != "empty" {
		t.Fatalf("expected empty disposition, got %+v", result.Metadata)
	}
	if !strings.Contains(result.Content, "空目录") {
		t.Fatalf("expected empty-directory body, got %q", result.Content)
	}
}

// TestHandlerClampsDepth pins the depth ceiling: a huge depth is clamped, not
// rejected, because the caller asked for "as deep as sensible".
func TestHandlerClampsDepth(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"depth": 999},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %+v, want success", result)
	}
	if depth := result.Metadata["depth"]; depth != MaxDepth {
		t.Fatalf("depth = %v, want %d", depth, MaxDepth)
	}
}

// TestHandlerRejectsNonPositiveDepth pins the error path: depth 0 is a caller
// mistake, not a request for the default.
func TestHandlerRejectsNonPositiveDepth(t *testing.T) {
	root := t.TempDir()
	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"depth": 0},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatalf("expected depth 0 to fail, got %+v", result)
	}
	if result.ErrorCode != toolcore.ErrInvalidArgument {
		t.Fatalf("error_code = %q, want %q", result.ErrorCode, toolcore.ErrInvalidArgument)
	}
}
