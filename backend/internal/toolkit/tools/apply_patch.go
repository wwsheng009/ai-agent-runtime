package tools

import (
	"context"
	"errors"
	"fmt"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	applyPatchBeginMarker  = "*** Begin Patch"
	applyPatchEndMarker    = "*** End Patch"
	applyPatchEOFMarker    = "*** End of File"
	applyPatchUpdatePrefix = "*** Update File: "
	applyPatchAddPrefix    = "*** Add File: "
	applyPatchDeletePrefix = "*** Delete File: "
	applyPatchMoveToPrefix = "*** Move to: "
	defaultPatchedFileMode = 0o644
)

const applyPatchLarkGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change_move? change?

filename: /(.+)/
add_line: "+" /(.*)/ LF -> line

change_move: "*** Move to: " filename LF
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF

%import common.LF`

// ApplyPatchTool applies Codex-style patch payloads directly to workspace files.
type ApplyPatchTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// NewApplyPatchTool creates a real apply_patch tool for workspace edits.
func NewApplyPatchTool() *ApplyPatchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patch": map[string]interface{}{
				"type":        "string",
				"description": "要应用的补丁文本。普通函数调用模式下放入 patch 参数；Codex/freeform 模式下直接发送补丁文本，不要包 JSON。必须使用 Codex apply_patch 格式，例如 *** Begin Patch / *** Update File / *** Add File / *** Delete File / *** End Patch。若补丁很大或包含多个独立变更，请拆分为多个更小的补丁块，每次只聚焦一个文件或一个变更区域，避免单次参数过长导致截断。",
			},
		},
		"required": []string{"patch"},
	}

	return &ApplyPatchTool{
		BaseTool: toolkit.NewBaseTool(
			"apply_patch",
			"应用 Codex 风格补丁到工作区文件，支持新增、更新、删除和重命名文件。Codex/custom-tool 模式下这是 FREEFORM 工具，应直接发送补丁文本，不要包 JSON；普通函数调用模式使用 patch 参数。若需要处理很大的变更或多个独立目标，请拆分为多个更小的 apply_patch 调用，每次只聚焦一个文件或一个变更区域，避免单次参数过大导致截断。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

func (t *ApplyPatchTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		"freeform": map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": applyPatchLarkGrammar,
		},
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        true,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
	}
}

// Execute implements the Tool interface.
func (t *ApplyPatchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}

	rawPatch, ok := params["patch"].(string)
	if (!ok || strings.TrimSpace(rawPatch) == "") && len(params) > 0 {
		if raw, ok := params["_raw"].(string); ok {
			rawPatch = raw
		}
	}
	if strings.TrimSpace(rawPatch) == "" {
		return toolResultFailureWithCode(
			fmt.Errorf("patch 参数缺失或为空"),
			string(runtimeerrors.ErrToolInvalidArgs),
			"Provide a non-empty Codex apply_patch text in the patch field (Begin Patch ... End Patch). Do not send empty or whitespace-only patch content.",
			map[string]interface{}{"failure_class": "invalid_patch_syntax"},
		), nil
	}

	operations, err := parseApplyPatch(rawPatch)
	if err != nil {
		return applyPatchToolFailure(err, ""), nil
	}
	if len(operations) == 0 {
		return toolResultFailureWithCode(
			fmt.Errorf("补丁中没有可执行的文件操作"),
			string(runtimeerrors.ErrToolInvalidArgs),
			"Include at least one Add/Update/Delete/Move File operation between *** Begin Patch and *** End Patch.",
			map[string]interface{}{"failure_class": "invalid_patch_syntax"},
		), nil
	}

	applier := &patchApplier{
		tool:  t,
		files: make(map[string]*stagedFile, len(operations)*2),
		ctx:   ctx,
	}
	summary := patchSummary{}

	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return toolResultFailure(err, "Context canceled or timed out while applying the patch. Retry with a smaller focused patch if the deadline is tight."), nil
		}
		if err := applier.apply(operation, &summary); err != nil {
			return applyPatchToolFailure(err, operation.Path), nil
		}
	}

	if err := applier.commit(); err != nil {
		return applyPatchToolFailure(err, ""), nil
	}

	mutatedPaths, combinedPatch := applier.diff()
	// Phase 5 变更源 1（edit hook）：补丁已落盘，同步标记全部被写路径。
	notifyFileChanges(ctx, mutatedPaths...)
	summary.Files = len(mutatedPaths)
	message := summary.message()
	if strings.TrimSpace(combinedPatch) != "" {
		message = formatEditSuccessContent(message, combinedPatch)
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    message,
		Metadata: map[string]interface{}{
			"patch":         combinedPatch,
			"files":         summary.Files,
			"created_files": summary.Created,
			"updated_files": summary.Updated,
			"deleted_files": summary.Deleted,
			"moved_files":   summary.Moved,
			"mutated_paths": mutatedPaths,
		},
	}, nil
}

type patchSummary struct {
	Files   int
	Created int
	Updated int
	Deleted int
	Moved   int
}

func (s patchSummary) message() string {
	parts := make([]string, 0, 5)
	if s.Created > 0 {
		parts = append(parts, fmt.Sprintf("新增 %d", s.Created))
	}
	if s.Updated > 0 {
		parts = append(parts, fmt.Sprintf("修改 %d", s.Updated))
	}
	if s.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("删除 %d", s.Deleted))
	}
	if s.Moved > 0 {
		parts = append(parts, fmt.Sprintf("移动 %d", s.Moved))
	}
	if len(parts) == 0 {
		parts = append(parts, "无变更")
	}
	return fmt.Sprintf("补丁已应用：%s；影响 %d 个路径", strings.Join(parts, "，"), s.Files)
}

type patchOperationKind string

const (
	patchOperationAdd    patchOperationKind = "add"
	patchOperationDelete patchOperationKind = "delete"
	patchOperationUpdate patchOperationKind = "update"
)

type patchOperation struct {
	Kind           patchOperationKind
	Path           string
	MoveTo         string
	AddLines       []string
	NoFinalNewline bool
	Hunks          []patchHunk
}

type patchHunk struct {
	Header    string
	Lines     []patchHunkLine
	EndOfFile bool
}

type patchHunkLine struct {
	Kind byte
	Text string
}

func parseApplyPatch(input string) ([]patchOperation, error) {
	normalizedInput := mutatecore.NormalizePatchEnvelope(strings.TrimSpace(strings.ReplaceAll(input, "\r\n", "\n")))
	lines := strings.Split(normalizedInput, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != applyPatchBeginMarker {
		return nil, fmt.Errorf("补丁必须以 %q 开始", applyPatchBeginMarker)
	}

	operations := make([]patchOperation, 0, 4)
	for index := 1; index < len(lines); {
		line := strings.TrimRight(lines[index], "\r")
		headerLine := strings.TrimSpace(line)
		switch {
		case headerLine == "":
			index++
		case headerLine == applyPatchEndMarker:
			// Models frequently append commentary or truncated prose after the
			// end marker. Operations already collected are complete, so ignore
			// trailing noise instead of failing the whole patch.
			return operations, nil
		case strings.HasPrefix(headerLine, applyPatchAddPrefix):
			operation, next, err := parseAddFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(headerLine, applyPatchDeletePrefix):
			operation, next, err := parseDeleteFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(headerLine, applyPatchUpdatePrefix):
			operation, next, err := parseUpdateFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		default:
			return nil, fmt.Errorf("第 %d 行不是合法的补丁操作头: %s。操作头必须是 %q / %q / %q / %q 之一", index+1, truncateDiagnosticText(line, 120), applyPatchAddPrefix, applyPatchUpdatePrefix, applyPatchDeletePrefix, applyPatchEndMarker)
		}
	}

	if len(operations) > 0 {
		// Tolerate truncated model output that finished all file operations
		// but dropped the end marker (common on large patches).
		return operations, nil
	}
	return nil, fmt.Errorf("补丁缺少 %q 结束标记，且没有可执行的文件操作", applyPatchEndMarker)
}

func isOnlyStarsAndSpaces(s string) bool {
	for _, r := range s {
		if r != '*' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func isApplyPatchBoundaryLine(line, marker string) bool {
	line = strings.TrimSpace(line)
	if line == marker {
		return true
	}
	// Accept common model variants: "*** Begin Patch ***", "*** Begin Patch***"
	if !strings.HasPrefix(line, marker) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, marker))
	if rest == "" {
		return true
	}
	for _, r := range rest {
		if r != '*' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func parseAddFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := sanitizeApplyPatchPath(strings.TrimPrefix(strings.TrimSpace(lines[start]), applyPatchAddPrefix))
	if path == "" {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少新增文件路径", start+1)
	}

	operation := patchOperation{Kind: patchOperationAdd, Path: path}
	index := start + 1
	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == applyPatchEndMarker:
			if len(operation.AddLines) == 0 {
				return patchOperation{}, 0, fmt.Errorf("第 %d 行的新增文件没有内容", start+1)
			}
			return operation, index, nil
		case isApplyPatchEOFMarkerLine(line):
			operation.NoFinalNewline = true
			index++
		case strings.HasPrefix(line, "+"):
			operation.AddLines = append(operation.AddLines, line[1:])
			index++
		case line == "" || isApplyPatchBareAddContentLine(line):
			// Models often omit the required "+" prefix for Add File bodies.
			// Treat plain content lines as added lines to avoid hard failures.
			operation.AddLines = append(operation.AddLines, line)
			index++
		default:
			return patchOperation{}, 0, fmt.Errorf("第 %d 行不是合法的新增文件内容: %s。Add File 内容行应以 '+' 开头，例如 '+package foo'", index+1, truncateDiagnosticText(line, 120))
		}
	}

	if len(operation.AddLines) == 0 {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行的新增文件没有内容", start+1)
	}
	return operation, index, nil
}

func isApplyPatchBareAddContentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	if isApplyPatchBoundaryLine(trimmed, applyPatchBeginMarker) || isApplyPatchBoundaryLine(trimmed, applyPatchEndMarker) {
		return false
	}
	if isPatchSectionHeader(line) || isApplyPatchEOFMarkerLine(line) || strings.HasPrefix(trimmed, applyPatchMoveToPrefix) {
		return false
	}
	// Keep strict rejection for obvious non-content markers.
	if strings.HasPrefix(trimmed, "***") {
		return false
	}
	return true
}

// isApplyPatchEOFMarkerLine accepts the grammar form and common model variants
// such as "*** End of File ***" / "*** End of File***".
func isApplyPatchEOFMarkerLine(line string) bool {
	return isApplyPatchBoundaryLine(line, applyPatchEOFMarker)
}

func parseDeleteFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := sanitizeApplyPatchPath(strings.TrimPrefix(strings.TrimSpace(lines[start]), applyPatchDeletePrefix))
	if path == "" {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少删除文件路径", start+1)
	}
	return patchOperation{
		Kind: patchOperationDelete,
		Path: path,
	}, start + 1, nil
}

func parseUpdateFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := sanitizeApplyPatchPath(strings.TrimPrefix(strings.TrimSpace(lines[start]), applyPatchUpdatePrefix))
	if path == "" {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少更新文件路径", start+1)
	}

	operation := patchOperation{
		Kind: patchOperationUpdate,
		Path: path,
	}
	index := start + 1
	if index < len(lines) && strings.HasPrefix(lines[index], applyPatchMoveToPrefix) {
		operation.MoveTo = sanitizeApplyPatchPath(strings.TrimPrefix(lines[index], applyPatchMoveToPrefix))
		if operation.MoveTo == "" {
			return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少移动目标路径", index+1)
		}
		index++
	}

	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == applyPatchEndMarker:
			if operation.MoveTo == "" && len(operation.Hunks) == 0 {
				return patchOperation{}, 0, fmt.Errorf("第 %d 行的更新文件没有内容变更", start+1)
			}
			return operation, index, nil
		case strings.TrimSpace(line) == "":
			index++
		case strings.HasPrefix(line, "@@"):
			hunk, next, err := parsePatchHunk(lines, index)
			if err != nil {
				return patchOperation{}, 0, err
			}
			operation.Hunks = append(operation.Hunks, hunk)
			index = next
		case len(operation.Hunks) == 0 && isPatchChangeLine(line):
			hunk, next, err := parsePatchHunkWithoutHeader(lines, index)
			if err != nil {
				return patchOperation{}, 0, err
			}
			operation.Hunks = append(operation.Hunks, hunk)
			index = next
		default:
			return patchOperation{}, 0, fmt.Errorf("第 %d 行不是合法的 hunk 头: %s", index+1, line)
		}
	}

	if operation.MoveTo == "" && len(operation.Hunks) == 0 {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行的更新文件没有内容变更", start+1)
	}
	return operation, index, nil
}

func parsePatchHunk(lines []string, start int) (patchHunk, int, error) {
	return parsePatchHunkBody(lines, start+1, strings.TrimRight(lines[start], "\r"), start)
}

func parsePatchHunkWithoutHeader(lines []string, start int) (patchHunk, int, error) {
	return parsePatchHunkBody(lines, start, "@@", start)
}

func parsePatchHunkBody(lines []string, index int, header string, headerLine int) (patchHunk, int, error) {
	hunk := patchHunk{Header: header}
	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case isApplyPatchEOFMarkerLine(line):
			hunk.EndOfFile = true
			index++
			return hunk, index, nil
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == applyPatchEndMarker || strings.HasPrefix(line, "@@"):
			if len(hunk.Lines) == 0 {
				return patchHunk{}, 0, fmt.Errorf("第 %d 行的 hunk 没有内容", headerLine+1)
			}
			return hunk, index, nil
		case len(line) == 0:
			hunk.Lines = append(hunk.Lines, patchHunkLine{
				Kind: ' ',
				Text: "",
			})
			index++
		default:
			prefix := line[0]
			if prefix != ' ' && prefix != '+' && prefix != '-' {
				return patchHunk{}, 0, fmt.Errorf("第 %d 行不是合法的 hunk 内容: %s", index+1, line)
			}
			hunk.Lines = append(hunk.Lines, patchHunkLine{
				Kind: prefix,
				Text: line[1:],
			})
			index++
		}
	}

	if len(hunk.Lines) == 0 {
		return patchHunk{}, 0, fmt.Errorf("第 %d 行的 hunk 没有内容", headerLine+1)
	}
	return hunk, index, nil
}

func isPatchChangeLine(line string) bool {
	if line == "" {
		return true
	}
	prefix := line[0]
	return prefix == ' ' || prefix == '+' || prefix == '-'
}

func isPatchSectionHeader(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, applyPatchUpdatePrefix) ||
		strings.HasPrefix(line, applyPatchAddPrefix) ||
		strings.HasPrefix(line, applyPatchDeletePrefix)
}

type patchApplier struct {
	tool  *ApplyPatchTool
	files map[string]*stagedFile
	ctx   context.Context
}

type stagedFile struct {
	Path            string
	Exists          bool
	Content         string
	Mode            fs.FileMode
	OriginalExists  bool
	OriginalContent string
	OriginalMode    fs.FileMode
	Dirty           bool
}

func (a *patchApplier) apply(operation patchOperation, summary *patchSummary) error {
	switch operation.Kind {
	case patchOperationAdd:
		if err := a.applyAdd(operation); err != nil {
			return err
		}
		summary.Created++
	case patchOperationDelete:
		if err := a.applyDelete(operation); err != nil {
			return err
		}
		summary.Deleted++
	case patchOperationUpdate:
		moved, err := a.applyUpdate(operation)
		if err != nil {
			return err
		}
		if moved {
			summary.Moved++
		} else {
			summary.Updated++
		}
	default:
		return fmt.Errorf("不支持的补丁操作: %s", operation.Kind)
	}
	return nil
}

func (a *patchApplier) applyAdd(operation patchOperation) error {
	absPath, err := a.resolvePath(operation.Path, runtimeexecutor.OpWrite)
	if err != nil {
		return err
	}
	file, err := a.load(absPath)
	if err != nil {
		return err
	}
	if file.Exists {
		return fmt.Errorf("文件已存在，无法新增: %s", absPath)
	}

	file.Exists = true
	file.Content = joinPatchLines(operation.AddLines, !operation.NoFinalNewline, "\n")
	file.Mode = defaultPatchedFileMode
	file.Dirty = true
	return nil
}

func (a *patchApplier) applyDelete(operation patchOperation) error {
	absPath, err := a.resolvePath(operation.Path, runtimeexecutor.OpDelete)
	if err != nil {
		return err
	}
	file, err := a.load(absPath)
	if err != nil {
		return err
	}
	if !file.Exists {
		return a.tool.buildPathNotFoundError(a.ctx, "文件不存在，无法删除", operation.Path)
	}

	file.Exists = false
	file.Content = ""
	file.Dirty = true
	return nil
}

func (a *patchApplier) applyUpdate(operation patchOperation) (bool, error) {
	sourcePath, err := a.resolvePath(operation.Path, runtimeexecutor.OpWrite)
	if err != nil {
		return false, err
	}
	source, err := a.load(sourcePath)
	if err != nil {
		return false, err
	}
	if !source.Exists {
		return false, a.tool.buildPathNotFoundError(a.ctx, "文件不存在，无法更新", operation.Path)
	}

	content := source.Content
	if len(operation.Hunks) > 0 {
		content, err = applyPatchHunks(content, operation.Hunks)
		if err != nil {
			return false, fmt.Errorf("更新文件 %s 失败: %w", sourcePath, err)
		}
	}

	if strings.TrimSpace(operation.MoveTo) == "" {
		source.Content = content
		source.Dirty = true
		return false, nil
	}

	targetPath, err := a.resolvePath(operation.MoveTo, runtimeexecutor.OpWrite)
	if err != nil {
		return false, err
	}
	if targetPath == sourcePath {
		source.Content = content
		source.Dirty = true
		return false, nil
	}

	if err := a.tool.checkPath(runtimeexecutor.OpDelete, sourcePath); err != nil {
		return false, err
	}

	target, err := a.load(targetPath)
	if err != nil {
		return false, err
	}
	if target.Exists {
		return false, fmt.Errorf("移动目标已存在: %s", targetPath)
	}

	source.Exists = false
	source.Content = ""
	source.Dirty = true

	target.Exists = true
	target.Content = content
	target.Mode = source.Mode
	if target.Mode == 0 {
		target.Mode = defaultPatchedFileMode
	}
	target.Dirty = true
	return true, nil
}

func (a *patchApplier) resolvePath(targetPath string, op runtimeexecutor.PermissionOp) (string, error) {
	resolved := a.tool.resolvePathWithContext(a.ctx, targetPath)
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("解析补丁路径失败 %q: %w", targetPath, err)
	}
	if err := a.tool.checkPath(op, absPath); err != nil {
		return "", err
	}
	return absPath, nil
}

// sanitizeApplyPatchPath trims whitespace and common model punctuation that
// leaks into Update/Add/Delete File headers (live residual: snippet.go", from
// mid-sentence / JSON / markdown fragments). Does not rewrite valid
// relative/absolute paths or intentional spaces inside names.
func sanitizeApplyPatchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	// Strip balanced outer quotes once: "foo.go" / 'foo.go' / `foo.go`.
	if len(path) >= 2 {
		first, last := path[0], path[len(path)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			path = strings.TrimSpace(path[1 : len(path)-1])
		}
	}
	// Trailing punctuation common when models paste path + sentence/JSON crumbs.
	for {
		trimmed := strings.TrimRight(path, " \t\"'`.,;:)]}>")
		if trimmed == path {
			break
		}
		path = strings.TrimSpace(trimmed)
	}
	// Leading quote crumbs after unbalanced paste.
	path = strings.TrimLeft(path, " \t\"'`([{<,")
	return strings.TrimSpace(path)
}

