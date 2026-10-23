package mutatecore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

const defaultMutationNextAction = "re-read the target with view, then retry the mutation with current content"

func NewWriteHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "write"
	}
	if def.Version == "" {
		def.Version = "1.1.0"
	}
	return &writeHandler{def: def}
}

type writeHandler struct {
	def toolcore.Definition
}

func (h *writeHandler) Definition() toolcore.Definition { return h.def }

func (h *writeHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	rawPath, content, failure := requiredPathAndContent(call)
	if failure != nil {
		return failure, nil
	}
	target, failure := authorizeWrite(call, rawPath)
	if failure != nil {
		return failure, nil
	}
	result, mutationErr := Write(target, content, WriteOptions{
		Mode:             DefaultFileMode,
		PreserveEncoding: true,
		RefuseBinary:     true,
	})
	if mutationErr != nil {
		return mutationFailure(mutationErr), nil
	}
	display := displayPath(call, target)
	verb := "Updated"
	if result.Created {
		verb = "Created"
	} else if result.Replayed {
		verb = "Unchanged"
	}
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    fmt.Sprintf("%s %s (%d bytes).", verb, display, result.NewSize),
		Metadata: map[string]any{
			"file_path":     display,
			"bytes_written": result.NewSize,
			"old_size":      result.OldSize,
			"new_size":      result.NewSize,
			"created":       result.Created,
			"replayed":      result.Replayed,
			"encoding":      result.Encoding.String(),
			"mutated_paths": []string{display},
		},
	})
}

func NewAppendHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "append_write"
	}
	if def.Version == "" {
		def.Version = "1.1.0"
	}
	return &appendHandler{def: def}
}

type appendHandler struct {
	def toolcore.Definition
}

func (h *appendHandler) Definition() toolcore.Definition { return h.def }

func (h *appendHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	rawPath, content, failure := requiredPathAndContent(call)
	if failure != nil {
		return failure, nil
	}
	target, failure := authorizeWrite(call, rawPath)
	if failure != nil {
		return failure, nil
	}
	createIfMissing, _ := call.Args["create_if_missing"].(bool)
	result, mutationErr := Append(ctx, target, content, AppendOptions{
		CreateIfMissing:  createIfMissing,
		Mode:             DefaultFileMode,
		PreserveEncoding: true,
		RefuseBinary:     true,
	})
	if mutationErr != nil {
		return mutationFailure(mutationErr), nil
	}
	display := displayPath(call, target)
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    fmt.Sprintf("Appended %d bytes to %s (total %d bytes).", result.BytesWritten, display, result.SizeAfter),
		Metadata: map[string]any{
			"file_path":      display,
			"bytes_appended": result.BytesWritten,
			"total_bytes":    result.SizeAfter,
			"created":        result.Created,
			"replayed":       result.Replayed,
			"encoding":       result.Encoding.String(),
			"mutated_paths":  []string{display},
		},
	})
}

func NewEditHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "edit"
	}
	if def.Version == "" {
		def.Version = "1.1.0"
	}
	return &editHandler{def: def}
}

type editHandler struct {
	def toolcore.Definition
}

func (h *editHandler) Definition() toolcore.Definition { return h.def }

