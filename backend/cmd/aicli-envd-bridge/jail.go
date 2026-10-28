package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

// resolveScript canonicalizes a path on the target before the bridge touches it.
//
// The lexical jail in resolve() cannot see a symlink: a link inside the
// workspace can point at /etc, and envd will follow it. The script resolves the
// path on the target instead. `realpath -m` is used when available because it
// also canonicalizes paths whose last components do not exist yet (write/mkdir).
// The fallback resolves the nearest existing ancestor with readlink -f and
// re-attaches the missing suffix.
//
// The target path is passed as "$1"; it is never interpolated into the script.
const resolveScript = `set -e
target=$1
if command -v realpath >/dev/null 2>&1; then
  if out=$(realpath -m -- "$target" 2>/dev/null); then
    printf '%s\n' "$out"
    exit 0
  fi
fi
suffix=
cur=$target
while [ "$cur" != "/" ] && [ "$cur" != "." ] && [ ! -e "$cur" ] && [ ! -L "$cur" ]; do
  suffix="/$(basename "$cur")$suffix"
  cur=$(dirname "$cur")
done
if [ -L "$cur" ] && [ ! -e "$cur" ]; then
  # A dangling symlink can point outside; readlink -f may refuse it, so fail
  # closed rather than treating the link path as a missing component.
  printf 'cannot resolve dangling symlink %s\n' "$cur" >&2
  exit 1
elif [ -e "$cur" ]; then
  resolved=$(readlink -f "$cur" 2>/dev/null) || {
    printf 'cannot resolve %s\n' "$target" >&2
    exit 1
  }
else
  resolved=$cur
fi
case "$resolved" in
  /) ;;
  */) resolved=${resolved%/} ;;
esac
printf '%s%s\n' "$resolved" "$suffix"
`

// resolveRemoteCanonical resolves one absolute path on the target.
//
// It fails closed: a non-zero exit, empty output or relative result is an error,
// not a reason to fall back to the lexical path.
func (b *bridge) resolveRemoteCanonical(ctx context.Context, target string) (string, error) {
	if b == nil || b.client == nil {
		return "", fmt.Errorf("envd client is not configured")
	}
	stdout, stderr, code, err := b.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", resolveScript, "aicli-envd-resolve", target},
			Cwd:  b.workspace,
		},
	})
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", target, err)
	}
	if code != 0 {
		return "", fmt.Errorf("resolve %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	resolved := strings.TrimSpace(string(stdout))
	if resolved == "" || !path.IsAbs(resolved) {
		return "", fmt.Errorf("resolve %s returned %q, want an absolute path", target, resolved)
	}
	if strings.ContainsAny(resolved, "\x00\r\n") {
		return "", fmt.Errorf("resolve %s returned an unusable path", target)
	}
	return path.Clean(resolved), nil
}

// isInsideJail reports whether a canonical path is inside the workspace or one
// of the configured allowed roots.
func (b *bridge) isInsideJail(candidate string) bool {
	candidate = path.Clean(candidate)
	allowed := append([]string{b.workspace}, b.roots...)
	for _, root := range allowed {
		cleanRoot := path.Clean(root)
		if cleanRoot == "" {
			continue
		}
		if candidate == cleanRoot || strings.HasPrefix(candidate, cleanRoot+"/") {
			return true
		}
	}
	return false
}

// isProtectedRoot reports whether a path is one of the jail roots themselves.
// Removing or renaming a root is never a file-level operation.
func (b *bridge) isProtectedRoot(candidate string) bool {
	candidate = path.Clean(candidate)
	for _, root := range append([]string{b.workspace}, b.roots...) {
		if candidate == path.Clean(root) {
			return true
		}
	}
	return false
}

// resolvePath applies the lexical jail and then canonicalizes the path on the
// target. The canonical path is what callers must use for the actual operation,
// so a symlink cannot redirect a second lookup.
func (b *bridge) resolvePath(ctx context.Context, raw string) (string, error) {
	lexical, err := b.resolve(raw)
	if err != nil {
		return "", err
	}
	resolved, err := b.resolveRemoteCanonical(ctx, lexical)
	if err != nil {
		return "", err
	}
	if !b.isInsideJail(resolved) {
		return "", fmt.Errorf("path %q resolves to %q, outside the workspace %q and the allowed roots", raw, resolved, b.workspace)
	}
	return resolved, nil
}

// resolveMutationPath prepares a path for unlink or rename.
//
// The final component is deliberately not followed: removing or renaming a
// symlink acts on the link itself. The parent is resolved and checked, because an
// intermediate symlink (workspace/link/file) would otherwise redirect the
// operation outside the jail.
func (b *bridge) resolveMutationPath(ctx context.Context, raw string) (string, error) {
	lexical, err := b.resolve(raw)
	if err != nil {
		return "", err
	}
	if b.isProtectedRoot(lexical) {
		return "", fmt.Errorf("refusing to mutate protected root %q", lexical)
	}
	parent := path.Dir(lexical)
	base := path.Base(lexical)
	resolvedParent, err := b.resolveRemoteCanonical(ctx, parent)
	if err != nil {
		return "", err
	}
	if !b.isInsideJail(resolvedParent) {
		return "", fmt.Errorf("path %q resolves through %q, outside the workspace %q and the allowed roots", raw, resolvedParent, b.workspace)
	}
	target := path.Join(resolvedParent, base)
	if !b.isInsideJail(target) {
		return "", fmt.Errorf("path %q resolves to %q, outside the workspace %q and the allowed roots", raw, target, b.workspace)
	}
	return target, nil
}
