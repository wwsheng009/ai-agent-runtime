package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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
		runtimetypes.ToolMetadataKindKey:            runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:        false,
		runtimetypes.ToolMetadataMutatesFSKey:       true,
		runtimetypes.ToolMetadataRequiresNetKey:     false,
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
		return toolResultFailure(
			fmt.Errorf("patch 参数缺失或为空"),
			"Provide a non-empty Codex apply_patch text in the patch field (Begin Patch ... End Patch). Do not send empty or whitespace-only patch content.",
		), nil
	}

	operations, err := parseApplyPatch(rawPatch)
	if err != nil {
		return applyPatchToolFailure(err, ""), nil
	}
	if len(operations) == 0 {
		return toolResultFailure(
			fmt.Errorf("补丁中没有可执行的文件操作"),
			"Include at least one Add/Update/Delete/Move File operation between *** Begin Patch and *** End Patch.",
		), nil
	}

	applier := &patchApplier{
		tool:  t,
		files: make(map[string]*stagedFile, len(operations)*2),
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
	normalizedInput := normalizeApplyPatchEnvelope(strings.TrimSpace(strings.ReplaceAll(input, "\r\n", "\n")))
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

func normalizeApplyPatchEnvelope(input string) string {
	input = unwrapApplyPatchHeredoc(input)
	lines := strings.Split(input, "\n")
	if len(lines) >= 3 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[1 : len(lines)-1]
	}
	// Models sometimes emit the whole patch on one line, e.g.
	// "*** Begin Patch *** *** Add File: foo.go package foo ..."
	// Split known markers onto their own lines before per-line cleanup.
	lines = expandCollapsedApplyPatchLines(lines)
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case isApplyPatchBoundaryLine(trimmed, applyPatchBeginMarker):
			normalized = append(normalized, applyPatchBeginMarker)
		case isApplyPatchBoundaryLine(trimmed, applyPatchEndMarker):
			normalized = append(normalized, applyPatchEndMarker)
		default:
			normalized = append(normalized, line)
		}
	}
	return strings.TrimSpace(strings.Join(normalized, "\n"))
}

// expandCollapsedApplyPatchLines splits lines that concatenate multiple Codex
// patch markers (Begin/End/Add/Update/Delete/Move/@@) without newlines.
func expandCollapsedApplyPatchLines(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	markers := []string{
		applyPatchBeginMarker,
		applyPatchEndMarker,
		applyPatchAddPrefix,
		applyPatchUpdatePrefix,
		applyPatchDeletePrefix,
		applyPatchMoveToPrefix,
		applyPatchEOFMarker,
	}
	out := make([]string, 0, len(lines)+4)
	for _, line := range lines {
		expanded := splitCollapsedApplyPatchLine(line, markers)
		out = append(out, expanded...)
	}
	return out
}