func (h *editHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	rawPath := stringArg(call, "file_path")
	if strings.TrimSpace(rawPath) == "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass file_path", fmt.Errorf("file_path is required")), nil
	}
	oldString, _ := call.Args["old_string"].(string)
	if oldString == "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass a non-empty old_string", fmt.Errorf("old_string is required")), nil
	}
	newString, _ := call.Args["new_string"].(string)
	replaceAll, _ := call.Args["replace_all"].(bool)
	replacementCount := 0
	if _, present := call.Args["replacement_count"]; present {
		replacementCount = intArg(call, "replacement_count", 0)
		if replacementCount <= 0 {
			return toolcore.Error(toolcore.ErrInvalidArgument, "pass a positive replacement_count",
				fmt.Errorf("replacement_count must be greater than 0")), nil
		}
	}
	if replaceAll && replacementCount > 0 {
		return toolcore.Error(toolcore.ErrInvalidArgument, "choose replace_all or replacement_count",
			fmt.Errorf("replace_all and replacement_count are mutually exclusive")), nil
	}
	target, failure := authorizeWrite(call, rawPath)
	if failure != nil {
		return failure, nil
	}
	result, mutationErr := Edit(target, oldString, newString, EditOptions{
		ReplaceAll:       replaceAll,
		ReplacementCount: replacementCount,
		Mode:             DefaultFileMode,
		PreserveEncoding: true,
		MaxBytes:         10 * 1024 * 1024,
	})
	if mutationErr != nil {
		return mutationFailure(mutationErr), nil
	}
	display := displayPath(call, target)
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    fmt.Sprintf("Replaced %d occurrence(s) in %s (%d -> %d bytes).", result.Replacements, display, result.SizeBefore, result.SizeAfter),
		Metadata: map[string]any{
			"file_path":     display,
			"replacements":  result.Replacements,
			"bytes_before":  result.SizeBefore,
			"bytes_after":   result.SizeAfter,
			"encoding":      result.Encoding.String(),
			"mutated_paths": []string{display},
		},
	})
}

func NewApplyPatchHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "apply_patch"
	}
	if def.Version == "" {
		def.Version = "1.1.0"
	}
	return &applyPatchHandler{def: def}
}

type applyPatchHandler struct {
	def toolcore.Definition
}

func (h *applyPatchHandler) Definition() toolcore.Definition { return h.def }

func (h *applyPatchHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	rawPatch := stringArg(call, "patch")
	if strings.TrimSpace(rawPatch) == "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass patch", fmt.Errorf("patch is required")), nil
	}
	if failure := runtimeFailure(call); failure != nil {
		return failure, nil
	}
	result, mutationErr := ApplyPatch(ctx, rawPatch, PatchBindings{
		Resolve: func(raw string) (string, error) {
			target, failure := authorizeWrite(call, raw)
			if failure != nil {
				return "", resultAsError(failure)
			}
			return target, nil
		},
		CheckWrite: ValidateWriteTarget,
		CheckDelete: func(abs string) error {
			if permissions := call.Runtime.Permissions(); permissions != nil {
				if err := permissions.CheckPath(toolcore.OpDelete, abs); err != nil {
					return err
				}
			}
			return nil
		},
		Display:       func(abs string) string { return displayPath(call, abs) },
		CreateParents: false,
		Mode:          DefaultFileMode,
	})
	if mutationErr != nil {
		return mutationFailure(mutationErr), nil
	}
	summary := fmt.Sprintf("Applied patch to %d file(s):\n", result.FilesChanged)
	for _, path := range result.MutatedPaths {
		summary += "  - " + path + "\n"
	}
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    summary,
		Metadata: map[string]any{
			"files_changed": result.FilesChanged,
			"created_files": result.Created,
			"updated_files": result.Updated,
			"deleted_files": result.Deleted,
			"moved_files":   result.Moved,
			"mutated_paths": result.MutatedPaths,
		},
	})
}

// ValidateWriteTarget rejects targets that cannot be written as a regular file:
// directories, special files, and new paths whose parent is missing or not a
// directory. The check is shared by local/remote surfaces.
func ValidateWriteTarget(absPath string) error {
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			parent := filepath.Dir(absPath)
			parentInfo, parentErr := os.Stat(parent)
			if parentErr != nil {
				return errorf(CodeNotFound, "create the parent directory first", "parent directory does not exist: %s", parent)
			}
			if !parentInfo.IsDir() {
				return errorf(CodeInvalidArgument, "the parent path is not a directory", "parent is not a directory: %s", parent)
			}
			return nil
		}
		return errorf(CodeInvalidArgument, "check permissions on the target", "cannot stat %s: %v", absPath, err)
	}
	if info.IsDir() {
		return errorf(CodeInvalidArgument, "pass a file path", "target is a directory: %s", absPath)
	}
	if !info.Mode().IsRegular() {
		return errorf(CodeInvalidArgument, "pass a regular file", "target is a special file: %s", absPath)
	}
	return nil
}

