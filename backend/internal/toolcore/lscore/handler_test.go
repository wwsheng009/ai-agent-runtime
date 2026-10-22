package lscore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// TestHandlerListsDirectory exercises the shared Handler without any surface
// dependency: path resolution and permission checks come from the injected
// Runtime, listing semantics stay in lscore.
func TestHandlerListsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"path": "."},
		Runtime: &fakeRuntime{paths: fakePaths{root: root}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("result = %+v, want success", result)
	}
	if !strings.Contains(result.Content, "alpha.txt") {
		t.Fatalf("content missing entry: %q", result.Content)
	}
	if result.Metadata["entries"] == nil {
		t.Fatalf("metadata missing entry count: %+v", result.Metadata)
	}
}

// TestHandlerClassifiesMissingDirectory pins the stable NOT_FOUND code instead
// of returning a raw filesystem error.
func TestHandlerClassifiesMissingDirectory(t *testing.T) {
	handler := NewHandler(toolcore.Definition{Name: "ls"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"path": "missing"},
		Runtime: &fakeRuntime{paths: fakePaths{root: t.TempDir()}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("result = %+v, want classified failure", result)
	}
	if result.ErrorCode != toolcore.ErrNotFound {
		t.Fatalf("error_code = %q, want %q", result.ErrorCode, toolcore.ErrNotFound)
	}
}

type fakePaths struct {
	root string
}

func (p fakePaths) Root() string { return p.root }

func (p fakePaths) Resolve(raw string) (string, error) {
	if raw == "" {
		return p.root, nil
	}
	return filepath.Join(p.root, raw), nil
}

func (p fakePaths) Relative(abs string) string {
	rel, err := filepath.Rel(p.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

type fakeRuntime struct {
	paths toolcore.PathResolver
}

func (f *fakeRuntime) Paths() toolcore.PathResolver            { return f.paths }
func (f *fakeRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (f *fakeRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (f *fakeRuntime) Env() toolcore.EnvProvider               { return nil }
func (f *fakeRuntime) Output() toolcore.OutputShaper           { return nil }
func (f *fakeRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (f *fakeRuntime) Sessions() toolcore.SessionState         { return nil }
func (f *fakeRuntime) Clock() toolcore.Clock                   { return nil }
func (f *fakeRuntime) IDs() toolcore.IDGenerator               { return nil }
