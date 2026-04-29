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

func defaultAICLIDir(name string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Join(".", ".aicli", name)
	}
	return filepath.Join(homeDir, ".aicli", name)
}
