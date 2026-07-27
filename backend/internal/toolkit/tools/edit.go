package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// EditTool 文件编辑工具（单处替换）
type EditTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	backupDir string
}

// NewEditTool 创建 Edit 工具
func NewEditTool() *EditTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要修改的文件绝对路径。若需要改写多个文件，请拆分为多次 edit 调用，每次只聚焦一个文件和一个替换目标。",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "要替换的文本（必须精确匹配，包括空格和换行）。edit 不做模糊匹配；仅适合刚通过 view/grep 确认存在的小片段。代码编辑、多行替换或上下文可能漂移时优先使用 apply_patch。若需要替换很长的片段或多个位置，请拆分为更小的定位块，避免单次参数过长导致工具调用被截断。",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "替换后的文本。若新内容较长，请拆分为多个更小的 edit/write 调用，每次只聚焦一个替换目标，按块逐步替换或重建。",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "是否替换所有匹配项（默认为 false，只替换第一处）",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}

	return &EditTool{
		BaseTool: toolkit.NewBaseTool(
			"edit",
			"编辑单个文件：使用 new_string 替换文件中的 old_string；只适合刚确认存在的小范围精确替换，不做模糊匹配。代码编辑、多行替换或上下文可能变化时优先使用 apply_patch。若要改写多个文件或大段内容，请拆分为多个更小的 edit/write 调用，每次只聚焦一个文件和一个替换目标，按章节或按块逐步处理，避免单次参数过大导致截断。",
			"1.0.0",
			parameters,
			true,
		),
		backupDir: ".backups",
	}
}

func (e *EditTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        true,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
	}
}

type EditParams struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Execute 实现 Tool 接口
func (e *EditTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p EditParams

	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}

	// 解析参数
	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path 参数缺失或无效"),
		}, nil
	}
	p.FilePath = filePath

	oldString, ok := params["old_string"].(string)
	if !ok || oldString == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("old_string 参数缺失或无效"),
		}, nil
	}
	p.OldString = oldString

	newString, ok := params["new_string"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("new_string 参数缺失或无效"),
		}, nil
	}
	p.NewString = newString
	if err := validateInlineFileMutationPayload(
		"edit",
		inlineMutationSegment{Name: "old_string", Value: p.OldString},
		inlineMutationSegment{Name: "new_string", Value: p.NewString},
	); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	if replaceAll, ok := params["replace_all"].(bool); ok {
		p.ReplaceAll = replaceAll
	}
	resolvedPath := e.resolvePath(p.FilePath)

	if err := e.checkPath(runtimeexecutor.OpWrite, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	// 读取文件内容
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("解析文件路径失败: %w", err),
		}, nil
	}

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      e.buildPathNotFoundError("读取文件失败", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取文件失败: %w", err),
		}, nil
	}
	if fileInfo.IsDir() {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      e.buildPathKindMismatchError("路径是目录，不是文件", p.FilePath),
		}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      e.buildPathNotFoundError("读取文件失败", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取文件失败: %w", err),
		}, nil
	}

	contentStr := string(content)

	// 检查 old_string 是否存在
	// Auto-heal common CRLF/LF mismatches so models do not need a retry
	// round-trip for pure line-ending differences.
	matchedOld, matchedNew, ok := matchEditStrings(contentStr, p.OldString, p.NewString)
	if !ok {
		extra := map[string]interface{}{
			"file_path":     absPath,
			"failure_class": "stale_context",
		}
		if closest, startLine := findClosestEditSnippetWithLine(
			normalizeEditLineEndings(contentStr),
			normalizeEditLineEndings(p.OldString),
		); closest != "" && startLine > 0 {
			extra["suggested_view_offset"] = startLine - 1 // view offset is 0-based
			extra["suggested_view_limit"] = 40
			// Structured copy-paste recovery: models often blind-retry edit after
			// STALE; expose exact current lines so old_string can be rebuilt without
			// an extra view round-trip when the window is already known.
			extra["current_snippet"] = closest
			extra["current_snippet_start_line"] = startLine
		}
		return toolResultFailureWithCode(
			buildEditOldStringNotFoundError(contentStr, p.OldString),
			string(runtimeerrors.ErrToolStaleContext),
			staleEditContextNextAction(),
			extra,
		), nil
	}

	// 创建备份
	backupPath, err := e.createBackup(absPath, content)
	if err != nil {
		// 备份失败不阻止编辑，只记录警告
		backupPath = ""
	}

	// 执行替换
	var newContent string
	var count int

	if p.ReplaceAll {
		// 替换所有匹配项
		newContent = strings.ReplaceAll(contentStr, matchedOld, matchedNew)
		count = strings.Count(contentStr, matchedOld)
	} else {
		// 只替换第一处
		newContent = strings.Replace(contentStr, matchedOld, matchedNew, 1)
		count = 1
	}

	// 写入文件
	err = os.WriteFile(absPath, []byte(newContent), 0644)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("写入文件失败: %w", err),
		}, nil
	}

	// 计算差异
	oldLen := len(contentStr)
	newLen := len(newContent)

	additions := 0
	removals := 0
	if newLen > oldLen {
		additions = newLen - oldLen
	} else {
		removals = oldLen - newLen
	}

	patch := buildUnifiedPatch(absPath, contentStr, newContent)
	result := toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    formatEditSuccessContent(fmt.Sprintf("成功替换了 %d 处匹配项", count), patch),
		Metadata: map[string]interface{}{
			"file_path":     absPath,
			"replacements":  count,
			"additions":     additions,
			"removals":      removals,
			"old_size":      oldLen,
			"new_size":      newLen,
			"patch":         patch,
			"mutated_paths": []string{absPath},
		},
	}

	if backupPath != "" {
		result.Metadata["backup_path"] = backupPath
	}

	return &result, nil
}

