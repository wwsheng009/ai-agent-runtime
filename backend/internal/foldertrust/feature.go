package foldertrust

import (
	"os"
	"strings"
)

// EnvFolderTrust is the runtime escape hatch for the folder-trust gate.
// Values: 1/true/on/yes → enabled; 0/false/off/no → disabled; unset → disabled.
//
// Default is OFF so existing installs keep prior behavior until operators opt in.
// Release productization may later flip the default; keep this env as the kill-switch.
const EnvFolderTrust = "AICLI_FOLDER_TRUST"

// FeatureEnabled reports whether the folder-trust gate is active for this process.
// Empty / unknown values keep the gate OFF (safe default).
func FeatureEnabled() bool {
	return FeatureEnabledFromEnv(os.Getenv(EnvFolderTrust))
}

// FeatureEnabledFromEnv parses an explicit env value (test seam).
func FeatureEnabledFromEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}
