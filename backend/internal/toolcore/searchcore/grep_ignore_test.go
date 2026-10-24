package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func grepIgnoreFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".gitignore":      "ignored.txt\nbuild\nnested/*.log\n!nested/keep.log\n",
		"ignored.txt":     "needle ignored\n",
		"keep.txt":        "needle keep\n",
		"build/x.txt":     "needle build\n",
		"nested/a.log":    "needle log\n",
		"nested/keep.log": "needle keeplog\n",
		"nested/c.txt":    "needle nested\n",
		"sub/.gitignore":  "local.txt\n",
		"sub/local.txt":   "needle sub ignored\n",
		"sub/other.txt":   "needle sub kept\n",
		"custom.ignore":   "keep.txt\n",
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGrepIgnoreFilePolicy pins the ignore contract copied from the local tool:
// `.gitignore` in the root and in subdirectories, `!` negation, per-directory
// scoping, and the no_ignore switches.
func TestGrepIgnoreFilePolicy(t *testing.T) {
	root := grepIgnoreFixture(t)

	defaultRun := grepFilterMatches(t, root, GrepOptions{Pattern: "needle"})
	assertFileList(t, defaultRun, []string{
		"keep.txt", "nested/c.txt", "nested/keep.log", "sub/other.txt",
	})

	all := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", NoIgnore: true})
	assertFileList(t, all, []string{
		"build/x.txt", "ignored.txt", "keep.txt", "nested/a.log", "nested/c.txt",
		"nested/keep.log", "sub/local.txt", "sub/other.txt",
	})

	noVCS := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", NoIgnoreVCS: true})
	assertFileList(t, noVCS, []string{
		"build/x.txt", "ignored.txt", "keep.txt", "nested/a.log", "nested/c.txt",
		"nested/keep.log", "sub/local.txt", "sub/other.txt",
	})
}

func TestGrepIgnoreExplicitFile(t *testing.T) {
	root := grepIgnoreFixture(t)

	withFile := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", IgnoreFiles: []string{filepath.Join(root, "custom.ignore")}})
	assertFileList(t, withFile, []string{"nested/c.txt", "nested/keep.log", "sub/other.txt"})

	// no_ignore_files disables the explicit file while leaving .gitignore alive.
	disabled := grepFilterMatches(t, root, GrepOptions{
		Pattern: "needle", IgnoreFiles: []string{filepath.Join(root, "custom.ignore")}, NoIgnoreFiles: true,
	})
	assertFileList(t, disabled, []string{"keep.txt", "nested/c.txt", "nested/keep.log", "sub/other.txt"})
}

func TestGrepIgnoreAncestorFiles(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, ".gitignore"), []byte("parentonly.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"parentonly.txt": "needle parent\n",
		"visible.txt":    "needle visible\n",
	} {
		if err := os.WriteFile(filepath.Join(inner, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	assertFileList(t, grepFilterMatches(t, inner, GrepOptions{Pattern: "needle"}), []string{"visible.txt"})
	assertFileList(t, grepFilterMatches(t, inner, GrepOptions{Pattern: "needle", NoIgnoreParent: true}),
		[]string{"parentonly.txt", "visible.txt"})
}

// TestGrepIgnoreSingleFileRoot documents the inherited local behaviour: even an
// explicitly named file is subject to the ignore rules of its directory.
func TestGrepIgnoreSingleFileRoot(t *testing.T) {
	root := grepIgnoreFixture(t)
	target := filepath.Join(root, "ignored.txt")

	result, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: target})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("an ignored explicit file stays ignored, got %+v", result.Matches)
	}

	explicit, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: target, NoIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.Matches) != 1 || filepath.Base(explicit.Matches[0].File) != "ignored.txt" {
		t.Fatalf("no_ignore must reconsider the explicit file, got %+v", explicit.Matches)
	}
}
