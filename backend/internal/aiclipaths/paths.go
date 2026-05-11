package aiclipaths

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultSessionsDir returns the default persisted chat session directory.
func DefaultSessionsDir() string {
	return defaultAICLIDir("sessions")
}

// DefaultChatLogsDir returns the default persisted chat log directory.
func DefaultChatLogsDir() string {
	return defaultAICLIDir("chat-logs")
}

// DefaultLogsDir returns the default global log directory (~/.aicli/logs).
func DefaultLogsDir() string {
	return defaultAICLIDir("logs")
}

// ExpandUserPath expands a leading "~" to the current user's home directory.
// It intentionally only supports the current-user forms "~", "~/..." and "~\...".
func ExpandUserPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return filepath.Clean(path)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(path)
	}
	if path == "~" {
		return filepath.Clean(homeDir)
	}
	return filepath.Join(homeDir, strings.TrimLeft(path[2:], "/\\"))
}

func defaultAICLIDir(name string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Join(".", ".aicli", name)
	}
	return filepath.Join(homeDir, ".aicli", name)
}
