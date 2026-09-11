package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestResolvePathWithContextPrefersSessionRoot verifies the file-tool path
// resolver anchors relative targets to the session-bound workspace root from
// ctx, falling back to the registered basePath only when no root is bound.
func TestResolvePathWithContextPrefersSessionRoot(t *testing.T) {
	globalBase := filepath.Join(t.TempDir(), "global-base")
	sessionRoot := filepath.Join(t.TempDir(), "session-root")

	tool := NewViewTool()
	tool.SetBasePath(globalBase)

	ctx := toolctx.WithWorkspaceRoot(context.Background(), sessionRoot)
	if got := tool.resolvePathWithContext(ctx, "src/main.go"); got != filepath.Clean(filepath.Join(sessionRoot, "src", "main.go")) {
		t.Fatalf("expected session-root join %q, got %q", filepath.Clean(filepath.Join(sessionRoot, "src", "main.go")), got)
	}

	// No session root in ctx: legacy basePath behavior must be preserved.
	if got := tool.resolvePathWithContext(context.Background(), "src/main.go"); got != filepath.Clean(filepath.Join(globalBase, "src", "main.go")) {
		t.Fatalf("expected basePath join %q, got %q", filepath.Clean(filepath.Join(globalBase, "src", "main.go")), got)
	}

	// Absolute targets are returned unchanged regardless of roots.
	abs := filepath.Join(sessionRoot, "abs", "file.txt")
	if got := tool.resolvePathWithContext(ctx, abs); got != filepath.Clean(abs) {
		t.Fatalf("expected absolute passthrough %q, got %q", filepath.Clean(abs), got)
	}

	// Empty target stays empty.
	if got := tool.resolvePathWithContext(ctx, "  "); got != "" {
		t.Fatalf("expected empty passthrough, got %q", got)
	}
}

// TestGlobToolResolvesRelativeSearchPathAgainstSessionRoot proves the
// end-to-end behavior: a tool registered with a global basePath still searches
// inside the session-bound workspace when the invocation ctx carries one.
func TestGlobToolResolvesRelativeSearchPathAgainstSessionRoot(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "base-a-only.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write rootA marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "session-b-only.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write rootB marker: %v", err)
	}

	tool := NewGlobTool()
	tool.SetBasePath(rootA)

	ctx := toolctx.WithWorkspaceRoot(context.Background(), rootB)
	result, err := tool.Execute(ctx, map[string]interface{}{"pattern": "*.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	files, _ := result.Metadata["files"].([]string)
	joined := strings.Join(files, "\n")
	if !strings.Contains(joined, "session-b-only.txt") {
		t.Fatalf("expected session file in results, got %v", files)
	}
	if strings.Contains(joined, "base-a-only.txt") {
		t.Fatalf("expected global-base file to be excluded, got %v", files)
	}
}

// TestApplyPatchRelativePathLandsInSessionRoot verifies patchApplier threads
// the invocation ctx so relative Add/Update/Delete operations execute inside
// the session-bound workspace instead of the globally registered basePath.
func TestApplyPatchRelativePathLandsInSessionRoot(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()

	tool := NewApplyPatchTool()
	tool.SetBasePath(rootA)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: session-bound.txt",
		"+created in session root",
		"*** End Patch",
	}, "\n")

	ctx := toolctx.WithWorkspaceRoot(context.Background(), rootB)
	result, err := tool.Execute(ctx, map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}

	content, err := os.ReadFile(filepath.Join(rootB, "session-bound.txt"))
	if err != nil {
		t.Fatalf("expected file in session root: %v", err)
	}
	if !strings.Contains(string(content), "created in session root") {
		t.Fatalf("unexpected file content: %q", string(content))
	}
	if _, err := os.Stat(filepath.Join(rootA, "session-bound.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected no file in global basePath, stat err=%v", err)
	}
}

