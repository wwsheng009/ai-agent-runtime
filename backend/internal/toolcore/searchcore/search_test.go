package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func testTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n// TODO: fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.go"), []byte("package b\n// TODO: other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGlobMatchesNestedAndBaseName(t *testing.T) {
	root := testTree(t)
	result, err := Glob(context.Background(), root, "*.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("matches = %v", result.Matches)
	}
	result, err = Glob(context.Background(), root, "sub/*.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0]) != "b.go" {
		t.Fatalf("matches = %v", result.Matches)
	}
}

func TestTraversalSkipsDiscoveredSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.go")
	if err := os.WriteFile(target, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("needle outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}

	globResult, err := Glob(context.Background(), root, "**/*.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(globResult.Matches) != 1 || filepath.Base(globResult.Matches[0]) != "target.go" {
		t.Fatalf("glob followed a symlink: %v", globResult.Matches)
	}

	grepResult, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    root,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(grepResult.Matches) != 1 || filepath.Base(grepResult.Matches[0].File) != "target.go" {
		t.Fatalf("grep followed a symlink: %+v", grepResult.Matches)
	}
}

func TestGrepRespectsLimitAndGlob(t *testing.T) {
	root := testTree(t)
	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "TODO",
		Path:    root,
		Glob:    "*.go",
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || !result.Truncated {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestGrepSingleFile(t *testing.T) {
	root := testTree(t)
	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "TODO",
		Path:    filepath.Join(root, "a.go"),
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SingleFile || len(result.Matches) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}
