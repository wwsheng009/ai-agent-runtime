package remotetools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Default output budgets. They mirror the local runtime contract:
//
//   - executor.DefaultRetainedOutputBytes (256 KiB) bounds one capture;
//   - output.ModelToolTextByteBudget (~12 KiB) bounds what the model sees.
//
// A remote call has no artifact store to fall back on, so the model-visible
// window is what it is: the server folds the payload and reports the truncation
// explicitly instead of silently dropping the middle (design §D6).
const (
	DefaultMaxOutputBytes  = 256 * 1024
	DefaultMaxBytesPerCall = 2 * 1024 * 1024
	// DefaultModelVisibleCap is the remote trust-boundary ceiling. It matches
	// the local output core ceiling; lowering it is allowed, raising it is not.
	DefaultModelVisibleCap = 64 * 1024
	// DefaultRemoteOutputHardCap mirrors toolcore/output.
	DefaultRemoteOutputHardCap = 64 * 1024
)

// Error codes are stable strings so the local side can branch on them instead
// of parsing prose (design §5.4 "error codes must match across transports").
const (
	CodeInvalidArgument   = "INVALID_ARGUMENT"
	CodePathDenied        = "PATH_DENIED"
	CodeNotFound          = "NOT_FOUND"
	CodeOutputLimit       = "OUTPUT_LIMIT_EXCEEDED"
	CodeTimeout           = "TIMEOUT"
	CodeUnsupported       = "UNSUPPORTED"
	CodeCommandDenied     = "COMMAND_DENIED"
	CodeCommandFailed     = "COMMAND_FAILED"
	// CodeCommandOutcomeUnknown marks a command whose result could not be
	// recorded (for example a job orphaned by a process restart). The captured
	// output may be complete, so this is not the same thing as a failure.
	CodeCommandOutcomeUnknown = "COMMAND_OUTCOME_UNKNOWN"
	CodeInternal          = "INTERNAL_ERROR"
	CodeWorkspaceDisabled = "WORKSPACE_UNAVAILABLE"
)

// ToolError is a model-visible, classified failure.
//
// It is returned as a normal MCP tool result (IsError=true) rather than a
// protocol error so the model can read the reason and self-correct.
type ToolError struct {
	Code       string
	Message    string
	NextAction string
	Retriable  bool
}

func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	if e.NextAction == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (next: %s)", e.Code, e.Message, e.NextAction)
}

// newToolError builds a classified error.
func newToolError(code, nextAction string, format string, args ...interface{}) *ToolError {
	return &ToolError{
		Code:       code,
		Message:    fmt.Sprintf(format, args...),
		NextAction: nextAction,
		Retriable:  code == CodeTimeout,
	}
}

// Workspace resolves tool paths inside an admitted root set.
//
// Every path goes through the same three steps before any I/O:
//
//	clean → join against the workspace → verify inside an allowed root
//
// and is then re-verified after symlink resolution, because a clean path can
// still escape through a link. The local executor.Sandbox does the same two-phase
// check; this package repeats it so the remote side does not depend on the local
// policy object (and so the remote process cannot be reconfigured by the client).
type Workspace struct {
	root         string
	allowedRoots []string
	platform     Platform
}

