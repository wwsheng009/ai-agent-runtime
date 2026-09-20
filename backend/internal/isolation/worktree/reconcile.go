// Worktree reconciliation (plan P1-3, findings H11 + H15).
//
// The isolation layer has three sources of truth that can drift apart:
//
//  1. the directories under the worktree base dir (`.aicli/agent-worktrees/*`);
//  2. the git worktree registration table (`git worktree list`);
//  3. the agent registry / session rows that own a worktree (passed in as
//     RegisteredPaths, because this package must not import the registry).
//
// A crashed or force-killed child leaves (1) behind without (2) — the live
// example is `.aicli/agent-worktrees/resume-redraw-fix`, which no longer shows
// up in `git worktree list`. Nothing scans for that today, so the directory
// leaks forever.
//
// ReconcileWorktrees classifies every path against the three sources and, in
// enforce mode, reclaims only what is confirmed by two of them:
//
//   - an orphan directory must be absent from BOTH the git registration and the
//     caller's registered set before it is deleted;
//   - a stale git registration must have a missing directory before `git
//     worktree prune` is allowed to drop it;
//   - a path the caller reports as live (RegisteredPaths) is never reclaimed,
//     even when git does not know it.
//
// Default mode is observe: the pass reports what an enforce pass would do and
// touches nothing, matching the P2-9 reconcile family.
package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReconcileMode selects between reporting and reclaiming.
type ReconcileMode string

const (
	// ReconcileModeObserve reports findings and reclaims nothing (default).
	ReconcileModeObserve ReconcileMode = "observe"
	// ReconcileModeEnforce performs the reclaim actions the report describes.
	ReconcileModeEnforce ReconcileMode = "enforce"
)

// Finding codes emitted by a worktree reconcile pass.
const (
	// FindingWorktreeOrphanDir is a directory under the base dir that is
	// neither git-registered nor owned by a registry row.
	FindingWorktreeOrphanDir = "worktree_orphan_dir"
	// FindingWorktreeStaleRegistration is a git worktree registration whose
	// directory no longer exists.
	FindingWorktreeStaleRegistration = "worktree_stale_registration"
	// FindingWorktreeRegistryStale is a registered path that exists neither as
	// a directory nor as a git registration (the registry row needs the
	// agent-side reclaim; this pass only reports it).
	FindingWorktreeRegistryStale = "worktree_registry_stale"
	// FindingWorktreeUnmanagedRegistration is a git-registered worktree under
	// the base dir that no registry row claims. It is reported, never
	// reclaimed: an apply/discard in flight looks the same.
	FindingWorktreeUnmanagedRegistration = "worktree_unmanaged_registration"
)

// Reconcile action names recorded on findings.
const (
	ReconcileActionNone     = ""
	ReconcileActionPruneDir = "prune_dir"
	ReconcileActionPruneGit = "prune_registration"
	ReconcileActionManual   = "manual_review"
)

// ReconcileOptions configures a worktree reconcile pass.
type ReconcileOptions struct {
	// RepoRoot is the main repository (or any path inside it). Required.
	RepoRoot string
	// BaseDir is where worktrees are created. Empty → <RepoRoot>/.aicli/agent-worktrees.
	BaseDir string
	// RegisteredPaths are worktree paths the caller knows to be live (agent
	// registry / session context). They are never reclaimed.
	RegisteredPaths []string
	// Mode observe (default) or enforce.
	Mode ReconcileMode
	// Now is the clock, injectable for tests; zero → time.Now().UTC().
	Now time.Time
}

