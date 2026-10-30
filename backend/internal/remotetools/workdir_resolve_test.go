package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveFromRequiresDirectoryWorkdir pins the cross-tool contract that a
// per-call workdir must be a directory.
//
// Before this check the path tools (ls, glob, grep) accepted a file as the base
// and happily resolved children beneath it, while shell and job_start rejected
// the same value. One parameter has to mean one thing, so ResolveFrom is where
// that agreement is enforced and therefore where it is tested.
func TestResolveFromRequiresDirectoryWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	workspace, err := NewWorkspace(root, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// A directory base keeps working, including the empty-raw case that returns
	// the base itself.
	if got, err := workspace.ResolveFrom("sub", "nested.txt"); err != nil {
		t.Fatalf("directory workdir must resolve: %v", err)
	} else if !strings.HasSuffix(filepath.ToSlash(got), "sub/nested.txt") {
		t.Fatalf("ResolveFrom = %q, want a path under sub/", got)
	}
	if got, err := workspace.ResolveFrom("sub", ""); err != nil {
		t.Fatalf("empty raw path must resolve to the base: %v", err)
	} else if !strings.HasSuffix(filepath.ToSlash(got), "/sub") {
		t.Fatalf("ResolveFrom(empty) = %q, want the base directory", got)
	}

	// A file base must be rejected, by relative and by absolute spelling, so the
	// message cannot depend on how the caller wrote it.
	for _, base := range []string{"sub/leaf.txt", filepath.Join(root, "sub", "leaf.txt")} {
		if _, err := workspace.ResolveFrom(base, "nested.txt"); err == nil {
			t.Fatalf("file workdir %q must be rejected", base)
		} else if !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("file workdir %q error = %v, want a 'not a directory' message", base, err)
		}
	}
}

// TestCallWorkdirAnchorsHintBase checks that the not-found hint is built from the
// directory the call actually used, not from the workspace root. Naming the wrong
// base sends the caller to fix a path that was never the problem.
func TestCallWorkdirAnchorsHintBase(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The candidate lives only under sub/. A hint that mentions it proves the
	// builder scanned the per-call workdir, which is the whole point of the fix;
	// scanning the workspace root would find nothing because root has no such
	// entry.
	if err := os.WriteFile(filepath.Join(root, "sub", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	workspace, err := NewWorkspace(root, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// PathHint on the runtime is what the tools call; assert through it rather
	// than re-deriving the base here.
	runtime := &remoteRuntime{ctx: WithCallWorkdir(context.Background(), "sub"), workspace: workspace}
	hint := runtime.PathHint("note.txt")
	if hint == "" {
		t.Fatal("PathHint returned nothing although a similar file exists in the workdir")
	}
	if !strings.Contains(hint, "notes.txt") {
		t.Fatalf("hint = %q, want it to surface the candidate found under the workdir", hint)
	}

	// The same query without a workdir resolves against the workspace root, where
	// the candidate does not exist. This documents that the base really changed
	// the scan rather than both paths happening to find the same file.
	plain := &remoteRuntime{ctx: context.Background(), workspace: workspace}
	if got := plain.PathHint("note.txt"); strings.Contains(got, "notes.txt") {
		t.Fatalf("hint without workdir = %q, must not see a file outside the scanned base", got)
	}
}
