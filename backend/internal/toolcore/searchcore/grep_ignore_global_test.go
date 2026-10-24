package searchcore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGrepGlobalIgnoreRespectsHomeEnv pins the global-ignore lookup: the
// HOME/USERPROFILE environment (what tests pin with t.Setenv) must point the
// global candidates, so a pinned temp home's rules apply.
func TestGrepGlobalIgnoreRespectsHomeEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "global.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("USERPROFILE", tempHome)
	if err := os.MkdirAll(filepath.Join(tempHome, ".config", "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempHome, ".config", "git", "ignore"), []byte("global.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	assertFileList(t, grepFilterMatches(t, root, GrepOptions{Pattern: "needle"}),
		[]string{"keep.txt"})
	assertFileList(t, grepFilterMatches(t, root, GrepOptions{Pattern: "needle", NoIgnoreGlobal: true}),
		[]string{"global.txt", "keep.txt"})
}
