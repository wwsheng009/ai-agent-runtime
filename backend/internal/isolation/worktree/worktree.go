// Package worktree provides git worktree isolation for spawn_agent children.
//
// Lifecycle: Create → bind child cwd/session paths → run → optional Apply → Remove.
// Failures never silently fall back to the main tree (no polluted primary checkout).
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/uniqid"
)

const (
	// ModeNone leaves the child in the parent workspace.
	ModeNone = "none"
	// ModeWorktree creates a dedicated git worktree for the child.
	ModeWorktree = "worktree"

	defaultBaseDirName = "agent-worktrees"
	branchPrefix       = "aicli/agent"
)

// Options configures worktree creation.
type Options struct {
	// RepoRoot is the main repository (or any path inside it). Required.
	RepoRoot string
	// SessionID is used for branch/path uniqueness. Required.
	SessionID string
	// BaseDir is where worktrees are created. Empty → <RepoRoot>/.aicli/agent-worktrees.
	BaseDir string
	// Branch is the worktree branch name. Empty → aicli/agent/<sanitized-session>.
	Branch string
	// StartPoint is the commitish to branch from. Empty → HEAD.
	StartPoint string
	// Keep if true, Remove skips git worktree remove (for debugging).
	Keep bool
}

// Handle is a live isolated worktree.
type Handle struct {
	// Path is the absolute worktree directory (child tool cwd / workspace root).
	Path string
	// Branch is the branch checked out in the worktree.
	Branch string
	// RepoRoot is the absolute main repository root.
	RepoRoot string
	// SessionID is the owning child session id.
	SessionID string
	// CreatedAt is when the worktree was created.
	CreatedAt time.Time
	// Keep preserves the worktree on Remove when true.
	Keep bool
}

// NormalizeMode returns none|worktree or an error for unknown values.
// Empty input normalizes to none.
func NormalizeMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ModeNone:
		return ModeNone, nil
	case ModeWorktree:
		return ModeWorktree, nil
	default:
		return "", fmt.Errorf("invalid isolation mode %q (want none|worktree)", strings.TrimSpace(raw))
	}
}

// Create adds a new git worktree for session isolation.
// On failure the main tree is left unchanged and a partial worktree path is cleaned when possible.
func Create(ctx context.Context, opts Options) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		return nil, errors.New("worktree isolation requires session_id")
	}
	repoRoot, err := ResolveRepoRoot(ctx, opts.RepoRoot)
	if err != nil {
		return nil, err
	}
	baseDir := strings.TrimSpace(opts.BaseDir)
	if baseDir == "" {
		baseDir = filepath.Join(repoRoot, ".aicli", defaultBaseDirName)
	} else if !filepath.IsAbs(baseDir) {
		baseDir = filepath.Join(repoRoot, baseDir)
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree base dir: %w", err)
	}

	branch := strings.TrimSpace(opts.Branch)
	if branch == "" {
		branch = branchPrefix + "/" + sanitizeSessionID(sessionID)
	}
	worktreePath := filepath.Join(baseDir, sanitizeSessionID(sessionID))
	if abs, absErr := filepath.Abs(worktreePath); absErr == nil {
		worktreePath = abs
	}

	// Refuse to reuse an existing path to avoid accidental main-tree pollution.
	if st, statErr := os.Stat(worktreePath); statErr == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", worktreePath)
	} else if st != nil || !os.IsNotExist(statErr) {
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("stat worktree path: %w", statErr)
		}
	}

	startPoint := strings.TrimSpace(opts.StartPoint)
	if startPoint == "" {
		startPoint = "HEAD"
	}

	// Prefer a new branch from startPoint so parallel agents do not share HEAD locks.
	args := []string{"worktree", "add", "-b", branch, worktreePath, startPoint}
	if err := runGit(ctx, repoRoot, args...); err != nil {
		// Branch may already exist from a previous crashed session: force unique branch.
		if strings.Contains(err.Error(), "already exists") {
			// 分支名必须唯一：粗粒度时钟下同一 tick 的两次重试会拿到
			// 相同后缀，第二次 worktree add 仍然失败。
			branch = fmt.Sprintf("%s-%s", branch, uniqid.Token())
			args = []string{"worktree", "add", "-b", branch, worktreePath, startPoint}
			if err2 := runGit(ctx, repoRoot, args...); err2 != nil {
				_ = os.RemoveAll(worktreePath)
				return nil, fmt.Errorf("git worktree add failed (no main-tree fallback): %w", err2)
			}
		} else {
			_ = os.RemoveAll(worktreePath)
			return nil, fmt.Errorf("git worktree add failed (no main-tree fallback): %w", err)
		}
	}

	return &Handle{
		Path:      worktreePath,
		Branch:    branch,
		RepoRoot:  repoRoot,
		SessionID: sessionID,
		CreatedAt: time.Now().UTC(),
		Keep:      opts.Keep,
	}, nil
}