func authorizeWrite(call *toolcore.Call, rawPath string) (string, *toolcore.Result) {
	if failure := runtimeFailure(call); failure != nil {
		return "", failure
	}
	paths := call.Runtime.Paths()
	target, err := paths.Resolve(rawPath)
	if err != nil {
		return "", toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpWrite, target); err != nil {
			return "", toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
		}
	}
	if err := ValidateWriteTarget(target); err != nil {
		return "", mutationFailure(err)
	}
	return target, nil
}

func runtimeFailure(call *toolcore.Call) *toolcore.Result {
	if call == nil || call.Runtime == nil || call.Runtime.Paths() == nil {
		return toolcore.Error(toolcore.ErrInternal, "fix the runtime binding, then retry", nil)
	}
	return nil
}

func requiredPathAndContent(call *toolcore.Call) (string, string, *toolcore.Result) {
	if call == nil {
		return "", "", toolcore.Error(toolcore.ErrInternal, "fix the runtime binding, then retry", nil)
	}
	rawPath := stringArg(call, "file_path")
	if strings.TrimSpace(rawPath) == "" {
		return "", "", toolcore.Error(toolcore.ErrInvalidArgument, "pass file_path", fmt.Errorf("file_path is required"))
	}
	content, ok := call.Args["content"].(string)
	if !ok {
		return "", "", toolcore.Error(toolcore.ErrInvalidArgument, "pass content as a string", fmt.Errorf("content is required"))
	}
	return rawPath, content, nil
}

func displayPath(call *toolcore.Call, abs string) string {
	if call == nil || call.Runtime == nil || call.Runtime.Paths() == nil {
		return abs
	}
	return call.Runtime.Paths().Relative(abs)
}

func mutationFailure(err error) *toolcore.Result {
	code := CodeOf(err)
	if code == "" {
		code = CodeInternal
	}
	nextAction := NextActionOf(err)
	if nextAction == "" {
		nextAction = defaultMutationNextAction
	}
	message := err.Error()
	if message == "" {
		message = code
	}
	content := fmt.Sprintf("%s: %s", code, message)
	if nextAction != "" {
		content += fmt.Sprintf(" (next: %s)", nextAction)
	}
	return &toolcore.Result{
		Success:    false,
		OutputKind: toolcore.KindText,
		Content:    content,
		ErrorCode:  code,
		NextAction: nextAction,
		// The legacy MCP projection renders Result.Err when it is non-nil, so
		// carry the classified text here instead of the bare mutatecore message.
		Err: fmt.Errorf("%s", content),
	}
}

func resultAsError(result *toolcore.Result) error {
	if result == nil {
		return nil
	}
	code := result.ErrorCode
	if code == "" {
		code = CodeInternal
	}
	return &Error{Code: code, NextAction: result.NextAction, Message: result.Content}
}

func shapeResult(call *toolcore.Call, tool string, result *toolcore.Result) (*toolcore.Result, error) {
	if call == nil || call.Runtime == nil || call.Runtime.Output() == nil {
		return result, nil
	}
	shaped, err := call.Runtime.Output().Shape(tool, result)
	if err != nil {
		return toolcore.Error(toolcore.ErrInternal, "retry the call", err), nil
	}
	if shaped == nil {
		return result, nil
	}
	return shaped, nil
}

func stringArg(call *toolcore.Call, key string) string {
	if call == nil || len(call.Args) == 0 {
		return ""
	}
	value, _ := call.Args[key].(string)
	return value
}

func intArg(call *toolcore.Call, key string, fallback int) int {
	if call == nil || len(call.Args) == 0 {
		return fallback
	}
	switch value := call.Args[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return fallback
	}
}
