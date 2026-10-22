package remotetools

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// newRemoteRuntime builds the call-scoped Runtime injected into shared core
// handlers. The context is captured deliberately: MCP roots narrowing is a
// per-request concern, so path resolution must use workspaceForCall instead of
// a Runtime shared across concurrent sessions.
func newRemoteRuntime(ctx context.Context, cfg *Config, workspace *Workspace, sandbox *Sandbox, artifacts *ArtifactStore) toolcore.Runtime {
	return &remoteRuntime{
		paths:     &remotePaths{ctx: ctx, workspace: workspace},
		perms:     remotePermissions{sandbox: sandbox},
		commands:  remoteCommands{sandbox: sandbox},
		env:       remoteEnv{sandbox: sandbox},
		capture:   remoteCapture{cfg: cfg},
		output:    remoteOutput{cfg: cfg, ctx: ctx, workspace: workspace},
		search:    resolveRipgrepRunner(),
		artifacts: remoteArtifacts{store: artifacts, target: targetName(cfg)},
		sessions:  &remoteSessions{ctx: ctx, workspace: workspace},
		clock:     remoteClock{},
		ids:       &remoteIDs{},
	}
}

type remoteRuntime struct {
	paths     toolcore.PathResolver
	perms     toolcore.PermissionChecker
	commands  toolcore.CommandPolicy
	env       toolcore.EnvProvider
	capture   remoteCapture
	output    toolcore.OutputShaper
	search    ripgrepRunner
	artifacts toolcore.ArtifactSink
	sessions  toolcore.SessionState
	clock     toolcore.Clock
	ids       toolcore.IDGenerator
}

func (r *remoteRuntime) Paths() toolcore.PathResolver            { return r.paths }
func (r *remoteRuntime) Permissions() toolcore.PermissionChecker { return r.perms }
func (r *remoteRuntime) Commands() toolcore.CommandPolicy        { return r.commands }
func (r *remoteRuntime) Env() toolcore.EnvProvider               { return r.env }
func (r *remoteRuntime) Output() toolcore.OutputShaper           { return r.output }
func (r *remoteRuntime) Artifacts() toolcore.ArtifactSink        { return r.artifacts }
func (r *remoteRuntime) Sessions() toolcore.SessionState         { return r.sessions }
func (r *remoteRuntime) Clock() toolcore.Clock                   { return r.clock }
func (r *remoteRuntime) IDs() toolcore.IDGenerator               { return r.ids }

// CaptureLimit implements toolcore.CaptureBudgetProvider: remote read tools
// capture up to the configured payload ceiling before folding to the
// model-visible window.
func (r *remoteRuntime) CaptureLimit(string) int { return r.capture.limit() }

// Glob implements searchcore.Searcher with the resolver-backed ripgrep binary.
func (r *remoteRuntime) Glob(ctx context.Context, base, pattern string, limit int) (searchcore.GlobResult, bool, error) {
	return r.search.glob(ctx, base, pattern, limit)
}

// Grep implements searchcore.Searcher with the resolver-backed ripgrep binary.
func (r *remoteRuntime) Grep(ctx context.Context, opts searchcore.GrepOptions) (searchcore.GrepResult, bool, error) {
	return r.search.grep(ctx, opts)
}

type remoteCapture struct {
	cfg *Config
}

func (c remoteCapture) limit() int {
	if c.cfg == nil {
		return DefaultMaxBytesPerCall
	}
	if c.cfg.Limits.CaptureLimit > 0 {
		return c.cfg.Limits.CaptureLimit
	}
	if c.cfg.Limits.MaxBytesPerCall > 0 {
		return c.cfg.Limits.MaxBytesPerCall
	}
	return DefaultMaxBytesPerCall
}

type remotePaths struct {
	ctx       context.Context
	workspace *Workspace
}

func (p *remotePaths) effective() *Workspace {
	if p == nil || p.workspace == nil {
		return nil
	}
	return workspaceForCall(p.ctx, p.workspace)
}

func (p *remotePaths) Root() string {
	if workspace := p.effective(); workspace != nil {
		return workspace.Root()
	}
	return ""
}

func (p *remotePaths) Resolve(raw string) (string, error) {
	workspace := p.effective()
	if workspace == nil {
		return "", fmt.Errorf("workspace is not configured")
	}
	return workspace.Resolve(raw)
}

func (p *remotePaths) Relative(abs string) string {
	if workspace := p.effective(); workspace != nil {
		return workspace.Relative(abs)
	}
	return abs
}

type remotePermissions struct {
	sandbox *Sandbox
}