func formatEditSuccessContent(message string, patch string) string {
	message = strings.TrimSpace(message)
	patch = strings.TrimRight(patch, "\n")
	if strings.TrimSpace(patch) == "" {
		return message + "\n\n文件差异:\n无内容变化"
	}
	return message + "\n\n文件差异:\n```diff\n" + patch + "\n```"
}

// createBackup 创建文件备份
func (e *EditTool) createBackup(filePath string, content []byte) (string, error) {
	// 获取绝对路径
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}

	// 创建备份目录
	backupDir := filepath.Join(filepath.Dir(absPath), e.backupDir)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", err
	}

	// 生成备份文件名（带时间戳）
	timestamp := time.Now().Format("20060102-150405")
	baseName := filepath.Base(absPath)
	backupName := fmt.Sprintf("%s.%s.bak", baseName, timestamp)
	backupPath := filepath.Join(backupDir, backupName)

	// 写入备份
	if err := os.WriteFile(backupPath, content, 0644); err != nil {
		return "", err
	}

	return backupPath, nil
}

func buildEditOldStringNotFoundError(content string, oldString string) error {
	parts := []string{
		"old_string 未在文件中找到；已尝试 CRLF/LF 与行尾空白/缩进对齐，仍无法唯一定位（不会做语义模糊改写）。",
	}

	normalizedContent := normalizeEditLineEndings(content)
	normalizedOld := normalizeEditLineEndings(oldString)
	if normalizedContent != content || normalizedOld != oldString {
		if strings.Contains(normalizedContent, normalizedOld) {
			parts = append(parts, "检测到 old_string 在统一为 LF 换行后可以匹配，失败很可能来自 CRLF/LF 换行差异。")
		}
	}

	if closest, startLine := findClosestEditSnippetWithLine(normalizedContent, normalizedOld); closest != "" {
		if startLine > 0 {
			parts = append(parts, fmt.Sprintf("建议从第 %d 行附近用 view 重读（suggested_view_offset=%d）。", startLine, startLine-1))
		}
		// Multi-line block (like apply_patch) so the model can copy exact current text.
		parts = append(parts, fmt.Sprintf(
			"最接近的当前内容（第 %d 行附近，可直接据此重建 old_string）:\n%s",
			startLine,
			formatEditClosestLines(closest, startLine),
		))
	}

	parts = append(parts,
		"next_action: 优先用上方“最接近的当前内容”作为 edit.old_string 的精确来源（去掉行号前缀后复制）；必要时再 view/grep 确认。代码编辑、多行替换或上下文可能变化时优先使用 apply_patch，并在 @@ 中提供靠近目标的函数/类上下文。不要原样重试同一 stale old_string。",
		fmt.Sprintf("old_string 预览: %q", truncateDiagnosticText(oldString, 200)),
	)
	return fmt.Errorf("%s", strings.Join(parts, "\n"))
}

func formatEditClosestLines(snippet string, startLine int) string {
	lines := strings.Split(normalizeEditLineEndings(snippet), "\n")
	const maxLines = 16
	limit := len(lines)
	if limit > maxLines {
		limit = maxLines
	}
	out := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		lineNo := startLine + i
		if lineNo <= 0 {
			lineNo = i + 1
		}
		// Keep full line text (no mid-line truncation) so models can copy exact
		// old_string bytes including indent; only cap total line count.
		out = append(out, fmt.Sprintf("%6d|%s", lineNo, lines[i]))
	}
	if len(lines) > limit {
		out = append(out, fmt.Sprintf("... 省略 %d 行", len(lines)-limit))
	}
	return strings.Join(out, "\n")
}

func staleEditContextNextAction() string {
	return "STALE_CONTEXT: copy exact lines from current_snippet / “最接近的当前内容” (or re-view with suggested_view_offset), rebuild a short confirmed old_string from that text, then retry; for multi-line or drifting context prefer apply_patch. Do not retry the same stale old_string unchanged."
}

