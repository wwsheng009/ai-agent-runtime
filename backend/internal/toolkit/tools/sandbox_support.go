package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
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

func (p *sandboxPolicy) resolvePath(targetPath string) string {
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		return targetPath
	}
	if filepath.IsAbs(targetPath) {
		return targetPath
	}
	if p == nil || strings.TrimSpace(p.basePath) == "" {
		return targetPath
	}
	return filepath.Clean(filepath.Join(p.basePath, targetPath))
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
