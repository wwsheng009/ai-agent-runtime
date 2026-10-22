package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Write tool names (canonical, unprefixed).
const (
	ToolWrite      = "write"
	ToolEdit       = "edit"
	ToolAppend     = "append_write"
	ToolApplyPatch = "apply_patch"
)

// remoteWriteToolBase carries the sandbox so every mutation path applies the same
// jail check before touching the filesystem.
type remoteWriteToolBase struct {
	remoteToolBase
	sandbox *Sandbox
}

// authorize resolves a target and validates the write permission.
//
// Resolution goes through the workspace jail first (which re-checks symlinks), so
// the path handed to the sandbox is already inside an admitted root; the sandbox
// check is then the second, independent layer.
func (b remoteWriteToolBase) authorize(ctx context.Context, rawPath string, op runtimeexecutor.PermissionOp) (string, error) {
	target, err := b.resolve(ctx, rawPath)
	if err != nil {
		return "", err
	}
	if b.sandbox != nil {
		if err := b.sandbox.CheckPath(op, target); err != nil {
			return "", err
		}
	}
	return target, nil
}

// writeFileAtomic writes data through a sibling temp file and a rename.
//
// A reader (or a parallel agent turn) therefore never observes a half-written
// file, and a crash mid-write leaves the original intact. The existing mode is
// preserved; a new file uses the requested fallback mode.
func writeFileAtomic(path string, data []byte, fallbackMode os.FileMode) error {
	mode := fallbackMode
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".aicli-write-*")
	if err != nil {
		return newToolError(CodeInvalidArgument, "check directory permissions", "cannot create temp file in %s: %v", dir, err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return newToolError(CodeInternal, "retry the write", "cannot write temp file: %v", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return newToolError(CodeInternal, "retry the write", "cannot flush temp file: %v", err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return newToolError(CodeInternal, "retry the write", "cannot close temp file: %v", err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		_ = os.Remove(tempName)
		return newToolError(CodeInternal, "retry the write", "cannot set file mode: %v", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return newToolError(CodeInternal, "retry the write", "cannot replace %s: %v", path, err)
	}
	return nil
}

// remoteWriteTool implements `write` (full-file overwrite or create).
type remoteWriteTool struct {
	*toolkit.BaseTool
	remoteWriteToolBase
}

func newRemoteWriteTool(cfg *Config, workspace *Workspace, sandbox *Sandbox) *remoteWriteTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Target file, relative to the remote workspace.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Complete file content. This replaces the file.",
			},
		},
		"required":             []string{"file_path", "content"},
		"additionalProperties": false,
	}
	tool := &remoteWriteTool{remoteWriteToolBase: remoteWriteToolBase{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		sandbox:        sandbox,
	}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolWrite,
		tool.describe("Write a complete file on the remote host (create or overwrite)."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPath, _ := params["file_path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass file_path", "file_path is required")), nil
	}
	content, ok := params["content"].(string)
	if !ok {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass content as a string", "content is required")), nil
	}
	target, err := t.authorize(ctx, rawPath, runtimeexecutor.OpWrite)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if err := validateWriteTarget(target); err != nil {
		return remoteErrorResult(err), nil
	}
	existed := fileExists(target)
	if err := writeFileAtomic(target, []byte(content), 0o644); err != nil {
		return remoteErrorResult(err), nil
	}
	display := t.cleanDisplayPath(target)
	metadata := map[string]interface{}{
		"file_path":    display,
		"bytes_written": len(content),
		"created":      !existed,
		"remote":       true,
		"workspace":    t.workspace.Root(),
		"mutated_paths": []string{display},
	}
	verb := "Updated"
	if !existed {
		verb = "Created"
	}
	return remoteTextResult(fmt.Sprintf("%s %s (%d bytes).", verb, display, len(content)), metadata), nil
}

// remoteAppendTool implements `append_write`.
type remoteAppendTool struct {
	*toolkit.BaseTool
	remoteWriteToolBase
}

