package policy

import (
	"path/filepath"
	"strings"
)

// This file owns the built-in sensitive-path classification used by the
// sensitive-write gate and by the read-only shell secret-argument filter
// (docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.4/§4.7).
//
// The list mirrors CommandCode's sensitive-write classes: secret material,
// persistence vectors, version-control / cloud / IDE control surfaces, and the
// runtime's own configuration (a self-escalation target). Classification is
// deliberately conservative: a false positive costs one confirmation, while a
// false negative silently edits a credential store.

// SensitivePathKind groups the reason shown on the approval prompt.
type SensitivePathKind string

const (
	// SensitivePathSecret marks secret material (environment files, keys,
	// credentials).
	SensitivePathSecret SensitivePathKind = "secret"
	// SensitivePathPersistence marks shell/editor persistence vectors that run
	// on the user's next login or command.
	SensitivePathPersistence SensitivePathKind = "persistence"
	// SensitivePathVCS marks version-control metadata (.git and friends).
	SensitivePathVCS SensitivePathKind = "vcs_metadata"
	// SensitivePathControlPlane marks runtime/tool configuration that can widen
	// this agent's own permissions.
	SensitivePathControlPlane SensitivePathKind = "control_plane"
)

// SensitivePathMatch carries the matched kind and the rule that fired, so
// approval reasons and tests can name it.
type SensitivePathMatch struct {
	Kind    SensitivePathKind
	Pattern string
}

// sensitiveDirSegments maps a directory segment to its category. A match fires
// when any path segment equals the key (case-insensitive).
var sensitiveDirSegments = map[string]SensitivePathKind{
	".git":          SensitivePathVCS,
	".hg":           SensitivePathVCS,
	".svn":          SensitivePathVCS,
	".ssh":          SensitivePathSecret,
	".aws":          SensitivePathSecret,
	".gnupg":        SensitivePathSecret,
	".kube":         SensitivePathSecret,
	".docker":       SensitivePathSecret,
	".vscode":       SensitivePathPersistence,
	".idea":         SensitivePathPersistence,
	".husky":        SensitivePathPersistence,
	".devcontainer": SensitivePathPersistence,
}

// sensitiveFilePatterns maps a lower-case file name rule to its category.
// Suffix rules start with "*"; prefix rules end with "*"; everything else is an
// exact base-name match.
var sensitiveFilePatterns = map[string]SensitivePathKind{
	".env":             SensitivePathSecret,
	".env.*":           SensitivePathSecret,
	".envrc":           SensitivePathSecret,
	"*.pem":            SensitivePathSecret,
	"*.key":            SensitivePathSecret,
	"*.p12":            SensitivePathSecret,
	"*.pfx":            SensitivePathSecret,
	"*.jks":            SensitivePathSecret,
	"*.keystore":       SensitivePathSecret,
	"*.ppk":            SensitivePathSecret,
	"id_rsa*":          SensitivePathSecret,
	"id_ed25519*":      SensitivePathSecret,
	"id_ecdsa*":        SensitivePathSecret,
	"id_dsa*":          SensitivePathSecret,
	"credentials":      SensitivePathSecret,
	"credentials.json": SensitivePathSecret,
	".netrc":           SensitivePathSecret,
	".npmrc":           SensitivePathSecret,
	".pypirc":          SensitivePathSecret,
	".git-credentials": SensitivePathSecret,
	".htpasswd":        SensitivePathSecret,
	".pgpass":          SensitivePathSecret,
	"authorized_keys":  SensitivePathSecret,
	"known_hosts":      SensitivePathSecret,
	".bashrc":          SensitivePathPersistence,
	".zshrc":           SensitivePathPersistence,
	".profile":         SensitivePathPersistence,
	".bash_profile":    SensitivePathPersistence,
	".bash_login":      SensitivePathPersistence,
	".inputrc":         SensitivePathPersistence,
	".gitconfig":       SensitivePathPersistence,
	".gitmodules":      SensitivePathPersistence,
	".mcp.json":        SensitivePathControlPlane,
}

