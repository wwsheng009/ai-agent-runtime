package memorystore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolveProjectKey returns the stable project isolation key defined by
// 03 §5.5. It prefers a git origin + repository-relative path and falls back to
// a canonical path hash for non-git workspaces.
func ResolveProjectKey(projectRoot string) string {
	root := canonicalProjectRoot(projectRoot)
	if root == "" {
		return ""
	}
	origin, gitRoot := gitOriginAndRoot(root)
	if origin != "" && gitRoot != "" {
		rel, err := filepath.Rel(gitRoot, root)
		if err != nil {
			rel = root
		}
		return "pk_" + hashProjectKey("git\x00" + normalizeGitOrigin(origin) + "\x00" + filepath.ToSlash(rel))[:16]
	}
	return "pk_" + hashProjectKey("path\x00" + normalizePathForKey(root))[:16]
}

func canonicalProjectRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		abs = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

func normalizePathForKey(root string) string {
	root = filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

func normalizeGitOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	origin = strings.TrimSuffix(origin, "/")
	origin = strings.TrimSuffix(origin, ".git")
	return origin
}

func hashProjectKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func gitOriginAndRoot(root string) (string, string) {
	gitRoot := strings.TrimSpace(runGit(root, "rev-parse", "--show-toplevel"))
	if gitRoot == "" {
		return "", ""
	}
	if abs, err := filepath.Abs(gitRoot); err == nil {
		gitRoot = filepath.Clean(abs)
		if resolved, err := filepath.EvalSymlinks(gitRoot); err == nil && strings.TrimSpace(resolved) != "" {
			gitRoot = filepath.Clean(resolved)
		}
	}
	origin := strings.TrimSpace(runGit(root, "remote", "get-url", "origin"))
	return origin, gitRoot
}

func runGit(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
