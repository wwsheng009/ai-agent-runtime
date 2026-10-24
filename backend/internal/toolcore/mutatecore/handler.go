package mutatecore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

const defaultMutationNextAction = "re-read the target with view, then retry the mutation with current content"

const (
	// MaxInlineFieldBytes bounds one text field of a mutation call. Past it the
	// transport is likely to truncate the argument instead of delivering it, and
	// a silently truncated write is worse than a refused one.
	MaxInlineFieldBytes = 64 * 1024
	// MaxInlinePayloadBytes bounds the combined text of one call.
	MaxInlinePayloadBytes = 128 * 1024
)

// ValidateInlinePayload rejects a mutation whose text is too large to be
// delivered reliably in one tool call. The check is a property of the tool
// contract, not of a surface: a remote call is truncated by the same limits.
func ValidateInlinePayload(toolName string, fields ...InlineField) string {
	total := 0
	for _, field := range fields {
		if field.Name == "" {
			continue
		}
		size := len(field.Value)
		total += size
		if size > MaxInlineFieldBytes {
			return fmt.Sprintf(
				"%s 工具文本参数过大：%s 为 %d 字节，超过单段限制 %d 字节。请拆分为多个更小的调用，每次只处理一个较小写入块或一个局部替换目标",
				toolName, field.Name, size, MaxInlineFieldBytes)
		}
	}
	if total > MaxInlinePayloadBytes {
		return fmt.Sprintf(
			"%s 工具文本参数总大小过大：%d 字节，超过总限制 %d 字节。请拆分为多个更小的调用，每次只聚焦一个文件和一个较小编辑块",
			toolName, total, MaxInlinePayloadBytes)
	}
	return ""
}

