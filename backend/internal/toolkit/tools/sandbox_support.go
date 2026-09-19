package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

type sandboxPolicy struct {
	sandbox  *runtimeexecutor.Sandbox
	basePath string
}

func (p *sandboxPolicy) SetSandbox(sandbox *runtimeexecutor.Sandbox) {
	p.sandbox = sandbox
}

func (p *sandboxPolicy) SetBasePath(basePath string) {
	if p == nil {
		return
	}
	basePath = strings.TrimSpace(basePath)
	if basePath != "" && !filepath.IsAbs(basePath) {
		if absPath, err := filepath.Abs(basePath); err == nil {
			basePath = absPath
		}
	}
	p.basePath = basePath
}

// effectiveBasePath returns the filesystem root relative tool paths should be
// resolved against for the current invocation: the session-bound workspace
// root carried in ctx (toolctx.WorkspaceRoot) when present, otherwise the
// tool-registered basePath (SetBasePath). This keeps file tools consistent
// with shell CWD resolution and preflight read-path checks for
// directory-bound sessions.
func (p *sandboxPolicy) effectiveBasePath(ctx context.Context) string {
	if root := strings.TrimSpace(toolctx.WorkspaceRoot(ctx)); root != "" {
		if !filepath.IsAbs(root) {
			if absPath, err := filepath.Abs(root); err == nil {
				return filepath.Clean(absPath)
			}
		}
		return filepath.Clean(root)
	}
	if p == nil {
		return ""
	}
	return p.basePath
}

// workdirForExecution returns the working directory a shell invocation runs in.
// It walks the same chain as effectiveBasePath - session workspace root from
// ctx, then the tool-registered base path (SetBasePath) - and only falls back to
// the process working directory when neither is bound. Shell commands therefore
// stay in the same directory the file tools read and write when a run context
// carries no workspace root, instead of silently executing in the server process
// directory while the policy cleared the call against the bound workspace.
// Precedence for an explicit workdir param is unchanged: absolute wins as-is,
// relative joins the base.
func (p *sandboxPolicy) workdirForExecution(ctx context.Context, workdir string) (string, error) {
	base := p.effectiveBasePath(ctx)
	if base == "" {
		return resolveWorkdir(workdir)
	}
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return filepath.Clean(base), nil
	}
	if filepath.IsAbs(workdir) {
		return filepath.Clean(workdir), nil
	}
	return filepath.Clean(filepath.Join(base, workdir)), nil
}

// resolvePathWithContext resolves a tool path argument like resolvePath, but
// anchors relative targets to the session-bound workspace root from ctx
// (falling back to the registered basePath). Absolute targets are returned
// unchanged. Callers inside Execute should prefer this variant so relative
// paths follow the session workspace instead of the global registration.
func (p *sandboxPolicy) resolvePathWithContext(ctx context.Context, targetPath string) string {
	trimmed := strings.TrimSpace(targetPath)
	if trimmed == "" || filepath.IsAbs(trimmed) {
		return trimmed
	}
	base := p.effectiveBasePath(ctx)
	if base == "" {
		// Fall back to the process working directory so relative paths resolve
		// consistently with shell workdir resolution (workdirForExecution /
		// resolveWorkdir) when no session workspace root or registered base
		// path is bound. Without this, relative paths are returned as-is and
		// stat/open calls miss the file relative to the server process CWD.
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			return filepath.Clean(filepath.Join(cwd, trimmed))
		}
		return trimmed
	}
	return filepath.Clean(filepath.Join(base, trimmed))
}

// buildPathNotFoundHint builds the "path not found" hint against the effective
// base path for the current invocation (session-bound workspace root when
// present, otherwise the registered basePath) so the suggested search root
// matches where relative paths were actually resolved.
func (p *sandboxPolicy) buildPathNotFoundHint(ctx context.Context, targetPath string) string {
	return runtimeexecutor.BuildPathNotFoundHintForPath(targetPath, p.effectiveBasePath(ctx))
}

func (p *sandboxPolicy) buildPathNotFoundError(ctx context.Context, prefix, targetPath string) error {
	if hint := p.buildPathNotFoundHint(ctx, targetPath); hint != "" {
		return fmt.Errorf("%s: %s\n%s", prefix, targetPath, hint)
	}
	return fmt.Errorf("%s: %s", prefix, targetPath)
}

// buildPathKindMismatchHint mirrors buildPathNotFoundHint for kind mismatches
// (path exists but is not the expected kind).
func (p *sandboxPolicy) buildPathKindMismatchHint(ctx context.Context, targetPath string) string {
	return runtimeexecutor.BuildPathKindMismatchHintForPath(targetPath, p.effectiveBasePath(ctx))
}

func (p *sandboxPolicy) buildPathKindMismatchError(ctx context.Context, prefix, targetPath string) error {
	if hint := p.buildPathKindMismatchHint(ctx, targetPath); hint != "" {
		return fmt.Errorf("%s: %s\n%s", prefix, targetPath, hint)
	}
	return fmt.Errorf("%s: %s", prefix, targetPath)
}

func (p *sandboxPolicy) checkPath(op runtimeexecutor.PermissionOp, targetPath string) error {
	if p == nil || p.sandbox == nil {
		return nil
	}
	if err := p.sandbox.CheckPermission(op, targetPath); err != nil {
		return wrapSandboxPermissionError(fmt.Sprintf("sandbox denied %s access", op), err, map[string]interface{}{
			"policy":      "sandbox",
			"operation":   string(op),
			"target_path": targetPath,
		})
	}
	return nil
}

func (p *sandboxPolicy) checkURL(rawURL string) error {
	if p == nil || p.sandbox == nil {
		return nil
	}
	if err := p.sandbox.CheckURL(rawURL); err != nil {
		return wrapSandboxPermissionError("sandbox denied network access", err, map[string]interface{}{
			"policy": "sandbox",
			"url":    rawURL,
		})
	}
	return nil
}

func validateRelativePattern(pattern string) error {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return nil
	}
	if filepath.IsAbs(trimmed) {
		return fmt.Errorf("sandbox policy requires a relative pattern")
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sandbox policy forbids escaping the search path")
	}
	return nil
}

func wrapSandboxPermissionError(message string, cause error, ctx map[string]interface{}) error {
	return runtimeerrors.WrapWithContext(
		runtimeerrors.ErrAgentPermission,
		message,
		cause,
		ctx,
	)
}