// matchEditStrings tries exact then line-ending-normalized matches so models
// recover from CRLF/LF drift without a retry. Returns the exact substrings to
// use for Replace (preserving the file's newline style when possible).
func matchEditStrings(content, oldString, newString string) (matchedOld, matchedNew string, ok bool) {
	if oldString == "" {
		return "", "", false
	}
	if strings.Contains(content, oldString) {
		return oldString, newString, true
	}

	normalizedOld := normalizeEditLineEndings(oldString)
	normalizedNew := normalizeEditLineEndings(newString)
	if normalizedOld == "" {
		return "", "", false
	}

	// LF old_string against CRLF file content.
	crlfOld := strings.ReplaceAll(normalizedOld, "\n", "\r\n")
	if crlfOld != oldString && strings.Contains(content, crlfOld) {
		return crlfOld, strings.ReplaceAll(normalizedNew, "\n", "\r\n"), true
	}

	// CRLF old_string against LF file content.
	if normalizedOld != oldString && strings.Contains(content, normalizedOld) {
		return normalizedOld, normalizedNew, true
	}

	// Whitespace/indent-tolerant match (same recovery class as apply_patch line
	// matchers). Prefer the file's exact bytes so replace cannot invent whitespace.
	if matchedOld, matchedNew, ok = matchEditStringsWhitespaceTolerant(content, normalizedOld, normalizedNew); ok {
		return matchedOld, matchedNew, true
	}

	return "", "", false
}

// matchEditStringsWhitespaceTolerant finds old_string in content when the only
// differences are trailing spaces, leading indent, internal column padding,
// or blank-line run length between the same significant lines.
// On success it returns the exact file substring and a new_string rewritten to
// use the file's indent style for the matched window.
func matchEditStringsWhitespaceTolerant(content, oldLF, newLF string) (matchedOld, matchedNew string, ok bool) {
	contentLF := normalizeEditLineEndings(content)
	oldLines := strings.Split(oldLF, "\n")
	// Drop a single trailing empty line from old so "foo\n" still matches "foo"
	// without requiring an extra blank line in the file.
	if len(oldLines) > 1 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	if len(oldLines) == 0 {
		return "", "", false
	}

	contentLines := strings.Split(contentLF, "\n")
	if len(oldLines) > len(contentLines) {
		return "", "", false
	}

	matchers := []func(actual, expected string) bool{
		func(actual, expected string) bool {
			return strings.TrimRight(actual, " \t") == strings.TrimRight(expected, " \t")
		},
		func(actual, expected string) bool {
			return strings.TrimSpace(actual) == strings.TrimSpace(expected)
		},
		// Same recovery class as apply_patch: collapse internal whitespace and
		// normalize fancy punctuation so column-aligned Go consts still match.
		func(actual, expected string) bool {
			return normalizePatchComparableLine(actual) == normalizePatchComparableLine(expected)
		},
	}

	start := -1
	for _, matcher := range matchers {
		for i := 0; i+len(oldLines) <= len(contentLines); i++ {
			window := contentLines[i : i+len(oldLines)]
			if editLineWindowMatches(window, oldLines, matcher) {
				// Prefer unique windows to avoid wrong-site edits.
				if start >= 0 {
					return "", "", false
				}
				start = i
			}
		}
		if start >= 0 {
			break
		}
	}
	var matchedLines []string
	if start >= 0 {
		matchedLines = contentLines[start : start+len(oldLines)]
	} else {
		// Fixed-length window failed: allow blank-run length drift between the
		// same non-blank lines (models often invent 3 vs 4 blank lines).
		var okBlank bool
		matchedLines, okBlank = matchEditBlankRunWindow(contentLines, oldLines)
		if !okBlank {
			return "", "", false
		}
	}
	matchedOldLF := strings.Join(matchedLines, "\n")

	// Rebuild replacement from the file window so unchanged lines keep exact
	// file bytes (column padding / blank runs), while changed bodies inherit
	// file indent.
	matchedNewLF := rebuildEditReplacement(matchedLines, oldLines, newLF)

	// Preserve original CRLF when the file uses it.
	if strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(matchedOldLF, "\n", "\r\n"),
			strings.ReplaceAll(matchedNewLF, "\n", "\r\n"),
			true
	}
	return matchedOldLF, matchedNewLF, true
}

func editLineWindowMatches(actual, expected []string, matcher func(actual, expected string) bool) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if !matcher(actual[i], expected[i]) {
			return false
		}
	}
	return true
}

// matchEditBlankRunWindow finds a unique content span whose non-blank lines
// match oldLines' non-blank lines under collapse-ws comparison. Blank runs
// between those lines may differ in length.
func matchEditBlankRunWindow(contentLines, oldLines []string) ([]string, bool) {
	start, end, ok := locateBlankRunLineSpan(contentLines, oldLines)
	if !ok {
		return nil, false
	}
	return contentLines[start : end+1], true
}

