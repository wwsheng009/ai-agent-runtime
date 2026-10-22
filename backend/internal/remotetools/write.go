package remotetools

import (
	"context"
	"fmt"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
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
		"1.1.0",
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

	result, mutationErr := mutatecore.Write(target, content, mutatecore.WriteOptions{
		Mode:             mutatecore.DefaultFileMode,
		PreserveEncoding: true,
		RefuseBinary:     true,
	})
	if mutationErr != nil {
		return remoteErrorResult(remoteMutationError(mutationErr)), nil
	}

	display := t.cleanDisplayPath(target)
	metadata := map[string]interface{}{
		"file_path":     display,
		"bytes_written": result.NewSize,
		"old_size":      result.OldSize,
		"new_size":      result.NewSize,
		"created":       result.Created,
		"replayed":      result.Replayed,
		"encoding":      result.Encoding.String(),
		"remote":        true,
		"workspace":     t.workspace.Root(),
		"mutated_paths": []string{display},
	}
	verb := "Updated"
	if result.Created {
		verb = "Created"
	} else if result.Replayed {
		verb = "Unchanged"
	}
	return remoteTextResult(fmt.Sprintf("%s %s (%d bytes).", verb, display, result.NewSize), metadata), nil
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
		"1.1.0",
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
	if err := validateWriteTarget(target); err != nil {
		return remoteErrorResult(err), nil
	}

	result, mutationErr := mutatecore.Append(ctx, target, content, mutatecore.AppendOptions{
		CreateIfMissing:  createIfMissing,
		Mode:             mutatecore.DefaultFileMode,
		PreserveEncoding: true,
		RefuseBinary:     true,
	})
	if mutationErr != nil {
		return remoteErrorResult(remoteMutationError(mutationErr)), nil
	}

	display := t.cleanDisplayPath(target)
	metadata := map[string]interface{}{
		"file_path":      display,
		"bytes_appended": result.BytesWritten,
		"total_bytes":    result.SizeAfter,
		"created":        result.Created,
		"replayed":       result.Replayed,
		"encoding":       result.Encoding.String(),
		"remote":         true,
		"workspace":      t.workspace.Root(),
		"mutated_paths":  []string{display},
	}
	return remoteTextResult(
		fmt.Sprintf("Appended %d bytes to %s (total %d bytes).", result.BytesWritten, display, result.SizeAfter),
		metadata,
	), nil
}

// remoteEditTool implements `edit` (exact replacement with ambiguity guard).
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
				"description": "Exact text to replace. Must occur exactly once unless replace_all/replacement_count is set.",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "Replacement text.",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "Replace every match. Mutually exclusive with replacement_count.",
			},
			"replacement_count": map[string]interface{}{
				"type":        "integer",
				"description": "Replace only the first N matches. Mutually exclusive with replace_all.",
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
		tool.describe("Replace exact text in a remote file with an ambiguity guard."),
		"1.1.0",
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
	replaceAll, _ := params["replace_all"].(bool)
	replacementCount := 0
	if _, present := params["replacement_count"]; present {
		replacementCount = intParam(params, "replacement_count", 0)
		if replacementCount <= 0 {
			return remoteErrorResult(newToolError(CodeInvalidArgument, "pass a positive replacement_count", "replacement_count must be greater than 0")), nil
		}
	}
	if replaceAll && replacementCount > 0 {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "choose replace_all or replacement_count", "replace_all and replacement_count are mutually exclusive")), nil
	}

	target, err := t.authorize(ctx, rawPath, runtimeexecutor.OpWrite)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if err := validateWriteTarget(target); err != nil {
		return remoteErrorResult(err), nil
	}

	result, mutationErr := mutatecore.Edit(target, oldString, newString, mutatecore.EditOptions{
		ReplaceAll:       replaceAll,
		ReplacementCount: replacementCount,
		Mode:             mutatecore.DefaultFileMode,
		PreserveEncoding: true,
		MaxBytes:         10 * 1024 * 1024,
	})
	if mutationErr != nil {
		return remoteErrorResult(remoteMutationError(mutationErr)), nil
	}

	display := t.cleanDisplayPath(target)
	metadata := map[string]interface{}{
		"file_path":     display,
		"replacements":  result.Replacements,
		"bytes_before":  result.SizeBefore,
		"bytes_after":   result.SizeAfter,
		"encoding":      result.Encoding.String(),
		"remote":        true,
		"workspace":     t.workspace.Root(),
		"mutated_paths": []string{display},
	}
	return remoteTextResult(
		fmt.Sprintf("Replaced %d occurrence(s) in %s (%d -> %d bytes).", result.Replacements, display, result.SizeBefore, result.SizeAfter),
		metadata,
	), nil
}
