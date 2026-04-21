package executor

import (
	"path/filepath"
	"runtime"
	"strings"
)

// NormalizePathForComparison canonicalises a path for cross-platform comparison.
// On Windows it lowercases the path and converts to forward slashes so that
// E:\Foo and e:/foo compare equal.
func NormalizePathForComparison(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(filepath.ToSlash(p))
	}
	return p
}

// FilterSensitiveEnv removes environment variables whose names contain
// sensitive keywords (KEY, SECRET, TOKEN), mirroring the default-excludes
// logic from codex-rs/core/src/exec_env.rs.
//
// This is applied on top of any sandbox EnvWhitelist filtering.
func FilterSensitiveEnv(env []string) []string {
	keywords := []string{"KEY", "SECRET", "TOKEN"}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 0 {
			continue
		}
		upper := strings.ToUpper(parts[0])
		skip := false
		for _, kw := range keywords {
			if strings.Contains(upper, kw) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// BuildFilteredEnv constructs the environment for a child process.
//   - If sandbox is active, uses its EnvWhitelist (plus sensitive-var filtering).
//   - If sandbox is nil/inactive, inherits the full parent env with sensitive
//     vars stripped (matching codex-rs default "inherit all + exclude sensitive"
//     policy).
func BuildFilteredEnv(sandbox *Sandbox, parentEnv []string) []string {
	if sandbox != nil && sandbox.active() && len(sandbox.Config().EnvWhitelist) > 0 {
		// Sandbox whitelist already limits vars, but still strip sensitive ones
		return FilterSensitiveEnv(sandbox.FilterEnv(parentEnv))
	}
	return FilterSensitiveEnv(parentEnv)
}

// IsWindows returns true when running on Windows.
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

// GoOS returns the current runtime.GOOS value.
func GoOS() string {
	return runtime.GOOS
}