// locateBlankRunLineSpan returns the unique [start,end] inclusive span in
// contentLines whose non-blank sequence matches oldLines under collapse-ws.
// Shared by edit and apply_patch so blank-run drift heals consistently.
func locateBlankRunLineSpan(contentLines, oldLines []string) (start, end int, ok bool) {
	oldNonBlank := editNonBlankLines(oldLines)
	if len(oldNonBlank) < 2 {
		// Single significant line is already covered by fixed-window matchers.
		return 0, 0, false
	}

	type span struct{ start, end int } // inclusive end
	var found []span
	for i := 0; i < len(contentLines); i++ {
		if strings.TrimSpace(contentLines[i]) == "" {
			continue
		}
		if normalizePatchComparableLine(contentLines[i]) != normalizePatchComparableLine(oldNonBlank[0]) {
			continue
		}
		ci := i
		matchOK := true
		for k := 1; k < len(oldNonBlank); k++ {
			ci++
			for ci < len(contentLines) && strings.TrimSpace(contentLines[ci]) == "" {
				ci++
			}
			if ci >= len(contentLines) ||
				normalizePatchComparableLine(contentLines[ci]) != normalizePatchComparableLine(oldNonBlank[k]) {
				matchOK = false
				break
			}
		}
		if matchOK {
			found = append(found, span{start: i, end: ci})
			if len(found) > 1 {
				return 0, 0, false
			}
		}
	}
	if len(found) != 1 {
		return 0, 0, false
	}
	s := found[0]
	return s.start, s.end, true
}

func editNonBlankLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// rebuildEditReplacement constructs the replacement text from the file window
// and the model's new_string. Unchanged lines (under collapse-ws) keep the
// file's exact bytes so column padding and blank runs are preserved; changed
// or inserted lines inherit file indent from the corresponding old/file line.
func rebuildEditReplacement(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	// Equal-length windows: map line-by-line when possible.
	if len(matchedLines) == len(oldLines) {
		return alignEditReplacementIndentPreserveExact(matchedLines, oldLines, newLF)
	}
	// Blank-run flexible window: preserve file blank runs and rewrite only
	// non-blank bodies that the model actually changed.
	return alignEditReplacementNonBlank(matchedLines, oldLines, newLF)
}

// alignEditReplacementIndentPreserveExact is like alignEditReplacementIndent
// but keeps the file line verbatim when the model body is collapse-ws-equal
// to the matched old body (so column padding is not flattened).
func alignEditReplacementIndentPreserveExact(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	newLines := strings.Split(newLF, "\n")
	trailingNL := strings.HasSuffix(newLF, "\n")
	if trailingNL && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}

	out := make([]string, len(newLines))
	for i, line := range newLines {
		if strings.TrimSpace(line) == "" {
			// Prefer file blank line when window still aligns by index.
			if i < len(matchedLines) && strings.TrimSpace(matchedLines[i]) == "" {
				out[i] = matchedLines[i]
			} else {
				out[i] = line
			}
			continue
		}
		newWS := leadingWhitespace(line)
		body := strings.TrimPrefix(line, newWS)

		if i < len(oldLines) && i < len(matchedLines) {
			// If model body equals matched file body under collapse-ws, keep
			// the file line bytes (column padding / fancy punctuation).
			if normalizePatchComparableLine(line) == normalizePatchComparableLine(matchedLines[i]) {
				out[i] = matchedLines[i]
				continue
			}
			// Also: if model new equals model old body (no semantic change)
			// keep file line even when new collapsed form differs only in pad.
			if normalizePatchComparableLine(oldLines[i]) == normalizePatchComparableLine(line) {
				out[i] = matchedLines[i]
				continue
			}

			oldWS := leadingWhitespace(oldLines[i])
			fileWS := leadingWhitespace(matchedLines[i])
			rel := newWS
			if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
				rel = strings.TrimPrefix(newWS, oldWS)
			} else if newWS == oldWS {
				rel = ""
			} else if oldWS == "" {
				rel = newWS
			} else {
				out[i] = line
				continue
			}
			out[i] = fileWS + rel + body
			continue
		}

		if len(matchedLines) > 0 && len(oldLines) > 0 {
			fileWS0 := leadingWhitespace(matchedLines[0])
			oldWS0 := leadingWhitespace(oldLines[0])
			if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
				out[i] = fileWS0 + line
				continue
			}
		}
		out[i] = line
	}

	joined := strings.Join(out, "\n")
	if trailingNL {
		joined += "\n"
	}
	return joined
}

