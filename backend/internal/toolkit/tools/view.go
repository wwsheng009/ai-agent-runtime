package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const viewDirPreviewLimit = 40

// viewBatchDefaultLimit caps per-item reads in batch mode when the caller
// omits limit. Batch calls are for scanning many files, not pulling whole
// files, and a smaller default keeps the combined output inside the model
// context window instead of being truncated by the echo layer. Explicit
// per-item limits are always honored as-is.
const viewBatchDefaultLimit = 200

// viewCompactHeadLines is the per-file head window rendered in compact batch
// mode (scan-then-read workflow). Metadata still carries lines_read /
// is_truncated / suggested_next_offset / total_lines for follow-up reads.
const viewCompactHeadLines = 10

// viewDefaultLimit is the default window size when callers omit limit.
// Large files should still be segmented with explicit offset/limit.
const viewDefaultLimit = 2000

// viewEfficiencyAdvisoryThreshold marks when a default-size leading window is
// large enough that models should prefer narrower ranges or continue via offset.
const viewEfficiencyAdvisoryThreshold = 2000

// ViewTool 文件查看工具
type ViewTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxLineSize int64
}

// NewViewTool 创建 View 工具
func NewViewTool() *ViewTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "单个文件路径。也可改用 files 在一次调用中读取多个文件。",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"description": "批量文件读取请求；适合一次获取多个独立文件或不同区间，减少 LLM 往返。批内未显式指定 limit 的项默认最多读取 200 行，避免合并输出过大被回显截断。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径。"},
						"offset":    map[string]interface{}{"type": "integer", "description": "0-based 起始行，默认 0。"},
						"limit":     map[string]interface{}{"type": "integer", "description": "读取行数，默认 2000。"},
					},
					"required":             []string{"file_path"},
					"additionalProperties": false,
				},
			},
			"compact": map[string]interface{}{
				"type":        "boolean",
				"description": "批量扫描模式（仅 files 数组生效）：每个文件只输出前 10 行 + 元数据摘要（lines_read/is_truncated/suggested_next_offset/total_lines），适合先扫描多个文件再定点细读。默认 false。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "开始读取的行号（0-based，默认为 0）。大文件请配合 limit 分段查看；不要反复全量 view 同一路径。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取行数，默认 2000；结果会标记 eof 和 is_truncated，以及 suggested_next_offset，便于按需继续。大文件优先更小 limit。",
			},
		},
		"required": []string{},
	}

	return &ViewTool{
		BaseTool: toolkit.NewBaseTool(
			"view",
			"查看一个或多个文件。用 files 批量读取独立文件或区间；单文件用 file_path。输出包含稳定行号和截断元数据。",
			"1.1.0",
			parameters,
			true,
		),
		maxLineSize: 5 * 1024 * 1024,
	}
}

func (v *ViewTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

type ViewParams struct {
	FilePath string            `json:"file_path,omitempty"`
	Files    []ViewFileRequest `json:"files,omitempty"`
	Offset   int               `json:"offset,omitempty"`
	Limit    int               `json:"limit,omitempty"`
	Compact  bool              `json:"compact,omitempty"`
}

type ViewFileRequest struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// Execute 实现 Tool 接口
func (v *ViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p ViewParams
	encoded, err := json.Marshal(params)
	if err != nil || json.Unmarshal(encoded, &p) != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("view 参数格式无效"),
		}, nil
	}
	requests := make([]ViewFileRequest, 0, len(p.Files)+1)
	if strings.TrimSpace(p.FilePath) != "" {
		requests = append(requests, ViewFileRequest{FilePath: p.FilePath, Offset: p.Offset, Limit: p.Limit})
	}
	requests = append(requests, p.Files...)
	if len(requests) == 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path 或 files 参数至少需要一个"),
		}, nil
	}
	// files 数组（即使只有 1 项）表达批量意图：应用批量默认 limit 与
	// compact 语义；仅 file_path 入参才是真正的单文件读取。
	if len(requests) == 1 && len(p.Files) == 0 {
		return v.executeSingle(ctx, requests[0])
	}
	return v.executeBatch(ctx, requests, p.Compact)
}