// Remove deletes the worktree and attempts to delete its branch.
// Safe to call on nil. Keep=true skips removal.
func (h *Handle) Remove(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if h.Keep {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path := strings.TrimSpace(h.Path)
	repoRoot := strings.TrimSpace(h.RepoRoot)
	if path == "" {
		return errors.New("worktree path is empty")
	}
	var errs []string
	if repoRoot != "" {
		if err := runGit(ctx, repoRoot, "worktree", "remove", "--force", path); err != nil {
			// Fall back to directory delete + prune when git remove fails (e.g. already gone).
			if removeErr := os.RemoveAll(path); removeErr != nil {
				errs = append(errs, fmt.Sprintf("worktree remove: %v; rmdir: %v", err, removeErr))
			} else {
				_ = runGit(ctx, repoRoot, "worktree", "prune")
			}
		}
		if branch := strings.TrimSpace(h.Branch); branch != "" {
			// Best-effort branch cleanup; ignore failure if branch still has refs.
			_ = runGit(ctx, repoRoot, "branch", "-D", branch)
		}
	} else if err := os.RemoveAll(path); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// DiffStat returns a short porcelain status for the worktree (for completion summary).
func (h *Handle) DiffStat(ctx context.Context) (string, error) {
	if h == nil {
		return "", errors.New("nil worktree handle")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := runGitOutput(ctx, h.Path, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ApplyOptions controls how worktree changes are applied to the main repo.
type ApplyOptions struct {
	// Paths limits checkout to specific paths relative to the worktree. Empty = all tracked changes.
	Paths []string
	// Force overwrites local main-tree modifications instead of refusing
	// (H14). Default false: a conflicting main-tree path fails the apply.
	Force bool
}

const (
	// maxApplyConflictPaths bounds the conflict list carried by
	// ApplyConflictError (the count still covers the full set).
	maxApplyConflictPaths = 8
	// maxApplySkippedPaths bounds the reported out-of-scope paths.
	maxApplySkippedPaths = 16
)

// ApplyConflict is a main-tree path whose local state would be overwritten by
// an apply.
type ApplyConflict struct {
	// Path is repo-relative.
	Path string `json:"path"`
	// Status is the `git status --porcelain` status of the main-tree entry
	// (for example " M" or "??").
	Status string `json:"status"`
}

// ApplyConflictError is returned when an apply would overwrite local main-tree
// changes. It carries the conflicting paths and an actionable NextAction so
// the parent agent can decide (commit/stash, narrow paths, or force) instead of
// losing the local edits silently.
type ApplyConflictError struct {
	Branch     string          `json:"branch"`
	Conflicts  []ApplyConflict `json:"conflicts"`
	NextAction string          `json:"next_action"`
}

func (e *ApplyConflictError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"apply refused: main tree has local changes in %d path(s) that %s would overwrite (%s); %s",
		len(e.Conflicts),
		strings.TrimSpace(e.Branch),
		strings.Join(applyConflictPaths(e.Conflicts), ", "),
		strings.TrimSpace(e.NextAction),
	)
}

// ApplyReport describes what an apply pass did, or — from CheckApply — what it
// would do.
type ApplyReport struct {
	Branch string `json:"branch"`
	// CandidatePaths are the repo-relative paths whose content differs between
	// the main tree and the worktree branch: the apply set.
	CandidatePaths []string `json:"candidate_paths,omitempty"`
	// Conflicts are candidate paths that carry local main-tree modifications
	// and would be overwritten by the checkout.
	Conflicts []ApplyConflict `json:"conflicts,omitempty"`
	// SkippedPaths are branch changes outside the requested paths filter. They
	// are never part of the apply set; they are reported so the caller stops
	// expecting them (untracked worktree files included).
	SkippedPaths []string `json:"skipped_paths,omitempty"`
	// Applied reports whether the checkout ran.
	Applied bool `json:"applied,omitempty"`
	// Forced reports whether local main-tree changes were overwritten on purpose.
	Forced bool `json:"forced,omitempty"`
}

// CheckApply runs the H14 preflight without touching the main tree: it computes
// the apply set (paths whose content differs between the main tree HEAD and the
// worktree branch) and the subset that has local main-tree modifications, plus
// the worktree untracked files that stay outside the apply set.
//
// It reflects the branch tip; ApplyWithReport commits the worktree working tree
// first so uncommitted child edits are included.
func (h *Handle) CheckApply(ctx context.Context, opts ApplyOptions) (ApplyReport, error) {
	if h == nil {
		return ApplyReport{}, errors.New("nil worktree handle")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	branch := strings.TrimSpace(h.Branch)
	repoRoot := strings.TrimSpace(h.RepoRoot)
	if branch == "" || repoRoot == "" {
		return ApplyReport{}, errors.New("worktree apply requires branch and repo root")
	}
	report := ApplyReport{Branch: branch}
	selected := normalizeApplyPaths(opts.Paths)
	candidates, err := h.applyCandidatePaths(ctx, selected)
	if err != nil {
		return report, err
	}
	report.CandidatePaths = candidates
	if len(selected) > 0 {
		allChanged, err := h.branchChangedPaths(ctx)
		if err != nil {
			return report, err
		}
		report.SkippedPaths = outOfScopePaths(allChanged, candidates)
	}
	if len(candidates) == 0 {
		return report, nil
	}
	dirty, err := h.mainTreeDirtyPaths(ctx, candidates)
	if err != nil {
		return report, err
	}
	for _, path := range candidates {
		if status, ok := dirty[path]; ok {
			report.Conflicts = append(report.Conflicts, ApplyConflict{Path: path, Status: status})
		}
	}
	return report, nil
}

// Apply copies committed or staged changes from the worktree branch into the main tree
// via `git checkout <branch> -- <paths>`. Untracked files are not applied.
// This never runs when isolation creation failed (caller must only apply a live Handle).
func (h *Handle) Apply(ctx context.Context, opts ApplyOptions) error {
	_, err := h.ApplyWithReport(ctx, opts)
	return err
}

// ApplyWithReport applies the worktree branch into the main tree and returns
// what happened. By default (Force=false) it refuses to overwrite main-tree
// local modifications and returns *ApplyConflictError; the report is still
// returned so the caller can render the conflicts and next_action.
func (h *Handle) ApplyWithReport(ctx context.Context, opts ApplyOptions) (ApplyReport, error) {
	if h == nil {
		return ApplyReport{}, errors.New("nil worktree handle")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	branch := strings.TrimSpace(h.Branch)
	repoRoot := strings.TrimSpace(h.RepoRoot)
	if branch == "" || repoRoot == "" {
		return ApplyReport{}, errors.New("worktree apply requires branch and repo root")
	}
	// Ensure worktree changes are committed enough for checkout from branch tip.
	// Prefer applying the working tree via path checkout of current worktree index+files:
	// `git -C repo checkout <worktree-path> -- .` is not valid; use branch after auto-commit optional.
	// For MVP: stage+commit in worktree when dirty, then checkout branch files into main.
	if dirty, err := h.isDirty(ctx); err != nil {
		return ApplyReport{}, err
	} else if dirty {
		if err := h.autoCommit(ctx, "aicli worktree apply snapshot"); err != nil {
			return ApplyReport{}, fmt.Errorf("commit worktree changes before apply: %w", err)
		}
	}
	report, err := h.CheckApply(ctx, opts)
	if err != nil {
		return report, err
	}
	if len(report.Conflicts) > 0 && !opts.Force {
		return report, h.applyConflictError(report)
	}
	paths := normalizeApplyPaths(opts.Paths)
	args := []string{"checkout", branch, "--"}
	if len(paths) == 0 {
		args = append(args, ".")
	} else {
		args = append(args, paths...)
	}
	if err := runGit(ctx, repoRoot, args...); err != nil {
		return report, fmt.Errorf("apply worktree changes to main tree: %w", err)
	}
	report.Applied = true
	report.Forced = opts.Force && len(report.Conflicts) > 0
	return report, nil
}

// applyConflictError builds the actionable refusal from a preflight report.
func (h *Handle) applyConflictError(report ApplyReport) *ApplyConflictError {
	capped := report.Conflicts
	if len(capped) > maxApplyConflictPaths {
		capped = capped[:maxApplyConflictPaths]
	}
	next := "commit or stash those main-tree paths first, re-run apply with a narrower paths list that excludes them, or pass force=true to overwrite them deliberately"
	if len(report.SkippedPaths) > 0 {
		next += fmt.Sprintf(
			"; note: %d worktree path(s) are outside the requested paths filter and were not applied (%s)",
			len(report.SkippedPaths),
			strings.Join(report.SkippedPaths, ", "),
		)
	}
	return &ApplyConflictError{Branch: report.Branch, Conflicts: capped, NextAction: next}
}

// branchChangedPaths lists every path the worktree branch changes relative to
// the main tree HEAD (the full apply set, before any paths filter).
func (h *Handle) branchChangedPaths(ctx context.Context) ([]string, error) {
	out, err := runGitOutput(ctx, h.RepoRoot, "diff", "--name-only", "-z", "HEAD", strings.TrimSpace(h.Branch))
	if err != nil {
		return nil, fmt.Errorf("worktree apply preflight (branch diff): %w", err)
	}
	paths := make([]string, 0, 8)
	for _, path := range splitNULPaths(out) {
		if path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// outOfScopePaths returns the changed paths that a paths-filtered apply leaves
// behind (set difference, capped and sorted).
func outOfScopePaths(allChanged, candidates []string) []string {
	if len(allChanged) == 0 {
		return nil
	}
	inScope := make(map[string]bool, len(candidates))
	for _, path := range candidates {
		inScope[path] = true
	}
	skipped := make([]string, 0, len(allChanged))
	for _, path := range allChanged {
		if !inScope[path] {
			skipped = append(skipped, path)
		}
	}
	if len(skipped) > maxApplySkippedPaths {
		skipped = skipped[:maxApplySkippedPaths]
	}
	return skipped
}

// applyCandidatePaths lists the paths whose content differs between the main
// tree HEAD and the worktree branch, optionally limited to paths.
func (h *Handle) applyCandidatePaths(ctx context.Context, paths []string) ([]string, error) {
	args := []string{"diff", "--name-only", "-z", "HEAD", strings.TrimSpace(h.Branch)}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := runGitOutput(ctx, h.RepoRoot, args...)
	if err != nil {
		return nil, fmt.Errorf("worktree apply preflight (diff): %w", err)
	}
	candidates := make([]string, 0, 8)
	for _, path := range splitNULPaths(out) {
		if path != "" {
			candidates = append(candidates, path)
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

// mainTreeDirtyPaths maps repo-relative path to porcelain status for every
// local main-tree entry inside candidates.
func (h *Handle) mainTreeDirtyPaths(ctx context.Context, candidates []string) (map[string]string, error) {
	args := []string{"status", "--porcelain", "-z", "--"}
	args = append(args, candidates...)
	out, err := runGitOutput(ctx, h.RepoRoot, args...)
	if err != nil {
		return nil, fmt.Errorf("worktree apply preflight (status): %w", err)
	}
	dirty := make(map[string]string, len(candidates))
	for _, entry := range parsePorcelainZ(out) {
		if entry.Path == "" {
			continue
		}
		dirty[entry.Path] = entry.Status
		if entry.OrigPath != "" {
			dirty[entry.OrigPath] = entry.Status
		}
	}
	return dirty, nil
}

// porcelainEntry is one `git status --porcelain -z` record.
type porcelainEntry struct {
	Status   string
	Path     string
	OrigPath string
}

// parsePorcelainZ parses NUL-separated porcelain v1 records. Rename/copy
// records carry the original path as the next NUL field.
func parsePorcelainZ(out string) []porcelainEntry {
	if out == "" {
		return nil
	}
	fields := strings.Split(out, "\x00")
	entries := make([]porcelainEntry, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		raw := fields[i]
		if len(raw) < 4 {
			continue
		}
		entry := porcelainEntry{Status: raw[:2], Path: raw[3:]}
		if strings.ContainsAny(entry.Status, "RC") && i+1 < len(fields) {
			i++
			entry.OrigPath = fields[i]
		}
		entries = append(entries, entry)
	}
	return entries
}

// splitNULPaths splits `--name-only -z` output into paths.
func splitNULPaths(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(out, "\x00")
}

// normalizeApplyPaths trims, dedupes and orders a caller-supplied path filter.
func normalizeApplyPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// applyConflictPaths renders the conflicting paths for the error message.
func applyConflictPaths(conflicts []ApplyConflict) []string {
	paths := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		if strings.TrimSpace(conflict.Path) != "" {
			paths = append(paths, conflict.Path)
		}
	}
	return paths
}

func (h *Handle) isDirty(ctx context.Context) (bool, error) {
	out, err := runGitOutput(ctx, h.Path, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (h *Handle) autoCommit(ctx context.Context, message string) error {
	if err := runGit(ctx, h.Path, "add", "-A"); err != nil {
		return err
	}
	// Allow empty? No — only called when dirty.
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=aicli",
		"GIT_AUTHOR_EMAIL=aicli@local",
		"GIT_COMMITTER_NAME=aicli",
		"GIT_COMMITTER_EMAIL=aicli@local",
	)
	return runGitEnv(ctx, h.Path, env, "commit", "-m", message, "--no-gpg-sign")
}

// ResolveRepoRoot finds the git top-level for path. Fails clearly when not a git repo.
func ResolveRepoRoot(ctx context.Context, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("worktree isolation requires repo root: %w", err)
		}
		path = cwd
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	if st, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("repo path not accessible: %w", err)
	} else if !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	out, err := runGitOutput(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("worktree isolation requires a git repository (no main-tree fallback): %w", err)
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", errors.New("git rev-parse returned empty toplevel")
	}
	// Normalize Windows paths from git (forward slashes).
	return filepath.Clean(root), nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := runGitOutput(ctx, dir, args...)
	return err
}

func runGitEnv(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := runGitOutputEnv(ctx, dir, env, args...)
	return err
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitOutputEnv(ctx, dir, nil, args...)
}

func runGitOutputEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func sanitizeSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "session"
	}
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "session"
	}
	// Keep path segment short for Windows MAX_PATH comfort.
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
