package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepCountMatchesCountsOccurrences pins the difference between the two
// counting modes: `count` counts matching lines, `count_matches` counts
// occurrences, so one line holding the pattern twice contributes 2.
func TestGrepCountMatchesCountsOccurrences(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo foo\nfoo bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, Mode: GrepModeCount, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(lines), []string{"a.txt:2"})

	occurrences, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, Mode: GrepModeCount,
		CountMatches: true, OnlyMatching: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(occurrences), []string{"a.txt:3"})
}

// TestGrepGlobCaseInsensitiveOnlyAffectsGlobs pins the scope: the flag changes
// how include/exclude/glob patterns match, never how the search pattern itself
// is compared.
func TestGrepGlobCaseInsensitiveOnlyAffectsGlobs(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"UP.GO":    "needle up\n",
		"low.go":   "needle low\n",
		"other.md": "needle md\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("glob is case-sensitive by default", func(t *testing.T) {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Glob: "*.go", Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFileList(t, grepFileNames(result), []string{"low.go"})
	})

	t.Run("glob matches case-insensitively when asked", func(t *testing.T) {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Glob: "*.go",
			GlobCaseInsensitive: true, Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Both files match "*.go" once case is folded; that is the point.
		assertFileList(t, grepFileNames(result), []string{"UP.GO", "low.go"})
	})

	t.Run("the search pattern keeps its own case rules", func(t *testing.T) {
		// The flag must not turn the pattern comparison case-insensitive.
		if err := os.WriteFile(filepath.Join(root, "LOWER.GO"), []byte("NEEDLE upper\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Glob: "*.go",
			GlobCaseInsensitive: true, Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range result.Matches {
			if filepath.Base(match.File) == "LOWER.GO" {
				t.Fatalf("glob case-insensitivity must not affect the search pattern: %+v", result.Matches)
			}
		}
	})
}

// TestGrepIgnoreFileCaseInsensitiveCoversDiscoveredFiles pins the scope from the
// other side. rg's own help text scopes the flag to ".gitignore, .ignore, etc.",
// and probing rg shows an explicit --ignore-file stays case-sensitive. The walk
// must behave the same way.
func TestGrepIgnoreFileCaseInsensitiveCoversDiscoveredFiles(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"keep.txt":   "needle keep\n",
		"UPSKIP.TXT": "needle up\n",
		".gitignore": "upskip.txt\n",
		"rules.ign":  "OTHER.TXT\n",
		"other.txt":  "needle other\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("discovered rule stays case-sensitive by default", func(t *testing.T) {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFileList(t, grepFileNames(result), []string{"UPSKIP.TXT", "keep.txt", "other.txt"})
	})

	t.Run("discovered rule folds case when asked", func(t *testing.T) {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Limit: 10,
			IgnoreFileCaseInsensitive: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFileList(t, grepFileNames(result), []string{"keep.txt", "other.txt"})
	})

	t.Run("explicit ignore_file stays case-sensitive", func(t *testing.T) {
		// The rule says OTHER.TXT; the flag must not make it match other.txt.
		result, err := Grep(context.Background(), GrepOptions{
			// The glob keeps the .gitignore-derived UPSKIP.TXT out of this
			// sub-case so the only variable is the explicit rule.
			Pattern: "needle", Path: root, Glob: "*.txt", Limit: 10,
			IgnoreFiles:               []string{filepath.Join(root, "rules.ign")},
			IgnoreFileCaseInsensitive: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		// keep.txt survives because rules.ign only names OTHER.TXT, and the
		// case-insensitive flag must not make that rule match other.txt.
		assertFileList(t, grepFileNames(result), []string{"keep.txt", "other.txt"})
	})
}
