package foldertrust

import (
	"os"
	"path/filepath"
	"strings"
)

// ConfigKind names a trust-sensitive repo-local config family.
type ConfigKind string

const (
	ConfigKindPlugins ConfigKind = "plugins"
	ConfigKindHooks   ConfigKind = "hooks"
	ConfigKindMCP     ConfigKind = "mcp"
	ConfigKindAgents  ConfigKind = "agents"
)

// RepoConfigsPresent reports whether any trust-sensitive project config exists
// under cwd (or its git root when present). Used by Decide step 4.
func RepoConfigsPresent(cwd string) bool {
	return len(CollectRepoConfigKinds(cwd, true)) > 0
}

// RepoConfigKinds returns distinct config kinds present under cwd (display / debug).
func RepoConfigKinds(cwd string) []ConfigKind {
	return CollectRepoConfigKinds(cwd, false)
}

// CollectRepoConfigKinds scans markers; when firstOnly is true, returns after first hit.
func CollectRepoConfigKinds(cwd string, firstOnly bool) []ConfigKind {
	roots := scanRoots(cwd)
	var kinds []ConfigKind
	hit := func(k ConfigKind) bool {
		for _, existing := range kinds {
			if existing == k {
				return firstOnly
			}
		}
		kinds = append(kinds, k)
		return firstOnly
	}

	for _, root := range roots {
		// Project plugins: code-exec via hooks/MCP contributions.
		for _, rel := range []string{
			filepath.Join(".aicli", "plugins"),
			filepath.Join(".agents", "plugins"),
		} {
			if directoryHasPluginChild(filepath.Join(root, rel)) {
				if hit(ConfigKindPlugins) && firstOnly {
					return kinds
				}
				break
			}
		}

		// Project hooks files (direct shell/http hooks without plugins).
		for _, rel := range []string{
			filepath.Join(".aicli", "hooks.yaml"),
			filepath.Join(".aicli", "hooks.yml"),
			filepath.Join(".aicli", "hooks.json"),
			"hooks.yaml",
			"hooks.yml",
		} {
			if pathExists(filepath.Join(root, rel)) {
				if hit(ConfigKindHooks) && firstOnly {
					return kinds
				}
				break
			}
		}

		// Project MCP configs that spawn local processes.
		for _, rel := range []string{
			"mcp.yaml",
			"mcp.yml",
			"mcp.json",
			".mcp.json",
			filepath.Join(".aicli", "mcp.yaml"),
			filepath.Join(".aicli", "mcp.yml"),
			filepath.Join(".aicli", "mcp.json"),
			filepath.Join(".cursor", "mcp.json"),
			filepath.Join("configs", "mcp.yaml"),
		} {
			if pathExists(filepath.Join(root, rel)) {
				if hit(ConfigKindMCP) && firstOnly {
					return kinds
				}
				break
			}
		}

		// Project agent definitions (may carry tools/hooks and shadow built-ins).
		for _, rel := range []string{
			filepath.Join(".aicli", "agents"),
			filepath.Join(".agents", "agents"),
		} {
			if directoryNonEmpty(filepath.Join(root, rel)) {
				if hit(ConfigKindAgents) && firstOnly {
					return kinds
				}
				break
			}
		}
	}
	return kinds
}

// IsProjectScopedPath reports whether path lives under projectRoot (for MCP gate).
func IsProjectScopedPath(path, projectRoot string) bool {
	path = Canonicalize(path)
	projectRoot = Canonicalize(projectRoot)
	if path == "" || projectRoot == "" {
		return false
	}
	// User-global homes are never project-scoped.
	if home, err := os.UserHomeDir(); err == nil {
		home = Canonicalize(home)
		globalRoots := []string{
			filepath.Join(home, ".aicli"),
			filepath.Join(home, ".config", "aicli"),
		}
		for _, g := range globalRoots {
			if pathIsUnder(path, g) {
				return false
			}
		}
	}
	return pathIsUnder(path, projectRoot)
}

func scanRoots(cwd string) []string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		if abs, err := os.Getwd(); err == nil {
			cwd = abs
		}
	}
	if cwd == "" {
		return nil
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	cwd = filepath.Clean(cwd)
	roots := []string{cwd}
	if gitRoot := findGitRoot(cwd); gitRoot != "" && !samePath(gitRoot, cwd) {
		roots = append(roots, gitRoot)
	}
	return roots
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	// Permission / I/O uncertainty: treat as present (fail closed for gating).
	return true
}

func directoryNonEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		return true
	}
	if !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, e := range entries {
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		return true
	}
	return false
}

func directoryHasPluginChild(parent string) bool {
	info, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		return true
	}
	if !info.IsDir() {
		return false
	}
	// Parent itself may be a single-plugin root (plugin.yaml present).
	for _, name := range []string{"plugin.yaml", "plugin.yml", "plugin.json"} {
		if pathExists(filepath.Join(parent, name)) {
			return true
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(parent, e.Name())
		for _, name := range []string{"plugin.yaml", "plugin.yml", "plugin.json"} {
			if pathExists(filepath.Join(child, name)) {
				return true
			}
		}
	}
	return false
}
