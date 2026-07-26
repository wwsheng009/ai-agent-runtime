package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", ModeNone, false},
		{"none", ModeNone, false},
		{"NONE", ModeNone, false},
		{"worktree", ModeWorktree, false},
		{" WorkTree ", ModeWorktree, false},
		{"sandbox", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeMode(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestCreateRemoveApplyRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	ctx := context.Background()

	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-1",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if handle == nil || handle.Path == "" {
		t.Fatal("expected non-nil handle with path")
	}
	if _, err := os.Stat(handle.Path); err != nil {
		t.Fatalf("worktree path missing: %v", err)
	}
	// Must not be the main repo root.
	if filepath.Clean(handle.Path) == filepath.Clean(repo) {
		t.Fatal("worktree path must not equal main repo root")
	}

	// Mutate only inside the worktree.
	isolatedFile := filepath.Join(handle.Path, "isolated.txt")
	if err := os.WriteFile(isolatedFile, []byte("from-worktree\n"), 0o644); err != nil {
		t.Fatalf("write isolated: %v", err)
	}
	mainCopy := filepath.Join(repo, "isolated.txt")
	if _, err := os.Stat(mainCopy); !os.IsNotExist(err) {
		t.Fatalf("main tree should not have isolated.txt yet, err=%v", err)
	}

	if err := handle.Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(mainCopy)
	if err != nil {
		t.Fatalf("read applied file: %v", err)
	}
	if string(data) != "from-worktree\n" {
		t.Fatalf("unexpected applied content: %q", data)
	}

	if err := handle.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(handle.Path); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path removed, err=%v", err)
	}
}

func TestCreateFailsOutsideGitRepoWithoutFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	_, err := Create(context.Background(), Options{
		RepoRoot:  dir,
		SessionID: "x",
	})
	if err == nil {
		t.Fatal("expected error outside git repo")
	}
	if !strings.Contains(err.Error(), "no main-tree fallback") && !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("expected clear isolation error, got: %v", err)
	}
}

func TestResolveRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	nested := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := ResolveRepoRoot(context.Background(), nested)
	if err != nil {
		t.Fatalf("ResolveRepoRoot: %v", err)
	}
	if filepath.Clean(root) != filepath.Clean(repo) {
		t.Fatalf("root=%q want %q", root, repo)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	// Avoid default branch ambiguity across git versions.
	run("checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "seed")
	return dir
}
