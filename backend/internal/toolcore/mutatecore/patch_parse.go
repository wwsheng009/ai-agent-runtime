package mutatecore

// ParsePatch is the tolerant Codex-style patch reader: it accepts the envelope
// variants models actually emit (bare add lines, missing first @@, End of File
// markers, star-padded boundaries, collapsed single-line forms) and reports
// precise line numbers when it cannot. It is the one parser behind every
// surface, so a patch that applies locally applies remotely.

import (
	"fmt"
	"strings"
)

func ParsePatch(input string) ([]PatchOperation, error) {
	normalizedInput := NormalizePatchEnvelope(strings.TrimSpace(strings.ReplaceAll(input, "\r\n", "\n")))
	lines := strings.Split(normalizedInput, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != PatchBegin {
		return nil, fmt.Errorf("补丁必须以 %q 开始", PatchBegin)
	}

	operations := make([]PatchOperation, 0, 4)
	for index := 1; index < len(lines); {
		line := strings.TrimRight(lines[index], "\r")
		headerLine := strings.TrimSpace(line)
		switch {
		case headerLine == "":
			index++
		case headerLine == PatchEnd:
			// Models frequently append commentary or truncated prose after the
			// end marker. Operations already collected are complete, so ignore
			// trailing noise instead of failing the whole patch.
			return operations, nil
		case strings.HasPrefix(headerLine, PatchAdd):
			operation, next, err := parseAddFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(headerLine, PatchDelete):
			operation, next, err := parseDeleteFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(headerLine, PatchUpdate):
			operation, next, err := parseUpdateFileOperation(lines, index)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
			index = next
		default:
			return nil, fmt.Errorf("第 %d 行不是合法的补丁操作头: %s。操作头必须是 %q / %q / %q / %q 之一", index+1, TruncateDiagnosticText(line, 120), PatchAdd, PatchUpdate, PatchDelete, PatchEnd)
		}
	}

	if len(operations) > 0 {
		// Tolerate truncated model output that finished all file operations
		// but dropped the end marker (common on large patches).
		return operations, nil
	}
	return nil, fmt.Errorf("补丁缺少 %q 结束标记，且没有可执行的文件操作", PatchEnd)
}

func parseAddFileOperation(lines []string, start int) (PatchOperation, int, error) {
	path := SanitizePatchPath(strings.TrimPrefix(strings.TrimSpace(lines[start]), PatchAdd))
	if path == "" {
		return PatchOperation{}, 0, fmt.Errorf("第 %d 行缺少新增文件路径", start+1)
	}

	operation := PatchOperation{Kind: PatchAddOp, Path: path}
	index := start + 1
	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == PatchEnd:
			if len(operation.AddLines) == 0 {
				return PatchOperation{}, 0, fmt.Errorf("第 %d 行的新增文件没有内容", start+1)
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
			return PatchOperation{}, 0, fmt.Errorf("第 %d 行不是合法的新增文件内容: %s。Add File 内容行应以 '+' 开头，例如 '+package foo'", index+1, TruncateDiagnosticText(line, 120))
		}
	}

	if len(operation.AddLines) == 0 {
		return PatchOperation{}, 0, fmt.Errorf("第 %d 行的新增文件没有内容", start+1)
	}
	return operation, index, nil
}

func isApplyPatchBareAddContentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	if isPatchBoundaryLine(trimmed, PatchBegin) || isPatchBoundaryLine(trimmed, PatchEnd) {
		return false
	}
	if isPatchSectionHeader(line) || isApplyPatchEOFMarkerLine(line) || strings.HasPrefix(trimmed, PatchMoveTo) {
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
	return isPatchBoundaryLine(line, PatchEOF)
}

func parseDeleteFileOperation(lines []string, start int) (PatchOperation, int, error) {
	path := SanitizePatchPath(strings.TrimPrefix(strings.TrimSpace(lines[start]), PatchDelete))
	if path == "" {
		return PatchOperation{}, 0, fmt.Errorf("第 %d 行缺少删除文件路径", start+1)
	}
	return PatchOperation{
		Kind: PatchDeleteOp,
		Path: path,
	}, start + 1, nil
}

