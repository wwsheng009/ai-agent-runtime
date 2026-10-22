package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Roots lets a client narrow the workspace for one MCP session.
//
// MCP's Roots feature lets a client tell a server which directories the session is
// allowed to operate in (spec: client/roots). The security direction is important:
// roots may only NARROW what the server already permits. A client that declares
// `/etc` must not gain access to `/etc`; it simply gets the intersection of its
// declaration and the server's configured roots. This is the opposite of trusting
// client input, which is why the intersection is computed here and not by the
// caller.
//
// P4 status: the transport plumbing (roots/list) is not wired yet, because the
// go-sdk client in this repository declares no roots capability by default and the
// server-side handler runs per request in stateless mode. The narrowing primitive
// below is complete and tested so wiring it later is a transport change only, not a
// security redesign.
type Roots struct {
	base       *Workspace
	requested  []string
	effective  []string
	violations []string
}

// NewRoots intersects the client's declared roots with the server's admitted roots.
//
// Every requested root is resolved relative to the workspace. Roots outside the
// server's allowed set are recorded as violations and ignored; they never widen
// access. An empty declaration means "no narrowing" and keeps the full workspace.
func NewRoots(base *Workspace, requested []string) (*Roots, error) {
	if base == nil {
		return nil, newToolError(CodeWorkspaceDisabled, "configure --workspace", "workspace is not configured")
	}
	roots := &Roots{base: base, requested: append([]string(nil), requested...)}
	if len(requested) == 0 {
		roots.effective = base.AllowedRoots()
		return roots, nil
	}

	accepted := make([]string, 0, len(requested))
	for _, raw := range requested {
		resolved, err := base.Resolve(raw)
		if err != nil {
			// A root the server does not permit is a violation, not a hard error:
			// the session continues with the remaining (permitted) roots, and the
			// caller can surface the list to the model.
			roots.violations = append(roots.violations, fmt.Sprintf("%s: outside the server workspace", raw))
			continue
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.IsDir() {
			roots.violations = append(roots.violations, fmt.Sprintf("%s: not an existing directory", raw))
			continue
		}
		accepted = append(accepted, resolved)
	}
	if len(accepted) == 0 {
		return nil, newToolError(
			CodePathDenied,
			"declare a root inside the server workspace",
			"none of the requested roots are inside the workspace",
		)
	}
	roots.effective = accepted
	return roots, nil
}

// Effective returns the admitted roots for this session.
func (r *Roots) Effective() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.effective...)
}

// Violations lists the declarations that were refused.
func (r *Roots) Violations() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.violations...)
}

// Workspace returns a workspace whose base is the first effective root.
//
// Tool construction stays unchanged: the returned workspace carries the narrowed
// root set, so a tool created for this session cannot escape the declaration.
func (r *Roots) Workspace() *Workspace {
	if r == nil || r.base == nil {
		return nil
	}
	root := r.base.Root()
	if len(r.effective) > 0 {
		root = r.effective[0]
	}
	return &Workspace{
		root:         root,
		allowedRoots: r.effective,
		platform:     r.base.platform,
	}
}

// rootsFromContextKey carries a session's narrowed roots into tool execution.
type rootsFromContextKey struct{}

// WithRoots attaches a session's roots to a context.
func WithRoots(ctx context.Context, roots *Roots) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if roots == nil {
		return ctx
	}
	return context.WithValue(ctx, rootsFromContextKey{}, roots)
}

// RootsFromContext retrieves the session roots.
func RootsFromContext(ctx context.Context) *Roots {
	if ctx == nil {
		return nil
	}
	roots, _ := ctx.Value(rootsFromContextKey{}).(*Roots)
	return roots
}

// NormalizeRootURI converts a `file://` root URI into a filesystem path.
//
// The MCP roots URI must be a file:// URI. A bare path is accepted too, because
// hand-written configuration is common and rejecting it would be needlessly strict;
// anything with another scheme is refused so an http:// "root" cannot be silently
// treated as a directory.
func NormalizeRootURI(uri string) (string, error) {
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" {
		return "", fmt.Errorf("root uri is empty")
	}
	if strings.HasPrefix(trimmed, "file://") {
		rest := strings.TrimPrefix(trimmed, "file://")
		// file:///C:/x on Windows and file:///home/x on Unix both leave a leading
		// slash that must be trimmed on Windows only.
		if strings.HasPrefix(rest, "/") && len(rest) > 2 && rest[2] == ':' {
			rest = rest[1:]
		}
		if rest == "" {
			return "", fmt.Errorf("root uri %q has no path", uri)
		}
		return filepath.FromSlash(rest), nil
	}
	if idx := strings.Index(trimmed, "://"); idx > 0 {
		return "", fmt.Errorf("unsupported root uri scheme %q (want file://)", trimmed[:idx])
	}
	return trimmed, nil
}