// alignEditReplacementNonBlank rewrites newLF by mapping non-blank lines onto
// the file window's non-blank indents. Blank runs keep the *file* shape (not
// the model's invented blank count) when the model only renames a significant
// line; extra model blank lines between non-blank pairs are not inserted.
func alignEditReplacementNonBlank(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	fileNB := editNonBlankLines(matchedLines)
	oldNB := editNonBlankLines(oldLines)
	newLines := strings.Split(newLF, "\n")
	trailingNL := strings.HasSuffix(newLF, "\n")
	if trailingNL && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}
	newNB := editNonBlankLines(newLines)

	// Build output by walking the file window structure: keep blank runs from
	// the file, rewrite non-blank lines from the model's non-blank sequence.
	if len(newNB) == 0 {
		return newLF
	}

	// Prefer file-shaped reconstruction when non-blank counts match old/file.
	if len(newNB) == len(fileNB) && len(oldNB) == len(fileNB) {
		out := make([]string, 0, len(matchedLines)+2)
		nbIdx := 0
		for _, fileLine := range matchedLines {
			if strings.TrimSpace(fileLine) == "" {
				out = append(out, fileLine)
				continue
			}
			if nbIdx >= len(newNB) {
				out = append(out, fileLine)
				continue
			}
			modelLine := newNB[nbIdx]
			// Unchanged under collapse-ws → keep file exact bytes.
			if normalizePatchComparableLine(modelLine) == normalizePatchComparableLine(fileLine) ||
				normalizePatchComparableLine(modelLine) == normalizePatchComparableLine(oldNB[nbIdx]) {
				out = append(out, fileLine)
			} else {
				newWS := leadingWhitespace(modelLine)
				body := strings.TrimPrefix(modelLine, newWS)
				oldWS := leadingWhitespace(oldNB[nbIdx])
				fileWS := leadingWhitespace(fileLine)
				rel := newWS
				if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
					rel = strings.TrimPrefix(newWS, oldWS)
				} else if newWS == oldWS {
					rel = ""
				} else if oldWS == "" {
					rel = newWS
				} else {
					out = append(out, modelLine)
					nbIdx++
					continue
				}
				out = append(out, fileWS+rel+body)
			}
			nbIdx++
		}
		// Append any extra model non-blank lines beyond the file window.
		for ; nbIdx < len(newNB); nbIdx++ {
			line := newNB[nbIdx]
			if len(fileNB) > 0 {
				fileWS0 := leadingWhitespace(fileNB[0])
				oldWS0 := ""
				if len(oldNB) > 0 {
					oldWS0 = leadingWhitespace(oldNB[0])
				}
				newWS := leadingWhitespace(line)
				if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
					out = append(out, fileWS0+line)
					continue
				}
			}
			out = append(out, line)
		}
		joined := strings.Join(out, "\n")
		if trailingNL {
			joined += "\n"
		}
		return joined
	}

	// Fallback: indent-align model lines without reshaping blank runs.
	return alignEditReplacementIndent(matchedLines, oldLines, newLF)
}

// alignEditReplacementIndent rewrites newLF so relative indent changes from the
// model are applied on top of the file's actual leading whitespace for the
// matched window. Absolute-looking first lines (no indent in both old and new)
// inherit the file first-line indent.
func alignEditReplacementIndent(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	newLines := strings.Split(newLF, "\n")
	// Preserve whether replacement ends with a trailing newline.
	trailingNL := strings.HasSuffix(newLF, "\n")
	if trailingNL && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}

	out := make([]string, len(newLines))
	for i, line := range newLines {
		if strings.TrimSpace(line) == "" {
			out[i] = line
			continue
		}
		newWS := leadingWhitespace(line)
		body := strings.TrimPrefix(line, newWS)

		// Prefer per-line remap when old/file windows still align by index.
		if i < len(oldLines) && i < len(matchedLines) {
			oldWS := leadingWhitespace(oldLines[i])
			fileWS := leadingWhitespace(matchedLines[i])
			rel := newWS
			if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
				rel = strings.TrimPrefix(newWS, oldWS)
			} else if newWS == oldWS {
				rel = ""
			} else if oldWS == "" {
				// Model dropped outer indent on this line; keep new's relative
				// indent on top of the file line's indent.
				rel = newWS
			} else {
				// Unrelated indent styles — keep model line unchanged.
				out[i] = line
				continue
			}
			out[i] = fileWS + rel + body
			continue
		}

		// Extra new lines beyond the matched window: inherit first-line file
		// indent delta when old first line lacked indent.
		if len(matchedLines) > 0 && len(oldLines) > 0 {
			fileWS0 := leadingWhitespace(matchedLines[0])
			oldWS0 := leadingWhitespace(oldLines[0])
			if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
				out[i] = fileWS0 + line
				continue
			}
		}
		out[i] = line
	}

	joined := strings.Join(out, "\n")
	if trailingNL {
		joined += "\n"
	}
	return joined
}

func leadingWhitespace(line string) string {
	i := 0
	for i < len(line) {
		if line[i] != ' ' && line[i] != '\t' {
			break
		}
		i++
	}
	return line[:i]
}

func normalizeEditLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func findClosestEditSnippet(content, oldString string) string {
	snippet, _ := findClosestEditSnippetWithLine(content, oldString)
	return snippet
}

// findClosestEditSnippetLine returns a 1-based line number near the closest
// current content for a missed old_string, or 0 when no useful hint exists.
func findClosestEditSnippetLine(content, oldString string) int {
	_, startLine := findClosestEditSnippetWithLine(
		normalizeEditLineEndings(content),
		normalizeEditLineEndings(oldString),
	)
	return startLine
}

