package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCallWorkdirRoundTrip covers the context carrier that lets a single
// wrapper add per-call directory support without threading a parameter through
// every handler.
func TestCallWorkdirRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := CallWorkdir(ctx); got != "" {
		t.Fatalf("unset workdir should read back empty, got %q", got)
	}
	ctx = WithCallWorkdir(ctx, "sub")
	if got := CallWorkdir(ctx); got != "sub" {
		t.Fatalf("CallWorkdir = %q, want %q", got, "sub")
	}
	// An empty value must be a no-op so callers can pass the extracted
	// parameter unconditionally.
	same := WithCallWorkdir(ctx, "")
	if got := CallWorkdir(same); got != "sub" {
		t.Fatalf("empty value must not clear the existing workdir, got %q", got)
	}
	// The child context must not leak back into the parent: nothing is sticky.
	if got := CallWorkdir(context.Background()); got != "" {
		t.Fatalf("workdir leaked into an unrelated context: %q", got)
	}
}

func pathSchema(props ...string) map[string]interface{} {
	properties := map[string]interface{}{}
	for _, p := range props {
		properties[p] = map[string]interface{}{"type": "string"}
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
}

// TestDeclaresPathParam pins down which tools receive the injected workdir.
// Over-injecting advertises a parameter on tools that cannot honour it, which is
// exactly the failure mode this whole change is meant to remove.
func TestDeclaresPathParam(t *testing.T) {
	yes := []string{"path", "paths", "files", "file_path", "source", "destination", "patch"}
	for _, key := range yes {
		if !declaresPathParam(pathSchema(key)) {
			t.Fatalf("%q should count as a path parameter", key)
		}
	}
	for _, key := range []string{"pattern", "query", "job_id", "limit"} {
		if declaresPathParam(pathSchema(key)) {
			t.Fatalf("%q must not count as a path parameter", key)
		}
	}
	if declaresPathParam(nil) {
		t.Fatal("nil schema must report no path parameter")
	}
}

// TestWithWorkdirProperty checks the schema fragment is added, is idempotent,
// and never mutates the caller's map. Tool schemas are built once and shared, so
// an in-place mutation would leak the parameter across surfaces.
func TestWithWorkdirProperty(t *testing.T) {
	original := pathSchema("file_path")
	got := withWorkdirProperty(original)

	props, _ := got["properties"].(map[string]interface{})
	if _, ok := props["workdir"]; !ok {
		t.Fatal("workdir missing from the wrapped schema")
	}
	if _, ok := props["file_path"]; !ok {
		t.Fatal("the original parameter must survive wrapping")
	}
	// The source map must be untouched.
	originalProps, _ := original["properties"].(map[string]interface{})
	if _, leaked := originalProps["workdir"]; leaked {
		t.Fatal("workdir leaked into the source schema")
	}
	// Re-wrapping must not stack a second copy.
	again := withWorkdirProperty(got)
	againProps, _ := again["properties"].(map[string]interface{})
	if len(againProps) != len(props) {
		t.Fatalf("re-wrapping changed the property count: %d -> %d", len(props), len(againProps))
	}

	description, _ := props["workdir"].(map[string]interface{})["description"].(string)
	for _, want := range []string{"not remembered", "must already exist", "workspace root"} {
		if !strings.Contains(description, want) {
			t.Fatalf("workdir description should mention %q, got %q", want, description)
		}
	}
}

func newWorkspaceFixture(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	workspace, err := NewWorkspace(root, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return workspace, root
}

// TestResolveFromAnchorsRelative is the core behaviour of O4: a relative path is
// anchored at the per-call workdir instead of the workspace root.
func TestResolveFromAnchorsRelative(t *testing.T) {
	workspace, root := newWorkspaceFixture(t)

	got, err := workspace.ResolveFrom("sub", "x.txt")
	if err != nil {
		t.Fatalf("ResolveFrom: %v", err)
	}
	want := filepath.Join(root, "sub", "x.txt")
	if got != want {
		t.Fatalf("ResolveFrom(sub, x.txt) = %q, want %q", got, want)
	}

	// With no base the historical behaviour must hold exactly.
	plain, err := workspace.ResolveFrom("", "x.txt")
	if err != nil {
		t.Fatalf("ResolveFrom with empty base: %v", err)
	}
	if plain != filepath.Join(root, "x.txt") {
		t.Fatalf("empty base should resolve against the workspace root, got %q", plain)
	}

	// An empty raw path collapses to the base itself.
	baseOnly, err := workspace.ResolveFrom("sub", "")
	if err != nil {
		t.Fatalf("ResolveFrom with empty raw: %v", err)
	}
	if baseOnly != filepath.Join(root, "sub") {
		t.Fatalf("empty raw should resolve to the base, got %q", baseOnly)
	}
}

// TestResolveFromAbsoluteIgnoresBase keeps the documented contract: workdir only
// moves relative anchors, never absolute ones.
func TestResolveFromAbsoluteIgnoresBase(t *testing.T) {
	workspace, root := newWorkspaceFixture(t)

	absolute := filepath.Join(root, "sub", "y.txt")
	got, err := workspace.ResolveFrom(".", absolute)
	if err != nil {
		t.Fatalf("ResolveFrom: %v", err)
	}
	if got != absolute {
		t.Fatalf("absolute path must bypass the base, got %q want %q", got, absolute)
	}
}

// TestResolveFromRejectsEscapingBase proves the new entry point did not open a
// second, weaker path into the jail: the standard lexical and symlink checks
// still run.
func TestResolveFromRejectsEscapingBase(t *testing.T) {
	workspace, root := newWorkspaceFixture(t)

	outside := filepath.Join(filepath.Dir(root), "outside")
	if _, err := workspace.ResolveFrom(outside, "x.txt"); err == nil {
		t.Fatal("a base outside the allowed roots must be rejected")
	}
	if _, err := workspace.ResolveFrom("sub", "../../escape.txt"); err == nil {
		t.Fatal("a traversal that escapes the workspace must be rejected")
	}
}

// TestEnvdDirParam pins the cross-backend alias contract: workdir is canonical,
// cwd remains accepted, and a disagreement is an error rather than a silent pick.
func TestEnvdDirParam(t *testing.T) {
	dir, conflict := envdDirParam(map[string]interface{}{"workdir": "a"})
	if dir != "a" || conflict != "" {
		t.Fatalf("workdir should win: dir=%q conflict=%q", dir, conflict)
	}

	dir, conflict = envdDirParam(map[string]interface{}{"cwd": "b"})
	if dir != "b" || conflict != "" {
		t.Fatalf("cwd alias should still work: dir=%q conflict=%q", dir, conflict)
	}

	dir, conflict = envdDirParam(map[string]interface{}{"workdir": "a", "cwd": "a"})
	if dir != "a" || conflict != "" {
		t.Fatalf("identical values should not conflict: dir=%q conflict=%q", dir, conflict)
	}

	_, conflict = envdDirParam(map[string]interface{}{"workdir": "a", "cwd": "b"})
	if conflict == "" {
		t.Fatal("disagreeing workdir and cwd must report a conflict")
	}

	dir, conflict = envdDirParam(map[string]interface{}{})
	if dir != "" || conflict != "" {
		t.Fatalf("no directory requested: dir=%q conflict=%q", dir, conflict)
	}
}
