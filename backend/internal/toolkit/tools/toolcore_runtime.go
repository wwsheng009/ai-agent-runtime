package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// localCoreRuntime adapts a local file tool's sandbox to the toolcore Runtime
// contract, so the very same core handler the remote surface executes can also
// run locally. Resolution and admission come from sandboxPolicy - the door
// every local file tool already uses - while the remaining runtime slots stay
// nil because the read-only handlers do not consume them.
type localCoreRuntime struct {
	policy *sandboxPolicy
	ctx    context.Context
	base   string
}

func newLocalCoreRuntime(ctx context.Context, policy *sandboxPolicy) toolcore.Runtime {
	if ctx == nil {
		ctx = context.Background()
	}
	base := ""
	if policy != nil {
		base = policy.effectiveBasePath(ctx)
	}
	return &localCoreRuntime{policy: policy, ctx: ctx, base: base}
}

// CoreRuntime exposes this tool's sandbox as a toolcore.Runtime, letting shared
// core handlers execute locally with the same resolution rules as the legacy
// path and the same admission checks (device paths, sandbox roots).
func (g *GrepTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if g == nil {
		return nil
	}
	return newLocalCoreRuntime(ctx, &g.sandboxPolicy)
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