func findClosestEditSnippetWithLine(content, oldString string) (string, int) {
	// Do not TrimSpace the whole file: leading blanks must keep stable 1-based
	// line numbers for suggested_view_offset / current_snippet_start_line.
	content = normalizeEditLineEndings(content)
	oldString = strings.TrimSpace(normalizeEditLineEndings(oldString))
	if strings.TrimSpace(content) == "" || oldString == "" {
		return "", 0
	}
	oldLines := strings.Split(oldString, "\n")
	// Drop a single trailing empty line so "foo\n" windows match "foo".
	if len(oldLines) > 1 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	if len(editNonBlankLines(oldLines)) == 0 {
		return "", 0
	}
	contentLines := strings.Split(content, "\n")
	bestIdx := -1
	bestEnd := -1
	bestScore := 0.0

	// Prefer multi-line window alignment. Scoring only the first old line often
	// anchors on generic tokens (import (, }, return) far from the real drift.
	maxStart := len(contentLines)
	if len(oldLines) <= len(contentLines) {
		maxStart = len(contentLines) - len(oldLines) + 1
	}
	for i := 0; i < maxStart; i++ {
		end := i + len(oldLines)
		if end > len(contentLines) {
			end = len(contentLines)
		}
		score := scoreEditClosestWindow(contentLines[i:end], oldLines)
		if score > bestScore {
			bestScore = score
			bestIdx = i
			bestEnd = end
		}
	}

	// Also try anchoring each non-blank old line against content lines so a
	// drifted first line still recovers when a later distinctive line matches.
	// Align from the strong line forward (do not force weak old prefix lines
	// into the window — that mis-anchors recovery on ")" / blanks).
	for oldIdx, oldLine := range oldLines {
		if strings.TrimSpace(oldLine) == "" {
			continue
		}
		tailOld := oldLines[oldIdx:]
		for contentIdx := range contentLines {
			lineScore := editClosestLineScore(contentLines[contentIdx], oldLine)
			if lineScore < 0.55 {
				continue
			}
			end := contentIdx + len(tailOld)
			if end > len(contentLines) {
				end = len(contentLines)
			}
			if end <= contentIdx {
				continue
			}
			score := scoreEditClosestWindow(contentLines[contentIdx:end], tailOld)
			// Boost windows that contain a strong single-line anchor.
			score += 0.08 * lineScore
			if score > bestScore {
				bestScore = score
				bestIdx = contentIdx
				bestEnd = end
			}
		}
	}

	if bestIdx >= 0 && bestEnd > bestIdx && bestScore >= 0.45 {
		// Late-line anchors align a full old_string-sized window that may start on
		// weak prefix lines (")", blank). Tighten to the strong-matching core so
		// current_snippet / suggested_view_offset point at the real drift site.
		window := contentLines[bestIdx:bestEnd]
		from, to := tightenClosestWindowBounds(window, oldLines)
		if to > from {
			return strings.Join(window[from:to], "\n"), bestIdx + from + 1
		}
		return strings.Join(window, "\n"), bestIdx + 1
	}

	// True content_diff residual: multi-line window score can fall below the
	// 0.45 gate when the first old line is fully gone / invented, but distinctive
	// identifiers still live nearby (e.g. selector_id / custom-tool-call-namespace).
	// Fall back to token/single-line anchors so STALE still exports a
	// copy-pasteable current_snippet instead of empty recovery (old "no closest body").
	return findClosestEditSnippetByTokenFallback(contentLines, oldLines)
}

// findClosestEditSnippetByTokenFallback recovers a padded current window when
// line-aligned multi-window scoring fails. It never auto-heals writes — only
// ranks a copy-paste recovery site for STALE_CONTEXT metadata / error body.
func findClosestEditSnippetByTokenFallback(contentLines, oldLines []string) (string, int) {
	if len(contentLines) == 0 || len(oldLines) == 0 {
		return "", 0
	}
	tokens := make([]string, 0, 16)
	seenTok := make(map[string]struct{}, 16)
	for _, line := range oldLines {
		for _, tok := range editDistinctiveTokens(line) {
			key := strings.ToLower(tok)
			if _, ok := seenTok[key]; ok {
				continue
			}
			seenTok[key] = struct{}{}
			tokens = append(tokens, tok)
		}
	}
	if len(tokens) == 0 {
		// Last resort: single best line-similarity hit (parity with apply_patch).
		bestIdx := -1
		bestScore := 0.0
		for i, actual := range contentLines {
			for _, expected := range oldLines {
				if strings.TrimSpace(expected) == "" {
					continue
				}
				if score := editClosestLineScore(actual, expected); score > bestScore {
					bestScore = score
					bestIdx = i
				}
			}
		}
		if bestIdx < 0 || bestScore < 0.35 {
			return "", 0
		}
		start, end := padEditClosestWindow(len(contentLines), bestIdx, bestIdx+1)
		return strings.Join(contentLines[start:end], "\n"), start + 1
	}

	bestIdx := -1
	bestScore := 0
	bestHits := 0
	// Prefer lines that hit multiple distinctive tokens; break ties toward fewer
	// total occurrences of the same token set (less ambiguous anchors).
	lineHits := make([]int, len(contentLines))
	lineScore := make([]int, len(contentLines))
	for i, actual := range contentLines {
		lower := strings.ToLower(actual)
		hits := 0
		score := 0
		for _, tok := range tokens {
			tl := strings.ToLower(tok)
			if strings.Contains(lower, tl) {
				hits++
				// Longer / more specific tokens dominate ranking.
				w := len(tok)
				if w > 24 {
					w = 24
				}
				score += w
			}
		}
		lineHits[i] = hits
		lineScore[i] = score
	}
	// Ambiguity penalty: tokens that appear on many lines are weaker anchors.
	occur := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		tl := strings.ToLower(tok)
		for _, actual := range contentLines {
			if strings.Contains(strings.ToLower(actual), tl) {
				occur[tl]++
			}
		}
	}
	for i := range contentLines {
		if lineHits[i] == 0 {
			continue
		}
		score := lineScore[i]
		for _, tok := range tokens {
			tl := strings.ToLower(tok)
			if !strings.Contains(strings.ToLower(contentLines[i]), tl) {
				continue
			}
			if n := occur[tl]; n > 3 {
				// Soft penalty for very common tokens (still keep unique multi-hit lines).
				score -= (n - 3)
			}
		}
		if score > bestScore || (score == bestScore && lineHits[i] > bestHits) {
			bestScore = score
			bestHits = lineHits[i]
			bestIdx = i
		}
	}
	if bestIdx < 0 || bestHits == 0 || bestScore <= 0 {
		return "", 0
	}
	// Expand to cover neighboring lines that also hit old tokens (block context).
	coreStart, coreEnd := bestIdx, bestIdx+1
	for i := bestIdx - 1; i >= 0 && bestIdx-i <= 4; i-- {
		if lineHits[i] == 0 {
			break
		}
		coreStart = i
	}
	for i := bestIdx + 1; i < len(contentLines) && i-bestIdx <= 6; i++ {
		if lineHits[i] == 0 {
			break
		}
		coreEnd = i + 1
	}
	start, end := padEditClosestWindow(len(contentLines), coreStart, coreEnd)
	return strings.Join(contentLines[start:end], "\n"), start + 1
}

