package aiclipaths

import (
	"os"
	"path/filepath"
	"strings"
	"time"
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

// DatePartition returns year/month/day path segments for t in local time.
// Zero times fall back to time.Now() so callers always get a usable partition.
func DatePartition(t time.Time) (year, month, day string) {
	if t.IsZero() {
		t = time.Now()
	}
	t = t.Local()
	return t.Format("2006"), t.Format("01"), t.Format("02")
}

// JoinDatePartition joins root/YYYY/MM/DD and optional trailing path elements.
// This mirrors Codex's sessions/YYYY/MM/DD layout for easier filesystem browsing.
func JoinDatePartition(root string, t time.Time, elems ...string) string {
	year, month, day := DatePartition(t)
	parts := make([]string, 0, 4+len(elems))
	parts = append(parts, root, year, month, day)
	parts = append(parts, elems...)
	return filepath.Join(parts...)
}

// ParseTimestampedSessionIDTime extracts a local timestamp from common session IDs:
//   - chat log: 20060102_150405.000_<suffix>
//   - file session: session_20060102150405_<suffix>
func ParseTimestampedSessionIDTime(sessionID string) (time.Time, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return time.Time{}, false
	}

	if strings.HasPrefix(sessionID, "session_") {
		rest := strings.TrimPrefix(sessionID, "session_")
		stamp, _, _ := strings.Cut(rest, "_")
		if t, err := time.ParseInLocation("20060102150405", stamp, time.Local); err == nil {
			return t, true
		}
		return time.Time{}, false
	}

	// chat log session ids: 20060102_150405.000_<suffix>
	parts := strings.SplitN(sessionID, "_", 3)
	if len(parts) < 2 {
		return time.Time{}, false
	}
	stamp := parts[0] + "_" + parts[1]
	if t, err := time.ParseInLocation("20060102_150405.000", stamp, time.Local); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("20060102_150405", parts[0]+"_"+strings.Split(parts[1], ".")[0], time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
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