func (a *patchApplier) load(path string) (*stagedFile, error) {
	if file, ok := a.files[path]; ok {
		return file, nil
	}

	file := &stagedFile{
		Path: path,
		Mode: defaultPatchedFileMode,
	}
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.IsDir() {
			return nil, a.tool.buildPathKindMismatchError(a.ctx, "路径是目录，不支持补丁操作", path)
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("读取文件失败 %s: %w", path, readErr)
		}
		file.Exists = true
		file.Content = string(content)
		file.Mode = info.Mode().Perm()
		file.OriginalExists = true
		file.OriginalContent = file.Content
		file.OriginalMode = file.Mode
	case os.IsNotExist(err):
		// Leave as zero state for staged creation.
	default:
		return nil, fmt.Errorf("访问文件失败 %s: %w", path, err)
	}

	a.files[path] = file
	return file, nil
}

func (a *patchApplier) commit() error {
	paths := make([]string, 0, len(a.files))
	for path, file := range a.files {
		if file.Dirty {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	for _, path := range paths {
		file := a.files[path]
		if file == nil || !file.Dirty {
			continue
		}
		if !file.Exists {
			if file.OriginalExists {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("删除文件失败 %s: %w", path, err)
				}
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("创建目录失败 %s: %w", filepath.Dir(path), err)
		}
		mode := file.Mode
		if mode == 0 {
			mode = defaultPatchedFileMode
		}
		if err := writeFileAtomicLocal(path, []byte(file.Content), mode); err != nil {
			return fmt.Errorf("写入文件失败 %s: %w", path, err)
		}
	}
	return nil
}

func (a *patchApplier) diff() ([]string, string) {
	paths := make([]string, 0, len(a.files))
	for path, file := range a.files {
		if file != nil && file.Dirty {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	patches := make([]string, 0, len(paths))
	for _, path := range paths {
		file := a.files[path]
		if file == nil {
			continue
		}
		var before *string
		var after *string
		if file.OriginalExists {
			before = &file.OriginalContent
		}
		if file.Exists {
			content := file.Content
			after = &content
		}
		if patch := buildUnifiedPatchFromStates(path, before, after); patch != "" {
			patches = append(patches, patch)
		}
	}

	return paths, strings.Join(patches, "")
}

func applyPatchHunks(content string, hunks []patchHunk) (string, error) {
	coreHunks := make([]mutatecore.PatchHunk, 0, len(hunks))
	for _, hunk := range hunks {
		lines := make([]mutatecore.PatchHunkLine, 0, len(hunk.Lines))
		for _, line := range hunk.Lines {
			lines = append(lines, mutatecore.PatchHunkLine{Kind: line.Kind, Text: line.Text})
		}
		coreHunks = append(coreHunks, mutatecore.PatchHunk{
			Header:    hunk.Header,
			Lines:     lines,
			EndOfFile: hunk.EndOfFile,
		})
	}
	result, err := mutatecore.ApplyHunks(content, coreHunks)
	if err == nil {
		return result, nil
	}
	var hunkErr *mutatecore.PatchHunkError
	if errors.As(err, &hunkErr) {
		return "", buildPatchHunkNotFoundError(
			patchHunk{Header: hunkErr.Header},
			hunkErr.Expected,
			hunkErr.Actual,
			"未找到期望旧内容",
		)
	}
	return "", err
}

func normalizePatchComparableLine(line string) string {
	return mutatecore.NormalizeComparableLine(line)
}

func hunkChangeContextLine(header string) string {
	header = strings.TrimRight(header, "\r")
	if header == "@@" {
		return ""
	}
	if !strings.HasPrefix(header, "@@") {
		return ""
	}
	// Codex style: "@@ func Foo()" → context is "func Foo()".
	// Unified-diff style models often emit "@@ -185,6 +185,12 @@" or
	// "@@ -185,6 +185,12 @@ func Foo" — line-number spans must not be used as
	// literal context lines (they never exist in the file).
	rest := strings.TrimSpace(strings.TrimPrefix(header, "@@"))
	if rest == "" {
		return ""
	}
	// Strip optional trailing "@@" from unified headers.
	if strings.HasSuffix(rest, "@@") {
		rest = strings.TrimSpace(strings.TrimSuffix(rest, "@@"))
	}
	if rest == "" {
		return ""
	}
	// Pure unified range: "-185,6 +185,12" or "-10 +10".
	if isUnifiedDiffRangeToken(rest) {
		return ""
	}
	// Mixed: "-185,6 +185,12 @@ func Foo" or "-185,6 +185,12 func Foo".
	if stripped := stripLeadingUnifiedDiffRanges(rest); stripped != rest {
		return stripped
	}
	// Remaining non-range text is Codex function/class context.
	return rest
}

func isUnifiedDiffRangeToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Must look like one or two hunk ranges: -N[,M] [+N[,M]]
	parts := strings.Fields(s)
	if len(parts) == 0 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !isUnifiedDiffRangePart(part) {
			return false
		}
	}
	return true
}

func isUnifiedDiffRangePart(part string) bool {
	if part == "" {
		return false
	}
	if part[0] != '-' && part[0] != '+' {
		return false
	}
	body := part[1:]
	if body == "" {
		return false
	}
	// N or N,M
	comma := strings.IndexByte(body, ',')
	if comma < 0 {
		return isAllDigits(body)
	}
	return isAllDigits(body[:comma]) && isAllDigits(body[comma+1:])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func stripLeadingUnifiedDiffRanges(s string) string {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return s
	}
	i := 0
	for i < len(parts) && isUnifiedDiffRangePart(parts[i]) {
		i++
	}
	if i == 0 {
		return s
	}
	if i >= len(parts) {
		return ""
	}
	// Drop a lone "@@" token if present after ranges.
	if parts[i] == "@@" {
		i++
	}
	if i >= len(parts) {
		return ""
	}
	return strings.Join(parts[i:], " ")
}

func buildPatchHunkNotFoundError(hunk patchHunk, expected, actual []string, reason string) error {
	header := strings.TrimSpace(hunk.Header)
	if header == "" {
		header = "@@"
	}
	current := ""
	startLine, closest := closestPatchCurrentContext(actual, expected)
	if len(closest) > 0 {
		current = fmt.Sprintf(
			"\n最接近的当前内容（第 %d 行附近，可直接据此修正补丁）:\n%s",
			startLine,
			formatPatchCurrentLines(closest, startLine),
		)
	}
	message := fmt.Sprintf(
		"无法定位 hunk: %s；%s。next_action: 先用 view/grep 重读目标文件附近最新内容，按返回的“最接近的当前内容”重建补丁（一次只改一个文件/区域）；不要原样重试同一 stale @@/旧行。也可改用更短、更靠近目标的 @@ 上下文。\n期望内容:\n%s%s",
		header,
		reason,
		formatPatchExpectedLines(expected),
		current,
	)
	return &patchHunkNotFoundError{
		message:   message,
		startLine: startLine,
		closest:   closest,
	}
}

// patchHunkNotFoundError carries structured recovery fields for STALE_CONTEXT
// so apply_patch can promote current_snippet without re-parsing error text.
type patchHunkNotFoundError struct {
	message   string
	startLine int
	closest   []string
}

func (e *patchHunkNotFoundError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func extractPatchHunkNotFoundError(err error) *patchHunkNotFoundError {
	var target *patchHunkNotFoundError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

// applyPatchToolFailure classifies apply_patch failures, stamping STALE_CONTEXT
// when @@ / old-content no longer matches the workspace.
func applyPatchToolFailure(err error, filePath string) *toolkit.ToolResult {
	if err == nil {
		return toolResultFailure(fmt.Errorf("apply_patch failed"), applyPatchFailureNextAction(nil))
	}
	next := applyPatchFailureNextAction(err)
	extra := map[string]interface{}{}
	if path := strings.TrimSpace(filePath); path != "" {
		extra["file_path"] = path
	}
	if isApplyPatchStaleContextError(err) {
		extra["failure_class"] = "stale_context"
		// Prefer structured fields from patchHunkNotFoundError (parity with edit).
		if detail := extractPatchHunkNotFoundError(err); detail != nil && len(detail.closest) > 0 && detail.startLine > 0 {
			extra["suggested_view_offset"] = detail.startLine - 1 // view offset is 0-based
			extra["suggested_view_limit"] = 40
			// Structured copy-paste recovery: models often blind-retry apply_patch
			// after STALE; expose exact current lines so @@ / old lines can be rebuilt
			// without an extra view round-trip when the window is already known.
			extra["current_snippet"] = strings.Join(detail.closest, "\n")
			extra["current_snippet_start_line"] = detail.startLine
		} else if startLine := extractPatchSuggestedViewLine(err.Error()); startLine > 0 {
			// Fallback for wrapped / legacy messages that only embed line hints.
			extra["suggested_view_offset"] = startLine - 1
			extra["suggested_view_limit"] = 40
		}
		return toolResultFailureWithCode(err, string(runtimeerrors.ErrToolStaleContext), next, extra)
	}
	if isApplyPatchInvalidSyntaxError(err) {
		extra["failure_class"] = "invalid_patch_syntax"
		return toolResultFailureWithCode(err, string(runtimeerrors.ErrToolInvalidArgs), next, extra)
	}
	return toolResultFailureWithCode(err, "", next, extra)
}

func isApplyPatchStaleContextError(err error) bool {
	if err == nil {
		return false
	}
	if extractPatchHunkNotFoundError(err) != nil {
		return true
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "无法定位 hunk") ||
		strings.Contains(msg, "未找到期望旧内容") ||
		strings.Contains(msg, "未找到 @@ 上下文") ||
		(strings.Contains(lower, "hunk") && strings.Contains(lower, "stale")) ||
		strings.Contains(msg, "stale @@")
}

// isApplyPatchInvalidSyntaxError reports parse/format failures that should be
// TOOL_INVALID_ARGS (not bare TOOL_EXECUTION). Live residual: hunk lines that
// re-open Begin/End markers mid-body, or malformed headers, were generic
// execution fails with "Inspect the error details…".
func isApplyPatchInvalidSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	if isApplyPatchStaleContextError(err) {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "不是合法的 hunk"),
		strings.Contains(msg, "不是合法的补丁"),
		strings.Contains(msg, "不是合法的补丁操作头"),
		strings.Contains(msg, "不是合法的新增文件内容"),
		strings.Contains(msg, "hunk 没有内容"),
		strings.Contains(msg, "更新文件没有内容变更"),
		strings.Contains(msg, "新增文件没有内容"),
		strings.Contains(msg, "缺少新增文件路径"),
		strings.Contains(msg, "缺少更新文件路径"),
		strings.Contains(msg, "缺少删除文件路径"),
		strings.Contains(msg, "缺少移动目标路径"),
		strings.Contains(msg, "patch 参数缺失"),
		strings.Contains(msg, "补丁中没有可执行的文件操作"),
		strings.Contains(msg, "补丁必须以"),
		strings.Contains(msg, "补丁缺少"),
		strings.Contains(msg, "应以 '+' 开头"):
		return true
	case strings.Contains(lower, "invalid patch"),
		strings.Contains(lower, "malformed patch"),
		// Envelope marker guidance errors always mention Begin/End Patch.
		strings.Contains(msg, "Begin Patch") || strings.Contains(msg, "End Patch"):
		return true
	default:
		return false
	}
}

func extractPatchSuggestedViewLine(message string) int {
	// Matches "第 %d 行附近" from buildPatchHunkNotFoundError.
	const marker = "第 "
	idx := strings.Index(message, marker)
	if idx < 0 {
		return 0
	}
	rest := message[idx+len(marker):]
	end := strings.Index(rest, " 行")
	if end <= 0 {
		return 0
	}
	num := strings.TrimSpace(rest[:end])
	var line int
	for _, r := range num {
		if r < '0' || r > '9' {
			return 0
		}
		line = line*10 + int(r-'0')
	}
	return line
}

// applyPatchFailureNextAction extracts recovery guidance for structured metadata.
func applyPatchFailureNextAction(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case isApplyPatchStaleContextError(err):
		return "STALE_CONTEXT: copy exact lines from current_snippet / “最接近的当前内容” (or re-view with suggested_view_offset), rebuild a smaller focused patch from that text (one file/region), and do not retry the same stale @@/old lines unchanged."
	case strings.Contains(msg, "Add File") || strings.Contains(msg, "应以 '+' 开头"):
		return "For Add File hunks, every content line must start with '+'. Rewrite the add body with '+' prefixes, or use write/append_write for new files."
	case strings.Contains(msg, "不是合法的 hunk") || strings.Contains(msg, "hunk 没有内容") || strings.Contains(msg, "更新文件没有内容变更"):
		return "TOOL_INVALID_ARGS: fix apply_patch syntax — each Update File needs @@ hunk headers and +/-/ context lines only (no nested Begin/End markers inside a hunk). Prefer one file/region; rebuild from a fresh view of the target."
	case strings.Contains(msg, "不是合法的补丁") || strings.Contains(msg, "Begin Patch") || strings.Contains(msg, "End Patch"):
		return "Use Codex apply_patch markers: *** Begin Patch / *** Update|Add|Delete File / *** End Patch. Keep freeform patch text outside JSON when possible."
	case strings.Contains(msg, "文件不存在") || strings.Contains(lower, "no such file") || strings.Contains(msg, "访问文件失败") || strings.Contains(msg, "解析补丁路径失败"):
		return "Confirm the target path with ls/glob/view first; fix path typos or create missing parents before retrying apply_patch."
	default:
		return "Inspect the apply_patch error, re-read the target file with view/grep, and retry with a smaller focused patch. Prefer one file/region per call."
	}
}

func formatPatchExpectedLines(lines []string) string {
	if len(lines) == 0 {
		return "(空旧内容)"
	}
	const maxLines = 12
	limit := len(lines)
	if limit > maxLines {
		limit = maxLines
	}
	preview := make([]string, 0, limit+1)
	for _, line := range lines[:limit] {
		preview = append(preview, truncateDiagnosticText(line, 200))
	}
	if len(lines) > limit {
		preview = append(preview, fmt.Sprintf("... 省略 %d 行", len(lines)-limit))
	}
	return strings.Join(preview, "\n")
}

func closestPatchCurrentContext(actual, expected []string) (int, []string) {
	return mutatecore.ClosestPatchWindow(actual, expected)
}

func formatPatchCurrentLines(lines []string, startLine int) string {
	formatted := make([]string, 0, len(lines))
	for index, line := range lines {
		// Keep full line text (no mid-line truncation) so models / offline
		// rehydrate can copy exact current_snippet bytes including long
		// identifiers; only cap total line count upstream.
		formatted = append(formatted, fmt.Sprintf("%6d|%s", startLine+index, line))
	}
	return strings.Join(formatted, "\n")
}

func truncateDiagnosticText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func joinPatchLines(lines []string, trailingNewline bool, newline string) string {
	if len(lines) == 0 {
		if trailingNewline {
			return newline
		}
		return ""
	}
	content := strings.Join(lines, newline)
	if trailingNewline {
		content += newline
	}
	return content
}
