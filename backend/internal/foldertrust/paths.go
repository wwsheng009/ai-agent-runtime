package foldertrust

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WorkspaceKey returns the durable trust key for cwd:
// git repository root when inside a repo, otherwise the absolute cwd.
// The key is cleaned; callers should still pass it through Canonicalize for store I/O.
func WorkspaceKey(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		if abs, err := os.Getwd(); err == nil {
			cwd = abs
		}
	}
	if cwd == "" {
		return ""
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	cwd = filepath.Clean(cwd)
	if root := findGitRoot(cwd); root != "" {
		return root
	}
	return cwd
}

// Canonicalize returns an absolute, cleaned path. On failure returns cleaned input.
func Canonicalize(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && strings.TrimSpace(resolved) != "" {
		path = resolved
	}
	return filepath.Clean(path)
}

// IsUnsafeTrustRoot reports over-broad keys the store must refuse to record or honor:
// empty, non-absolute, filesystem root, or the current user's home directory.
func IsUnsafeTrustRoot(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return true
	}
	// Filesystem root (/, C:\, \\).
	if isFilesystemRoot(clean) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	home = Canonicalize(home)
	if home == "" {
		return false
	}
	return samePath(clean, home)
}

func isFilesystemRoot(path string) bool {
	path = filepath.Clean(path)
	if path == string(filepath.Separator) {
		return true
	}
	if runtime.GOOS == "windows" {
		// C:\ or C:
		vol := filepath.VolumeName(path)
		if vol == "" {
			return false
		}
		rest := strings.TrimPrefix(path, vol)
		rest = strings.Trim(rest, `/\`)
		return rest == ""
	}
	return false
}

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathIsUnder reports whether child is equal to parent or nested under it.
func pathIsUnder(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if parent == "" || child == "" {
		return false
	}
	if samePath(child, parent) {
		return true
	}
	sep := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(child), strings.ToLower(parent)+sep)
	}
	return strings.HasPrefix(child, parent+sep)
}

// findGitRoot walks upward from start looking for a .git entry (dir or file for worktrees).
func findGitRoot(start string) string {
	dir := filepath.Clean(start)
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			// .git may be a directory or a gitfile for worktrees/submodules.
			if info.IsDir() || info.Mode().IsRegular() {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