func newRemoteAppendTool(cfg *Config, workspace *Workspace, sandbox *Sandbox) *remoteAppendTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Target file, relative to the remote workspace.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Text appended to the end of the file.",
			},
			"create_if_missing": map[string]interface{}{
				"type":        "boolean",
				"description": "Create the file when it does not exist. Default false.",
			},
		},
		"required":             []string{"file_path", "content"},
		"additionalProperties": false,
	}
	tool := &remoteAppendTool{remoteWriteToolBase: remoteWriteToolBase{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		sandbox:        sandbox,
	}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolAppend,
		tool.describe("Append text to the end of a remote file."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteAppendTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPath, _ := params["file_path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass file_path", "file_path is required")), nil
	}
	content, ok := params["content"].(string)
	if !ok {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass content as a string", "content is required")), nil
	}
	createIfMissing, _ := params["create_if_missing"].(bool)

	target, err := t.authorize(ctx, rawPath, runtimeexecutor.OpWrite)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	exists := fileExists(target)
	if !exists && !createIfMissing {
		return remoteErrorResult(newToolError(CodeNotFound, "pass create_if_missing=true or use write", "file does not exist: %s", t.cleanDisplayPath(target))), nil
	}
	if err := validateWriteTarget(target); err != nil {
		return remoteErrorResult(err), nil
	}

	// Read-modify-write through the atomic helper: appending in place would leave
	// a partially written tail if the process died mid-write.
	existing, readErr := os.ReadFile(target)
	if readErr != nil && !os.IsNotExist(readErr) {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check file permissions", "cannot read %s: %v", t.cleanDisplayPath(target), readErr)), nil
	}
	combined := append(existing, []byte(content)...)
	if err := writeFileAtomic(target, combined, 0o644); err != nil {
		return remoteErrorResult(err), nil
	}

	display := t.cleanDisplayPath(target)
	metadata := map[string]interface{}{
		"file_path":     display,
		"bytes_appended": len(content),
		"total_bytes":   len(combined),
		"created":       !exists,
		"remote":        true,
		"workspace":     t.workspace.Root(),
		"mutated_paths": []string{display},
	}
	return remoteTextResult(fmt.Sprintf("Appended %d bytes to %s (total %d bytes).", len(content), display, len(combined)), metadata), nil
}

// remoteEditTool implements `edit` (exact single-occurrence replacement).
type remoteEditTool struct {
	*toolkit.BaseTool
	remoteWriteToolBase
}

func newRemoteEditTool(cfg *Config, workspace *Workspace, sandbox *Sandbox) *remoteEditTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Target file, relative to the remote workspace.",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "Exact text to replace. Must occur exactly once.",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "Replacement text.",
			},
		},
		"required":             []string{"file_path", "old_string", "new_string"},
		"additionalProperties": false,
	}
	tool := &remoteEditTool{remoteWriteToolBase: remoteWriteToolBase{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		sandbox:        sandbox,
	}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolEdit,
		tool.describe("Replace one exact, unique occurrence of old_string in a remote file."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteEditTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPath, _ := params["file_path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass file_path", "file_path is required")), nil
	}
	oldString, ok := params["old_string"].(string)
	if !ok || oldString == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass a non-empty old_string", "old_string is required")), nil
	}
	newString, _ := params["new_string"].(string)

	target, err := t.authorize(ctx, rawPath, runtimeexecutor.OpWrite)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if err := validateWriteTarget(target); err != nil {
		return remoteErrorResult(err), nil
	}
	existing, readErr := os.ReadFile(target)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return remoteErrorResult(newToolError(CodeNotFound, "verify the path with ls or glob", "file not found: %s", t.cleanDisplayPath(target))), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check file permissions", "cannot read %s: %v", t.cleanDisplayPath(target), readErr)), nil
	}

	body := string(existing)
	count := strings.Count(body, oldString)
	if count == 0 {
		return remoteErrorResult(newToolError(CodeNotFound, "re-read the file and copy the exact text", "old_string not found in %s", t.cleanDisplayPath(target))), nil
	}
	if count > 1 {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "include more surrounding context to make the match unique", "old_string occurs %d times in %s", count, t.cleanDisplayPath(target))), nil
	}

	updated := strings.Replace(body, oldString, newString, 1)
	if err := writeFileAtomic(target, []byte(updated), 0o644); err != nil {
		return remoteErrorResult(err), nil
	}
	display := t.cleanDisplayPath(target)
	metadata := map[string]interface{}{
		"file_path":     display,
		"replacements":  1,
		"bytes_before":  len(existing),
		"bytes_after":   len(updated),
		"remote":        true,
		"workspace":     t.workspace.Root(),
		"mutated_paths": []string{display},
	}
	return remoteTextResult(fmt.Sprintf("Replaced 1 occurrence in %s (%d -> %d bytes).", display, len(existing), len(updated)), metadata), nil
}

// fileExists reports whether path exists (any type).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}