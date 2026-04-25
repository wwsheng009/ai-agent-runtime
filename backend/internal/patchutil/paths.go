package patchutil

import "strings"

const (
	codexUpdateFilePrefix = "*** Update File: "
	codexAddFilePrefix    = "*** Add File: "
	codexDeleteFilePrefix = "*** Delete File: "
	codexMoveToPrefix     = "*** Move to: "
)

// ExtractPaths returns file paths referenced by either a unified diff or the
// Codex apply_patch format.
func ExtractPaths(patch string) []string {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil
	}

	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	paths := make([]string, 0, 4)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "diff --git "):
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if path := normalizePath(strings.TrimPrefix(strings.TrimPrefix(parts[3], "b/"), "a/")); path != "" {
					paths = append(paths, path)
				}
			}
		case strings.HasPrefix(line, "+++ "):
			if path := normalizePath(strings.TrimSpace(strings.TrimPrefix(line, "+++ "))); path != "" {
				paths = append(paths, path)
			}
		case strings.HasPrefix(line, codexUpdateFilePrefix):
			if path := normalizePath(strings.TrimSpace(strings.TrimPrefix(line, codexUpdateFilePrefix))); path != "" {
				paths = append(paths, path)
			}
		case strings.HasPrefix(line, codexAddFilePrefix):
			if path := normalizePath(strings.TrimSpace(strings.TrimPrefix(line, codexAddFilePrefix))); path != "" {
				paths = append(paths, path)
			}
		case strings.HasPrefix(line, codexDeleteFilePrefix):
			if path := normalizePath(strings.TrimSpace(strings.TrimPrefix(line, codexDeleteFilePrefix))); path != "" {
				paths = append(paths, path)
			}
		case strings.HasPrefix(line, codexMoveToPrefix):
			if path := normalizePath(strings.TrimSpace(strings.TrimPrefix(line, codexMoveToPrefix))); path != "" {
				paths = append(paths, path)
			}
		}
	}

	return dedupe(paths)
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"`)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	if path == "" || path == "/dev/null" {
		return ""
	}
	return path
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
