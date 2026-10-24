package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// localCoreRuntime adapts a local file tool's sandbox to the toolcore Runtime
// contract, so the very same core handler the remote surface executes can also
// run locally. Resolution and admission come from sandboxPolicy - the door
// every local file tool already uses - while the remaining runtime slots stay
// nil because the read-only handlers do not consume them.
type localCoreRuntime struct {
	policy *sandboxPolicy
	grep   *GrepTool
	ctx    context.Context
	base   string
}

func newLocalCoreRuntime(ctx context.Context, tool *GrepTool) toolcore.Runtime {
	if ctx == nil {
		ctx = context.Background()
	}
	var policy *sandboxPolicy
	if tool != nil {
		policy = &tool.sandboxPolicy
	}
	base := ""
	if policy != nil && base == "" {
		base = policy.effectiveBasePath(ctx)
	}
	// When neither a session workspace nor a registered base path is bound,
	// anchor to the process CWD so unqualified `path` arguments still resolve
	// (the legacy path did this through resolvePathWithContext).
	if base == "" {
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			base = cwd
		}
	}
	return &localCoreRuntime{policy: policy, grep: tool, ctx: ctx, base: base}
}

// CoreRuntime exposes this tool's sandbox as a toolcore.Runtime, letting shared
// core handlers execute locally with the same resolution rules as the legacy
// path and the same admission checks (device paths, sandbox roots).
func (g *GrepTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if g == nil {
		return nil
	}
	return newLocalCoreRuntime(ctx, g)
}

// Grep implements searchcore.Searcher with the shared ripgrep engine, executed
// through this tool's lookPath/runCommand seam so tests and hosts keep their
// injection point. wasUsed=false means "use the pure-Go walker".
func (r *localCoreRuntime) Grep(ctx context.Context, opts searchcore.GrepOptions) (searchcore.GrepResult, bool, error) {
	engine, ok := r.ripgrepEngine()
	if !ok {
		return searchcore.GrepResult{}, false, nil
	}
	return engine.Grep(ctx, opts)
}

// Glob implements searchcore.Searcher the same way as Grep.
func (r *localCoreRuntime) Glob(ctx context.Context, opts searchcore.GlobOptions) (searchcore.GlobResult, bool, error) {
	engine, ok := r.ripgrepEngine()
	if !ok {
		return searchcore.GlobResult{}, false, nil
	}
	return engine.Glob(ctx, opts)
}

func (r *localCoreRuntime) ripgrepEngine() (searchcore.Ripgrep, bool) {
	if r == nil || r.grep == nil {
		return searchcore.Ripgrep{}, false
	}
	path, ok := r.grep.ripgrepBinaryPath()
	if !ok {
		return searchcore.Ripgrep{}, false
	}
	return searchcore.Ripgrep{Path: path, Stream: r.grep.ripgrepStream()}, true
}

// ripgrepBinaryPath resolves the rg binary through the tool's lookPath seam,
// which is exactly how the local legacy path decides whether ripgrep is usable.
func (g *GrepTool) ripgrepBinaryPath() (string, bool) {
	if g == nil || g.lookPath == nil {
		return "", false
	}
	path, err := g.lookPath("rg")
	if err != nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	return strings.TrimSpace(path), true
}

// ripgrepStream runs ripgrep through the tool's runCommand seam and replays its
// buffered output line by line. That seam cannot cancel a process mid-stream,
// so the shared engine's early stop only ends consumption here; surfaces whose
// stream is the engine's own exec implementation keep streaming cancellation.
func (g *GrepTool) ripgrepStream() searchcore.RipgrepStream {
	return func(ctx context.Context, args []string, dir string, consume func(line []byte) bool) error {
		if g == nil || g.runCommand == nil {
			return fmt.Errorf("ripgrep runner is not bound")
		}
		path, ok := g.ripgrepBinaryPath()
		if !ok {
			return fmt.Errorf("ripgrep is not available")
		}
		_ = dir // the shared engine passes absolute roots; the runner's dir is informational
		dir = g.sandboxPolicy.effectiveBasePath(ctx)
		output, err := g.runCommand(ctx, path, dir, args)
		if err != nil {
			if !isRipgrepNoMatch(err) {
				return err
			}
		}
		for _, line := range splitRipgrepStreamLines(output) {
			if !consume(line) {
				break
			}
		}
		return nil
	}
}

func splitRipgrepStreamLines(output []byte) [][]byte {
	if len(output) == 0 {
		return nil
	}
	lines := make([][]byte, 0, 16)
	start := 0
	for i := 0; i < len(output); i++ {
		if output[i] != '\n' {
			continue
		}
		line := output[start:i]
		if len(line) > 0 {
			lines = append(lines, line)
		}
		start = i + 1
	}
	if start < len(output) {
		lines = append(lines, output[start:])
	}
	return lines
}

func (r *localCoreRuntime) Root() string { return r.base }

// Paths returns the runtime itself: the same value implements the resolver
// contract the core handlers consume.
func (r *localCoreRuntime) Paths() toolcore.PathResolver { return r }

// Resolve mirrors the remote resolver's contract - an empty target means the
// workspace root - and then runs the local admission check, so a core handler
// can never read outside the sandbox the legacy path enforces.
func (r *localCoreRuntime) Resolve(raw string) (string, error) {
	if r.policy == nil {
		return "", fmt.Errorf("workspace is not configured")
	}
	if strings.TrimSpace(raw) != "" && !filepath.IsAbs(raw) {
		// Resolve relative ignore_file spellings against the workspace root
		// first, matching how search roots are resolved.
		if base := r.policy.effectiveBasePath(r.ctx); base != "" {
			raw = filepath.Join(base, raw)
		}
	}
	resolved := r.policy.resolvePathWithContext(r.ctx, raw)
	if strings.TrimSpace(resolved) == "" {
		resolved = r.base
	}
	if strings.TrimSpace(resolved) == "" {
		return "", fmt.Errorf("workspace is not configured")
	}
	resolved = filepath.Clean(resolved)
	if err := r.policy.checkPath(runtimeexecutor.OpRead, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// Relative mirrors the remote workspace spelling exactly (a relative slash
// path, the absolute path when it escapes the root), so both surfaces render
// the same path for the same file.
func (r *localCoreRuntime) Relative(abs string) string {
	if r.base == "" {
		return abs
	}
	rel, err := filepath.Rel(r.base, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

// Permissions stays nil: admission already happens in Resolve, and the core
// handlers skip CheckPath when no checker is supplied.
func (r *localCoreRuntime) Permissions() toolcore.PermissionChecker { return nil }

func (r *localCoreRuntime) Commands() toolcore.CommandPolicy { return nil }
func (r *localCoreRuntime) Env() toolcore.EnvProvider        { return nil }
func (r *localCoreRuntime) Output() toolcore.OutputShaper    { return nil }
func (r *localCoreRuntime) Artifacts() toolcore.ArtifactSink { return nil }
func (r *localCoreRuntime) Sessions() toolcore.SessionState  { return nil }
func (r *localCoreRuntime) Clock() toolcore.Clock            { return nil }
func (r *localCoreRuntime) IDs() toolcore.IDGenerator        { return nil }