// InlineField is one named text argument of a mutation call.
type InlineField struct {
	Name  string
	Value string
}

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
	// expected_sha256 is a concurrency guard the caller can supply. It is
	// validated before any I/O so a malformed value fails as an argument error
	// instead of looking like a precondition mismatch.
	expected, expectedOK, expectedFailure := expectedRevisionArg(call)
	if expectedFailure != nil {
		return expectedFailure, nil
	}
	if message := ValidateInlinePayload(h.def.Name, InlineField{Name: "content", Value: content}); message != "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "split the write into smaller calls", fmt.Errorf("%s", message)), nil
	}
	// The document guard runs before the write-target validation: a refusable
	// document is refused on its own terms (`report.pptx.` must report the
	// document extension, not "parent directory does not exist").
	if rawTarget, resolveFailure := resolveWriteTarget(call, rawPath); resolveFailure != nil {
		return resolveFailure, nil
	} else if ext := DocumentExtensionRefusal(rawTarget); ext != "" {
		content, code := DocumentRefusalContent(h.def.Name, "以文本覆盖", rawTarget, ext)
		return &toolcore.Result{
			Success:    false,
			OutputKind: toolcore.KindText,
			Content:    content,
			ErrorCode:  code,
			Err:        fmt.Errorf("%s", content),
			Metadata: map[string]any{
				"file_path":          displayPath(call, rawTarget),
				"document_refused":   true,
				"document_extension": ext,
			},
		}, nil
	}
	target, failure := authorizeWrite(call, rawPath)
	if failure != nil {
		return failure, nil
	}
	// The host may know something the handler cannot: whether the model has
	// actually read the current bytes. A veto here happens before any I/O, so a
	// refused write leaves the file untouched.
	guardResult, readState := checkMutationGuard(call, h.def.Name, target, expectedOK)
	if guardResult != nil {
		return guardResult, nil
	}
	result, mutationErr := Write(target, content, WriteOptions{
		ExpectedRevision: expected,
		Mode:             DefaultFileMode,
		PreserveEncoding: true,
		RefuseBinary:     true,
		CreateParents:    true,
	})
	if mutationErr != nil {
		return mutationFailure(mutationErr), nil
	}
	display := displayPath(call, target)
	// Tell the host the write landed, if it keeps any per-session state about
	// what the model has seen. The mutation itself stays in this one handler.
	if observer, ok := call.Runtime.(toolcore.MutationObserver); ok {
		if raw, readErr := os.ReadFile(target); readErr == nil {
			observer.FileMutated(h.def.Name, target, raw)
		}
	}
	verb := "Updated"
	if result.Created {
		verb = "Created"
	} else if result.Replayed {
		verb = "Unchanged"
	}
	metadata := map[string]any{
		"file_path":     display,
		"bytes_written": result.NewSize,
		"old_size":      result.OldSize,
		"new_size":      result.NewSize,
		"created":       result.Created,
		"replayed":      result.Replayed,
		"encoding":      result.Encoding.String(),
		"old_sha256":    result.OldRevision,
		"new_sha256":    result.NewRevision,
	}
	if result.Replayed {
		// A replayed write changed nothing. Publishing the path as mutated would
		// tell a caller its edit landed when the file is byte-identical.
		metadata["idempotent_replay"] = true
		metadata["mutated_paths"] = []string{}
	} else {
		metadata["mutated_paths"] = []string{display}
	}
	// A unified diff of what changed is the cheapest way for a caller to verify
	// the edit it asked for; it costs one string and is bounded by the write
	// payload the caller just sent.
	if patch := UnifiedDiff(display, result.OldContent, result.NewContent); patch != "" {
		metadata["patch"] = patch
	}
	if expectedOK {
		metadata["expected_sha256"] = expected
	}
	for key, value := range readState {
		metadata[key] = value
	}
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    fmt.Sprintf("%s %s (%d bytes).", verb, display, result.NewSize),
		Metadata:   metadata,
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
	if ext := DocumentExtensionRefusal(target); ext != "" {
		content, code := DocumentRefusalContent(h.def.Name, "以文本追加到", target, ext)
		return &toolcore.Result{
			Success:    false,
			OutputKind: toolcore.KindText,
			Content:    content,
			ErrorCode:  code,
			Err:        fmt.Errorf("%s", content),
			Metadata: map[string]any{
				"file_path":          displayPath(call, target),
				"document_refused":   true,
				"document_extension": ext,
			},
		}, nil
	}
	guardResult, readState := checkMutationGuard(call, h.def.Name, target, false)
	if guardResult != nil {
		return guardResult, nil
	}
	createIfMissing, _ := call.Args["create_if_missing"].(bool)
	truncateFirst, _ := call.Args["truncate_first"].(bool)
	ensureTrailingNewline, _ := call.Args["ensure_trailing_newline"].(bool)
	expectedOffset, offsetOK, offsetFailure := expectedOffsetArg(call)
	if offsetFailure != nil {
		return offsetFailure, nil
	}
	if message := ValidateInlinePayload(h.def.Name, InlineField{Name: "content", Value: content}); message != "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "split the append into smaller calls", fmt.Errorf("%s", message)), nil
	}
	result, mutationErr := Append(ctx, target, content, AppendOptions{
		CreateIfMissing:       createIfMissing,
		TruncateFirst:         truncateFirst,
		EnsureTrailingNewline: ensureTrailingNewline,
		ExpectedOffset:        expectedOffset,
		Mode:                  DefaultFileMode,
		PreserveEncoding:      true,
		RefuseBinary:          true,
		CreateParents:         true,
	})
	if mutationErr != nil {
		return mutationFailure(mutationErr), nil
	}
	display := displayPath(call, target)
	if observer, ok := call.Runtime.(toolcore.MutationObserver); ok {
		if raw, readErr := os.ReadFile(target); readErr == nil {
			observer.FileMutated(h.def.Name, target, raw)
		}
	}
	verb := "Appended"
	action := "append"
	if truncateFirst {
		verb = "Wrote"
		action = "overwrite_first_chunk"
		if result.Created {
			action = "create_first_chunk"
		}
	}
	metadata := map[string]any{
		"file_path":      display,
		"action":         action,
		"bytes_appended": result.BytesWritten,
		"size_before":    result.SizeBefore,
		"size_after":     result.SizeAfter,
		"total_bytes":    result.SizeAfter,
		"created":        result.Created,
		"replayed":       result.Replayed,
		"encoding":       result.Encoding.String(),
		"truncate_first": truncateFirst,
		"appended":       !truncateFirst,
	}
	if result.Replayed {
		metadata["idempotent_replay"] = true
		metadata["mutated_paths"] = []string{}
	} else {
		metadata["mutated_paths"] = []string{display}
	}
	if offsetOK {
		metadata["expected_offset"] = expectedOffset
	}
	for key, value := range readState {
		metadata[key] = value
	}
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    fmt.Sprintf("%s %d bytes to %s (total %d bytes).", verb, result.BytesWritten, display, result.SizeAfter),
		Metadata:   metadata,
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
	if message := ValidateInlinePayload(h.def.Name,
		InlineField{Name: "old_string", Value: oldString},
		InlineField{Name: "new_string", Value: newString}); message != "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "split the edit into smaller targets", fmt.Errorf("%s", message)), nil
	}
	guardResult, readState := checkMutationGuard(call, h.def.Name, target, false)
	if guardResult != nil {
		return guardResult, nil
	}
	// Snapshot before the edit. A failed backup must not block the edit (the
	// caller asked for the change), but it is recorded so nobody assumes a
	// rollback point exists when it does not.
	backupPath := ""
	if shouldBackup(call, target) {
		if original, readErr := os.ReadFile(target); readErr == nil {
			if created, backupErr := CreateBackup(target, BackupDirName, original); backupErr == nil {
				backupPath = created
			}
		}
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
	if observer, ok := call.Runtime.(toolcore.MutationObserver); ok {
		if raw, readErr := os.ReadFile(target); readErr == nil {
			observer.FileMutated(h.def.Name, target, raw)
		}
	}
	return shapeResult(call, h.def.Name, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    fmt.Sprintf("Replaced %d occurrence(s) in %s (%d -> %d bytes).", result.Replacements, display, result.SizeBefore, result.SizeAfter),
		Metadata: mergeMetadata(map[string]any{
			"file_path":     display,
			"replacements":  result.Replacements,
			"bytes_before":  result.SizeBefore,
			"bytes_after":   result.SizeAfter,
			"encoding":      result.Encoding.String(),
			"mutated_paths": []string{display},
		}, readState, backupPath),
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
			// A patch that creates or rewrites a document container replaces it
			// with plain text, exactly like write would. The refusal has to be
			// inside Resolve so it applies to every path the patch names.
			if ext := DocumentExtensionRefusal(target); ext != "" {
				content, code := DocumentRefusalContent(h.def.Name, "以文本改写", target, ext)
				return "", &Error{Code: code, Message: content}
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
	// Stat, not Lstat: the write lands on the link target, so the question is
	// what that target is. Using Lstat made every symlink look like a special
	// file (blocking writes through a plain-text alias) and - worse - skipped
	// the "is this a regular file" question for whatever the link pointed at.
	info, err := os.Stat(absPath)
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
	target, failure := resolveWriteTarget(call, rawPath)
	if failure != nil {
		return "", failure
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpWrite, target); err != nil {
			return "", toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
		}
	}
	if err := ValidateWriteTarget(target); err != nil {
		// A directory target is a kind mismatch with a specific recovery route
		// (find the file the caller meant); everything else keeps its own code.
		if needsKindHint(err) {
			return "", kindMismatchFailure(call, rawPath, target, err)
		}
		return "", mutationFailure(err)
	}
	return target, nil
}

func needsKindHint(err error) bool {
	var coded *Error
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code == CodeInvalidArgument && strings.Contains(coded.Message, "is a directory")
}

// kindMismatchFailure adds the same candidate hint the local tool has always
// produced when a path exists but is not the kind the caller expected.
func kindMismatchFailure(call *toolcore.Call, rawPath, absPath string, cause error) *toolcore.Result {
	message := fmt.Sprintf("路径是目录，不是文件: %s", displayPath(call, absPath))
	if hinter, ok := call.Runtime.(toolcore.KindHinter); ok {
		if hint := hinter.KindHint(rawPath); hint != "" {
			message += "\n" + hint
		}
	}
	code := CodeInvalidArgument
	return &toolcore.Result{
		Success:    false,
		OutputKind: toolcore.KindText,
		Content:    message,
		ErrorCode:  code,
		NextAction: "pass a file path",
		Err:        fmt.Errorf("%s", message),
	}
}

// resolveWriteTarget resolves the argument without validating the target's kind
// or the parent directory. Callers that need to refuse on a more specific
// ground (a document container) use this first, so the refusal names the real
// problem instead of a downstream prerequisite.
func resolveWriteTarget(call *toolcore.Call, rawPath string) (string, *toolcore.Result) {
	if failure := runtimeFailure(call); failure != nil {
		return "", failure
	}
	paths := call.Runtime.Paths()
	target, err := paths.Resolve(rawPath)
	if err != nil {
		return "", toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
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

// expectedRevisionArg reads the optional expected_sha256 precondition. An
// empty value means "no precondition"; anything that is not a 64-character
// SHA-256 or `absent` is a caller mistake, not a mismatch.
func expectedRevisionArg(call *toolcore.Call) (string, bool, *toolcore.Result) {
	if call == nil || len(call.Args) == 0 {
		return "", false, nil
	}
	raw, present := call.Args["expected_sha256"]
	if !present || raw == nil {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", false, toolcore.Error(toolcore.ErrInvalidArgument,
			"pass a 64-character SHA-256 or absent",
			fmt.Errorf("expected_sha256 必须是 64 位十六进制 SHA-256 或 absent"))
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if !ValidRevision(value) {
		return "", false, toolcore.Error(toolcore.ErrInvalidArgument,
			"pass a 64-character SHA-256 or absent",
			fmt.Errorf("expected_sha256 必须是 64 位十六进制 SHA-256 或 absent"))
	}
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}

// checkMutationGuard asks the host whether this mutation is allowed, and for
// whatever it can say about the target's read state. A missing guard means
// "allowed, nothing to add": surfaces without session state have no ledger.
func checkMutationGuard(call *toolcore.Call, tool, absPath string, hasExplicitPrecondition bool) (*toolcore.Result, map[string]any) {
	if call == nil || call.Runtime == nil {
		return nil, nil
	}
	guard, ok := call.Runtime.(toolcore.MutationGuard)
	if !ok {
		return nil, nil
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		// A missing file is a valid new-file state with nothing to guard.
		return nil, nil
	}
	return guard.CheckMutation(tool, absPath, raw, hasExplicitPrecondition)
}

// expectedOffsetArg reads the optional expected_offset precondition used by the
// chunked append contract. A negative value is a caller mistake; a missing one
// means "no precondition".
func expectedOffsetArg(call *toolcore.Call) (*int64, bool, *toolcore.Result) {
	if call == nil || len(call.Args) == 0 {
		return nil, false, nil
	}
	raw, present := call.Args["expected_offset"]
	if !present || raw == nil {
		return nil, false, nil
	}
	value, ok := int64Arg(raw)
	if !ok {
		return nil, false, toolcore.Error(toolcore.ErrInvalidArgument,
			"pass a non-negative integer expected_offset",
			fmt.Errorf("expected_offset 必须是非负整数"))
	}
	if value < 0 {
		return nil, false, toolcore.Error(toolcore.ErrInvalidArgument,
			"pass a non-negative integer expected_offset",
			fmt.Errorf("expected_offset 必须是非负整数"))
	}
	return &value, true, nil
}

func int64Arg(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	default:
		return 0, false
	}
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
	// A precondition failure is not a generic mutation error: callers assert on
	// it (runtimeerrors.ErrWritePrecondition) to decide whether to re-read and
	// retry. Carry the stable identity alongside the code so that check keeps
	// working across the local and remote surfaces.
	cause := fmt.Errorf("%s", content)
	if code == CodePrecondition {
		// Keep the stable identity visible to errors.Is while the text stays the
		// classified, model-facing sentence.
		cause = &preconditionError{message: content}
	}
	return &toolcore.Result{
		Success:    false,
		OutputKind: toolcore.KindText,
		Content:    content,
		ErrorCode:  code,
		NextAction: nextAction,
		// The legacy MCP projection renders Result.Err when it is non-nil, so
		// carry the classified text here instead of the bare mutatecore message.
		Err: cause,
	}
}

// mergeMetadata folds optional annotation maps into the base metadata.
func mergeMetadata(base map[string]any, extras ...any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for _, extra := range extras {
		switch typed := extra.(type) {
		case map[string]any:
			for key, value := range typed {
				base[key] = value
			}
		case string:
			if typed != "" {
				base["backup_path"] = typed
			}
		}
	}
	return base
}

// shouldBackup reports whether this call asked for a rollback point. The host
// decides whether one is needed (a git work tree already has one).
func shouldBackup(call *toolcore.Call, absPath string) bool {
	if call == nil || call.Runtime == nil {
		// No host policy means no rollback point: writing .backups/ next to a
		// file on a surface that never reads it is just clutter.
		return false
	}
	if policy, ok := call.Runtime.(toolcore.BackupPolicy); ok {
		return policy.WantsBackup(absPath)
	}
	return false
}

// preconditionError is the sentinel-carrying precondition failure. Unwrap
// exposes the shared identity so a caller can test for it without parsing text.
type preconditionError struct {
	message string
}

func (e *preconditionError) Error() string        { return e.message }
func (e *preconditionError) Is(target error) bool { return target == PreconditionError }

// PreconditionError is the identity every precondition failure matches.
var PreconditionError = fmt.Errorf("write precondition failed")

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
