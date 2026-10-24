package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/pathguard"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
	// symbolResolver is the tool's injected code index, when it has one. The
	// index itself never leaves the host: only the resolved window crosses.
	symbolResolver toolcore.SymbolResolver
	// insideGitWorkTree reports whether a directory is covered by version
	// control. A work tree already holds the pre-edit content, so the local host
	// does not ask for a `.backups/` copy there.
	insideGitWorkTree func(string) bool
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

// WantsBackup asks for a rollback point only where version control will not
// provide one.
func (r *localCoreRuntime) WantsBackup(absPath string) bool {
	if r == nil {
		return false
	}
	probe := r.insideGitWorkTree
	if probe == nil {
		probe = isInsideGitWorkTree
	}
	return !probe(filepath.Dir(absPath))
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

// CoreRuntime exposes write's sandbox as a toolcore.Runtime.
func (w *WriteTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if w == nil {
		return nil
	}
	return newLocalCoreRuntimeWith(ctx, &w.sandboxPolicy, nil)
}

// CoreRuntime exposes append_write's sandbox as a toolcore.Runtime.
func (w *AppendWriteTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if w == nil {
		return nil
	}
	return newLocalCoreRuntimeWith(ctx, &w.sandboxPolicy, nil)
}

// CoreRuntime exposes edit's sandbox as a toolcore.Runtime.
func (e *EditTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if e == nil {
		return nil
	}
	runtime := newLocalCoreRuntimeWith(ctx, &e.sandboxPolicy, nil)
	// Carry the tool's own version-control probe into the runtime: a host (or a
	// test) that decides where `.backups/` is wanted must actually be consulted,
	// otherwise the seam silently does nothing.
	if local, ok := runtime.(*localCoreRuntime); ok && e.insideGitWorkTree != nil {
		local.insideGitWorkTree = e.insideGitWorkTree
	}
	return runtime
}

// CoreRuntime exposes apply_patch's sandbox as a toolcore.Runtime.
func (a *ApplyPatchTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if a == nil {
		return nil
	}
	return newLocalCoreRuntimeWith(ctx, &a.sandboxPolicy, nil)
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
	case *WriteTool:
		handler = mutatecore.NewWriteHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
	case *AppendWriteTool:
		handler = mutatecore.NewAppendHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
	case *EditTool:
		handler = mutatecore.NewEditHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
	case *ApplyPatchTool:
		handler = mutatecore.NewApplyPatchHandler(toolcore.Definition{Name: typed.Name(), Version: typed.Version()})
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
		converted = localizeErrorCode(converted)
	}
	return converted, nil
}

// localizeErrorCode maps a shared handler's surface-independent code onto the
// spelling the local surface has always published. Agents classify retries on
// that string (it is what the diagnostic table and the retry policy read), so
// the local surface must not start answering in a second vocabulary just
// because the handler moved into the core.
func localizeErrorCode(result *toolkit.ToolResult) *toolkit.ToolResult {
	if result == nil || result.Metadata == nil {
		return result
	}
	if code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string); code == toolcore.ErrInvalidArgument {
		result.Metadata[toolresult.MetadataErrorCodeKey] = string(runtimeerrors.ErrToolInvalidArgs)
	}
	return result
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
	// The hint builder wants the spelling the caller used; it resolves it
	// against the workspace itself. An already-absolute path is used as-is.
	return r.policy.buildPathNotFoundHint(r.ctx, raw)
}

// KindHint explains a path that exists but is not the kind the caller wanted.
// A request for a file that names an existing directory usually means a typo in
// the file name, so the hint proposes near-name siblings.
func (r *localCoreRuntime) KindHint(raw string) string {
	if r == nil || r.policy == nil {
		return ""
	}
	return r.policy.buildPathKindMismatchHint(r.ctx, raw)
}

// FileMutated keeps the session state the local host maintains for mutations:
// the read-before-write ledger and the knowledge-layer change notification.
// Neither has meaning on a remote host, which is why they live behind this
// optional interface instead of inside the shared mutation handler.
func (r *localCoreRuntime) FileMutated(tool, absPath string, newBytes []byte) {
	if r == nil {
		return
	}
	recordFileWrite(r.ctx, absPath, newBytes, tool)
	notifyFileChanges(r.ctx, absPath)
}

