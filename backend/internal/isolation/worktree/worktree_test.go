package worktree

import (
	"context"
	"errors"
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

// H14: an apply must refuse to overwrite local main-tree changes instead of
// silently losing them (and must offer an actionable next_action).
func TestApplyRefusesMainTreeConflicts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-conflict",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Child edits a tracked file and adds a new one.
	if err := os.WriteFile(filepath.Join(handle.Path, "README.md"), []byte("child-version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handle.Path, "child-only.txt"), []byte("child-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Main tree has an uncommitted local edit to the same tracked file.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("main-local-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handle.ApplyWithReport(ctx, ApplyOptions{})
	var conflictErr *ApplyConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ApplyConflictError, got report=%+v err=%v", report, err)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].Path != "README.md" {
		t.Fatalf("expected README.md conflict, got %+v", report.Conflicts)
	}
	if report.Applied {
		t.Fatal("refused apply must not report Applied")
	}
	if !strings.Contains(conflictErr.NextAction, "force=true") || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("expected actionable error, got next_action=%q err=%q", conflictErr.NextAction, err.Error())
	}
	// The local main-tree edit survives the refusal.
	data, readErr := os.ReadFile(filepath.Join(repo, "README.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "main-local-edit\n" {
		t.Fatalf("main tree edit was lost: %q", data)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "child-only.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("refused apply must not land any path, stat err=%v", statErr)
	}

	// force=true overwrites deliberately.
	forced, err := handle.ApplyWithReport(ctx, ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("forced apply: %v", err)
	}
	if !forced.Applied || !forced.Forced {
		t.Fatalf("expected applied+forced report, got %+v", forced)
	}
	data, readErr = os.ReadFile(filepath.Join(repo, "README.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "child-version\n" {
		t.Fatalf("forced apply content=%q want child-version", data)
	}
	if err := handle.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// runGitTest runs git with a deterministic identity for test commits.
func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// seedTrackedFile commits one extra tracked file into the test repo.
func seedTrackedFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", name)
	runGitTest(t, repo, "commit", "-m", "seed "+name)
}

// localStatus returns the trimmed porcelain status of one path.
func localStatus(t *testing.T, repo, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--", path).Output()
	if err != nil {
		t.Fatalf("git status %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// Incident regression (2026-09-27): an apply without a paths filter ran
// `git checkout <branch> -- .`, which rewrote every tracked path in the main
// tree and silently wiped uncommitted WIP the worktree never touched. The
// checkout must stay inside the branch's apply set.
func TestApplyWithoutPathsLeavesUnrelatedMainTreeChangesAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	seedTrackedFile(t, repo, "notes.txt", "seed-notes\n")
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-out-of-scope",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Remove(context.Background()) }()

	// The child changes README.md only.
	if err := os.WriteFile(filepath.Join(handle.Path, "README.md"), []byte("child-version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The main tree carries unrelated uncommitted WIP on notes.txt.
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("MAIN-WIP\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handle.ApplyWithReport(ctx, ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyWithReport: %v", err)
	}
	if !report.Applied {
		t.Fatalf("expected applied report, got %+v", report)
	}
	if len(report.CandidatePaths) != 1 || report.CandidatePaths[0] != "README.md" {
		t.Fatalf("expected only README.md in the apply set, got %+v", report.CandidatePaths)
	}
	readme, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(readme) != "child-version\n" {
		t.Fatalf("README.md content=%q want child-version", readme)
	}
	notes, err := os.ReadFile(filepath.Join(repo, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(notes) != "MAIN-WIP\n" {
		t.Fatalf("unrelated main-tree WIP was overwritten: %q", notes)
	}
	if got := localStatus(t, repo, "notes.txt"); got != "M notes.txt" {
		t.Fatalf("notes.txt must stay an unstaged local modification, got %q", got)
	}
}

// A clean worktree has nothing to land: an apply must not run any main-tree
// checkout. (`checkout <branch> -- .` of HEAD content previously reset the
// whole tree even when the branch had no changes.)
func TestApplyCleanWorktreeDoesNotTouchMainTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	seedTrackedFile(t, repo, "notes.txt", "seed-notes\n")
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-clean",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Remove(context.Background()) }()

	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("MAIN-WIP\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handle.ApplyWithReport(ctx, ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyWithReport: %v", err)
	}
	if report.Applied {
		t.Fatalf("a clean worktree must not report Applied: %+v", report)
	}
	if len(report.CandidatePaths) != 0 {
		t.Fatalf("expected an empty apply set, got %+v", report.CandidatePaths)
	}
	notes, err := os.ReadFile(filepath.Join(repo, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(notes) != "MAIN-WIP\n" {
		t.Fatalf("main-tree WIP was overwritten: %q", notes)
	}
	if got := localStatus(t, repo, "notes.txt"); got != "M notes.txt" {
		t.Fatalf("notes.txt must stay an unstaged local modification, got %q", got)
	}
}

// A branch-side deletion cannot be expressed by a path checkout; it must be
// reported (deferred_deletions) instead of silently ignored or failing the
// whole apply.
func TestApplyReportsDeferredBranchDeletions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	seedTrackedFile(t, repo, "notes.txt", "seed-notes\n")
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-delete",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Remove(context.Background()) }()

	if err := os.Remove(filepath.Join(handle.Path, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	report, err := handle.ApplyWithReport(ctx, ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyWithReport: %v", err)
	}
	if report.Applied {
		t.Fatalf("nothing landable, got %+v", report)
	}
	if len(report.DeferredDeletions) != 1 || report.DeferredDeletions[0] != "notes.txt" {
		t.Fatalf("expected notes.txt to be reported as a deferred deletion, got %+v", report.DeferredDeletions)
	}
	notes, err := os.ReadFile(filepath.Join(repo, "notes.txt"))
	if err != nil {
		t.Fatalf("deferred deletion must leave the file in place: %v", err)
	}
	if string(notes) != "seed-notes\n" {
		t.Fatalf("notes.txt content=%q want seed-notes", notes)
	}
}

// A forced apply overwrites tracked main-tree changes; it must keep a
// recoverable snapshot (dangling stash commit) so force is never final.
func TestForcedApplySnapshotsOverwrittenMainTreeChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-force-backup",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Remove(context.Background()) }()

	if err := os.WriteFile(filepath.Join(handle.Path, "README.md"), []byte("child-version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("main-local-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handle.ApplyWithReport(ctx, ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("forced apply: %v", err)
	}
	if !report.Applied || !report.Forced {
		t.Fatalf("expected applied+forced report, got %+v", report)
	}
	if report.BackupCommit == "" {
		t.Fatalf("forced apply must keep a backup commit, got %+v", report)
	}
	// The snapshot holds the overwritten main-tree version.
	out, err := exec.Command("git", "-C", repo, "show", report.BackupCommit+":README.md").Output()
	if err != nil {
		t.Fatalf("read backup commit: %v", err)
	}
	if string(out) != "main-local-edit\n" {
		t.Fatalf("backup content=%q want main-local-edit", out)
	}
	// stash create is side-effect free: no stash entry appears.
	listOut, err := exec.Command("git", "-C", repo, "stash", "list").Output()
	if err != nil {
		t.Fatalf("git stash list: %v", err)
	}
	if strings.TrimSpace(string(listOut)) != "" {
		t.Fatalf("stash create must not add a stash entry, got %q", listOut)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "child-version\n" {
		t.Fatalf("forced apply content=%q want child-version", data)
	}
}

// An untracked main-tree file cannot be captured by `git stash create`, so a
// forced apply that would overwrite one must refuse instead of destroying it.
func TestForcedApplyRefusesUntrackedConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-force-untracked",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Remove(context.Background()) }()

	if err := os.WriteFile(filepath.Join(handle.Path, "child-only.txt"), []byte("child-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "child-only.txt"), []byte("main-untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handle.ApplyWithReport(ctx, ApplyOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "untracked") || !strings.Contains(err.Error(), "cannot be snapshotted") {
		t.Fatalf("expected untracked-conflict refusal, got report=%+v err=%v", report, err)
	}
	if report.Applied {
		t.Fatalf("refused forced apply must not report Applied: %+v", report)
	}
	data, readErr := os.ReadFile(filepath.Join(repo, "child-only.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "main-untracked\n" {
		t.Fatalf("untracked main-tree file was overwritten: %q", data)
	}
}

// H14: a paths-filtered apply reports the branch changes it left behind.
func TestApplyReportsOutOfScopePaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	ctx := context.Background()
	handle, err := Create(ctx, Options{
		RepoRoot:  repo,
		SessionID: "child-scope",
		BaseDir:   filepath.Join(repo, ".aicli", "agent-worktrees"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(handle.Path, "README.md"), []byte("child-readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handle.Path, "child-only.txt"), []byte("child-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handle.ApplyWithReport(ctx, ApplyOptions{Paths: []string{"README.md"}})
	if err != nil {
		t.Fatalf("ApplyWithReport: %v", err)
	}
	if !report.Applied {
		t.Fatalf("expected applied report, got %+v", report)
	}
	if len(report.SkippedPaths) != 1 || report.SkippedPaths[0] != "child-only.txt" {
		t.Fatalf("expected child-only.txt to be reported out of scope, got %+v", report.SkippedPaths)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "child-only.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("out-of-scope path must not be applied, stat err=%v", statErr)
	}
	if err := handle.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}
