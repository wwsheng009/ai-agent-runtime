package aiclipaths

import (
	"os"
	"path/filepath"
	"strings"
)

// IsExplicitRuntimeConfigOverride reports whether a configured
// skills_runtime.config_file value names a deliberate runtime.yaml override:
// a non-empty, non-convention path (custom filename or location). An empty
// value — or one of the legacy convention layouts (runtime.yaml,
// configs/runtime.yaml, backend/configs/runtime.yaml, ".") — is NOT an
// override: those fall through to the .aicli layer auto-discovery in
// ResolveRuntimeConfigBootstrapPath.
//
// Callers must use this predicate instead of testing the raw string for
// non-emptiness. A `config_file != ""` gate misclassifies "pointer deleted /
// never written" as "explicit override", which has repeatedly flipped
// discovery/loading branches off for the common no-override configuration
// (see: skills user-root discovery, runtime tool config path, chat
// persistence runtime config — all fixed for this exact failure mode).
func IsExplicitRuntimeConfigOverride(configPath string) bool {
	trimmed := strings.TrimSpace(expandExplicitConfigPath(configPath))
	if trimmed == "" {
		return false
	}
	return !IsRuntimeConfigConventionPath(trimmed)
}

// ResolveRuntimeConfigAnchorPath returns the anchor directory a runtime.yaml
// derived discovery should scan from. When an explicit override is configured
// its directory is used; otherwise the process CWD (falling back to the
// convention layer candidates' directories when CWD is unavailable). The
// returned value is "" only when neither CWD nor a home directory can be
// resolved.
//
// Discovery callers (skills roots, watch candidates) pass this to
// DiscoverCodexCompatibleSkillDirs so the anchor semantics stay identical
// across chat / runtime-server / hot-reload paths instead of each caller
// re-deriving "config dir vs CWD" by hand.
func ResolveRuntimeConfigAnchorPath(configPath string) string {
	if IsExplicitRuntimeConfigOverride(configPath) {
		resolved := expandExplicitConfigPath(configPath)
		if absolute, err := filepath.Abs(resolved); err == nil && strings.TrimSpace(absolute) != "" {
			resolved = absolute
		}
		return filepath.Dir(resolved)
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Clean(cwd)
	}
	// CWD unavailable: prefer the layers ResolveRuntimeConfigBootstrapPath would
	// have used, so behavior degrades deterministically instead of silently
	// scanning the wrong tree.
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Clean(home)
	}
	return ""
}

// ResolveRuntimeConfigFilePath resolves the skills_runtime.config_file pointer
// the same way every consumer does: explicit override wins (kept even when the
// file is missing so callers can report it); otherwise the .aicli layer stack
// (./.aicli/runtime.yaml → ~/.aicli/runtime.yaml); "" when nothing exists.
//
// This is the shared body behind chat's resolveGlobalRuntimeConfigPath minus
// the chat-specific resolveExistingPathValue widening (upward/executable-dir
// search), which stays a chat-layer concern.
func ResolveRuntimeConfigFilePath(configPath string) string {
	return ResolveRuntimeConfigBootstrapPath(expandExplicitConfigPath(configPath))
}
