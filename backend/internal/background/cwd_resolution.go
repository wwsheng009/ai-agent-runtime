package background

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// resolveJobCwd anchors a background job's working directory to the session
// workspace root carried in ctx (toolctx.WorkspaceRoot).
//
// Background jobs used to hand their cwd straight to exec.Cmd.Dir, so a
// relative cwd - and an omitted one - resolved against the server process
// directory while the tool policy validated the very same cwd argument against
// the session workspace root. A job could therefore start in a directory the
// policy never approved, or report a cwd unrelated to the files the session
// reads and writes. This helper walks the same chain as the shell and file
// tools: session workspace root from ctx, otherwise the process working
// directory (the registered toolkit base path is not visible in this package;
// callers that know it resolve before submitting).
//
// Absolute cwd values win unchanged. A relative cwd joins the anchor. An empty
// cwd yields the anchor itself, so a job started from a bound session runs in
// that session's workspace instead of inheriting the server directory. When no
// anchor is bound the input is returned untouched, which keeps exec.Cmd on the
// process working directory exactly as before.
func resolveJobCwd(ctx context.Context, cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if filepath.IsAbs(cwd) {
		return filepath.Clean(cwd)
	}

	base := strings.TrimSpace(toolctx.WorkspaceRoot(ctx))
	if base != "" {
		if !filepath.IsAbs(base) {
			if abs, err := filepath.Abs(base); err == nil {
				base = abs
			}
		}
		base = filepath.Clean(base)
	}
	if base == "" {
		return cwd
	}
	if cwd == "" {
		return base
	}
	return filepath.Clean(filepath.Join(base, cwd))
}