func splitCollapsedApplyPatchLine(line string, markers []string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return []string{line}
	}
	// Fast path: already a single marker / normal content line.
	if !strings.Contains(trimmed, "***") && !strings.HasPrefix(trimmed, "@@") {
		return []string{line}
	}

	// Walk the line and cut before each marker occurrence after offset 0.
	type cut struct {
		at     int
		marker string
	}
	cuts := make([]cut, 0, 4)
	for _, marker := range markers {
		searchFrom := 0
		for {
			idx := strings.Index(trimmed[searchFrom:], marker)
			if idx < 0 {
				break
			}
			abs := searchFrom + idx
			// Skip marker that starts the line (handled as the first segment).
			if abs > 0 {
				cuts = append(cuts, cut{at: abs, marker: marker})
			}
			searchFrom = abs + len(marker)
		}
	}
	// Also split @@ hunk headers that models paste mid-line after file ops,
	// e.g. "*** Update File: foo.go @@ func Foo()". If the line already
	// starts with "@@", it is a complete hunk header (including unified-diff
	// form "@@ -N,M +N,M @@") and must not be re-split on a trailing "@@".
	if !strings.HasPrefix(trimmed, "@@") {
		searchFrom := 0
		for {
			idx := strings.Index(trimmed[searchFrom:], "@@")
			if idx < 0 {
				break
			}
			abs := searchFrom + idx
			if abs > 0 {
				cuts = append(cuts, cut{at: abs, marker: "@@"})
			}
			searchFrom = abs + 2
		}
	}
	if len(cuts) == 0 {
		return []string{line}
	}
	// Sort cuts by position (stable insertion for tiny N).
	for i := 1; i < len(cuts); i++ {
		j := i
		for j > 0 && cuts[j].at < cuts[j-1].at {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
			j--
		}
	}

	parts := make([]string, 0, len(cuts)+1)
	prev := 0
	for _, c := range cuts {
		if c.at <= prev {
			continue
		}
		// Avoid cutting inside an already-started marker token stream when
		// adjacent markers share stars (e.g. "*** Begin Patch *** *** Add").
		segment := strings.TrimSpace(trimmed[prev:c.at])
		if segment != "" && !isOnlyStarsAndSpaces(segment) {
			parts = append(parts, segment)
		} else if segment != "" && len(parts) > 0 {
			// Trailing stars after Begin/End markers: drop as noise.
		} else if segment != "" {
			parts = append(parts, segment)
		}
		prev = c.at
	}
	tail := strings.TrimSpace(trimmed[prev:])
	if tail != "" {
		parts = append(parts, tail)
	}
	if len(parts) == 0 {
		return []string{line}
	}
	return parts
}

func isOnlyStarsAndSpaces(s string) bool {
	for _, r := range s {
		if r != '*' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func unwrapApplyPatchHeredoc(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < 4 {
		return input
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	switch first {
	case "<<EOF", "<<'EOF'", `<<"EOF"`:
		if strings.HasSuffix(last, "EOF") {
			return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	return input
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
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[start]), applyPatchAddPrefix))
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
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[start]), applyPatchDeletePrefix))
	if path == "" {
		return patchOperation{}, 0, fmt.Errorf("第 %d 行缺少删除文件路径", start+1)
	}
	return patchOperation{
		Kind: patchOperationDelete,
		Path: path,
	}, start + 1, nil
}

func parseUpdateFileOperation(lines []string, start int) (patchOperation, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[start]), applyPatchUpdatePrefix))
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
		return a.tool.buildPathNotFoundError("文件不存在，无法删除", operation.Path)
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
		return false, a.tool.buildPathNotFoundError("文件不存在，无法更新", operation.Path)
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
			return nil, a.tool.buildPathKindMismatchError("路径是目录，不支持补丁操作", path)
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

		searchCursor := cursor
		if contextLine := hunkChangeContextLine(hunk.Header); contextLine != "" {
			contextStart := locateHunk(lines, []string{contextLine}, searchCursor, false)
			if contextStart >= 0 {
				// Prefer searching near the @@ context marker when present.
				searchCursor = contextStart + 1
			}
			// If @@ context is stale/wrong (common when models invent function
			// names or copy outdated headers), fall through and locate by old
			// content across the whole file instead of hard-failing.
			if contextStart < 0 && len(oldLines) == 0 {
				return "", buildPatchHunkNotFoundError(hunk, []string{contextLine}, lines, "未找到 @@ 上下文行，且 hunk 无旧内容可回退匹配")
			}
		}

		matchOldLines := oldLines
		matchNewLines := newLines
		start := len(lines)
		if len(matchOldLines) > 0 {
			start = locateHunk(lines, matchOldLines, searchCursor, hunk.EndOfFile)
			if start < 0 && matchOldLines[len(matchOldLines)-1] == "" {
				trimmedOldLines := matchOldLines[:len(matchOldLines)-1]
				trimmedNewLines := matchNewLines
				if len(trimmedNewLines) > 0 && trimmedNewLines[len(trimmedNewLines)-1] == "" {
					trimmedNewLines = trimmedNewLines[:len(trimmedNewLines)-1]
				}
				if retryStart := locateHunk(lines, trimmedOldLines, searchCursor, hunk.EndOfFile); retryStart >= 0 {
					matchOldLines = trimmedOldLines
					matchNewLines = trimmedNewLines
					start = retryStart
				}
			}
		}
		if start < 0 {
			return "", buildPatchHunkNotFoundError(hunk, oldLines, lines, "未找到期望旧内容")
		}
		touchesEOF := start+len(matchOldLines) == len(lines)

		updated := make([]string, 0, len(lines)-len(matchOldLines)+len(matchNewLines))
		updated = append(updated, lines[:start]...)
		updated = append(updated, matchNewLines...)
		updated = append(updated, lines[start+len(matchOldLines):]...)
		lines = updated
		cursor = start + len(matchNewLines)

		if touchesEOF {
			trailingNewline = true
		}
	}

	result := joinPatchLines(lines, trailingNewline, "\n")
	if newlineStyle == "\r\n" {
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}
	return result, nil
}

