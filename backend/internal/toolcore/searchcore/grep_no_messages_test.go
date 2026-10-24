package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateGrepIgnoreFilesExplicitFileIsAPromise pins the contract that
// separates an explicit ignore_file from a discovered one: naming a file makes
// its rules part of the query, so an unreadable file must fail rather than
// silently produce a different result set.
func TestValidateGrepIgnoreFilesExplicitFileIsAPromise(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent.ignore")
	readable := filepath.Join(root, "ok.ignore")
	if err := os.WriteFile(readable, []byte("skip.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asDir := filepath.Join(root, "dir.ignore")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("missing fails", func(t *testing.T) {
		err := ValidateGrepIgnoreFiles(GrepOptions{IgnoreFiles: []string{missing}})
		if err == nil {
			t.Fatal("a missing explicit ignore_file must fail")
		}
		if !strings.Contains(err.Error(), "ignore_file") {
			t.Fatalf("the error must name the parameter: %v", err)
		}
	})

	t.Run("missing is skipped with no_messages", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(GrepOptions{
			IgnoreFiles: []string{missing}, NoMessages: true,
		}); err != nil {
			t.Fatalf("no_messages must turn the failure into a skip: %v", err)
		}
	})

	t.Run("directory fails", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(GrepOptions{IgnoreFiles: []string{asDir}}); err == nil {
			t.Fatal("a directory named as ignore_file must fail")
		}
	})

	t.Run("readable file passes", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(GrepOptions{IgnoreFiles: []string{readable}}); err != nil {
			t.Fatalf("a readable ignore_file must pass: %v", err)
		}
	})

	t.Run("no_ignore_files disables the check", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(GrepOptions{
			IgnoreFiles: []string{missing}, NoIgnoreFiles: true,
		}); err != nil {
			t.Fatalf("no_ignore_files discards the list, so nothing can fail: %v", err)
		}
	})

	t.Run("no_ignore disables the check", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(GrepOptions{
			IgnoreFiles: []string{missing}, NoIgnore: true,
		}); err != nil {
			t.Fatalf("no_ignore disables the whole mechanism: %v", err)
		}
	})
}

// TestGrepDoesNotFailOnDiscoveredIgnoreFiles is the other half: the files found
// by convention stay optional. A search must not break because a repository has
// no .gitignore.
func TestGrepDoesNotFailOnDiscoveredIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10})
	if err != nil {
		t.Fatalf("a tree without ignore files must search normally: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("expected one match, got %+v", result.Matches)
	}
}