// sensitiveAICLIConfigFiles are the workspace configuration files that steer
// this runtime itself. Writing them is a self-escalation path (permissions,
// grants, MCP servers, profiles), so they are always gated.
var sensitiveAICLIConfigFiles = map[string]bool{
	"permissions.yaml":       true,
	"permissions.yml":        true,
	"permissions.local.yaml": true,
	"permissions.local.yml":  true,
	"grants.json":            true,
	"mcp.yaml":               true,
	"mcp.yml":                true,
	"config.yaml":            true,
	"config.yml":             true,
	"profiles.yaml":          true,
	"presets.yaml":           true,
}

// ClassifySensitivePath reports whether path names a sensitive write/read
// target. Both absolute and relative paths are accepted; the match is
// case-insensitive on every platform so a case-only rename cannot dodge a rule.
func ClassifySensitivePath(path string) (SensitivePathMatch, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return SensitivePathMatch{}, false
	}
	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	normalized = strings.TrimRight(normalized, "/")
	if normalized == "" {
		return SensitivePathMatch{}, false
	}
	lower := strings.ToLower(filepath.ToSlash(normalized))
	segments := strings.Split(lower, "/")
	base := segments[len(segments)-1]

	if match, ok := classifyAICLIControlPlane(lower, segments); ok {
		return match, true
	}
	if strings.Contains(lower, "node_modules/.bin") {
		return SensitivePathMatch{Kind: SensitivePathPersistence, Pattern: "node_modules/.bin"}, true
	}
	for segment, kind := range sensitiveDirSegments {
		for _, candidate := range segments {
			if candidate == segment {
				return SensitivePathMatch{Kind: kind, Pattern: segment}, true
			}
		}
	}
	for pattern, kind := range sensitiveFilePatterns {
		if sensitiveFileMatch(pattern, base) {
			return SensitivePathMatch{Kind: kind, Pattern: pattern}, true
		}
	}
	return SensitivePathMatch{}, false
}

func classifyAICLIControlPlane(lower string, segments []string) (SensitivePathMatch, bool) {
	base := segments[len(segments)-1]
	inAICLIDir := false
	for _, segment := range segments[:len(segments)-1] {
		if segment == ".aicli" {
			inAICLIDir = true
			break
		}
	}
	if inAICLIDir {
		if sensitiveAICLIConfigFiles[base] || strings.HasPrefix(base, "permissions.") {
			return SensitivePathMatch{Kind: SensitivePathControlPlane, Pattern: ".aicli/" + base}, true
		}
	}
	// Any permissions.* file outside .aicli still steers rule loading when it
	// lands in a config lookup path; keep the base-name rule narrow but global.
	switch base {
	case "permissions.yaml", "permissions.yml":
		return SensitivePathMatch{Kind: SensitivePathControlPlane, Pattern: base}, true
	}
	return SensitivePathMatch{}, false
}

func sensitiveFileMatch(pattern, base string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return base == pattern
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	if prefix != "" && !strings.HasPrefix(base, prefix) {
		return false
	}
	if suffix != "" && !strings.HasSuffix(base, suffix) {
		return false
	}
	return len(base) >= len(prefix)+len(suffix)
}

// SensitiveReadArgument reports whether a shell argument names a sensitive
// file, for the read-only shell fast path (a command that reads secrets must
// fall out of the auto-allow table and reach the mode/approval decision).
func SensitiveReadArgument(argument string) bool {
	cleaned := strings.TrimSpace(argument)
	if cleaned == "" || strings.HasPrefix(cleaned, "-") {
		return false
	}
	// Shell variables and globs cannot be resolved statically: treat them as
	// sensitive only when they spell a secret location.
	lower := strings.ToLower(cleaned)
	if strings.Contains(lower, "$home") || strings.Contains(lower, "~") || strings.Contains(lower, "%userprofile%") {
		if _, ok := ClassifySensitivePath(cleaned); ok {
			return true
		}
		for _, secretDir := range []string{".ssh", ".aws", ".gnupg", ".kube"} {
			if strings.Contains(lower, secretDir) {
				return true
			}
		}
	}
	_, ok := ClassifySensitivePath(cleaned)
	return ok
}