func (v *ViewTool) executeSingle(ctx context.Context, p ViewFileRequest) (*toolkit.ToolResult, error) {
	if strings.TrimSpace(p.FilePath) == "" {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: fmt.Errorf("file_path 参数缺失或无效")}, nil
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Limit <= 0 {
		p.Limit = viewDefaultLimit
	}
	resolvedPath := v.resolvePathWithContext(ctx, p.FilePath)

	// 检查文件是否存在
	if err := v.checkPath(runtimeexecutor.OpRead, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	fileInfo, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      v.buildPathNotFoundError(ctx, "路径不存在", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("无法访问文件: %w", err),
		}, nil
	}

	// 检查是否为目录：自动列出浅层内容，避免模型多一轮改用 ls。
	if fileInfo.IsDir() {
		listing, listErr := v.listDirectoryPreview(resolvedPath)
		if listErr != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("路径是目录，不是文件: %s；尝试列出内容失败: %w", p.FilePath, listErr),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"路径是目录，不是文件: %s\n已自动列出目录内容（depth=1，最多 %d 项）。如需递归请改用 ls 并设置 depth。\n\n%s",
				p.FilePath,
				viewDirPreviewLimit,
				listing,
			),
			Metadata: map[string]interface{}{
				"file_path":    resolvedPath,
				"is_directory": true,
				"auto_listed":  true,
				"depth":        1,
			},
		}, nil
	}

	// 读取文件
	content, readMeta, err := v.readFile(resolvedPath, p.Offset, p.Limit)
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      v.buildPathNotFoundError(ctx, "读取文件失败", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取文件失败: %w", err),
		}, nil
	}

	// 检查是否为二进制文件
	if v.isBinaryFile(content) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("文件似乎是二进制文件，不支持显示"),
		}, nil
	}

	result := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata: map[string]interface{}{
			"file_path":    resolvedPath,
			"file_size":    fileInfo.Size(),
			"lines_read":   readMeta.LinesRead,
			"offset":       p.Offset,
			"limit":        p.Limit,
			"eof":          readMeta.EOF,
			"is_truncated": readMeta.HasMore,
		},
	}
	if readMeta.TotalLinesKnown {
		result.Metadata["total_lines"] = readMeta.TotalLines
	}
	attachViewEfficiencyHints(result, p, readMeta)
	return result, nil
}

// attachViewEfficiencyHints stamps continuation metadata and a soft advisory when
// a large leading window is truncated. This is non-blocking guidance only.
func attachViewEfficiencyHints(result *toolkit.ToolResult, request ViewFileRequest, readMeta viewReadResult) {
	if result == nil || result.Metadata == nil {
		return
	}
	if readMeta.HasMore {
		nextOffset := request.Offset + readMeta.LinesRead
		if nextOffset < request.Offset {
			nextOffset = request.Offset
		}
		result.Metadata["suggested_next_offset"] = nextOffset
	}
	// Soft-warn only for default-sized leading windows that still truncated.
	// Explicit small ranges are already efficient; EOF windows need no advisory.
	if !readMeta.HasMore || request.Offset != 0 || request.Limit < viewEfficiencyAdvisoryThreshold {
		return
	}
	nextOffset, _ := result.Metadata["suggested_next_offset"].(int)
	result.Metadata["efficiency_advisory"] = "prefer_offset_limit"
	advisory := fmt.Sprintf(
		"[efficiency] File continues past this window (is_truncated=true, lines_read=%d). Prefer a smaller limit for large files, or continue with offset=%d limit<=%d. For multiple independent files, use view.files in one call instead of repeated full-file views.",
		readMeta.LinesRead,
		nextOffset,
		viewDefaultLimit,
	)
	if strings.TrimSpace(result.Content) == "" {
		result.Content = advisory
		return
	}
	result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + advisory
}

func (v *ViewTool) executeBatch(ctx context.Context, requests []ViewFileRequest, compact bool) (*toolkit.ToolResult, error) {
	sections := make([]string, 0, len(requests))
	items := make([]map[string]interface{}, 0, len(requests))
	failures := make([]string, 0)
	failedItems := make([]map[string]interface{}, 0)
	succeeded := 0
	defaulted := make([]int, 0, len(requests))
	for index, request := range requests {
		if request.Limit <= 0 {
			request.Limit = viewBatchDefaultLimit
			defaulted = append(defaulted, index)
		}
		if compact && request.Limit > viewCompactHeadLines {
			request.Limit = viewCompactHeadLines
		}
		result, err := v.executeSingle(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", request.FilePath, err))
			if row := toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, err.Error()); row != nil {
				failedItems = append(failedItems, row)
			}
			continue
		}
		if result == nil || !result.Success {
			message := "unknown error"
			if result != nil && result.Error != nil {
				message = result.Error.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", request.FilePath, message))
			if row := toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, message); row != nil {
				failedItems = append(failedItems, row)
			}
			continue
		}
		succeeded++
		section := fmt.Sprintf("===== %s =====\n%s", request.FilePath, result.Content)
		if compact {
			section += "\n" + compactViewSummary(result.Metadata)
		}
		sections = append(sections, section)
		items = append(items, result.Metadata)
	}
	if len(failures) > 0 {
		sections = append(sections, "===== errors =====\n"+strings.Join(failures, "\n"))
	}
	if succeeded == 0 {
		meta := map[string]interface{}{
			"batch":                       true,
			"request_count":               len(requests),
			"failed_count":                len(failures),
			"batch_default_limit_applied": defaulted,
			"compact":                     compact,
		}
		if len(failedItems) > 0 {
			meta[toolresult.MetadataFailedItemsKey] = failedItems
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("批量读取失败: %s", strings.Join(failures, "; ")),
			Metadata:   meta,
		}, nil
	}
	meta := map[string]interface{}{
		"batch":                       true,
		"request_count":               len(requests),
		"succeeded_count":             succeeded,
		"failed_count":                len(failures),
		"partial_failure":             len(failures) > 0,
		"items":                       items,
		"batch_default_limit_applied": defaulted,
		"compact":                     compact,
	}
	if len(failedItems) > 0 {
		meta[toolresult.MetadataFailedItemsKey] = failedItems
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    strings.Join(sections, "\n\n"),
		Metadata:   meta,
	}, nil
}

