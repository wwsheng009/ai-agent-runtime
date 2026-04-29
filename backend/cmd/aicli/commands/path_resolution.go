package commands

import (
	"os"
	"path/filepath"
	"strings"

	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
)

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
	return resolved
}