// ReconcileFinding is one drift finding.
type ReconcileFinding struct {
	Code   string `json:"code"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Action is what an enforce pass does (or, when Reclaimed is true, did).
	Action string `json:"action,omitempty"`
	// Reclaimed reports whether this pass actually reclaimed the path.
	Reclaimed bool `json:"reclaimed,omitempty"`
	// Error carries a per-finding reclaim failure; the pass keeps going.
	Error string `json:"error,omitempty"`
}

// ReconcileReport summarizes one pass. Counts are what the pass saw and, in
// enforce mode, what it did.
type ReconcileReport struct {
	Mode           string             `json:"mode"`
	ReconciledAt   time.Time          `json:"reconciled_at"`
	RepoRoot       string             `json:"repo_root"`
	BaseDir        string             `json:"base_dir"`
	DirsChecked    int                `json:"dirs_checked"`
	ManagedChecked int                `json:"managed_checked"`
	Registered     int                `json:"registered"`
	Findings       []ReconcileFinding `json:"findings,omitempty"`
	OrphanDirs     int                `json:"orphan_dirs"`
	StaleGit       int                `json:"stale_registrations"`
	StaleRegistry  int                `json:"stale_registry_paths"`
	Unmanaged      int                `json:"unmanaged_registrations"`
	ReclaimedDirs  int                `json:"reclaimed_dirs"`
	ReclaimedGit   int                `json:"reclaimed_registrations"`
	Failed         int                `json:"failed"`
	Error          string             `json:"error,omitempty"`
}

// ReconcileWorktrees compares the worktree directories, the git registration
// table and the caller's registered paths, then reclaims the provable drift in
// enforce mode. Observe mode (the default) is read-only.
func ReconcileWorktrees(ctx context.Context, opts ReconcileOptions) (ReconcileReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	mode := opts.Mode
	if mode != ReconcileModeEnforce {
		mode = ReconcileModeObserve
	}
	report := ReconcileReport{Mode: string(mode), ReconciledAt: now}

	repoRoot, err := ResolveRepoRoot(ctx, opts.RepoRoot)
	if err != nil {
		return report, err
	}
	report.RepoRoot = repoRoot
	baseDir := strings.TrimSpace(opts.BaseDir)
	if baseDir == "" {
		baseDir = filepath.Join(repoRoot, ".aicli", defaultBaseDirName)
	} else if !filepath.IsAbs(baseDir) {
		baseDir = filepath.Join(repoRoot, baseDir)
	}
	if abs, absErr := filepath.Abs(baseDir); absErr == nil {
		baseDir = abs
	}
	report.BaseDir = baseDir

	registered := normalizeReconcilePaths(opts.RegisteredPaths)
	report.Registered = len(registered)

	managed, err := listGitWorktrees(ctx, repoRoot)
	if err != nil {
		return report, err
	}
	managedByPath := make(map[string]gitWorktreeEntry, len(managed))
	for _, entry := range managed {
		managedByPath[entry.Path] = entry
	}

	dirs, err := listWorktreeDirs(baseDir)
	if err != nil {
		return report, err
	}
	report.DirsChecked = len(dirs)

	// 1. Directories: orphan when neither git nor the registry knows them.
	for _, dir := range dirs {
		if _, ok := managedByPath[dir]; ok {
			report.ManagedChecked++
			if _, owned := registered[dir]; !owned {
				report.Unmanaged++
				report.Findings = append(report.Findings, ReconcileFinding{
					Code:   FindingWorktreeUnmanagedRegistration,
					Path:   dir,
					Branch: managedByPath[dir].Branch,
					Detail: "git-registered worktree with no owning registry row; an apply/discard in flight looks the same, so it is never reclaimed automatically",
					Action: ReconcileActionManual,
				})
			}
			continue
		}
		if _, owned := registered[dir]; owned {
			// Live per the registry; git simply does not track it (already
			// applied/discarded worktree dir kept for debugging). Report only.
			report.Findings = append(report.Findings, ReconcileFinding{
				Code:   FindingWorktreeOrphanDir,
				Path:   dir,
				Detail: "directory is owned by a registry row but is not git-registered; keeping it because the registry claims it",
				Action: ReconcileActionNone,
			})
			continue
		}
		report.OrphanDirs++
		finding := ReconcileFinding{
			Code:   FindingWorktreeOrphanDir,
			Path:   dir,
			Detail: "directory is neither git-registered nor owned by any registry row (crashed or force-killed child)",
			Action: ReconcileActionPruneDir,
		}
		if mode == ReconcileModeEnforce {
			if err := reclaimOrphanDir(ctx, repoRoot, dir); err != nil {
				finding.Error = err.Error()
				report.Failed++
			} else {
				finding.Reclaimed = true
				report.ReclaimedDirs++
			}
		}
		report.Findings = append(report.Findings, finding)
	}

	// 2. Git registrations under the base dir whose directory is gone.
	for _, entry := range managed {
		if !pathWithinBase(baseDir, entry.Path) {
			continue
		}
		if _, err := os.Stat(entry.Path); err == nil || !os.IsNotExist(err) {
			continue
		}
		report.StaleGit++
		finding := ReconcileFinding{
			Code:   FindingWorktreeStaleRegistration,
			Path:   entry.Path,
			Branch: entry.Branch,
			Detail: "git registration points at a missing directory",
			Action: ReconcileActionPruneGit,
		}
		if mode == ReconcileModeEnforce {
			if err := runGit(ctx, repoRoot, "worktree", "prune"); err != nil {
				finding.Error = err.Error()
				report.Failed++
			} else {
				finding.Reclaimed = true
				report.ReclaimedGit++
			}
		}
		report.Findings = append(report.Findings, finding)
	}

	// 3. Registry paths that exist nowhere: the registry row itself is stale,
	// which is the agent-side reclaim's business — report it here so the two
	// reconcile passes can be cross-checked.
	for _, path := range sortedKeys(registered) {
		if _, statErr := os.Stat(path); statErr == nil || !os.IsNotExist(statErr) {
			continue
		}
		if _, ok := managedByPath[path]; ok {
			continue
		}
		report.StaleRegistry++
		report.Findings = append(report.Findings, ReconcileFinding{
			Code:   FindingWorktreeRegistryStale,
			Path:   path,
			Detail: "registry row references a worktree path that exists neither as a directory nor as a git registration",
			Action: ReconcileActionManual,
		})
	}

	sort.SliceStable(report.Findings, func(i, j int) bool {
		if report.Findings[i].Code != report.Findings[j].Code {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return report.Findings[i].Path < report.Findings[j].Path
	})
	return report, nil
}

// reclaimOrphanDir removes a confirmed orphan directory and prunes the git
// registration table so the two sources stay consistent.
func reclaimOrphanDir(ctx context.Context, repoRoot, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove orphan worktree dir: %w", err)
	}
	if err := runGit(ctx, repoRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune after orphan dir removal: %w", err)
	}
	return nil
}

// gitWorktreeEntry is one `git worktree list --porcelain` record.
type gitWorktreeEntry struct {
	Path   string
	Branch string
}

// listGitWorktrees parses `git worktree list --porcelain`.
func listGitWorktrees(ctx context.Context, repoRoot string) ([]gitWorktreeEntry, error) {
	out, err := runGitOutput(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %w", err)
	}
	entries := make([]gitWorktreeEntry, 0, 4)
	var current *gitWorktreeEntry
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			if current != nil {
				entries = append(entries, *current)
				current = nil
			}
		case strings.HasPrefix(line, "worktree "):
			if current != nil {
				entries = append(entries, *current)
			}
			path := normalizeReconcilePath(strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
			current = &gitWorktreeEntry{Path: path}
		case strings.HasPrefix(line, "branch ") && current != nil:
			branch := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			current.Branch = strings.TrimPrefix(branch, "refs/heads/")
		}
	}
	if current != nil {
		entries = append(entries, *current)
	}
	return entries, nil
}

// listWorktreeDirs lists the direct child directories of baseDir. A missing
// base dir is not an error: it means there is nothing to reconcile.
func listWorktreeDirs(baseDir string) ([]string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read worktree base dir: %w", err)
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirs = append(dirs, normalizeReconcilePath(filepath.Join(baseDir, entry.Name())))
	}
	sort.Strings(dirs)
	return dirs, nil
}

// pathWithinBase reports whether path is a direct child of base.
func pathWithinBase(base, path string) bool {
	rel, err := filepath.Rel(normalizeReconcilePath(base), normalizeReconcilePath(path))
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return !strings.Contains(rel, string(filepath.Separator))
}

func normalizeReconcilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

func normalizeReconcilePaths(paths []string) map[string]bool {
	out := make(map[string]bool, len(paths))
	for _, raw := range paths {
		path := normalizeReconcilePath(raw)
		if path != "" {
			out[path] = true
		}
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
