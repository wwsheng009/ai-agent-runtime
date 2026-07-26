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
	"strings"
	"time"
	"unicode"
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
			branch = fmt.Sprintf("%s-%d", branch, time.Now().UnixNano())
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
}

// Apply copies committed or staged changes from the worktree branch into the main tree
// via `git checkout <branch> -- <paths>`. Untracked files are not applied.
// This never runs when isolation creation failed (caller must only apply a live Handle).
func (h *Handle) Apply(ctx context.Context, opts ApplyOptions) error {
	if h == nil {
		return errors.New("nil worktree handle")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	branch := strings.TrimSpace(h.Branch)
	repoRoot := strings.TrimSpace(h.RepoRoot)
	if branch == "" || repoRoot == "" {
		return errors.New("worktree apply requires branch and repo root")
	}
	// Ensure worktree changes are committed enough for checkout from branch tip.
	// Prefer applying the working tree via path checkout of current worktree index+files:
	// `git -C repo checkout <worktree-path> -- .` is not valid; use branch after auto-commit optional.
	// For MVP: stage+commit in worktree when dirty, then checkout branch files into main.
	if dirty, err := h.isDirty(ctx); err != nil {
		return err
	} else if dirty {
		if err := h.autoCommit(ctx, "aicli worktree apply snapshot"); err != nil {
			return fmt.Errorf("commit worktree changes before apply: %w", err)
		}
	}
	args := []string{"checkout", branch, "--"}
	if len(opts.Paths) == 0 {
		args = append(args, ".")
	} else {
		for _, p := range opts.Paths {
			p = strings.TrimSpace(p)
			if p != "" {
				args = append(args, p)
			}
		}
		if len(args) == 3 {
			args = append(args, ".")
		}
	}
	if err := runGit(ctx, repoRoot, args...); err != nil {
		return fmt.Errorf("apply worktree changes to main tree: %w", err)
	}
	return nil
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
