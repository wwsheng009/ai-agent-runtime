package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// reconcileHarness creates a temp repo plus a worktree base dir and returns
// them with a ready-to-use worktree handle factory.
type reconcileHarness struct {
	repo    string
	baseDir string
}

func newReconcileHarness(t *testing.T) reconcileHarness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	return reconcileHarness{repo: repo, baseDir: filepath.Join(repo, ".aicli", "agent-worktrees")}
}

func (h reconcileHarness) createWorktree(t *testing.T, sessionID string) *Handle {
	t.Helper()
	handle, err := Create(context.Background(), Options{
		RepoRoot:  h.repo,
		SessionID: sessionID,
		BaseDir:   h.baseDir,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", sessionID, err)
	}
	return handle
}

func (h reconcileHarness) reconcile(t *testing.T, opts ReconcileOptions) ReconcileReport {
	t.Helper()
	opts.RepoRoot = h.repo
	if opts.BaseDir == "" {
		opts.BaseDir = h.baseDir
	}
	report, err := ReconcileWorktrees(context.Background(), opts)
	if err != nil {
		t.Fatalf("ReconcileWorktrees: %v", err)
	}
	return report
}

func hasFinding(report ReconcileReport, code, path string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code && filepath.Clean(finding.Path) == filepath.Clean(path) {
			return true
		}
	}
	return false
}

// isGitRegistered compares normalized paths: git prints forward slashes on
// Windows while the reconciler (and the caller) speak filepath.Clean paths.
func isGitRegistered(t *testing.T, repo, path string) bool {
	t.Helper()
	entries, err := listGitWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatalf("listGitWorktrees: %v", err)
	}
	want := filepath.Clean(path)
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == want {
			return true
		}
	}
	return false
}

// An unregistered directory with no git registration is the crashed-child
// leak H11/H15 describes: observe reports it, enforce reclaims it.
func TestReconcileWorktreesReclaimsOrphanDir(t *testing.T) {
	harness := newReconcileHarness(t)
	orphan := filepath.Join(harness.baseDir, "resume-redraw-fix")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	observed := harness.reconcile(t, ReconcileOptions{Mode: ReconcileModeObserve})
	if observed.OrphanDirs != 1 || !hasFinding(observed, FindingWorktreeOrphanDir, orphan) {
		t.Fatalf("observe report = %+v, want one orphan dir finding", observed)
	}
	if observed.ReclaimedDirs != 0 {
		t.Fatalf("observe must not reclaim, got %d", observed.ReclaimedDirs)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("observe removed the orphan dir: %v", err)
	}

	enforced := harness.reconcile(t, ReconcileOptions{Mode: ReconcileModeEnforce})
	if enforced.ReclaimedDirs != 1 {
		t.Fatalf("enforce reclaimed %d dirs, want 1 (%+v)", enforced.ReclaimedDirs, enforced)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan dir still present after enforce, err=%v", err)
	}
}

// A worktree the registry still owns must survive an enforce pass even when
// git has already forgotten it.
func TestReconcileWorktreesKeepsRegisteredWorktree(t *testing.T) {
	harness := newReconcileHarness(t)
	handle := harness.createWorktree(t, "child-registered")

	report := harness.reconcile(t, ReconcileOptions{
		Mode:            ReconcileModeEnforce,
		RegisteredPaths: []string{handle.Path},
	})
	if report.ReclaimedDirs != 0 || report.ReclaimedGit != 0 {
		t.Fatalf("registered worktree must never be reclaimed: %+v", report)
	}
	if _, err := os.Stat(handle.Path); err != nil {
		t.Fatalf("registered worktree removed: %v", err)
	}
}

// A git-registered worktree with no registry row looks exactly like an
// apply/discard in flight, so it is reported but never reclaimed.
func TestReconcileWorktreesKeepsGitRegisteredUnmanaged(t *testing.T) {
	harness := newReconcileHarness(t)
	handle := harness.createWorktree(t, "child-unmanaged")

	report := harness.reconcile(t, ReconcileOptions{Mode: ReconcileModeEnforce})
	if report.Unmanaged != 1 || !hasFinding(report, FindingWorktreeUnmanagedRegistration, handle.Path) {
		t.Fatalf("want one unmanaged registration finding: %+v", report)
	}
	if report.ReclaimedDirs != 0 {
		t.Fatalf("unmanaged registration must not be reclaimed: %+v", report)
	}
	if _, err := os.Stat(handle.Path); err != nil {
		t.Fatalf("unmanaged worktree removed: %v", err)
	}
}

// A git registration whose directory vanished is provable drift: enforce
// prunes it from the registration table.
func TestReconcileWorktreesPrunesStaleRegistration(t *testing.T) {
	harness := newReconcileHarness(t)
	handle := harness.createWorktree(t, "child-stale")
	if err := os.RemoveAll(handle.Path); err != nil {
		t.Fatal(err)
	}
	if !isGitRegistered(t, harness.repo, handle.Path) {
		t.Fatalf("precondition: %s should still be git-registered", handle.Path)
	}

	observed := harness.reconcile(t, ReconcileOptions{Mode: ReconcileModeObserve})
	if observed.StaleGit != 1 {
		t.Fatalf("observe stale registrations = %d, want 1 (%+v)", observed.StaleGit, observed)
	}
	if !isGitRegistered(t, harness.repo, handle.Path) {
		t.Fatal("observe must not prune the registration")
	}

	enforced := harness.reconcile(t, ReconcileOptions{Mode: ReconcileModeEnforce})
	if enforced.ReclaimedGit != 1 {
		t.Fatalf("enforce reclaimed %d registrations, want 1 (%+v)", enforced.ReclaimedGit, enforced)
	}
	if isGitRegistered(t, harness.repo, handle.Path) {
		t.Fatalf("stale registration for %s still present after enforce", handle.Path)
	}
}

// A missing base dir is "nothing to reconcile", not an error.
func TestReconcileWorktreesMissingBaseDirIsEmpty(t *testing.T) {
	harness := newReconcileHarness(t)
	report := harness.reconcile(t, ReconcileOptions{BaseDir: filepath.Join(harness.repo, "does-not-exist")})
	if report.DirsChecked != 0 || len(report.Findings) != 0 || report.OrphanDirs != 0 {
		t.Fatalf("unexpected findings for missing base dir: %+v", report)
	}
}