// TestGrepParseOptionsResolvesAgainstSessionRoot covers the grep option
// builder: relative search paths follow the session root from ctx and keep
// the registered basePath fallback when no root is bound.
func TestGrepParseOptionsResolvesAgainstSessionRoot(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()

	tool := NewGrepTool()
	tool.SetBasePath(rootA)

	ctx := toolctx.WithWorkspaceRoot(context.Background(), rootB)
	opts, err := tool.parseOptions(ctx, map[string]interface{}{"pattern": "needle"})
	if err != nil {
		t.Fatalf("parseOptions with session root: %v", err)
	}
	if want := filepath.Clean(rootB); opts.resolvedPath != want {
		t.Fatalf("expected resolvedPath %q, got %q", want, opts.resolvedPath)
	}
	if want := filepath.Clean(rootB); opts.basePath != want {
		t.Fatalf("expected opts.basePath %q, got %q", want, opts.basePath)
	}

	opts, err = tool.parseOptions(context.Background(), map[string]interface{}{"pattern": "needle"})
	if err != nil {
		t.Fatalf("parseOptions without session root: %v", err)
	}
	if want := filepath.Clean(rootA); opts.resolvedPath != want {
		t.Fatalf("expected basePath fallback %q, got %q", want, opts.resolvedPath)
	}
	if want := filepath.Clean(rootA); opts.basePath != want {
		t.Fatalf("expected opts.basePath fallback %q, got %q", want, opts.basePath)
	}
}

// TestPathNotFoundHintAnchorsToSessionRoot verifies the not-found hint for a
// relative tool path points at the session-bound workspace root (where the
// path was actually resolved) instead of the globally registered basePath.
func TestPathNotFoundHintAnchorsToSessionRoot(t *testing.T) {
	globalBase := t.TempDir()
	sessionRoot := t.TempDir()
	// A similarly named sibling makes the hint builder emit candidates, so the
	// assertion can observe which resolution root it used.
	if err := os.WriteFile(filepath.Join(sessionRoot, "notes.txt.bak"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed session root: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(globalBase)

	ctx := toolctx.WithWorkspaceRoot(context.Background(), sessionRoot)
	result, err := tool.Execute(ctx, map[string]interface{}{"file_path": "notes.txt"})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected not-found failure, got success=%v error=%v", result.Success, result.Error)
	}
	message := result.Error.Error()
	if !strings.Contains(message, sessionRoot) {
		t.Fatalf("expected hint anchored to session root %q, got %q", sessionRoot, message)
	}
	if strings.Contains(message, globalBase) {
		t.Fatalf("expected hint to avoid global basePath %q, got %q", globalBase, message)
	}
}

// TestPathKindMismatchHintAnchorsToSessionRoot mirrors the not-found case for a
// path that exists but is a directory (write tool rejects directory targets).
func TestPathKindMismatchHintAnchorsToSessionRoot(t *testing.T) {
	globalBase := t.TempDir()
	sessionRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(sessionRoot, "assets"), 0o755); err != nil {
		t.Fatalf("seed session root dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "assets.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed session root sibling: %v", err)
	}

	tool := NewWriteTool()
	tool.SetBasePath(globalBase)

	ctx := toolctx.WithWorkspaceRoot(context.Background(), sessionRoot)
	result, err := tool.Execute(ctx, map[string]interface{}{"file_path": "assets", "content": "payload"})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected kind-mismatch failure, got success=%v error=%v", result.Success, result.Error)
	}
	message := result.Error.Error()
	if !strings.Contains(message, sessionRoot) {
		t.Fatalf("expected hint anchored to session root %q, got %q", sessionRoot, message)
	}
	if strings.Contains(message, globalBase) {
		t.Fatalf("expected hint to avoid global basePath %q, got %q", globalBase, message)
	}
}

// TestApplyPatchHintAnchorsToSessionRoot covers the patchApplier ctx threading:
// failure hints for relative patch operations must use the session root.
func TestApplyPatchHintAnchorsToSessionRoot(t *testing.T) {
	globalBase := t.TempDir()
	sessionRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionRoot, "notes.txt.bak"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed session root: %v", err)
	}

	tool := NewApplyPatchTool()
	tool.SetBasePath(globalBase)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: notes.txt",
		"*** End Patch",
	}, "\n")

	ctx := toolctx.WithWorkspaceRoot(context.Background(), sessionRoot)
	result, err := tool.Execute(ctx, map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected delete-missing failure, got success=%v error=%v", result.Success, result.Error)
	}
	message := result.Error.Error()
	if !strings.Contains(message, sessionRoot) {
		t.Fatalf("expected hint anchored to session root %q, got %q", sessionRoot, message)
	}
	if strings.Contains(message, globalBase) {
		t.Fatalf("expected hint to avoid global basePath %q, got %q", globalBase, message)
	}
}