// CheckMutation is the read-before-write guard. It refuses a write whose target
// changed since this session last read it, unless the caller supplied its own
// explicit precondition - in that case the precondition is the authority.
func (r *localCoreRuntime) CheckMutation(tool, absPath string, raw []byte, hasExplicitPrecondition bool) (*toolcore.Result, map[string]any) {
	if r == nil || hasExplicitPrecondition {
		// An explicit precondition is the caller's own statement about the
		// expected state, so there is nothing to veto; the ledger note is still
		// useful, and it is computed from the pre-write snapshot.
		if r == nil {
			return nil, nil
		}
		return nil, staleWriteMetadata(evaluateStaleWrite(r.ctx, absPath, raw))
	}
	verdict := evaluateStaleWrite(r.ctx, absPath, raw)
	if !shouldRefuseStaleWrite(verdict) {
		return nil, staleWriteMetadata(verdict)
	}
	// Advertise the revision the shared handler actually compares. That is
	// mutatecore.Revision over decoded text, not the raw-byte digest the ledger
	// uses for change detection: for a BOM/UTF-16 file the raw digest can never
	// pass expected_sha256, so following the hint would fail again.
	state, inspectErr := mutatecore.Inspect(absPath, 0)
	currentRevision := verdict.CurrentSHA
	if inspectErr == nil {
		currentRevision = state.Revision
	}
	// Only write takes expected_sha256. Advertising it from another tool would
	// send the caller into a retry loop with an argument that tool ignores.
	supportsExplicitRevision := tool == "write"
	refusal := staleWriteFailure(absPath, currentRevision, verdict.LastRecord, supportsExplicitRevision)
	return toolkit.ResultFromToolkit(refusal), nil
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
	// Device/FIFO/reserved-name rejection is a path-shape invariant, not a
	// permission: opening /dev/zero blocks or streams forever regardless of what
	// the caller is allowed to do. It stays at resolution time so every core
	// handler inherits it.
	//
	// The operation-specific permission check does NOT happen here. The core
	// handlers call Permissions().CheckPath with the operation they actually
	// perform; admitting the path as a read here hid that call and let a
	// mutation through on a read-only path.
	if reason := unsupportedPathNameReason(resolved); reason != "" {
		return "", errors.New(pathguard.RefusalMessage(resolved, reason))
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

// ResolveSymbol exposes the tool's injected code index to the shared handler, so
// a `symbol` request resolves the same way on every surface that has one.
func (r *localCoreRuntime) ResolveSymbol(ctx context.Context, name string, limit int) (toolcore.SymbolTarget, bool) {
	if r == nil || r.symbolResolver == nil {
		return toolcore.SymbolTarget{}, false
	}
	return r.symbolResolver.ResolveSymbol(ctx, name, limit)
}

// localSymbolResolver adapts the local view tool's code index to the
// toolcore.SymbolResolver seam. The index, its shadow-mode rule and the
// path-for-file mapping stay in the tool; the shared handler only receives the
// window to read.
type localSymbolResolver struct {
	tool *ViewTool
	ctx  context.Context
}

func (r localSymbolResolver) ResolveSymbol(_ context.Context, name string, limit int) (toolcore.SymbolTarget, bool) {
	if r.tool == nil {
		return toolcore.SymbolTarget{}, false
	}
	window, ok := r.tool.resolveViewSymbol(r.ctx, name, limit)
	if !ok {
		return toolcore.SymbolTarget{}, false
	}
	return toolcore.SymbolTarget{Path: window.FilePath, Offset: window.Offset, Limit: window.Limit}, true
}

// The next three methods implement toolcore.ReadObserver for the local surface:
// the session read ledger and the delivered-window cache both live here, because
// they are session-scoped host state. The shared handler decides *when* an entry
// may be written (only for a window that was really delivered); this adapter only
// knows *where* to write it. A runtime whose ctx carries no session records
// nothing, which is exactly what the helpers already do.

func (r *localCoreRuntime) ReadDelivered(tool, absPath string, window toolcore.ReadWindow) {
	if r == nil || r.ctx == nil {
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		info = nil
	}
	recordFileReadFromDiskObserved(r.ctx, absPath, window.FullRead, tool, fileReadWindow{
		Offset:     window.Offset,
		Limit:      window.Limit,
		LinesRead:  window.LinesRead,
		TotalLines: window.TotalLines,
		Truncated:  window.More || window.ByteBudgetApplied || window.LongLinesTruncated > 0 || window.Tail,
	}, info)
}

func (r *localCoreRuntime) ReadRemembered(tool, absPath string, window toolcore.ReadWindow) {
	if r == nil || r.ctx == nil {
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return
	}
	recordViewWindowRead(r.ctx, absPath, info, window.Offset, window.Limit, window.LinesRead, window.TotalLines, window.EOF)
}

func (r *localCoreRuntime) RememberedStub(tool, absPath string, offset, limit int) (*toolcore.Result, func(), bool) {
	if r == nil || r.ctx == nil {
		return nil, nil, false
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, nil, false
	}
	stub, hit, commit := viewDedupPeek(r.ctx, absPath, info, offset, limit)
	if !hit {
		return nil, nil, false
	}
	return toolkit.ResultFromToolkit(stub), func() { commit() }, true
}

// Permissions exposes the sandbox by operation. Resolution alone is not enough:
// Resolve admits a path for reading, while a mutation must additionally be
// checked against the write/delete rules (a read-only path is readable and must
// still be refused for write). Returning nil here silently disabled those
// checks for every core mutation handler.
func (r *localCoreRuntime) Permissions() toolcore.PermissionChecker {
	if r == nil || r.policy == nil {
		return nil
	}
	return localCorePermissions{policy: r.policy}
}

type localCorePermissions struct {
	policy *sandboxPolicy
}

func (p localCorePermissions) CheckPath(op toolcore.Operation, abs string) error {
	if p.policy == nil {
		return nil
	}
	return p.policy.checkPath(corePermissionOp(op), abs)
}

// corePermissionOp maps the surface-independent operation onto the executor's
// permission vocabulary.
func corePermissionOp(op toolcore.Operation) runtimeexecutor.PermissionOp {
	switch op {
	case toolcore.OpWrite:
		return runtimeexecutor.OpWrite
	case toolcore.OpDelete:
		return runtimeexecutor.OpDelete
	case toolcore.OpExec:
		return runtimeexecutor.OpExecute
	default:
		return runtimeexecutor.OpRead
	}
}

func (r *localCoreRuntime) Commands() toolcore.CommandPolicy { return nil }
func (r *localCoreRuntime) Env() toolcore.EnvProvider        { return nil }
func (r *localCoreRuntime) Output() toolcore.OutputShaper    { return nil }
func (r *localCoreRuntime) Artifacts() toolcore.ArtifactSink { return nil }
func (r *localCoreRuntime) Sessions() toolcore.SessionState  { return nil }
func (r *localCoreRuntime) Clock() toolcore.Clock            { return nil }
func (r *localCoreRuntime) IDs() toolcore.IDGenerator        { return nil }
