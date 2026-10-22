package remotetools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// View defaults mirror the local view tool so a remote read and a local read of
// the same file produce the same window and continuation hints.
const (
	remoteViewDefaultLimit = 400
	remoteViewMaxLimit     = 2000
	remoteViewLineCap      = 4096
)

// remoteViewTool reads one file window inside the workspace jail.
//
// It intentionally does not reuse toolkit/tools.ViewTool: that tool carries
// local-only concerns (symbol index, image attachments, read dedup ledger, LSP
// hooks) that would either be wrong on a remote host or add a large dependency
// tree to the small binary. The wire contract - line numbers, is_truncated,
// suggested_next_offset - is kept identical.
type remoteViewTool struct {
	*toolkit.BaseTool
	remoteToolBase
}

func newRemoteViewTool(cfg *Config, workspace *Workspace) *remoteViewTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "File to read, relative to the remote workspace (or an absolute path inside it).",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "1-based line number to start from. Default 1.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"maximum":     remoteViewMaxLimit,
				"description": "Maximum lines to return. Default 400, capped at 2000.",
			},
		},
		"required":             []string{"file_path"},
		"additionalProperties": false,
	}
	tool := &remoteViewTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolView,
		tool.describe("Read a window of a remote text file with stable line numbers and truncation metadata."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPath, _ := params["file_path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass file_path", "file_path is required")), nil
	}
	target, err := t.resolve(ctx, rawPath)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if err := ensureNotDevice(target); err != nil {
		return remoteErrorResult(err), nil
	}

	info, statErr := os.Stat(target)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return remoteErrorResult(newToolError(CodeNotFound, "verify the path with ls or glob", "file not found: %s", t.cleanDisplayPath(target))), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check permissions on the target file", "cannot stat %s: %v", t.cleanDisplayPath(target), statErr)), nil
	}
	if info.IsDir() {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "use ls for directories", "%s is a directory", t.cleanDisplayPath(target))), nil
	}

	offset := intParam(params, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := intParam(params, "limit", remoteViewDefaultLimit)
	if limit < 1 {
		limit = remoteViewDefaultLimit
	}
	if limit > remoteViewMaxLimit {
		limit = remoteViewMaxLimit
	}

	content, linesRead, totalLines, eof, truncatedByBytes, readErr := readLineWindow(target, offset, limit, t.maxViewBytes())
	if readErr != nil {
		return remoteErrorResult(readErr), nil
	}

	var builder strings.Builder
	display := t.cleanDisplayPath(target)
	fmt.Fprintf(&builder, "=== %s (offset %d, limit %d) ===\n", display, offset, limit)
	for i, line := range content {
		fmt.Fprintf(&builder, "%d: %s\n", offset+i, line)
	}
	if len(content) == 0 {
		fmt.Fprintf(&builder, "(no lines in this window; total_lines=%d)\n", totalLines)
	}

	metadata := map[string]interface{}{
		"file_path":    display,
		"offset":       offset,
		"limit":        limit,
		"lines_read":   linesRead,
		"total_lines":  totalLines,
		"eof":          eof,
		"is_truncated": !eof,
		"remote":       true,
		"workspace":    t.workspace.Root(),
	}
	if !eof && linesRead > 0 {
		next := offset + linesRead
		metadata["suggested_next_offset"] = next
		fmt.Fprintf(&builder, "\n[more lines available: continue with offset=%d limit<=%d]\n", next, remoteViewDefaultLimit)
	}
	if truncatedByBytes {
		metadata["truncated_by_bytes"] = true
		builder.WriteString("\n[window stopped at the byte budget; narrow the window with offset/limit]\n")
	}
	if !utf8.ValidString(builder.String()) {
		metadata["binary"] = true
	}
	body, metadata := t.shapeModelOutput(ToolView, builder.String(), metadata)
	return remoteTextResult(body, metadata), nil
}

// readLineWindow streams the requested line window.
//
// Streaming means a 400 MB single-line file never forces the whole line into
// memory: lines longer than remoteViewLineCap are clamped with an explicit
// marker (mirrors the local view tool's line clamp).
func readLineWindow(path string, offset, limit, maxBytes int) (lines []string, linesRead, totalLines int, eof bool, truncatedByBytes bool, err error) {
	file, openErr := os.Open(path)
	if openErr != nil {
		return nil, 0, 0, false, false, newToolError(CodeInvalidArgument, "check file permissions", "cannot open %s: %v", filepath.Base(path), openErr)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), remoteViewLineCap*4)

	lineNo := 0
	totalBytes := 0
	for scanner.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if len(lines) >= limit {
			// The window is full; keep counting so total_lines is honest.
			continue
		}
		text := scanner.Text()
		if len(text) > remoteViewLineCap {
			text = text[:remoteViewLineCap] + fmt.Sprintf("…[line truncated: showing first %d of %d bytes]", remoteViewLineCap, len(scanner.Text()))
		}
		totalBytes += len(text) + 1
		if maxBytes > 0 && totalBytes > maxBytes && len(lines) > 0 {
			truncatedByBytes = true
			break
		}
		lines = append(lines, text)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, 0, 0, false, false, newToolError(CodeInvalidArgument, "the file may be binary; use download instead", "scan %s: %v", filepath.Base(path), scanErr)
	}

	totalLines = lineNo
	linesRead = len(lines)
	eof = offset+linesRead > totalLines
	return lines, linesRead, totalLines, eof, truncatedByBytes, nil
}

// intParam reads an integer argument that may arrive as int/float64/json.Number.
func intParam(params map[string]interface{}, key string, fallback int) int {
	raw, ok := params[key]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
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