// compactViewSummary renders a single-line metadata digest for compact batch
// mode, so models can decide the next offset without re-reading the tool
// schema or guessing from truncated content.
func compactViewSummary(metadata map[string]interface{}) string {
	parts := make([]string, 0, 4)
	if v, ok := metadata["lines_read"].(int); ok {
		parts = append(parts, fmt.Sprintf("lines_read=%d", v))
	}
	if v, ok := metadata["is_truncated"].(bool); ok {
		parts = append(parts, fmt.Sprintf("is_truncated=%t", v))
	}
	if v, ok := metadata["suggested_next_offset"].(int); ok {
		parts = append(parts, fmt.Sprintf("suggested_next_offset=%d", v))
	}
	if v, ok := metadata["total_lines"].(int); ok {
		parts = append(parts, fmt.Sprintf("total_lines=%d", v))
	}
	if len(parts) == 0 {
		return "..."
	}
	return "... " + strings.Join(parts, ", ")
}

type viewReadResult struct {
	TotalLines      int
	TotalLinesKnown bool
	LinesRead       int
	HasMore         bool
	EOF             bool
}

// readFile 读取文件内容
func (v *ViewTool) readFile(filePath string, offset, limit int) (string, viewReadResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", viewReadResult{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), int(v.maxLineSize))
	var lines []string
	meta := viewReadResult{}

	// 跳过 offset 行
	skipped := 0
	for skipped < offset && scanner.Scan() {
		skipped++
		meta.TotalLines++
	}

	if err := scanner.Err(); err != nil {
		return "", meta, err
	}
	if skipped < offset {
		meta.EOF = true
		meta.TotalLinesKnown = true
		return fmt.Sprintf("Reached end of file: offset %d is beyond total lines %d.", offset, meta.TotalLines), meta, nil
	}

	// 读取 limit 行
	readCount := 0
	for readCount < limit && scanner.Scan() {
		line := scanner.Text()
		meta.TotalLines++

		// 跳过过长的行
		if utf8.RuneCountInString(line) > 2000 {
			line = string([]rune(line)[:2000]) + "..."
		}

		lines = append(lines, line)
		readCount++
	}
	meta.LinesRead = readCount

	if err := scanner.Err(); err != nil {
		return "", meta, err
	}

	if scanner.Scan() {
		meta.TotalLines++
		meta.HasMore = true
	}
	if err := scanner.Err(); err != nil {
		return "", meta, err
	}
	if !meta.HasMore {
		meta.TotalLinesKnown = true
		meta.EOF = true
	}

	if readCount == 0 {
		meta.EOF = true
		meta.TotalLinesKnown = true
		if offset == meta.TotalLines {
			return fmt.Sprintf("Reached end of file: offset %d equals total lines %d.", offset, meta.TotalLines), meta, nil
		}
		return fmt.Sprintf("Reached end of file: offset %d is beyond total lines %d.", offset, meta.TotalLines), meta, nil
	}

	return v.formatContent(lines, offset), meta, nil
}

// formatContent 格式化内容
func (v *ViewTool) formatContent(lines []string, offset int) string {
	if len(lines) == 0 {
		return ""
	}
	var output strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&output, "%d: %s", offset+index+1, line)
		if index < len(lines)-1 {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

// listDirectoryPreview returns a shallow directory listing for auto-heal when
// view is pointed at a directory instead of a file.
func (v *ViewTool) listDirectoryPreview(dirPath string) (string, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += string(filepath.Separator)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	limit := viewDirPreviewLimit
	if len(names) < limit {
		limit = len(names)
	}
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d: %s", i+1, names[i])
	}
	if len(names) > viewDirPreviewLimit {
		fmt.Fprintf(&b, "\n... 省略 %d 项（共 %d）", len(names)-viewDirPreviewLimit, len(names))
	}
	if len(names) == 0 {
		return "(空目录)", nil
	}
	return b.String(), nil
}

// isBinaryFile 检查文件是否为二进制文件
func (v *ViewTool) isBinaryFile(content string) bool {
	if len(content) == 0 {
		return false
	}

	// 检查前 500 个字节
	checkLen := 500
	if len(content) < checkLen {
		checkLen = len(content)
	}

	nullCount := 0
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			nullCount++
		}
	}

	// 如果超过一定比例是 null 字节，认为是二进制文件
	return nullCount > checkLen/20
}