// TestFileToolsExecuteRelativePathsInSessionRoot is the end-to-end guard for
// the ctx plumbing: with a registered global basePath and a session root in
// ctx, relative paths for reading and mutating file tools must land in the
// session root, never in the global basePath.
func TestFileToolsExecuteRelativePathsInSessionRoot(t *testing.T) {
	baseRoot := t.TempDir()
	sessionRoot := t.TempDir()
	seed := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s in %s: %v", name, dir, err)
		}
	}
	seed(baseRoot, "marker.txt", "base-marker")
	seed(baseRoot, "base-only.txt", "base-only")
	seed(sessionRoot, "marker.txt", "session-marker")
	seed(sessionRoot, "session-only.txt", "session-only")
	seed(sessionRoot, "multi.txt", "alpha beta")

	ctx := toolctx.WithWorkspaceRoot(context.Background(), sessionRoot)

	assertSessionFile := func(t *testing.T, name, want string) {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(sessionRoot, name))
		if err != nil {
			t.Fatalf("expected %s in session root: %v", name, err)
		}
		if !strings.Contains(string(content), want) {
			t.Fatalf("expected %s to contain %q, got %q", name, want, string(content))
		}
		// marker.txt is seeded in both roots on purpose (the edit case checks the
		// global copy separately), so only assert absence for session-only names.
		if _, err := os.Stat(filepath.Join(baseRoot, name)); name != "marker.txt" && !os.IsNotExist(err) {
			t.Fatalf("expected no %s in global basePath, stat err=%v", name, err)
		}
	}

	t.Run("view", func(t *testing.T) {
		tool := NewViewTool()
		tool.SetBasePath(baseRoot)
		res, err := tool.Execute(ctx, map[string]interface{}{"file_path": "marker.txt"})
		if err != nil || res == nil || !res.Success {
			t.Fatalf("view failed: err=%v res=%v", err, res)
		}
		if content := res.Content; !strings.Contains(content, "session-marker") {
			t.Fatalf("expected session-root content, got %q", content)
		}
	})

	t.Run("ls", func(t *testing.T) {
		tool := NewLsTool()
		tool.SetBasePath(baseRoot)
		res, err := tool.Execute(ctx, map[string]interface{}{"path": "."})
		if err != nil || res == nil || !res.Success {
			t.Fatalf("ls failed: err=%v res=%v", err, res)
		}
		content := res.Content
		if !strings.Contains(content, "session-only.txt") {
			t.Fatalf("expected session-root listing, got %q", content)
		}
		if strings.Contains(content, "base-only.txt") {
			t.Fatalf("expected global basePath listing to be excluded, got %q", content)
		}
	})

	t.Run("write", func(t *testing.T) {
		tool := NewWriteTool()
		tool.SetBasePath(baseRoot)
		res, err := tool.Execute(ctx, map[string]interface{}{"file_path": "written.txt", "content": "written-in-session"})
		if err != nil || res == nil || !res.Success {
			t.Fatalf("write failed: err=%v res=%v", err, res)
		}
		assertSessionFile(t, "written.txt", "written-in-session")
	})

	t.Run("edit", func(t *testing.T) {
		tool := NewEditTool()
		tool.SetBasePath(baseRoot)
		res, err := tool.Execute(ctx, map[string]interface{}{
			"file_path":  "marker.txt",
			"old_string": "session-marker",
			"new_string": "edited-marker",
		})
		if err != nil || res == nil || !res.Success {
			t.Fatalf("edit failed: err=%v res=%v", err, res)
		}
		assertSessionFile(t, "marker.txt", "edited-marker")
		if content, err := os.ReadFile(filepath.Join(baseRoot, "marker.txt")); err != nil || !strings.Contains(string(content), "base-marker") {
			t.Fatalf("global basePath file must stay untouched, content=%q err=%v", string(content), err)
		}
	})

	t.Run("multiedit", func(t *testing.T) {
		tool := NewMultieditTool()
		tool.SetBasePath(baseRoot)
		res, err := tool.Execute(ctx, map[string]interface{}{
			"file_path": "multi.txt",
			"edits": []interface{}{
				map[string]interface{}{"old_string": "alpha", "new_string": "gamma"},
			},
		})
		if err != nil || res == nil || !res.Success {
			t.Fatalf("multiedit failed: err=%v res=%v", err, res)
		}
		assertSessionFile(t, "multi.txt", "gamma beta")
	})

	t.Run("append_write", func(t *testing.T) {
		seed(sessionRoot, "append.txt", "first")
		tool := NewAppendWriteTool()
		tool.SetBasePath(baseRoot)
		res, err := tool.Execute(ctx, map[string]interface{}{
			"file_path":               "append.txt",
			"content":                 "second",
			"ensure_trailing_newline": true,
		})
		if err != nil || res == nil || !res.Success {
			t.Fatalf("append_write failed: err=%v res=%v", err, res)
		}
		assertSessionFile(t, "append.txt", "second")
	})
}