func parseUpdateFileOperation(lines []string, start int) (PatchOperation, int, error) {
	path := SanitizePatchPath(strings.TrimPrefix(strings.TrimSpace(lines[start]), PatchUpdate))
	if path == "" {
		return PatchOperation{}, 0, fmt.Errorf("第 %d 行缺少更新文件路径", start+1)
	}

	operation := PatchOperation{
		Kind: PatchUpdateOp,
		Path: path,
	}
	index := start + 1
	if index < len(lines) && strings.HasPrefix(lines[index], PatchMoveTo) {
		operation.MoveTo = SanitizePatchPath(strings.TrimPrefix(lines[index], PatchMoveTo))
		if operation.MoveTo == "" {
			return PatchOperation{}, 0, fmt.Errorf("第 %d 行缺少移动目标路径", index+1)
		}
		index++
	}

	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == PatchEnd:
			if operation.MoveTo == "" && len(operation.Hunks) == 0 {
				return PatchOperation{}, 0, fmt.Errorf("第 %d 行的更新文件没有内容变更", start+1)
			}
			return operation, index, nil
		case strings.TrimSpace(line) == "":
			index++
		case strings.HasPrefix(line, "@@"):
			hunk, next, err := parsePatchHunk(lines, index)
			if err != nil {
				return PatchOperation{}, 0, err
			}
			operation.Hunks = append(operation.Hunks, hunk)
			index = next
		case len(operation.Hunks) == 0 && isPatchChangeLine(line):
			hunk, next, err := parsePatchHunkWithoutHeader(lines, index)
			if err != nil {
				return PatchOperation{}, 0, err
			}
			operation.Hunks = append(operation.Hunks, hunk)
			index = next
		default:
			return PatchOperation{}, 0, fmt.Errorf("第 %d 行不是合法的 hunk 头: %s", index+1, line)
		}
	}

	if operation.MoveTo == "" && len(operation.Hunks) == 0 {
		return PatchOperation{}, 0, fmt.Errorf("第 %d 行的更新文件没有内容变更", start+1)
	}
	return operation, index, nil
}

func parsePatchHunk(lines []string, start int) (PatchHunk, int, error) {
	return parsePatchHunkBody(lines, start+1, strings.TrimRight(lines[start], "\r"), start)
}

func parsePatchHunkWithoutHeader(lines []string, start int) (PatchHunk, int, error) {
	return parsePatchHunkBody(lines, start, "@@", start)
}

func parsePatchHunkBody(lines []string, index int, header string, headerLine int) (PatchHunk, int, error) {
	hunk := PatchHunk{Header: header}
	for index < len(lines) {
		line := strings.TrimRight(lines[index], "\r")
		switch {
		case isApplyPatchEOFMarkerLine(line):
			hunk.EndOfFile = true
			index++
			return hunk, index, nil
		case isPatchSectionHeader(line) || strings.TrimSpace(line) == PatchEnd || strings.HasPrefix(line, "@@"):
			if len(hunk.Lines) == 0 {
				return PatchHunk{}, 0, fmt.Errorf("第 %d 行的 hunk 没有内容", headerLine+1)
			}
			return hunk, index, nil
		case len(line) == 0:
			hunk.Lines = append(hunk.Lines, PatchHunkLine{
				Kind: ' ',
				Text: "",
			})
			index++
		default:
			prefix := line[0]
			if prefix != ' ' && prefix != '+' && prefix != '-' {
				return PatchHunk{}, 0, fmt.Errorf("第 %d 行不是合法的 hunk 内容: %s", index+1, line)
			}
			hunk.Lines = append(hunk.Lines, PatchHunkLine{
				Kind: prefix,
				Text: line[1:],
			})
			index++
		}
	}

	if len(hunk.Lines) == 0 {
		return PatchHunk{}, 0, fmt.Errorf("第 %d 行的 hunk 没有内容", headerLine+1)
	}
	return hunk, index, nil
}

func isPatchSectionHeader(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, PatchUpdate) ||
		strings.HasPrefix(line, PatchAdd) ||
		strings.HasPrefix(line, PatchDelete)
}