func locateHunk(lines []string, expected []string, cursor int, eof bool) int {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(lines) {
		cursor = len(lines)
	}
	if len(expected) == 0 {
		if eof {
			return len(lines)
		}
		return cursor
	}
	if len(expected) > len(lines) {
		return -1
	}

	maxStart := len(lines) - len(expected)
	if eof {
		tailStart := maxStart
		for _, matcher := range patchLineMatchers() {
			if patchSliceMatches(lines[tailStart:tailStart+len(expected)], expected, matcher) {
				return tailStart
			}
		}
		return -1
	}

	for _, matcher := range patchLineMatchers() {
		for _, searchRange := range hunkSearchRanges(cursor, maxStart) {
			for index := searchRange.start; index <= searchRange.end; index++ {
				if patchSliceMatches(lines[index:index+len(expected)], expected, matcher) {
					return index
				}
			}
		}
	}
	return -1
}

type hunkSearchRange struct {
	start int
	end   int
}

func hunkSearchRanges(cursor int, maxStart int) []hunkSearchRange {
	if maxStart < 0 {
		return nil
	}
	if cursor > maxStart {
		cursor = maxStart + 1
	}
	ranges := make([]hunkSearchRange, 0, 2)
	if cursor <= maxStart {
		ranges = append(ranges, hunkSearchRange{start: cursor, end: maxStart})
	}
	if cursor > 0 {
		ranges = append(ranges, hunkSearchRange{start: 0, end: cursor - 1})
	}
	return ranges
}

type patchLineMatcher func(actual string, expected string) bool

func patchLineMatchers() []patchLineMatcher {
	return []patchLineMatcher{
		func(actual string, expected string) bool {
			return actual == expected
		},
		func(actual string, expected string) bool {
			return strings.TrimRightFunc(actual, unicode.IsSpace) == strings.TrimRightFunc(expected, unicode.IsSpace)
		},
		func(actual string, expected string) bool {
			return strings.TrimSpace(actual) == strings.TrimSpace(expected)
		},
		func(actual string, expected string) bool {
			return normalizePatchComparableLine(actual) == normalizePatchComparableLine(expected)
		},
	}
}

func patchSliceMatches(left, right []string, matcher patchLineMatcher) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !matcher(left[index], right[index]) {
			return false
		}
	}
	return true
}