func (p remotePermissions) CheckPath(op toolcore.Operation, abs string) error {
	if p.sandbox == nil {
		// The workspace resolver is the primary jail; without an explicit
		// sandbox there is no additional policy to apply here.
		return nil
	}
	switch op {
	case toolcore.OpRead, toolcore.OpList:
		return p.sandbox.CheckPath(runtimeexecutor.OpRead, abs)
	case toolcore.OpWrite:
		return p.sandbox.CheckPath(runtimeexecutor.OpWrite, abs)
	case toolcore.OpExec:
		return p.sandbox.CheckPath(runtimeexecutor.OpExecute, abs)
	default:
		return fmt.Errorf("unsupported path operation %q", op)
	}
}

type remoteCommands struct {
	sandbox *Sandbox
}

func (c remoteCommands) Check(command string) (string, error) {
	if c.sandbox == nil {
		return "", fmt.Errorf("shell policy is not configured")
	}
	return c.sandbox.CheckCommand(command)
}

type remoteEnv struct {
	sandbox *Sandbox
}

func (e remoteEnv) Filter(env []string) []string {
	if e.sandbox == nil {
		return nil
	}
	return e.sandbox.FilterEnv(env)
}

type remoteOutput struct {
	cfg       *Config
	ctx       context.Context
	workspace *Workspace
}

func (o remoteOutput) Budget(string) int {
	if o.cfg == nil || o.cfg.Limits.ModelVisibleCap <= 0 {
		return DefaultModelVisibleCap
	}
	return o.cfg.Limits.ModelVisibleCap
}

func (o remoteOutput) Shape(tool string, result *toolcore.Result) (*toolcore.Result, error) {
	if result == nil {
		return nil, nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["remote"] = true
	if workspace := workspaceForCall(o.ctx, o.workspace); workspace != nil {
		result.Metadata["workspace"] = workspace.Root()
	}
	if result.OutputKind != toolcore.KindText {
		return result, nil
	}
	shaped, shapedMetadata, _, _ := toolcoreoutput.ShapeText(tool, result.Content, result.Metadata, 0, o.Budget(tool))
	result.Content = shaped
	result.Metadata = shapedMetadata
	return result, nil
}

type remoteArtifacts struct {
	store  *ArtifactStore
	target string
}

func (a remoteArtifacts) Put(_ context.Context, ref toolcore.ArtifactRef, data []byte, _ toolcore.ArtifactMeta) (toolcore.ArtifactRef, error) {
	if a.store == nil {
		return ref, fmt.Errorf("artifact store is not configured")
	}
	id, err := a.store.Put(data)
	if err != nil {
		return ref, err
	}
	ref.ID = id
	ref.Scope = toolcore.ArtifactScopeRemote
	ref.Target = a.target
	return ref, nil
}

func (a remoteArtifacts) Open(_ context.Context, ref toolcore.ArtifactRef, offset, limit int) (toolcore.ArtifactChunk, error) {
	if a.store == nil {
		return toolcore.ArtifactChunk{}, fmt.Errorf("artifact store is not configured")
	}
	if ref.Scope != toolcore.ArtifactScopeRemote || (ref.Target != "" && ref.Target != a.target) {
		return toolcore.ArtifactChunk{}, fmt.Errorf("artifact scope denied")
	}
	content, size, eof, err := a.store.Read(ref.ID, offset, limit)
	if err != nil {
		return toolcore.ArtifactChunk{}, err
	}
	return toolcore.ArtifactChunk{Data: []byte(content), Size: size, Offset: int64(offset), EOF: eof}, nil
}

type remoteSessions struct {
	ctx       context.Context
	workspace *Workspace
}

func (s *remoteSessions) IDs() toolcore.SessionIDs { return toolcore.SessionIDs{} }

func (s *remoteSessions) Workspace() toolcore.WorkspaceState {
	state := toolcore.WorkspaceState{}
	if workspace := workspaceForCall(s.ctx, s.workspace); workspace != nil {
		state.Root = workspace.Root()
		state.AllowedRoots = workspace.AllowedRoots()
	}
	return state
}

func (s *remoteSessions) Artifacts() toolcore.ArtifactState {
	return toolcore.ArtifactState{}
}

func (s *remoteSessions) Extensions() toolcore.ExtensionStore { return remoteExtensions{} }

type remoteExtensions struct{}

func (remoteExtensions) Get(string) (any, bool) { return nil, false }
func (remoteExtensions) Set(string, any)        {}

type remoteClock struct{}

func (remoteClock) Now() time.Time { return time.Now() }

type remoteIDs struct {
	seq atomic.Uint64
}

func (g *remoteIDs) NewID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, g.seq.Add(1))
}

// targetName keeps the artifact namespace stable even when the config omits a
// label; the default mirrors the audit/description fallback.
func targetName(cfg *Config) string {
	if cfg == nil {
		return "remote"
	}
	if name := cfg.TargetName; name != "" {
		return name
	}
	return "remote"
}