// NewWorkspace builds a resolver over the configured roots.
func NewWorkspace(workspace string, allowedRoots []string) (*Workspace, error) {
	root := strings.TrimSpace(workspace)
	if root == "" {
		return nil, newToolError(CodeWorkspaceDisabled, "configure --workspace", "workspace is not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, newToolError(CodeWorkspaceDisabled, "check the workspace path", "cannot resolve workspace %q: %v", root, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return nil, newToolError(CodeWorkspaceDisabled, "create or fix the workspace directory", "workspace %q is not accessible: %v", abs, err)
	}
	if !info.IsDir() {
		return nil, newToolError(CodeWorkspaceDisabled, "point --workspace at a directory", "workspace %q is not a directory", abs)
	}

	roots := make([]string, 0, len(allowedRoots)+1)
	seen := map[string]bool{abs: true}
	roots = append(roots, abs)
	for _, candidate := range allowedRoots {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		candidateAbs, absErr := filepath.Abs(trimmed)
		if absErr != nil {
			return nil, newToolError(CodeWorkspaceDisabled, "fix allowedRoots", "cannot resolve allowed root %q: %v", trimmed, absErr)
		}
		candidateAbs = filepath.Clean(candidateAbs)
		if seen[candidateAbs] {
			continue
		}
		seen[candidateAbs] = true
		roots = append(roots, candidateAbs)
	}
	return &Workspace{root: abs, allowedRoots: roots, platform: currentPlatform()}, nil
}

// Root returns the workspace root (the base for relative paths).
func (w *Workspace) Root() string {
	if w == nil {
		return ""
	}
	return w.root
}

// AllowedRoots returns a copy of the admitted roots.
func (w *Workspace) AllowedRoots() []string {
	if w == nil {
		return nil
	}
	return append([]string(nil), w.allowedRoots...)
}

// Resolve maps a tool path argument to an absolute path inside the jail.
//
// An empty argument resolves to the workspace root itself (ls with no path).
// Absolute paths are accepted only when they already sit inside an admitted
// root; relative paths are anchored to the workspace, never to the process CWD.
func (w *Workspace) Resolve(raw string) (string, error) {
	if w == nil {
		return "", newToolError(CodeWorkspaceDisabled, "configure --workspace", "workspace is not configured")
	}
	// Device namespaces are checked on the RAW spelling, before normalization:
	// normalizePath strips the `\\?\` prefix (so the jail compares against the
	// filesystem's own spelling), which would erase the very marker this guard
	// looks for. Order matters here, not just the check itself.
	if w.platform.Windows {
		if err := rejectWindowsDevicePath(raw); err != nil {
			return "", err
		}
	}
	trimmed := w.platform.normalizePath(raw)
	var target string
	switch {
	case trimmed == "":
		target = w.root
	case filepath.IsAbs(trimmed):
		target = filepath.Clean(trimmed)
	default:
		target = filepath.Clean(filepath.Join(w.root, trimmed))
	}

	if err := w.ensureInside(target); err != nil {
		return "", err
	}
	// Second phase: a symlink inside the workspace may still point outside it.
	// Resolve the deepest existing ancestor and re-check that form. A path that
	// does not exist yet (write target) keeps its lexical value, matching the
	// local sandbox's behavior.
	resolved := resolveExistingAncestor(target)
	if resolved != target {
		if err := w.ensureInside(resolved); err != nil {
			return "", err
		}
	}
	return target, nil
}

// ResolveFrom resolves raw against an explicit base directory instead of the
// workspace root, which is what a per-call workdir needs.
//
// The base is resolved first so an out-of-jail base fails with an error that
// names the real cause (PATH_DENIED on the base) rather than a confusing denial
// on a derived path. Absolute raw values bypass the base entirely, matching the
// documented contract that workdir only moves relative anchors.
//
// Both branches end in Resolve, so the device-name guard, the lexical jail check
// and the symlink re-check all still apply. There is deliberately no second,
// weaker code path here.
func (w *Workspace) ResolveFrom(base, raw string) (string, error) {
	if base == "" {
		return w.Resolve(raw)
	}
	baseAbs, err := w.Resolve(base)
	if err != nil {
		return "", err
	}
	// A workdir must be a directory. Without this check the path tools (ls,
	// glob, grep) would accept a file as the base and resolve children under a
	// non-directory, while shell and job_start reject the same value. One
	// parameter must mean one thing across tools.
	if isDir, statErr := isDirectory(baseAbs); statErr != nil {
		return "", newToolError(CodeInvalidArgument, "pass a directory",
			"workdir is not readable: %q: %v", base, statErr)
	} else if !isDir {
		return "", newToolError(CodeInvalidArgument, "pass a directory",
			"workdir is not a directory: %q", base)
	}
	if raw == "" {
		return baseAbs, nil
	}
	if filepath.IsAbs(w.platform.normalizePath(raw)) {
		return w.Resolve(raw)
	}
	return w.Resolve(filepath.Join(baseAbs, raw))
}

// ensureInside verifies an absolute path sits under one of the admitted roots.
func (w *Workspace) ensureInside(target string) error {
	for _, root := range w.allowedRoots {
		if w.platform.pathWithinPlatform(target, root) {
			return nil
		}
	}
	return newToolError(
		CodePathDenied,
		"use a path inside the configured workspace",
		"path %q is outside the allowed roots %v; re-issue the call with a path under one of these roots", target, w.allowedRoots,
	)
}

// resolveExistingAncestor resolves symlinks for the deepest existing prefix of
// path and re-appends the missing tail. It returns the input unchanged when
// nothing resolves, so callers can simply compare.
func resolveExistingAncestor(path string) string {
	current := path
	tail := ""
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if tail == "" {
				return filepath.Clean(resolved)
			}
			return filepath.Clean(filepath.Join(resolved, tail))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return path
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		tail = filepath.Join(filepath.Base(current), tail)
		current = parent
	}
}

// Relative returns the workspace-relative spelling of an absolute path, used in
// tool results so the model sees stable, machine-independent paths.
func (w *Workspace) Relative(abs string) string {
	if w == nil {
		return abs
	}
	rel, err := filepath.Rel(w.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}
