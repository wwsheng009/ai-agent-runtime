package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// localCoreRuntime adapts a local file tool's sandbox to the toolcore Runtime
// contract, so the very same core handler the remote surface executes can also
// run locally. Resolution and admission come from sandboxPolicy - the door
// every local file tool already uses - while the remaining runtime slots stay
// nil because the read-only handlers do not consume them.
type localCoreRuntime struct {
	policy *sandboxPolicy
	// searcher is the tool's own ripgrep seam, when it has one. Grep and glob
	// both reach ripgrep through it; a tool without one simply gets the shared
	// pure-Go walker.
	searcher coreSearcher
	ctx      context.Context
	base     string
}

// coreSearcher is the slice of a local search tool the runtime adapter needs:
// where ripgrep is and how to run it. Both GrepTool and GlobTool satisfy it.
type coreSearcher interface {
	ripgrepBinaryPath() (string, bool)
	ripgrepStream() searchcore.RipgrepStream
}

func newLocalCoreRuntime(ctx context.Context, tool *GrepTool) toolcore.Runtime {
	return newLocalCoreRuntimeWith(ctx, &tool.sandboxPolicy, tool)
}

// newGlobCoreRuntime binds glob's sandbox and ripgrep seam to the same runtime
// contract, so the shared glob handler can run locally.
func newGlobCoreRuntime(ctx context.Context, tool *GlobTool) toolcore.Runtime {
	return newLocalCoreRuntimeWith(ctx, &tool.sandboxPolicy, tool)
}

// newLocalCoreRuntimeWith is the shared constructor: a sandbox policy plus an
// optional ripgrep seam. Everything else in the runtime is nil because the
// read-only handlers do not consume it.
func newLocalCoreRuntimeWith(ctx context.Context, policy *sandboxPolicy, searcher coreSearcher) toolcore.Runtime {
	if ctx == nil {
		ctx = context.Background()
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
	return &localCoreRuntime{policy: policy, searcher: searcher, ctx: ctx, base: base}
}

// CoreRuntime exposes glob's sandbox as a toolcore.Runtime.
func (g *GlobTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if g == nil {
		return nil
	}
	return newGlobCoreRuntime(ctx, g)
}

// CoreRuntime exposes ls's sandbox as a toolcore.Runtime.
func (l *LsTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if l == nil {
		return nil
	}
	return newLocalCoreRuntimeWith(ctx, &l.sandboxPolicy, nil)
}

// coreBackedTool is the local half of a shared handler: a tool that owns a
// sandbox (and, for the search tools, a ripgrep seam).
type coreBackedTool interface {
	toolkit.Tool
	CoreRuntime(context.Context) toolcore.Runtime
}

// executeCoreSearchTool runs a file tool through its shared core handler. Local
// glob, grep and ls all delegate here, which is what makes "one tool, two run
// locations" literal: the handler, the traversal rules, the paging rules and the
// output shape are the same code the remote surface executes.
//
// The result is stamped as tool-owned because the core handler already applied
// the tool's own byte window; letting the render layer fold it again would cut
// a page the handler had sized deliberately.
func executeCoreSearchTool(ctx context.Context, tool coreBackedTool, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if tool == nil {
		return nil, fmt.Errorf("tool is nil")
	}
	var handler toolcore.Handler
	switch typed := tool.(type) {
	case *GlobTool:
		handler = searchcore.NewGlobHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
	case *GrepTool:
		handler = searchcore.NewGrepHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
	case *LsTool:
		handler = lscore.NewHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
	default:
		return nil, fmt.Errorf("tool %q has no shared core handler", tool.Name())
	}
	result, err := handler.Execute(ctx, &toolcore.Call{Args: params, Runtime: tool.CoreRuntime(ctx)})
	if err != nil {
		return nil, err
	}
	converted := toolkit.ResultToToolkit(result)
	if converted != nil {
		converted = stampToolOwnsOutput(converted)
	}
	return converted, nil
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
	if r == nil || r.searcher == nil {
		return searchcore.Ripgrep{}, false
	}
	path, ok := r.searcher.ripgrepBinaryPath()
	if !ok {
		return searchcore.Ripgrep{}, false
	}
	return searchcore.Ripgrep{Path: path, Stream: r.searcher.ripgrepStream()}, true
}

// ripgrepBinaryPath resolves the rg binary through the tool's lookPath seam,
// which is exactly how the local legacy path decides whether ripgrep is usable.
func (g *GrepTool) ripgrepBinaryPath() (string, bool) {
	return resolveRipgrepBinary(g.lookPath)
}

// ripgrepBinaryPath is the same seam for glob, so both search tools share one
// binary-resolution rule.
func (g *GlobTool) ripgrepBinaryPath() (string, bool) {
	return resolveRipgrepBinary(g.lookPath)
}

func resolveRipgrepBinary(lookPath func(string) (string, error)) (string, bool) {
	if lookPath == nil {
		return "", false
	}
	path, err := lookPath("rg")
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
	return localRipgrepStream(g.lookPath, g.runCommand, &g.sandboxPolicy)
}

// ripgrepStream is the same runner for glob.
func (g *GlobTool) ripgrepStream() searchcore.RipgrepStream {
	return localRipgrepStream(g.lookPath, g.runCommand, &g.sandboxPolicy)
}

func localRipgrepStream(
	lookPath func(string) (string, error),
	runCommand func(context.Context, string, string, []string) ([]byte, error),
	policy *sandboxPolicy,
) searchcore.RipgrepStream {
	return func(ctx context.Context, args []string, dir string, consume func(line []byte) bool) error {
		if runCommand == nil {
			return fmt.Errorf("ripgrep runner is not bound")
		}
		path, ok := resolveRipgrepBinary(lookPath)
		if !ok {
			return fmt.Errorf("ripgrep is not available")
		}
		// The engine runs ripgrep with "." as the path argument and passes the
		// admitted search root as dir. Ignoring it and always running from the
		// workspace base made `path: "nested"` list the whole workspace and then
		// join the results onto "nested", producing paths that do not exist
		// (`nested/nested/c.go`). Use the root the handler resolved; fall back to
		// the policy base only when no dir was supplied.
		if strings.TrimSpace(dir) == "" {
			dir = policy.effectiveBasePath(ctx)
		}
		output, err := runCommand(ctx, path, dir, args)
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

// SearchBackend reports how the last search ran. The local tool has always
// published the ripgrep path it resolved, and the command a human would retype;
// keeping that here preserves the diagnostics while the engine itself moved
// into the core.
func (r *localCoreRuntime) SearchBackend(engine string) (string, string) {
	switch engine {
	case "ripgrep", "rg":
		path := ""
		if r != nil && r.searcher != nil {
			if resolved, ok := r.searcher.ripgrepBinaryPath(); ok {
				path = resolved
			}
		}
		return "rg --files", path
	default:
		return "builtin-walker", ""
	}
}

// PathHint explains a missing path the way the local tool always has: relative
// to the sandbox root, with the closest existing candidate.
func (r *localCoreRuntime) PathHint(raw string) string {
	if r == nil || r.policy == nil {
		return ""
	}
	return r.policy.buildPathNotFoundHint(r.ctx, raw)
}

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
