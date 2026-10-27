package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
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
//   - Prepends the resolver-selected ripgrep directory to PATH so shell
//     children can invoke plain `rg` even when it is only available via
//     AICLI_RG_PATH or the release-bundled codex-path copy.
func BuildFilteredEnv(sandbox *Sandbox, parentEnv []string) []string {
	if sandbox != nil && sandbox.active() && len(sandbox.Config().EnvWhitelist) > 0 {
		// Sandbox whitelist already limits vars, but still strip sensitive ones
		return WithResolvedRipgrepPath(FilterSensitiveEnv(sandbox.FilterEnv(parentEnv)))
	}
	return WithResolvedRipgrepPath(FilterSensitiveEnv(parentEnv))
}

// PrependPathDir returns a copy of env with dir prepended to PATH. It is a
// no-op when dir is empty or already present in PATH (case-insensitive on
// Windows); when env carries no PATH entry at all, the entry is appended.
func PrependPathDir(env []string, dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	isPathKey := func(key string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(key, "PATH")
		}
		return key == "PATH"
	}
	prepared := make([]string, 0, len(env)+1)
	found := false
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || !isPathKey(parts[0]) {
			prepared = append(prepared, entry)
			continue
		}
		found = true
		if pathListContainsDir(parts[1], dir) {
			return env
		}
		if strings.TrimSpace(parts[1]) == "" {
			prepared = append(prepared, parts[0]+"="+dir)
			continue
		}
		prepared = append(prepared, parts[0]+"="+dir+string(os.PathListSeparator)+parts[1])
	}
	if !found {
		prepared = append(prepared, "PATH="+dir)
	}
	return prepared
}

func pathListContainsDir(pathValue, dir string) bool {
	for _, entry := range filepath.SplitList(pathValue) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			if strings.EqualFold(filepath.Clean(entry), filepath.Clean(dir)) {
				return true
			}
			continue
		}
		if filepath.Clean(entry) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// NeutralizeConsoleStylingEnv forces the child process into a non-interactive,
// unstyled baseline so its captured output is safe for model consumption.
//
// This mirrors Codex's UNIFIED_EXEC_ENV approach (codex-rs/core/src/
// unified_exec/process_manager.rs): prevent console styling at the source by
// injecting a fixed environment, instead of post-hoc scrubbing ANSI bytes from
// already-captured output. The captured bytes are what the model reads, so
// killing color/pager chrome upstream keeps the model input clean while
// preserving UTF-8 (PowerShell's [Console]::OutputEncoding directive is owned
// by the shell payload and is unaffected here).
//
// Mirroring Codex:
//   - NO_COLOR=1: honour the no-color.org convention, most CLIs then skip color.
//   - TERM=dumb: tell programs they drive a "dumb" terminal, disabling color,
//     cursor movement and other terminal capabilities.
//   - COLORTERM="": remove the true-color capability hint so 24-bit escapes are
//     never emitted.
//   - PAGER=cat / GIT_PAGER=cat / GH_PAGER=cat: replace less-style pagers (which
//     add reverse-video status lines / progress chrome) with a straight pass-through.
//
// Clearing CLICOLOR / CLICOLOR_FORCE prevents the BSD "color if on a tty" family
// from emitting color into a captured pipe.
func NeutralizeConsoleStylingEnv(env []string) []string {
	// Forced values. Empty value means "clear this variable" so callers cannot
	// re-enable styling by inheriting a host value.
	neutralized := []struct {
		key   string
		value string
	}{
		{"NO_COLOR", "1"},
		{"TERM", "dumb"},
		{"COLORTERM", ""},
		{"PAGER", "cat"},
		{"GIT_PAGER", "cat"},
		{"GH_PAGER", "cat"},
		{"CLICOLOR", ""},
		{"CLICOLOR_FORCE", ""},
	}
	// Seed from the input, dropping any entry whose key collides with a forced
	// variable. Case-insensitive on Windows, exact elsewhere (os/exec semantics).
	normalized := make([]string, 0, len(env)+len(neutralized))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 0 {
			continue
		}
		skip := false
		for _, n := range neutralized {
			if runtime.GOOS == "windows" {
				if strings.EqualFold(parts[0], n.key) {
					skip = true
					break
				}
			} else if parts[0] == n.key {
				skip = true
				break
			}
		}
		if !skip {
			normalized = append(normalized, entry)
		}
	}
	for _, n := range neutralized {
		normalized = append(normalized, n.key+"="+n.value)
	}
	return normalized
}

// WithResolvedRipgrepPath prepends the resolver-selected ripgrep directory to
// PATH so child shells can run plain `rg` even when the binary is not on the
// parent PATH. It is a no-op when rg is unresolvable, the resolved file is not
// canonically named rg/rg.exe, or its directory is already on PATH.
func WithResolvedRipgrepPath(env []string) []string {
	dir, ok := runtimeripgrep.ShellPrependDir()
	if !ok {
		return env
	}
	return PrependPathDir(env, dir)
}

// IsWindows returns true when running on Windows.
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

// GoOS returns the current runtime.GOOS value.
func GoOS() string {
	return runtime.GOOS
}
