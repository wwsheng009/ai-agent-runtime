package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
				"description": "要应用的补丁文本。必须使用 Codex apply_patch 格式，例如 *** Begin Patch / *** Update File / *** Add File / *** Delete File / *** End Patch。",
			},
		},
		"required": []string{"patch"},
	}

	return &ApplyPatchTool{
		BaseTool: toolkit.NewBaseTool(
			"apply_patch",
			"应用 Codex 风格补丁到工作区文件，支持新增、更新、删除和重命名文件。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

// Execute implements the Tool interface.
func (t *ApplyPatchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	rawPatch, ok := params["patch"].(string)
	if !ok || strings.TrimSpace(rawPatch) == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("patch 参数缺失或为空"),
		}, nil
	}

	operations, err := parseApplyPatch(rawPatch)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	if len(operations) == 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("补丁中没有可执行的文件操作"),
		}, nil
	}

	applier := &patchApplier{
		tool:  t,
		files: make(map[string]*stagedFile, len(operations)*2),
	}
	summary := patchSummary{}

	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      err,
			}, nil
		}
		if err := applier.apply(operation, &summary); err != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      err,
			}, nil
		}
	}

	if err := applier.commit(); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	mutatedPaths, combinedPatch := applier.diff()
	summary.Files = len(mutatedPaths)

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    summary.message(),
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
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != applyPatchBeginMarker {
		return nil, fmt.Errorf("补丁必须以 %q 开始", applyPatchBeginMarker)
	}

	operations := make([]patchOperation, 0, 4)
	for index := 1; index < len(lines); {
		line := strings.TrimRight(lines[index], "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			index++
		case trimmed == applyPatchEndMarker:
			for _, tail := range lines[index+1:] {
				if strings.TrimSpace(tail) != "" {
					return nil, fmt.Errorf("第 %d 行后存在无效补丁内容", index+2)
				}
			}
			return operations, nil
		case strings.HasPrefix(line, applyPatchAddPrefix):
			operation, next, err := parseAddFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(line, applyPatchDeletePrefix):
			operation, next, err := parseDeleteFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(line, applyPatchUpdatePrefix):
			operation, next, err := parseUpdateFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		default:
			return nil, fmt.Errorf("第 %d 行不是合法的补丁操作头: %s", index+1, line)
		}
	}

	return nil, fmt.Errorf("补丁缺少 %q 结束标记", applyPatchEndMarker)
}

func parseAddFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], applyPatchAddPrefix))
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
		case line == applyPatchEOFMarker:
			operation.NoFinalNewline = true
			index++
		case strings.HasPrefix(line, "+"):
			operation.AddLines = append(operation.AddLines, line[1:])
			index++
		default:
			return patchOperation{}, 0, fmt.Errorf("第 %d 行不是合法的新增文件内容: %s", index+1, line)
		}
	}

	if len(operation.AddLines) == 0 {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行的新增文件没有内容", start+1)
	}
	return operation, index, nil
}

func parseDeleteFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], applyPatchDeletePrefix))
	if path == "" {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少删除文件路径", start+1)
	}
	return patchOperation{
		Kind: patchOperationDelete,
		Path: path,
	}, start + 1, nil
}

func parseUpdateFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], applyPatchUpdatePrefix))
	if path == "" {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少更新文件路径", start+1)
	}

	operation := patchOperation{
		Kind: patchOperationUpdate,
		Path: path,
	}
	index := start + 1
	if index < len(lines) && strings.HasPrefix(lines[index], applyPatchMoveToPrefix) {
		operation.MoveTo = strings.TrimSpace(strings.TrimPrefix(lines[index], applyPatchMoveToPrefix))
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
	hunk := patchHunk{Header: strings.TrimRight(lines[start], "\r")}
	index := start + 1
	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case line == applyPatchEOFMarker:
			hunk.EndOfFile = true
			index++
			return hunk, index, nil
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == applyPatchEndMarker || strings.HasPrefix(line, "@@"):
			if len(hunk.Lines) == 0 {
				return patchHunk{}, 0, fmt.Errorf("第 %d 行的 hunk 没有内容", start+1)
			}
			return hunk, index, nil
		case len(line) == 0:
			return patchHunk{}, 0, fmt.Errorf("第 %d 行缺少 hunk 行前缀", index+1)
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
		return patchHunk{}, 0, fmt.Errorf("第 %d 行的 hunk 没有内容", start+1)
	}
	return hunk, index, nil
}

func isPatchSectionHeader(line string) bool {
	return strings.HasPrefix(line, applyPatchUpdatePrefix) ||
		strings.HasPrefix(line, applyPatchAddPrefix) ||
		strings.HasPrefix(line, applyPatchDeletePrefix)
}

type patchApplier struct {
	tool  *ApplyPatchTool
	files map[string]*stagedFile
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
		return fmt.Errorf("文件不存在，无法删除: %s", absPath)
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
		return false, fmt.Errorf("文件不存在，无法更新: %s", sourcePath)
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
	resolved := a.tool.resolvePath(targetPath)
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("解析补丁路径失败 %q: %w", targetPath, err)
	}
	if err := a.tool.checkPath(op, absPath); err != nil {
		return "", err
	}
	return absPath, nil
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
			return nil, fmt.Errorf("路径是目录，不支持补丁操作: %s", path)
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
		if err := os.WriteFile(path, []byte(file.Content), mode); err != nil {
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
	newlineStyle := detectLineEnding(content)
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines, trailingNewline := splitPatchLines(normalized)
	cursor := 0

	for _, hunk := range hunks {
		oldLines := make([]string, 0, len(hunk.Lines))
		newLines := make([]string, 0, len(hunk.Lines))
		for _, line := range hunk.Lines {
			if line.Kind == ' ' || line.Kind == '-' {
				oldLines = append(oldLines, line.Text)
			}
			if line.Kind == ' ' || line.Kind == '+' {
				newLines = append(newLines, line.Text)
			}
		}

		start := locateHunk(lines, oldLines, cursor)
		if start < 0 {
			return "", fmt.Errorf("无法定位 hunk: %s", hunk.Header)
		}
		touchesEOF := start+len(oldLines) == len(lines)

		updated := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
		updated = append(updated, lines[:start]...)
		updated = append(updated, newLines...)
		updated = append(updated, lines[start+len(oldLines):]...)
		lines = updated
		cursor = start + len(newLines)

		if hunk.EndOfFile {
			trailingNewline = false
		} else if touchesEOF {
			trailingNewline = true
		}
	}

	result := joinPatchLines(lines, trailingNewline, "\n")
	if newlineStyle == "\r\n" {
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}
	return result, nil
}

func locateHunk(lines []string, expected []string, cursor int) int {
	if len(expected) == 0 {
		if cursor < 0 {
			return 0
		}
		if cursor > len(lines) {
			return len(lines)
		}
		return cursor
	}
	if cursor < 0 {
		cursor = 0
	}

	for _, start := range []int{cursor, 0} {
		for index := start; index+len(expected) <= len(lines); index++ {
			if patchSliceEqual(lines[index:index+len(expected)], expected) {
				return index
			}
		}
		if start == 0 {
			break
		}
	}
	return -1
}

func patchSliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func detectLineEnding(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func splitPatchLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	hasTrailingNewline := strings.HasSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	if hasTrailingNewline {
		lines = lines[:len(lines)-1]
	}
	return lines, hasTrailingNewline
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
