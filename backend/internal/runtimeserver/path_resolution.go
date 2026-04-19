package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
)

func ResolveUpwardPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}

	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	if pathExists(cleaned) {
		return cleaned
	}

	relative := strings.TrimPrefix(cleaned, "."+string(filepath.Separator))
	if relative == "." || relative == "" {
		return cleaned
	}

	cwd, err := os.Getwd()
	if err != nil {
		return cleaned
	}

	for dir := cwd; dir != ""; {
		candidate := filepath.Join(dir, relative)
		if pathExists(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return cleaned
}

func ResolveUpwardPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		resolved = append(resolved, ResolveUpwardPath(path))
	}
	return resolved
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