// padEditClosestWindow expands [coreStart, coreEnd) with modest context for
// token-fallback recovery (mirrors apply_patch pad policy: prefer right pad).
func padEditClosestWindow(fileLen, coreStart, coreEnd int) (start, end int) {
	if fileLen <= 0 {
		return 0, 0
	}
	if coreStart < 0 {
		coreStart = 0
	}
	if coreEnd > fileLen {
		coreEnd = fileLen
	}
	if coreEnd <= coreStart {
		coreStart = 0
		coreEnd = fileLen
		if coreEnd > 8 {
			coreEnd = 8
		}
	}
	const (
		preferPad = 2
		minWindow = 8
		maxWindow = 16
	)
	start = coreStart - preferPad
	if start < 0 {
		start = 0
	}
	end = coreEnd + preferPad
	if end > fileLen {
		end = fileLen
	}
	for end-start < minWindow && end < fileLen {
		end++
	}
	for end-start < minWindow && start > 0 {
		start--
	}
	if end-start > maxWindow {
		// Keep core near the start; trim far-side padding.
		if coreEnd-coreStart >= maxWindow {
			start = coreStart
			end = coreStart + maxWindow
			if end > fileLen {
				end = fileLen
				start = end - maxWindow
				if start < 0 {
					start = 0
				}
			}
		} else {
			start = coreStart
			if start > 0 && coreStart-start < preferPad {
				start = coreStart - preferPad
				if start < 0 {
					start = 0
				}
			}
			end = start + maxWindow
			if end > fileLen {
				end = fileLen
				start = end - maxWindow
				if start < 0 {
					start = 0
				}
			}
			if start > coreStart {
				start = coreStart
				end = start + maxWindow
				if end > fileLen {
					end = fileLen
				}
			}
		}
	}
	return start, end
}

