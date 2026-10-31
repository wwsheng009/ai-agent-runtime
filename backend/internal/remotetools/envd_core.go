package remotetools

// envd_core.go wires the envd backend to the shared search cores.
//
// S6 of the seam migration: envd's ls/glob/grep are no longer target-side
// scripts. They run the same handlers the local and ssh/http surfaces run,
// against a runtime whose FileAccess() is the envd adapter, so one core answers
// on every transport and a fix lands in one place. The three script bodies
// (find, the rg-or-grep one-shot shell, and the hand-rolled ls renderer) are
// gone; what remains is the wiring below.
//
// What the cores get, and what they deliberately do not:
//
//   - FileAccess() is the guarded envd adapter over the live connection
//     (envd_guarded_access.go): the bytes it serves are limited to files the
//     target user may open, the same kernel verdict the bespoke tools apply, so a
//     search cannot see content that shell cannot. Paths are still resolved and
//     jailed by this runtime before the core ever touches the accessor, so the
//     adapter re-checks no *path policy* (envd_access.go rule 1).
//   - Paths() is the target's own resolution: POSIX spelling, the target-side
//     workspace as the root, and the same choke point the script tools used
//     (be.resolvePath: per-call workdir, lexical jail, target-side
//     canonicalization, jail check against the canonical path). Relative()
//     renders labels the way every other surface renders them, which is what
//     makes a page byte-identical to the local one.
//   - No Searcher. ripgrep is the *host's* engine; binding it here would search
//     this machine instead of the target. Without the extension the grep
//     handler uses its walker over the accessor and reports `engine=walker`,
//     which is the honest name.
//   - No PathHinter. A hint is computed by listing the directory that contains
//     the missing path; only the target can answer that, and this runtime has
//     no accessor-based hint implementation yet, so it declines rather than
//     reading the wrong machine's filesystem.
//
// Budget and folding stay where they already are, shared: the capture ceiling
// is the same Limits block as every other remote read (be.viewLimit()), and the
// model-visible window is folded by the same toolcoreoutput.ShapeText with the
// same provenance metadata the envd surface has always published, so an envd
// read cannot spend more context than an ssh/http read of the same file.

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// envdSharedCoreTools builds the three tools whose execution belongs to the
// shared cores, over one envd connection.
//
// Definitions are the ones the ssh/http registry publishes: one tool name means
// one schema on every transport, so a caller switching backends sends the same
// arguments and reads the same result shape. The envd surface therefore gains
// the parameter surface it never had (paging, ignore handling, sorting, the
// glob include/exclude filters) - and, where a parameter names a capability
// this transport cannot honestly provide, the shared core refuses it with the
// workaround in the message instead of silently ignoring it the way the
// scripts did.
func envdSharedCoreTools(base remoteToolBase, backend *envdBackend) ([]toolkit.Tool, error) {
	factory := func(ctx context.Context) (toolcore.Runtime, error) {
		return newEnvdRuntime(ctx, backend), nil
	}

	lsSchema, err := lsInputSchema()
	if err != nil {
		return nil, fmt.Errorf("envd ls schema: %w", err)
	}
	lsTool, err := toolkit.NewRuntimeTool(lscore.NewHandler(toolcore.Definition{
		Name:                ToolLs,
		Description:         base.describe("List a remote directory (one level) with entry type and size."),
		Version:             "1.0.0",
		InputSchema:         lsSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolLs),
		DirectCall:          true,
	}), factory)
	if err != nil {
		return nil, fmt.Errorf("envd ls handler: %w", err)
	}

	globSchema, err := globInputSchema()
	if err != nil {
		return nil, fmt.Errorf("envd glob schema: %w", err)
	}
	globTool, err := toolkit.NewRuntimeTool(searchcore.NewGlobHandler(toolcore.Definition{
		Name:                ToolGlob,
		Description:         base.describe("Find remote files by glob pattern; respects the workspace boundary."),
		Version:             "1.0.0",
		InputSchema:         globSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolGlob),
		DirectCall:          true,
	}), factory)
	if err != nil {
		return nil, fmt.Errorf("envd glob handler: %w", err)
	}

	grepSchema, err := grepInputSchema()
	if err != nil {
		return nil, fmt.Errorf("envd grep schema: %w", err)
	}
	grepTool, err := toolkit.NewRuntimeTool(searchcore.NewGrepHandler(toolcore.Definition{
		Name:                ToolGrep,
		Description:         base.describe("Search remote file contents with a regular expression; results are file:line:text."),
		Version:             "1.0.0",
		InputSchema:         grepSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolGrep),
		DirectCall:          true,
	}), factory)
	if err != nil {
		return nil, fmt.Errorf("envd grep handler: %w", err)
	}

	return []toolkit.Tool{lsTool, globTool, grepTool}, nil
}

