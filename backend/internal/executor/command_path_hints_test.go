package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPathNotFoundHintFromTokens_UsesWorkdirCandidates(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	candidate := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime.yaml")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	tokens := SplitCommandTokens(`git diff -- "frontend/src/pages/setting/runtime.yaml"`)
	hint := BuildPathNotFoundHintFromTokens(tokens, workdir)
	if !strings.Contains(hint, "workdir=") {
		t.Fatalf("expected workdir guidance, got %q", hint)
	}
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
	if !strings.Contains(hint, "frontend/src/pages/setting/runtime.yaml") {
		t.Fatalf("expected quoted path token in hint, got %q", hint)
	}
}

func TestBuildPathNotFoundHintForPath_UsesWorkdirCandidates(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "project")
	candidate := filepath.Join(workdir, "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}

	hint := BuildPathNotFoundHintForPath("project/setting", root)
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
	if !strings.Contains(hint, "project/setting") {
		t.Fatalf("expected original path in hint, got %q", hint)
	}
}

func TestBuildPathKindMismatchHintForPath_UsesSiblingCandidates(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(workdir, "setting"), 0o755); err != nil {
		t.Fatalf("mkdir path tree: %v", err)
	}
	candidate := filepath.Join(workdir, "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}

	hint := BuildPathKindMismatchHintForPath("project/setting", root)
	if !strings.Contains(hint, "路径是目录，不是文件") {
		t.Fatalf("expected kind-mismatch guidance, got %q", hint)
	}
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
}