// editDistinctiveTokens extracts identifier-like tokens useful for closest
// ranking when line-aligned windows fail. Short/generic tokens are dropped.
func editDistinctiveTokens(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || isGenericStructuralLine(line) {
		return nil
	}
	var (
		out   []string
		cur   strings.Builder
		flush = func() {
			if cur.Len() == 0 {
				return
			}
			tok := cur.String()
			cur.Reset()
			if len(tok) < 6 {
				return
			}
			lower := strings.ToLower(tok)
			switch lower {
			case "return", "import", "package", "switch", "select", "default",
				"const", "type", "func", "class", "export", "string", "number",
				"object", "public", "private", "static", "async", "await",
				"interface", "struct", "error", "context", "testing":
				return
			}
			out = append(out, tok)
		}
	)
	for _, r := range line {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// tightenClosestWindowBounds returns [from,to) indexes inside window covering
// strong old/file line pairs, with one line of padding when available.
func tightenClosestWindowBounds(window, oldLines []string) (from, to int) {
	if len(window) == 0 {
		return 0, 0
	}
	firstStrong := -1
	lastStrong := -1
	limit := len(oldLines)
	if limit > len(window) {
		limit = len(window)
	}
	for i := 0; i < limit; i++ {
		if strings.TrimSpace(oldLines[i]) == "" {
			continue
		}
		if editClosestLineScore(window[i], oldLines[i]) >= 0.55 {
			if firstStrong < 0 {
				firstStrong = i
			}
			lastStrong = i
		}
	}
	if firstStrong < 0 {
		return 0, len(window)
	}
	from = firstStrong
	if from > 0 {
		from--
	}
	to = lastStrong + 1
	if to < len(window) {
		to++
	}
	if to > len(window) {
		to = len(window)
	}
	if to <= from {
		return 0, len(window)
	}
	return from, to
}

// scoreEditClosestWindow ranks how well a file window matches the model's
// missed old_string lines. Blank lines contribute little; collapse-ws equality
// nearly matches exact equality so column-padding drift still anchors recovery.
func scoreEditClosestWindow(window, oldLines []string) float64 {
	if len(oldLines) == 0 {
		return 0
	}
	total := 0.0
	strong := 0
	compared := 0
	for i, oldLine := range oldLines {
		if strings.TrimSpace(oldLine) == "" {
			// Model/file blank-run drift should not dominate the score.
			if i < len(window) && strings.TrimSpace(window[i]) == "" {
				total += 0.35
			}
			continue
		}
		compared++
		if i >= len(window) {
			continue
		}
		score := editClosestLineScore(window[i], oldLine)
		total += score
		if score >= 0.55 {
			strong++
		}
	}
	if compared == 0 {
		return total / float64(len(oldLines))
	}
	avg := total / float64(compared)
	// Multi-line agreement is the main signal for true content_diff recovery.
	if compared >= 2 && strong >= 2 {
		avg += 0.12 * (float64(strong) / float64(compared))
	} else if compared >= 2 && strong == 1 {
		// One strong + weak neighbors still beats a lone generic first-line hit.
		avg += 0.04
	}
	if avg > 1.2 {
		avg = 1.2
	}
	return avg
}

func editClosestLineScore(actual, expected string) float64 {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return 0
	}
	// Tiny structural lines (}, ), {) match everywhere and must not dominate
	// multi-line recovery anchors.
	if isGenericStructuralLine(actual) || isGenericStructuralLine(expected) {
		if actual == expected {
			return 0.25
		}
		return 0
	}
	if score := editLineSimilarity(actual, expected); score >= 0.8 {
		return score
	}
	// Column-alignment / punctuation-normalized equality (shared with matchers).
	if normalizePatchComparableLine(actual) == normalizePatchComparableLine(expected) {
		return 0.92
	}
	return editLineSimilarity(actual, expected)
}

func isGenericStructuralLine(line string) bool {
	line = strings.TrimSpace(line)
	switch line {
	case "}", ")", "(", "{", "};", "},", "];", "],", "*/", "/*":
		return true
	}
	// bare braces / parens only
	trimmed := strings.Trim(line, " \t{};,()")
	return trimmed == "" && line != ""
}

func editLineSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return 0.8
	}
	// Near-identifier typos: "HelloWord" vs "HelloWorld" after normalizing
	// punctuation so func signatures still rank as strong anchors.
	normA := normalizePatchComparableLine(a)
	normB := normalizePatchComparableLine(b)
	if normA != "" && normB != "" {
		if normA == normB {
			return 0.95
		}
		if runeEditSimilarity(normA, normB) >= 0.72 {
			return 0.78
		}
	}
	// lightweight token overlap (also soft-match near-equal tokens)
	aParts := strings.Fields(normA)
	bParts := strings.Fields(normB)
	if len(aParts) == 0 || len(bParts) == 0 {
		aParts = strings.Fields(a)
		bParts = strings.Fields(b)
	}
	if len(aParts) == 0 || len(bParts) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(aParts))
	for _, p := range aParts {
		set[p] = struct{}{}
	}
	overlap := 0.0
	for _, p := range bParts {
		if _, ok := set[p]; ok {
			overlap++
			continue
		}
		// Soft token match for near typos inside signatures.
		best := 0.0
		for _, ap := range aParts {
			if s := runeEditSimilarity(ap, p); s > best {
				best = s
			}
		}
		if best >= 0.72 {
			overlap += best
		}
	}
	union := float64(len(aParts)+len(bParts)) - overlap
	if union <= 0 {
		return 1
	}
	return overlap / union
}

// runeEditSimilarity is a cheap normalized inverse Levenshtein on runes.
// Used only for closest-snippet ranking (never for auto-heal writes).
func runeEditSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return 0
	}
	// Reject wildly different lengths early.
	if len(ar) > len(br)*2 || len(br) > len(ar)*2 {
		return 0
	}
	// DP Levenshtein with two rows.
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = del
			if ins < cur[j] {
				cur[j] = ins
			}
			if sub < cur[j] {
				cur[j] = sub
			}
		}
		prev, cur = cur, prev
	}
	dist := prev[len(br)]
	maxLen := len(ar)
	if len(br) > maxLen {
		maxLen = len(br)
	}
	if maxLen == 0 {
		return 1
	}
	sim := 1 - float64(dist)/float64(maxLen)
	if sim < 0 {
		return 0
	}
	return sim
}
