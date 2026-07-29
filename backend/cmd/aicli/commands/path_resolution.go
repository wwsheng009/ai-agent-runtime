package commands

import (
	"os"
	"path/filepath"
	"strings"

	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
)

// executablePathForTest overrides os.Executable during unit tests so path
// fallback can be exercised without depending on the real test binary location.
var executablePathForTest string

func resolveExistingPathValue(path string, requireDir bool) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "~"+string(filepath.Separator)) || trimmed == "~" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if trimmed == "~" {
				trimmed = home
			} else {
				trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~"+string(filepath.Separator)))
			}
		}
	}

	resolved := runtimeserver.ResolveUpwardPath(trimmed)
	if accepted := acceptExistingResolvedPath(resolved, requireDir); accepted != "" {
		return accepted
	}

	// Relative optional configs such as backend/configs/runtime.yaml are often
	// repo-layout paths. When aicli is launched from outside the repo, CWD
	// upward search misses them even though the binary still lives in-tree.
	// Search upward from the executable directory as a second anchor.
	if !filepath.IsAbs(trimmed) {
		if exeDir := currentExecutableDir(); exeDir != "" {
			if accepted := resolveExistingPathFromBase(trimmed, exeDir, requireDir); accepted != "" {
				return accepted
			}
		}
	}
	return ""
}

func currentExecutableDir() string {
	exe := strings.TrimSpace(executablePathForTest)
	if exe == "" {
		resolved, err := os.Executable()
		if err != nil {
			return ""
		}
		exe = strings.TrimSpace(resolved)
	}
	if exe == "" {
		return ""
	}
	dir := filepath.Dir(exe)
	if evaluated, err := filepath.EvalSymlinks(dir); err == nil && strings.TrimSpace(evaluated) != "" {
		dir = evaluated
	}
	return dir
}

func resolveExistingPathFromBase(path, baseDir string, requireDir bool) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	baseDir = strings.TrimSpace(baseDir)
	if cleaned == "" || cleaned == "." || baseDir == "" {
		return ""
	}
	for dir := baseDir; dir != ""; {
		candidate := filepath.Join(dir, cleaned)
		if accepted := acceptExistingResolvedPath(candidate, requireDir); accepted != "" {
			return accepted
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func acceptExistingResolvedPath(resolved string, requireDir bool) string {
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ""
	}
	if requireDir && !info.IsDir() {
		return ""
	}
	if !requireDir && info.IsDir() {
		return ""
	}
	if !filepath.IsAbs(resolved) {
		if abs, err := filepath.Abs(resolved); err == nil && strings.TrimSpace(abs) != "" {
			resolved = abs
		}
	}
	return resolved
}