func normalizePatchComparableLine(line string) string {
	line = strings.TrimSpace(line)
	var builder strings.Builder
	builder.Grow(len(line))
	for _, char := range line {
		switch char {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			builder.WriteRune('-')
		case '\u2018', '\u2019', '\u201a', '\u201b':
			builder.WriteRune('\'')
		case '\u201c', '\u201d', '\u201e', '\u201f':
			builder.WriteRune('"')
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006',
			'\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
			builder.WriteRune(' ')
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
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
	if startLine, closest := closestPatchCurrentContext(actual, expected); len(closest) > 0 {
		current = fmt.Sprintf(
			"\n最接近的当前内容（第 %d 行附近，可直接据此修正补丁）:\n%s",
			startLine,
			formatPatchCurrentLines(closest, startLine),
		)
	}
	return fmt.Errorf(
		"无法定位 hunk: %s；%s。next_action: 先用 view/grep 重读目标文件附近最新内容，按返回的“最接近的当前内容”重建补丁（一次只改一个文件/区域）；不要原样重试同一 stale @@/旧行。也可改用更短、更靠近目标的 @@ 上下文。\n期望内容:\n%s%s",
		header,
		reason,
		formatPatchExpectedLines(expected),
		current,
	)
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
		if startLine := extractPatchSuggestedViewLine(err.Error()); startLine > 0 {
			extra["suggested_view_offset"] = startLine - 1 // view offset is 0-based
			extra["suggested_view_limit"] = 40
		}
		return toolResultFailureWithCode(err, string(runtimeerrors.ErrToolStaleContext), next, extra)
	}
	return toolResultFailureWithCode(err, "", next, extra)
}

func isApplyPatchStaleContextError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "无法定位 hunk") ||
		strings.Contains(msg, "未找到期望旧内容") ||
		strings.Contains(msg, "未找到 @@ 上下文") ||
		(strings.Contains(lower, "hunk") && strings.Contains(lower, "stale")) ||
		strings.Contains(msg, "stale @@")
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
		return "STALE_CONTEXT: re-view/grep the target file nearby (use suggested_view_offset when present), rebuild the patch from the closest current content, and keep the patch focused on one file/region. Do not retry the same stale @@/old lines unchanged."
	case strings.Contains(msg, "Add File") || strings.Contains(msg, "应以 '+' 开头"):
		return "For Add File hunks, every content line must start with '+'. Rewrite the add body with '+' prefixes, or use write/append_write for new files."
	case strings.Contains(msg, "不是合法的补丁") || strings.Contains(msg, "Begin Patch") || strings.Contains(msg, "End Patch"):
		return "Use Codex apply_patch markers: *** Begin Patch / *** Update|Add|Delete File / *** End Patch. Keep freeform patch text outside JSON when possible."
	case strings.Contains(msg, "文件不存在") || strings.Contains(lower, "no such file") || strings.Contains(msg, "路径"):
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
	if len(actual) == 0 {
		return 0, nil
	}
	bestIndex := -1
	bestScore := 0
	for actualIndex, actualLine := range actual {
		for _, expectedLine := range expected {
			if score := patchLineSimilarity(actualLine, expectedLine); score > bestScore {
				bestScore = score
				bestIndex = actualIndex
			}
		}
	}
	if bestIndex < 0 {
		return 0, nil
	}
	start := bestIndex - 3
	if start < 0 {
		start = 0
	}
	window := len(expected) + 6
	if window < 8 {
		window = 8
	}
	if window > 16 {
		window = 16
	}
	end := start + window
	if end > len(actual) {
		end = len(actual)
		start = end - window
		if start < 0 {
			start = 0
		}
	}
	return start + 1, append([]string(nil), actual[start:end]...)
}

func patchLineSimilarity(actual, expected string) int {
	actual = strings.ToLower(normalizePatchComparableLine(actual))
	expected = strings.ToLower(normalizePatchComparableLine(expected))
	if actual == "" || expected == "" {
		return 0
	}
	if actual == expected {
		return 10000 + len([]rune(actual))
	}
	if strings.Contains(actual, expected) || strings.Contains(expected, actual) {
		return 5000 + min(len([]rune(actual)), len([]rune(expected)))
	}
	actualTerms := patchComparableTerms(actual)
	expectedTerms := patchComparableTerms(expected)
	if len(actualTerms) == 0 || len(expectedTerms) == 0 {
		return 0
	}
	score := 0
	for term := range expectedTerms {
		if actualTerms[term] {
			score += 100 + len([]rune(term))
		}
	}
	return score
}

func patchComparableTerms(line string) map[string]bool {
	terms := make(map[string]bool)
	for _, term := range strings.FieldsFunc(line, func(char rune) bool {
		return char != '_' && !unicode.IsLetter(char) && !unicode.IsNumber(char)
	}) {
		if len([]rune(term)) >= 2 {
			terms[term] = true
		}
	}
	return terms
}

func formatPatchCurrentLines(lines []string, startLine int) string {
	formatted := make([]string, 0, len(lines))
	for index, line := range lines {
		formatted = append(formatted, fmt.Sprintf("%d: %s", startLine+index, truncateDiagnosticText(line, 240)))
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
