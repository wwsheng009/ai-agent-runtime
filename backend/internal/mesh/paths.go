// Package mesh implements the local multi-process mesh: node records, session
// bindings, leases, the journal and the aggregation views that back the
// `aicli-mesh` CLI and the per-node `/web/api/mesh/*` control plane.
//
// Two deliberate differences from internal/aiclipaths (documented here so the
// next reader does not "fix" them by accident):
//
//  1. The mesh honours AICLI_HOME (same home root as workspaceregistry /
//     foldertrust / plugins), while aiclipaths.defaultAICLIDir only looks at
//     os.UserHomeDir().
//  2. When no home can be resolved the mesh is fail-closed: reads return an
//     empty view and writes are silently skipped. It never falls back to a
//     CWD-relative directory (aiclipaths.defaultAICLIDir does exactly that
//     with its "./.aicli/<name>" fallback, which the mesh must not inherit).
//
// The package only uses the standard library on purpose: it is the shared
// foundation for both the aicli process and the standalone aicli-mesh tool.
package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables that steer mesh path resolution.
const (
	// EnvMeshDir overrides the mesh root directory (tests, multiple homes).
	EnvMeshDir = "AICLI_MESH_DIR"
	// EnvHome is the shared AICLI home root honoured by workspaceregistry.
	EnvHome = "AICLI_HOME"
)

// SchemaVersion is the record structure version written by this build
// (node records, session bindings, leases). Readers treat unknown major
// versions as `unknown` and never rewrite them.
const SchemaVersion = 2

// Sub-directory names under the mesh root (see architecture §2.2).
const (
	dirNodes    = "nodes"
	dirBindings = "bindings"
	dirLeases   = "leases"
	dirJournal  = "journal"
)

// Path source labels reported by ResolvePaths.
const (
	PathSourceEnv       = "env"
	PathSourceAICLIHome = "aicli-home"
	PathSourceUserHome  = "user-home"
	PathSourceUnset     = "unset"
)

// Paths is the resolved mesh directory layout. A zero Root means the mesh is
// fail-closed: nothing can be read or written and every path helper returns "".
type Paths struct {
	Root     string
	Nodes    string
	Bindings string
	Leases   string
	Journal  string
	// Source records which rule produced Root (env / aicli-home / user-home /
	// unset) for diagnostics (doctor, self view).
	Source string
}

// ResolvePaths resolves the mesh root directory, highest priority first:
//
//  1. AICLI_MESH_DIR (explicit override, used by tests)
//  2. AICLI_HOME + /mesh (shared home root convention)
//  3. os.UserHomeDir() + /.aicli/mesh
//  4. no home: fail-closed (Root == "")
//
// The environment is read on every call so tests can flip it with t.Setenv.
func ResolvePaths() Paths {
	if override := strings.TrimSpace(os.Getenv(EnvMeshDir)); override != "" {
		return pathsFor(override, PathSourceEnv)
	}
	if home := strings.TrimSpace(os.Getenv(EnvHome)); home != "" {
		if expanded := ExpandUserPath(home); expanded != "" {
			return pathsFor(filepath.Join(expanded, "mesh"), PathSourceAICLIHome)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return pathsFor(filepath.Join(home, ".aicli", "mesh"), PathSourceUserHome)
	}
	return Paths{Source: PathSourceUnset}
}

// ExpandUserPath expands a leading "~" to the current user's home directory.
// It mirrors aiclipaths.ExpandUserPath (current-user forms only) and is
// duplicated here so internal/mesh keeps its standard-library-only surface.
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

func pathsFor(root, source string) Paths {
	root = filepath.Clean(root)
	return Paths{
		Root:     root,
		Nodes:    filepath.Join(root, dirNodes),
		Bindings: filepath.Join(root, dirBindings),
		Leases:   filepath.Join(root, dirLeases),
		Journal:  filepath.Join(root, dirJournal),
		Source:   source,
	}
}

// Enabled reports whether the mesh has a usable root directory. When false the
// caller must skip every read and write (fail-closed, never a CWD fallback).
func (p Paths) Enabled() bool {
	return strings.TrimSpace(p.Root) != ""
}

// EnsureDirs creates the four mesh sub-directories (0700). It is a no-op for
// an unresolved (fail-closed) layout and never fails because of an
// unavailable permission bit (Windows, see architecture §9.6).
func (p Paths) EnsureDirs() error {
	if !p.Enabled() {
		return nil
	}
	for _, dir := range []string{p.Nodes, p.Bindings, p.Leases, p.Journal} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mesh: create %s: %w", dir, err)
		}
		// Best effort: os.Chmod on Windows only toggles the read-only bit.
		_ = os.Chmod(dir, 0o700)
	}
	return nil
}

// NodePath returns mesh/nodes/<node_id>.json ("" when unavailable/invalid).
func (p Paths) NodePath(nodeID string) string {
	return p.filePath(p.Nodes, nodeID, ".json")
}

// BindingPath returns mesh/bindings/<session_id>.json.
func (p Paths) BindingPath(sessionID string) string {
	return p.filePath(p.Bindings, sessionID, ".json")
}

// JournalPath returns mesh/journal/<node_id>.ndjson.
func (p Paths) JournalPath(nodeID string) string {
	return p.filePath(p.Journal, nodeID, ".ndjson")
}

// LeasePath returns mesh/leases/<purpose>-<key>.lock.
func (p Paths) LeasePath(purpose, key string) string {
	if !p.Enabled() {
		return ""
	}
	sanitizedPurpose, err := SanitizeKey(purpose)
	if err != nil {
		return ""
	}
	sanitizedKey, err := SanitizeKey(key)
	if err != nil {
		return ""
	}
	return filepath.Join(p.Leases, sanitizedPurpose+"-"+sanitizedKey+".lock")
}

func (p Paths) filePath(dir, key, suffix string) string {
	if !p.Enabled() {
		return ""
	}
	sanitized, err := SanitizeKey(key)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, sanitized+suffix)
}

// SanitizeKey normalises an identifier (node id, session id, lease key) into a
// safe file-name fragment: only [A-Za-z0-9._-] survive, every other rune
// becomes '_'. Empty strings, "." and ".." are rejected outright.
//
// The rule mirrors commands.sanitizeChatWebPortSessionID (the record store it
// replaces), so existing session ids keep resolving to the same file name.
func SanitizeKey(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("mesh: empty key")
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	sanitized := b.String()
	if sanitized == "." || sanitized == ".." {
		return "", fmt.Errorf("mesh: unsafe key %q", raw)
	}
	return sanitized, nil
}