// newEnvdRuntime builds the call-scoped Runtime injected into the shared cores
// on the envd backend. The context is captured deliberately: path resolution
// honours a per-call workdir, so the resolver cannot be shared across calls.
func newEnvdRuntime(ctx context.Context, backend *envdBackend) toolcore.Runtime {
	return &envdRuntime{
		ctx:     ctx,
		backend: backend,
		paths:   &envdPaths{ctx: ctx, backend: backend},
		output:  envdOutput{backend: backend},
	}
}

type envdRuntime struct {
	ctx     context.Context
	backend *envdBackend
	paths   toolcore.PathResolver
	output  toolcore.OutputShaper
}

func (r *envdRuntime) Paths() toolcore.PathResolver { return r.paths }

// FileAccess is the seam this whole migration exists to open: the cores read
// the target through the envd adapter, so a walk, a stat and a read all
// describe the machine holding the files rather than the one running the tool.
// The adapter is wrapped with the target-user readability gate; see
// envd_guarded_access.go for why the transport, not the core, owns that answer.
func (r *envdRuntime) FileAccess() toolcore.Access { return newEnvdGuardedAccess(r.backend) }

// CaptureLimit mirrors remoteCapture: the pre-fold capture ceiling comes from
// the same Limits block as every other remote read.
func (r *envdRuntime) CaptureLimit(string) int { return r.backend.viewLimit() }

func (r *envdRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (r *envdRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (r *envdRuntime) Env() toolcore.EnvProvider               { return nil }
func (r *envdRuntime) Output() toolcore.OutputShaper           { return r.output }
func (r *envdRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (r *envdRuntime) Sessions() toolcore.SessionState         { return nil }
func (r *envdRuntime) Clock() toolcore.Clock                   { return nil }
func (r *envdRuntime) IDs() toolcore.IDGenerator               { return nil }

// envdPaths is the target-side PathResolver for the shared cores.
//
// Every method delegates to the same helpers the envd tools have always used -
// resolvePath for resolution (workdir anchoring, lexical jail, target-side
// canonicalization, jail check against the canonical path) and
// workspaceRelative for labels - so the cores cannot grow a second idea of what
// a target path is. In particular nothing here uses filepath: the target is a
// POSIX daemon, and the host's separator would silently miss every nested path.
type envdPaths struct {
	ctx     context.Context
	backend *envdBackend
}

func (p *envdPaths) Root() string {
	if p == nil || p.backend == nil {
		return ""
	}
	return p.backend.workspace
}

func (p *envdPaths) Resolve(raw string) (string, error) {
	if p == nil || p.backend == nil {
		return "", fmt.Errorf("envd path resolver is not bound")
	}
	return p.backend.resolvePath(p.ctx, raw)
}

func (p *envdPaths) Relative(abs string) string {
	if p == nil || p.backend == nil {
		return abs
	}
	return p.backend.workspaceRelative(abs)
}

// envdOutput folds one shared-core result into the model-visible window with
// the same rules the remote runtime applies, and stamps the same provenance
// keys the envd surface has always published (remote, backend, workspace).
type envdOutput struct {
	backend *envdBackend
}

func (o envdOutput) Budget(string) int {
	if o.backend == nil {
		return DefaultModelVisibleCap
	}
	return o.backend.outputBudget("")
}

func (o envdOutput) Shape(tool string, result *toolcore.Result) (*toolcore.Result, error) {
	if result == nil {
		return nil, nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["remote"] = true
	result.Metadata["backend"] = string(BackendEnvD)
	if o.backend != nil {
		result.Metadata["workspace"] = o.backend.workspace
	}
	if result.OutputKind != toolcore.KindText {
		return result, nil
	}
	if toolcoreoutput.IsSelfSizedWindowTool(tool) {
		result.Metadata[toolcoreoutput.KeySkipRenderTruncation] = true
		result.Metadata[toolcoreoutput.KeyModelVisibleBudget] = toolcoreoutput.ToolBudget(tool)
		return result, nil
	}
	shaped, shapedMetadata, _, _ := toolcoreoutput.ShapeText(tool, result.Content, result.Metadata, 0, o.Budget(tool))
	result.Content = shaped
	result.Metadata = shapedMetadata
	return result, nil
}
